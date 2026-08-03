package handler

import (
	"fmt"
	"regexp"
)

// maxVendorErrorBodyLog bounds the raw vendor body in a log line. Vendor
// rejections are small JSON envelopes; this is generous for one and still keeps a
// vendor that answers a 4xx with an HTML error page from filling the log.
//
// Duplicated from videotranslator/internal/handler/vendor_error.go rather than
// imported — see image.go's package doc comment: api/imagetranslator is a
// structurally separate package tree from api/videotranslator, sharing no code.
const maxVendorErrorBodyLog = 500

// A vendor is not the only thing that writes these error bodies: a WAF, CDN, or
// reverse proxy answering a 4xx on the vendor's behalf commonly echoes the
// offending REQUEST back, headers included — and the header this sidecar relays is
// the deployment's Kling API key. CLAUDE.md's rule on credentials in logs is
// unconditional, and a log is exactly the place a leaked key outlives the request
// that leaked it.
var (
	// bearerToken covers the form this sidecar actually transmits:
	// "Authorization: Bearer <key>". "|" is in the class because the broker's own
	// session tokens are "<base64>|<signature>" — without it the signature half
	// would survive. "=" is in the CLASS rather than a trailing "=*" for the same
	// reason: base64 padding sits mid-token there ("...fQ==|0xa886"), so a trailing
	// quantifier stops at the padding and leaves the rest in the log.
	bearerToken = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9\-._~+/|=]+`)
	// bareAPIKey covers a key echoed WITHOUT its scheme — a gateway reflecting a
	// header value, or a JSON field. Keyed on the prefix 0G ("app-sk-") and
	// DashScope/Kling ("sk-") use. Over-redacting a diagnostic that merely mentions
	// such a prefix is the cheap direction of this trade.
	bareAPIKey = regexp.MustCompile(`(?i)\b(?:app-)?sk-[A-Za-z0-9\-._~+/|=]+`)
	// jwtToken covers a prefix-less JWT credential — no scheme to anchor on AND no
	// "sk-" prefix, the intersection of the two patterns above's blind spots.
	jwtToken = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)
)

// vendorErrorDetail renders the vendor's own explanation of a rejection for a log
// line, falling back to the RAW BODY when the parsed fields do not carry one —
// see videotranslator/internal/handler/vendor_error.go's identical helper for the
// full rationale (a code/message that fails to parse must not silently produce
// `code="" message=""` with no other trace).
//
// requestID is the vendor's own correlation id, the identifier their support asks
// for.
func vendorErrorDetail(code, message, body, requestID string) string {
	detail := fmt.Sprintf("code=%q message=%q", code, redactCredentials(message))
	if message == "" {
		if body == "" {
			detail += " (and an empty body — the vendor explained nothing)"
		} else {
			detail += fmt.Sprintf(" unparsed_body=%q", truncateForLog(redactCredentials(body), maxVendorErrorBodyLog))
		}
	}
	if requestID != "" {
		detail += fmt.Sprintf(" request_id=%q", requestID)
	}
	return detail
}

// redactCredentials strips the credential forms this deployment can actually emit
// — a Bearer header value, and a bare sk-/app-sk- key — from a body about to be
// logged or returned to the client. Not a general secret scanner: a vendor that
// mints opaque keys with no prefix and no scheme, or one split across JSON
// fields, passes through. The size cap bounds log volume, not exposure.
func redactCredentials(s string) string {
	s = bearerToken.ReplaceAllString(s, "Bearer [redacted]")
	s = bareAPIKey.ReplaceAllString(s, "[redacted]")
	return jwtToken.ReplaceAllString(s, "[redacted]")
}

// truncateForLog caps s at limit BYTES, marking that it was cut so a truncated
// value is never mistaken for the whole one. Byte-based, so it can split a
// multi-byte rune; callers render the result with %q, which escapes the orphan as
// \xNN rather than emitting invalid UTF-8.
func truncateForLog(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "...(truncated)"
}
