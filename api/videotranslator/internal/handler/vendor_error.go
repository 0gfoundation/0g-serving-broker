package handler

import (
	"fmt"
	"regexp"
)

// maxVendorErrorBodyLog bounds the raw vendor body in a log line. Vendor
// rejections are small JSON envelopes; this is generous for one and still keeps a
// vendor that answers a 4xx with an HTML error page from filling the log.
const maxVendorErrorBodyLog = 500

// A vendor is not the only thing that writes these error bodies: a WAF, CDN, or
// reverse proxy answering a 4xx on the vendor's behalf commonly echoes the
// offending REQUEST back, headers included — and the header this sidecar relays is
// the deployment's MiniMax/DashScope API key. CLAUDE.md's rule on credentials in
// logs is unconditional, and a log is exactly the place a leaked key outlives the
// request that leaked it.
var (
	// bearerToken covers the form this sidecar actually transmits:
	// "Authorization: Bearer <key>". "|" is in the class because the broker's own
	// session tokens are "<base64>|<signature>" — without it the signature half
	// would survive. "=" is in the CLASS rather than a trailing "=*" for the same
	// reason: base64 padding sits mid-token there ("...fQ==|0xa886"), so a trailing
	// quantifier stops at the padding and leaves the rest in the log.
	bearerToken = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9\-._~+/|=]+`)
	// bareAPIKey covers a key echoed WITHOUT its scheme — a gateway reflecting a
	// header value, or a JSON field. Keyed on the prefix both 0G ("app-sk-") and
	// the vendors ("sk-") use. Over-redacting a diagnostic that merely mentions
	// such a prefix is the cheap direction of this trade.
	bareAPIKey = regexp.MustCompile(`(?i)\b(?:app-)?sk-[A-Za-z0-9\-._~+/|=]+`)
)

// vendorErrorDetail renders the vendor's own explanation of a rejection for a log
// line, falling back to the RAW BODY when the parsed fields do not carry one.
//
// That fallback is the whole point. Both vendors' APIError keeps Body
// "for logging when Code/Message couldn't be parsed" — but the handlers only ever
// logged code and message, so the one case where the body is the sole explanation
// produced `code="" message=""` and nothing else. Observed live: MiniMax rejecting
// every video create with a 400 whose body neither parser recognized, leaving an
// operator with two empty strings and no way to tell a bad model name from a bad
// parameter from an exhausted quota.
//
// This helper is log-side only — the raw body never goes to the client. That is
// NOT the same as saying nothing vendor-authored reaches them: the handlers return
// apiErr.Message, which is a substring of this body, and on a providerType:standard
// deployment a vendor message like "model X does not exist" names the upstream that
// type exists to hide. That channel pre-dates this file (every forwarder relays
// upstream 4xx bodies through the broker's sanitizeResponseBody, which strips leak
// KEYS and never inspects prose), and closing it belongs broker-side where
// ProviderType is visible. Not claimed as solved here.
//
// The MESSAGE is what decides, not the code: a message is the prose an operator
// acts on, and a code without one (`code="1004" message=""`) leaves them with a
// number to go look up. So the body rides along whenever the message is empty,
// even though something parsed.
//
// requestID is the vendor's own correlation id, the identifier their support asks
// for. It is threaded here rather than left on APIError because the 4xx branch —
// the only one that populates it — logs through this helper and never through
// APIError.Error().
func vendorErrorDetail(code, message, body, requestID string) string {
	detail := fmt.Sprintf("code=%q message=%q", code, message)
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
// logged. See the patterns for why a vendor error body can contain one at all.
//
// Not a general secret scanner, and not claimed as one: a credential shaped like
// neither (a vendor that mints opaque keys with no prefix, one split across JSON
// fields) passes through. The 500-byte cap is the backstop that bounds what any
// such miss costs.
func redactCredentials(body string) string {
	body = bearerToken.ReplaceAllString(body, "Bearer [redacted]")
	return bareAPIKey.ReplaceAllString(body, "[redacted]")
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
