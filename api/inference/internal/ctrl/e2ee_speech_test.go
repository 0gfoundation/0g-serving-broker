package ctrl

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"maps"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/0gfoundation/0g-pc-e2ee/protocol/wire"
	"github.com/gin-gonic/gin"
	"github.com/patrickmn/go-cache"

	"github.com/0glabs/0g-serving-broker/inference/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

// The speech profile is the first JSON-ified one (SPEC §5.3): the client seals a
// JSON object and the enclave materializes multipart for the upstream. These
// tests go through the real seal — wire.SealRequestFor — rather than a
// hand-written envelope, so a change to the profile's rules fails here rather
// than being asserted around.

const speechAudio = "RIFF....some audio bytes...."

func speechFixture(t *testing.T) *e2eeTestFixture {
	t.Helper()
	f := newE2EEFixture(t)
	f.c.Service = config.Service{Type: constant.ServiceTypeSpeechToText}
	return f
}

// speechCtx is the gin context for a sealed transcription: a JSON Content-Type,
// because a sealed request on this endpoint IS JSON (§5.3.1).
func speechCtx() *gin.Context {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/audio/transcriptions", nil)
	ctx.Request.Header.Set("Content-Type", "application/json")
	return ctx
}

// speechSealedFields is the presence filter a conforming client applies: the
// profile's payload list, narrowed to the fields this request actually carries.
// Three of the four are optional (SPEC §5.3.2), so the profile's list is not
// itself a valid sealed set for every request — and the list is read from the
// library rather than restated here, so a change to the profile reaches these
// tests instead of being asserted around.
func speechSealedFields(req wire.Request) []string {
	var fields []string
	for _, f := range wire.DefaultSealedFieldsFor(wire.ProfileSpeech) {
		if _, ok := req[f]; ok {
			fields = append(fields, f)
		}
	}
	return fields
}

// sealSpeech seals a JSON-ified transcription request, sealing exactly the
// fields the profile requires for the fields present.
func sealSpeech(t *testing.T, f *e2eeTestFixture, req wire.Request) []byte {
	t.Helper()
	sealed, err := wire.SealRequestFor(wire.ProfileSpeech, f.encPub, req, speechSealedFields(req), f.signerAddr, f.clientEphPub)
	if err != nil {
		t.Fatalf("SealRequestFor(speech): %v", err)
	}
	b, err := json.Marshal(sealed)
	if err != nil {
		t.Fatalf("marshal sealed speech request: %v", err)
	}
	return b
}

// readForm parses a materialized body the way the upstream would, returning the
// file part's bytes and filename plus every text field.
func readForm(t *testing.T, contentType string, body []byte) (audio []byte, filename string, fields map[string][]string) {
	t.Helper()
	mediatype, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatalf("the materialized Content-Type does not parse: %v", err)
	}
	if mediatype != "multipart/form-data" {
		t.Fatalf("materialized media type = %q, want multipart/form-data", mediatype)
	}
	if params["boundary"] == "" {
		t.Fatal("the materialized Content-Type declares no boundary")
	}

	fields = map[string][]string{}
	r := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		part, err := r.NextPart()
		if err == io.EOF { //nolint:errorlint // a bare io.EOF is the clean end of parts
			break
		}
		if err != nil {
			t.Fatalf("the materialized body does not parse as multipart: %v", err)
		}
		content, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("read part %q: %v", part.FormName(), err)
		}
		if part.FileName() != "" {
			audio, filename = content, part.FileName()
			if got := part.FormName(); got != speechUpstreamFileField {
				t.Errorf("the audio part is named %q, want %q — the upstream reads the audio from that field", got, speechUpstreamFileField)
			}
			continue
		}
		fields[part.FormName()] = append(fields[part.FormName()], string(content))
	}
	return audio, filename, fields
}

// The whole point of the profile, end to end: the client sends JSON, the
// upstream gets multipart, and the audio survives the round trip byte for byte.
func TestSealedSpeechRequestIsMaterializedAsMultipart(t *testing.T) {
	f := speechFixture(t)
	body := sealSpeech(t, f, wire.Request{
		"model":           mustRaw(t, "whisper-large-v3"),
		"response_format": mustRaw(t, "json"),
		"file_base64":     mustRaw(t, base64.StdEncoding.EncodeToString([]byte(speechAudio))),
		"filename":        mustRaw(t, "board-meeting-2026Q3.m4a"),
		"language":        mustRaw(t, "en"),
		"prompt":          mustRaw(t, "attendees are named in the recording"),
	})

	ctx := speechCtx()
	out, err := f.c.MaybeUnsealRequest(ctx, body)
	if err != nil {
		t.Fatalf("MaybeUnsealRequest: %v", err)
	}

	// The Content-Type must move with the body: everything downstream reads the
	// boundary out of the header, so a multipart body under `application/json`
	// reaches the upstream unparseable.
	contentType := ctx.Request.Header.Get("Content-Type")
	audio, filename, fields := readForm(t, contentType, out)

	if string(audio) != speechAudio {
		t.Errorf("audio round-tripped as %q, want %q", audio, speechAudio)
	}
	// Forwarded deliberately (SPEC §5.3): some backends sniff the container from
	// the extension. It is sealed on the way in, so this is the first point it
	// exists in the clear, and that point is inside the enclave.
	if filename != "board-meeting-2026Q3.m4a" {
		t.Errorf("filename = %q, want the sealed one forwarded", filename)
	}
	for field, want := range map[string]string{
		"model":           "whisper-large-v3",
		"response_format": "json",
		"language":        "en",
		"prompt":          "attendees are named in the recording",
	} {
		if got := fields[field]; len(got) != 1 || got[0] != want {
			t.Errorf("field %q = %v, want [%q]", field, got, want)
		}
	}
	// The base64 field itself must NOT survive as a form field: it is the audio's
	// encoding, not a field of the request the upstream serves.
	if v, ok := fields[speechFileField]; ok {
		t.Errorf("%q leaked into the form as %v", speechFileField, v)
	}
	// Nor may the envelope's own marker.
	if v, ok := fields[e2eeBodyMarker]; ok {
		t.Errorf("%q leaked into the form as %v", e2eeBodyMarker, v)
	}

	// Content-Length describes the forwarded bytes, not the envelope's.
	if ctx.Request.ContentLength != int64(len(out)) {
		t.Errorf("ContentLength = %d, want %d", ctx.Request.ContentLength, len(out))
	}

	// The boundary is generated here — none crosses the sealed channel (§5.3), so
	// it cannot be one the client chose.
	if bytes.Contains(body, []byte(strings.TrimPrefix(contentType, "multipart/form-data; boundary="))) {
		t.Error("the materialized boundary appears in the sealed request, so it was carried rather than generated")
	}
}

