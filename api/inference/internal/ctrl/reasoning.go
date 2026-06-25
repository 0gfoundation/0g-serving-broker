package ctrl

// This file implements reasoning-parameter translation: re-expressing a client's
// portable OpenAI `reasoning_effort` as the upstream-native "thinking" control
// the target model understands (e.g. Qwen3/vLLM's chat_template_kwargs.
// enable_thinking). The translation target is picked from the model's advertised
// supportedParameters; the per-dialect mechanics live in the switch in
// applyNativeReasoning. See docs/design/reasoning-translation.md.

import (
	"encoding/json"
	"strings"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/inference/config"
)

// reasoningEffortKey is the portable OpenAI field the broker translates FROM. It
// is never a translation target — only an input.
const reasoningEffortKey = "reasoning_effort"

// isNativeReasoningParam reports whether a supportedParameters entry names an
// upstream-native thinking control the broker can translate to. This set is the
// counterpart of the switch in applyNativeReasoning: every name here must have a
// case there, and vice versa.
//
// Note the advertised name is the wire parameter the model accepts, which is not
// always the toggle key itself: a Qwen3/GLM model advertises the container
// "chat_template_kwargs" and the thinking toggle lives in its nested
// "enable_thinking" key (see applyNativeReasoning).
func isNativeReasoningParam(name string) bool {
	switch name {
	case nativeParamChatTemplateKwargs:
		return true
	default:
		return false
	}
}

// nativeParamChatTemplateKwargs is the container parameter Qwen3/GLM models on
// vLLM/SGLang advertise; the thinking toggle is its nested enable_thinking bool.
const (
	nativeParamChatTemplateKwargs = "chat_template_kwargs"
	enableThinkingKey             = "enable_thinking"
)

// reasoningIntent is the binary thinking on/off decision derived from the
// client's reasoning_effort. Unset means the client expressed no preference, so
// the broker emits nothing and the upstream default stands.
type reasoningIntent int

const (
	reasoningUnset reasoningIntent = iota
	reasoningOff
	reasoningOn
)

// normalizeReasoningEffort maps an OpenAI reasoning_effort value to a binary
// intent. Any non-empty effort other than "none"/"minimal" turns thinking on;
// "none"/"minimal" turn it off; absent leaves it unset.
func normalizeReasoningEffort(effort string) reasoningIntent {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "":
		return reasoningUnset
	case "none", "minimal":
		return reasoningOff
	default:
		return reasoningOn
	}
}

// resolveModelInfo returns the effective ModelInfo for a model: a per-model
// pricing entry's own ModelInfo wins, otherwise the service-level ModelInfo.
// Mirrors the resolution used by ModelExpiration and the /v1/models handler.
func (c *Ctrl) resolveModelInfo(model string) *config.ModelInfo {
	mi := c.Service.ModelInfo
	if c.Service.HasMultiModelPricing() {
		if mp := c.Service.GetModelPricing(model); mp != nil && mp.ModelInfo != nil {
			mi = mp.ModelInfo
		}
	}
	return mi
}

// nativeReasoningParam returns the upstream-native thinking control the given
// model advertises in supportedParameters (e.g. "enable_thinking"), or "" if it
// advertises none the broker can translate to.
func (c *Ctrl) nativeReasoningParam(model string) string {
	mi := c.resolveModelInfo(model)
	if mi == nil {
		return ""
	}
	for _, p := range mi.SupportedParameters {
		if isNativeReasoningParam(p) {
			return p
		}
	}
	return ""
}

// nativeReasoningParamSet reports whether the client already set the native
// parameter in its wire location. When true the broker leaves it untouched
// (explicit native parameter wins over a derived reasoning_effort).
func nativeReasoningParamSet(bodyMap map[string]interface{}, nativeParam string) bool {
	switch nativeParam {
	case nativeParamChatTemplateKwargs:
		kw, ok := bodyMap[nativeParamChatTemplateKwargs].(map[string]interface{})
		if !ok {
			return false
		}
		_, present := kw[enableThinkingKey]
		return present
	default:
		_, present := bodyMap[nativeParam]
		return present
	}
}

// applyNativeReasoning writes the native thinking control for `on` into bodyMap,
// in the wire location the upstream dialect expects, and reports whether it wrote
// anything. Each case owns its own value shape (bool here) — there is no shared
// value/type data. A name handled here must also be recognized by
// isNativeReasoningParam; the bool return guards TranslateReasoning against
// dropping reasoning_effort when a recognized-but-unhandled name writes nothing.
func applyNativeReasoning(bodyMap map[string]interface{}, nativeParam string, on bool) bool {
	switch nativeParam {
	case nativeParamChatTemplateKwargs:
		// Qwen3/GLM on vLLM/SGLang: bool nested under chat_template_kwargs.
		// Preserve any other kwargs the client already set.
		kw, ok := bodyMap[nativeParamChatTemplateKwargs].(map[string]interface{})
		if !ok {
			kw = map[string]interface{}{}
			bodyMap[nativeParamChatTemplateKwargs] = kw
		}
		kw[enableThinkingKey] = on
		return true
	}
	return false
}

// TranslateReasoning rewrites a chatbot request body so the client's portable
// reasoning_effort is expressed as the upstream-native thinking control the
// target model advertises (e.g. chat_template_kwargs.enable_thinking). The model
// is read from the body itself, so the call needs no extra plumbing.
//
// It returns the body unchanged when:
//   - the body is empty or not a JSON object (mirrors EnsureStreamOptions);
//   - the resolved model advertises no native reasoning parameter;
//   - the client already set that native parameter (explicit native wins);
//   - the client sent no reasoning_effort (intent Unset) — upstream default stands.
//
// When translation occurs, reasoning_effort is removed from the outgoing body:
// it has been consumed and re-expressed natively, and a Qwen/vLLM upstream that
// needs enable_thinking may reject the unknown OpenAI field.
func (c *Ctrl) TranslateReasoning(body []byte) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}

	var bodyMap map[string]interface{}
	if err := json.Unmarshal(body, &bodyMap); err != nil {
		// Non-JSON request: leave as-is, consistent with the other body rewriters.
		return body, nil
	}

	model, _ := bodyMap["model"].(string)
	nativeParam := c.nativeReasoningParam(model)
	if nativeParam == "" {
		return body, nil
	}

	// Explicit native parameter wins: the client addressed the upstream directly,
	// so forward it untouched and do not derive from reasoning_effort.
	if nativeReasoningParamSet(bodyMap, nativeParam) {
		return body, nil
	}

	effort, _ := bodyMap[reasoningEffortKey].(string)
	intent := normalizeReasoningEffort(effort)
	if intent == reasoningUnset {
		return body, nil
	}

	if !applyNativeReasoning(bodyMap, nativeParam, intent == reasoningOn) {
		// Recognized but unhandled (should not happen while detection and the
		// apply switch stay in sync): write nothing and leave the body untouched
		// rather than stripping reasoning_effort without a replacement.
		return body, nil
	}
	// reasoning_effort has been re-expressed natively; drop it so a vLLM-style
	// upstream that only understands the native control doesn't reject it.
	delete(bodyMap, reasoningEffortKey)

	modified, err := json.Marshal(bodyMap)
	if err != nil {
		return body, errors.Wrap(err, "failed to marshal reasoning-translated body")
	}
	return modified, nil
}
