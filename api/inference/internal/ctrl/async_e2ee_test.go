package ctrl

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pccrypto "github.com/0gfoundation/0g-pc-e2ee/protocol/crypto"
	"github.com/0gfoundation/0g-pc-e2ee/protocol/proof"
	"github.com/0gfoundation/0g-pc-e2ee/protocol/wire"
	"github.com/patrickmn/go-cache"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// Async text-to-image E2EE (0g-pc SPEC §7.5) on the enclave side: opened at
// submit, sealed once at completion, stored as ciphertext, and opened by the
// sender's half of the protocol package exactly as the gateway will.

const asyncSecretPrompt = "the async secret prompt"

// Two 1x1 "images"; their bytes are what must never appear in the clear.
var asyncImageB64 = []string{
	base64.StdEncoding.EncodeToString([]byte("first-image-plaintext-bytes")),
	base64.StdEncoding.EncodeToString([]byte("second-image-plaintext-bytes")),
}

func asyncImageResponse(n int) string {
	data := make([]map[string]string, n)
	for i := range data {
		data[i] = map[string]string{"b64_json": asyncImageB64[i%len(asyncImageB64)]}
	}
	b, _ := json.Marshal(map[string]any{"created": 1, "data": data})
	return string(b)
}

type asyncE2EEFixture struct {
	*e2eeTestFixture
	store    *mockDB
	upstream *httptest.Server
	// received is the body the upstream was sent.
	received []byte
}

// newAsyncE2EEFixture is the sync E2EE fixture made a text-to-image provider
// with an async subsystem, whose upstream answers with status and body.
func newAsyncE2EEFixture(t *testing.T, status int, body string) *asyncE2EEFixture {
	t.Helper()
	f := &asyncE2EEFixture{e2eeTestFixture: newE2EEFixture(t), store: newMockDB()}
	f.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.received, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(f.upstream.Close)

	svcCache := cache.New(5*time.Minute, 10*time.Minute)
	svcCache.Set("current_service", model.Service{OutputPrice: "100"}, cache.DefaultExpiration)
	c := f.c
	c.Service.Type = constant.ServiceTypeTextToImage
	c.Service.TargetURL = f.upstream.URL
	c.asyncDB = f.store
	c.httpClient = &http.Client{Timeout: 10 * time.Second}
	c.asyncResultTTL = 5 * time.Minute
	c.asyncJobTimeout = time.Minute
	c.asyncJobQueue = make(chan asyncJobParams, 10)
	c.asyncEnabled = true
	c.whitelistUsers = map[string]struct{}{}
	c.serviceCache = svcCache
	return f
}

