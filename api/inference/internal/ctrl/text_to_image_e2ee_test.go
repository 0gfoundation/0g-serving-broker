package ctrl

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/0gfoundation/0g-pc-e2ee/protocol/wire"

	"github.com/0glabs/0g-serving-broker/inference/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
)

// sealImageRequest builds a sealed /v1/images/generations envelope with the
// image profile (prompt sealed; model/n/size cleartext for routing and billing).
func (f *e2eeTestFixture) sealImageRequest(t *testing.T, sealedFields []string) []byte {
	t.Helper()
	req := wire.Request{
		"model":           mustRaw(t, "z-image"),
		"n":               mustRaw(t, 2),
		"size":            mustRaw(t, "1024x1024"),
		"response_format": mustRaw(t, "b64_json"),
		"prompt":          mustRaw(t, "top secret prompt"),
	}
	sealed, err := wire.SealRequestFor(wire.ProfileImage, f.encPub, req, sealedFields, f.signerAddr, f.clientEphPub)
	if err != nil {
		t.Fatalf("SealRequestFor(image): %v", err)
	}
	b, err := json.Marshal(sealed)
	if err != nil {
		t.Fatalf("marshal sealed image request: %v", err)
	}
	return b
}

func TestMaybeUnsealImageRequestReconstructsPrompt(t *testing.T) {
	f := newE2EEFixture(t)
	f.c.Service = config.Service{Type: constant.ServiceTypeTextToImage}
	ctx := newGinCtx()

	out, err := f.c.MaybeUnsealRequest(ctx, f.sealImageRequest(t, []string{"prompt"}))
	if err != nil {
		t.Fatalf("MaybeUnsealRequest: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal reconstructed request: %v", err)
	}
	if got["prompt"] != "top secret prompt" {
		t.Fatalf("prompt = %v, want the sealed prompt merged back", got["prompt"])
	}
	if got["model"] != "z-image" {
		t.Fatalf("model = %v, want the cleartext routing field preserved", got["model"])
	}
	if _, ok := got["_e2ee"]; ok {
		t.Fatal("the reconstructed request must not carry the envelope key")
	}
	if sealed, _ := ctx.Get(CtxKeyE2EESealed); sealed != true {
		t.Fatal("the response path must be told the request was sealed")
	}
}

// The enclave enforces the per-endpoint sealed-set policy itself, independently
// of the client-side guard: the reference client's SealRequestFor refuses to
// BUILD an image envelope that omits "prompt", but a third-party client is under
// no such obligation, so the enclave must refuse one too.
//
// The envelope here declares a sealed set the enclave must reject; the assertion
// on the message is what proves the policy check fired rather than the (later)
// AAD/Open failure any tampered envelope would also produce.
func TestMaybeUnsealImageRequestRejectsSealedSetWithoutPrompt(t *testing.T) {
	f := newE2EEFixture(t)
	f.c.Service = config.Service{Type: constant.ServiceTypeTextToImage}

	var env map[string]json.RawMessage
	if err := json.Unmarshal(f.sealImageRequest(t, []string{"prompt"}), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	var e2ee map[string]json.RawMessage
	if err := json.Unmarshal(env["_e2ee"], &e2ee); err != nil {
		t.Fatalf("unmarshal _e2ee: %v", err)
	}
	e2ee["sealed_fields"] = json.RawMessage(`["size"]`)
	env["_e2ee"] = mustRaw(t, e2ee)
	body, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	_, err = f.c.MaybeUnsealRequest(newGinCtx(), body)
	if err == nil {
		t.Fatal("expected the enclave to refuse an image request that does not seal its prompt")
	}
	if !strings.Contains(err.Error(), `must include "prompt"`) {
		t.Fatalf("expected the sealed-set policy to reject it, got: %v", err)
	}
}

// The broker's half of the policy: which endpoint maps to which wire profile,
// and which endpoints accept a sealed request at all. The RULE each profile
// then applies lives in the protocol package (wire.ValidateSealedFieldsFor,
// reached via wire.OpenRequestFor) — this only decides which rule to apply.
func TestProfileForServiceType(t *testing.T) {
	tests := []struct {
		name         string
		svcType      string
		wantProfile  wire.Profile
		wantSealable bool
	}{
		{"chatbot", constant.ServiceTypeChatbot, wire.ProfileChat, true},
		{"text-to-image", constant.ServiceTypeTextToImage, wire.ProfileImage, true},
		// An ALLOWLIST, not a switch with a default. The multipart shapes cannot
		// be envelopes at all; video-generation and anything added later simply
		// have no profile specified, and guessing one would apply the wrong rule
		// to a request shape nobody has analyzed.
		{"speech-to-text", constant.ServiceTypeSpeechToText, "", false},
		{"image-editing", constant.ServiceTypeImageEditing, "", false},
		{"video-generation", constant.ServiceTypeVideoGeneration, "", false},
		{"a service type that does not exist yet", "some-future-type", "", false},
		{"unset", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotProfile, gotSealable := profileForServiceType(tt.svcType)
			if gotSealable != tt.wantSealable {
				t.Fatalf("sealable = %v, want %v", gotSealable, tt.wantSealable)
			}
			if gotProfile != tt.wantProfile {
				t.Fatalf("profile = %q, want %q", gotProfile, tt.wantProfile)
			}
		})
	}
}

