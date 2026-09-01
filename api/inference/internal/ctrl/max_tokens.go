package ctrl

// This file implements output-token-cap translation: re-expressing a client's
// portable OpenAI output-token limit (max_tokens / max_completion_tokens) under
// whichever of the two field names the target model actually accepts. The
// accepted field is picked from the model's advertised supportedParameters; the
// motivating case is DeepSeek-on-vLLM, which accepts only max_tokens and rejects
// the newer max_completion_tokens an OpenAI-compatible client sends.

import (
	"bytes"
	"encoding/json"

	"github.com/0glabs/0g-serving-broker/common/errors"
)

// Output-token-cap field names. Both express the same thing — a cap on generated
// output tokens — but belong to different generations of the OpenAI
// chat-completions schema: max_tokens is the original (now deprecated) field,
// max_completion_tokens its replacement. Some upstreams accept only one.
const (
	maxTokensKey           = "max_tokens"
	maxCompletionTokensKey = "max_completion_tokens"
)

// TranslateMaxTokens rewrites a chatbot request body so the client's output-token
// cap is expressed under the field name the target model accepts. resolvedModel is
// the public/canonical id the broker resolved the request to (CtxKeyResolvedModel)
// — NOT the body's "model" field, which ValidateModelAllowlist may have already
// rewritten to the upstream id; the accepted field is detected from THAT model's
// advertised supportedParameters (an empty resolvedModel selects the service-level
// ModelInfo, matching Inject/StripBodyFields):
//
//   - a model that advertises max_tokens but NOT max_completion_tokens has a
//     client-sent max_completion_tokens renamed to max_tokens (the DeepSeek-on-
//     vLLM case: the upstream rejects the unknown max_completion_tokens);
//   - the mirror also holds — max_tokens is renamed to max_completion_tokens for a
//     model that advertises only the newer field.
//
// It returns the body unchanged when:
//   - the body is empty or not a JSON object (mirrors EnsureStreamOptions);
//   - the resolved model has no ModelInfo (no supportedParameters to detect from);
//   - the model advertises both fields, or neither (no single target to pick);
//   - the source field that would be translated FROM is absent from the body.
//
// The two fields share identical semantics (a max-output-tokens cap), so the
// value is moved verbatim — billing is unaffected and the cap is preserved. When
// the destination field is ALSO present (a client that sent both), the client's
// destination value is kept and only the source is removed (it would otherwise be
// rejected upstream); the dropped source is logged (name only, never the value).
//
// Decoding uses json.Number so large integer fields elsewhere in the body (e.g. a
// seed) survive the round-trip unmangled, matching Inject/StripBodyFields.
func (c *Ctrl) TranslateMaxTokens(body []byte, resolvedModel string) ([]byte, error) {
	return c.TranslateMaxTokensFor(body, resolvedModel, "")
}

// TranslateMaxTokensFor is TranslateMaxTokens keyed to the router-named upstream
// identity (config.UpstreamIdentityHeader), so a multi-upstream model's
// per-entry supportedParameters drive the translation for the selected upstream
// rather than being taken from the cheapest entry. identity="" is identical to
// the bare call.
func (c *Ctrl) TranslateMaxTokensFor(body []byte, resolvedModel, identity string) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}

	mi := c.Service.EffectiveModelInfoFor(resolvedModel, identity)
	if mi == nil {
		return body, nil
	}
	supportsMax := containsParam(mi.SupportedParameters, maxTokensKey)
	supportsCompletion := containsParam(mi.SupportedParameters, maxCompletionTokensKey)
	if supportsMax == supportsCompletion {
		// Advertises both (accepts either) or neither (cannot tell which the upstream
		// wants): no single target to translate to, so skip parsing entirely and
		// forward unchanged.
		return body, nil
	}

	var bodyMap map[string]interface{}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	// A literal JSON `null` decodes to a nil map without error; treat it the same
	// as a non-object body and forward unchanged.
	if err := dec.Decode(&bodyMap); err != nil || bodyMap == nil {
		return body, nil
	}

	// Exactly one of the two is advertised (the equal case returned above), so the
	// target is unambiguous: rename FROM the field the upstream does not accept TO
	// the one it does.
	from, to := maxCompletionTokensKey, maxTokensKey
	if supportsCompletion {
		from, to = maxTokensKey, maxCompletionTokensKey
	}

	v, ok := bodyMap[from]
	if !ok {
		// Client did not send the field that would need translating.
		return body, nil
	}
	delete(bodyMap, from)
	if _, exists := bodyMap[to]; exists {
		// Client sent BOTH fields. The destination is the form the upstream accepts,
		// so keep the client's explicit value there and drop only the source (which
		// the upstream would reject). Log the dropped source so a surprising cap is
		// debuggable; never log the value.
		c.logger.Infof("translateMaxTokens: request for model %q sent both %q and %q; forwarding %q and dropping unsupported %q", resolvedModel, from, to, to, from)
	} else {
		bodyMap[to] = v
	}

	modified, err := json.Marshal(bodyMap)
	if err != nil {
		return body, errors.Wrap(err, "failed to marshal max-tokens-translated body")
	}
	return modified, nil
}

// containsParam reports whether params contains name. Matching is exact and
// case-sensitive: supportedParameters entries are upstream wire field names.
func containsParam(params []string, name string) bool {
	for _, p := range params {
		if p == name {
			return true
		}
	}
	return false
}
