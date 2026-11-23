package ctrl

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"

	paramnorm "github.com/0gfoundation/0g-param-normalization"

	"github.com/0glabs/0g-serving-broker/inference/config"
)

// Engine-control guard.
//
// On 2026-11-19 a stolen key drove the self-hosted sglang engine behind this
// broker with fields no client needs:
//   - chat_template_kwargs.chat_template: transformers' apply_chat_template takes
//     chat_template as a named parameter, and sglang splats chat_template_kwargs
//     into that call, so the caller's Jinja replaced the model's template and
//     echoed data back through raise_exception in the 400 error text;
//   - image_url / video_url links the engine fetched from inside its own network:
//     file:///etc/passwd, 127.0.0.1:<port> scans, metadata and internal hosts.
//
// The broker is the trust boundary for every engine whether or not the caller
// came through the router, so these are removed or refused here, before any
// broker-side rewrite (reasoning translation, LoRA, injectBodyFields) adds the
// fields it legitimately sets.

// The part shared with the router lives in paramnorm.Sanitize
// (github.com/0gfoundation/0g-param-normalization):
//   - top-level fields that change what the engine runs rather than what it
//     answers are dropped: a per-request template (vLLM), a pickled logit
//     processor (sglang dill.loads), a client-chosen adapter path or name, or
//     internal hidden states. No advertised model takes them from clients; the
//     LoRA rewrite sets lora_adapter_name itself after this guard runs;
//   - chat_template_kwargs keeps only the thinking toggles our templates read.
//     A denylist of apply_chat_template's own parameters is not enough: engines
//     merge these kwargs into their own with dict.update (vLLM CVE-2025-61620 /
//     CVE-2025-62426), so any key the engine passes itself can be overridden.
//
// What stays here needs the broker's own service config: the self-hosted field
// allowlist, the vllm_xargs shape check and the media checks.

// selfHostedFieldsAllowed are the top-level fields forwarded to an engine that
// runs beside the broker (sglang / vLLM): the OpenAI chat, Anthropic messages
// and Responses request fields plus common sampling extras. Anything else
// (engine-specific switches such as return_hidden_states, custom_logit_processor,
// lora_path, chat_template, ebnf, ...) is dropped rather than refused, so a
// client sending an unknown optional field still gets an answer. Forwarders
// (hosted APIs) keep their own extensions and only lose the engine-control
// fields paramnorm.Sanitize drops.
var selfHostedFieldsAllowed = map[string]bool{
	// shared
	"model": true, "stream": true, "stream_options": true, "temperature": true, "top_p": true,
	"top_k": true, "min_p": true, "max_tokens": true, "max_completion_tokens": true, "n": true,
	"stop": true, "seed": true, "presence_penalty": true, "frequency_penalty": true,
	"repetition_penalty": true, "logprobs": true, "top_logprobs": true, "logit_bias": true,
	"response_format": true, "tools": true, "tool_choice": true, "parallel_tool_calls": true,
	"user": true, "metadata": true, "reasoning_effort": true, "reasoning": true, "thinking": true,
	"chat_template_kwargs": true, "separate_reasoning": true, "include_reasoning": true,
	"functions": true, "function_call": true, "skip_special_tokens": true,
	// chat completions / anthropic messages
	"messages": true, "system": true, "stop_sequences": true,
	// responses
	"input": true, "instructions": true, "max_output_tokens": true, "text": true,
	"previous_response_id": true, "store": true, "include": true, "truncation": true,
}

// mediaContainers are the keys whose value an engine may load as media: a link
// string, or an object carrying it under url / file_url / file_data. They are
// read by key wherever they appear, with any "type" or none: engines' pydantic
// models ignore extra keys and vLLM reads typed-less parts by key. "source" is
// the Anthropic image/document source, whose url sglang fetches for every
// source.type except base64. "image" / "video" are the Qwen-style fields.
var mediaContainers = map[string]bool{
	"image_url": true, "video_url": true, "audio_url": true, "file_url": true, "file_data": true,
	"source": true, "image": true, "video": true, "input_audio": true,
}