// Only `file_base64` is required unconditionally; the other three payload fields
// are optional, and a request omitting them must still materialize.
func TestSealedSpeechRequestWithOnlyTheAudio(t *testing.T) {
	f := speechFixture(t)
	body := sealSpeech(t, f, wire.Request{
		"model":           mustRaw(t, "whisper-large-v3"),
		"response_format": mustRaw(t, "verbose_json"),
		"file_base64":     mustRaw(t, base64.StdEncoding.EncodeToString([]byte(speechAudio))),
	})

	ctx := speechCtx()
	out, err := f.c.MaybeUnsealRequest(ctx, body)
	if err != nil {
		t.Fatalf("MaybeUnsealRequest: %v", err)
	}
	audio, filename, fields := readForm(t, ctx.Request.Header.Get("Content-Type"), out)
	if string(audio) != speechAudio {
		t.Errorf("audio = %q, want %q", audio, speechAudio)
	}
	// A part needs SOME filename to read as a file upload rather than a text
	// field, so an absent one becomes a placeholder rather than nothing.
	if filename != speechFallbackFilename {
		t.Errorf("filename = %q, want the %q placeholder", filename, speechFallbackFilename)
	}
	if got := fields["response_format"]; len(got) != 1 || got[0] != "verbose_json" {
		t.Errorf("response_format = %v, want [verbose_json]", got)
	}
}

// §5.3.1's other half: on this endpoint a JSON body MUST be a valid envelope or
// be refused. Forwarding it "just in case" is how "is this sealed?" stops being
// a question anyone answers.
func TestJSONBodyOnTheSpeechEndpointMustBeAnEnvelope(t *testing.T) {
	f := speechFixture(t)
	for _, tt := range []struct {
		name string
		body string
	}{
		{"an ordinary JSON request", `{"model":"whisper-large-v3","file_base64":"AA=="}`},
		{"a body that merely mentions the marker", `{"model":"m","prompt":"what is _e2ee?"}`},
		{"not an object at all", `[1,2,3]`},
		{"empty", ``},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := f.c.MaybeUnsealRequest(speechCtx(), []byte(tt.body)); err == nil {
				t.Error("a JSON body here must be refused rather than forwarded (SPEC §5.3.1)")
			}
		})
	}

	// And the endpoint's ordinary shape is untouched: a multipart transcription
	// carrying no envelope is forwarded exactly as before.
	body, contentType := transcriptionBody(t, nil)
	ctx := speechCtx()
	ctx.Request.Header.Set("Content-Type", contentType)
	got, err := f.c.MaybeUnsealRequest(ctx, body)
	if err != nil {
		t.Fatalf("an ordinary multipart transcription must be forwarded, got %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Error("forwarded means forwarded unchanged")
	}
}

