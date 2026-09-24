package handler

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/audiotranslator/internal/seedaudio"
	commonconfig "github.com/0glabs/0g-serving-broker/common/config"
	"github.com/0glabs/0g-serving-broker/common/log"
)

func testLogger() log.Logger {
	l, _ := log.GetLogger(&commonconfig.LoggerConfig{Format: "text", Level: "error"})
	return l
}

// vendorDouble records what the vendor received and replies with a recorded
// shape. Recorded, not emulated: it reproduces the response fields BytePlus's
// reference documents, which is the contract this sidecar translates.
type vendorDouble struct {
	gotBody    []byte
	gotHeaders http.Header
	reply      string
	status     int
}

func (v *vendorDouble) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v.gotBody, _ = io.ReadAll(r.Body)
		v.gotHeaders = r.Header.Clone()
		status := v.status
		if status == 0 {
			status = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(v.reply))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func post(t *testing.T, h *AudioHandler, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	c.Request = req
	h.Speech(c)
	return rec
}

// The broker calls targetUrl + "/audio/speech" (it strips any leading /v1), so
// that path must be registered — not only the OpenAI-style /v1 one. Exercised
// through Routes, the same registration main uses, with the handler's own
// validation answering: a 400 for an empty input proves the route reached Speech
// rather than gin's 404.
func TestRoutesServeTheUnprefixedPathTheBrokerCalls(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	Routes(engine, NewAudioHandler(seedaudio.NewClient("http://unused.invalid", http.DefaultClient), testLogger()))

	for _, path := range []string{"/audio/speech", "/v1/audio/speech"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"input":""}`))
		req.Header.Set("Content-Type", "application/json")
		engine.ServeHTTP(rec, req)
		if rec.Code == http.StatusNotFound {
			t.Errorf("POST %s = 404; the broker's requests would never reach the adaptor", path)
		}
		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST %s = %d, want 400 from Speech's own validation", path, rec.Code)
		}
	}
}

func okReply(audio string, duration, originalDuration string) string {
	return `{"code":0,"message":"ok","audio":"` + base64.StdEncoding.EncodeToString([]byte(audio)) +
		`","url":"https://asset.example/x","duration":` + duration +
		`,"original_duration":` + originalDuration + `}`
}

// The whole contract in one test: OpenAI request in, raw audio out, billable
// duration in the header — and the duration must be original_duration.
func TestSpeech_ReturnsRawAudioAndBillableDuration(t *testing.T) {
	const audio = "ID3\x04fake-mp3-bytes"
	v := &vendorDouble{reply: okReply(audio, "24", "48")} // diverging: a 2x-speed request
	client := seedaudio.NewClient(v.server(t).URL, http.DefaultClient)

	rec := post(t, NewAudioHandler(client, testLogger()),
		`{"model":"seed-audio-1.0","input":"hello","response_format":"mp3","speed":2.0}`,
		map[string]string{seedaudio.HeaderAPIKey: "k"})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	// The body must be the AUDIO, byte for byte — an OpenAI SDK writes it to a
	// file, so any wrapping or re-encoding produces a corrupt download.
	if rec.Body.String() != audio {
		t.Errorf("body = %q, want the raw audio %q", rec.Body.String(), audio)
	}
	if got := rec.Header().Get("Content-Type"); got != "audio/mpeg" {
		t.Errorf("Content-Type = %q, want audio/mpeg", got)
	}
	// 48, not 24: original_duration bills.
	if got := rec.Header().Get(DurationHeader); got != "48" {
		t.Errorf("%s = %q, want 48 — the post-processed 24 must not bill", DurationHeader, got)
	}
}

// The vendor authenticates with a header SET, not a bearer token. A single-value
// auth parameter would drop everything but the first and fail every call with
// what looks like a bad key.
func TestSpeech_ForwardsCredentialHeaderSet(t *testing.T) {
	v := &vendorDouble{reply: okReply("x", "5", "5")}
	client := seedaudio.NewClient(v.server(t).URL, http.DefaultClient)

	post(t, NewAudioHandler(client, testLogger()), `{"input":"hi"}`, map[string]string{
		seedaudio.HeaderAPIKey:    "secret-key",
		seedaudio.HeaderRequestID: "req-uuid",
	})

	if got := v.gotHeaders.Get(seedaudio.HeaderAPIKey); got != "secret-key" {
		t.Errorf("%s not forwarded: %q", seedaudio.HeaderAPIKey, got)
	}
	if got := v.gotHeaders.Get(seedaudio.HeaderRequestID); got != "req-uuid" {
		t.Errorf("%s not forwarded: %q", seedaudio.HeaderRequestID, got)
	}
	if got := v.gotHeaders.Get("Authorization"); got != "" {
		t.Errorf("an Authorization header was sent (%q); BytePlus Voice does not use bearer auth", got)
	}
}

// The OpenAI body must arrive at the vendor in ITS shape.
func TestSpeech_TranslatesTheRequestBody(t *testing.T) {
	v := &vendorDouble{reply: okReply("x", "5", "5")}
	client := seedaudio.NewClient(v.server(t).URL, http.DefaultClient)

	post(t, NewAudioHandler(client, testLogger()),
		`{"model":"seed-audio-1.0","input":"@Audio1 hello","reference_audio":["https://example.com/a.mp3"],"response_format":"opus"}`,
		map[string]string{seedaudio.HeaderAPIKey: "k"})

	var sent seedaudio.CreateRequest
	if err := json.Unmarshal(v.gotBody, &sent); err != nil {
		t.Fatalf("vendor received unparseable body: %v", err)
	}
	if sent.TextPrompt != "@Audio1 hello" {
		t.Errorf("text_prompt = %q — OpenAI's `input` must become text_prompt", sent.TextPrompt)
	}
	if len(sent.References) != 1 || sent.References[0].AudioURL != "https://example.com/a.mp3" {
		t.Errorf("references = %+v", sent.References)
	}
	if sent.AudioConfig == nil || sent.AudioConfig.Format != "ogg_opus" {
		t.Errorf("format = %+v, want ogg_opus (OpenAI names the codec, the vendor the container)", sent.AudioConfig)
	}
}

// A bad request is refused BEFORE the vendor is called: a local failure costs
// nothing, while a vendor 400 arrives only after the broker has routed the
// request and taken a balance reserve.
func TestSpeech_RejectsBeforeCallingTheVendor(t *testing.T) {
	v := &vendorDouble{reply: okReply("x", "5", "5")}
	client := seedaudio.NewClient(v.server(t).URL, http.DefaultClient)

	rec := post(t, NewAudioHandler(client, testLogger()),
		`{"input":"hi","reference_audio":["https://a"],"reference_image":"https://b"}`, nil)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if v.gotBody != nil {
		t.Error("the vendor was called despite an invalid request")
	}
}

// A vendor failure must NOT return 200 with an empty body — the broker bills any
// 200, so that would charge for audio nobody received.
func TestSpeech_VendorFailureIsNotBillable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reply  string
		status int
	}{
		{name: "non-zero code in a 200", reply: `{"code":40001,"message":"quota exceeded"}`},
		{name: "empty audio in a 200", reply: `{"code":0,"audio":"","original_duration":5}`},
		{name: "http 500", reply: `{"code":50000,"message":"upstream down"}`, status: 500},
		{name: "http 400", reply: `{"code":40000,"message":"bad prompt"}`, status: 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := &vendorDouble{reply: tc.reply, status: tc.status}
			client := seedaudio.NewClient(v.server(t).URL, http.DefaultClient)

			rec := post(t, NewAudioHandler(client, testLogger()), `{"input":"hi"}`, nil)

			if rec.Code == http.StatusOK {
				t.Fatalf("status 200 on a vendor failure — the broker would bill this. body=%s", rec.Body.String())
			}
			if rec.Header().Get(DurationHeader) != "" {
				t.Error("a duration header was set on a failure")
			}
		})
	}
}

