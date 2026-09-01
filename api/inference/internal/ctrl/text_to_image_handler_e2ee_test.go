package ctrl

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/0gfoundation/0g-pc-e2ee/protocol/proof"
	"github.com/0gfoundation/0g-pc-e2ee/protocol/wire"
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/inference/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

// These exercise handleTextToImageResponse itself. The rest of the image e2ee
// tests sit a layer down (MaybeUnsealRequest / maybeSealNonStreamResponse /
// withImageUsage), which leaves the handler's own decisions — which body reaches
// the writer, which guard fires, whether the §8 signature survives the plaintext
// signing switch — with no coverage at all. Those are exactly the places a
// future edit breaks silently.

// newSealedImageHandlerCtx is the image analogue of newSealedHandlerCtx: it
// populates the E2EE context the way the proxy does, by sealing a real image
// request to this enclave and running it through MaybeUnsealRequest, so the
// sealed flag, the client ephemeral key and the §8 request binding hash are all
// set the way production sets them.
func (f *e2eeTestFixture) newSealedImageHandlerCtx(t *testing.T) (*gin.Context, *httptest.ResponseRecorder, wire.Request) {
	t.Helper()
	f.c.Service = config.Service{Type: constant.ServiceTypeTextToImage}
	f.c.reconciliationDB = &mockReconciliationDB{}

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(&closeNotifyRecorder{ResponseRecorder: rec, closed: make(chan bool, 1)})
	ctx.Request = httptest.NewRequest("POST", "/v1/images/generations", nil)

	sealedBytes := f.sealImageRequest(t, []string{"prompt"})
	if _, err := f.c.MaybeUnsealRequest(ctx, sealedBytes); err != nil {
		t.Fatalf("MaybeUnsealRequest: %v", err)
	}
	var reqEnv wire.Request
	if err := json.Unmarshal(sealedBytes, &reqEnv); err != nil {
		t.Fatalf("unmarshal sealed image request: %v", err)
	}
	return ctx, rec, reqEnv
}

