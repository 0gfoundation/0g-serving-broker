package ctrl

import (
	"github.com/prometheus/client_golang/prometheus/testutil"

	"bytes"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
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
// carriesE2EEPart and isSealedRequestBool are the bool-shaped answers the tests
// below were written against. The production signatures return a SealedVerdict
// because the exact and structural refusals earn different treatment; these
// collapse it again for the many assertions that only ask "refused or not".
// TestStructuralRefusalsAreGated is where the distinction is asserted.
func carriesE2EEPart(contentType string, reqBody []byte) (bool, string) {
	verdict, _, why := multipartCarriesE2EEPart(contentType, reqBody)
	return verdict != NotSealed, why
}

func isSealedRequestBool(contentType string, reqBody []byte) (bool, string) {
	verdict, _, why := IsSealedRequest(contentType, reqBody)
	return verdict != NotSealed, why
}

// strictCtrl is a Ctrl with the structural SPEC §5.3.1 refusals ON.
//
// Every test below that asserts a refusal for a body whose part names could not
// be READ — a nested part, a form past the budget, an unrecoverable boundary, an
// enumeration that stopped early, an unparseable part header — is asserting the
// strict behaviour, because those verdicts are governed by
// cfg.e2eeStrictMultipart and default to forwarding-and-counting. The EXACT
// refusals (a name resolving to the marker) need none of this and hold either
// way; TestStructuralRefusalsAreGated pins the difference.
func strictCtrl() *Ctrl { return &Ctrl{e2eeStrictMultipart: true} }

// strictFixture is newE2EEFixture's Ctrl with the same flag set, for the tests
// that need a real enclave key as well.
func strictFixture(t *testing.T) *Ctrl {
	t.Helper()
	c := newE2EEFixture(t).c
	c.e2eeStrictMultipart = true
	return c
}

// refuseAsyncBool is the old bool-shaped answer, kept for the tests that only
// care whether the async routes refuse at all. The verdict itself is what
// async.go switches on, and TestAsyncMessagesDifferPerVerdict covers that.
func (c *Ctrl) refuseAsyncBool(contentType string, reqBody []byte) (bool, string) {
	verdict, why := c.RefuseAsync(contentType, reqBody)
	return verdict != NotSealed, why
}

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

	c := strictCtrl()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, contentType := buildMultipart(t, tt.parts)

			if sealed, _ := c.refuseAsyncBool(contentType, body); !sealed {
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

	c := strictCtrl()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, contentType := buildMultipart(t, func(w *multipart.Writer) {
				rawPart(t, w, tt.disposition, sealedEnvelopeJSON)
			})
			if bytes.Contains(body, []byte("_e2ee")) {
				t.Fatal("fixture no longer tests the encoded case: the literal marker is present")
			}

			if sealed, _ := c.refuseAsyncBool(contentType, body); !sealed {
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

	c := strictCtrl()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, contentType := buildMultipart(t, func(w *multipart.Writer) {
				rawPart(t, w, tt.disposition, sealedEnvelopeJSON)
			})
			if bytes.Contains(body, []byte("_e2ee")) || bytes.Contains(body, []byte("name*")) {
				t.Fatal("fixture no longer tests the case-folded path")
			}

			if sealed, _ := c.refuseAsyncBool(contentType, body); !sealed {
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
	c := strictCtrl()
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
			if sealed, _ := c.refuseAsyncBool(contentType, body); !sealed {
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
	c := strictCtrl()
	body := []byte("--x\r\n" +
		"Content-Disposition: form-data; name=\"file\"\r\n" +
		"Content-Disposition: form-data; name=\"_e2ee\"\r\n\r\n" +
		sealedEnvelopeJSON + "\r\n--x--\r\n")
	const contentType = `multipart/form-data; boundary=x`

	if sealed, _ := c.refuseAsyncBool(contentType, body); !sealed {
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
	c := strictCtrl()
	body := []byte("--x\r\nContent-Disposition: form-data; name=\"_e2ee\"\r\n\r\n" +
		sealedEnvelopeJSON + "\r\n--x--\r\n")
	const contentType = `multipart/form-data; boundary=WRONGBOUND`

	if sealed, _ := c.refuseAsyncBool(contentType, body); !sealed {
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
	c := strictCtrl()
	body := []byte("--x\r\nContent-Disposition: form-data; NAME*=utf-8''%5Fe2ee\r\n\r\n" +
		sealedEnvelopeJSON + "\r\n--x--\r\n")
	const contentType = `multipart/form-data; boundary=x; boundary=y`

	if bytes.Contains(body, []byte("_e2ee")) || bytes.Contains(body, []byte("name*")) {
		t.Fatal("fixture no longer tests the case-folded path")
	}

	if sealed, _ := c.refuseAsyncBool(contentType, body); !sealed {
		t.Error("a body that cannot be shown NOT to carry an envelope must be treated as one")
	}
	if _, err := c.MaybeUnsealRequest(ginCtxWithContentType(contentType), body); err == nil {
		t.Fatal("must refuse rather than forward")
	}
}

// The complement, so the branch above is not simply refusing everything it
// cannot parse: an unparseable multipart Content-Type over a body that could not
// be naming the marker is still forwarded.
// INVERTED when the request-level branch stopped consulting the gate. The body
// here is a real, well-formed multipart body — its parts exist and are readable
// by any parser that resolves the duplicate boundary parameter — and the reason
// this enumeration sees none of them is the REQUEST's Content-Type, not anything
// about the parts. "The names were never read" is exact here, so the spelling
// heuristic has no business narrowing it: gated, this was the way around the
// ungated budget and nested branches.
//
// Its complement is TestMalformedMultipartWithoutTheMarkerIsStillForwarded: a
// body with no part-shaped line at all is still forwarded, on the structural
// fact that no parser reads a part from it under any boundary.
func TestUnparseableContentTypeWithARealBodyIsRefused(t *testing.T) {
	c := strictCtrl()
	body, _ := buildMultipart(t, func(w *multipart.Writer) {
		if err := w.WriteField("prompt", "an ordinary transcription"); err != nil {
			t.Fatalf("WriteField: %v", err)
		}
	})
	const contentType = `multipart/form-data; boundary=x; boundary=y`
	if couldNameE2EEPart(body) {
		t.Fatal("fixture must not trip the gate, or it cannot show the branch is ungated")
	}

	// The reason comes from the structural branch rather than the
	// boundary-missing one, because recoverBoundary now salvages `x` from the
	// duplicate parameter and the enumeration runs — and finds nothing, since
	// the body's real delimiter is the writer's. Either way the parts exist and
	// were never read, which is what must be asserted.
	if sealed, why := c.refuseAsyncBool(contentType, body); !sealed {
		t.Error("parts that exist and were never enumerated cannot be cleared")
	} else if !strings.Contains(why, "never reached") && !strings.Contains(why, "cannot be enumerated") {
		t.Errorf("the refusal should cite the unread parts, got %q", why)
	}
	if _, err := c.MaybeUnsealRequest(ginCtxWithContentType(contentType), body); err == nil {
		t.Fatal("must refuse rather than forward")
	}
}

// The five shapes that reached the ungated branches BY WAY of the two that were
// still gated. Each carries the marker in a part that is byte-identical and
// perfectly well-formed — named with an RFC 2047 encoded word, the one spelling
// couldNameE2EEPart provably cannot see — behind one broken, unrelated header.
//
// The fixtures must NOT spell the marker literally, which is the trap this test
// was nearly written into: with a literal `_e2ee` inside, the gate is true and
// gated and ungated behave identically, so the test would pass against the bug.
// The gate assertion below is what holds that.
func TestABrokenHeaderDoesNotHideTheRestOfTheBody(t *testing.T) {
	c := strictCtrl()
	const wordName = `=?utf-8?B?X2UyZWU=?=` // RFC 2047 for `_e2ee`
	const goodPart = "--B\r\nContent-Disposition: form-data; name=\"file\"\r\n\r\naudio\r\n"
	const wordPart = "--B\r\nContent-Disposition: form-data; name=\"" + wordName + "\"\r\n\r\n" + sealedEnvelopeJSON + "\r\n"
	const nestedPart = "--B\r\nContent-Disposition: form-data; name=\"w\"\r\n" +
		"Content-Type: multipart/mixed; boundary=I\r\n\r\n" +
		"--I\r\nContent-Disposition: form-data; name=\"" + wordName + "\"\r\n\r\nX\r\n--I--\r\n"
	const badHeader = "--B\r\nthis-is-not-a-header\r\n\r\njunk\r\n"

	for _, tt := range []struct {
		name        string
		contentType string
		body        string
	}{
		{"duplicate boundary parameter", `multipart/form-data; boundary=B; boundary=C`, goodPart + wordPart + "--B--\r\n"},
		{"unclosed quote in the boundary", `multipart/form-data; boundary="B`, goodPart + wordPart + "--B--\r\n"},
		{"trailing junk parameter", `multipart/form-data; boundary=B; =junk`, goodPart + wordPart + "--B--\r\n"},
		{"a part header without a colon, before the marker", `multipart/form-data; boundary=B`, badHeader + wordPart + "--B--\r\n"},
		{"a part header without a colon, before a nested part", `multipart/form-data; boundary=B`, badHeader + nestedPart + "--B--\r\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(tt.body)
			if couldNameE2EEPart(body) {
				t.Fatal("fixture spells something the gate can see, so it cannot distinguish gated from ungated")
			}
			if sealed, _ := c.refuseAsyncBool(tt.contentType, body); !sealed {
				t.Error("a broken unrelated header does not clear the parts behind it")
			}
			if _, err := c.MaybeUnsealRequest(ginCtxWithContentType(tt.contentType), body); err == nil {
				t.Fatal("must refuse rather than forward")
			}
		})
	}

	// The controls: the same marker-bearing parts, reachable, are refused — so
	// the rows above are about reachability and not about the name.
	for _, tt := range []struct {
		name string
		body string
	}{
		{"encoded-word part alone", wordPart + "--B--\r\n"},
		{"nested part alone", nestedPart + "--B--\r\n"},
	} {
		t.Run("control: "+tt.name, func(t *testing.T) {
			if sealed, _ := c.refuseAsyncBool(`multipart/form-data; boundary=B`, []byte(tt.body)); !sealed {
				t.Error("the control must be refused, or the rows above prove nothing")
			}
		})
	}
}

// countLineStartsWith's anchor is the whole point of it, and two mutations
// survived without this: counting the needle ANYWHERE, and anchoring on CRLF
// instead of LF. Both change what the two ungated branches decide.
func TestCountLineStartsWith(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		want int
	}{
		{"a part and the close", "--B\r\nx\r\n--B--\r\n", 2},
		// Mid-line occurrences are content, not delimiters. Counting them
		// overcounts and turns a forwardable body into a refusal.
		{"the needle inside part content", "--B\r\nCD: x\r\n\r\nsee --B here\r\n--B--\r\n", 2},
		// Bare LF is malformed but lenient parsers read it. Anchoring on CRLF
		// undercounts this to 0 and forwards a body with unenumerated parts.
		{"bare LF line endings", "--B\nx\n--B--\n", 2},
		{"at the very start only", "--B", 1},
		{"nowhere", "no delimiters at all", 0},
		{"empty", "", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := countLineStartsWith([]byte(tt.body), "--B"); got != tt.want {
				t.Errorf("got %d, want %d", got, tt.want)
			}
		})
	}
}

// And the two anchor properties as BEHAVIOUR, since a helper's unit test does
// not prove the branch reads it the way the branch needs.
func TestTheDelimiterAnchorDecidesTwoRealBodies(t *testing.T) {
	const contentType = `multipart/form-data; boundary=B`

	// Bare LF throughout: Go cannot enumerate it, and the marker-bearing part is
	// unenumerated content. Anchoring on CRLF would count 0 delimiters and
	// forward this.
	bareLF := []byte("--B\nthis-is-not-a-header\n\njunk\n" +
		"--B\nContent-Disposition: form-data; name=\"=?utf-8?B?X2UyZWU=?=\"\n\nX\n--B--\n")
	if couldNameE2EEPart(bareLF) {
		t.Fatal("fixture must not trip the gate")
	}
	if carries, _ := carriesE2EEPart(contentType, bareLF); !carries {
		t.Error("a bare-LF body with unenumerated parts must fail closed")
	}

	// The other direction: one enumerable part whose CONTENT mentions the
	// delimiter twice, and no closing delimiter. Nothing is unenumerated, so it
	// must be forwarded — counting the needle anywhere would make it 3 against 2
	// and refuse an ordinary truncated upload.
	contentMentions := []byte("--B\r\nContent-Disposition: form-data; name=\"file\"\r\n\r\nsee --B and --B\r\n")
	if couldNameE2EEPart(contentMentions) {
		t.Fatal("fixture must not trip the gate")
	}
	if carries, why := carriesE2EEPart(contentType, contentMentions); carries {
		t.Errorf("content mentioning the delimiter is not an unenumerated part: %s", why)
	}
}

// The NextPart error class is wider than "a part header is malformed", so the
// branch decides structurally rather than refusing everything that reaches it.
// This is the whole class, measured, with the verdict each row must get: a
// client that aborts an upload must not get an e2ee-flavoured 400, while a body
// that still declares parts nobody enumerated must fail closed.
func TestUnparseableBodyIsJudgedByWhatRemainsUnenumerated(t *testing.T) {
	const contentType = `multipart/form-data; boundary=B`
	const goodPart = "--B\r\nContent-Disposition: form-data; name=\"file\"\r\n\r\naudio\r\n"
	const badHeader = "--B\r\nthis-is-not-a-header\r\n\r\njunk\r\n"
	const wordPart = "--B\r\nContent-Disposition: form-data; name=\"=?utf-8?B?X2UyZWU=?=\"\r\n\r\nX\r\n"

	for _, tt := range []struct {
		name   string
		body   string
		refuse bool
	}{
		{"bad part header, then more parts", badHeader + wordPart + "--B--\r\n", true},
		{"bad part header, then the close", badHeader + "--B--\r\n", true},
		{"declared boundary never appears, body is part-shaped", "--OTHER\r\nContent-Disposition: form-data; name=\"x\"\r\n\r\nX\r\n--OTHER--\r\n", true},
		{"declared boundary never appears, body is JSON", `{"prompt":"not multipart at all"}`, false},
		{"no closing delimiter", goodPart, false},
		{"truncated mid part body", goodPart + "--B\r\nContent-Disposition: form-data; name=\"x\"\r\n\r\npartial", false},
		{"truncated mid part header", goodPart + "--B\r\nContent-Disposi", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(tt.body)
			if couldNameE2EEPart(body) {
				t.Fatal("fixture must not trip the gate: these branches are ungated and the gate would mask that")
			}
			carries, why := carriesE2EEPart(contentType, body)
			if carries != tt.refuse {
				t.Fatalf("carries = %v, want %v (%s)", carries, tt.refuse, why)
			}
		})
	}
}

// Any multipart SUBTYPE can carry parts, so the check cannot be scoped to
// form-data: matching only that left the hole reachable by changing one word of
// the Content-Type.
func TestNonFormDataMultipartSubtypeIsStillChecked(t *testing.T) {
	c := strictCtrl()
	body, contentType := buildMultipart(t, func(w *multipart.Writer) {
		if err := w.WriteField("_e2ee", sealedEnvelopeJSON); err != nil {
			t.Fatalf("WriteField: %v", err)
		}
	})
	mixed := strings.Replace(contentType, "multipart/form-data", "multipart/mixed", 1)
	if mixed == contentType {
		t.Fatal("fixture did not change the subtype")
	}

	if sealed, _ := c.refuseAsyncBool(mixed, body); !sealed {
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
	c := strictCtrl()
	body := []byte(`{"prompt":"a json body sent with a multipart content type"}`)
	const contentType = "multipart/form-data; boundary=abc123"

	if sealed, _ := c.refuseAsyncBool(contentType, body); sealed {
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
	c := strictCtrl()
	for _, where := range []string{"prompt", "language"} {
		t.Run(where, func(t *testing.T) {
			body, contentType := buildMultipart(t, func(w *multipart.Writer) {
				if err := w.WriteField(where, "explain what _e2ee means"); err != nil {
					t.Fatalf("WriteField: %v", err)
				}
			})

			if sealed, _ := c.refuseAsyncBool(contentType, body); sealed {
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
	c := strictCtrl()
	body, contentType := buildMultipart(t, func(*multipart.Writer) {})

	if sealed, _ := c.refuseAsyncBool(contentType, body); sealed {
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
	c := strictCtrl()
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
			if sealed, _ := c.refuseAsyncBool(tt.contentType, []byte(tt.body)); !sealed {
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
	c := strictCtrl()
	const body = "--x\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\nwhisper\r\n" +
		"--x\r\nContent-Disposition: form-data; name=\"_e2ee"
	const contentType = `multipart/form-data; boundary=x`

	if sealed, _ := c.refuseAsyncBool(contentType, []byte(body)); sealed {
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
	c := strictCtrl()
	body := []byte(`{"model":"whisper-large-v3","_e2ee":` + sealedEnvelopeJSON + `}`)

	if carries, _ := carriesE2EEPart("application/json", body); carries {
		t.Error("the multipart rule must not claim a JSON body")
	}
	if sealed, _ := c.refuseAsyncBool("application/json", body); !sealed {
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
	// The obvious implementation of the SAME rule: walk every offset, fold 5
	// bytes, and require the match to sit at a parameter position. The boundary
	// clause is not optional here — without it the reference matched `filename*`,
	// which is what browsers emit for a non-ASCII upload name, and a differential
	// test against a reference implementing a DIFFERENT rule proves nothing.
	//
	// The boundary set is spelled out rather than calling isParamBoundary: a
	// differential test that shares a helper with the code under test moves with
	// it, so a change to that helper would agree with itself and prove nothing
	// either. This is the same failure the two tests below were added for.
	walk := func(body []byte) bool {
		needle := []byte("name*")
		for i := 0; i+len(needle) <= len(body); i++ {
			if !bytes.EqualFold(body[i:i+len(needle)], needle) {
				continue
			}
			// IndexByte, not ContainsRune: a byte >= 0x80 becomes a multi-byte
			// rune, which would be looked up as its UTF-8 encoding.
			if i == 0 || bytes.IndexByte([]byte("; \t\r\n"), body[i-1]) >= 0 {
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
		// The parameter-position boundary. `filename*` is RFC 8187, which
		// browsers emit for a non-ASCII upload name, so matching it made the gate
		// true for most internationalised forms — and the gate decides the
		// fail-closed branches.
		`form-data; name="x"; filename*=UTF-8''caf%C3%A9.png`,
		"filename*=x", "FILENAME*=x", "xname*=x", `filename="name*.wav"`,
		// The spellings the boundary must still admit.
		"Content-Disposition: form-data; name*=utf-8''%5Fe2ee",
		"form-data;name*=x", "\r\nname*=x", "\tname*=x", "name*=x",
		// A rejected match must not end the scan: the real one comes after it.
		`form-data; filename*=UTF-8''x; name*=utf-8''%5Fe2ee`,
		"filename*filename*name*=x", "filename* name*=x",
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

	c := strictCtrl()
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

			if sealed, _ := c.refuseAsyncBool(contentType, body); !sealed {
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
	c := strictCtrl()
	for _, disposition := range []string{
		`form-data; name="file"; filename*=iso-8859-1''caf%E9.wav`,
		`form-data; name="file"; filename*0*=utf-8''caf%C3%A9; filename*1=".wav"`,
		`form-data; name="file"; FILENAME*=iso-8859-1''caf%E9.wav`,
	} {
		t.Run(disposition, func(t *testing.T) {
			body, contentType := buildMultipart(t, func(w *multipart.Writer) {
				rawPart(t, w, disposition, "RIFF....audio....")
			})
			if sealed, _ := c.refuseAsyncBool(contentType, body); sealed {
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
	c := strictCtrl()
	const disposition = `form-data; name="notes"; filename="my"file.txt"`
	if _, _, err := mime.ParseMediaType(disposition); err == nil {
		t.Fatal("fixture assumes this disposition does NOT parse")
	}

	body := []byte("--x\r\nContent-Disposition: " + disposition + "\r\n\r\nhello\r\n--x--\r\n")
	const contentType = `multipart/form-data; boundary=x`
	if couldNameE2EEPart(body) {
		t.Fatal("fixture must not mention the marker in any form")
	}

	if sealed, _ := c.refuseAsyncBool(contentType, body); sealed {
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

// The behaviour the parameter-position boundary in mentionsEncodedName exists
// for, which no differential test against a reference can pin: an RFC 8187
// `filename*` is not a mention of `name*`, so it must not arm the fail-closed
// branches for an unrelated part in the same body.
//
// This is the shape a real internationalised form takes: one part naming its
// upload with a percent-encoded filename, another with a sloppy unescaped quote
// that Go declines to parse. Before the boundary the first part made the gate
// true and the second one turned that into an e2ee-flavoured 400 — a request the
// broker forwarded before this check existed, refused for a reason that had
// nothing to do with the request.
func TestEncodedFilenameDoesNotArmTheFailClosedBranches(t *testing.T) {
	c := strictCtrl()
	const sloppy = `form-data; name="notes"; filename="my"file.txt"`
	if _, _, err := mime.ParseMediaType(sloppy); err == nil {
		t.Fatal("fixture assumes the second disposition does NOT parse")
	}

	body := []byte("--x\r\nContent-Disposition: form-data; name=\"file\"; filename*=UTF-8''caf%C3%A9.png\r\n\r\n\x89PNG\r\n" +
		"--x\r\nContent-Disposition: " + sloppy + "\r\n\r\nhello\r\n--x--\r\n")
	const contentType = `multipart/form-data; boundary=x`

	// Not an assumption: the gate is what decides the branch, and the whole
	// point is that this body no longer trips it.
	if couldNameE2EEPart(body) {
		t.Error("an RFC 8187 filename* is not a mention of the RFC 2231 name*")
	}
	if sealed, why := c.refuseAsyncBool(contentType, body); sealed {
		t.Errorf("an ordinary internationalised form is not a sealed request: %s", why)
	}
	got, err := c.MaybeUnsealRequest(ginCtxWithContentType(contentType), body)
	if err != nil {
		t.Fatalf("must be forwarded, got %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Error("the body must be forwarded unchanged")
	}
}

// Breaking the marker-bearing header must not clear what it declares. The gate
// on this branch was justified by "evading it needs the marker-bearing header to
// itself be unparseable, so the name is a guess either way" — the antecedent is
// right and the consequent does not follow: the header still visibly names the
// part, in the one spelling couldNameE2EEPart provably cannot see.
//
// Each row carries a byte-identical, well-formed part named with finding 17's
// RFC 2047 encoded word, behind a header broken in the three ways this file
// already enumerates for the request's Content-Type. The fixtures must NOT trip
// the gate, or they cannot tell the recovery from the heuristic.
func TestBreakingTheHeaderDoesNotClearTheNameItDeclares(t *testing.T) {
	c := strictCtrl()
	const word = `=?utf-8?B?X2UyZWU=?=` // RFC 2047 for `_e2ee`

	for _, tt := range []struct {
		name   string
		header string
	}{
		{"duplicate name", `Content-Disposition: form-data; name=x; name="` + word + `"`},
		{"unclosed quote", `Content-Disposition: form-data; name="` + word},
		{"junk parameter", `Content-Disposition: form-data; name="` + word + `"; =junk`},
		{"duplicate name on Content-Type", `Content-Type: text/plain; name=x; name="` + word + `"`},
		{"unclosed quote on Content-Type", `Content-Type: text/plain; name="` + word},
		// Only the encoded word can appear here. A padded literal and a
		// `name*=` spelling behind the same broken header ARE also refused, but
		// both trip the gate — the literal through hasE2EEMarker, `name*` through
		// mentionsEncodedName — so a fixture using them cannot tell the recovery
		// from the heuristic. That the recovered value is judged by every reading
		// is TestRecoverParamValues plus nameVerdict's own tests.
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte("--B\r\n" + tt.header + "\r\n\r\n" + sealedEnvelopeJSON + "\r\n--B--\r\n")
			const contentType = `multipart/form-data; boundary=B`
			if couldNameE2EEPart(body) {
				t.Fatal("fixture trips the gate, so it cannot distinguish recovery from the heuristic")
			}
			if sealed, _ := c.refuseAsyncBool(contentType, body); !sealed {
				t.Error("a broken header still declares the name it declares")
			}
			if _, err := c.MaybeUnsealRequest(ginCtxWithContentType(contentType), body); err == nil {
				t.Fatal("must refuse rather than forward")
			}
		})
	}
}

func TestRecoverParamValues(t *testing.T) {
	for _, tt := range []struct {
		header string
		want   []string
	}{
		{`form-data; name=x; name="a b"`, []string{"x", "a b"}},
		{`form-data; name="unclosed`, []string{"unclosed"}},
		{`form-data; name="v"; =junk`, []string{"v"}},
		{`form-data; NAME=upper`, []string{"upper"}},
		{`form-data;name=nospace`, []string{"nospace"}},
		{`form-data; name=  padded  ; x=1`, []string{"padded"}},
		{`form-data; name="quoted \" inside"`, []string{`quoted \" inside`}},
		// A `name=` inside a quoted VALUE is content, not a parameter — the
		// finding-11 shape, and the reason this walks parameters rather than
		// scanning for the needle.
		{`form-data; filename="a; name=_e2ee"`, nil},
		{`form-data; filename="name=x"`, nil},
		{`form-data`, nil},
		{``, nil},
	} {
		t.Run(tt.header, func(t *testing.T) {
			got := recoverParamValues(tt.header, "name")
			if len(got) != len(tt.want) {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("[%d] got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// And the same shape WITH the marker, so gating the branch did not disarm it.
func TestUnparseablePartDispositionMentioningTheMarkerIsRefused(t *testing.T) {
	c := strictCtrl()
	body := []byte("--x\r\nContent-Disposition: form-data; name=\"notes\"; filename=\"a\"b.txt\"\r\n\r\n_e2ee\r\n--x--\r\n")
	const contentType = `multipart/form-data; boundary=x`

	if sealed, _ := c.refuseAsyncBool(contentType, body); !sealed {
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
	c := strictCtrl()
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
			if sealed, _ := c.refuseAsyncBool(contentType, body); sealed {
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
	c := strictCtrl()
	tests := []struct {
		name        string
		disposition string
		parsedName  string // what Go yields, measured
	}{
		{"backslash before the underscore", `form-data; name="\_e2ee"`, `\_e2ee`},
		{"backslash mid-name", `form-data; name="\_e2\ee"`, `\_e2\ee`},
		// Padding, which Go reports as given. isEncodedWord was trimmed for the
		// same reason and these were not — and this reading needs strictly LESS
		// of the upstream than that one, because a parser trims before it decodes
		// anything.
		{"leading space", `form-data; name=" _e2ee"`, ` _e2ee`},
		{"trailing space", `form-data; name="_e2ee "`, `_e2ee `},
		{"padded both sides", `form-data; name="  _e2ee  "`, `  _e2ee  `},
		// Trim and quoted pair TOGETHER, in the order a single pass gets wrong:
		// the backslash protects the space that has to go, so trimming first
		// leaves `\ _e2ee` and resolving first leaves ` _e2ee`.
		{"a quoted pair protecting the space", `form-data; name="\ _e2ee"`, `\ _e2ee`},
		{"and padded after it", `form-data; name="\ _e2ee "`, `\ _e2ee `},
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
			if sealed, _ := c.refuseAsyncBool(contentType, body); !sealed {
				t.Error("a conformant parser reads this as the marker, so it cannot be ruled out")
			}
			if _, err := c.MaybeUnsealRequest(ginCtxWithContentType(contentType), body); err == nil {
				t.Fatal("must refuse rather than forward")
			}
		})
	}
}

// resolvesToMarker claims its four readings are the whole closure under
// trimming and quoted-pair resolution, not a sample of it. That is a claim about
// a fixpoint, so it is checked against one: apply both operations until nothing
// changes, and see whether the marker is anywhere in the resulting set.
//
// The reference does NOT call unquotePairs or strings.TrimSpace through the
// production predicate — it reaches for the same two primitives directly — for
// the reason the last round's differential had to be rewritten: a reference that
// shares the code under test agrees with itself under mutation.
//
// This exists because the suggested fix for this finding trimmed only BEFORE
// unquoting, which forwards `\ _e2ee`. A predicate over an unordered set of
// transformations is exactly the kind that gets a composition wrong, so the
// composition is what the test pins.
func TestResolvesToMarker(t *testing.T) {
	closure := func(s string) bool {
		seen := map[string]bool{s: true}
		for grew := true; grew; {
			grew = false
			for v := range seen {
				for _, next := range []string{strings.TrimSpace(v), strings.ReplaceAll(v, `\`, "")} {
					if !seen[next] {
						seen[next] = true
						grew = true
					}
				}
			}
		}
		return seen[e2eeBodyMarker]
	}

	// Every string over the alphabet that matters — the marker's own bytes plus a
	// backslash, a space and a tab — up to a length that can express the
	// compositions. Exhaustive beats hand-picked here: the failure this test
	// exists for was a composition nobody thought to write down.
	alphabet := []rune{'\\', ' ', '\t', '_', 'e', '2'}
	var gen func(prefix string, depth int)
	checked := 0
	gen = func(prefix string, depth int) {
		if got, want := resolvesToMarker(prefix), closure(prefix); got != want {
			t.Errorf("%q: got %v, the closure says %v", prefix, got, want)
		}
		checked++
		if depth == 0 {
			return
		}
		for _, r := range alphabet {
			gen(prefix+string(r), depth-1)
		}
	}
	gen("", 6)
	if checked < 40000 {
		t.Fatalf("the sweep only checked %d strings, so it is not exhaustive over the alphabet", checked)
	}

	// And the spellings the sweep's alphabet cannot reach, including the
	// near-misses that must stay forwarded.
	for value, want := range map[string]bool{
		`_e2ee`:                true,
		` _e2ee`:               true,
		`_e2ee `:               true,
		"\t_e2ee\r\n":          true,
		`\_e2ee`:               true,
		`\ _e2ee`:              true,
		` \_e2ee `:             true,
		`file`:                 false,
		``:                     false,
		`_e2e`:                 false,
		`__e2ee`:               false,
		`_e 2ee`:               false, // interior white space is not trimmed
		`_e2ee_`:               false,
		`=?utf-8?B?X2UyZWU=?=`: false, // isEncodedWord's business, not this one
	} {
		if got := resolvesToMarker(value); got != want {
			t.Errorf("%q: got %v, want %v", value, got, want)
		}
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
	c := strictCtrl()
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
			if sealed, _ := c.refuseAsyncBool(contentType, body); sealed {
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
	c := strictCtrl()
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

	if sealed, _ := c.refuseAsyncBool(contentType, body); !sealed {
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
		if carries, _ := carriesE2EEPart(contentType, body); carries {
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
// RFC 2046 §5.1.1 admits boundary characters that RFC 2045 calls tspecials, so
// an entirely legal but UNQUOTED boundary makes mime.ParseMediaType fail. Those
// bodies used to hit the structural refusal — a 400 on a legal request,
// pre-authentication on the sync path. Recovering the boundary enumerates them
// instead, which removes the false positive AND gives real part-name detection
// where there was only a blanket verdict.
func TestLegalUnquotedBoundariesAreEnumeratedRatherThanRefused(t *testing.T) {
	c := strictCtrl()
	for _, boundary := range []string{
		`abc:def`, `abc=def`, `a?b/c`, `a'b(c)d`, `a+b,c-d.e_f`, `boundary with space`,
	} {
		t.Run(boundary, func(t *testing.T) {
			contentType := "multipart/form-data; boundary=" + boundary
			if _, _, err := mime.ParseMediaType(contentType); err == nil {
				t.Fatal("fixture assumes Go REJECTS this Content-Type")
			}

			// An ordinary body under that boundary is forwarded...
			ordinary := []byte("--" + boundary + "\r\nContent-Disposition: form-data; name=\"file\"\r\n\r\naudio\r\n--" + boundary + "--\r\n")
			if carries, why := carriesE2EEPart(contentType, ordinary); carries {
				t.Errorf("a legal unquoted boundary is not a smuggled envelope: %s", why)
			}

			// ...and the marker under the same boundary is still found BY NAME,
			// which a blanket refusal could never report.
			marked := []byte("--" + boundary + "\r\nContent-Disposition: form-data; name=\"" + e2eeBodyMarker + "\"\r\n\r\n" + sealedEnvelopeJSON + "\r\n--" + boundary + "--\r\n")
			if carries, why := carriesE2EEPart(contentType, marked); !carries {
				t.Error("the marker must still be found under a recovered boundary")
			} else if !strings.Contains(why, "is named") {
				t.Errorf("it should be found by name, not by a structural fallback: %q", why)
			}
			_ = c
		})
	}
}

func TestRecoverBoundary(t *testing.T) {
	for _, tt := range []struct {
		contentType string
		want        string
	}{
		{`multipart/form-data; boundary=abc:def`, `abc:def`},
		{`multipart/form-data; boundary="quoted one"`, `quoted one`},
		{`multipart/form-data; boundary=with space; charset=utf-8`, `with space`},
		{`multipart/form-data; BOUNDARY=upper`, `upper`},
		{`multipart/form-data;boundary=nospace`, `nospace`},
		{`multipart/form-data; boundary=  padded  `, `padded`},
		{`multipart/form-data; boundary=x; boundary=y`, `x`},
		// Not a parameter position, so not a boundary.
		{`multipart/form-data; xboundary=no`, ``},
		{`multipart/form-data; name="boundary=inside a value"`, ``},
		{`multipart/form-data`, ``},
		{`multipart/form-data; boundary=`, ``},
	} {
		t.Run(tt.contentType, func(t *testing.T) {
			if got := recoverBoundary(tt.contentType); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// A body is an envelope or not by its BYTES. The Content-Type is the client's
// claim about them, and §5.3.1 read in the other direction says a body that IS
// an envelope is one whatever label it arrives under.
//
// An early return on `looksMultipart` sat above the envelope check and forwarded
// a genuine sealed envelope upstream in cleartext whenever the client labelled
// it multipart — the exact failure this PR exists to close, on the sync path,
// introduced by this PR. It also made the two entry points disagree about
// identical bytes, which is finding 1 returning.
//
// Deleting that block left the whole suite green, so this test is the pin: it
// asserts the two entry points AGREE, which is the property that was actually
// broken, rather than any particular verdict.
func TestARealEnvelopeIsNotForwardedBecauseItIsLabelledMultipart(t *testing.T) {
	envelope := []byte(`{"` + e2eeBodyMarker + `":` + sealedEnvelopeJSON + `}`)

	for _, contentType := range []string{
		`multipart/form-data; boundary=B`,
		`multipart/form-data`,
		`multipart/mixed; boundary=B`,
		`multipart/related; boundary=B; type="application/json"`,
	} {
		t.Run(contentType, func(t *testing.T) {
			// The async entry point has always seen this correctly.
			sealed, _ := isSealedRequestBool(contentType, envelope)
			if !sealed {
				t.Fatal("a body carrying a top-level envelope is sealed whatever its label")
			}

			// The sync one must not disagree. The envelope here is not a valid
			// one, so the sealed path fails closed on it — what matters is that
			// it REACHES that path instead of handing the body back to be
			// forwarded in the clear.
			c := strictFixture(t)
			got, err := c.MaybeUnsealRequest(ginCtxWithContentType(contentType), envelope)
			if err == nil && bytes.Equal(got, envelope) {
				t.Fatal("the sync path forwarded a sealed envelope unchanged, in cleartext")
			}
			if err == nil {
				t.Fatalf("expected the sealed path to be reached and fail closed, got a rewritten body %q", got)
			}
		})
	}

	// The complement, and the reason the deleted block looked reasonable: a real
	// multipart body still forwards. It gets there through the JSON path, whose
	// unmarshal fails — which is why IsSealedRequest never needed such a return.
	c := strictFixture(t)
	body, contentType := buildMultipart(t, func(w *multipart.Writer) {
		if err := w.WriteField("prompt", "mentions _e2ee in content"); err != nil {
			t.Fatalf("WriteField: %v", err)
		}
	})
	if !hasE2EEMarker(body) {
		t.Fatal("fixture must contain the literal marker, or it never reaches the JSON path")
	}
	got, err := c.MaybeUnsealRequest(ginCtxWithContentType(contentType), body)
	if err != nil {
		t.Fatalf("an ordinary multipart body must be forwarded, got %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Error("the body must be forwarded unchanged")
	}
}

// RFC 2045 §5 puts a `name` on Content-Type, and a part may declare it with NO
// Content-Disposition at all — in which case the disposition loop never runs and
// every check inside it is skipped. javax.mail's getFileName() reads it as a
// fallback. All three readings the disposition gets apply here, on the same
// premise.
//
// Each fixture is asserted to have no Content-Disposition, because that is the
// whole point of the branch: with one present these would be caught upstream and
// the test would pass while testing nothing.
func TestMultipartNamingTheMarkerOnContentTypeIsRefused(t *testing.T) {
	c := strictCtrl()
	for _, tt := range []struct {
		name        string
		contentType string
	}{
		{"literal name", `text/plain; name="_e2ee"`},
		{"padded name", `text/plain; name=" _e2ee"`},
		{"quoted pair in the name", `text/plain; name="\_e2ee"`},
		{"RFC 2231 in a charset Go drops", `text/plain; name*=iso-8859-1''%5Fe2ee`},
		{"RFC 2047 encoded word", `text/plain; name="=?utf-8?B?X2UyZWU=?="`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte("--B\r\nContent-Type: " + tt.contentType + "\r\n\r\n" +
				sealedEnvelopeJSON + "\r\n" +
				"--B\r\nContent-Disposition: form-data; name=\"file\"\r\n\r\naudio\r\n--B--\r\n")
			const contentType = `multipart/form-data; boundary=B`
			if bytes.Contains(body, []byte("Content-Disposition: form-data; name=\"_e2ee\"")) {
				t.Fatal("fixture must declare the name on Content-Type only")
			}

			if sealed, _ := c.refuseAsyncBool(contentType, body); !sealed {
				t.Error("a name declared on Content-Type cannot be ruled out")
			}
			if _, err := c.MaybeUnsealRequest(ginCtxWithContentType(contentType), body); err == nil {
				t.Fatal("must refuse rather than forward")
			}
		})
	}
}

// Two shapes the first version of this test could not see, both found by a
// surviving mutation rather than by reading the code.
//
// The Content-Type check must not be conditional on the disposition's absence.
// Scoping it to "no Content-Disposition" survives every fixture above, since
// none of them has one on the marker-bearing part — but which header a lenient
// parser prefers is not ours to assume, and a part declaring `name="file"` in
// one header and the marker in the other is exactly the shape that assumption
// gets wrong.
//
// And an unparseable part Content-Type must fail closed on the same terms as an
// unparseable disposition: gated, so a sloppy header alone is still forwarded,
// but refused once the body could be naming the marker.
func TestContentTypeNameIsCheckedRegardlessOfTheDisposition(t *testing.T) {
	c := strictCtrl()
	const contentType = `multipart/form-data; boundary=B`

	t.Run("a benign disposition does not excuse the Content-Type", func(t *testing.T) {
		body := []byte("--B\r\nContent-Disposition: form-data; name=\"file\"\r\n" +
			"Content-Type: text/plain; name=\"_e2ee\"\r\n\r\n" + sealedEnvelopeJSON + "\r\n--B--\r\n")
		if sealed, _ := c.refuseAsyncBool(contentType, body); !sealed {
			t.Error("the marker on Content-Type cannot be ruled out because another header names something else")
		}
	})

	t.Run("unparseable Content-Type, marker in the body", func(t *testing.T) {
		// The marker has to be in the CONTENT here: sealedEnvelopeJSON is the
		// envelope's value, and the literal `_e2ee` is the key that wraps it,
		// which a multipart body carries as the part name rather than in the
		// bytes. So a body whose only marker is a name the parser could not read
		// does not trip the gate — which is the whole shape of this branch.
		body := []byte("--B\r\nContent-Type: text/plain; charset=\"utf\"8\"\r\n\r\n" +
			`{"_e2ee":` + sealedEnvelopeJSON + "}\r\n--B--\r\n")
		if _, _, err := mime.ParseMediaType(`text/plain; charset="utf"8"`); err == nil {
			t.Fatal("fixture assumes this Content-Type does NOT parse")
		}
		if !couldNameE2EEPart(body) {
			t.Fatal("fixture must trip the gate, or it is not testing the gated branch")
		}
		if sealed, why := c.refuseAsyncBool(contentType, body); !sealed {
			t.Error("malformed AND could be naming the marker must fail closed")
		} else if !strings.Contains(why, "Content-Type could not be parsed") {
			t.Errorf("the refusal should cite the Content-Type parse, got %q", why)
		}
	})
}

// And the complement, so the Content-Type check refuses a NAME rather than the
// header: `name` on Content-Type is ordinary (old-style uploads and mail clients
// emit it), and a part whose type merely fails to parse without mentioning
// anything reserved was forwarded before this branch existed.
func TestOrdinaryContentTypeNamesAreForwarded(t *testing.T) {
	c := strictCtrl()
	for _, tt := range []struct {
		name        string
		contentType string
		// Whether the fixture is expected to trip couldNameE2EEPart. For most
		// rows it must not, so that a gated branch cannot be what forwards them.
		// `filename="_e2ee"` necessarily does — the marker is right there in the
		// body — and forwarding it anyway is the stronger claim: the gate is true
		// and no branch fires, because a filename is not a form-field name.
		tripsGate bool
	}{
		{name: "an unrelated name", contentType: `text/plain; name="notes.txt"`},
		{name: "a filename, not a name", contentType: `text/plain; filename="_e2ee"`, tripsGate: true},
		{name: "an encoded filename", contentType: `text/plain; filename*=utf-8''%5Fe2ee`},
		{name: "no parameters at all", contentType: `audio/wav`},
		{name: "unparseable, nothing reserved", contentType: `text/plain; charset="utf"8"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte("--B\r\nContent-Type: " + tt.contentType + "\r\n\r\nhello\r\n" +
				"--B\r\nContent-Disposition: form-data; name=\"file\"\r\n\r\naudio\r\n--B--\r\n")
			const contentType = `multipart/form-data; boundary=B`
			if got := couldNameE2EEPart(body); got != tt.tripsGate {
				t.Fatalf("gate = %v, want %v: the fixture is not exercising what this row is for", got, tt.tripsGate)
			}

			if sealed, why := c.refuseAsyncBool(contentType, body); sealed {
				t.Errorf("must be forwarded, refused because %s", why)
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

// The preamble and the epilogue, together, because mime/multipart is symmetric
// about the delimiters and the residuals list has to be right about both. Named
// as known-open in the PR description; RFC 2046 §5.1.1 says a conformant
// upstream ignores both, so reaching either needs a splitter that honours
// neither delimiter.
//
// This test pins what is NOT covered, which is unusual but deliberate: it fails
// if either position starts being enumerated, which is the moment the residuals
// list becomes wrong in the other direction.
func TestUnenumeratedPreambleAndEpilogueAreForwarded(t *testing.T) {
	const contentType = `multipart/form-data; boundary=B`
	const partShaped = "Content-Disposition: form-data; name=\"_e2ee\"\r\n\r\n" + sealedEnvelopeJSON + "\r\n"
	const file = "--B\r\nContent-Disposition: form-data; name=\"file\"\r\n\r\naudio\r\n"
	const term = "--B--\r\n"

	for _, tt := range []struct {
		name string
		body string
	}{
		{"preamble, before the first delimiter", partShaped + file + term},
		{"epilogue, after the close delimiter", file + term + "--B\r\n" + partShaped + term},
	} {
		t.Run(tt.name, func(t *testing.T) {
			carries, why := carriesE2EEPart(contentType, []byte(tt.body))
			if carries {
				t.Fatalf("this position is enumerated now (%s) — the residuals list needs updating, not this test", why)
			}
		})
	}

	// The control that makes the two rows above meaningful: the same block INSIDE
	// the delimiters is refused. Without this, a broken fixture would pass both.
	if carries, _ := carriesE2EEPart(contentType, []byte(file+"--B\r\n"+partShaped+term)); !carries {
		t.Fatal("the same block as a real part must be refused, or the fixtures prove nothing")
	}
}

func TestNestedMultipartPartIsRefused(t *testing.T) {
	c := strictCtrl()
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
		if sealed, _ := c.refuseAsyncBool(contentType, body); !sealed {
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
		if sealed, _ := c.refuseAsyncBool(contentType, body); !sealed {
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
		if sealed, _ := c.refuseAsyncBool(contentType, body); !sealed {
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
	carries, why := carriesE2EEPart(contentType, within)
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
	carries, why = carriesE2EEPart(contentType, past)
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
	if carries, why := carriesE2EEPart(contentType, quiet); !carries {
		t.Error("past the budget, a body whose parts were never enumerated cannot be cleared")
	} else if !strings.Contains(why, "more than") {
		t.Errorf("the refusal should cite the budget, got %q", why)
	}

	// The evasion the un-gating closes.
	evasive := build(pastIndex, `=?utf-8?B?X2UyZWU=?=`)
	if couldNameE2EEPart(evasive) {
		t.Fatal("fixture must not trip the gate, or it is not testing the evasion")
	}
	if carries, _ := carriesE2EEPart(contentType, evasive); !carries {
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
	if carries, why := carriesE2EEPart(contentType, manyDispositions.Bytes()); !carries {
		t.Error("one part declaring more dispositions than the budget must hit the same branch")
	} else if !strings.Contains(why, "more than") {
		t.Errorf("the refusal should cite the budget, got %q", why)
	}

	// And the third per-part loop, over the Content-Type headers that decide the
	// nested-multipart branch. Cheaper than the dispositions — a prefix test, no
	// ParseMediaType — but it is a per-part loop, which is the dimension the
	// budget exists for rather than a cost threshold it clears.
	var manyTypes bytes.Buffer
	manyTypes.WriteString("--B\r\nContent-Disposition: form-data; name=\"f\"\r\n")
	for i := 0; i <= maxHeadersExamined; i++ {
		manyTypes.WriteString("Content-Type: text/plain\r\n")
	}
	manyTypes.WriteString("\r\n\r\n--B--\r\n")
	if couldNameE2EEPart(manyTypes.Bytes()) {
		t.Fatal("fixture must not trip the gate")
	}
	if carries, why := carriesE2EEPart(contentType, manyTypes.Bytes()); !carries {
		t.Error("one part declaring more Content-Types than the budget must hit the same branch")
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
	c := strictCtrl()
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
		// RFC 2047 §2 permits linear white space around an encoded word, and a
		// prefix/suffix test does not. Before the trim these two were forwarded
		// while the row above was refused — the same word, one space over.
		{"leading space", `form-data; name=" =?utf-8?B?X2UyZWU=?="`, ` =?utf-8?B?X2UyZWU=?=`},
		{"trailing space", `form-data; name="=?utf-8?B?X2UyZWU=?= "`, `=?utf-8?B?X2UyZWU=?= `},
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

			if sealed, _ := c.refuseAsyncBool(contentType, body); !sealed {
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
		// RFC 2047 §2 linear white space, which the delimiters sit inside.
		` =?utf-8?B?X2UyZWU=?=`:      true,
		`=?utf-8?B?X2UyZWU=?= `:      true,
		"\t=?utf-8?B?X2UyZWU=?=\r\n": true,
		`  =?a?b?c?=  `:              true,
		// Trimming must not manufacture one out of whitespace alone.
		`   `:    false,
		"\t":     false,
		` =?=  `: false,
	}
	for value, want := range tests {
		if got := isEncodedWord(value); got != want {
			t.Errorf("%q: got %v, want %v", value, got, want)
		}
	}
}

// The flag's whole contract in one place: EXACT refusals hold either way,
// STRUCTURAL ones only when strict, and with the flag off the request is
// forwarded rather than silently dropped.
//
// The split matters because the structural branches refuse shapes that are legal
// or merely sloppy far more often than hostile, run before ValidateSession on
// the sync path, and have never seen production traffic. The exact ones are a
// fact about the request and no operator setting should forward them.
func TestStructuralRefusalsAreGated(t *testing.T) {
	const marker = "--B\r\nContent-Disposition: form-data; name=\"" + e2eeBodyMarker + "\"\r\n\r\n" + sealedEnvelopeJSON + "\r\n--B--\r\n"
	nested := "--B\r\nContent-Disposition: form-data; name=\"w\"\r\nContent-Type: multipart/mixed; boundary=I\r\n\r\n" +
		"--I\r\nContent-Disposition: form-data; name=\"x\"\r\n\r\ny\r\n--I--\r\n--B--\r\n"

	// Every structural branch, and an exact one behind each shape that could be
	// mistaken for structural. Four mutations survived a version of this test
	// carrying only the first two rows: the over-budget branch reclassified as
	// exact, and a RECOVERED marker name reclassified as structural, both went
	// unnoticed.
	var overBudget bytes.Buffer
	for i := 0; i <= maxHeadersExamined; i++ {
		overBudget.WriteString("--B\r\nContent-Disposition: form-data; name=\"f\"\r\n\r\nx\r\n")
	}
	overBudget.WriteString("--B--\r\n")

	for _, tt := range []struct {
		name          string
		body          string
		contentType   string
		strictRefuses bool
		laxRefuses    bool
	}{
		{name: "exact: a part names the marker", body: marker, strictRefuses: true, laxRefuses: true},
		// The name was RECOVERED from a header that would not parse, but it was
		// still read — so this is a fact about the request, not about what we
		// could not determine, and no flag may forward it.
		{
			name:          "exact: a marker name recovered from a broken header",
			body:          "--B\r\nContent-Disposition: form-data; name=x; name=\"=?utf-8?B?X2UyZWU=?=\"\r\n\r\n" + sealedEnvelopeJSON + "\r\n--B--\r\n",
			strictRefuses: true, laxRefuses: true,
		},
		{name: "structural: a nested multipart part", body: nested, strictRefuses: true, laxRefuses: false},
		{name: "structural: past the header budget", body: overBudget.String(), strictRefuses: true, laxRefuses: false},
		{
			name:          "structural: a boundary that cannot be recovered",
			body:          "--x\r\nContent-Disposition: form-data; name=\"f\"\r\n\r\nx\r\n--x--\r\n",
			contentType:   "multipart/form-data",
			strictRefuses: true, laxRefuses: false,
		},
		{
			name:          "structural: the parse stops with content ahead of it",
			body:          "--B\r\nthis-is-not-a-header\r\n\r\njunk\r\n--B\r\nContent-Disposition: form-data; name=\"f\"\r\n\r\nx\r\n--B--\r\n",
			strictRefuses: true, laxRefuses: false,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			contentType := tt.contentType
			if contentType == "" {
				contentType = `multipart/form-data; boundary=B`
			}
			for _, strict := range []bool{false, true} {
				c := &Ctrl{e2eeStrictMultipart: strict}
				want := tt.laxRefuses
				if strict {
					want = tt.strictRefuses
				}

				verdict, _ := c.RefuseAsync(contentType, []byte(tt.body))
				if got := verdict != NotSealed; got != want {
					t.Errorf("strict=%v: async refused=%v, want %v", strict, got, want)
				}

				got, err := c.MaybeUnsealRequest(ginCtxWithContentType(contentType), []byte(tt.body))
				if (err != nil) != want {
					t.Errorf("strict=%v: sync refused=%v, want %v (%v)", strict, err != nil, want, err)
				}
				// Forwarded means forwarded UNCHANGED, not dropped.
				if err == nil && !bytes.Equal(got, []byte(tt.body)) {
					t.Errorf("strict=%v: the body must be forwarded unchanged", strict)
				}
			}
		})
	}
}

// The shared budget is not the part limit, and the difference is ~3x: a part
// costs one unit plus one for each header it declares. The refusal message says
// "combined" for that reason — naming the constant would tell a caller cut off
// at 1365 parts that the limit is 4096.
func TestTheBudgetCountsPartsAndHeadersTogether(t *testing.T) {
	const contentType = `multipart/form-data; boundary=B`
	build := func(n int, extraHeader string) []byte {
		var b bytes.Buffer
		for i := 0; i < n; i++ {
			b.WriteString("--B\r\nContent-Disposition: form-data; name=\"f\"\r\n" + extraHeader + "\r\nx\r\n")
		}
		b.WriteString("--B--\r\n")
		return b.Bytes()
	}
	for _, tt := range []struct {
		name  string
		extra string
		under int
		over  int
	}{
		// part + disposition = 2 units, so ~2048 fit.
		{"plain fields", "", 2045, 2050},
		// part + disposition + type = 3 units, so ~1365 fit.
		{"file parts", "Content-Type: text/plain\r\n", 1360, 1370},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if carries, _ := carriesE2EEPart(contentType, build(tt.under, tt.extra)); carries {
				t.Errorf("%d %s should be under the budget", tt.under, tt.name)
			}
			carries, why := carriesE2EEPart(contentType, build(tt.over, tt.extra))
			if !carries {
				t.Fatalf("%d %s should be over the budget", tt.over, tt.name)
			}
			if !strings.Contains(why, "combined") {
				t.Errorf("the message must say the budget is shared, got %q", why)
			}
			if strings.Contains(why, "parts or part headers") {
				t.Errorf("the message must not read as a part limit, got %q", why)
			}
		})
	}
}

// The sync path's two refusals are two different problems, exactly as the async
// path's are: one says a part IS named the marker, the other says the body could
// not be checked. Collapsing them into one message survived as a mutation.
func TestSyncMessagesDifferPerVerdict(t *testing.T) {
	c := strictCtrl()
	const ct = `multipart/form-data; boundary=B`

	named := []byte("--B\r\nContent-Disposition: form-data; name=\"" + e2eeBodyMarker + "\"\r\n\r\n" + sealedEnvelopeJSON + "\r\n--B--\r\n")
	_, err := c.MaybeUnsealRequest(ginCtxWithContentType(ct), named)
	if err == nil {
		t.Fatal("a part named the marker must be refused")
	}
	if !strings.Contains(err.Error(), "must not carry a sealed envelope") {
		t.Errorf("the exact refusal should say the body carries one, got: %v", err)
	}

	unreadable := []byte("--B\r\nContent-Disposition: form-data; name=\"w\"\r\nContent-Type: multipart/mixed; boundary=I\r\n\r\n--I\r\nContent-Disposition: form-data; name=\"x\"\r\n\r\ny\r\n--I--\r\n--B--\r\n")
	_, err = c.MaybeUnsealRequest(ginCtxWithContentType(ct), unreadable)
	if err == nil {
		t.Fatal("an unreadable body must be refused when strict")
	}
	if !strings.Contains(err.Error(), "cannot be checked") {
		t.Errorf("the structural refusal should say it could not be checked, got: %v", err)
	}
	if strings.Contains(err.Error(), "must not carry a sealed envelope") {
		t.Errorf("it must not claim the body carries an envelope, got: %v", err)
	}
}

// Forwarding a structural refusal is only defensible if it is COUNTED — that
// counter is the evidence for flipping the flag, so losing the increment would
// make the rollout plan silently useless. It survived as a mutation without
// this.
func TestForwardedStructuralRefusalIsCounted(t *testing.T) {
	monitor.PrometheusInit("e2ee-multipart-test", "0x0000000000000000000000000000000000000001")

	counter := monitor.E2EEMultipartWouldRefuseTotal.WithLabelValues(monitor.E2EERefuseNestedMultipart)
	before := testutil.ToFloat64(counter)

	c := &Ctrl{} // flag off: forward and count
	body := []byte("--B\r\nContent-Disposition: form-data; name=\"w\"\r\nContent-Type: multipart/mixed; boundary=I\r\n\r\n--I\r\nContent-Disposition: form-data; name=\"x\"\r\n\r\ny\r\n--I--\r\n--B--\r\n")
	if _, err := c.MaybeUnsealRequest(ginCtxWithContentType(`multipart/form-data; boundary=B`), body); err != nil {
		t.Fatalf("with the flag off it must be forwarded, got %v", err)
	}

	if after := testutil.ToFloat64(counter); after != before+1 {
		t.Errorf("counter = %v, want %v: a forwarded structural refusal must be counted", after, before+1)
	}
}

// A 2231-encoded name the parser DECODED is an answer, and refusing it is a
// false positive on a legal, internationalised form. The branch fires on the
// raw header, so it did not care whether the parser had already resolved the
// name — and because it was classed EXACT, the false positive was ungated (an
// operator could not turn it off), uncounted (it returned before the counter, so
// the branch most likely to fire on real traffic was invisible to the rollout
// the counter exists for), and mislabelled (the async route said the part was
// named `_e2ee`).
func TestADecodedEncodedNameIsNotRefused(t *testing.T) {
	c := strictCtrl()
	for _, tt := range []struct {
		name        string
		disposition string
		wantName    string
	}{
		{"utf-8, fully decoded", `form-data; name*=UTF-8''hello`, "hello"},
		{"plain continuation, no charset", `form-data; name*0=he; name*1=llo`, "hello"},
		{"us-ascii", `form-data; name*=us-ascii''hello`, "hello"},
		// RFC 2231 §4.1: only the FIRST segment carries the charset, so a
		// continuation legitimately has no `charset''` prefix. Judging it as if
		// it did refused this, which decodes cleanly.
		{"multi-segment, charset on the first only", `form-data; name*0*=utf-8''he; name*1*=llo`, "hello"},
		{"multi-segment with percent escapes", `form-data; name*0*=utf-8''caf%C3%A9; name*1*=%20file`, "café file"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, params, err := mime.ParseMediaType(tt.disposition)
			if err != nil || params["name"] != tt.wantName {
				t.Fatalf("fixture assumes a clean decode to %q, got %q (err %v)", tt.wantName, params["name"], err)
			}
			body := []byte("--B\r\nContent-Disposition: " + tt.disposition + "\r\n\r\nx\r\n--B--\r\n")
			if carries, why := carriesE2EEPart(`multipart/form-data; boundary=B`, body); carries {
				t.Errorf("a decoded name is an answer, not a refusal: %s", why)
			}
			if _, err := c.MaybeUnsealRequest(ginCtxWithContentType(`multipart/form-data; boundary=B`), body); err != nil {
				t.Errorf("must be forwarded even when strict, got %v", err)
			}
		})
	}
}

// And the complement: when the parser could NOT decode it, the branch still
// fires — but as STRUCTURAL, so it is gated and counted like every other
// "cannot be ruled out". Emptiness alone is not the test: an unsupported charset
// can still yield a non-empty PARTIAL concatenation missing the segment that
// carried the marker, and a supported charset can still fail on a bad escape.
func TestAnUndecodableEncodedNameIsStructural(t *testing.T) {
	for _, tt := range []struct {
		name        string
		disposition string
		parsedName  string
	}{
		{"unsupported charset", `form-data; name*=iso-8859-1''x`, ""},
		{"empty charset", `form-data; name*=''%5Fe2ee`, ""},
		{"partial: one segment dropped", `form-data; name*0*=iso-8859-1''%5Fe2; name*1=ee`, "ee"},
		{"supported charset, bad escape", `form-data; name*=utf-8''%ZZe2ee`, ""},
		// The FIRST segment has no charset delimiter, so Go drops it and keeps
		// the rest — a non-empty name that is missing the segment which mattered.
		// Neither half of the test alone catches this one.
		{"first segment has no charset delimiter", `form-data; name*0*=hello; name*1=x`, "x"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, params, _ := mime.ParseMediaType(tt.disposition)
			if params["name"] != tt.parsedName {
				t.Fatalf("Go now yields %q, not %q: re-check the premise", params["name"], tt.parsedName)
			}
			body := []byte("--B\r\nContent-Disposition: " + tt.disposition + "\r\n\r\nx\r\n--B--\r\n")
			const ct = `multipart/form-data; boundary=B`
			// No gate assertion here, unlike the tests for the heuristic branches:
			// every fixture necessarily contains `name*` at a parameter position,
			// so couldNameE2EEPart is true by construction. It is also never
			// consulted — these headers PARSE, so the verdict comes from
			// nameVerdict's classification, which is what the strict/lax
			// difference below is measuring.

			// Strict: refused. Default: forwarded and counted.
			if _, err := strictCtrl().MaybeUnsealRequest(ginCtxWithContentType(ct), body); err == nil {
				t.Error("an undecodable name cannot be ruled out")
			}
			got, err := (&Ctrl{}).MaybeUnsealRequest(ginCtxWithContentType(ct), body)
			if err != nil {
				t.Errorf("with the flag off it must be forwarded, got %v", err)
			}
			if err == nil && !bytes.Equal(got, body) {
				t.Error("forwarded means forwarded unchanged")
			}
		})
	}
}

// The pre-authentication path must not write a log line per request: the caller
// chooses the rate and the body decides the content. proxy/rejection.go exists
// because that pattern turned one client into a 150k-line/day flood, and its
// conclusion — the Prometheus counter is the real-time signal — applies here.
func TestTheGatedPathDoesNotLogPerRequest(t *testing.T) {
	logger := &warnCountingLogger{}
	c := &Ctrl{logger: logger}
	body := []byte("--B\r\nContent-Disposition: form-data; name=\"w\"\r\nContent-Type: multipart/mixed; boundary=I\r\n\r\n--I\r\nContent-Disposition: form-data; name=\"x\"\r\n\r\ny\r\n--I--\r\n--B--\r\n")

	for i := 0; i < 50; i++ {
		if _, err := c.MaybeUnsealRequest(ginCtxWithContentType(`multipart/form-data; boundary=B`), body); err != nil {
			t.Fatalf("with the flag off it must be forwarded, got %v", err)
		}
	}
	if logger.warns != 0 {
		t.Errorf("wrote %d log lines for 50 pre-auth requests; the counter is the signal", logger.warns)
	}
}

// warnCountingLogger counts warnings so TestTheGatedPathDoesNotLogPerRequest can
// assert on VOLUME rather than content. It embeds log.Logger, like the package's
// countingLogger, so only the methods under test need implementing.
type warnCountingLogger struct {
	log.Logger
	warns int
}

func (l *warnCountingLogger) Warn(args ...interface{})                 { l.warns++ }
func (l *warnCountingLogger) Warnf(format string, args ...interface{}) { l.warns++ }
