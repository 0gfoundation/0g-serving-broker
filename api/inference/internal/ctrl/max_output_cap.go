package ctrl

// This file clamps a chatbot request's output-token cap to the resolved model's
// advertised modelInfo.maxCompletionTokens, when the service opts in with
// enforceMaxCompletionTokens.
//
// Why it is not injectBodyFields: that map is server-config-wins by definition
// (config.go), so configuring max_tokens there would overwrite a client's
// smaller value and bill them for output they did not request. The rule here is
// take-the-minimum — lower when higher, set when missing, never raise.
//
// Why it exists at all: an OpenAI-compatible engine given no output cap
// generates until the context window stops it. On a self-hosted engine each
// in-flight request holds KV for as long as it keeps producing, and KV is the
// resource the box runs out of first. An unbounded reasoning request can hold a
// slot for tens of minutes.

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/inference/config"
)

// conservativeBytesPerToken converts request-body bytes to a deliberately HIGH
// prompt-token estimate, used only to decide how much output can still fit in
// the context window.
//
// Three bytes per token is roughly CJK-shaped; English/JSON runs nearer four.
// Over-estimating is the safe direction here: it shrinks the injected cap, and
// the failure mode of an injected cap that is too LARGE is an upstream 400
// (input + max_tokens > context length), which turns a request that would have
// worked into an error. A cap that is slightly too small only shortens a reply
// the client did not ask to bound in the first place.
const conservativeBytesPerToken = 3

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
// A field is read as a number whether it arrives as a JSON number or a quoted
// string, because the engines behind this broker coerce the latter. Anything
// that cannot be read as a number at all, and anything zero or negative, is not
// a bound this pass can honour, so it is replaced or dropped rather than
// forwarded — leaving it would make the clamp bypassable. A JSON `null` is
// treated as absent, because that is what it means: no limit.
//
// Runs BEFORE TranslateMaxTokensFor so the field name written when the client
// sent none (max_tokens, the name every OpenAI-compatible upstream still
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
	// hasCap tracks whether the client expressed ANY usable output bound. A null
	// does not count, and neither does a value we could not read.
	hasCap := false
	for _, key := range []string{maxTokensKey, maxCompletionTokensKey} {
		raw, ok := bodyMap[key]
		if !ok {
			continue
		}
		if raw == nil {
			// An explicit null means "no limit" — the case this exists to close.
			// Drop it so the injection below can supply a bound, unless the sibling
			// spelling carries a real one.
			delete(bodyMap, key)
			changed = true
			continue
		}
		v, ok := readOutputCapValue(raw)
		if !ok {
			// Not readable as a number at all ("lots", 1e400, an object). Such a
			// value cannot be compared, so it cannot be honoured — and leaving it
			// would let the clamp be bypassed by writing something unparseable.
			// Replace it with the limit: no legitimate client sends a cap it cannot
			// express as a number, so nothing correct is lost.
			bodyMap[key] = limit
			hasCap = true
			changed = true
			continue
		}
		if v <= 0 {
			// Zero or negative is not a usable bound. Drop it and let the injection
			// below supply a real one, rather than treating "max_tokens: -1" —
			// which several clients use to mean "unlimited" — as a cap.
			delete(bodyMap, key)
			changed = true
			continue
		}
		hasCap = true
		if v > float64(limit) {
			bodyMap[key] = limit
			changed = true
		}
	}

	if !hasCap {
		// No usable cap: the unbounded-generation case. Inject what still fits in
		// the context window rather than the advertised maximum, which for a model
		// whose maxCompletionTokens is a large fraction of its context length would
		// turn a long prompt that used to work into an upstream 400.
		if injected := c.injectableOutputCap(mi, limit, len(body)); injected > 0 {
			bodyMap[maxTokensKey] = injected
			changed = true
		}
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

// injectableOutputCap is the cap to write into a request that carried none: the
// advertised maximum, reduced to whatever the context window still has room for
// after a conservative estimate of the prompt.
//
// Returns 0 when there is no room, in which case nothing is injected and the
// engine decides — it computes the true remaining context from the tokenized
// prompt, which is strictly better than a guess made from byte counts.
//
// With no advertised contextLength there is nothing to reason about, so the
// advertised maximum is used as-is.
func (c *Ctrl) injectableOutputCap(mi *config.ModelInfo, limit int64, bodyLen int) int64 {
	if mi.ContextLength <= 0 {
		return limit
	}
	promptEstimate := int64(bodyLen) / conservativeBytesPerToken
	room := int64(mi.ContextLength) - promptEstimate
	if room <= 0 {
		return 0
	}
	if room < limit {
		return room
	}
	return limit
}

// readOutputCapValue reads a cap field as a float64 for comparison.
//
// Two decodings, for two different reasons.
//
// json.Number.Int64 is strconv.ParseInt, which rejects every non-integer
// literal — 1e3, 1000.0 and 100.5 all fail, and 1000.0 is what any client
// computing its cap in floating point sends. Treating those failures as "too
// big" would RAISE a client's 1000 to the advertised maximum, the exact
// opposite of this pass's contract. float64 compares all of them correctly, and
// its 2^53 exact-integer range is far above any real token cap.
//
// A JSON string is read too, because the engines behind this broker accept one.
// sglang and vLLM validate with pydantic in its default lax mode, which
// silently coerces "1000000" to an int — so a quoted cap is a real request that
// really is honoured upstream. Skipping it here would leave the clamp bypassable
// by one character.
func readOutputCapValue(raw interface{}) (float64, bool) {
	switch v := raw.(type) {
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return float64(i), true
		}
		f, err := v.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}
