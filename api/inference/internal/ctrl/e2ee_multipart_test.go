package ctrl

import (
	"bytes"
	"mime/multipart"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// A sealed envelope smuggled into a multipart request is the one shape every
// other check in this file cannot see: they all begin by parsing the body as
// JSON, and a multipart body is not JSON, so "parse failed ⇒ not sealed" hands
// the envelope to the upstream in the clear. SPEC §5.3.1 exists for exactly this
// and states it as: a body that cannot be parsed as an envelope is not thereby
// an unsealed body.
//
// The spellings below are not variations for their own sake. Go's
// multipart.Part.FormName() — the accessor a first attempt reaches for — returns
// "" for any part whose Content-Disposition is not exactly `form-data`, so the
// third one answers "" to it while still declaring the name. Measured:
//
//	FormName()="_e2ee"  disposition="form-data; name=\"_e2ee\""
//	FormName()="_e2ee"  disposition="form-data; name=\"_e2ee\"; filename=\"env.json\""
//	FormName()=""       disposition="attachment; name=\"_e2ee\""
//
// A check written on FormName() therefore passes the first two tests and leaks
// on the third, which is the spelling someone smuggling an envelope would pick.
const sealedEnvelopeJSON = `{"v":1,"kem_id":"0x0020","ciphertext":"AAAA"}`

// buildMultipart writes a transcription-shaped body: the audio first (as a real
// client sends it), then the named parts the case is about.
func buildMultipart(t *testing.T, parts func(*multipart.Writer)) (body []byte, contentType string) {
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
		t.Fatalf("WriteField model: %v", err)
	}
	parts(w)
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return buf.Bytes(), w.FormDataContentType()
}

// rawPart appends a part with a hand-written Content-Disposition, so a
// disposition type other than form-data can be exercised.
func rawPart(t *testing.T, w *multipart.Writer, disposition, content string) {
	t.Helper()
	// multipart.Writer.CreatePart takes a MIMEHeader, which is the only way to
	// set a disposition its helpers would not produce.
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", disposition)
	p, err := w.CreatePart(h)
	if err != nil {
		t.Fatalf("CreatePart(%q): %v", disposition, err)
	}
	if _, err := p.Write([]byte(content)); err != nil {
		t.Fatalf("write part: %v", err)
	}
}

func ginCtxWithContentType(contentType string) *gin.Context {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/proxy/audio/transcriptions", nil)
	ctx.Request.Header.Set("Content-Type", contentType)
	return ctx
}

func TestMultipartCarryingASealedEnvelopeIsRefused(t *testing.T) {
	tests := []struct {
		name  string
		parts func(*multipart.Writer)
	}{
		{
			name: "as an ordinary form field",
			parts: func(w *multipart.Writer) {
				if err := w.WriteField("_e2ee", sealedEnvelopeJSON); err != nil {
					t.Fatalf("WriteField: %v", err)
				}
			},
		},
		{
			name: "as a file part, which FormName still reports",
			parts: func(w *multipart.Writer) {
				rawPart(t, w, `form-data; name="_e2ee"; filename="env.json"`, sealedEnvelopeJSON)
			},
		},
		{
			// The one a FormName()-based check misses.
			name: "under a disposition that is not form-data",
			parts: func(w *multipart.Writer) {
				rawPart(t, w, `attachment; name="_e2ee"`, sealedEnvelopeJSON)
			},
		},
		{
			name: "with the name spelled in single quotes, as some clients emit",
			parts: func(w *multipart.Writer) {
				rawPart(t, w, `form-data; name=_e2ee`, sealedEnvelopeJSON)
			},
		},
	}

	c := &Ctrl{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, contentType := buildMultipart(t, tt.parts)

			if !c.IsSealedRequest(contentType, body) {
				t.Error("IsSealedRequest must see it: the async routes refuse on this, and a miss there enqueues the envelope")
			}

			_, err := c.MaybeUnsealRequest(ginCtxWithContentType(contentType), body)
			if err == nil {
				t.Fatal("MaybeUnsealRequest must refuse it rather than return the body for forwarding")
			}
			if !strings.Contains(err.Error(), "sealed envelope") {
				t.Errorf("the error should say what is wrong, got %v", err)
			}
		})
	}
}

