package handler

import "fmt"

// maxVendorErrorBodyLog bounds the raw vendor body in a log line. Vendor
// rejections are small JSON envelopes; this is generous for one and still keeps a
// vendor that answers a 4xx with an HTML error page from filling the log.
const maxVendorErrorBodyLog = 500

// vendorErrorDetail renders the vendor's own explanation of a rejection for a log
// line, falling back to the RAW BODY when neither a code nor a message could be
// parsed out of it.
//
// That fallback is the whole point. Both vendors' APIError keeps Body
// "for logging when Code/Message couldn't be parsed" — but the handlers only ever
// logged code and message, so the one case where the body is the sole explanation
// produced `code="" message=""` and nothing else. Observed live: MiniMax rejecting
// every video create with a 400 whose body neither parser recognized, leaving an
// operator with two empty strings and no way to tell a bad model name from a bad
// parameter from an exhausted quota.
//
// Log-side only, deliberately: the body is not surfaced to the client, because on
// a centralized/standard provider it can name the upstream this deployment is
// required to hide.
func vendorErrorDetail(code, message, body string) string {
	if code != "" || message != "" {
		return fmt.Sprintf("code=%q message=%q", code, message)
	}
	if body == "" {
		return `code="" message="" (and an empty body — the vendor explained nothing)`
	}
	return fmt.Sprintf("code=\"\" message=\"\" unparsed_body=%q", truncateForLog(body, maxVendorErrorBodyLog))
}

// truncateForLog caps s at max characters, marking that it was cut so a truncated
// value is never mistaken for the whole one.
func truncateForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}
