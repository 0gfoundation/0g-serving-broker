package ctrl

// This file implements request profiles (config Service.RequestProfile): what a
// known upstream engine's request schema accepts for an optional field, so a
// value a newer client sends and the engine does not list yet is dropped instead
// of 400ing the whole request. It is the per-engine half of the OpenRouter rule
// that providers "ignore unknown parameters"; the router's capability routing is
// the other half.
//
// A rule exists only for a field whose absence leaves the upstream's default as a
// valid reading of the request (display style, effort depth). Structural fields —
// roles, content-block types, thinking.type, tool_choice — have no rule: dropping
// them would change the answer, so the engine's own 400 stands. Unknown field
// NAMES need no rule either: SGLang's request models ignore extra keys.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/inference/config"
)

// profileRule is one optional enum field: the dotted path in the request body and
// the string values the engine accepts there. numeric marks a field that also takes
// a number, which the engine's lax parsing accepts as a numeric string too.
type profileRule struct {
	path     string
	accepted []string
	numeric  bool
}

// maxLoggedValueLen caps a dropped value in the log line: the value is chosen by
// the client and otherwise bounded only by the request size.
const maxLoggedValueLen = 64

// requestProfiles maps profile -> API surface -> rules. The SGLang sets are copied
// from python/sglang/srt/entrypoints/{anthropic,openai}/protocol.py at v0.5.18
// (the version 19-glm5.3 serves); re-check them when the deployed SGLang moves.
var requestProfiles = map[string]map[string][]profileRule{
	config.RequestProfileSGLang: {
		config.APIFormatAnthropic: {
			// AnthropicThinkingParam.display. Claude Code also sends "updates" and
			// "highlights"; SGLang only logs on display, so dropping it loses nothing.
			{path: "thinking.display", accepted: []string{"summarized", "omitted"}},
			// AnthropicOutputConfig.effort.
			{path: "output_config.effort", accepted: []string{"minimal", "low", "medium", "high", "xhigh", "max"}},
		},
		config.APIFormatOpenAI: {
			// ReasoningEffortTier, or a float in [0, 0.99] that pydantic's lax union
			// also takes as a string ("0.5"). Any number, string or not, passes
			// through to be judged by SGLang, out-of-range ones included.
			{path: "reasoning_effort", accepted: []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}, numeric: true},
		},
	},
}

// ApplyRequestProfile drops, from a chat request arriving on surface, every field
// the profile has a rule for whose value is a string the engine does not accept.
// With enforce=false nothing is dropped and the would-be drops are only logged.
// The body is returned unchanged when the profile is empty or unknown, the surface
// has no rules, the body is not a JSON object, or nothing matched.
func (c *Ctrl) ApplyRequestProfile(body []byte, profile, surface, model string, enforce bool) ([]byte, error) {
	rules := requestProfiles[profile][surface]
	if len(rules) == 0 || len(body) == 0 {
		return body, nil
	}

	var bodyMap map[string]interface{}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&bodyMap); err != nil || bodyMap == nil {
		return body, nil
	}

	var dropped []string
	for _, r := range rules {
		parent, leaf := lookupParent(bodyMap, r.path)
		if parent == nil {
			continue
		}
		v, ok := parent[leaf].(string)
		if !ok || containsString(r.accepted, v) {
			continue
		}
		if r.numeric {
			// Pydantic also takes underscores Go's parser does not ("0._5").
			if _, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil || strings.ContainsRune(v, '_') {
				continue
			}
		}
		if len(v) > maxLoggedValueLen {
			v = v[:maxLoggedValueLen] + "..."
		}
		dropped = append(dropped, fmt.Sprintf("%s=%q", r.path, v))
		if enforce {
			delete(parent, leaf)
		}
	}
	if len(dropped) == 0 {
		return body, nil
	}
	if !enforce {
		c.logger.Infof("request profile %q would drop %v for model %q (log-only)", profile, dropped, model)
		return body, nil
	}
	c.logger.Infof("request profile %q dropped %v for model %q", profile, dropped, model)

	modified, err := json.Marshal(bodyMap)
	if err != nil {
		return body, errors.Wrap(err, "failed to marshal request-profile body")
	}
	return modified, nil
}

// lookupParent walks a dotted path and returns the object holding its last
// segment plus that segment's name, or nil when an intermediate value is absent
// or not an object.
func lookupParent(m map[string]interface{}, path string) (map[string]interface{}, string) {
	parts := strings.Split(path, ".")
	for _, p := range parts[:len(parts)-1] {
		next, ok := m[p].(map[string]interface{})
		if !ok {
			return nil, ""
		}
		m = next
	}
	return m, parts[len(parts)-1]
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
