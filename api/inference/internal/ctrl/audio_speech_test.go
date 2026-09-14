package ctrl

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// newAudioSpeechCtrl builds a Ctrl with the real collaborators the synchronous
// handler touches. The reconciliation sink is a fake because it is an interface;
// everything else is the production type.
func newAudioSpeechCtrl(t *testing.T) (*Ctrl, *fakeReconciliationDB) {
	t.Helper()
	recon := &fakeReconciliationDB{}
	return &Ctrl{
		logger:           testLogger(),
		reconciliationDB: recon,
	}, recon
}

// ginCtxWithRequest returns a gin context carrying a real request, plus the
// recorder its response is written to.
func ginCtxWithRequest(t *testing.T, body string, contentType string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/proxy/audio/speech", bytes.NewBufferString(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	ctx.Request = req
	return ctx, rec
}

// upstreamAudioResponse fakes what the adaptor returns: audio bytes, plus the
// duration header when one is given.
func upstreamAudioResponse(audio string, durationHeader string) *http.Response {
	h := http.Header{}
	if durationHeader != "" {
		h.Set(AudioDurationHeader, durationHeader)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     h,
		Body:       io.NopCloser(bytes.NewBufferString(audio)),
	}
}

// Exercises the REAL resolver, not a reimplementation of it. An earlier version of
// this file trimmed the header itself and called the parse helper directly, which
// meant production's own trimming was never executed — if it changed, the test
// would still pass.
func TestResolveAudioSpeechSeconds(t *testing.T) {
	tests := []struct {
		name       string
		header     string
		wantSecs   int64
		wantSource audioQuantitySource
	}{
		{name: "whole seconds", header: "48", wantSecs: 48, wantSource: audioQuantityUsage},
		// Seed Audio reports original_duration as a float.
		{name: "fractional rounds up", header: "47.2", wantSecs: 48, wantSource: audioQuantityUsage},
		{name: "whole number expressed fractionally", header: "47.0", wantSecs: 47, wantSource: audioQuantityUsage},
		{name: "sub-second still bills one", header: "0.4", wantSecs: 1, wantSource: audioQuantityUsage},
		{name: "surrounding whitespace is trimmed", header: "  48  ", wantSecs: 48, wantSource: audioQuantityUsage},

		// Every unusable header falls back. With no vendor rules configured the
		// reserve resolves to 0 and is floored to 1 — billing zero would read as a
		// free request, which is the one answer that hides the problem.
		{name: "absent", header: "", wantSecs: 1, wantSource: audioQuantityReserve},
		{name: "zero", header: "0", wantSecs: 1, wantSource: audioQuantityReserve},
		{name: "negative", header: "-5", wantSecs: 1, wantSource: audioQuantityReserve},
		{name: "not a number", header: "abc", wantSecs: 1, wantSource: audioQuantityReserve},
		{name: "unit suffix", header: "48s", wantSecs: 1, wantSource: audioQuantityReserve},
		{name: "absurd", header: "1e300", wantSecs: 1, wantSource: audioQuantityReserve},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newAudioSpeechCtrl(t)
			ctx, _ := ginCtxWithRequest(t, `{"model":"seed-audio-1.0","input":"hi"}`, "application/json")
			h := http.Header{}
			if tt.header != "" {
				h.Set(AudioDurationHeader, tt.header)
			}

			secs, source := c.resolveAudioSpeechSeconds(ctx, h, []byte(`{"model":"seed-audio-1.0","input":"hi"}`))
			if secs != tt.wantSecs {
				t.Errorf("seconds = %d, want %d", secs, tt.wantSecs)
			}
			if source != tt.wantSource {
				t.Errorf("source = %q, want %q", source, tt.wantSource)
			}
		})
	}
}

