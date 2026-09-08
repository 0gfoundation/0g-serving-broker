package ctrl

import (
	"bytes"
	"mime/multipart"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// SPEC §5.3.1: a body that cannot be parsed as an envelope is not thereby an
// unsealed body, and a body that IS an envelope is one whatever Content-Type
// carries it. A multipart body never parses as JSON, so before this guard both
// entry points called an envelope in a form part "not sealed" and forwarded it.
//
// The guard reads what Go's multipart parser reads and nothing more. That is a
// deliberate limit, not an oversight: no endpoint accepting multipart has a
// sealed request profile, so a client evading the check has forwarded its own
// ciphertext upstream and gets garbage back. There is no adversary with a
// motive, only an honest client with a bug — so the malformed and exotically
// encoded shapes are forwarded exactly as they were before the guard existed,
// and TestMalformedAndExoticBodiesAreForwarded pins that.

const sealedEnvelopeJSON = `{"v":1,"kem_id":"0x0020","ciphertext":"AAAA"}`

func ginCtxWithContentType(contentType string) *gin.Context {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/audio/transcriptions", nil)
	ctx.Request.Header.Set("Content-Type", contentType)
	return ctx
}

// transcriptionBody writes a body shaped like a real speech-to-text request —
// the audio first, as a client sends it — then whatever the case is about.
func transcriptionBody(t *testing.T, extra func(*multipart.Writer)) (body []byte, contentType string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	file, err := w.CreateFormFile("file", "audio.wav")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := file.Write([]byte("RIFF....audio....")); err != nil {
		t.Fatalf("write audio: %v", err)
	}
	if err := w.WriteField("model", "whisper-large-v3"); err != nil {
		t.Fatalf("WriteField: %v", err)
	}
	if extra != nil {
		extra(w)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return buf.Bytes(), w.FormDataContentType()
}

func TestMultipartCarryingTheEnvelopeIsRefused(t *testing.T) {
	body, contentType := transcriptionBody(t, func(w *multipart.Writer) {
		if err := w.WriteField(e2eeBodyMarker, sealedEnvelopeJSON); err != nil {
			t.Fatalf("WriteField: %v", err)
		}
	})

	// Both entry points, because the async routes never reach the proxy and that
	// is exactly how they came to be a hole one request shape over.
	if _, err := (&Ctrl{}).MaybeUnsealRequest(ginCtxWithContentType(contentType), body); err == nil {
		t.Error("the sync proxy must refuse a multipart body carrying the envelope")
	}
	if why := (&Ctrl{}).RefuseAsync(contentType, body); why == "" {
		t.Error("the async routes must refuse it too")
	}
}

// A part named `_e2ee` is refused wherever it sits and whatever else the body
// declares — the guard reads every part, so nothing earlier in the body hides
// one later. That property is the whole point of the check, so it is asserted
// against the shapes most likely to break it.
func TestTheMarkerPartIsFoundWhereverItSits(t *testing.T) {
	marker := func(w *multipart.Writer) {
		if err := w.WriteField(e2eeBodyMarker, sealedEnvelopeJSON); err != nil {
			t.Fatalf("WriteField: %v", err)
		}
	}
	for _, tt := range []struct {
		name  string
		parts func(*multipart.Writer)
	}{
		{"alone", marker},
		{"after many other fields", func(w *multipart.Writer) {
			for i := 0; i < 500; i++ {
				if err := w.WriteField("f", "v"); err != nil {
					t.Fatalf("WriteField: %v", err)
				}
			}
			marker(w)
		}},
		{"after a field whose value mentions the marker", func(w *multipart.Writer) {
			if err := w.WriteField("prompt", "a talk about _e2ee"); err != nil {
				t.Fatalf("WriteField: %v", err)
			}
			marker(w)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body, contentType := transcriptionBody(t, tt.parts)
			if _, err := (&Ctrl{}).MaybeUnsealRequest(ginCtxWithContentType(contentType), body); err == nil {
				t.Error("must refuse rather than forward")
			}
			if why := (&Ctrl{}).RefuseAsync(contentType, body); why == "" {
				t.Error("the async routes must refuse it too")
			}
		})
	}
}

// The rule is on part NAMES, never on the raw bytes. `prompt` carries arbitrary
// caller text, so a substring rule would 400 a legitimate transcription for
// talking about the protocol.
func TestAnOrdinaryTranscriptionIsForwarded(t *testing.T) {
	for _, tt := range []struct {
		name  string
		parts func(*multipart.Writer)
	}{
		{"no extra fields", nil},
		{"a field whose value mentions the marker", func(w *multipart.Writer) {
			if err := w.WriteField("prompt", `explain the "_e2ee" envelope`); err != nil {
				t.Fatalf("WriteField: %v", err)
			}
		}},
		{"a field whose NAME merely contains the marker", func(w *multipart.Writer) {
			if err := w.WriteField("my_e2eefield", "v"); err != nil {
				t.Fatalf("WriteField: %v", err)
			}
		}},
		{"a filename that is the marker", func(w *multipart.Writer) {
			if _, err := w.CreateFormFile("attachment", e2eeBodyMarker); err != nil {
				t.Fatalf("CreateFormFile: %v", err)
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body, contentType := transcriptionBody(t, tt.parts)
			got, err := (&Ctrl{}).MaybeUnsealRequest(ginCtxWithContentType(contentType), body)
			if err != nil {
				t.Fatalf("must be forwarded, got %v", err)
			}
			if !bytes.Equal(got, body) {
				t.Error("forwarded means forwarded unchanged, not rewritten")
			}
			if why := (&Ctrl{}).RefuseAsync(contentType, body); why != "" {
				t.Errorf("the async routes must accept it too, got %q", why)
			}
		})
	}
}

// The limits of the guard, asserted so they are a decision on record rather
// than something to be rediscovered. Each of these forwards, as it did before
// the guard existed. Refusing them would mean reimplementing a MIME parser to
// out-guess Go's — which buys nothing here, because the client who would send
// them gains nothing by evading the check.
func TestMalformedAndExoticBodiesAreForwarded(t *testing.T) {
	const marker = "--B\r\nContent-Disposition: form-data; name=\"" + e2eeBodyMarker + "\"\r\n\r\n" + sealedEnvelopeJSON + "\r\n"
	for _, tt := range []struct {
		name        string
		contentType string
		body        string
	}{
		{
			name:        "the boundary is missing from the Content-Type",
			contentType: "multipart/form-data",
			body:        marker + "--B--\r\n",
		},
		{
			// Parts are read only when the CLIENT said multipart. Otherwise a
			// stray `boundary` parameter on some other media type would make an
			// unrelated body get parsed for form fields.
			name:        "a non-multipart Content-Type carrying a boundary parameter",
			contentType: "application/json; boundary=B",
			body:        marker + "--B--\r\n",
		},
		{
			name:        "the enumeration dies on a part header with no colon",
			contentType: "multipart/form-data; boundary=B",
			body:        "--B\r\nnot-a-header\r\n\r\nv\r\n" + marker + "--B--\r\n",
		},
		{
			name:        "the marker is one nesting level down",
			contentType: "multipart/form-data; boundary=B",
			body: "--B\r\nContent-Disposition: form-data; name=\"w\"\r\nContent-Type: multipart/mixed; boundary=I\r\n\r\n" +
				"--I\r\nContent-Disposition: form-data; name=\"" + e2eeBodyMarker + "\"\r\n\r\n" + sealedEnvelopeJSON +
				"\r\n--I--\r\n--B--\r\n",
		},
		{
			name:        "the name is RFC 2231-encoded in a charset Go will not decode",
			contentType: "multipart/form-data; boundary=B",
			body:        "--B\r\nContent-Disposition: form-data; name*=iso-8859-1''%5Fe2ee\r\n\r\n" + sealedEnvelopeJSON + "\r\n--B--\r\n",
		},
		{
			name:        "the name is an RFC 2047 encoded word",
			contentType: "multipart/form-data; boundary=B",
			body:        "--B\r\nContent-Disposition: form-data; name=\"=?utf-8?B?X2UyZWU=?=\"\r\n\r\n" + sealedEnvelopeJSON + "\r\n--B--\r\n",
		},
		{
			name:        "the marker is on Content-Type rather than Content-Disposition",
			contentType: "multipart/form-data; boundary=B",
			body:        "--B\r\nContent-Type: text/plain; name=\"" + e2eeBodyMarker + "\"\r\n\r\n" + sealedEnvelopeJSON + "\r\n--B--\r\n",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := (&Ctrl{}).MaybeUnsealRequest(ginCtxWithContentType(tt.contentType), []byte(tt.body)); err != nil {
				t.Errorf("this shape is knowingly out of scope and must forward, got %v", err)
			}
			if why := (&Ctrl{}).RefuseAsync(tt.contentType, []byte(tt.body)); why != "" {
				t.Errorf("both entry points must agree it is out of scope, got %q", why)
			}
		})
	}
}

// A JSON envelope is sealed whatever Content-Type is on it, and a non-multipart
// Content-Type must not send the body down the multipart path.
func TestTheJSONPathIsUnaffected(t *testing.T) {
	envelope := []byte(`{"model":"gpt-4","_e2ee":{"v":1}}`)
	if why := (&Ctrl{}).RefuseAsync("application/json", envelope); why == "" {
		t.Error("a JSON envelope must still be refused on the async routes")
	}
	// Mislabelled as multipart: not a multipart body, so the multipart check
	// declines, and the JSON check still recognises the envelope.
	if why := (&Ctrl{}).RefuseAsync("multipart/form-data; boundary=B", envelope); why == "" {
		t.Error("an envelope mislabelled multipart must still be refused")
	}
	// And a body that merely mentions the marker in its content is not sealed.
	plain := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"what is _e2ee?"}]}`)
	if why := (&Ctrl{}).RefuseAsync("application/json", plain); why != "" {
		t.Errorf("mentioning the marker is not sending one, got %q", why)
	}
	got, err := (&Ctrl{}).MaybeUnsealRequest(ginCtxWithContentType("application/json"), plain)
	if err != nil {
		t.Fatalf("must be forwarded, got %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Error("forwarded unchanged")
	}
}