// The multipart service types have no envelope format yet (SPEC §1 scope note),
// so a JSON envelope arriving on one of them is refused rather than unsealed
// into a body the multipart upstream cannot consume.
func TestMaybeUnsealRejectsSealedRequestOnMultipartServiceTypes(t *testing.T) {
	for _, svcType := range []string{constant.ServiceTypeSpeechToText, constant.ServiceTypeImageEditing} {
		t.Run(svcType, func(t *testing.T) {
			f := newE2EEFixture(t)
			f.c.Service = config.Service{Type: svcType}
			if _, err := f.c.MaybeUnsealRequest(newGinCtx(), f.sealRequest(t, f.signerAddr)); err == nil {
				t.Fatalf("expected a sealed request on %q to be refused", svcType)
			}
		})
	}
}

// The chat path is unaffected by the new policy check.
func TestMaybeUnsealChatRequestUnaffectedByProfilePolicy(t *testing.T) {
	f := newE2EEFixture(t)
	f.c.Service = config.Service{Type: constant.ServiceTypeChatbot}
	if _, err := f.c.MaybeUnsealRequest(newGinCtx(), f.sealRequest(t, f.signerAddr)); err != nil {
		t.Fatalf("chat request must still unseal: %v", err)
	}
}

// End to end for the response half: the enclave seals data[] and publishes the
// delivered count as cleartext usage.output_images, so the router bills on a
// field it can read while the images stay sealed to the client.
func TestSealedImageResponseHidesImagesAndPublishesBillableCount(t *testing.T) {
	f := newE2EEFixture(t)
	f.c.Service = config.Service{Type: constant.ServiceTypeTextToImage}
	ctx := newGinCtx()
	if _, err := f.c.MaybeUnsealRequest(ctx, f.sealImageRequest(t, []string{"prompt"})); err != nil {
		t.Fatalf("unseal: %v", err)
	}

	// The provider clamped n=2 to one image; the delivered count is what bills.
	provider := `{"created":1700000000,"data":[{"b64_json":"aW1hZ2VieXRlcw"}]}`
	withUsage, err := withImageUsage([]byte(provider), 1)
	if err != nil {
		t.Fatalf("withImageUsage: %v", err)
	}
	out, isSealed, _, err := f.c.maybeSealNonStreamResponse(ctx, withUsage, wire.ProfileImage)
	if err != nil || !isSealed {
		t.Fatalf("seal image response: sealed=%v err=%v", isSealed, err)
	}

	if strings.Contains(string(out), "aW1hZ2VieXRlcw") {
		t.Fatal("image bytes must not appear in the sealed frame")
	}

	var frame wire.Response
	if err := json.Unmarshal(out, &frame); err != nil {
		t.Fatalf("unmarshal sealed frame: %v", err)
	}
	if _, ok := frame["data"]; ok {
		t.Fatal("data must be sealed, not cleartext")
	}
	// The router reads this without any key.
	var usage struct {
		OutputImages int `json:"output_images"`
	}
	if err := json.Unmarshal(frame["usage"], &usage); err != nil {
		t.Fatalf("usage must be readable cleartext: %v", err)
	}
	if usage.OutputImages != 1 {
		t.Fatalf("usage.output_images = %d, want the delivered count 1", usage.OutputImages)
	}

	// The client recovers the images.
	opened, err := wire.OpenResponseFor(wire.ProfileImage, f.clientEphSk, frame)
	if err != nil {
		t.Fatalf("client open: %v", err)
	}
	var data []struct {
		B64JSON string `json:"b64_json"`
	}
	if err := json.Unmarshal(opened["data"], &data); err != nil {
		t.Fatalf("decode opened data: %v", err)
	}
	if len(data) != 1 || data[0].B64JSON != "aW1hZ2VieXRlcw" {
		t.Fatalf("opened data = %+v", data)
	}
}

