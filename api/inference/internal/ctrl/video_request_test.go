package ctrl

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime/multipart"
	"strconv"
	"strings"
	"testing"

	"github.com/0glabs/0g-serving-broker/inference/config"
)

// videoBilling is the per-model block a MiniMax-H3-shaped deployment configures:
// H3 renders 4–15s at 2K, and 4 is both its floor and OpenAI's default.
func videoBilling() *config.BillingConfig {
	return &config.BillingConfig{
		Mode:              config.BillingModePerVideoSecond,
		DefaultSeconds:    4,
		MinSeconds:        4,
		MaxSeconds:        15,
		DefaultResolution: "2K",
	}
}

// buildMultipart writes a real multipart body (fields in the given order) with a
// fixed boundary, so a test can assert on what the re-encode preserved.
func buildMultipart(t *testing.T, fields [][2]string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.SetBoundary("testboundary"); err != nil {
		t.Fatalf("SetBoundary: %v", err)
	}
	for _, f := range fields {
		if err := w.WriteField(f[0], f[1]); err != nil {
			t.Fatalf("WriteField %s: %v", f[0], err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return buf.Bytes(), w.FormDataContentType()
}

// formValues re-parses a multipart body into name -> ordered values, so a test
// reads the rewritten request exactly the way the upstream's ParseMultipartForm
// does rather than by scanning bytes.
func formValues(t *testing.T, body []byte, contentType string) map[string][]string {
	t.Helper()
	boundary, err := multipartBoundary(contentType)
	if err != nil {
		t.Fatalf("multipartBoundary: %v", err)
	}
	parts, err := readMultipartParts(body, boundary)
	if err != nil {
		t.Fatalf("readMultipartParts: %v", err)
	}
	out := map[string][]string{}
	for _, p := range parts {
		out[p.name] = append(out[p.name], string(p.data))
	}
	return out
}

// ==========================================================================
// The three authoring rules, on both transports.
// ==========================================================================

func TestAuthorMultipartVideoRequest_SecondsRules(t *testing.T) {
	tests := []struct {
		name        string
		seconds     string
		wantSeconds string
		wantErr     bool
	}{
		{name: "absent writes the configured default", seconds: "", wantSeconds: "4"},
		{name: "present is written back normalised", seconds: "8", wantSeconds: "8"},
		{name: "fractional rounds up, the vendor takes an integer", seconds: "7.2", wantSeconds: "8"},
		{name: "below the vendor floor is raised to it", seconds: "1", wantSeconds: "4"},
		{name: "above the vendor ceiling is clamped down", seconds: "60", wantSeconds: "15"},
		// Refusals: every one of these used to be a place where the broker read a
		// number the upstream would not, and every divergence under-reserved.
		{name: "non-numeric is refused", seconds: "abc", wantErr: true},
		{name: "zero is refused", seconds: "0", wantErr: true},
		{name: "negative is refused", seconds: "-5", wantErr: true},
		{name: "trailing garbage is refused, not truncated", seconds: "5s", wantErr: true},
		{name: "an oversized value is refused, not read as its prefix", seconds: "5" + strings.Repeat("0", 200), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fields := [][2]string{{"model", "MiniMax-H3"}, {"prompt", "a cat"}}
			if tt.seconds != "" {
				fields = append(fields, [2]string{"seconds", tt.seconds})
			}
			body, ct := buildMultipart(t, fields)

			out, auth, err := authorMultipartVideoRequest(body, ct, videoBilling())
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected a refusal for seconds=%q, got seconds=%d", tt.seconds, auth.seconds)
				}
				return
			}
			if err != nil {
				t.Fatalf("authorMultipartVideoRequest: %v", err)
			}
			got := formValues(t, out, ct)
			if len(got["seconds"]) != 1 || got["seconds"][0] != tt.wantSeconds {
				t.Errorf("forwarded seconds = %v, want [%s]", got["seconds"], tt.wantSeconds)
			}
			// What is forwarded and what is priced must be the same number, or the
			// whole exercise is pointless.
			if got["seconds"][0] != strconv.FormatInt(auth.seconds, 10) {
				t.Errorf("priced %d but forwarded %s", auth.seconds, got["seconds"][0])
			}
		})
	}
}