// The same rule must NOT fire on a service type with no JSON-ified profile: an
// ordinary JSON request on a JSON endpoint is not an envelope and never was.
func TestTheJSONRuleIsScopedToJSONIfiedEndpoints(t *testing.T) {
	f := newE2EEFixture(t) // chatbot
	plain := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	got, err := f.c.MaybeUnsealRequest(ginCtxWithContentType("application/json"), plain)
	if err != nil {
		t.Fatalf("an ordinary chat request must be forwarded, got %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Error("forwarded unchanged")
	}
}

// The §5.3.1 rule is about ONE ENDPOINT, and scoping it by service type alone
// was a live break rather than a loose edge: MaybeUnsealRequest runs before the
// route is classified, and `serviceGroup.Any("*any", …)` puts every method and
// path under /v1/proxy through it. So on a speech provider every one of these
// was a 400 — measured — including the endpoint a sealed client must call to
// fetch the §8 signature this very profile emits. Clients that set a JSON
// content type on every request (the OpenAI SDKs among them) would have lost the
// signature fetch to the change that started producing signatures.
func TestTheJSONRuleFiresOnlyOnTheTranscriptionRoute(t *testing.T) {
	for _, tt := range []struct {
		path    string
		refused bool
	}{
		{"/v1/proxy/audio/transcriptions", true},
		// The proxy collapses a redundant /v1, so SDKs reach the same endpoint
		// through either spelling and both must be covered.
		{"/v1/proxy/v1/audio/transcriptions", true},
		{"/v1/proxy/audio/transcriptions/", true},
		{"/v1/proxy/audio/transcriptions?foo=bar", true},
		// Everything else on the same provider must pass through untouched.
		{"/v1/proxy/signature/some-chat-id", false},
		{"/v1/proxy/attestation/report", false},
		{"/v1/proxy/models", false},
		{"/v1/proxy/chat/completions", false},
		{"/v1/proxy/", false},
	} {
		for _, method := range []string{"GET", "POST"} {
			t.Run(method+" "+tt.path, func(t *testing.T) {
				f := speechFixture(t)
				gin.SetMode(gin.TestMode)
				ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
				ctx.Request = httptest.NewRequest(method, tt.path, nil)
				ctx.Request.Header.Set("Content-Type", "application/json")

				_, err := f.c.MaybeUnsealRequest(ctx, nil)
				if tt.refused != (err != nil) {
					t.Fatalf("refused = %v, want %v (err: %v)", err != nil, tt.refused, err)
				}
			})
		}
	}
}

// A form field name and a filename reach the multipart writer straight from the
// opened envelope, and multipart.Writer writes header values verbatim — it
// escapes `\` and `"` and does nothing at all about CR/LF. So a sealed field
// name could inject header lines into the body the BROKER builds.
//
// The boundary never leaves this process, so a whole extra part cannot be
// injected; the reachable damage is a parser differential on a duplicated
// Content-Disposition, which is the broker and the upstream disagreeing about
// which model was requested. That is what the profile exists to prevent.
func TestSealedSpeechRefusesHeaderInjectionInNamesAndFilename(t *testing.T) {
	for _, tt := range []struct {
		name string
		req  wire.Request
	}{
		{
			"a field name carrying CRLF and a second Content-Disposition",
			wire.Request{"zz\r\nContent-Disposition: form-data; name=model\r\n\r\nexpensive-model\r\nX": mustRaw(t, "v")},
		},
		{"a field name carrying a bare LF", wire.Request{"a\nb": mustRaw(t, "v")}},
		{"a field name carrying a bare CR", wire.Request{"a\rb": mustRaw(t, "v")}},
		// Escaped by Go as \" and resolved correctly by Go's own ParseMediaType,
		// but a parser that does not process backslash escapes reads a second
		// `name` parameter out of it. Measured both halves.
		{"a field name carrying a quote", wire.Request{`a"; name="model`: mustRaw(t, "v")}},
		{"a filename carrying CRLF", wire.Request{"filename": mustRaw(t, "a.mp3\r\nX-Injected-Header: yes")}},
		{"a filename carrying a quote", wire.Request{"filename": mustRaw(t, `a.mp3"; name="model`)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := speechFixture(t)
			req := wire.Request{
				"model":           mustRaw(t, "cheap-model"),
				"response_format": mustRaw(t, "json"),
				"file_base64":     mustRaw(t, base64.StdEncoding.EncodeToString([]byte(speechAudio))),
			}
			maps.Copy(req, tt.req)

			ctx := speechCtx()
			out, err := f.c.MaybeUnsealRequest(ctx, sealSpeech(t, f, req))
			if err == nil {
				t.Fatalf("a name that cannot appear in a multipart header must be refused; body was:\n%s", out)
			}
			// And refused for THIS reason, not incidentally by some other rule.
			if !strings.Contains(err.Error(), "cannot appear in a multipart part header") {
				t.Errorf("refused for the wrong reason: %v", err)
			}
		})
	}
}

// A backslash is deliberately NOT refused: escaped, it yields a literal
// backslash in the value rather than a parameter break, and refusing it would
// reject an ordinary Windows-style filename for no gain.
func TestSealedSpeechAcceptsABackslashInAFilename(t *testing.T) {
	f := speechFixture(t)
	req := wire.Request{
		"model":           mustRaw(t, "cheap-model"),
		"response_format": mustRaw(t, "json"),
		"file_base64":     mustRaw(t, base64.StdEncoding.EncodeToString([]byte(speechAudio))),
		"filename":        mustRaw(t, `C:\recordings\a.mp3`),
	}
	ctx := speechCtx()
	out, err := f.c.MaybeUnsealRequest(ctx, sealSpeech(t, f, req))
	if err != nil {
		t.Fatalf("an ordinary Windows-style filename must be forwarded: %v", err)
	}
	_, filename, _ := readForm(t, ctx.Request.Header.Get("Content-Type"), out)
	if filename != `C:\recordings\a.mp3` {
		t.Errorf("filename reached the upstream as %q", filename)
	}
}

// `file` is the name the audio part is written under, so a sealed field of the
// same name materializes TWO parts called `file`. Measured: Go's ReadForm keeps
// both, sorting them by kind, while a backend reading `form["file"]` or taking
// the last match gets the decoy text and transcribes nothing.
//
// Refused rather than skipped: nothing in the profile legitimately seals `file`,
// and silently dropping a field the client sealed is the one outcome a profile
// whose whole claim is "the upstream gets what the client sealed" must not have.
func TestSealedSpeechRefusesAFieldNamedFile(t *testing.T) {
	f := speechFixture(t)
	req := wire.Request{
		"model":           mustRaw(t, "cheap-model"),
		"response_format": mustRaw(t, "json"),
		"file_base64":     mustRaw(t, base64.StdEncoding.EncodeToString([]byte(speechAudio))),
		"file":            mustRaw(t, "decoy"),
	}
	ctx := speechCtx()
	out, err := f.c.MaybeUnsealRequest(ctx, sealSpeech(t, f, req))
	if err == nil {
		t.Fatalf("a sealed %q field must be refused; body was:\n%s", speechUpstreamFileField, out)
	}
	if !strings.Contains(err.Error(), "reserved for the materialized audio part") {
		t.Errorf("refused for the wrong reason: %v", err)
	}
}

// §5.3.3: the profile defines no streaming frames, so a streaming request is
// refused rather than answered with a shape the SPEC does not describe. The rule
// is a PERMITTED SET compared by materialized rendering, so every spelling a form
// would read as true fails — including the string and numeric ones a blacklist by
// JSON type let through in the first implementation.
//
// WHICH HALF this test covers, measured rather than assumed: the SENDER's. §5.3.3
// binds both halves, and the enclave's own check (wire's validatePinnedIfPresent,
// reached through OpenRequestFor) is NOT reachable from here — the AAD covers the
// whole cleartext envelope, so any tampering that would put `stream: true` in
// front of the enclave fails the AAD first, and producing a correctly-AAD'd
// non-conforming envelope means reimplementing the sealer. That half guards a
// third-party client that does not implement §5.3.3 (SPEC §12) and is covered by
// the protocol package's tests.
//
// An earlier version of this test looped over the spellings and treated a sealer
// refusal as a pass, which made it assert nothing about either half: the sealer
// refuses all five, so the enclave branch never ran.
func TestSealedSpeechStreamingIsRefusedBySender(t *testing.T) {
	f := speechFixture(t)
	audio := mustRaw(t, base64.StdEncoding.EncodeToString([]byte(speechAudio)))
	for _, stream := range []any{true, "true", 1, "1", "yes"} {
		req := wire.Request{
			"model":           mustRaw(t, "whisper-large-v3"),
			"response_format": mustRaw(t, "json"),
			"file_base64":     audio,
			"stream":          mustRaw(t, stream),
		}
		if _, err := wire.SealRequestFor(wire.ProfileSpeech, f.encPub, req, speechSealedFields(req), f.signerAddr, f.clientEphPub); err == nil {
			t.Errorf("stream=%#v sealed; the profile defines no streaming response shape", stream)
			continue
		}
	}
}

// The conditional half of the pin: ABSENCE is compliant, and so is the permitted
// value. An implementation that reused the unconditional machinery would reject
// every conforming request, which is the trap §5.3.3 names.
func TestSealedSpeechAcceptsAbsentAndFalseStream(t *testing.T) {
	f := speechFixture(t)
	audio := mustRaw(t, base64.StdEncoding.EncodeToString([]byte(speechAudio)))
	for _, tt := range []struct {
		name string
		req  wire.Request
	}{
		{"absent", wire.Request{
			"model": mustRaw(t, "whisper-large-v3"), "response_format": mustRaw(t, "json"), "file_base64": audio,
		}},
		{"false", wire.Request{
			"model": mustRaw(t, "whisper-large-v3"), "response_format": mustRaw(t, "json"), "file_base64": audio,
			"stream": mustRaw(t, false),
		}},
		// A sender carrying form fields across as strings is doing nothing wrong —
		// the form they came from had no types (§5.3.3).
		{"the string false", wire.Request{
			"model": mustRaw(t, "whisper-large-v3"), "response_format": mustRaw(t, "json"), "file_base64": audio,
			"stream": mustRaw(t, "false"),
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := speechCtx()
			out, err := f.c.MaybeUnsealRequest(ctx, sealSpeech(t, f, tt.req))
			if err != nil {
				t.Fatalf("must be accepted: %v", err)
			}
			// And `stream` is DROPPED rather than rendered: a field that is not
			// written cannot be misread, and the values a form parser reads as true
			// are an open set.
			_, _, fields := readForm(t, ctx.Request.Header.Get("Content-Type"), out)
			if v, ok := fields[speechStreamField]; ok {
				t.Errorf("%q was materialized as %v; it is dropped so no rendering of it can read as true", speechStreamField, v)
			}
		})
	}
}

// The materializer's own edges, at the level where they are decidable.
func TestSpeechFormValues(t *testing.T) {
	for _, tt := range []struct {
		name       string
		field      string
		json       string
		wantField  string
		wantValues []string
		wantErr    bool
	}{
		{name: "string", field: "language", json: `"en"`, wantField: "language", wantValues: []string{"en"}},
		{name: "bool", field: "b", json: `true`, wantField: "b", wantValues: []string{"true"}},
		// Shortest round-trip, so an integral value does not reach the upstream as
		// `12.0` where the client wrote `12`.
		{name: "integral number", field: "n", json: `12`, wantField: "n", wantValues: []string{"12"}},
		{name: "fractional number", field: "temperature", json: `0.25`, wantField: "temperature", wantValues: []string{"0.25"}},
		// The OpenAI surface's multipart spelling for a repeated field.
		{
			name: "array", field: "timestamp_granularities", json: `["word","segment"]`,
			wantField: "timestamp_granularities[]", wantValues: []string{"word", "segment"},
		},
		{name: "empty array", field: "a", json: `[]`, wantField: "a[]", wantValues: []string{}},
		// No field at all, rather than the four letters: absence is a value the
		// endpoint understands, `"null"` is a string it would try to parse.
		{name: "null", field: "x", json: `null`, wantField: "x", wantValues: nil},
		// Refused rather than guessed: bracket paths, JSON-in-a-field and dotted
		// keys are all in use, so there is no one rendering to pick.
		{name: "object", field: "o", json: `{"a":1}`, wantErr: true},
		{name: "array holding an object", field: "a", json: `[{"a":1}]`, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			field, values, err := speechFormValues(tt.field, json.RawMessage(tt.json))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if field != tt.wantField {
				t.Errorf("field = %q, want %q", field, tt.wantField)
			}
			if len(values) != len(tt.wantValues) {
				t.Fatalf("values = %v, want %v", values, tt.wantValues)
			}
			for i := range values {
				if values[i] != tt.wantValues[i] {
					t.Errorf("values[%d] = %q, want %q", i, values[i], tt.wantValues[i])
				}
			}
		})
	}
}

