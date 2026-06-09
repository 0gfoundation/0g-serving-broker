package ctrl

// This file holds the client-facing response sanitization layer: stripping
// upstream identity/cost/fingerprint fields (#184), dropping the synthesized
// openrouter.reasoning redacted_thinking leak (router #373), and clamping
// inconsistent reasoning/thinking token details (router #374) — plus the body
// decompression and per-SSE-line helpers these rely on. Billing and TEE signing
// are unaffected (billing reads the raw upstream bytes; signing attests the
// sanitized client copy — see sanitizeResponseBody). Tests live in sanitize_test.go.

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/gin-gonic/gin"
)

// leakKeysAlways are response fields that disclose the upstream aggregator's
// identity, wholesale cost, or schema, and are removed wherever they appear in
// the response tree (#184). They are provider-agnostic: a vLLM/OpenAI upstream
// omits them so stripping is a no-op, while aggregating upstreams (e.g.
// OpenRouter) populate them.
//
//   - provider, is_byok                       → aggregator identity
//   - cost, cost_details                       → upstream wholesale cost / margin
//   - native_finish_reason, reasoning_details  → aggregator schema fingerprints
var leakKeysAlways = map[string]bool{
	"provider":             true,
	"is_byok":              true,
	"cost":                 true,
	"cost_details":         true,
	"native_finish_reason": true,
	"reasoning_details":    true,
}

// leakKeysIfZero are standard OpenAI token-detail sub-fields that an aggregator
// emits pre-normalised to zero; their mere presence fingerprints the upstream
// normaliser, so they are removed only when zero (a non-zero value is real
// usage and is kept). cached_tokens and reasoning_tokens are intentionally NOT
// listed — they carry real information and must survive.
var leakKeysIfZero = map[string]bool{
	"audio_tokens":       true,
	"video_tokens":       true,
	"image_tokens":       true,
	"cache_write_tokens": true,
}

// stripLeakKeysContainers are object keys whose values are opaque user/tool
// payloads (assistant content, tool-call arguments). stripLeakKeys never
// descends into them: a structured payload could legitimately contain a field
// literally named "cost"/"provider", and stripping it would corrupt the user's
// data. Leak fields live in response metadata, not inside these.
var stripLeakKeysContainers = map[string]bool{
	"content":   true,
	"arguments": true,
}

// stripLeakKeys recursively removes leak fields from a decoded JSON value
// (object or array), descending into nested objects/arrays so fields buried in
// choices[].message / usage.*_tokens_details are caught. Returns whether
// anything changed.
func stripLeakKeys(v interface{}) bool {
	changed := false
	switch t := v.(type) {
	case map[string]interface{}:
		for k, val := range t {
			if leakKeysAlways[k] {
				delete(t, k)
				changed = true
				continue
			}
			if leakKeysIfZero[k] && isZeroNumber(val) {
				delete(t, k)
				changed = true
				continue
			}
			if stripLeakKeysContainers[k] {
				continue // opaque user/tool payload — never descend (avoid corrupting it)
			}
			if stripLeakKeys(val) {
				changed = true
			}
		}
	case []interface{}:
		for _, item := range t {
			if stripLeakKeys(item) {
				changed = true
			}
		}
	}
	return changed
}

// isZeroNumber reports whether a decoded JSON value is the number 0. Handles
// both json.Number (UseNumber decoding) and float64.
func isZeroNumber(v interface{}) bool {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		return err == nil && f == 0
	case float64:
		return n == 0
	}
	return false
}

// openrouterLeakPrefix marks a synthesized redacted_thinking block whose `data`
// is the upstream aggregator's reasoning (base64 of plaintext, carrying a vendor
// prefix) rather than Anthropic's own opaque encrypted blob. Such a block leaks
// the upstream vendor (OpenRouter/LiteLLM), duplicates the reasoning already
// present in the proper `thinking` block, and violates the Anthropic spec — a
// real redacted_thinking carries an encrypted payload, so a client that
// round-trips this one back to Anthropic sends garbage. See router #373.
const openrouterLeakPrefix = "openrouter."