// guardEngineControls strips engine-control fields and refuses media links an
// engine would fetch from its own network. selfHosted narrows the top-level
// fields to selfHostedFieldsAllowed. The error is a client fault (400).
//
// A body that is not a JSON object is refused, not passed through: the engines
// parse with Python's json.loads, which accepts NaN/Infinity, a UTF-8 BOM and
// UTF-16 that Go rejects, so an unparsed body would reach them unchecked.
func guardEngineControls(body []byte, selfHosted bool) ([]byte, error) {
	return guardEngineControlsMedia(body, selfHosted, nil)
}

// guardEngineControlsMedia is guardEngineControls with the media kinds the model
// declares (inputModalities: image / video / audio). nil means image only. It only
// matters for self-hosted engines, which decode inline media themselves.
func guardEngineControlsMedia(body []byte, selfHosted bool, declared map[string]bool) ([]byte, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return body, nil
	}
	// Engine-control fields and chat_template_kwargs keys outside the thinking
	// toggles. A body that is not a JSON object comes back as an error.
	body, err := paramnorm.Sanitize(body, paramnorm.SanitizeOptions{})
	if err != nil {
		return nil, err
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil || top == nil {
		return nil, fmt.Errorf("request body must be a JSON object")
	}
	changed := false
	if selfHosted {
		for f := range top {
			if f == "vllm_xargs" {
				if !scalarArgs(top[f]) {
					delete(top, f)
					changed = true
				}
				continue
			}
			if !selfHostedFieldsAllowed[f] {
				delete(top, f)
				changed = true
			}
		}
	}
	for _, field := range []string{"messages", "input"} {
		if raw, ok := top[field]; ok {
			var v any
			if json.Unmarshal(raw, &v) == nil {
				if err := checkMediaLinks(v, &mediaPolicy{inlineOnly: selfHosted, declared: declared}); err != nil {
					return nil, err
				}
			}
		}
	}
	if !changed {
		return body, nil
	}
	return json.Marshal(top)
}

// checkMediaLinks walks a decoded messages/input value and validates every
// media link it finds (see mediaContainers). Tool-call arguments are JSON
// strings, so they are never walked into.
// mediaPolicy is what checkMediaLinks enforces: inlineOnly (self-hosted engines)
// and, for those, the media kinds the model declares.
type mediaPolicy struct {
	inlineOnly bool
	declared   map[string]bool // nil: image only
}

func (p *mediaPolicy) allows(kind string) bool {
	if p.declared == nil {
		return kind == "image"
	}
	return p.declared[kind]
}