// The audio field is the one thing the profile cannot do without, and its
// encoding is fixed by §5.3 at STANDARD base64 with padding — not §3's
// base64url-without-padding, because the same field name already has an unsealed
// contract on the router's JSON surface and one name must not have two decoders.
func TestSpeechAudioBytes(t *testing.T) {
	for _, tt := range []struct {
		name    string
		req     wire.Request
		want    string
		wantErr bool
	}{
		{
			name: "standard base64 with padding",
			req:  wire.Request{speechFileField: json.RawMessage(`"` + base64.StdEncoding.EncodeToString([]byte("hi")) + `"`)},
			want: "hi",
		},
		{name: "absent", req: wire.Request{}, wantErr: true},
		{name: "not a string", req: wire.Request{speechFileField: json.RawMessage(`123`)}, wantErr: true},
		{name: "not base64", req: wire.Request{speechFileField: json.RawMessage(`"not!base64"`)}, wantErr: true},
		// Decodes to nothing: an empty audio part would reach the upstream as a
		// request to transcribe silence and be billed for it.
		{name: "empty", req: wire.Request{speechFileField: json.RawMessage(`""`)}, wantErr: true},
		// base64url without padding is §3's encoding for the fields that travel in
		// the clear, and is NOT this field's.
		{
			name:    "base64url without padding",
			req:     wire.Request{speechFileField: json.RawMessage(`"` + base64.RawURLEncoding.EncodeToString([]byte{0xfb, 0xff}) + `"`)},
			wantErr: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := speechAudioBytes(tt.req)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && string(got) != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// §7.3: the response sealed set is NOT a constant. `text` always, plus each of
// `segments` / `words` / `language` the frame carries — a profile-wide constant
// would reject a plain `json` transcription (no segments) or leak a
// `verbose_json` one (segments in the clear). The resolution lives in the wire
// package and this path reaches it through the same prepareFrameForSealing the
// chat and image paths use; these rows are what proves the speech handler is on
// that path rather than beside it.
func TestSealedSpeechResponseSealsWhatTheFrameCarries(t *testing.T) {
	for _, tt := range []struct {
		name       string
		body       string
		wantSealed []string
		// The billable quantity MUST stay cleartext, as EITHER usage.seconds or the
		// top-level duration (§7.3). Billing reads it out of the plaintext, and the
		// router reads it off the wire, so a seal that swallowed it would bill zero.
		cleartext []string
	}{
		{
			name:       "json carries only the transcript",
			body:       `{"model":"whisper-large-v3","usage":{"type":"duration","seconds":12.5},"text":"the transcript"}`,
			wantSealed: []string{"text"},
			cleartext:  []string{"usage"},
		},
		{
			name:       "verbose_json carries segments and an inferred language",
			body:       `{"model":"whisper-large-v3","task":"transcribe","duration":12.5,"language":"english","text":"the transcript","segments":[{"id":0,"text":"the transcript"}]}`,
			wantSealed: []string{"language", "segments", "text"},
			cleartext:  []string{"duration"},
		},
		{
			name:       "word granularity adds words",
			body:       `{"model":"whisper-large-v3","duration":1.0,"text":"hi","segments":[],"words":[{"word":"hi"}]}`,
			wantSealed: []string{"segments", "text", "words"},
			cleartext:  []string{"duration"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := speechFixture(t)
			ctx := newGinCtx()
			ctx.Set(CtxKeyE2EESealed, true)
			ctx.Set(CtxKeyE2EEProfile, wire.ProfileSpeech)
			ctx.Set(CtxKeyE2EEClientEphPub, f.clientEphPub)
			ctx.Set(CtxKeyE2EEReqBindHash, f.reqBindHash(t))

			out, isSealed, _, err := f.c.maybeSealNonStreamResponse(ctx, []byte(tt.body))
			if err != nil {
				t.Fatalf("maybeSealNonStreamResponse: %v", err)
			}
			if !isSealed {
				t.Fatal("a sealed request must get a sealed response")
			}

			var frame map[string]json.RawMessage
			if err := json.Unmarshal(out, &frame); err != nil {
				t.Fatalf("sealed frame is not a JSON object: %v", err)
			}
			for _, field := range tt.wantSealed {
				if _, ok := frame[field]; ok {
					t.Errorf("%q is still in the frame's cleartext half", field)
				}
			}
			for _, field := range tt.cleartext {
				if _, ok := frame[field]; !ok {
					t.Errorf("%q must stay cleartext — it is the billable quantity (§7.3)", field)
				}
			}

			// And it opens, so the sealing is real rather than a deletion.
			opened, err := wire.OpenResponseFor(wire.ProfileSpeech, f.clientEphSk, frame)
			if err != nil {
				t.Fatalf("a conforming client must be able to open it: %v", err)
			}
			for _, field := range tt.wantSealed {
				if _, ok := opened[field]; !ok {
					t.Errorf("%q did not come back out of the seal", field)
				}
			}
		})
	}
}

// One request must materialize to one field order. Go's map iteration is
// randomized, so without the sort the forwarded bytes — and anything downstream
// that hashes them — differ run to run for identical input.
func TestSpeechMaterializationFieldOrderIsStable(t *testing.T) {
	f := speechFixture(t)
	req := wire.Request{
		"model":           mustRaw(t, "whisper-large-v3"),
		"response_format": mustRaw(t, "json"),
		"file_base64":     mustRaw(t, base64.StdEncoding.EncodeToString([]byte(speechAudio))),
		"language":        mustRaw(t, "en"),
		"prompt":          mustRaw(t, "a hint"),
		"temperature":     mustRaw(t, 0.2),
	}

	order := func() []string {
		ctx := speechCtx()
		out, err := f.c.MaybeUnsealRequest(ctx, sealSpeech(t, f, req))
		if err != nil {
			t.Fatalf("MaybeUnsealRequest: %v", err)
		}
		_, params, err := mime.ParseMediaType(ctx.Request.Header.Get("Content-Type"))
		if err != nil {
			t.Fatalf("Content-Type: %v", err)
		}
		var names []string
		r := multipart.NewReader(bytes.NewReader(out), params["boundary"])
		for {
			part, err := r.NextPart()
			if err == io.EOF { //nolint:errorlint // a bare io.EOF is the clean end of parts
				break
			}
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			names = append(names, part.FormName())
			if _, err := io.Copy(io.Discard, part); err != nil {
				t.Fatalf("drain part: %v", err)
			}
		}
		return names
	}

	first := order()
	// Repeated, because one run cannot distinguish a sort from a map that happened
	// to iterate that way.
	for i := 0; i < 8; i++ {
		if got := order(); !slices.Equal(got, first) {
			t.Fatalf("field order varies between materializations: %v then %v", first, got)
		}
	}
	// The audio leads, as a real client sends it; the rest are sorted.
	rest := first[1:]
	if !slices.IsSorted(rest) {
		t.Errorf("fields after the audio are not sorted: %v", rest)
	}
}

// The handler WIRING, not the sealer. maybeSealNonStreamResponse is covered
// above at unit level, and that is structurally unable to catch this handler
// failing to use the result: two mutations — never sealing at all, and writing
// the plaintext transcript instead of the sealed frame — passed every test in
// this file until this one existed. The same shape as PR 1's Content-Type gap,
// one layer up.
func TestSpeechHandlerWritesTheSealedFrame(t *testing.T) {
	f := speechFixture(t)
	// TargetSeparated so the signing RPC is out of scope here, and whitelisted so
	// billing is skipped — what is under test is seal-then-write.
	f.c.Service.TargetSeparated = true
	f.c.reconciliationDB = &mockReconciliationDB{}

	rec := httptest.NewRecorder()
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest("POST", "/v1/audio/transcriptions", nil)
	ctx.Set(CtxKeyE2EESealed, true)
	ctx.Set(CtxKeyE2EEProfile, wire.ProfileSpeech)
	ctx.Set(CtxKeyE2EEClientEphPub, f.clientEphPub)
	ctx.Set(CtxKeyE2EEReqBindHash, f.reqBindHash(t))

	const transcript = "the transcript nobody else may read"
	upstream := `{"model":"whisper-large-v3","usage":{"type":"duration","seconds":12.5},"text":"` + transcript + `"}`
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(upstream)),
	}

	if err := f.c.handleNonStreamingSpeechToText(ctx, resp, nil, model.Request{IsWhitelisted: true, RequestHash: "req-1"}); err != nil {
		t.Fatalf("handleNonStreamingSpeechToText: %v", err)
	}

	written := rec.Body.Bytes()
	if bytes.Contains(written, []byte(transcript)) {
		t.Fatal("the plaintext transcript reached the client on a sealed turn")
	}
	var frame map[string]json.RawMessage
	if err := json.Unmarshal(written, &frame); err != nil {
		t.Fatalf("what was written is not a JSON object: %v", err)
	}
	if _, ok := frame[e2eeBodyMarker]; !ok {
		t.Fatalf("what was written carries no %q envelope, so it was not sealed: %s", e2eeBodyMarker, written)
	}
	// The billable quantity stays cleartext (§7.3) — billing and the router both
	// read it off the frame.
	if _, ok := frame["usage"]; !ok {
		t.Error("usage must stay cleartext")
	}
	opened, err := wire.OpenResponseFor(wire.ProfileSpeech, f.clientEphSk, frame)
	if err != nil {
		t.Fatalf("a conforming client must be able to open it: %v", err)
	}
	var got string
	if err := json.Unmarshal(opened["text"], &got); err != nil {
		t.Fatalf("unmarshal the opened transcript: %v", err)
	}
	if got != transcript {
		t.Errorf("opened transcript = %q, want %q", got, transcript)
	}
}

// The §8 signature must reach the client on a sealed turn WHATEVER the provider
// topology. chat and image already relax the ZG-Res-Key / signing gate to
// `... || e2eeSealed`; speech kept a bare `!TargetSeparated`, so on a
// TargetSeparated or centralized provider a sealed transcription got no
// signature at all and an E2EE client had a sealed response it could not verify.
//
// The PR's own note — "no test for the signed text, because the fixture runs
// TargetSeparated" — was the symptom: that is exactly the configuration where
// signing did not run.
func TestSealedSpeechIsSignedOnEveryProviderTopology(t *testing.T) {
	for _, tt := range []struct {
		name            string
		targetSeparated bool
	}{
		{"in-network", false},
		{"TargetSeparated", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := speechFixture(t)
			f.c.Service.TargetSeparated = tt.targetSeparated
			f.c.reconciliationDB = &mockReconciliationDB{}

			rec := httptest.NewRecorder()
			gin.SetMode(gin.TestMode)
			ctx, _ := gin.CreateTestContext(rec)
			ctx.Request = httptest.NewRequest("POST", "/v1/audio/transcriptions", nil)
			ctx.Set(CtxKeyE2EESealed, true)
			ctx.Set(CtxKeyE2EEProfile, wire.ProfileSpeech)
			ctx.Set(CtxKeyE2EEClientEphPub, f.clientEphPub)
			ctx.Set(CtxKeyE2EEReqBindHash, f.reqBindHash(t))

			upstream := `{"model":"whisper-large-v3","usage":{"type":"duration","seconds":3.5},"text":"secret"}`
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{},
				Body:       io.NopCloser(strings.NewReader(upstream)),
			}
			if err := f.c.handleNonStreamingSpeechToText(ctx, resp, nil, model.Request{IsWhitelisted: true, RequestHash: "req-1"}); err != nil {
				t.Fatalf("handleNonStreamingSpeechToText: %v", err)
			}

			// The client needs the handle...
			chatKey := rec.Header().Get("ZG-Res-Key")
			if chatKey == "" {
				t.Fatal("no ZG-Res-Key on a sealed turn: the client cannot fetch the §8 signature")
			}
			// ...and the handle must resolve to a signature, or it is a promise of
			// nothing.
			cached, ok := f.c.svcCache.Get(f.c.chatCacheKey(chatKey))
			if !ok {
				t.Fatal("ZG-Res-Key was emitted but no signature was cached for it")
			}
			sig, ok := cached.(ChatSignature)
			if !ok {
				t.Fatalf("cached value is %T, want ChatSignature", cached)
			}
			// §8 binds the ON-WIRE ciphertext, so the signed text must not be a
			// digest of the plaintext transcript.
			if sig.Text == "" {
				t.Error("the signed text is empty")
			}
			if strings.Contains(sig.Text, "secret") {
				t.Error("the signed text carries the plaintext transcript")
			}
		})
	}
}