func imageResp(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func whitelistImageReq(n int64) model.Request {
	return model.Request{IsWhitelisted: true, ServiceName: "text-to-image", RequestHash: "h", OutputCount: n}
}

// The sealed body — not the plaintext one — must be what reaches the writer, and
// the billable count must ride alongside it in the clear. This is the property
// the whole feature rests on, and nothing above this file checked that outBody
// (rather than clientBody) is the value passed to ctx.Writer.Write.
func TestHandleTextToImageResponse_WritesSealedBodyWithCleartextCount(t *testing.T) {
	f := newE2EEFixture(t)
	ctx, rec, reqEnv := f.newSealedImageHandlerCtx(t)

	const secret = "aW1hZ2VieXRlcw=="
	resp := imageResp(`{"created":1700000000,"data":[{"b64_json":"` + secret + `"}]}`)

	if err := f.c.handleTextToImageResponse(ctx, resp, model.User{}, "0",
		[]byte(`{"model":"z-image","prompt":"p"}`), whitelistImageReq(2)); err != nil {
		t.Fatalf("handleTextToImageResponse: %v", err)
	}

	out := rec.Body.String()
	if strings.Contains(out, secret) {
		t.Fatalf("plaintext image bytes reached the client: %s", out)
	}
	var frame wire.Response
	if err := json.Unmarshal([]byte(out), &frame); err != nil {
		t.Fatalf("response is not a JSON object: %v (%s)", err, out)
	}
	if _, still := frame["data"]; still {
		t.Error("data must be sealed, not cleartext")
	}

	// The router reads this without any key. One image was delivered even though
	// the request asked for two — the delivered count is what bills.
	var usage struct {
		OutputImages *int `json:"output_images"`
	}
	if err := json.Unmarshal(frame["usage"], &usage); err != nil {
		t.Fatalf("usage must be readable cleartext: %v", err)
	}
	if usage.OutputImages == nil || *usage.OutputImages != 1 {
		t.Errorf("usage.output_images = %v, want 1 (delivered), not the requested 2", usage.OutputImages)
	}

	opened, err := wire.OpenResponseFor(wire.ProfileImage, f.clientEphSk, frame)
	if err != nil {
		t.Fatalf("client OpenResponseFor: %v", err)
	}
	if !strings.Contains(string(opened["data"]), secret) {
		t.Errorf("opened data missing the images: %s", opened["data"])
	}

	// And the §8 signature must have survived the plaintext signing switch below
	// the flush — see TestHandleTextToImageResponse_SealedArmDoesNotResign.
	chatKey := rec.Header().Get("ZG-Res-Key")
	if chatKey == "" {
		t.Fatal("ZG-Res-Key must be advertised for a sealed request")
	}
	sig, err := f.c.GetChatSignature(chatKey)
	if err != nil {
		t.Fatalf("GetChatSignature: %v", err)
	}
	if err := brokerChatSig(sig).VerifyE2EE(reqEnv, frame, f.signerAddr, ethRecover); err != nil {
		t.Fatalf("client VerifyE2EE: %v", err)
	}
}

// The empty `case e2eeSealed:` arm in the signing switch. Nothing fails to
// compile if a future edit reorders that switch or drops the arm — the failure
// mode is that the plaintext signing path overwrites the cached §8 signature
// with one over a body the client never received, and VerifyE2EE then fails on a
// response that was otherwise perfectly good. Pin it by scheme tag.
func TestHandleTextToImageResponse_SealedArmDoesNotResign(t *testing.T) {
	f := newE2EEFixture(t)
	ctx, rec, _ := f.newSealedImageHandlerCtx(t)
	// A centralized service would otherwise sign a routing proof after the flush,
	// which is the arm most likely to swallow the sealed case if the switch is
	// reordered.
	f.c.Service = config.Service{Type: constant.ServiceTypeTextToImage, ProviderType: constant.ProviderTypeCentralized}

	resp := imageResp(`{"created":1,"data":[{"b64_json":"aW1n"}]}`)
	if err := f.c.handleTextToImageResponse(ctx, resp, model.User{}, "0",
		[]byte(`{"model":"z-image","prompt":"p"}`), whitelistImageReq(1)); err != nil {
		t.Fatalf("handleTextToImageResponse: %v", err)
	}

	sig, err := f.c.GetChatSignature(rec.Header().Get("ZG-Res-Key"))
	if err != nil {
		t.Fatalf("GetChatSignature: %v", err)
	}
	if !strings.HasPrefix(sig.Text, proof.SchemeE2EECiphertext+":") {
		t.Fatalf("cached signature is not the §8 ciphertext binding (%q) — the plaintext path overwrote it", sig.Text)
	}
}

// Sign BEFORE the flush (#619): a client that reads ZG-Res-Key and immediately
// fetches the signature must not race the cache write. The chat path has the
// same guard in signature_race_test.go; the image path reordered its sealed
// branch for the same reason and had nothing holding it there.
func TestHandleTextToImageResponse_SignsBeforeFlush(t *testing.T) {
	f := newE2EEFixture(t)
	f.c.Service = config.Service{Type: constant.ServiceTypeTextToImage}
	f.c.reconciliationDB = &mockReconciliationDB{}

	probe := &sigProbeWriter{}
	var sigCachedAtFlush bool
	var chatKeyAtFlush string
	probe.onFirstWrite = func() {
		chatKeyAtFlush = probe.Header().Get("ZG-Res-Key")
		if chatKeyAtFlush == "" {
			return
		}
		if _, err := f.c.GetChatSignature(chatKeyAtFlush); err == nil {
			sigCachedAtFlush = true
		}
	}

	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(probe)
	ctx.Request = httptest.NewRequest("POST", "/v1/images/generations", nil)
	if _, err := f.c.MaybeUnsealRequest(ctx, f.sealImageRequest(t, []string{"prompt"})); err != nil {
		t.Fatalf("MaybeUnsealRequest: %v", err)
	}

	resp := imageResp(`{"created":1,"data":[{"b64_json":"aW1n"}]}`)
	if err := f.c.handleTextToImageResponse(ctx, resp, model.User{}, "0",
		[]byte(`{"model":"z-image","prompt":"p"}`), whitelistImageReq(1)); err != nil {
		t.Fatalf("handleTextToImageResponse: %v", err)
	}

	if chatKeyAtFlush == "" {
		t.Fatal("ZG-Res-Key was not set before the body flush")
	}
	if !sigCachedAtFlush {
		t.Fatal("signature was not cached before the sealed body was flushed (issue #619)")
	}
}

// Guard 2: an undecodable 200 leaves no honest count to publish. Refuse rather
// than seal a response asking the router to bill a number the enclave never
// verified — and, critically, do not write the provider's bytes either.
func TestHandleTextToImageResponse_RefusesUndecodableSealedResponse(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"no decodable images", `{"created":1,"data":[{"url":"http://10.0.0.1/a.png"}]}`},
		{"empty data array", `{"created":1,"data":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newE2EEFixture(t)
			ctx, rec, _ := f.newSealedImageHandlerCtx(t)

			err := f.c.handleTextToImageResponse(ctx, imageResp(tc.body), model.User{}, "0",
				[]byte(`{"model":"z-image","prompt":"p"}`), whitelistImageReq(1))
			if err == nil {
				t.Fatal("an unbillable sealed image response must be refused")
			}
			if !strings.Contains(err.Error(), "e2ee") {
				t.Errorf("error should name e2ee, got: %v", err)
			}
			if strings.Contains(rec.Body.String(), "10.0.0.1") {
				t.Errorf("provider bytes must not be forwarded: %s", rec.Body.String())
			}
			// Attributed upstream: a 200 the broker cannot decode is the provider
			// misbehaving, so it must trip the upstream-fault alert rather than
			// being counted against the caller.
			if got, _ := ctx.Get(monitor.CtxKeyFailureSource); got != monitor.FailureSourceUpstream {
				t.Errorf("failure source = %v, want upstream", got)
			}
		})
	}
}

// Guard 1: response_format=url puts the plaintext images outside the sealed
// channel. Unreachable in production — the protocol's pinned-cleartext check
// rejects such a request at unseal time — but this is the backstop if that pin
// is ever relaxed, and it is attributed to the CLIENT, unlike guard 2.
func TestHandleTextToImageResponse_RefusesSealedURLMode(t *testing.T) {
	f := newE2EEFixture(t)
	ctx, rec, _ := f.newSealedImageHandlerCtx(t)
	// Force the state the pin normally makes impossible.
	ctx.Set("clientResponseFormat", "url")

	err := f.c.handleTextToImageResponse(ctx, imageResp(`{"created":1,"data":[{"b64_json":"aW1n"}]}`),
		model.User{}, "0", []byte(`{"model":"z-image","prompt":"p"}`), whitelistImageReq(1))
	if err == nil {
		t.Fatal("url mode must be refused for a sealed request")
	}
	if !strings.Contains(err.Error(), "b64_json") {
		t.Errorf("error should tell the caller what to use instead, got: %v", err)
	}
	// An error body is written (handleBrokerError), but the images never are —
	// which is the property that matters.
	if strings.Contains(rec.Body.String(), "aW1n") {
		t.Errorf("images must not be forwarded for a refused sealed response: %s", rec.Body.String())
	}
	// NOT upstream: asking for a format this mode cannot honour is the caller's
	// error. Guard 2 above overrides to upstream; this one must not.
	if got, exists := ctx.Get(monitor.CtxKeyFailureSource); exists && got == monitor.FailureSourceUpstream {
		t.Error("a client-chosen incompatible format must not be attributed upstream")
	}
}

// The plaintext image path must be untouched by all of the above: no
// usage.output_images, no sealing, body forwarded as-is.
func TestHandleTextToImageResponse_PlaintextPathUnchanged(t *testing.T) {
	f := newE2EEFixture(t)
	f.c.Service = config.Service{Type: constant.ServiceTypeTextToImage}
	f.c.reconciliationDB = &mockReconciliationDB{}

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(&closeNotifyRecorder{ResponseRecorder: rec, closed: make(chan bool, 1)})
	ctx.Request = httptest.NewRequest("POST", "/v1/images/generations", nil)
	// No MaybeUnsealRequest → not sealed.

	const img = "aW1hZ2VieXRlcw=="
	if err := f.c.handleTextToImageResponse(ctx, imageResp(`{"created":1,"data":[{"b64_json":"`+img+`"}]}`),
		model.User{}, "0", []byte(`{"model":"z-image","prompt":"p"}`), whitelistImageReq(1)); err != nil {
		t.Fatalf("handleTextToImageResponse: %v", err)
	}

	out := rec.Body.Bytes()
	if !bytes.Contains(out, []byte(img)) {
		t.Fatalf("the plaintext path must forward the images: %s", out)
	}
	var resp map[string]json.RawMessage
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("plaintext response is not a JSON object: %v", err)
	}
	if _, sealed := resp["_e2ee"]; sealed {
		t.Error("an unsealed request must not produce a sealed frame")
	}
	if raw, ok := resp["usage"]; ok && strings.Contains(string(raw), "output_images") {
		t.Errorf("usage.output_images must not be written on the plaintext path: %s", raw)
	}
}
