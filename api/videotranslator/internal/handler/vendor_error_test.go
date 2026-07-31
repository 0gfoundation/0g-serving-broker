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
		name, code, message, body string
		wantContains              []string
		wantMissing               string
	}{
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
			got := vendorErrorDetail(tc.code, tc.message, tc.body)
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
	got := vendorErrorDetail("", "", strings.Repeat("x", 5000))
	if len(got) > maxVendorErrorBodyLog+80 {
		t.Errorf("detail is %d chars, want it bounded near %d", len(got), maxVendorErrorBodyLog)
	}
	if !strings.Contains(got, "(truncated)") {
		t.Errorf("a cut body must say so, got %q", got[:80])
	}
}