// The rule is on PART NAMES, never on the raw bytes, and this is the case that
// forces the distinction: `prompt` carries arbitrary caller text, so a substring
// rule would refuse a legitimate transcription for mentioning the marker.
func TestMultipartMentioningTheMarkerInContentIsForwarded(t *testing.T) {
	c := &Ctrl{}
	for _, where := range []string{"prompt", "language"} {
		t.Run(where, func(t *testing.T) {
			body, contentType := buildMultipart(t, func(w *multipart.Writer) {
				if err := w.WriteField(where, "explain what _e2ee means"); err != nil {
					t.Fatalf("WriteField: %v", err)
				}
			})

			if c.IsSealedRequest(contentType, body) {
				t.Error("a field whose VALUE mentions the marker is not a sealed request")
			}

			got, err := c.MaybeUnsealRequest(ginCtxWithContentType(contentType), body)
			if err != nil {
				t.Fatalf("a legitimate request must be forwarded, got %v", err)
			}
			if !bytes.Equal(got, body) {
				t.Error("the body must be forwarded unchanged")
			}
		})
	}
}

// An ordinary multipart request with no mention of the marker must not pay for
// any of this: the substring pre-check has to short-circuit before the parse.
func TestOrdinaryMultipartIsUntouched(t *testing.T) {
	c := &Ctrl{}
	body, contentType := buildMultipart(t, func(*multipart.Writer) {})

	if c.IsSealedRequest(contentType, body) {
		t.Error("an ordinary transcription is not a sealed request")
	}
	got, err := c.MaybeUnsealRequest(ginCtxWithContentType(contentType), body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Error("the body must be forwarded unchanged")
	}
}

// Fails closed where an error IS reported. The boundary of this claim is
// measured in multipartCarriesE2EEPart's doc comment, and the two cases here are
// the ones that reach the fail-closed branches.
func TestUnparseableMultipartMentioningTheMarkerIsRefused(t *testing.T) {
	c := &Ctrl{}
	tests := []struct {
		name        string
		body        string
		contentType string
	}{
		{
			name:        "no boundary in the content type",
			body:        "--x\r\nContent-Disposition: form-data; name=\"_e2ee\"\r\n\r\n{}\r\n--x--\r\n",
			contentType: "multipart/form-data",
		},
		{
			// The adversarial one: the header TERMINATES, so the part carries the
			// envelope as its body, but the name is malformed so no accessor
			// resolves it. ParseMediaType errors, and the refusal comes from there.
			name:        "unterminated quote in the part name",
			body:        "--x\r\nContent-Disposition: form-data; name=\"_e2ee\r\n\r\n" + sealedEnvelopeJSON + "\r\n--x--\r\n",
			contentType: `multipart/form-data; boundary=x`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !c.IsSealedRequest(tt.contentType, []byte(tt.body)) {
				t.Error("a body that cannot be shown NOT to carry an envelope must be treated as one")
			}
			if _, err := c.MaybeUnsealRequest(ginCtxWithContentType(tt.contentType), []byte(tt.body)); err == nil {
				t.Fatal("must refuse rather than forward")
			}
		})
	}
}

// The case that is NOT refused, asserted so the boundary of the rule is a test
// rather than a claim. A header truncated mid-name is dropped by Go with a plain
// io.EOF — there is no error to fail closed on — and that is sound rather than
// lucky: a header that never terminates has no body after it, so it holds no
// envelope, and no downstream parser sees a part there either.
func TestMultipartWithATruncatedHeaderIsForwarded(t *testing.T) {
	c := &Ctrl{}
	const body = "--x\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\nwhisper\r\n" +
		"--x\r\nContent-Disposition: form-data; name=\"_e2ee"
	const contentType = `multipart/form-data; boundary=x`

	if c.IsSealedRequest(contentType, []byte(body)) {
		t.Error("a header that never terminates carries no envelope")
	}
	if _, err := c.MaybeUnsealRequest(ginCtxWithContentType(contentType), []byte(body)); err != nil {
		t.Fatalf("unexpected refusal: %v", err)
	}
}

// A JSON request must not be judged by the multipart rule: the new branch must
// not swallow the path that actually opens envelopes. Asserted on the predicate
// directly, because MaybeUnsealRequest's JSON path reads enclave key material
// that a zero Ctrl does not have.
func TestJSONRequestsAreNotJudgedByTheMultipartRule(t *testing.T) {
	c := &Ctrl{}
	body := []byte(`{"model":"whisper-large-v3","_e2ee":` + sealedEnvelopeJSON + `}`)

	if carries, _ := multipartCarriesE2EEPart("application/json", body); carries {
		t.Error("the multipart rule must not claim a JSON body")
	}
	if !c.IsSealedRequest("application/json", body) {
		t.Error("a JSON envelope is still a sealed request")
	}
}