// A sealed request must never take the streaming branch, and the reason it could
// is that the branch was chosen by a substring scan over the materialized
// multipart rather than by the protocol. handleStreamingSpeechToText is
// E2EE-unaware and forwards the transcript line by line in the clear.
func TestSealedSpeechNeverTakesTheStreamingBranch(t *testing.T) {
	f := speechFixture(t)
	f.c.Service.TargetSeparated = true
	f.c.reconciliationDB = &mockReconciliationDB{}

	// A prompt a user could plausibly dictate, carrying both substrings the old
	// detector scanned for. Sealed, so it is the enclave that materializes it.
	const decoy = "the form field is written name=\"stream\" and the value is\ntrue"
	sealedBody := sealSpeech(t, f, wire.Request{
		"model":           mustRaw(t, "whisper-large-v3"),
		"response_format": mustRaw(t, "json"),
		"file_base64":     mustRaw(t, base64.StdEncoding.EncodeToString([]byte(speechAudio))),
		"prompt":          mustRaw(t, decoy),
	})

	unsealCtx := speechCtx()
	materialized, err := f.c.MaybeUnsealRequest(unsealCtx, sealedBody)
	if err != nil {
		t.Fatalf("MaybeUnsealRequest: %v", err)
	}
	// The premise: the decoy really is in the body the handler will inspect.
	if !bytes.Contains(materialized, []byte(`name="stream"`)) {
		t.Fatal("fixture no longer carries the decoy, so it proves nothing")
	}

	rec := httptest.NewRecorder()
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = unsealCtx.Request
	ctx.Set(CtxKeyE2EESealed, true)
	ctx.Set(CtxKeyE2EEProfile, wire.ProfileSpeech)
	ctx.Set(CtxKeyE2EEClientEphPub, f.clientEphPub)
	ctx.Set(CtxKeyE2EEReqBindHash, f.reqBindHash(t))

	const transcript = "the transcript nobody else may read"
	upstream := `{"model":"whisper-large-v3","usage":{"type":"duration","seconds":3.5},"text":"` + transcript + `"}`
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(upstream)),
	}

	// Through the DISPATCH, not the non-streaming handler directly — the choice of
	// branch is what is under test.
	if err := f.c.handleSpeechToTextResponse(ctx, resp, model.User{}, "", materialized, model.Request{IsWhitelisted: true, RequestHash: "req-1"}); err != nil {
		t.Fatalf("handleSpeechToTextResponse: %v", err)
	}

	written := rec.Body.Bytes()
	if bytes.Contains(written, []byte(transcript)) {
		t.Fatal("the plaintext transcript reached the client: the streaming branch was taken on a sealed turn")
	}
	var frame map[string]json.RawMessage
	if err := json.Unmarshal(written, &frame); err != nil {
		t.Fatalf("what was written is not a sealed JSON frame: %v", err)
	}
	if _, ok := frame[e2eeBodyMarker]; !ok {
		t.Fatalf("what was written carries no %q envelope: %s", e2eeBodyMarker, written)
	}
}

