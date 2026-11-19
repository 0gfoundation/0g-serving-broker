package ctrl

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
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

// templateCallParams are apply_chat_template's own named parameters. A key in
// chat_template_kwargs with one of these names is not a template variable: it
// reconfigures the call (chat_template swaps the whole template). Template
// variables such as enable_thinking / thinking / reasoning_effort pass through.
// ponytail: denylist of the call's parameters, not an allowlist of variables,
// so models whose templates read other flags keep working; switch to an
// allowlist if a template is found that treats a variable as code.
var templateCallParams = map[string]bool{
	"chat_template": true, "conversation": true, "tools": true, "documents": true,
	"tokenize": true, "add_generation_prompt": true, "continue_final_message": true,
	"return_tensors": true, "return_dict": true, "return_assistant_tokens_mask": true,
	"tokenizer_kwargs": true, "padding": true, "truncation": true, "max_length": true,
}

// mediaLinkKeys name the content parts whose link an engine fetches itself.
var mediaLinkKeys = map[string]bool{"image_url": true, "video_url": true, "audio_url": true, "file_url": true}

// guardEngineControls strips engine-control fields and refuses media links an
// engine would fetch from its own network. The error is a client fault (400).
// A body that is not a JSON object is returned unchanged for later stages.
func guardEngineControls(body []byte) ([]byte, error) {
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil {
		return body, nil
	}
	changed := false
	for _, f := range engineControlFields {
		if _, ok := top[f]; ok {
			delete(top, f)
			changed = true
		}
	}
	if raw, ok := top["chat_template_kwargs"]; ok {
		var kw map[string]json.RawMessage
		if json.Unmarshal(raw, &kw) == nil {
			stripped := false
			for k := range kw {
				if templateCallParams[k] {
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
				if err := checkMediaLinks(v); err != nil {
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
// image_url / video_url / audio_url / file_url link, as a string or {"url": …}.
// Tool-call arguments are JSON strings, so they are never walked into.
func checkMediaLinks(v any) error {
	switch t := v.(type) {
	case []any:
		for _, e := range t {
			if err := checkMediaLinks(e); err != nil {
				return err
			}
		}
	case map[string]any:
		for k, e := range t {
			if mediaLinkKeys[k] {
				link, _ := e.(string)
				if m, ok := e.(map[string]any); ok {
					link, _ = m["url"].(string)
				}
				if link != "" {
					if err := validateMediaLink(link); err != nil {
						return fmt.Errorf("invalid %s: %w", k, err)
					}
				}
				continue
			}
			if err := checkMediaLinks(e); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateMediaLink accepts data: URIs and http(s) links to a public host. It
// refuses file://, bare paths and other schemes, and hosts that only make sense
// inside the engine's network: localhost, single-label names, internal suffixes,
// and literal loopback / private / link-local / CGNAT / unspecified IPs.
// ponytail: no DNS lookup and no redirect check; a public name that resolves or
// redirects inside is the engine's egress to close (sglang follows redirects).
func validateMediaLink(link string) error {
	if strings.HasPrefix(link, "data:") {
		return nil
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