// isLeakedRedactedThinking reports whether a decoded content block is a
// synthesized redacted_thinking leak. It keys on the `data` value (a string
// beginning with the vendor prefix), NOT merely on type=="redacted_thinking",
// so a genuine Anthropic redacted_thinking block — an opaque encrypted blob with
// no vendor prefix — is never touched.
//
// The prefix test is case-folded and leading-whitespace-trimmed: this is a
// fail-open security control (a missed match re-leaks the vendor identity to the
// client, #373), so we harden it against trivial upstream serialization drift
// (" openrouter.", "Openrouter.") rather than matching one exact literal. Only
// the comparison is folded — the original `data` bytes are untouched.
func isLeakedRedactedThinking(v interface{}) bool {
	m, ok := v.(map[string]interface{})
	if !ok {
		return false
	}
	data, ok := m["data"].(string)
	return ok && strings.HasPrefix(strings.ToLower(strings.TrimSpace(data)), openrouterLeakPrefix)
}

// stripUpstreamReasoningBlocks removes the synthesized openrouter.reasoning
// redacted_thinking leak from an Anthropic response (router #373), handling both
// response shapes that sanitizeResponseBody sees:
//
//   - Non-stream: the leak is an element of the top-level `content[]` array; the
//     whole block is dropped (this also removes the duplicate reasoning).
//   - Stream: the leak rides on a `content_block` object of a content_block_start
//     event. We cannot drop the surrounding event:/data: line pair from inside
//     the JSON sanitizer without desyncing the stream, so the leaky `data` is
//     blanked in place — the block becomes opaque and carries no vendor marker
//     or plaintext, which the spec explicitly permits for redacted_thinking.
//
// Returns whether anything changed.
func stripUpstreamReasoningBlocks(v interface{}) bool {
	changed := false
	switch t := v.(type) {
	case map[string]interface{}:
		// Streaming content_block envelope (or any nested map that is itself a
		// leak block): neutralize the vendor data in place.
		if isLeakedRedactedThinking(t) {
			t["data"] = ""
			changed = true
		}
		for k, val := range t {
			if k == "content" {
				if arr, ok := val.([]interface{}); ok {
					kept := make([]interface{}, 0, len(arr))
					for _, el := range arr {
						if isLeakedRedactedThinking(el) {
							changed = true
							continue // drop the leak block from content[]
						}
						kept = append(kept, el)
					}
					if len(kept) != len(arr) {
						t["content"] = kept
					}
					// DO NOT mirror this drop onto the streaming path: blanking
					// `data` in place (above) is deliberate. Dropping a streamed
					// content_block would desync the content_block_start/delta/stop
					// index sequence the client reassembles by index.
					continue // content elements are leaves; do not recurse further
				}
			}
			if stripUpstreamReasoningBlocks(val) {
				changed = true
			}
		}
	case []interface{}:
		for _, item := range t {
			if stripUpstreamReasoningBlocks(item) {
				changed = true
			}
		}
	}
	return changed
}

// clampReasoningTokenDetails enforces the invariant that the reasoning/thinking
// token subset reported in *_details never exceeds the corresponding total
// (router #374). Some aggregating upstreams (OpenRouter/LiteLLM for glm-5) report
// a reasoning/thinking count larger than the completion/output total, which is
// arithmetically impossible (a subset cannot exceed the whole) and makes
// downstream `text = total - reasoning` go negative. Billing is unaffected: it
// reads the total (the lower, correct number), not these detail fields, and from
// the raw upstream bytes rather than this client-facing copy. We clamp the
// reported detail down to the total. Returns whether anything changed.
func clampReasoningTokenDetails(obj map[string]interface{}) bool {
	usage, ok := obj["usage"].(map[string]interface{})
	if !ok {
		return false
	}
	changed := false
	// OpenAI surface: completion_tokens_details.reasoning_tokens <= completion_tokens
	if clampTokenDetail(usage, "completion_tokens", "completion_tokens_details", "reasoning_tokens") {
		changed = true
	}
	// Anthropic surface: output_tokens_details.thinking_tokens <= output_tokens
	if clampTokenDetail(usage, "output_tokens", "output_tokens_details", "thinking_tokens") {
		changed = true
	}
	return changed
}