// Which vendor failures reach the caller as their own fault. The broker marks
// every non-429 4xx "client", and the router then neither fails over nor
// penalizes the provider — so a vendor 401/403 (the PROVIDER's key rejected)
// passed through read to every user as "your key is wrong" while the provider
// stayed in rotation. Only statuses the request itself causes pass through.
func TestSpeech_VendorStatusAttribution(t *testing.T) {
	for _, tc := range []struct {
		vendor int
		want   int
	}{
		{vendor: http.StatusBadRequest, want: http.StatusBadRequest},
		{vendor: http.StatusRequestEntityTooLarge, want: http.StatusRequestEntityTooLarge},
		{vendor: http.StatusUnprocessableEntity, want: http.StatusUnprocessableEntity},
		{vendor: http.StatusTooManyRequests, want: http.StatusTooManyRequests},

		{vendor: http.StatusUnauthorized, want: http.StatusBadGateway},
		{vendor: http.StatusPaymentRequired, want: http.StatusBadGateway},
		{vendor: http.StatusForbidden, want: http.StatusBadGateway},
		{vendor: http.StatusNotFound, want: http.StatusBadGateway},
		{vendor: http.StatusInternalServerError, want: http.StatusBadGateway},
	} {
		v := &vendorDouble{reply: `{"code":1,"message":"vendor says no"}`, status: tc.vendor}
		client := seedaudio.NewClient(v.server(t).URL, http.DefaultClient)
		rec := post(t, NewAudioHandler(client, testLogger()), `{"input":"hi"}`, nil)
		if rec.Code != tc.want {
			t.Errorf("vendor %d -> %d, want %d", tc.vendor, rec.Code, tc.want)
		}
	}
}

// No usable duration still delivers the audio. The broker falls back to the
// reserved ceiling, which over-bills — but withholding audio the vendor already
// charged us for is worse.
func TestSpeech_MissingDurationStillDeliversAudio(t *testing.T) {
	v := &vendorDouble{reply: `{"code":0,"audio":"` + base64.StdEncoding.EncodeToString([]byte("aud")) + `"}`}
	client := seedaudio.NewClient(v.server(t).URL, http.DefaultClient)

	rec := post(t, NewAudioHandler(client, testLogger()), `{"input":"hi"}`, nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Body.String() != "aud" {
		t.Errorf("body = %q, want the audio", rec.Body.String())
	}
	if got := rec.Header().Get(DurationHeader); got != "" {
		t.Errorf("%s = %q, want it omitted so the broker takes its documented fallback", DurationHeader, got)
	}
}

// The header name is a cross-process contract. Renaming it on one side alone
// makes every request fall back to the reserved ceiling — silently over-billing
// every caller, with a 200 and correct audio to hide it.
func TestDurationHeaderMatchesTheBrokerContract(t *testing.T) {
	if DurationHeader != "X-0G-Audio-Duration-Seconds" {
		t.Errorf("DurationHeader = %q; must match ctrl.AudioDurationHeader in the broker", DurationHeader)
	}
}
