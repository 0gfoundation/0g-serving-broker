package ctrl

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/0gfoundation/0g-pc-e2ee/protocol/wire"
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/inference/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
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
