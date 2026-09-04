package ctrl

import (
	"bytes"
	"mime"
	"mime/multipart"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"

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

			if sealed, _ := c.IsSealedRequest(contentType, body); !sealed {
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

// The name can be ENCODED, and then the literal never appears in the body. Both
// forms below decode to "_e2ee" through mime.ParseMediaType, so a check gated on
// the literal substring — which is what every other sealed-request test in this
// package uses — hands them straight through.
func TestMultipartWithAnEncodedE2EEPartNameIsRefused(t *testing.T) {
	tests := []struct {
		name        string
		disposition string
	}{
		{"RFC 2231 percent-encoded", `form-data; name*=utf-8''%5Fe2ee`},
		{"RFC 2231 continuation", `form-data; name*0="_e2"; name*1="ee"`},
	}

	c := &Ctrl{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, contentType := buildMultipart(t, func(w *multipart.Writer) {
				rawPart(t, w, tt.disposition, sealedEnvelopeJSON)
			})
			if bytes.Contains(body, []byte("_e2ee")) {
				t.Fatal("fixture no longer tests the encoded case: the literal marker is present")
			}

			if sealed, _ := c.IsSealedRequest(contentType, body); !sealed {
				t.Error("an encoded part name must be seen: the parser resolves it, so a downstream parser does too")
			}
			if _, err := c.MaybeUnsealRequest(ginCtxWithContentType(contentType), body); err == nil {
				t.Fatal("must refuse rather than forward")
			}
		})
	}
}

// mime.ParseMediaType lowercases parameter NAMES, so these decode to the marker
// exactly as their lowercase twins do — while a raw-bytes scan for "name*" sees
// nothing. Detection therefore cannot be gated on any spelling heuristic: it
// enumerates and asks the parser.
func TestMultipartWithAnUppercaseEncodedPartNameIsRefused(t *testing.T) {
	tests := []struct {
		name        string
		disposition string
	}{
		{"uppercase parameter name", `form-data; NAME*=utf-8''%5Fe2ee`},
		{"mixed-case continuation", `form-data; Name*0="_e2"; Name*1="ee"`},
	}

	c := &Ctrl{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, contentType := buildMultipart(t, func(w *multipart.Writer) {
				rawPart(t, w, tt.disposition, sealedEnvelopeJSON)
			})
			if bytes.Contains(body, []byte("_e2ee")) || bytes.Contains(body, []byte("name*")) {
				t.Fatal("fixture no longer tests the case-folded path")
			}

			if sealed, _ := c.IsSealedRequest(contentType, body); !sealed {
				t.Error("the parser resolves this name, so a downstream parser does too")
			}
			if _, err := c.MaybeUnsealRequest(ginCtxWithContentType(contentType), body); err == nil {
				t.Fatal("must refuse rather than forward")
			}
		})
	}
}

// A Content-Type that CLAIMS multipart but does not parse must not skip the
// guard. Deciding "is this our business" by parsing was fail-OPEN: all three
// below are rejected by mime.ParseMediaType, so the envelope was forwarded
// intact and the fail-closed branch written for them was unreachable. Lenient
// upstream parsers read parts out of all three.
func TestUnparseableMultipartContentTypeStillReachesTheGuard(t *testing.T) {
	c := &Ctrl{}
	body, _ := buildMultipart(t, func(w *multipart.Writer) {
		if err := w.WriteField("_e2ee", sealedEnvelopeJSON); err != nil {
			t.Fatalf("WriteField: %v", err)
		}
	})

	for _, contentType := range []string{
		`multipart/form-data; boundary=x; boundary=y`,
		`multipart/form-data; boundary=x; =junk`,
		`multipart/form-data; boundary="x`,
		`MULTIPART/FORM-DATA; boundary=x; boundary=y`,
	} {
		t.Run(contentType, func(t *testing.T) {
			if sealed, _ := c.IsSealedRequest(contentType, body); !sealed {
				t.Error("a Content-Type that claims multipart must not skip the guard by failing to parse")
			}
			if _, err := c.MaybeUnsealRequest(ginCtxWithContentType(contentType), body); err == nil {
				t.Fatal("must refuse rather than forward")
			}
		})
	}
}

// A part may declare Content-Disposition TWICE — innocuous first, marker second
// — and Header.Get answers with the first, so the part enumerated cleanly, named
// nothing reserved, and was forwarded with the envelope in it. No parse error, so
// the fail-closed heuristic never ran either. Which value a downstream parser
// honours is not the question: the body declares the name, so the guard cannot
// show it carries no envelope.
func TestMultipartWithADuplicateDispositionIsRefused(t *testing.T) {
	c := &Ctrl{}
	body := []byte("--x\r\n" +
		"Content-Disposition: form-data; name=\"file\"\r\n" +
		"Content-Disposition: form-data; name=\"_e2ee\"\r\n\r\n" +
		sealedEnvelopeJSON + "\r\n--x--\r\n")
	const contentType = `multipart/form-data; boundary=x`

	if sealed, _ := c.IsSealedRequest(contentType, body); !sealed {
		t.Error("a part declaring the marker in any of its dispositions must be seen")
	}
	if _, err := c.MaybeUnsealRequest(ginCtxWithContentType(contentType), body); err == nil {
		t.Fatal("must refuse rather than forward")
	}
}