// The handler end to end on the path that can run without a database: whitelisted
// traffic bills nothing but must still stream the audio and record the resolved
// duration for reconciliation.
//
// This is the test the previous version of this file was missing entirely — it
// covered a parsing helper and never ran the handler, which is where the audio
// actually reaches the client and the quantity actually reaches the books.
func TestHandleAudioSpeechResponse_StreamsAudioAndRecordsUsage(t *testing.T) {
	const audio = "RIFF....WAVEfake-audio-bytes"

	c, recon := newAudioSpeechCtrl(t)
	ctx, rec := ginCtxWithRequest(t, `{"model":"seed-audio-1.0","input":"hello"}`, "application/json")
	resp := upstreamAudioResponse(audio, "47.2")

	reqModel := whitelistedAudioRequest()
	if err := c.handleAudioSpeechResponse(ctx, resp, testUser(), "1000", []byte(`{"input":"hello"}`), reqModel); err != nil {
		t.Fatalf("handleAudioSpeechResponse: %v", err)
	}

	// The audio must reach the client BYTE FOR BYTE. This is an OpenAI /audio/speech
	// response: an SDK reads the body as audio, so any wrapping or re-encoding here
	// produces a corrupt file at the caller.
	if got := rec.Body.String(); got != audio {
		t.Errorf("client received %q, want the upstream bytes %q", got, audio)
	}

	if len(recon.rows) != 1 {
		t.Fatalf("reconciliation rows = %d, want 1 — whitelisted traffic has no Request row, so this rollup is its only record", len(recon.rows))
	}
	row := recon.rows[0]
	if row.OutputCount != 48 {
		t.Errorf("recorded %d seconds, want 48 (47.2 rounded up — the fee is charged in whole seconds)", row.OutputCount)
	}
	if row.ServiceType != "audio-generation" {
		t.Errorf("serviceType = %q, want audio-generation", row.ServiceType)
	}
	if row.Unit != "seconds" {
		t.Errorf("unit = %q, want seconds", row.Unit)
	}
	if !row.IsWhitelisted {
		t.Error("the rollup row is not marked whitelisted")
	}
}

// A response with no usable duration still delivers the audio and still records
// usage — at the fallback quantity. Serving audio while recording nothing would
// make the request invisible to reconciliation.
func TestHandleAudioSpeechResponse_MissingHeaderStillDeliversAndRecords(t *testing.T) {
	const audio = "fake-audio-no-header"

	c, recon := newAudioSpeechCtrl(t)
	ctx, rec := ginCtxWithRequest(t, `{"model":"seed-audio-1.0","input":"hello"}`, "application/json")
	resp := upstreamAudioResponse(audio, "")

	if err := c.handleAudioSpeechResponse(ctx, resp, testUser(), "1000", []byte(`{"input":"hello"}`), whitelistedAudioRequest()); err != nil {
		t.Fatalf("handleAudioSpeechResponse: %v", err)
	}

	if got := rec.Body.String(); got != audio {
		t.Errorf("client received %q, want %q — a missing duration must not cost the caller their audio", got, audio)
	}
	if len(recon.rows) != 1 {
		t.Fatalf("reconciliation rows = %d, want 1", len(recon.rows))
	}
	if recon.rows[0].OutputCount < 1 {
		t.Errorf("recorded %d seconds; the fallback must never record zero — that reads as a free request", recon.rows[0].OutputCount)
	}
}

// An empty upstream body is a vendor defect, not a client one. The handler must not
// panic or hang, and must still record the request.
func TestHandleAudioSpeechResponse_EmptyBody(t *testing.T) {
	c, recon := newAudioSpeechCtrl(t)
	ctx, rec := ginCtxWithRequest(t, `{"input":"hi"}`, "application/json")

	if err := c.handleAudioSpeechResponse(ctx, upstreamAudioResponse("", "12"), testUser(), "1000", []byte(`{"input":"hi"}`), whitelistedAudioRequest()); err != nil {
		t.Fatalf("handleAudioSpeechResponse: %v", err)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("client received %d bytes from an empty upstream body", rec.Body.Len())
	}
	if len(recon.rows) != 1 || recon.rows[0].OutputCount != 12 {
		t.Errorf("usage not recorded from the header on an empty body: %+v", recon.rows)
	}
}

// The header name is part of the adaptor contract across a process boundary.
// Renaming it breaks billing on every request at once — the fallback fires, every
// caller is charged the ceiling, and nothing else complains.
func TestAudioDurationHeaderName(t *testing.T) {
	if AudioDurationHeader != "X-0G-Audio-Duration-Seconds" {
		t.Errorf("AudioDurationHeader = %q; changing it silently breaks billing against every deployed adaptor", AudioDurationHeader)
	}
}

// whitelistedAudioRequest is the request shape the handler's no-billing path takes.
// Whitelisted traffic creates no Request row, which is exactly why these tests can
// run the real handler without a database — and why the rollup assertions above are
// the only record of the request that exists.
func whitelistedAudioRequest() model.Request {
	return model.Request{
		RequestHash:   "req-audio-1",
		UserAddress:   "0xUser",
		ServiceName:   constant.ServiceTypeAudioGeneration,
		IsWhitelisted: true,
	}
}

func testUser() model.User { return model.User{User: "0xUser"} }
