package ctrl

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
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

// engineControlFields are top-level request fields that change what the engine
// runs rather than what it answers: a per-request template (vLLM), a pickled
// logit processor (sglang dill.loads), a client-chosen adapter path, or
// internal hidden states. No advertised model takes them from clients; the
// LoRA rewrite sets lora_adapter_name itself after this guard runs.
var engineControlFields = []string{
	"chat_template", "custom_logit_processor", "lora_path", "lora_adapter_name", "return_hidden_states",
}

// templateKwargsAllowed are the chat_template_kwargs keys clients may set:
// the thinking toggles our templates read. Everything else is dropped. A
// denylist of apply_chat_template's own parameters is not enough: engines
// merge these kwargs into their own with dict.update (vLLM CVE-2025-61620 /
// CVE-2025-62426), so any key the engine passes itself can be overridden.
// Same list as the router (0g-router#1589).
var templateKwargsAllowed = map[string]bool{
	"enable_thinking": true, "thinking": true, "clear_thinking": true,
	"preserve_thinking": true, "thinking_budget": true, "reasoning_effort": true,
}

// selfHostedFieldsAllowed are the top-level fields forwarded to an engine that
// runs beside the broker (sglang / vLLM): the OpenAI chat, Anthropic messages
// and Responses request fields plus common sampling extras. Anything else
// (engine-specific switches such as return_hidden_states, custom_logit_processor,
// lora_path, chat_template, ebnf, ...) is dropped rather than refused, so a
// client sending an unknown optional field still gets an answer. Forwarders
// (hosted APIs) keep their own extensions and only lose engineControlFields.
var selfHostedFieldsAllowed = map[string]bool{
	// shared
	"model": true, "stream": true, "stream_options": true, "temperature": true, "top_p": true,
	"top_k": true, "min_p": true, "max_tokens": true, "max_completion_tokens": true, "n": true,
	"stop": true, "seed": true, "presence_penalty": true, "frequency_penalty": true,
	"repetition_penalty": true, "logprobs": true, "top_logprobs": true, "logit_bias": true,
	"response_format": true, "tools": true, "tool_choice": true, "parallel_tool_calls": true,
	"user": true, "metadata": true, "reasoning_effort": true, "reasoning": true, "thinking": true,
	"chat_template_kwargs": true, "separate_reasoning": true, "include_reasoning": true,
	"functions": true, "function_call": true,
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
	"source": true, "image": true, "video": true,
}

// guardEngineControls strips engine-control fields and refuses media links an
// engine would fetch from its own network. selfHosted narrows the top-level
// fields to selfHostedFieldsAllowed. The error is a client fault (400). A body
// that is not a JSON object is returned unchanged for later stages.
func guardEngineControls(body []byte, selfHosted bool) ([]byte, error) {
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil {
		return body, nil
	}
	changed := false
	for f := range top {
		if (selfHosted && !selfHostedFieldsAllowed[f]) || slices.Contains(engineControlFields, f) {
			delete(top, f)
			changed = true
		}
	}
	if raw, ok := top["chat_template_kwargs"]; ok {
		var kw map[string]json.RawMessage
		if json.Unmarshal(raw, &kw) == nil {
			stripped := false
			for k := range kw {
				if !templateKwargsAllowed[k] {
					delete(kw, k)
					stripped = true
				}
			}
			if stripped {
				b, err := json.Marshal(kw)
				if err != nil {
					return nil, err
				}
				top["chat_template_kwargs"] = b
				changed = true
			}
		}
	}
	for _, field := range []string{"messages", "input"} {
		if raw, ok := top[field]; ok {
			var v any
			if json.Unmarshal(raw, &v) == nil {
				if err := checkMediaLinks(v, selfHosted); err != nil {
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
func checkMediaLinks(v any, inlineOnly bool) error {
	switch t := v.(type) {
	case []any:
		for _, e := range t {
			if err := checkMediaLinks(e, inlineOnly); err != nil {
				return err
			}
		}
	case map[string]any:
		for k, e := range t {
			if mediaContainers[k] {
				if err := checkMediaValue(k, e, inlineOnly); err != nil {
					return fmt.Errorf("invalid %s: %w", k, err)
				}
			}
			if err := checkMediaLinks(e, inlineOnly); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkMediaValue validates a media container's value: a string, a list of
// strings (video frames), or an object with url / file_url / file_data.
func checkMediaValue(key string, v any, inlineOnly bool) error {
	video := key == "video_url" || key == "video"
	if _, isObj := v.(map[string]any); key == "source" && !isObj {
		return nil // a citation's "source" text, not a fetch
	}
	switch t := v.(type) {
	case string:
		return validateMediaLink(t, video, inlineOnly)
	case []any:
		for _, e := range t {
			if s, ok := e.(string); ok {
				if err := validateMediaLink(s, video, inlineOnly); err != nil {
					return err
				}
			}
		}
	case map[string]any:
		for _, f := range []string{"url", "file_url", "file_data"} {
			if s, ok := t[f].(string); ok {
				if err := validateMediaLink(s, video, inlineOnly); err != nil {
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
	if !video && !strings.Contains(link, ":") && !strings.HasPrefix(link, "/") && !strings.Contains(link, ".") {
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