func TestAuthorJSONVideoRequest_SecondsRules(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantSeconds int64
		wantErr     bool
	}{
		{name: "absent writes the configured default", body: `{"model":"m","prompt":"p"}`, wantSeconds: 4},
		{name: "null is absent", body: `{"seconds":null}`, wantSeconds: 4},
		{name: "present is written back", body: `{"seconds":8}`, wantSeconds: 8},
		{name: "float rounds up", body: `{"seconds":7.2}`, wantSeconds: 8},
		{name: "clamped to the vendor ceiling", body: `{"seconds":60}`, wantSeconds: 15},
		// The upstream decodes this field into a json.Number, which rejects a
		// string — so reading one here would be the broker accepting a request the
		// upstream will not.
		{name: "a string is refused", body: `{"seconds":"8"}`, wantErr: true},
		{name: "a non-object body is refused", body: `[1,2,3]`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, auth, err := authorJSONVideoRequest([]byte(tt.body), videoBilling())
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected a refusal, got seconds=%d", auth.seconds)
				}
				return
			}
			if err != nil {
				t.Fatalf("authorJSONVideoRequest: %v", err)
			}
			if auth.seconds != tt.wantSeconds {
				t.Errorf("priced seconds = %d, want %d", auth.seconds, tt.wantSeconds)
			}
			var m map[string]json.RawMessage
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatalf("rewritten body is not JSON: %v", err)
			}
			if string(m["seconds"]) != strconv.FormatInt(tt.wantSeconds, 10) {
				t.Errorf("forwarded seconds = %s, want %d", m["seconds"], tt.wantSeconds)
			}
		})
	}
}

// ==========================================================================
// Resolution authoring: the tier the vendor renders must be the one priced.
// ==========================================================================

func TestAuthoredVideoResolution(t *testing.T) {
	tests := []struct {
		name string
		size string
		want string
	}{
		{name: "absent size gets the configured tier", size: "", want: "2K"},
		// Pixel dimensions are an aspect ratio to every supported vendor, never a
		// tier — MiniMax renders its deployment default, DashScope snaps to its own
		// enum. Neither is knowable here, so the configured tier is written instead.
		{name: "pixel dimensions are not a tier", size: "1280x720", want: "2K"},
		{name: "portrait pixel dimensions are not a tier either", size: "720x1280", want: "2K"},
		{name: "a resolution token is honoured", size: "1080P", want: "1080P"},
		{name: "a lowercase token is honoured", size: "4k", want: "4k"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := authoredVideoResolution(tt.size, "2K"); got != tt.want {
				t.Errorf("authoredVideoResolution(%q) = %q, want %q", tt.size, got, tt.want)
			}
		})
	}

	// With no configured default there is nothing to author for a pixel size —
	// the pre-#628 behaviour, kept so an unconfigured deployment bills as it did.
	if got := authoredVideoResolution("1280x720", ""); got != "" {
		t.Errorf("authoredVideoResolution with no default = %q, want empty", got)
	}
}

func TestAuthorMultipartVideoRequest_WritesResolutionAndDropsClientSupplied(t *testing.T) {
	// A client that names "resolution" itself would select a tier the reserve did
	// not price, so it must not survive into the forwarded request.
	body, ct := buildMultipart(t, [][2]string{
		{"prompt", "a cat"},
		{"size", "1280x720"},
		{"resolution", "4K"},
	})

	out, auth, err := authorMultipartVideoRequest(body, ct, videoBilling())
	if err != nil {
		t.Fatalf("authorMultipartVideoRequest: %v", err)
	}
	got := formValues(t, out, ct)
	if len(got["resolution"]) != 1 || got["resolution"][0] != "2K" {
		t.Errorf("forwarded resolution = %v, want [2K]", got["resolution"])
	}
	if auth.priceResolution != "2K" {
		t.Errorf("priced resolution = %q, want 2K", auth.priceResolution)
	}
	// size is left alone: it is the client's aspect-ratio hint, and the vendor
	// still needs it.
	if len(got["size"]) != 1 || got["size"][0] != "1280x720" {
		t.Errorf("forwarded size = %v, want [1280x720]", got["size"])
	}
}

// ==========================================================================
// Everything the broker does not author must survive the rewrite untouched.
// ==========================================================================

func TestAuthorMultipartVideoRequest_PreservesOtherFields(t *testing.T) {
	// A prompt that LOOKS like a "seconds" form field but is not one (the boundary
	// in it is not this body's). A rewrite that scanned for the field name instead
	// of parsing MIME would either read 999 as the duration or corrupt this value;
	// the upstream, which parses MIME properly, would then disagree with it.
	trickyPrompt := "a cat\r\n--nottheboundary\r\nContent-Disposition: form-data; name=\"seconds\"\r\n\r\n999"
	body, ct := buildMultipart(t, [][2]string{
		{"model", "MiniMax-H3"},
		{"prompt", trickyPrompt},
		{"seed", "42"},
		{"seconds", "6"},
	})

	out, _, err := authorMultipartVideoRequest(body, ct, videoBilling())
	if err != nil {
		t.Fatalf("authorMultipartVideoRequest: %v", err)
	}
	got := formValues(t, out, ct)
	for _, want := range [][2]string{{"model", "MiniMax-H3"}, {"prompt", trickyPrompt}, {"seed", "42"}} {
		if len(got[want[0]]) != 1 || got[want[0]][0] != want[1] {
			t.Errorf("field %q = %q, want %q", want[0], got[want[0]], want[1])
		}
	}
	if len(got["seconds"]) != 1 || got["seconds"][0] != "6" {
		t.Errorf("seconds = %v, want [6] exactly once", got["seconds"])
	}
}