// usage.output_images is BOUND (not in unbound_fields), so a router that
// inflates the count to over-bill breaks the client's Open instead of going
// unnoticed.
func TestSealedImageResponseDetectsTamperedBillableCount(t *testing.T) {
	f := newE2EEFixture(t)
	f.c.Service = config.Service{Type: constant.ServiceTypeTextToImage}
	ctx := newGinCtx()
	if _, err := f.c.MaybeUnsealRequest(ctx, f.sealImageRequest(t, []string{"prompt"})); err != nil {
		t.Fatalf("unseal: %v", err)
	}

	withUsage, err := withImageUsage([]byte(`{"data":[{"b64_json":"aW1n"}]}`), 1)
	if err != nil {
		t.Fatalf("withImageUsage: %v", err)
	}
	out, _, _, err := f.c.maybeSealNonStreamResponse(ctx, withUsage, wire.ProfileImage)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	var frame wire.Response
	if err := json.Unmarshal(out, &frame); err != nil {
		t.Fatalf("unmarshal frame: %v", err)
	}
	frame["usage"] = json.RawMessage(`{"output_images":99}`)
	if _, err := wire.OpenResponseFor(wire.ProfileImage, f.clientEphSk, frame); err == nil {
		t.Fatal("an inflated usage.output_images must fail the client's Open")
	}
}

func TestWithImageUsage(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		imageNum  int64
		wantUsage string
		wantErr   bool
		wantKeeps string // a top-level key that must survive
	}{
		{
			name:      "adds usage to a response that has none",
			body:      `{"created":1,"data":[]}`,
			imageNum:  2,
			wantUsage: `{"output_images":2}`,
			wantKeeps: "created",
		},
		{
			name:      "preserves other usage keys and overrides images",
			body:      `{"data":[],"usage":{"output_images":9,"input_tokens":7}}`,
			imageNum:  3,
			wantUsage: `{"output_images":3,"input_tokens":7}`,
			wantKeeps: "data",
		},
		{
			name:      "replaces a non-object usage rather than failing",
			body:      `{"data":[],"usage":"weird"}`,
			imageNum:  1,
			wantUsage: `{"output_images":1}`,
			wantKeeps: "data",
		},
		{
			name:    "rejects a non-object body",
			body:    `[1,2,3]`,
			wantErr: true,
		},
		{
			name:    "rejects a null body",
			body:    `null`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := withImageUsage([]byte(tt.body), tt.imageNum)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("withImageUsage: %v", err)
			}
			var got map[string]json.RawMessage
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("unmarshal result: %v", err)
			}
			var gotUsage, wantUsage map[string]any
			if err := json.Unmarshal(got["usage"], &gotUsage); err != nil {
				t.Fatalf("decode usage: %v", err)
			}
			if err := json.Unmarshal([]byte(tt.wantUsage), &wantUsage); err != nil {
				t.Fatalf("decode want: %v", err)
			}
			if len(gotUsage) != len(wantUsage) {
				t.Fatalf("usage = %s, want %s", got["usage"], tt.wantUsage)
			}
			for k, v := range wantUsage {
				if gotUsage[k] != v {
					t.Fatalf("usage[%s] = %v, want %v", k, gotUsage[k], v)
				}
			}
			if _, ok := got[tt.wantKeeps]; !ok {
				t.Fatalf("top-level field %q was dropped", tt.wantKeeps)
			}
		})
	}
}

// ensureSealedFieldsPresent must cover whichever profile's fields it is given,
// so a frame legitimately missing one (a usage-only chat chunk) still seals.
func TestEnsureSealedFieldsPresent(t *testing.T) {
	frame := wire.Response{"usage": json.RawMessage(`{}`)}
	ensureSealedFieldsPresent(frame, e2eeChatResponseSealedFields)
	if string(frame["choices"]) != "[]" {
		t.Fatalf("choices = %s, want an injected empty array", frame["choices"])
	}

	frame = wire.Response{"data": json.RawMessage(`[{"b64_json":"x"}]`)}
	ensureSealedFieldsPresent(frame, e2eeImageResponseSealedFields)
	if string(frame["data"]) != `[{"b64_json":"x"}]` {
		t.Fatalf("an existing sealed field must not be overwritten, got %s", frame["data"])
	}
}

