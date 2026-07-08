package ctrl

// This file implements reasoning-parameter translation: re-expressing a client's
// portable OpenAI `reasoning_effort` as the upstream-native "thinking" control
// the target model understands (e.g. Qwen3/vLLM's chat_template_kwargs.
// enable_thinking). The translation target is picked from the model's advertised
// supportedParameters; the per-dialect mechanics live in the switch in
// applyNativeReasoning. See docs/design/reasoning-translation.md.

import (
	"bytes"
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
// always the toggle key itself: a Qwen3/GLM model on vLLM advertises the
// container "chat_template_kwargs" and the thinking toggle lives in its nested
// "enable_thinking" key (see applyNativeReasoning).
//
// "preserve_thinking" (advertised by DeepSeek/Qwen on DashScope) is deliberately
// NOT recognized: it is not an on/off toggle but a multi-turn flag that keeps
// prior turns' reasoning in context. The DashScope thinking toggle is a top-level
// "enable_thinking" instead.
func isNativeReasoningParam(name string) bool {
	switch name {
	case nativeParamChatTemplateKwargs, nativeParamEnableThinking, nativeParamThinking, nativeParamReasoning:
		return true
	default:
		return false
	}
}

// Native thinking-control parameter names the broker can translate reasoning_effort into.
const (
	// nativeParamChatTemplateKwargs is the container Qwen3/GLM models on
	// vLLM/SGLang advertise; the thinking toggle is its nested enable_thinking bool.
	nativeParamChatTemplateKwargs = "chat_template_kwargs"
	// nativeParamEnableThinking is the top-level bool DashScope (Aliyun) accepts
	// as an extra_body key — distinct from the vLLM nested form above.
	nativeParamEnableThinking = "enable_thinking"
	// nativeParamThinking is MiniMax's object control: {"type": "enabled"|"disabled"}.
	nativeParamThinking = "thinking"
	// nativeParamReasoning is OpenRouter's unified object control:
	// {"enabled": bool}. OpenRouter also accepts an "effort" (low|medium|high) or
	// "max_tokens" sub-field for finer-grained control, but the broker's intent is
	// only ever binary (see reasoningIntent), so "enabled" is the only sub-field
	// the broker writes.
	nativeParamReasoning = "reasoning"

	// enableThinkingKey is the toggle key NESTED under chat_template_kwargs (the
	// vLLM/SGLang dialect). It coincidentally shares the literal "enable_thinking"
	// with nativeParamEnableThinking above, but the two play different roles — this
	// is a key inside a container, that is a top-level advertised param / wire key —
	// so they are deliberately kept as separate constants.
	enableThinkingKey = "enable_thinking"
)

// requiresAnthropicBudgetTokens reports whether formats declare the genuine
// Anthropic /v1/messages surface (config.APIFormatAnthropic). On that surface
// the native "thinking" control is Anthropic's own — {"type": "enabled",
// "budget_tokens": N} — where budget_tokens is mandatory (min 1024, must be
// less than the request's max_tokens) and the broker has no basis to compute
// it. This is a different wire shape from MiniMax/Zhipu's "thinking" on the
// OpenAI surface, a plain {"type": "enabled"|"disabled"} toggle with no such
// requirement: same advertised name, incompatible dialects (see
// nativeParamThinking). "thinking" is therefore excluded as a translation
// target — and left off the advertised-parameters addition below — for any
// model that declares the Anthropic surface.
func requiresAnthropicBudgetTokens(formats []string) bool {
	for _, f := range formats {
		if strings.EqualFold(f, config.APIFormatAnthropic) {
			return true
		}
	}
	return false
}

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

// AdvertisedSupportedParameters returns supportedParameters as it should appear in
// the GET /v1/models response. When the model advertises a native thinking toggle
// the broker can translate into (chat_template_kwargs / enable_thinking / thinking /
// reasoning) but not the portable "reasoning_effort" itself, reasoning_effort is
// appended so OpenAI-schema clients discover they can use it — the broker accepts
// it via translation (see TranslateReasoning). formats is the model's
// SupportedFormats: a "thinking" advertisement on the Anthropic surface is not
// counted (see requiresAnthropicBudgetTokens) since the broker cannot translate it.
//
// The input slice is never mutated, and the config value used by nativeReasoningParam
// for detection is unaffected: only the advertised view gains the field, keeping
// "broker can translate reasoning_effort" ⇔ "reasoning_effort is advertised" true.
func AdvertisedSupportedParameters(params []string, formats []string) []string {
	anthropicThinking := requiresAnthropicBudgetTokens(formats)
	hasNative, hasEffort := false, false
	for _, p := range params {
		if isNativeReasoningParam(p) && !(p == nativeParamThinking && anthropicThinking) {
			hasNative = true
		}
		if p == reasoningEffortKey {
			hasEffort = true
		}
	}
	if !hasNative || hasEffort {
		return params
	}
	out := make([]string, len(params), len(params)+1)
	copy(out, params)
	return append(out, reasoningEffortKey)
}

// nativeReasoningParam returns the upstream-native thinking control the given
// model advertises in supportedParameters (e.g. "enable_thinking"), or "" if it
// advertises none the broker can translate to. A "thinking" advertisement on the
// Anthropic surface is skipped — see requiresAnthropicBudgetTokens.
func (c *Ctrl) nativeReasoningParam(model string) string {
	mi := c.Service.EffectiveModelInfo(model)
	if mi == nil {
		return ""
	}
	anthropicThinking := requiresAnthropicBudgetTokens(mi.SupportedFormats)
	for _, p := range mi.SupportedParameters {
		if p == nativeParamThinking && anthropicThinking {
			continue
		}
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
	case nativeParamReasoning:
		// OpenRouter: the client may set unrelated sub-fields (e.g. max_tokens)
		// without addressing the toggle itself; only "enabled" makes the native
		// control explicit, mirroring chat_template_kwargs's nested-key check.
		r, ok := bodyMap[nativeParamReasoning].(map[string]interface{})
		if !ok {
			return false
		}
		_, present := r["enabled"]
		return present
	default:
		// Top-level params (enable_thinking, thinking): present iff the key exists.
		_, present := bodyMap[nativeParam]
		return present
	}
}

// applyNativeReasoning writes the native thinking control for `on` into bodyMap,
// in the wire location the upstream dialect expects, and reports whether it wrote
// anything. Each case owns its own value shape (bool, or MiniMax's object) — there
// is no shared value/type data. A name handled here must also be recognized by
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
	case nativeParamEnableThinking:
		// DashScope (Aliyun): top-level bool.
		bodyMap[nativeParamEnableThinking] = on
		return true
	case nativeParamThinking:
		// MiniMax: object {"type": "enabled"|"disabled"}.
		thinkingType := "disabled"
		if on {
			thinkingType = "enabled"
		}
		bodyMap[nativeParamThinking] = map[string]interface{}{"type": thinkingType}
		return true
	case nativeParamReasoning:
		// OpenRouter: object {"enabled": bool}. Preserve any other sub-fields the
		// client already set (e.g. max_tokens), mirroring chat_template_kwargs.
		r, ok := bodyMap[nativeParamReasoning].(map[string]interface{})
		if !ok {
			r = map[string]interface{}{}
			bodyMap[nativeParamReasoning] = r
		}
		r["enabled"] = on
		return true
	}
	return false
}

