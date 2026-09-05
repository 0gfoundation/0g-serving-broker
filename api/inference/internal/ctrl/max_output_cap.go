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
	"math"
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
// Over-estimating is the safe direction for the upper bound: an injected cap
// that is too LARGE means an upstream 400 (input + max_tokens > context
// length), turning a request that would have worked into an error.
//
// It is used only as a yes/no test — does the advertised cap still fit — never
// to compute a smaller cap from. Deriving one would compound the error as the
// prompt approaches the window (at a real prompt of 70% of context the estimate
// leaves a twentieth of the room that actually remains) and produce caps small
// enough to be swallowed whole by a reasoning model's thinking tokens. See
// injectableOutputCap.
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
// The spelling written when the client sent no cap is chosen from the model's
// own supportedParameters, not assumed — an upstream that accepts only
// max_completion_tokens (OpenAI's reasoning models answer max_tokens with a 400)
// must not be handed the other name. Config load requires that declaration
// whenever this flag is on, so the choice is always informed.
//
// Runs BEFORE TranslateMaxTokensFor, which then normalizes whatever the CLIENT
// sent; the injected spelling is already correct by construction. Anthropic's
// /v1/messages spells it max_tokens too, so the same clamp covers that surface.
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
			// Not readable as a number at all ("lots", 1e400, an object). It cannot
			// be compared, so it cannot be honoured — and leaving it in place would
			// let the clamp be bypassed by writing something unparseable.
			//
			// Dropped rather than overwritten with the limit. Overwriting looks
			// equivalent but is not: TranslateMaxTokensFor keeps the destination
			// spelling and discards the source when both are present, so writing
			// the limit into one spelling can silently replace a perfectly good
			// smaller value the client put in the other — raising the cap, which is
			// the one thing this pass promises never to do. Dropping leaves the
			// sibling alone, and if there is no sibling the injection below still
			// supplies a bound.
			delete(bodyMap, key)
			changed = true
			continue
		}
		if v < 0 {
			// Negative is how several clients spell "unlimited". Drop it and let the
			// injection below supply a real bound; going from unlimited to the cap
			// is a reduction, so take-the-minimum holds.
			delete(bodyMap, key)
			changed = true
			continue
		}
		if v == 0 {
			// NOT the same as negative. Zero is what an unset int field serializes
			// to in Go and Java when the struct tag has no omitempty, and it is an
			// explicit "generate nothing" everywhere else. Replacing it with the
			// limit would be this pass's single largest possible raise. Forward it
			// and let the upstream answer for it (vLLM: "max_tokens must be at
			// least 1").
			hasCap = true
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
			bodyMap[injectionKey(mi)] = injected
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

// injectableOutputCap is the cap to write into a request that carried none.
// It is all or nothing: the advertised maximum when a conservative reading of
// the prompt says it still fits in the context window, and zero — inject
// nothing — when it does not.
//
// The tempting alternative, injecting whatever room the estimate says is left,
// is worse than either. The estimate deliberately runs a third high so it never
// overshoots the window, and that error compounds as the prompt grows: at a
// real prompt of 70% of context it reports about a twentieth of the room that
// actually remains. On a reasoning model, where max_tokens covers the thinking
// tokens, a cap that small is consumed before any answer starts — the client
// gets empty content with finish_reason "length" and pays for the reasoning.
// And it would sit immediately beside a working outcome, since a body a few
// kilobytes larger crosses into "no room", is forwarded untouched, and
// succeeds. Two adjacent inputs, opposite results, with the out-of-range one
// better.
//
// Injecting nothing hands the decision to the engine, which computes the true
// remaining context from the tokenized prompt — strictly better than a guess
// made from byte counts. What is given up is bounding these particular
// requests, and they are the ones least in need of it: a prompt that nearly
// fills the window has little room left to generate into, so the window itself
// is already the bound.
//
// With no advertised contextLength there is nothing to reason about, so the
// advertised maximum is used as-is.
func (c *Ctrl) injectableOutputCap(mi *config.ModelInfo, limit int64, bodyLen int) int64 {
	if mi.ContextLength <= 0 {
		return limit
	}
	if int64(mi.ContextLength)-int64(bodyLen)/conservativeBytesPerToken < limit {
		return 0
	}
	return limit
}

// injectionKey picks the spelling to write a cap under: the newer
// max_completion_tokens only when the model advertises that and not the older
// max_tokens. Every other case takes max_tokens, which is what self-hosted
// engines and the Anthropic surface accept.
func injectionKey(mi *config.ModelInfo) string {
	if containsParam(mi.SupportedParameters, maxCompletionTokensKey) &&
		!containsParam(mi.SupportedParameters, maxTokensKey) {
		return maxCompletionTokensKey
	}
	return maxTokensKey
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
		return finite(f)
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0, false
		}
		return finite(f)
	default:
		return 0, false
	}
}

// finite rejects NaN and the infinities.
//
// NaN is the one value that would slip past every guard at once: strconv
// accepts the literal "NaN", and every comparison against NaN is false, so it
// would be neither clamped (v > limit is false) nor dropped (v <= 0 is false)
// while still counting as a cap and suppressing the injection. The result would
// be a request forwarded with no enforceable bound — precisely what this pass
// exists to prevent. The infinities are unreadable as a token count for the
// same reason and take the same exit.
func finite(f float64) (float64, bool) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}