// The other side of the dispatch: an UNSEALED streaming request must still
// stream. Failing closed for sealed traffic must not cost real streaming STT
// clients their branch — and nothing covered that until a mutation inverting the
// condition (`e2eeSealed && ...`) survived every test in this file.
//
// The branches are told apart by whether the response was FLUSHED: the streaming
// handler goes through ctx.Stream, which flushes per line; the non-streaming one
// does a single plain Write.
func TestUnsealedStreamingSpeechStillStreams(t *testing.T) {
	body, contentType := transcriptionBody(t, func(w *multipart.Writer) {
		if err := w.WriteField("stream", "true"); err != nil {
			t.Fatalf("WriteField: %v", err)
		}
	})

	for _, tt := range []struct {
		name        string
		sealed      bool
		wantFlushed bool
	}{
		{"unsealed stream=true streams", false, true},
		// And the sealed case does not, however the body reads.
		{"sealed never streams", true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := speechFixture(t)
			f.c.reconciliationDB = &mockReconciliationDB{}

			// ctx.Stream needs a CloseNotifier, which the bare recorder is not —
			// and the streaming branch reaching that requirement is itself part of
			// what distinguishes the two paths.
			rec := httptest.NewRecorder()
			w := &closeNotifyRecorder{ResponseRecorder: rec, closed: make(chan bool, 1)}
			gin.SetMode(gin.TestMode)
			ctx, _ := gin.CreateTestContext(w)
			ctx.Request = httptest.NewRequest("POST", "/v1/audio/transcriptions", nil)
			ctx.Request.Header.Set("Content-Type", contentType)
			if tt.sealed {
				ctx.Set(CtxKeyE2EESealed, true)
				ctx.Set(CtxKeyE2EEProfile, wire.ProfileSpeech)
				ctx.Set(CtxKeyE2EEClientEphPub, f.clientEphPub)
				ctx.Set(CtxKeyE2EEReqBindHash, f.reqBindHash(t))
			}

			upstream := `{"model":"whisper-large-v3","usage":{"type":"duration","seconds":3.5},"text":"hi"}`
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{},
				Body:       io.NopCloser(strings.NewReader(upstream)),
			}
			// Errors are not the subject here — which branch ran is.
			_ = f.c.handleSpeechToTextResponse(ctx, resp, model.User{}, "", body, model.Request{IsWhitelisted: true, RequestHash: "req-1"})

			if rec.Flushed != tt.wantFlushed {
				t.Errorf("flushed = %v, want %v (flushed means the streaming branch ran)", rec.Flushed, tt.wantFlushed)
			}
		})
	}
}

