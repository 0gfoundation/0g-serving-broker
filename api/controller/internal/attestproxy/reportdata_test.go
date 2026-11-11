package attestproxy

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// The ingress's socket quotes only a zero second half: no layout of ours has one, so an
// ingress cannot obtain a quote binding an enc_pub of its choosing to the broker's signer.
// What reaches dstack is the canonical form of the validated bytes, nothing else.
func TestReadOnlyQuoteOnlyIngressShaped(t *testing.T) {
	var reached []string
	c := startWith(t, &reached, true)
	ingress := strings.Repeat("ab", 32) + strings.Repeat("00", 32)
	broker := strings.Repeat("ab", 64) // enc_pub ‖ signer-shaped: second half non-zero

	get := func(q string) int {
		code, _ := post(t, c, http.MethodGet, "/GetQuote?"+q)
		return code
	}
	if code := get("report_data=" + ingress); code != http.StatusOK {
		t.Fatalf("ingress-shaped GET = %d, want 200", code)
	}
	if got := reached[len(reached)-1]; got != "/GetQuote?report_data="+ingress {
		t.Errorf("dstack got %q, want the canonical query only", got)
	}

	before := len(reached)
	for name, q := range map[string]string{
		"broker-shaped":       "report_data=" + broker,
		"short, nonzero tail": "report_data=" + strings.Repeat("ab", 40),
		"0x prefix":           "report_data=0x" + ingress,
		"extra field":         "report_data=" + ingress + "&vm_config=x",
		"two report_data":     "report_data=" + ingress + "&report_data=" + broker,
		"too long":            "report_data=" + ingress + "00",
	} {
		if code := get(q); code != http.StatusForbidden && code != http.StatusBadRequest {
			t.Errorf("%s: GET = %d, want 403 or 400", name, code)
		}
	}
	// The same through POST, the shape the broker uses.
	cl := &http.Client{Transport: c.Transport}
	resp, err := cl.Post("http://proxy/GetQuote", "application/json", strings.NewReader(`{"report_data":"`+broker+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("broker-shaped POST = %d, want 403", resp.StatusCode)
	}
	if len(reached) != before {
		t.Errorf("dstack saw %v after refused quotes, want none", reached[before:])
	}
}