// sealSubmit seals an async-image submit the way the gateway does: `_bind`
// derived from the client's eph key, sealed with the prompt.
func (f *asyncE2EEFixture) sealSubmit(t *testing.T, n int) []byte {
	t.Helper()
	req := wire.Request{
		"model":           mustRaw(t, "z-image"),
		"n":               mustRaw(t, n),
		"response_format": mustRaw(t, "b64_json"),
		"prompt":          mustRaw(t, asyncSecretPrompt),
	}
	req, err := wire.AttachAsyncBind(req, f.clientEphSk)
	if err != nil {
		t.Fatalf("AttachAsyncBind: %v", err)
	}
	sealed, err := wire.SealRequestFor(wire.ProfileAsyncImage, f.encPub, req, nil, f.signerAddr, f.clientEphPub)
	if err != nil {
		t.Fatalf("SealRequestFor: %v", err)
	}
	b, err := json.Marshal(sealed)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// submitAndRun opens and submits body, then runs the queued job to completion.
func (f *asyncE2EEFixture) submitAndRun(t *testing.T, body []byte, whitelisted bool) string {
	t.Helper()
	sealed, err := f.c.UnsealAsyncRequest(constant.ServiceTypeTextToImage, body)
	if err != nil || sealed == nil {
		t.Fatalf("UnsealAsyncRequest: %v (sealed=%v)", err, sealed != nil)
	}
	jobID, err := f.c.SubmitAsyncJob(newTestGinContext(), "0xuser", constant.ServiceTypeTextToImage, []byte(`{"Content-Type":["application/json"]}`), body, whitelisted, sealed)
	if err != nil {
		t.Fatalf("SubmitAsyncJob: %v", err)
	}
	f.c.processAsyncJob(<-f.c.asyncJobQueue)
	return jobID
}

// assertNoPlaintext fails if any secret is visible in b.
func assertNoPlaintext(t *testing.T, where string, b []byte) {
	t.Helper()
	for _, secret := range append([]string{asyncSecretPrompt, "plaintext-bytes"}, asyncImageB64...) {
		if bytes.Contains(b, []byte(secret)) {
			t.Errorf("%s carries plaintext %q", where, secret)
		}
	}
}

func TestAsyncE2EE_RoundTrip(t *testing.T) {
	f := newAsyncE2EEFixture(t, http.StatusOK, asyncImageResponse(2))
	envelope := f.sealSubmit(t, 2)
	jobID := f.submitAndRun(t, envelope, true)

	// The upstream got the plaintext request, without the protocol's metadata.
	var sent map[string]json.RawMessage
	if err := json.Unmarshal(f.received, &sent); err != nil {
		t.Fatalf("upstream body: %v", err)
	}
	if string(sent["prompt"]) != `"`+asyncSecretPrompt+`"` {
		t.Errorf("upstream prompt = %s", sent["prompt"])
	}
	for _, k := range []string{"_bind", "_e2ee"} {
		if _, ok := sent[k]; ok {
			t.Errorf("upstream request carries %q", k)
		}
	}

	job, _ := f.store.GetAsyncJob(jobID)
	if job.Status != model.AsyncJobStatusCompleted {
		t.Fatalf("status = %s (%s)", job.Status, job.ErrorMessage)
	}
	if !bytes.Equal(job.RequestBody, envelope) {
		t.Error("the row must store the sealed envelope")
	}
	if len(job.ResponseBody) != 0 {
		t.Error("a sealed job must have no ResponseBody")
	}
	assertNoPlaintext(t, "the job row's request", job.RequestBody)
	assertNoPlaintext(t, "the stored frame", job.SealedFrame)
	assertNoPlaintext(t, "the stored ciphertext", job.SealedResult)

	// Every poll serves the same bytes.
	frameBytes, err := AssembleSealedAsyncFrame(job.SealedFrame, job.SealedResult)
	if err != nil {
		t.Fatalf("AssembleSealedAsyncFrame: %v", err)
	}
	again, _ := AssembleSealedAsyncFrame(job.SealedFrame, job.SealedResult)
	if !bytes.Equal(frameBytes, again) {
		t.Error("the frame must be the same bytes on every poll")
	}

	// The gateway's half opens it, with the router's x_0g_trace injected.
	var frame wire.Response
	if err := json.Unmarshal(frameBytes, &frame); err != nil {
		t.Fatalf("frame: %v", err)
	}
	if string(frame["usage"]) != `{"output_images":2}` {
		t.Errorf("cleartext usage = %s", frame["usage"])
	}
	frame["x_0g_trace"] = json.RawMessage(`{"request_id":"r"}`)
	opened, err := wire.OpenAsyncImageResult(f.clientEphSk, frame, jobID)
	if err != nil {
		t.Fatalf("OpenAsyncImageResult: %v", err)
	}
	if string(opened["status"]) != `"completed"` || string(opened["jobId"]) != `"`+jobID+`"` {
		t.Errorf("opened jobId/status = %s/%s", opened["jobId"], opened["status"])
	}
	if !bytes.Contains(opened["data"], []byte(asyncImageB64[1])) {
		t.Error("opened data does not carry the images")
	}

	// The §8 signature binds the stored envelope and the served frame.
	var headers map[string][]string
	_ = json.Unmarshal(job.ResponseHeaders, &headers)
	keys := headers["ZG-Res-Key"]
	if len(keys) != 1 {
		t.Fatalf("ZG-Res-Key = %v", keys)
	}
	sig, err := f.c.GetChatSignature(keys[0])
	if err != nil {
		t.Fatalf("GetChatSignature: %v", err)
	}
	var env, served map[string]json.RawMessage
	_ = json.Unmarshal(envelope, &env)
	_ = json.Unmarshal(frameBytes, &served)
	want, err := proof.SignedTextE2EE(env, served)
	if err != nil {
		t.Fatalf("SignedTextE2EE: %v", err)
	}
	if sig.Text != want {
		t.Errorf("signed text = %s, want %s", sig.Text, want)
	}
}

// Another client's eph key cannot open the frame, and a different job id is
// refused: what the gateway's checks key off is in the frame.
func TestAsyncE2EE_FrameIsBoundToTheJob(t *testing.T) {
	f := newAsyncE2EEFixture(t, http.StatusOK, asyncImageResponse(1))
	jobID := f.submitAndRun(t, f.sealSubmit(t, 1), true)
	job, _ := f.store.GetAsyncJob(jobID)
	frameBytes, _ := AssembleSealedAsyncFrame(job.SealedFrame, job.SealedResult)
	var frame wire.Response
	_ = json.Unmarshal(frameBytes, &frame)

	if _, err := wire.OpenAsyncImageResult(f.clientEphSk, frame, "another-job"); err == nil {
		t.Error("a frame must not open for another job id")
	}
	otherSk, _, _ := pccrypto.GenerateRecipientKey()
	if _, err := wire.OpenAsyncImageResult(otherSk, frame, jobID); err == nil {
		t.Error("a frame must not open under another eph key")
	}
}

// The job is billed on the images delivered, which is the count the frame
// publishes for the router to bill on — not the requested n.
func TestAsyncE2EE_BillsDeliveredCount(t *testing.T) {
	f := newAsyncE2EEFixture(t, http.StatusOK, asyncImageResponse(2))
	body := f.sealSubmit(t, 3)
	sealed, err := f.c.UnsealAsyncRequest(constant.ServiceTypeTextToImage, body)
	if err != nil {
		t.Fatalf("UnsealAsyncRequest: %v", err)
	}
	if err := f.store.CreateAsyncJob(model.AsyncJob{JobID: "job-bill", Status: model.AsyncJobStatusPending, ServiceType: constant.ServiceTypeTextToImage}); err != nil {
		t.Fatalf("CreateAsyncJob: %v", err)
	}
	f.c.processAsyncJob(asyncJobParams{
		JobID: "job-bill", ServiceType: constant.ServiceTypeTextToImage, RequestBody: sealed.Forward,
		BillingReq: model.Request{OutputCount: 3, RequestHash: "h"}, Sealed: sealed,
	})
	job, _ := f.store.GetAsyncJob("job-bill")
	if job.Status != model.AsyncJobStatusCompleted {
		t.Fatalf("status = %s (%s)", job.Status, job.ErrorMessage)
	}
	if f.store.billedOutputCount != 2 {
		t.Errorf("billed %d images, want the 2 delivered", f.store.billedOutputCount)
	}
	if !bytes.Contains(job.SealedFrame, []byte(`"usage":{"output_images":2}`)) {
		t.Errorf("frame usage does not publish 2: %s", job.SealedFrame)
	}
}

// A failed sealed job stores a fixed code, never upstream text, and serves no
// content.
func TestAsyncE2EE_FailureStoresAFixedCode(t *testing.T) {
	echo := `{"error":{"message":"rejected: ` + asyncSecretPrompt + `"}}`
	for _, tc := range []struct {
		name   string
		status int
		body   string
		n      int
		want   string
	}{
		{"upstream 4xx echoing the prompt", http.StatusBadRequest, echo, 1, "upstream_error:400"},
		{"upstream 5xx", http.StatusBadGateway, echo, 1, "upstream_error:502"},
		{"no images", http.StatusOK, `{"created":1,"data":[]}`, 1, "upstream_error"},
		{"not b64", http.StatusOK, `{"data":[{"url":"http://10.0.0.1/` + asyncSecretPrompt + `"}]}`, 1, "upstream_error"},
		{"more images than requested", http.StatusOK, asyncImageResponse(2), 1, "upstream_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAsyncE2EEFixture(t, tc.status, tc.body)
			jobID := f.submitAndRun(t, f.sealSubmit(t, tc.n), true)
			job, _ := f.store.GetAsyncJob(jobID)
			if job.Status != model.AsyncJobStatusFailed {
				t.Fatalf("status = %s", job.Status)
			}
			if job.ErrorMessage != tc.want {
				t.Errorf("errorMessage = %q, want %q", job.ErrorMessage, tc.want)
			}
			if len(job.SealedFrame) != 0 || len(job.ResponseBody) != 0 {
				t.Error("a failed sealed job must store no result")
			}
		})
	}
}