// The mismatched-boundary case, which exists to pin `err == io.EOF` against a
// change to errors.Is. mime/multipart reports a clean end of parts as a bare
// io.EOF but WRAPS the failure to ever find the declared boundary, so errors.Is
// matches both — and reading this body as a clean end forwards an envelope
// nobody enumerated. Measured:
//
//	clean end                    -> "EOF"                     == io.EOF
//	boundary never found in body -> "multipart: NextPart: EOF" != io.EOF
func TestMismatchedBoundaryMentioningTheMarkerIsRefused(t *testing.T) {
	c := &Ctrl{}
	body := []byte("--x\r\nContent-Disposition: form-data; name=\"_e2ee\"\r\n\r\n" +
		sealedEnvelopeJSON + "\r\n--x--\r\n")
	const contentType = `multipart/form-data; boundary=WRONGBOUND`

	if sealed, _ := c.IsSealedRequest(contentType, body); !sealed {
		t.Error("a body whose parts were never enumerated must be treated as carrying an envelope")
	}
	if _, err := c.MaybeUnsealRequest(ginCtxWithContentType(contentType), body); err == nil {
		t.Fatal("must refuse rather than forward")
	}
}

// The two failures compose, and the composition is the case the pre-filter has
// to fold case for. When the boundary cannot be read, no part can be enumerated,
// so the exact check is unavailable and the fail-closed branch decides on the raw
// bytes alone — and those bytes spell neither the marker (it is percent-encoded)
// nor the lowercase `name*` (the parameter is uppercase). Only the folded scan
// sees anything here. Written after a mutation of containsFold back to
// bytes.Contains left the whole suite green.
func TestUnparseableBoundaryWithAnUppercaseEncodedNameIsRefused(t *testing.T) {
	c := &Ctrl{}
	body := []byte("--x\r\nContent-Disposition: form-data; NAME*=utf-8''%5Fe2ee\r\n\r\n" +
		sealedEnvelopeJSON + "\r\n--x--\r\n")
	const contentType = `multipart/form-data; boundary=x; boundary=y`

	if bytes.Contains(body, []byte("_e2ee")) || bytes.Contains(body, []byte("name*")) {
		t.Fatal("fixture no longer tests the case-folded path")
	}

	if sealed, _ := c.IsSealedRequest(contentType, body); !sealed {
		t.Error("a body that cannot be shown NOT to carry an envelope must be treated as one")
	}
	if _, err := c.MaybeUnsealRequest(ginCtxWithContentType(contentType), body); err == nil {
		t.Fatal("must refuse rather than forward")
	}
}

// The complement, so the branch above is not simply refusing everything it
// cannot parse: an unparseable multipart Content-Type over a body that could not
// be naming the marker is still forwarded.
func TestUnparseableContentTypeWithoutTheMarkerIsStillForwarded(t *testing.T) {
	c := &Ctrl{}
	body, _ := buildMultipart(t, func(w *multipart.Writer) {
		if err := w.WriteField("prompt", "an ordinary transcription"); err != nil {
			t.Fatalf("WriteField: %v", err)
		}
	})
	const contentType = `multipart/form-data; boundary=x; boundary=y`

	if sealed, _ := c.IsSealedRequest(contentType, body); sealed {
		t.Error("a body that could not be naming the marker is not a sealed request")
	}
	if _, err := c.MaybeUnsealRequest(ginCtxWithContentType(contentType), body); err != nil {
		t.Fatalf("must be forwarded, got %v", err)
	}
}

// Any multipart SUBTYPE can carry parts, so the check cannot be scoped to
// form-data: matching only that left the hole reachable by changing one word of
// the Content-Type.
func TestNonFormDataMultipartSubtypeIsStillChecked(t *testing.T) {
	c := &Ctrl{}
	body, contentType := buildMultipart(t, func(w *multipart.Writer) {
		if err := w.WriteField("_e2ee", sealedEnvelopeJSON); err != nil {
			t.Fatalf("WriteField: %v", err)
		}
	})
	mixed := strings.Replace(contentType, "multipart/form-data", "multipart/mixed", 1)
	if mixed == contentType {
		t.Fatal("fixture did not change the subtype")
	}

	if sealed, _ := c.IsSealedRequest(mixed, body); !sealed {
		t.Error("a multipart/mixed body carrying the envelope must be seen too")
	}
	if _, err := c.MaybeUnsealRequest(ginCtxWithContentType(mixed), body); err == nil {
		t.Fatal("must refuse rather than forward")
	}
}