// The enclave half of the §7.1 pin: sealing the prompt does not stop a cleartext
// response_format from telling this broker to publish the images from a plain
// URL. Checked pre-inference, and required rather than merely "not url" — the
// OpenAI default for the DALL·E family IS url, so silence is the leak.
func TestMaybeUnsealImageRequestRequiresExplicitB64ResponseFormat(t *testing.T) {
	tests := []struct {
		name           string
		responseFormat json.RawMessage // nil = field omitted entirely
		wantErr        bool
	}{
		{"explicit b64_json", mustRawJSON(t, `"b64_json"`), false},
		{"explicit url", mustRawJSON(t, `"url"`), true},
		{"omitted — the server default is url", nil, true},
		{"null", mustRawJSON(t, `null`), true},
		{"non-string", mustRawJSON(t, `7`), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newE2EEFixture(t)
			f.c.Service = config.Service{Type: constant.ServiceTypeTextToImage}

			// Build the envelope, then set response_format on the CLEARTEXT half —
			// it is not sealed, so this is exactly what a non-conforming client can
			// put on the wire.
			var env map[string]json.RawMessage
			if err := json.Unmarshal(f.sealImageRequest(t, []string{"prompt"}), &env); err != nil {
				t.Fatalf("unmarshal envelope: %v", err)
			}
			if tt.responseFormat == nil {
				delete(env, "response_format")
			} else {
				env["response_format"] = tt.responseFormat
			}
			body, err := json.Marshal(env)
			if err != nil {
				t.Fatalf("marshal envelope: %v", err)
			}

			_, err = f.c.MaybeUnsealRequest(newGinCtx(), body)
			if !tt.wantErr {
				// Mutating a bound cleartext field breaks the AAD, so the only case
				// that can succeed is the untouched b64_json one.
				if err != nil {
					t.Fatalf("a conforming sealed image request must unseal: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected the enclave to refuse this sealed image request")
			}
			if !strings.Contains(err.Error(), "response_format") {
				t.Fatalf("expected the response_format pin to reject it, got: %v", err)
			}
		})
	}
}

// Chat is unaffected: it has no pinned cleartext field, and a chat request
// carrying its own response_format (JSON mode) must pass through.
func TestMaybeUnsealChatRequestHasNoResponseFormatPin(t *testing.T) {
	f := newE2EEFixture(t)
	f.c.Service = config.Service{Type: constant.ServiceTypeChatbot}
	if _, err := f.c.MaybeUnsealRequest(newGinCtx(), f.sealRequest(t, f.signerAddr)); err != nil {
		t.Fatalf("a chat request without response_format must still unseal: %v", err)
	}
}

func mustRawJSON(t *testing.T, s string) json.RawMessage {
	t.Helper()
	if !json.Valid([]byte(s)) {
		t.Fatalf("invalid test JSON %q", s)
	}
	return json.RawMessage(s)
}

// The image response path must seal under the IMAGE profile, not the chat one.
// Both profiles produce the same wire format, so a mix-up is invisible in the
// output — what differs is that the image profile refuses a final frame with no
// cleartext usage.output_images (SPEC §7.1), and the chat profile knows of no
// such rule. Sealing images through the chat profile therefore ships a frame the
// router bills as zero images, silently.
func TestSealedImageResponseWithoutBillableCountIsRefused(t *testing.T) {
	f := newE2EEFixture(t)
	f.c.Service = config.Service{Type: constant.ServiceTypeTextToImage}
	ctx := newGinCtx()
	if _, err := f.c.MaybeUnsealRequest(ctx, f.sealImageRequest(t, []string{"prompt"})); err != nil {
		t.Fatalf("unseal: %v", err)
	}

	// withImageUsage was skipped (or the count could not be determined).
	noCount := []byte(`{"created":1700000000,"data":[{"b64_json":"aW1hZ2VieXRlcw"}]}`)
	out, isSealed, _, err := f.c.maybeSealNonStreamResponse(ctx, noCount, wire.ProfileImage)
	if err == nil {
		t.Fatal("an image response with no billable count must not be sealed")
	}
	if !strings.Contains(err.Error(), "output_images") {
		t.Fatalf("error should name the missing count, got: %v", err)
	}
	// Fail-closed: the caller must have nothing forwardable, since the plaintext
	// body still holds the images.
	if out != nil {
		t.Fatal("a failed seal must not hand back a body to forward")
	}
	if !isSealed {
		t.Fatal("the request WAS sealed; reporting otherwise would let the caller forward plaintext")
	}
}