func TestUnsealAsyncRequest(t *testing.T) {
	f := newAsyncE2EEFixture(t, http.StatusOK, "")

	t.Run("unsealed is not opened", func(t *testing.T) {
		got, err := f.c.UnsealAsyncRequest(constant.ServiceTypeTextToImage, []byte(`{"prompt":"what is _e2ee?"}`))
		if got != nil || err != nil {
			t.Errorf("got %v, %v", got, err)
		}
	})
	t.Run("opened", func(t *testing.T) {
		got, err := f.c.UnsealAsyncRequest(constant.ServiceTypeTextToImage, f.sealSubmit(t, 1))
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		want, _ := wire.DeriveAsyncBind(f.clientEphSk)
		if !bytes.Equal(got.Bind, want) {
			t.Error("bind is not the request's")
		}
		if !bytes.Equal(got.ClientEphPub, f.clientEphPub) {
			t.Error("client eph pub is not the request's")
		}
		if bytes.Contains(got.Forward, []byte("_bind")) {
			t.Error("_bind must be removed before the upstream")
		}
	})
	t.Run("not on edits", func(t *testing.T) {
		if _, err := f.c.UnsealAsyncRequest(constant.ServiceTypeImageEditing, f.sealSubmit(t, 1)); err == nil {
			t.Error("a sealed edit must be refused")
		}
	})
	t.Run("not on a non-image provider", func(t *testing.T) {
		g := newAsyncE2EEFixture(t, http.StatusOK, "")
		g.c.Service.Type = constant.ServiceTypeChatbot
		if _, err := g.c.UnsealAsyncRequest(constant.ServiceTypeTextToImage, g.sealSubmit(t, 1)); err == nil {
			t.Error("a provider that is not text-to-image must refuse")
		}
	})
	t.Run("the sync image profile has no _bind", func(t *testing.T) {
		req := wire.Request{
			"model": mustRaw(t, "z-image"), "response_format": mustRaw(t, "b64_json"),
			"prompt": mustRaw(t, asyncSecretPrompt),
		}
		sealed, err := wire.SealRequestFor(wire.ProfileImage, f.encPub, req, nil, f.signerAddr, f.clientEphPub)
		if err != nil {
			t.Fatalf("SealRequestFor: %v", err)
		}
		b, _ := json.Marshal(sealed)
		if _, err := f.c.UnsealAsyncRequest(constant.ServiceTypeTextToImage, b); err == nil {
			t.Error("an envelope without _bind must be refused")
		}
	})
	t.Run("stale key is a key mismatch", func(t *testing.T) {
		g := newAsyncE2EEFixture(t, http.StatusOK, "")
		g.c.teeService.KeyID = []byte("rotated!")
		_, err := g.c.UnsealAsyncRequest(constant.ServiceTypeTextToImage, g.sealSubmit(t, 1))
		if !errors.Is(err, ErrE2EEKeyMismatch) {
			t.Errorf("err = %v, want ErrE2EEKeyMismatch", err)
		}
	})
}

func TestRefuseAsync_AdmitsSealedTextToImageOnly(t *testing.T) {
	env := []byte(`{"model":"z-image","_e2ee":{"v":1}}`)
	if why := (&Ctrl{}).RefuseAsync(constant.ServiceTypeTextToImage, "application/json", env); why != "" {
		t.Errorf("sealed text-to-image refused: %s", why)
	}
	if why := (&Ctrl{}).RefuseAsync(constant.ServiceTypeImageEditing, "application/json", env); !strings.Contains(why, "e2ee") {
		t.Errorf("sealed edit not refused: %q", why)
	}
}