// When the frame CANNOT be sealed, the transcript must not be forwarded anyway.
//
// This is reachable, not hypothetical: `response_format=text` (and srt/vtt) makes
// the upstream answer with a bare transcript rather than a JSON object, and
// maybeSealNonStreamResponse refuses a non-object body fail-closed. Erroring the
// request is the right answer — a sealed client that asked for a plaintext format
// asked for two incompatible things — but only if the handler honours the refusal.
// A mutation that dropped the `sealErr != nil` arm and fell through with the
// plaintext `body` survived every other test here.
func TestSealedSpeechFailsClosedWhenTheFrameCannotBeSealed(t *testing.T) {
	f := speechFixture(t)
	f.c.Service.TargetSeparated = true
	f.c.reconciliationDB = &mockReconciliationDB{}
	// Seeded because the mutant this test exists to kill runs ON past the seal into
	// the plaintext-billing fallback, and an unseeded cache makes that path panic on
	// a nil cache instead of reaching the assertion below. A panic and a failed
	// assertion are not the same result: the second proves the transcript was
	// forwarded, the first only proves the fixture is thin.
	f.c.serviceCache = cache.New(5*time.Minute, 10*time.Minute)
	f.c.serviceCache.Set("current_service", model.Service{InputPrice: "1", OutputPrice: "1"}, cache.DefaultExpiration)

	rec := httptest.NewRecorder()
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest("POST", "/v1/audio/transcriptions", nil)
	ctx.Set(CtxKeyE2EESealed, true)
	ctx.Set(CtxKeyE2EEProfile, wire.ProfileSpeech)
	ctx.Set(CtxKeyE2EEClientEphPub, f.clientEphPub)
	ctx.Set(CtxKeyE2EEReqBindHash, f.reqBindHash(t))

	const transcript = "the transcript nobody else may read"
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/plain"}},
		Body:       io.NopCloser(strings.NewReader(transcript)),
	}

	err := f.c.handleNonStreamingSpeechToText(ctx, resp, nil, model.Request{IsWhitelisted: true, RequestHash: "req-1"})
	if err == nil {
		t.Error("a seal failure on a sealed turn must fail the request")
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(transcript)) {
		t.Fatalf("the plaintext transcript reached the client after the seal failed: %s", rec.Body.Bytes())
	}
}