func TestAuthorMultipartVideoRequest_DuplicateSecondsCollapse(t *testing.T) {
	// Two "seconds" parts: the upstream's FormValue reads the first, so that is
	// what is priced — and only one survives, so nothing downstream can pick the
	// other.
	body, ct := buildMultipart(t, [][2]string{
		{"seconds", "5"},
		{"seconds", "15"},
	})
	out, auth, err := authorMultipartVideoRequest(body, ct, videoBilling())
	if err != nil {
		t.Fatalf("authorMultipartVideoRequest: %v", err)
	}
	if auth.seconds != 5 {
		t.Errorf("priced seconds = %d, want 5 (the first value, matching FormValue)", auth.seconds)
	}
	if got := formValues(t, out, ct)["seconds"]; len(got) != 1 || got[0] != "5" {
		t.Errorf("forwarded seconds = %v, want [5]", got)
	}
}

func TestAuthorJSONVideoRequest_PreservesOtherFields(t *testing.T) {
	// A seed past 2^53 would be mangled by a float64 round-trip.
	in := `{"model":"m","prompt":"p","seed":9007199254740993,"input_reference":{"file_id":"f1"}}`
	out, _, err := authorJSONVideoRequest([]byte(in), videoBilling())
	if err != nil {
		t.Fatalf("authorJSONVideoRequest: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("rewritten body is not JSON: %v", err)
	}
	if string(m["seed"]) != "9007199254740993" {
		t.Errorf("seed = %s, want 9007199254740993", m["seed"])
	}
	if string(m["input_reference"]) != `{"file_id":"f1"}` {
		t.Errorf("input_reference = %s", m["input_reference"])
	}
	if string(m["prompt"]) != `"p"` {
		t.Errorf("prompt = %s", m["prompt"])
	}
}

// ==========================================================================
// Single-model services have no per-model billing block at all.
// ==========================================================================

func TestResolveVideoAuthoring_NilBilling(t *testing.T) {
	auth := resolveVideoAuthoring(0, "1280x720", nil)
	if auth.seconds != config.DefaultVideoSeconds {
		t.Errorf("seconds = %d, want %d", auth.seconds, config.DefaultVideoSeconds)
	}
	if auth.resolution != "" {
		t.Errorf("resolution = %q, want empty (nothing configured to author)", auth.resolution)
	}
	// Nothing authored, so the fee basis stays the client's own size — exactly
	// what a single-model deployment billed before.
	if auth.priceResolution != "1280x720" {
		t.Errorf("priceResolution = %q, want 1280x720", auth.priceResolution)
	}
}

// TestAuthorVideoRequest_ClientErrorClassification pins which failures the proxy
// may suppress as ordinary bad requests. Misclassifying a broker-side pricing or
// config fault as a client error would hide it from the broker-fault alert.
func TestIsClientVideoRequestError(t *testing.T) {
	body, ct := buildMultipart(t, [][2]string{{"seconds", "abc"}})
	if _, _, err := authorMultipartVideoRequest(body, ct, videoBilling()); !IsClientVideoRequestError(err) {
		t.Errorf("an unreadable seconds should classify as a client error, got %v", err)
	}
	if _, _, err := authorMultipartVideoRequest([]byte("garbage"), "multipart/form-data", videoBilling()); !IsClientVideoRequestError(err) {
		t.Errorf("a multipart body with no boundary should classify as a client error, got %v", err)
	}
	if _, _, err := authorMultipartVideoRequest([]byte("garbage"), ct, videoBilling()); !IsClientVideoRequestError(err) {
		t.Errorf("an unparsable multipart body should classify as a client error, got %v", err)
	}
	if _, _, err := authorJSONVideoRequest([]byte(`"not an object"`), videoBilling()); !IsClientVideoRequestError(err) {
		t.Errorf("a non-object JSON body should classify as a client error, got %v", err)
	}
	if IsClientVideoRequestError(errors.New("price feed unavailable")) {
		t.Error("an unrelated broker fault must not be suppressed as a client error")
	}
}