// clampTokenDetail clamps usage[detailsKey][subKey] down to usage[totalKey] when
// it exceeds it. No-op when either value is absent or unparseable, or when the
// subset is already within bounds. The clamped value is re-encoded as a
// json.Number so it round-trips as an integer (UseNumber decoding).
func clampTokenDetail(usage map[string]interface{}, totalKey, detailsKey, subKey string) bool {
	total, ok := jsonNumberToInt(usage[totalKey])
	if !ok {
		return false
	}
	details, ok := usage[detailsKey].(map[string]interface{})
	if !ok {
		return false
	}
	sub, ok := jsonNumberToInt(details[subKey])
	if !ok || sub <= total {
		return false
	}
	details[subKey] = json.Number(strconv.Itoa(total))
	return true
}

// jsonNumberToInt extracts an integer from a decoded JSON value, handling both
// json.Number (UseNumber decoding, the path sanitizeResponseBody uses) and
// float64 (plain decoding). Returns ok=false for any other type.
func jsonNumberToInt(v interface{}) (int, bool) {
	switch n := v.(type) {
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i), true
		}
		if f, err := n.Float64(); err == nil {
			return int(f), true
		}
		return 0, false
	case float64:
		return int(n), true
	}
	return 0, false
}

// sanitizeResponseBody removes upstream identity/cost/fingerprint fields from a
// JSON response object (a full chat completion or a single SSE chunk) before it
// is forwarded to the client (#184), and — when newID is non-empty — rewrites
// the top-level "id" to a broker-issued value so the upstream's id format (e.g.
// OpenRouter's "gen-...") cannot fingerprint the provider.
//
// It returns (body, false) unchanged when the body is not a JSON object or
// nothing needed changing, so it is safe to call on every chunk. Billing and
// TEE signing are unaffected: billing reads the raw upstream bytes, and signing
// attests this client-facing copy.
func (c *Ctrl) sanitizeResponseBody(body []byte, newID string) ([]byte, bool) {
	// UseNumber so integer fields (token counts, created, ids) round-trip without
	// the float64 precision loss / scientific-notation reshaping of a plain
	// interface{} decode.
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var obj map[string]interface{}
	if err := dec.Decode(&obj); err != nil {
		// Fail-open: a body we cannot parse is forwarded as-is. #184 is a security
		// control, so log at Warn (not Debug) — a leaky-but-unparseable response
		// means stripping silently no-opped and must be visible in production.
		// Callers decode compressed bodies first (see decodeBody) and upstream is
		// requested with Accept-Encoding: identity, so this should be rare.
		if len(bytes.TrimSpace(body)) > 0 {
			c.logger.Warnf("sanitizeResponseBody: body not a JSON object, leak-field stripping skipped (forwarded unsanitized): %v", err)
		}
		return body, false
	}

	changed := stripLeakKeys(obj)

	// Drop the synthesized openrouter.reasoning redacted_thinking block leaked by
	// aggregating upstreams on the Anthropic surface (router #373).
	if stripUpstreamReasoningBlocks(obj) {
		changed = true
	}

	// Clamp the reasoning/thinking token subset so it never exceeds the
	// completion/output total (router #374).
	if clampReasoningTokenDetails(obj) {
		changed = true
	}

	if newID != "" {
		if _, ok := obj["id"]; ok {
			obj["id"] = newID
			changed = true
		}
	}

	if !changed {
		return body, false
	}

	// Encode with HTML escaping disabled so message content with <, >, & is not
	// rewritten to < etc. (preserves byte-fidelity of the assistant text).
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(obj); err != nil {
		// We parsed and stripped leak fields but cannot re-encode; returning the
		// original would re-leak. Practically unreachable for JSON-decoded data,
		// but surface it loudly rather than silently forwarding the leaky body.
		c.logger.Errorf("sanitizeResponseBody: failed to re-encode sanitized body, forwarding original unsanitized: %v", err)
		return body, false
	}
	// Encoder.Encode appends a trailing newline; drop it.
	return bytes.TrimRight(buf.Bytes(), "\n"), true
}

