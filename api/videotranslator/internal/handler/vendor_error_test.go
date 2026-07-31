package handler

import (
	"strings"
	"testing"
)

// The case this helper exists for: the vendor rejected the request and NEITHER
// parser recognized its error envelope. Before, that logged two empty strings and
// the operator had nothing. The raw body must survive into the line.
func TestVendorErrorDetail(t *testing.T) {
	for _, tc := range []struct {
		name, code, message, body, requestID string
		wantContains                         []string
		wantMissing                          string
	}{
		{
			name:         "a credential echoed back by a gateway never reaches the log",
			body:         `{"error":"bad request","echo":{"headers":{"Authorization":"Bearer sk-api-e53HjFRV49EE_tz04enq"}}}`,
			wantContains: []string{"Bearer [redacted]"},
			wantMissing:  "sk-api-",
		},
		{
			// The broker's own session token is "<base64>|<signature>". Without "|"
			// in the pattern the signature half survived the redaction.
			name:         "a piped session token is redacted whole",
			body:         `{"echo":{"Authorization":"Bearer app-sk-eyJhZGRyZXNzIjoiMHgifQ==|0xa886f31ecc4a20fc"}}`,
			wantContains: []string{"Bearer [redacted]"},
			wantMissing:  "0xa886",
		},
		{
			// A gateway reflecting the header VALUE, or a JSON field — no scheme to
			// anchor on.
			name:         "a bare key with no Bearer prefix is still redacted",
			body:         `{"echo":{"X-Api-Key":"sk-api-e53HjFRV49EE","api_key":"app-sk-eyJhIjoxfQ"}}`,
			wantContains: []string{"[redacted]"},
			wantMissing:  "e53HjFRV49EE",
		},
		{
			name: "request_id is logged — it is what vendor support asks for",
			code: "1004", message: "invalid api key", requestID: "06bbd146",
			wantContains: []string{`request_id="06bbd146"`},
		},
		{
			name: "a code with no message still gets the body: nothing else carries prose",
			code: "1004", body: `{"detail":"quota exhausted for account"}`,
			wantContains: []string{"quota exhausted for account"},
		},
		{
			name: "parsed envelope: body is redundant, keep the line short",
			code: "1004", message: "invalid api key", body: `{"base_resp":{"status_code":1004}}`,
			wantContains: []string{`code="1004"`, `message="invalid api key"`},
			wantMissing:  "base_resp",
		},
		{
			name: "unparsed: the body is the ONLY explanation",
			body: `{"error":"model MiniMax-H3 does not exist"}`,
			wantContains: []string{
				`code=""`, `message=""`,
				"model MiniMax-H3 does not exist",
			},
		},
		{
			name:         "unparsed and empty: say so rather than looking like a bug here",
			wantContains: []string{"the vendor explained nothing"},
		},
		{
			name:    "only a message parsed",
			message: "quota exceeded", body: "ignored",
			wantContains: []string{`message="quota exceeded"`},
			wantMissing:  "ignored",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := vendorErrorDetail(tc.code, tc.message, tc.body, tc.requestID)
			for _, want := range tc.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("detail = %q, want it to contain %q", got, want)
				}
			}
			if tc.wantMissing != "" && strings.Contains(got, tc.wantMissing) {
				t.Errorf("detail = %q, must not contain %q", got, tc.wantMissing)
			}
		})
	}
}

// A vendor answering a 4xx with an HTML error page must not fill the log.
func TestVendorErrorDetailTruncatesABigBody(t *testing.T) {
	// Two inputs: plain bytes, and the worst case for %q — every byte an escape,
	// which the previous version of this test missed by using only "x".
	for name, body := range map[string]string{
		"plain":             strings.Repeat("x", 5000),
		"all escaped by %q": strings.Repeat(`"`, 5000),
	} {
		got := vendorErrorDetail("", "", body, "")
		// %q can expand one byte to six (\xNN), so the honest ceiling is on the
		// TRUNCATED byte count, not on the rendered length.
		if len(got) > maxVendorErrorBodyLog*6+120 {
			t.Errorf("%s: detail is %d chars, want it bounded by the %d-byte cut", name, len(got), maxVendorErrorBodyLog)
		}
		if !strings.Contains(got, "(truncated)") {
			t.Errorf("%s: a cut body must say so", name)
		}
	}
}