// The pre-filter keeps the fail-closed branches narrow, and this is the case
// that proves it: a body that is not valid multipart at all, with a multipart
// Content-Type, and no mention of the marker in any form. The broker forwarded
// such requests before this change and must keep doing so — refusing them would
// be a behaviour change for requests that have nothing to do with sealing.
func TestMalformedMultipartWithoutTheMarkerIsStillForwarded(t *testing.T) {
	c := &Ctrl{}
	body := []byte(`{"prompt":"a json body sent with a multipart content type"}`)
	const contentType = "multipart/form-data; boundary=abc123"

	if sealed, _ := c.IsSealedRequest(contentType, body); sealed {
		t.Error("a malformed body that cannot be naming the marker is not a sealed request")
	}
	got, err := c.MaybeUnsealRequest(ginCtxWithContentType(contentType), body)
	if err != nil {
		t.Fatalf("must be forwarded, got %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Error("the body must be forwarded unchanged")
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

			if sealed, _ := c.IsSealedRequest(contentType, body); sealed {
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

	if sealed, _ := c.IsSealedRequest(contentType, body); sealed {
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
			if sealed, _ := c.IsSealedRequest(tt.contentType, []byte(tt.body)); !sealed {
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

	if sealed, _ := c.IsSealedRequest(contentType, []byte(body)); sealed {
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
	if sealed, _ := c.IsSealedRequest("application/json", body); !sealed {
		t.Error("a JSON envelope is still a sealed request")
	}
}

// mentionsEncodedName gates the fail-closed branches, so a false NEGATIVE leaks
// an envelope and a false POSITIVE refuses an innocent malformed request. Both
// directions are pinned by differential comparison against the obvious-but-slow
// implementation it replaced, swept over every byte value in every position.
//
// This is not test-for-its-own-sake: the first hand-rolled fold used the usual
// `b|0x20` trick, which maps '\n' (0x0A) onto '*' (0x2A) and so reported
// "name\n" as a mention. The sweep is what found it.
func TestMentionsEncodedName(t *testing.T) {
	// The implementation being replaced: walk every offset, fold 5 bytes.
	walk := func(body []byte) bool {
		needle := []byte("name*")
		for i := 0; i+len(needle) <= len(body); i++ {
			if bytes.EqualFold(body[i:i+len(needle)], needle) {
				return true
			}
		}
		return false
	}

	for pos := 0; pos < len("name*"); pos++ {
		for b := 0; b < 256; b++ {
			body := []byte("name*")
			body[pos] = byte(b)
			if got, want := mentionsEncodedName(body), walk(body); got != want {
				t.Errorf("byte %#02x at position %d: got %v, walk says %v", b, pos, got, want)
			}
		}
	}

	// Shapes the single-position sweep cannot reach: restart-after-partial-match,
	// the spellings the parser actually resolves, and the near-misses.
	for _, s := range []string{
		"", "n", "na", "nam", "name", "name*", "NAME*", "Name*0=", "nAmE*",
		"nname*", "nnnname*", "namname*", "namenam*", "name\n", "name+",
		`form-data; name*=utf-8''%5Fe2ee`, `form-data; NAME*0="_e2"; Name*1="ee"`,
		"an ordinary prompt about names and stars *", "\x00\xff\xfename*",
	} {
		if got, want := mentionsEncodedName([]byte(s)), walk([]byte(s)); got != want {
			t.Errorf("%q: got %v, walk says %v", s, got, want)
		}
	}
}

// A clean parse is not an answer. mime.ParseMediaType drops an RFC 2231
// parameter whose charset it cannot decode and reports NO error, so `name` comes
// back "" — or partially decoded — on a part that plainly declares one. With no
// error, neither fail-closed branch runs either.
//
// Only the charset token separates these from TestMultipartWithAnEncodedE2EEPartNameIsRefused,
// which passes: the guard was closed for utf-8 and open one token over. Lenient
// parsers (Python's email, Node's busboy via iconv-lite) do decode latin-1 and
// windows-1252 and resolve the name to the marker.
func TestMultipartWithAnUndecodableEncodedNameIsRefused(t *testing.T) {
	tests := []struct {
		name        string
		disposition string
		parsedName  string // what mime.ParseMediaType yields, measured
	}{
		{"latin-1 charset", `form-data; name*=iso-8859-1''%5Fe2ee`, ""},
		// The parameter name folds too, so the spelling axis from round 2 has to
		// be covered here as well, end to end and not only in TestDeclaresEncodedName.
		{"uppercase attribute, latin-1 charset", `form-data; NAME*=iso-8859-1''%5Fe2ee`, ""},
		{"windows-1252 charset", `form-data; name*=windows-1252''%5Fe2ee`, ""},
		{"empty charset", `form-data; name*=''%5Fe2ee`, ""},
		{"invalid percent-escape", `form-data; name*=utf-8''%ZZe2ee`, ""},
		{"not form-data either", `attachment; name*=iso-8859-1''%5Fe2ee`, ""},
		// The sharpest one: the parser keeps the segment it can decode and drops
		// the one it cannot, so the name is neither absent nor right. "Refuse
		// when the name came back empty" would miss exactly this row.
		{"partially decodable continuation", `form-data; name*0*=iso-8859-1''%5Fe2; name*1*=ee`, "ee"},
	}

	c := &Ctrl{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Pin the parser behaviour the case rests on, so this test says why
			// it exists if a future Go release starts decoding these.
			_, params, err := mime.ParseMediaType(tt.disposition)
			if err != nil {
				t.Fatalf("fixture assumes a CLEAN parse, got %v", err)
			}
			if params["name"] != tt.parsedName {
				t.Fatalf("parser now yields name=%q, not %q: re-check whether this case is still a bypass",
					params["name"], tt.parsedName)
			}

			body, contentType := buildMultipart(t, func(w *multipart.Writer) {
				rawPart(t, w, tt.disposition, sealedEnvelopeJSON)
			})
			if bytes.Contains(body, []byte("_e2ee")) {
				t.Fatal("fixture no longer tests the encoded case: the literal marker is present")
			}

			if sealed, _ := c.IsSealedRequest(contentType, body); !sealed {
				t.Error("a name the parser refused to decode cannot be ruled out; the async route would enqueue this")
			}
			if _, err := c.MaybeUnsealRequest(ginCtxWithContentType(contentType), body); err == nil {
				t.Fatal("must refuse rather than forward")
			}
		})
	}
}

// The complement, and the reason declaresEncodedName tests a token boundary
// rather than a substring: `filename*=` CONTAINS "name*", and encoding a
// filename is both legitimate and common. Refusing these would break ordinary
// uploads.
func TestMultipartWithAnEncodedFilenameIsForwarded(t *testing.T) {
	c := &Ctrl{}
	for _, disposition := range []string{
		`form-data; name="file"; filename*=iso-8859-1''caf%E9.wav`,
		`form-data; name="file"; filename*0*=utf-8''caf%C3%A9; filename*1=".wav"`,
		`form-data; name="file"; FILENAME*=iso-8859-1''caf%E9.wav`,
	} {
		t.Run(disposition, func(t *testing.T) {
			body, contentType := buildMultipart(t, func(w *multipart.Writer) {
				rawPart(t, w, disposition, "RIFF....audio....")
			})
			if sealed, _ := c.IsSealedRequest(contentType, body); sealed {
				t.Error("an encoded FILENAME is not an encoded field name")
			}
			got, err := c.MaybeUnsealRequest(ginCtxWithContentType(contentType), body)
			if err != nil {
				t.Fatalf("must be forwarded, got %v", err)
			}
			if !bytes.Equal(got, body) {
				t.Error("the body must be forwarded unchanged")
			}
		})
	}
}

// The part-level twin of TestMalformedMultipartWithoutTheMarkerIsStillForwarded.
// An unparseable Content-Disposition used to refuse unconditionally, so an
// unescaped quote in a filename — sloppy but real, and forwarded by the broker
// before this check existed — came back as an e2ee error. The branch is gated
// now; this pins the gate.
func TestUnparseablePartDispositionWithoutTheMarkerIsForwarded(t *testing.T) {
	c := &Ctrl{}
	const disposition = `form-data; name="notes"; filename="my"file.txt"`
	if _, _, err := mime.ParseMediaType(disposition); err == nil {
		t.Fatal("fixture assumes this disposition does NOT parse")
	}

	body := []byte("--x\r\nContent-Disposition: " + disposition + "\r\n\r\nhello\r\n--x--\r\n")
	const contentType = `multipart/form-data; boundary=x`
	if couldNameE2EEPart(body) {
		t.Fatal("fixture must not mention the marker in any form")
	}

	if sealed, _ := c.IsSealedRequest(contentType, body); sealed {
		t.Error("a malformed disposition that cannot be naming the marker is not a sealed request")
	}
	got, err := c.MaybeUnsealRequest(ginCtxWithContentType(contentType), body)
	if err != nil {
		t.Fatalf("must be forwarded, got %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Error("the body must be forwarded unchanged")
	}
}

// And the same shape WITH the marker, so gating the branch did not disarm it.
func TestUnparseablePartDispositionMentioningTheMarkerIsRefused(t *testing.T) {
	c := &Ctrl{}
	body := []byte("--x\r\nContent-Disposition: form-data; name=\"notes\"; filename=\"a\"b.txt\"\r\n\r\n_e2ee\r\n--x--\r\n")
	const contentType = `multipart/form-data; boundary=x`

	if sealed, _ := c.IsSealedRequest(contentType, body); !sealed {
		t.Error("malformed AND mentioning the marker must fail closed")
	}
	if _, err := c.MaybeUnsealRequest(ginCtxWithContentType(contentType), body); err == nil {
		t.Fatal("must refuse rather than forward")
	}
}

// declaresEncodedName decides which dispositions reach the refusal above, so its
// boundary is worth pinning directly: every 2231 form of `name`, none of the
// attributes that merely end in it.
func TestDeclaresEncodedName(t *testing.T) {
	tests := map[string]bool{
		`form-data; name*=utf-8''%5Fe2ee`:                 true,
		`form-data; name*=iso-8859-1''x`:                  true,
		`form-data; NAME*=iso-8859-1''x`:                  true,
		`form-data; name*0*=utf-8''a; name*1*=b`:          true,
		`form-data;name*=x`:                               true, // no space after the semicolon
		`form-data; name*0=a; name*1=b`:                   true,
		`form-data; name*12*=utf-8''a`:                    true,
		`form-data; name="file"`:                          false,
		`form-data; name=_e2ee`:                           false,
		`form-data; name="file"; filename*=iso-8859-1''x`: false,
		`form-data; name="file"; FILENAME*0*=utf-8''x`:    false,
		`form-data; name="file"; x-my-name*=utf-8''x`:     false,
		`attachment`: false,
		``:           false,
		// Inside a quoted VALUE, so not a parameter — the finding that made the
		// scan quote-aware. The third survives a follow-the-match shape check.
		`form-data; name="file"; filename="name*.wav"`:             false,
		`form-data; name="file"; filename="recording (name*).wav"`: false,
		`form-data; name="file"; filename="name*=x.wav"`:           false,
		`form-data; name="name*=x"`:                                false,
		// Starts with "name*" but is none of the three RFC 2231 shapes.
		`form-data; name*foo=x`:  false,
		`form-data; name*`:       false,
		`form-data; namespace=x`: false,
		// No disposition type, so no parameter position: mime.ParseMediaType
		// rejects this outright ("expected slash after first token"), which puts
		// the whole header in the caller's gated fail-closed branch rather than
		// here. TestBareEncodedNameDispositionIsRefused pins that end to end.
		`name*=x`: false,
	}
	for disposition, want := range tests {
		if got := declaresEncodedName(disposition); got != want {
			t.Errorf("%q: got %v, want %v", disposition, got, want)
		}
	}
}

// The mirror image of TestMultipartWithAnEncodedFilenameIsForwarded: a `name*`
// inside a quoted VALUE is not a parameter, and these are ordinary uploads the
// broker forwarded before this PR. Checking only the byte before the match — the
// first attempt at the boundary — read all of them as parameters.
//
// The last row is why the scan tracks quoting rather than inspecting the match's
// neighbours: it satisfies any follow-the-match shape check too.
func TestMultipartWithNameStarInsideAQuotedValueIsForwarded(t *testing.T) {
	c := &Ctrl{}
	for _, disposition := range []string{
		`form-data; name="file"; filename="name*.wav"`,
		`form-data; name="file"; filename="recording (name*).wav"`,
		`form-data; name="file"; filename="my name*is.wav"`,
		`form-data; name="file"; filename="name*=x.wav"`,
		// These two are the rows the QUOTE TRACKING exists for, and the ones the
		// four above turn out not to cover: a `;` inside the quoted filename, so
		// a scan that does not track quoting sees a fresh parameter position and
		// reads the `name*=` after it as an encoded field name. Both parse
		// cleanly. Written after a mutation that disabled the quote tracking left
		// the four above green — they are caught by the parameter-position
		// tracking alone, so they proved less than they looked like they did.
		`form-data; name="file"; filename="a; name*=utf-8''%5Fe2ee.wav"`,
		`form-data; name="file"; filename="report; name*0=x"`,
		// And these are the rows the ESCAPE SKIP inside the quote tracking
		// exists for, which the two above still do not reach: an escaped quote
		// before the `;`, so a scan that treats `\"` as closing the string ends
		// up outside quotes at the semicolon and reads a parameter again. Go
		// unescapes a pair before a tspecial, so both parse cleanly with the
		// whole thing as the filename. Added after a mutation that removed the
		// skip survived everything else here.
		`form-data; name="file"; filename="a\"; name*=utf-8''%5Fe2ee.wav"`,
		`form-data; name="file"; filename="say \"hi\"; name*0=x.wav"`,
	} {
		t.Run(disposition, func(t *testing.T) {
			body, contentType := buildMultipart(t, func(w *multipart.Writer) {
				rawPart(t, w, disposition, "RIFF....audio....")
			})
			if sealed, _ := c.IsSealedRequest(contentType, body); sealed {
				t.Error("a name* inside a quoted value does not declare an encoded field name")
			}
			got, err := c.MaybeUnsealRequest(ginCtxWithContentType(contentType), body)
			if err != nil {
				t.Fatalf("must be forwarded, got %v", err)
			}
			if !bytes.Equal(got, body) {
				t.Error("the body must be forwarded unchanged")
			}
		})
	}
}

// Go unescapes an RFC 2045 quoted pair only where it precedes a tspecial, so the
// name comes back with the backslash still in it and compares unequal to the
// marker — while javax.mail and Ruby's mail read the marker. Nothing in this
// stack demonstrably decodes it that way today, which makes this a smaller claim
// than the charset gap; the comparison costs one call and the alternative is
// trusting every upstream to agree with Go about a backslash.
func TestMultipartWithAQuotedPairInTheNameIsRefused(t *testing.T) {
	c := &Ctrl{}
	tests := []struct {
		name        string
		disposition string
		parsedName  string // what Go yields, measured
	}{
		{"backslash before the underscore", `form-data; name="\_e2ee"`, `\_e2ee`},
		{"backslash mid-name", `form-data; name="\_e2\ee"`, `\_e2\ee`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, params, err := mime.ParseMediaType(tt.disposition)
			if err != nil {
				t.Fatalf("fixture assumes a clean parse, got %v", err)
			}
			if params["name"] != tt.parsedName {
				t.Fatalf("Go now yields name=%q, not %q: re-check whether this is still a divergence",
					params["name"], tt.parsedName)
			}

			body, contentType := buildMultipart(t, func(w *multipart.Writer) {
				rawPart(t, w, tt.disposition, sealedEnvelopeJSON)
			})
			if sealed, _ := c.IsSealedRequest(contentType, body); !sealed {
				t.Error("a conformant parser reads this as the marker, so it cannot be ruled out")
			}
			if _, err := c.MaybeUnsealRequest(ginCtxWithContentType(contentType), body); err == nil {
				t.Fatal("must refuse rather than forward")
			}
		})
	}
}

// And the complement, so the quoted-pair comparison refuses the MARKER rather
// than every backslash. Two rows, and the second is the one that distinguishes
// them: Go unescapes a pair before a tspecial (so `a\"b` keeps no backslash at
// all and any rule would forward it), but LEAVES one before a non-tspecial — so
// `a\b` comes back as `a\b`, and a rule of "refuse any backslash in the name"
// would reject an ordinary field. Only comparing the unescaped value to the
// marker forwards it. Written after that over-broad rule survived as a mutation.
func TestMultipartWithAnUnrelatedQuotedPairIsForwarded(t *testing.T) {
	c := &Ctrl{}
	tests := []struct {
		name        string
		disposition string
		parsedName  string // what Go yields, measured
	}{
		{"pair before a tspecial, which Go unescapes", `form-data; name="a\"b"`, `a"b`},
		{"pair before a non-tspecial, which Go leaves in place", `form-data; name="a\b"`, `a\b`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, params, err := mime.ParseMediaType(tt.disposition)
			if err != nil {
				t.Fatalf("fixture assumes a clean parse, got %v", err)
			}
			if params["name"] != tt.parsedName {
				t.Fatalf("fixture assumes Go yields name=%q, got %q", tt.parsedName, params["name"])
			}

			body, contentType := buildMultipart(t, func(w *multipart.Writer) {
				rawPart(t, w, tt.disposition, "some field value")
			})
			if sealed, _ := c.IsSealedRequest(contentType, body); sealed {
				t.Error("a quoted pair in a name that is not the marker is not a sealed request")
			}
			if _, err := c.MaybeUnsealRequest(ginCtxWithContentType(contentType), body); err != nil {
				t.Fatalf("must be forwarded, got %v", err)
			}
		})
	}
}

// A disposition with no type at all — just the encoded parameter. It has no
// parameter position for declaresEncodedName to find, because mime.ParseMediaType
// rejects the header outright ("expected slash after first token"), so the
// refusal has to come from the gated unparseable-disposition branch instead. Both
// halves of that are worth pinning: the predicate says false, the guard still
// refuses.
func TestBareEncodedNameDispositionIsRefused(t *testing.T) {
	c := &Ctrl{}
	const disposition = `name*=utf-8''%5Fe2ee`
	if _, _, err := mime.ParseMediaType(disposition); err == nil {
		t.Fatal("fixture assumes this disposition does NOT parse")
	}
	if declaresEncodedName(disposition) {
		t.Error("a header with no disposition type has no parameter position")
	}

	body := []byte("--x\r\nContent-Disposition: " + disposition + "\r\n\r\n" + sealedEnvelopeJSON + "\r\n--x--\r\n")
	const contentType = `multipart/form-data; boundary=x`
	if bytes.Contains(body, []byte("_e2ee")) {
		t.Fatal("fixture no longer tests the encoded case")
	}

	if sealed, _ := c.IsSealedRequest(contentType, body); !sealed {
		t.Error("the marker cannot be ruled out, so this must fail closed via the unparseable-disposition branch")
	}
	if _, err := c.MaybeUnsealRequest(ginCtxWithContentType(contentType), body); err == nil {
		t.Fatal("must refuse rather than forward")
	}
}

// The gate's answer depends on nothing but the body, so it must be computed at
// most once per call — not once per part. The Content-Disposition branch
// `continue`s on a false result rather than returning, so calling it per part
// cost parts x len(body):
//
//	  1 malformed part  / 32 MiB ->    97 ms
//	 50 malformed parts / 32 MiB -> 4 107 ms
//	200 malformed parts / 32 MiB -> 16 370 ms   (~82 ms each, perfectly linear)
//
// A malformed part is 33 bytes, so 1,016,800 of them fit inside the 32 MiB
// request limit — and the sync proxy unseals before ValidateSession, so no
// session is needed to spend that.
//
// Nothing else in this file would have caught it: every other case uses a
// handful of parts, where the repetition is invisible. So this asserts the
// SHAPE — cost must not grow with part count — rather than an absolute time,
// which would be flaky on shared CI. The ratio bound is loose (8x for a 200x
// increase in parts) because it only has to fail the quadratic version, which
// exceeded it by two orders of magnitude.
func TestGateIsNotRecomputedPerPart(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates two multi-megabyte bodies")
	}

	// Parts whose Content-Disposition does not parse, so each one reaches the
	// gated fail-closed branch, plus padding so the scan has a body to walk.
	// No marker in any form: the gate must return false, which is what makes the
	// branch `continue` instead of returning.
	build := func(parts, size int) []byte {
		var b bytes.Buffer
		for i := 0; i < parts; i++ {
			b.WriteString("--x\r\nContent-Disposition: \"\r\n\r\n\r\n")
		}
		b.WriteString("--x\r\nContent-Disposition: form-data; name=\"pad\"\r\n\r\n")
		if rem := size - b.Len() - 16; rem > 0 {
			b.Write(bytes.Repeat([]byte("A"), rem))
		}
		b.WriteString("\r\n--x--\r\n")
		return b.Bytes()
	}

	const contentType = `multipart/form-data; boundary=x`
	const size = 8 << 20

	few, many := build(1, size), build(200, size)
	for _, body := range [][]byte{few, many} {
		if couldNameE2EEPart(body) {
			t.Fatal("fixture must not trip the gate, or the branch returns instead of continuing")
		}
		if carries, _ := multipartCarriesE2EEPart(contentType, body); carries {
			t.Fatal("fixture must not be refused, or the loop exits early")
		}
	}

	measure := func(body []byte) time.Duration {
		best := time.Duration(1 << 62)
		for i := 0; i < 3; i++ { // best-of-3, so a scheduling hiccup does not fail the build
			start := time.Now()
			multipartCarriesE2EEPart(contentType, body)
			if d := time.Since(start); d < best {
				best = d
			}
		}
		return best
	}

	one, twoHundred := measure(few), measure(many)
	if one <= 0 {
		t.Skip("timer resolution too coarse to compare")
	}
	if ratio := float64(twoHundred) / float64(one); ratio > 8 {
		t.Errorf("200 malformed parts cost %v against %v for one (%.0fx): the whole-body gate is being recomputed per part",
			twoHundred, one, ratio)
	}
}

// mime/multipart does not descend, so a part that is itself multipart hides its
// own parts from the enumeration: the inner part named `_e2ee` parses cleanly,
// raises no error, and used to be forwarded with the literal marker in the body.
//
// Closed without recursion, because "parts that cannot be enumerated from here"
// is already the fail-closed condition. The second row is what keeps that from
// being a blanket refusal of nested bodies.
func TestNestedMultipartPartIsRefused(t *testing.T) {
	c := &Ctrl{}
	nested := func(innerDisposition, innerBody string) []byte {
		return []byte("--OUTER\r\n" +
			"Content-Disposition: form-data; name=\"wrapper\"\r\n" +
			"Content-Type: multipart/mixed; boundary=INNER\r\n\r\n" +
			"--INNER\r\nContent-Disposition: " + innerDisposition + "\r\n\r\n" +
			innerBody + "\r\n--INNER--\r\n" +
			"--OUTER--\r\n")
	}
	const contentType = `multipart/form-data; boundary=OUTER`

	t.Run("inner part named the marker", func(t *testing.T) {
		body := nested(`form-data; name="_e2ee"`, sealedEnvelopeJSON)
		if sealed, _ := c.IsSealedRequest(contentType, body); !sealed {
			t.Error("an envelope one nesting level down cannot be ruled out")
		}
		if _, err := c.MaybeUnsealRequest(ginCtxWithContentType(contentType), body); err == nil {
			t.Fatal("must refuse rather than forward")
		}
	})

	// Nesting alone IS grounds for refusal, and that changed deliberately. The
	// branch used to be gated on couldNameE2EEPart, which made it evadable by the
	// one spelling that heuristic provably cannot see: an inner part named
	// `=?utf-8?B?X2UyZWU=?=` was forwarded, because the gate looks for the
	// literal marker or `name*` and finds neither. A heuristic over spellings
	// cannot narrow a branch whose premise is that the names were never read.
	//
	// So the cost of the un-gating is asserted here rather than left implicit: a
	// nested body mentioning nothing reserved is now refused too. Nothing on
	// /audio/transcriptions, /images/edits or /v1/async/images/edits sends a
	// nested multipart body, so that costs no real traffic.
	t.Run("inner part mentioning nothing is refused too", func(t *testing.T) {
		body := nested(`form-data; name="notes"`, "an ordinary nested field")
		if couldNameE2EEPart(body) {
			t.Fatal("fixture must not mention the marker in any form")
		}
		if sealed, _ := c.IsSealedRequest(contentType, body); !sealed {
			t.Error("nesting hides its own part names, so it cannot be cleared")
		}
		if _, err := c.MaybeUnsealRequest(ginCtxWithContentType(contentType), body); err == nil {
			t.Fatal("must refuse rather than forward")
		}
	})

	// The evasion the un-gating closes: an RFC 2047 encoded word one level down.
	// The gate is false on this body — no literal marker, no `name*` — so the
	// gated version forwarded it.
	t.Run("inner part named by an encoded word", func(t *testing.T) {
		body := nested(`form-data; name="=?utf-8?B?X2UyZWU=?="`, sealedEnvelopeJSON)
		if couldNameE2EEPart(body) {
			t.Fatal("fixture must not trip the gate, or it is not testing the evasion")
		}
		if sealed, _ := c.IsSealedRequest(contentType, body); !sealed {
			t.Error("an encoded-word name one level down must not be forwarded")
		}
		if _, err := c.MaybeUnsealRequest(ginCtxWithContentType(contentType), body); err == nil {
			t.Fatal("must refuse rather than forward")
		}
	})
}

// The enumeration is unbounded in PART COUNT, and memoizing the gate did not
// bound it — the gate is never consulted on a well-formed body, so nothing
// stopped the loop. A minimum well-formed part is 9 bytes, so 3,728,269 fit in
// the 32 MiB limit. Measured before the cap, both bodies at the limit:
//
//	one 32 MiB part (a legitimate upload) ->   16 ms
//	3,728,269 empty parts                 -> 2230 ms
//
// The assertion is on the REASON, not on elapsed time, and that is a correction:
// a timing version of this test passed locally and failed in CI, because CI runs
// integration with -covermode=atomic -coverpkg=./..., a body past the cap pays
// one couldName() scan, and that scan is a byte-at-a-time loop INSIDE the
// instrumented package. So the ratio measured coverage instrumentation (50x
// locally under those flags) rather than enumeration. Reproduced with the CI
// flags before rewriting.
//
// The reason string pins the cap's MAGNITUDE, which is what actually matters and
// which no timing bound establishes: a marker placed past the cap must be
// refused for "declares more than N parts" and not found by name. Raise the cap
// above that part index and the reason changes, so the test fails.
func TestEnumerationIsBoundedByPartCount(t *testing.T) {
	const contentType = `multipart/form-data; boundary=B`
	const unit = "--B\r\n\r\n\r\n" // 9 bytes: the minimum well-formed part

	// n empty parts, then one named part, then the terminator.
	build := func(n int, name string) []byte {
		var b bytes.Buffer
		for i := 0; i < n; i++ {
			b.WriteString(unit)
		}
		b.WriteString("--B\r\nContent-Disposition: form-data; name=\"" + name + "\"\r\n\r\n" +
			sealedEnvelopeJSON + "\r\n--B--\r\n")
		return b.Bytes()
	}

	// Both indices are FIXED, not derived from maxHeadersExamined, and that is
	// the second time this test needed the correction: a version that built
	// `maxHeadersExamined+1000` parts moved with the constant, so raising the cap
	// to 20000 left it green and raising it to 1<<40 made it try to allocate a
	// trillion parts and hang. Together these two pin 10 <= cap < 8192 — which is
	// the claim worth holding, since real transcription and image-edit forms have
	// well under a hundred parts.
	const withinIndex, pastIndex = 10, 8192

	// Within the cap, the marker is found by NAME: the loop reaches it.
	within := build(withinIndex, e2eeBodyMarker)
	carries, why := multipartCarriesE2EEPart(contentType, within)
	if !carries {
		t.Fatal("a marker within the cap must be found")
	}
	if !strings.Contains(why, "is named") {
		t.Errorf("within the cap the marker should be found by name, got %q", why)
	}

	// Past the cap, the same body is refused for a DIFFERENT reason: the loop
	// stopped before reaching that part, so the marker was never seen and the
	// refusal comes from the gate. If the cap were raised past this index, the
	// name would be found instead and this assertion fails.
	past := build(pastIndex, e2eeBodyMarker)
	carries, why = multipartCarriesE2EEPart(contentType, past)
	if !carries {
		t.Fatal("past the cap, a body that could be naming the marker must fail closed")
	}
	if !strings.Contains(why, "more than") {
		t.Errorf("past the cap the refusal should cite the part count, not the name, got %q", why)
	}

	// Past the budget the refusal is UNGATED, and that changed deliberately.
	// Gating it made it evadable by the one spelling couldNameE2EEPart provably
	// cannot see: a part named `=?utf-8?B?X2eyZWU=?=` placed past the budget was
	// forwarded, because the gate looks for the literal marker or `name*` and
	// finds neither. A heuristic over spellings cannot narrow a branch whose
	// premise is that the name was never read.
	//
	// So the cost is asserted rather than left implicit: a body past the budget
	// with nothing reserved in it is refused too. No client on these endpoints
	// sends thousands of parts, so that costs no real traffic.
	quiet := build(pastIndex, "ordinary")
	if couldNameE2EEPart(quiet) {
		t.Fatal("fixture must not trip the gate, or it is not testing the un-gating")
	}
	if carries, why := multipartCarriesE2EEPart(contentType, quiet); !carries {
		t.Error("past the budget, a body whose parts were never enumerated cannot be cleared")
	} else if !strings.Contains(why, "more than") {
		t.Errorf("the refusal should cite the budget, got %q", why)
	}

	// The evasion the un-gating closes.
	evasive := build(pastIndex, `=?utf-8?B?X2UyZWU=?=`)
	if couldNameE2EEPart(evasive) {
		t.Fatal("fixture must not trip the gate, or it is not testing the evasion")
	}
	if carries, _ := multipartCarriesE2EEPart(contentType, evasive); !carries {
		t.Error("an encoded-word name past the budget must not be forwarded")
	}

	// The budget covers HEADERS, not just parts: the inner loop over the
	// dispositions a part declares was separately unbounded, and each one pays a
	// mime.ParseMediaType plus a declaresEncodedName walk. Measured at 32 MiB
	// before the shared budget: 35x a legitimate one-part body. One part with
	// more dispositions than the budget must be refused on the same branch.
	var manyDispositions bytes.Buffer
	manyDispositions.WriteString("--B\r\n")
	for i := 0; i <= maxHeadersExamined; i++ {
		manyDispositions.WriteString("Content-Disposition: form-data; name=\"f\"\r\n")
	}
	manyDispositions.WriteString("\r\n\r\n--B--\r\n")
	if couldNameE2EEPart(manyDispositions.Bytes()) {
		t.Fatal("fixture must not trip the gate")
	}
	if carries, why := multipartCarriesE2EEPart(contentType, manyDispositions.Bytes()); !carries {
		t.Error("one part declaring more dispositions than the budget must hit the same branch")
	} else if !strings.Contains(why, "more than") {
		t.Errorf("the refusal should cite the budget, got %q", why)
	}

	// The cost claim, asserted only where instrumentation cancels: two bodies of
	// the SAME SIZE, both past the cap, differing only in how many parts they
	// split into. Both pay exactly one gate scan, so the difference is the
	// enumeration alone. Uncapped, the second enumerates ~450x more parts.
	if testing.Short() {
		return
	}
	const requestLimit = 32 << 20
	const terminator = "--B--\r\n"

	// As many minimum-size parts as fit under the limit.
	var more bytes.Buffer
	for more.Len()+len(unit)+len(terminator) <= requestLimit {
		more.WriteString(unit)
	}
	more.WriteString(terminator)

	// The same NUMBER OF BYTES, split into far fewer parts: pastIndex of them plus one
	// padded part. Its length is derived from `more` rather than computed
	// independently — a first version computed both against the limit and they
	// landed 39 bytes apart, which the guard below caught.
	var fewer bytes.Buffer
	for i := 0; i < pastIndex; i++ {
		fewer.WriteString(unit)
	}
	const padHeader = "--B\r\nContent-Disposition: form-data; name=\"pad\"\r\n\r\n"
	fewer.WriteString(padHeader)
	pad := more.Len() - fewer.Len() - len("\r\n") - len(terminator)
	if pad < 0 {
		t.Fatalf("the padded fixture already exceeds the other: %d vs %d", fewer.Len(), more.Len())
	}
	fewer.Write(bytes.Repeat([]byte("A"), pad))
	fewer.WriteString("\r\n" + terminator)

	if fewer.Len() != more.Len() {
		t.Fatalf("fixtures must be the same size for instrumentation to cancel: %d vs %d", fewer.Len(), more.Len())
	}

	measure := func(body []byte) time.Duration {
		best := time.Duration(1 << 62)
		for i := 0; i < 3; i++ {
			start := time.Now()
			multipartCarriesE2EEPart(contentType, body)
			if d := time.Since(start); d < best {
				best = d
			}
		}
		return best
	}
	a, b := measure(fewer.Bytes()), measure(more.Bytes())
	if a <= 0 {
		t.Skip("timer resolution too coarse to compare")
	}
	if ratio := float64(b) / float64(a); ratio > 4 {
		t.Errorf("%d parts cost %v against %v for %d parts at the same body size (%.1fx): the enumeration is not bounded by part count",
			more.Len()/len(unit), b, pastIndex, a, ratio)
	}
}

// An RFC 2047 encoded word: Go hands the name back verbatim with no error, and
// the raw bytes spell neither the marker nor `name*`, so the gate is false too —
// the same shape as the RFC 2231 charset gap and the quoted pair, one encoding
// over. Refused without decoding the word.
func TestMultipartWithAnEncodedWordNameIsRefused(t *testing.T) {
	c := &Ctrl{}
	tests := []struct {
		name        string
		disposition string
		parsedName  string // what Go yields, measured
	}{
		{"base64 encoded word", `form-data; name="=?utf-8?B?X2UyZWU=?="`, `=?utf-8?B?X2UyZWU=?=`},
		{"quoted-printable, latin-1", `form-data; name="=?iso-8859-1?Q?=5Fe2ee?="`, `=?iso-8859-1?Q?=5Fe2ee?=`},
		// Refused too: what the word decodes to is the question this guard
		// declines to answer for itself, here as for RFC 2231.
		{"an encoded word that is not the marker", `form-data; name="=?utf-8?Q?ordinary?="`, `=?utf-8?Q?ordinary?=`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, params, err := mime.ParseMediaType(tt.disposition)
			if err != nil {
				t.Fatalf("fixture assumes a clean parse, got %v", err)
			}
			if params["name"] != tt.parsedName {
				t.Fatalf("Go now yields name=%q, not %q: re-check whether this is still undecoded",
					params["name"], tt.parsedName)
			}

			body, contentType := buildMultipart(t, func(w *multipart.Writer) {
				rawPart(t, w, tt.disposition, sealedEnvelopeJSON)
			})
			if couldNameE2EEPart(body) {
				t.Fatal("fixture must not trip the gate, or the branch under test is not what refuses it")
			}

			if sealed, _ := c.IsSealedRequest(contentType, body); !sealed {
				t.Error("a name other parsers decode cannot be ruled out")
			}
			if _, err := c.MaybeUnsealRequest(ginCtxWithContentType(contentType), body); err == nil {
				t.Fatal("must refuse rather than forward")
			}
		})
	}
}

// isEncodedWord decides which names reach that refusal, so its edges are pinned
// directly: ordinary names, and the near-misses that are not encoded words.
func TestIsEncodedWord(t *testing.T) {
	tests := map[string]bool{
		`=?utf-8?B?X2UyZWU=?=`: true,
		`=?a?b?c?=`:            true,
		`=??=`:                 false, // too short to carry a charset
		`=?utf-8?B?x`:          false, // no closing delimiter
		`utf-8?B?x?=`:          false, // no opening delimiter
		`_e2ee`:                false,
		`file`:                 false,
		``:                     false,
		`=?=`:                  false,
	}
	for value, want := range tests {
		if got := isEncodedWord(value); got != want {
			t.Errorf("%q: got %v, want %v", value, got, want)
		}
	}
}