// TranslateReasoning rewrites a chatbot request body so the client's portable
// reasoning_effort is expressed as the upstream-native thinking control the
// target model advertises (e.g. chat_template_kwargs.enable_thinking). The model
// is passed in as the resolved canonical id (from CtxKeyResolvedModel), NOT read
// from the body: ValidateModelAllowlist may have already rewritten the body's
// "model" to the upstream id, while per-model supportedParameters are keyed by
// canonical id. An empty model resolves to the service-level ModelInfo.
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
func (c *Ctrl) TranslateReasoning(body []byte, model string) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}

	// Eligibility check first — it needs only the model id, not the body. Most models
	// in a deployment advertise no native thinking toggle, so bail out before the
	// decode/allocate to keep this a cheap no-op on the bulk of chatbot traffic
	// (mirrors TranslateMaxTokens's supportsMax==supportsCompletion early return).
	nativeParam := c.nativeReasoningParam(model)
	if nativeParam == "" {
		return body, nil
	}

	// UseNumber so large integer fields (e.g. a client-supplied `seed` above 2^53)
	// survive the decode→encode round-trip intact instead of being silently mangled
	// through float64. Mirrors TranslateMaxTokens / Strip/InjectBodyFields.
	var bodyMap map[string]interface{}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	// A literal JSON `null` decodes to a nil map without error; treat it like a
	// non-object body and forward unchanged.
	if err := dec.Decode(&bodyMap); err != nil || bodyMap == nil {
		// Non-JSON request: leave as-is, consistent with the other body rewriters.
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