func checkMediaLinks(v any, pol *mediaPolicy) error {
	switch t := v.(type) {
	case []any:
		for _, e := range t {
			if err := checkMediaLinks(e, pol); err != nil {
				return err
			}
		}
	case map[string]any:
		for k, e := range t {
			if k == "image_embeds" || k == "audio_embeds" {
				return fmt.Errorf("%s is not accepted", k) // vLLM decodes these with torch.load
			}
			if mediaContainers[k] {
				if err := checkMediaValue(k, e, pol); err != nil {
					return fmt.Errorf("invalid %s: %w", k, err)
				}
			}
			if err := checkMediaLinks(e, pol); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkMediaValue validates a media container's value: a string, a list of
// strings (video frames), or an object with url / file_url / file_data.
func checkMediaValue(key string, v any, pol *mediaPolicy) error {
	inlineOnly := pol.inlineOnly
	kind := mediaKind(key)
	if inlineOnly && kind != "" && !pol.allows(kind) {
		if _, isObj := v.(map[string]any); key != "source" || isObj {
			return fmt.Errorf("this model does not accept %s input", kind)
		}
	}
	video := key == "video_url" || key == "video"
	if _, isObj := v.(map[string]any); key == "source" && !isObj {
		return nil // a citation's "source" text, not a fetch
	}
	switch t := v.(type) {
	case string:
		if err := validateMediaLink(t, video, inlineOnly); err != nil {
			return err
		}
		return checkInline(t, kind, inlineOnly)
	case []any:
		for _, e := range t {
			if s, ok := e.(string); ok {
				if err := validateMediaLink(s, video, inlineOnly); err != nil {
					return err
				}
				if err := checkInline(s, frameKind(kind), inlineOnly); err != nil {
					return err
				}
			}
		}
	case map[string]any:
		// base64 payloads the engine wraps into a data: URI itself (input_audio.data,
		// an Anthropic source of type base64): signature check only, never a path.
		if d, ok := t["data"].(string); ok && (key == "input_audio" || key == "source") {
			if err := checkInline(d, kind, inlineOnly); err != nil {
				return err
			}
		}
		for _, f := range []string{"url", "file_url", "file_data"} {
			if s, ok := t[f].(string); ok {
				if err := validateMediaLink(s, video, inlineOnly); err != nil {
					return err
				}
				if err := checkInline(s, kind, inlineOnly); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// validateMediaLink accepts data: URIs, bare base64 images, and (except when
// inlineOnly, i.e. self-hosted engines) http(s) links to a public host. It
// refuses file://, bare paths and other schemes, and hosts that only make sense
// inside the engine's network: localhost, single-label names, internal suffixes,
// and literal loopback / private / link-local / CGNAT / unspecified IPs.
// ponytail: no DNS lookup and no redirect check; a public name that resolves or
// redirects inside is the engine's egress to close (sglang follows redirects).
func validateMediaLink(link string, video, inlineOnly bool) error {
	if link == "" || strings.HasPrefix(link, "data:") {
		return nil
	}
	// sglang decodes a bare string as base64 image data, except one that starts
	// with "/" (opened as a path) or ends in an image extension (opened as a
	// file). Videos are checked with os.path.isfile first, so a bare string is
	// never safe there: require data: or http(s).
	if !video && !strings.Contains(link, ":") && !strings.HasPrefix(link, "/") && !strings.Contains(link, ".") &&
		!hasImageSuffix(link) {
		return nil
	}
	// A self-hosted engine fetches links from inside the CVM, following redirects,
	// so a public URL that 302s to 127.0.0.1 or a metadata address cannot be
	// checked here. The router already asks clients to inline media for these
	// models (features.media_input_routing); a pinned or sealed request that still
	// carries a link is refused rather than fetched.
	if inlineOnly {
		return fmt.Errorf("this model accepts media inline only: send it as a data: URI or base64")
	}
	u, err := url.Parse(link)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("only http(s) and data: links are accepted")
	}
	if u.User != nil {
		return fmt.Errorf("links with credentials are not accepted")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if ip := net.ParseIP(host); ip != nil {
		if !publicIP(ip) {
			return fmt.Errorf("link points at a non-public address")
		}
		return nil
	}
	// glibc's resolver also takes inet_aton forms (127.1, 0x7f.0.0.1, 2130706433)
	// that ParseIP rejects; no real TLD is numeric, so refuse any such host.
	last := host[strings.LastIndex(host, ".")+1:]
	if strings.HasPrefix(last, "0x") || strings.Trim(last, "0123456789") == "" {
		return fmt.Errorf("link points at a non-public address")
	}
	if host == "localhost" || !strings.Contains(host, ".") {
		return fmt.Errorf("link points at an internal host")
	}
	for _, s := range []string{".localhost", ".local", ".internal", ".lan", ".home.arpa", ".svc", ".cluster.local"} {
		if strings.HasSuffix(host, s) {
			return fmt.Errorf("link points at an internal host")
		}
	}
	return nil
}

var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

func publicIP(ip net.IP) bool {
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() || cgnat.Contains(ip))
}

// hasImageSuffix mirrors sglang's load_image check (lower().endswith(("png", "jpg",
// "jpeg", "webp", "gif")), no dot required), which opens such a string as a path.
func hasImageSuffix(s string) bool {
	l := strings.ToLower(s)
	for _, x := range []string{"png", "jpg", "jpeg", "webp", "gif"} {
		if strings.HasSuffix(l, x) {
			return true
		}
	}
	return false
}

// scalarArgs reports whether raw is an object whose values are all numbers,
// strings, booleans or lists of numbers: the shape vLLM's vllm_xargs takes and
// hands to per-request logits processors (Unlimited-OCR's NGramPerReqLogitsProcessor
// reads ngram_size / window_size / whitelist_token_ids from it, as the router's
// model docs tell clients to send). Anything nested deeper is dropped.
func scalarArgs(raw json.RawMessage) bool {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	for _, v := range m {
		switch x := v.(type) {
		case float64, string, bool:
		case []any:
			for _, e := range x {
				if _, ok := e.(float64); !ok {
					return false
				}
			}
		default:
			return false
		}
	}
	return true
}

// mediaKind maps a media container key to the modality it carries.
func mediaKind(key string) string {
	switch key {
	case "video_url", "video":
		return "video"
	case "audio_url", "input_audio":
		return "audio"
	case "image_url", "image", "source", "file_url", "file_data":
		return "image"
	}
	return ""
}

// frameKind: a Qwen-style "video" list carries image frames.
func frameKind(kind string) string {
	if kind == "video" {
		return "video-frame"
	}
	return kind
}

// checkInline refuses inline media whose decoded bytes do not start with a real
// container signature of the declared kind. On 2026-11-19 an attacker sent
// data:video/mp4;base64 payloads that decoded to an FFmpeg ffconcat script
// ("ffconcat version 1.0\nfile '../../etc/passwd'") and HLS playlists (#EXTM3U),
// and a data:image/svg+xml: inline, so "media inline only" let them through, and
// only the decoder failing stopped them. Text-like payloads match no signature.
func checkInline(link, kind string, inlineOnly bool) error {
	if !inlineOnly || link == "" {
		return nil
	}
	b64 := link
	if strings.HasPrefix(link, "data:") {
		i := strings.IndexByte(link, ',')
		if i < 0 || !strings.HasSuffix(strings.ToLower(link[:i]), ";base64") {
			return fmt.Errorf("inline media must be base64 encoded")
		}
		b64 = link[i+1:]
	}
	head := b64
	if len(head) > 64 {
		head = head[:64]
	}
	raw, err := base64.StdEncoding.DecodeString(head[:len(head)/4*4])
	if err != nil || len(raw) < 12 {
		return fmt.Errorf("inline media is not valid base64")
	}
	ok := false
	switch kind {
	case "image", "video-frame":
		ok = isImage(raw)
	case "video":
		ok = isVideo(raw)
	case "audio":
		ok = isAudio(raw)
	}
	if !ok {
		return fmt.Errorf("inline %s data is not a recognised %s format", kind, kind)
	}
	return nil
}

func isImage(b []byte) bool {
	return bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")) || bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}) ||
		bytes.HasPrefix(b, []byte("GIF87a")) || bytes.HasPrefix(b, []byte("GIF89a")) ||
		(bytes.HasPrefix(b, []byte("RIFF")) && string(b[8:12]) == "WEBP") || bytes.HasPrefix(b, []byte("BM"))
}

func isVideo(b []byte) bool {
	return string(b[4:8]) == "ftyp" || bytes.HasPrefix(b, []byte{0x1A, 0x45, 0xDF, 0xA3}) ||
		(bytes.HasPrefix(b, []byte("RIFF")) && string(b[8:12]) == "AVI ")
}

func isAudio(b []byte) bool {
	return (bytes.HasPrefix(b, []byte("RIFF")) && string(b[8:12]) == "WAVE") || bytes.HasPrefix(b, []byte("ID3")) ||
		(b[0] == 0xFF && b[1]&0xE0 == 0xE0) || bytes.HasPrefix(b, []byte("fLaC")) ||
		bytes.HasPrefix(b, []byte("OggS")) || string(b[4:8]) == "ftyp"
}

// declaredMedia is the set of media modalities a model declares, or nil (image
// only) when the service has no single resolvable model info.
func declaredMedia(mi *config.ModelInfo) map[string]bool {
	if mi == nil || mi.Architecture == nil {
		return nil
	}
	m := map[string]bool{}
	for _, x := range mi.Architecture.InputModalities {
		m[strings.ToLower(x)] = true
	}
	return m
}
