package ctrl

import (
	"bytes"
	"mime/multipart"
	"testing"

	"github.com/0glabs/0g-serving-broker/common/audiospec"
)

func TestRawJSONAudioMaxDuration(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "integer", body: `{"max_duration":60}`, want: "60"},
		{name: "float", body: `{"max_duration":12.5}`, want: "12.5"},
		{name: "alongside other fields", body: `{"model":"seed-audio-1.0","input":"hi","max_duration":30}`, want: "30"},

		// Every one of these yields "", which the spec resolves to its ceiling —
		// i.e. the SAFE direction. None of them is a degraded path.
		{name: "absent", body: `{"model":"seed-audio-1.0"}`, want: ""},
		{name: "null", body: `{"max_duration":null}`, want: ""},
		{name: "not json", body: `not json at all`, want: ""},
		{name: "empty body", body: ``, want: ""},
		{name: "array body", body: `[1,2,3]`, want: ""},

		// Quoted values are rejected even though they look numeric. A JSON string
		// unmarshals into json.Number when its contents are numeric, so without the
		// quote check this would resolve to 60 while a struct decode downstream
		// rejects the whole request.
		{name: "quoted numeric is rejected", body: `{"max_duration":"60"}`, want: ""},
		{name: "quoted non-numeric is rejected", body: `{"max_duration":"sixty"}`, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rawJSONAudioMaxDuration([]byte(tt.body)); got != tt.want {
				t.Errorf("rawJSONAudioMaxDuration(%q) = %q, want %q", tt.body, got, tt.want)
			}
		})
	}
}

// An oversized value is dropped rather than truncated: a shortened number is a
// DIFFERENT number, and silently reserving against it would be worse than
// reserving the ceiling.
func TestRawJSONAudioMaxDurationRejectsOversizedValue(t *testing.T) {
	huge := append(bytes.Repeat([]byte("9"), maxRawAudioFieldBytes+1), []byte("")...)
	body := []byte(`{"max_duration":` + string(huge) + `}`)
	if got := rawJSONAudioMaxDuration(body); got != "" {
		t.Errorf("an oversized max_duration resolved to %q; it must be dropped, not truncated", got)
	}
}

func multipartAudioBody(t *testing.T, fields map[string]string, fileField string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatalf("write field %q: %v", k, err)
		}
	}
	if fileField != "" {
		fw, err := w.CreateFormFile(fileField, "sample.wav")
		if err != nil {
			t.Fatalf("create file part: %v", err)
		}
		if _, err := fw.Write([]byte("RIFF....WAVE")); err != nil {
			t.Fatalf("write file part: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return buf.Bytes(), w.FormDataContentType()
}

func TestRawAudioMaxDurationMultipart(t *testing.T) {
	t.Run("reads a plain form value", func(t *testing.T) {
		body, ct := multipartAudioBody(t, map[string]string{"model": "seed-audio-1.0", "max_duration": "45"}, "")
		if got := rawAudioMaxDuration(body, ct); got != "45" {
			t.Errorf("got %q, want %q", got, "45")
		}
	})

	// The multipart transport exists so a client can send reference audio as file
	// parts. A file part must never be read as this field — the upstream's own form
	// reader does not either.
	t.Run("skips file parts", func(t *testing.T) {
		body, ct := multipartAudioBody(t, map[string]string{"max_duration": "45"}, "reference_audio")
		if got := rawAudioMaxDuration(body, ct); got != "45" {
			t.Errorf("got %q, want %q — a reference-audio file part interfered", got, "45")
		}
	})

	t.Run("a file part named max_duration is not read as the value", func(t *testing.T) {
		body, ct := multipartAudioBody(t, nil, "max_duration")
		if got := rawAudioMaxDuration(body, ct); got != "" {
			t.Errorf("got %q, want \"\" — a file part was read as a form value", got)
		}
	})

	t.Run("absent", func(t *testing.T) {
		body, ct := multipartAudioBody(t, map[string]string{"model": "seed-audio-1.0"}, "")
		if got := rawAudioMaxDuration(body, ct); got != "" {
			t.Errorf("got %q, want \"\"", got)
		}
	})

	// A body that stops parsing partway yields what was found before that point;
	// here nothing, which is the ceiling, not an error.
	t.Run("truncated body", func(t *testing.T) {
		body, ct := multipartAudioBody(t, map[string]string{"max_duration": "45"}, "")
		if got := rawAudioMaxDuration(body[:len(body)/3], ct); got != "" && got != "45" {
			t.Errorf("got %q; a truncated body must yield either the value read so far or \"\"", got)
		}
	})
}

// A JSON body sent without a multipart content type must take the JSON path, and
// vice versa — the dispatch is on Content-Type, not on sniffing the bytes.
func TestRawAudioMaxDurationTransportDispatch(t *testing.T) {
	jsonBody := []byte(`{"max_duration":60}`)
	if got := rawAudioMaxDuration(jsonBody, "application/json"); got != "60" {
		t.Errorf("json path: got %q, want %q", got, "60")
	}
	if got := rawAudioMaxDuration(jsonBody, ""); got != "60" {
		t.Errorf("absent content type must fall back to JSON: got %q", got)
	}
	// multipart without a boundary is unparseable as multipart; falling back to the
	// JSON reader yields "" rather than erroring, which reserves the ceiling.
	if got := rawAudioMaxDuration(jsonBody, "multipart/form-data"); got != "60" {
		t.Errorf("multipart with no boundary should fall through to JSON: got %q", got)
	}
}

// The end-to-end property this whole file protects: whatever a client sends, the
// seconds fed to the fee calculation are in (0, ceiling]. There is no input that
// produces a zero reserve, because a zero reserve disables the gate — N
// simultaneous creates would each read 0 and every one pass against the same
// balance.
func TestReserveSecondsFromAnyBodyIsAlwaysUsable(t *testing.T) {
	spec, ok := audiospec.Get(audiospec.VendorSeedAudio)
	if !ok {
		t.Fatal("seedaudio rules are not registered")
	}
	max := spec.MaxOutputSeconds()

	bodies := []string{
		`{"max_duration":60}`,
		`{"max_duration":0}`,
		`{"max_duration":-5}`,
		`{"max_duration":1e300}`,
		`{"max_duration":"60"}`,
		`{"max_duration":null}`,
		`{}`,
		`garbage`,
		``,
	}
	for _, body := range bodies {
		raw := rawAudioMaxDuration([]byte(body), "application/json")
		secs := spec.ReserveSeconds(raw)
		if secs <= 0 || secs > max {
			t.Errorf("body %q → raw %q → %d seconds, want a bound in (0, %d]", body, raw, secs, max)
		}
	}
}