// §7.3 requires a numeric cleartext duration, and real upstreams often send
// none. The shapes below are what whisper-1, self-hosted faster-whisper and
// gpt-4o-transcribe actually return, measured against the pinned protocol
// package — so sealing demands MORE of an upstream than proxying does, and
// this file's own updateSpeechToTextFallback exists because upstreams so often
// report no usage.
//
// Fail-closed is right (a synthesized duration would have §8 sign a number the
// model never produced), so what is under test is that the refusal is NAMED and
// attributed UPSTREAM — the transcript was produced and the provider's shape is
// what makes it unusable, so it must not land in the broker's alert bucket.
func TestSealedSpeechRefusesAnUnbillableUpstreamShape(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		// refused: does the request fail, and with the duration-specific error
		// attributed upstream?
		refused bool
	}{
		{"no usage at all (whisper-1, response_format=json)", `{"text":"hello"}`, true},
		{"token usage, no seconds (gpt-4o-transcribe)", `{"text":"hello","usage":{"type":"tokens","input_tokens":14,"output_tokens":4,"total_tokens":18}}`, true},
		// flexFloat64 exists because a whisper backend was observed emitting this;
		// the profile takes a JSON number only, so it is refused too. Recorded here
		// rather than worked around: making the protocol accept a quoted number is
		// the protocol package's call, not the broker's.
		{"duration as a quoted number", `{"text":"hello","duration":"3.2"}`, true},
		// And the shapes that DO satisfy §7.3 must still go through, or the guard
		// would be a blanket refusal rather than a shape check.
		{"numeric usage.seconds", `{"text":"hello","usage":{"type":"duration","seconds":3.2}}`, false},
		{"numeric top-level duration", `{"text":"hello","duration":3.2}`, false},
		// A genuine ZERO is a value §7.3 accepts, even though hasBillableUsage
		// will not bill it — a billing question, not a sealing one.
		{"a genuine zero duration", `{"text":"hello","duration":0}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := speechFixture(t)
			f.c.Service.TargetSeparated = true
			f.c.reconciliationDB = &mockReconciliationDB{}
			f.c.serviceCache = cache.New(5*time.Minute, 10*time.Minute)
			f.c.serviceCache.Set("current_service", model.Service{InputPrice: "1", OutputPrice: "1"}, cache.DefaultExpiration)

			rec := httptest.NewRecorder()
			gin.SetMode(gin.TestMode)
			ctx, _ := gin.CreateTestContext(rec)
			ctx.Request = httptest.NewRequest("POST", "/v1/audio/transcriptions", nil)
			ctx.Set(CtxKeyE2EESealed, true)
			ctx.Set(CtxKeyE2EEProfile, wire.ProfileSpeech)
			ctx.Set(CtxKeyE2EEClientEphPub, f.clientEphPub)
			ctx.Set(CtxKeyE2EEReqBindHash, f.reqBindHash(t))

			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(tt.body)),
			}
			err := f.c.handleNonStreamingSpeechToText(ctx, resp, nil, model.Request{IsWhitelisted: true, RequestHash: "req-1"})

			if !tt.refused {
				if err != nil {
					t.Fatalf("a §7.3-conforming shape must be served, got: %v", err)
				}
				// Served means sealed, not passed through in the clear.
				if bytes.Contains(rec.Body.Bytes(), []byte("hello")) {
					t.Errorf("the plaintext transcript reached the client: %s", rec.Body.Bytes())
				}
				return
			}

			if err == nil {
				t.Fatal("an upstream shape with no cleartext duration must be refused")
			}
			if bytes.Contains(rec.Body.Bytes(), []byte("hello")) {
				t.Errorf("the plaintext transcript reached the client: %s", rec.Body.Bytes())
			}
			// Named, so an operator reading the log or the response knows this is a
			// provider shape problem and not a broker fault.
			if !strings.Contains(err.Error(), "no cleartext audio duration") {
				t.Errorf("the refusal is not the duration-specific one: %v", err)
			}
			// Attributed upstream. Without the override resolveFailureSource returns
			// "broker" for an un-flagged 4xx, which would fire the broker alert for a
			// provider degradation.
			src, _ := ctx.Get(monitor.CtxKeyFailureSource)
			if src != monitor.FailureSourceUpstream {
				t.Errorf("failure source = %v, want %q", src, monitor.FailureSourceUpstream)
			}
		})
	}
}

// The other direction, and it is the common case rather than an edge: an
// UNSEALED transcription with no duration must still be SERVED. `{"text":…}` is
// what whisper-1 and most self-hosted builds answer `response_format=json`
// with, and updateSpeechToTextFallback exists for exactly that — so a guard that
// forgot to scope itself to sealed traffic would refuse ordinary non-E2EE
// requests wholesale.
//
// Nothing covered this until a mutation widening the guard to
// `!isSealed || …` survived every other test in this file — the same shape as
// the inverted-dispatch survivor one round earlier, in the same `!sealed`
// direction.
func TestUnsealedTranscriptionWithNoDurationIsStillServed(t *testing.T) {
	f := speechFixture(t)
	f.c.Service.TargetSeparated = true
	f.c.reconciliationDB = &mockReconciliationDB{}
	f.c.serviceCache = cache.New(5*time.Minute, 10*time.Minute)
	f.c.serviceCache.Set("current_service", model.Service{InputPrice: "1", OutputPrice: "1"}, cache.DefaultExpiration)

	rec := httptest.NewRecorder()
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest("POST", "/v1/audio/transcriptions", nil)
	// No CtxKeyE2EESealed: an ordinary client.

	const upstream = `{"text":"hello"}`
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(upstream)),
	}
	if err := f.c.handleNonStreamingSpeechToText(ctx, resp, nil, model.Request{IsWhitelisted: true, RequestHash: "req-1"}); err != nil {
		t.Fatalf("an unsealed transcription with no duration must still be served: %v", err)
	}
	if got := rec.Body.String(); got != upstream {
		t.Errorf("body forwarded = %q, want the upstream body %q", got, upstream)
	}
	if src, ok := ctx.Get(monitor.CtxKeyFailureSource); ok {
		t.Errorf("a served request must not be attributed as a failure, got %v", src)
	}
}

// The gate change is wire-visible for UNSEALED traffic too, and that half had no
// test. Relaxing the gate to `… || IsCentralized()` and routing through
// signChatResponse means a centralized STT provider now emits ZG-Res-Key and
// caches a ROUTING PROOF where before it emitted nothing — the same evidence
// chat and image already give, but a real behaviour change for non-E2EE clients.
//
// Both branches, because the streaming half kept the old `!TargetSeparated` gate
// and signChatWithKey after the non-streaming half was fixed: an unsealed
// centralized provider got the proof on a non-streaming transcription and
// nothing on a streaming one, a difference in the client's evidence that the
// streaming flag has no business making.
func TestUnsealedCentralizedSpeechIsSignedOnBothBranches(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "non-streaming"
		if streaming {
			name = "streaming"
		}
		t.Run(name, func(t *testing.T) {
			f := speechFixture(t)
			f.c.Service.TargetSeparated = true
			f.c.Service.ProviderType = constant.ProviderTypeCentralized
			f.c.reconciliationDB = &mockReconciliationDB{}
			f.c.serviceCache = cache.New(5*time.Minute, 10*time.Minute)
			f.c.serviceCache.Set("current_service", model.Service{InputPrice: "1", OutputPrice: "1"}, cache.DefaultExpiration)

			body, contentType := transcriptionBody(t, func(w *multipart.Writer) {
				if streaming {
					if err := w.WriteField("stream", "true"); err != nil {
						t.Fatalf("WriteField: %v", err)
					}
				}
			})

			rec := httptest.NewRecorder()
			w := &closeNotifyRecorder{ResponseRecorder: rec, closed: make(chan bool, 1)}
			gin.SetMode(gin.TestMode)
			ctx, _ := gin.CreateTestContext(w)
			ctx.Request = httptest.NewRequest("POST", "/v1/proxy/audio/transcriptions", nil)
			ctx.Request.Header.Set("Content-Type", contentType)
			// No E2EE keys set: an ordinary, unsealed client.
			//
			// The upstream TLS fingerprint the proxy captures for a centralized 200.
			// Without it routingProofOverHashes refuses to sign — deliberately, since
			// a proof with no TLS evidence gives verifiers false confidence — so a
			// fixture that omitted it would show "no signature" for a reason that has
			// nothing to do with the gate under test.
			ctx.Set(CtxKeyUpstreamCertFingerprint, strings.Repeat("ab", 32))

			upstream := `{"model":"whisper-large-v3","usage":{"type":"duration","seconds":3.5},"text":"hi"}`
			if streaming {
				upstream = "data: " + `{"type":"transcript.text.done","text":"hi","usage":{"type":"duration","seconds":3.5}}` + "\n\n"
			}
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{},
				Body:       io.NopCloser(strings.NewReader(upstream)),
			}
			_ = f.c.handleSpeechToTextResponse(ctx, resp, model.User{}, "", body, model.Request{IsWhitelisted: true, RequestHash: "req-1"})

			// The branch under test actually ran.
			if rec.Flushed != streaming {
				t.Fatalf("flushed = %v, want %v — the wrong branch ran", rec.Flushed, streaming)
			}
			chatKey := rec.Header().Get("ZG-Res-Key")
			if chatKey == "" {
				t.Fatal("a centralized provider must emit ZG-Res-Key: it is the broker that signs")
			}
			cached, ok := f.c.svcCache.Get(f.c.chatCacheKey(chatKey))
			if !ok {
				t.Fatal("ZG-Res-Key was emitted but nothing was cached for it")
			}
			sig, ok := cached.(ChatSignature)
			if !ok {
				t.Fatalf("cached value is %T, want ChatSignature", cached)
			}
			// A routing proof, not signChatWithKey's plain sha256(req):sha256(resp)
			// binding — ProviderType is set only by the centralized path.
			if sig.ProviderType == "" {
				t.Error("a centralized provider must cache a routing proof, not a plain request/response binding")
			}
		})
	}
}

// The predicate itself, on the shapes the handler test cannot reach: a non-JSON
// body must stay on the sealer's own "not a JSON object" refusal, which is
// already accurate, rather than being reported as a missing duration.
func TestSpeechLacksCleartextDuration(t *testing.T) {
	for _, tt := range []struct {
		body string
		want bool
	}{
		{`{"text":"hello"}`, true},
		{`{"text":"hello","usage":{"type":"tokens","input_tokens":14}}`, true},
		{`{"text":"hello","duration":"3.2"}`, true},
		{`{"text":"hello","usage":{"seconds":"3.2"}}`, true},
		// The protocol package refuses all three null shapes by name, measured:
		// "null is the absence of one, not a zero". A plain float64 decode would
		// accept null silently and read it as a genuine 0.
		{`{"text":"hello","usage":null}`, true},
		{`{"text":"hello","duration":null}`, true},
		{`{"text":"hello","usage":{"seconds":null}}`, true},
		{`{"text":"hello","duration":3.2}`, false},
		{`{"text":"hello","duration":0}`, false},
		{`{"text":"hello","usage":{"seconds":3.2}}`, false},
		{`{"text":"hello","usage":{"seconds":0}}`, false},
		// Not JSON objects: the sealer's refusal is the accurate one.
		{`a bare transcript`, false},
		{`null`, false},
		{`[]`, false},
		{`"hello"`, false},
		{``, false},
	} {
		t.Run(tt.body, func(t *testing.T) {
			if got := speechLacksCleartextDuration([]byte(tt.body)); got != tt.want {
				t.Errorf("speechLacksCleartextDuration(%s) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}