// isCompressedEncoding reports whether a Content-Encoding value denotes a
// compressed body that must be decoded before JSON sanitization.
func isCompressedEncoding(enc string) bool {
	switch strings.ToLower(strings.TrimSpace(enc)) {
	case "", "identity":
		return false
	default:
		return true
	}
}

// decodeBody decompresses a response body per its Content-Encoding so leak-field
// sanitization can run on inspectable JSON even when an upstream compressed
// despite the identity request (#184).
//
// Unlike initializeReader (which silently returns the raw, still-compressed
// reader for unknown encodings or a bad gzip header), decodeBody returns an
// explicit error in those cases. That distinction matters: the caller deletes
// Content-Encoding on success, so a silent raw passthrough would ship compressed
// bytes labelled as identity — a broken, still-leaky response. On error the
// caller keeps the original body and header untouched.
func decodeBody(body []byte, encoding string) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "identity":
		return body, nil
	case "gzip":
		r, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("gzip reader: %w", err)
		}
		defer r.Close()
		return io.ReadAll(r)
	case "deflate":
		// HTTP "deflate" is ambiguous: some servers send zlib-wrapped (RFC 1950),
		// others raw (RFC 1951). Try zlib first, fall back to raw flate.
		if zr, err := zlib.NewReader(bytes.NewReader(body)); err == nil {
			defer zr.Close()
			return io.ReadAll(zr)
		}
		r := flate.NewReader(bytes.NewReader(body))
		defer r.Close()
		return io.ReadAll(r)
	case "br":
		return io.ReadAll(brotli.NewReader(bytes.NewReader(body)))
	default:
		return nil, fmt.Errorf("unsupported content-encoding %q", encoding)
	}
}

// sanitizeStreamLine prepares one SSE line for the client. It drops SSE
// comment/keepalive lines (returns forward=false) — e.g. OpenRouter's
// ": OPENROUTER PROCESSING", which leaks the upstream's identity and carries no
// data — and, for "data: {json}" chunks, rewrites the model name (LoRA) and
// strips identity/cost/fingerprint leak fields and rewrites the chunk id to
// idRewrite (#184). Non-JSON lines (e.g. "data: [DONE]") pass through after the
// model rewrite. idRewrite must be stable across a stream so every chunk carries
// the same id. The raw upstream stream is captured separately (rawBody) for
// billing, so billing is unaffected; TEE signing attests the sanitized bytes the
// client actually receives.
func (c *Ctrl) sanitizeStreamLine(ctx *gin.Context, line string, idRewrite string) (string, bool) {
	lead := strings.TrimSpace(line)
	if lead == "" {
		return line, true // preserve SSE event separators
	}
	if isSSEComment([]byte(lead)) {
		return "", false
	}

	// Model rewrite first (LoRA); format-preserving string replace.
	line = c.rewriteResponseModelLine(ctx, line)

	if after, ok := strings.CutPrefix(strings.TrimSpace(line), "data:"); ok {
		payload := strings.TrimSpace(after)
		if strings.HasPrefix(payload, "{") {
			if sanitized, changed := c.sanitizeResponseBody([]byte(payload), idRewrite); changed {
				return "data: " + string(sanitized) + "\n", true
			}
		}
	}
	return line, true
}
