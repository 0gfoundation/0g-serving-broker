package ctrl

// This file clamps a chatbot request's output-token cap to the resolved model's
// advertised modelInfo.maxCompletionTokens, when the service opts in with
// enforceMaxCompletionTokens.
//
// Why it is not injectBodyFields: that map is server-config-wins by definition
// (config.go), so configuring max_tokens there would overwrite a client's
// smaller value and bill them for output they did not request. The rule here is
// take-the-minimum — set when missing, lower when higher, never raise.
//
// Why it exists at all: an OpenAI-compatible engine given no output cap
// generates until the context window stops it. On a self-hosted engine each
// in-flight request holds KV for as long as it keeps producing, and KV is the
// resource the box runs out of first. An unbounded reasoning request can hold a
// slot for tens of minutes.

import (
	"bytes"
	"encoding/json"

	"github.com/0glabs/0g-serving-broker/common/errors"
)

// CapMaxOutputTokens lowers the request's output-token cap to the resolved
// model's maxCompletionTokens. resolvedModel is the public/canonical id
// (CtxKeyResolvedModel), not the body's "model" field; identity names the
// selected upstream for a multi-upstream model. Both are resolved exactly as
// TranslateMaxTokensFor resolves them.
//
// It returns the body unchanged when:
//   - the service has not set enforceMaxCompletionTokens;
//   - the body is empty or not a JSON object;
//   - the resolved model has no ModelInfo, or a non-positive maxCompletionTokens;
//   - every cap field already present is at or below the limit.
//
// A field holding a non-numeric value other than null (a string, say) is left
// alone: that is a malformed request and the upstream's own validation should be
// the one to say so, with its own error message.
//
// Runs BEFORE TranslateMaxTokensFor so the field name this writes when the
// client sent none (max_tokens, the name every OpenAI-compatible upstream still
// accepts) is then renamed by that pass if the model advertises only the newer
// max_completion_tokens. Anthropic's /v1/messages spells it max_tokens too, so
// the same clamp covers that surface.
func (c *Ctrl) CapMaxOutputTokens(body []byte, resolvedModel, identity string) ([]byte, error) {
	if !c.Service.EnforceMaxCompletionTokens || len(body) == 0 {
		return body, nil
	}

	mi := c.Service.EffectiveModelInfoFor(resolvedModel, identity)
	if mi == nil || mi.MaxCompletionTokens <= 0 {
		return body, nil
	}
	limit := int64(mi.MaxCompletionTokens)

	var bodyMap map[string]interface{}
	dec := json.NewDecoder(bytes.NewReader(body))
	// json.Number for the same reason the sibling passes use it: a large integer
	// elsewhere in the body (a seed, say) must survive the round-trip unmangled.
	dec.UseNumber()
	if err := dec.Decode(&bodyMap); err != nil || bodyMap == nil {
		return body, nil
	}

	changed := false
	present := false
	for _, key := range []string{maxTokensKey, maxCompletionTokensKey} {
		raw, ok := bodyMap[key]
		if !ok {
			continue
		}
		// An explicit null is the client saying "no limit" — the case this exists
		// to close, so it counts as absent and gets the cap written in.
		if raw == nil {
			bodyMap[key] = limit
			present, changed = true, true
			continue
		}
		num, isNum := raw.(json.Number)
		if !isNum {
			// Malformed; leave it for the upstream to reject.
			present = true
			continue
		}
		present = true
		v, err := num.Int64()
		if err != nil {
			// Fractional or out of int64 range. Out of range means absurdly large,
			// so clamping is right; a fraction is malformed but clamping it to a
			// valid cap is no worse than forwarding it.
			bodyMap[key] = limit
			changed = true
			continue
		}
		if v > limit {
			bodyMap[key] = limit
			changed = true
		}
	}

	if !present {
		// No cap at all: the unbounded-generation case.
		bodyMap[maxTokensKey] = limit
		changed = true
	}
	if !changed {
		return body, nil
	}

	modified, err := json.Marshal(bodyMap)
	if err != nil {
		return body, errors.Wrap(err, "failed to marshal output-capped body")
	}
	return modified, nil
}
