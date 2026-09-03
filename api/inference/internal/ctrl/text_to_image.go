package ctrl

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/0gfoundation/0g-pc-e2ee/protocol/proof"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/middleware"
	"github.com/0glabs/0g-serving-broker/common/util"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

// maxB64ImageBytes caps the decoded size of a single image returned by the
// provider. Without this, a compromised or buggy provider could ship a single
// hundred-MB b64_json blob that the broker would buffer in memory (during
// io.ReadAll on the upstream body), decode in memory again, and then write to
// disk — amplifying a single request into a multi-hundred-MB allocation per
// image. 50 MiB is well above any realistic AI image output (DALL-E 3 PNGs
// top out around 4 MB) and still small enough that even N=10 (the OpenAI
// upper bound for "n") stays under 500 MiB worst-case.
const maxB64ImageBytes = 50 * 1024 * 1024

// imageResponseData mirrors the OpenAI image object inside data[].
type imageResponseData struct {
	B64JSON       string `json:"b64_json,omitempty"`
	URL           string `json:"url,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}

// imageResponseEnvelope is the top-level OpenAI image response shape.
type imageResponseEnvelope struct {
	Created int64               `json:"created"`
	Data    []imageResponseData `json:"data"`
}

// billableImageCount returns the number of images to bill for.
//
// A provider may return fewer images than requested — e.g. it silently clamps
// `n` to its per-model maximum (z-image caps at 2; router#354). Billing the
// requested count in that case over-charges the user for images they never
// received. When the response decoded cleanly we therefore bill the actual
// delivered count; extractB64Images already caps it at the request's
// OutputCount, so this is always <= requested and never over-charges.
//
// When the response was not decodable b64 (extractErr != nil, e.g. a multipart
// provider quirk) we cannot count delivered images, so fall back to the
// requested count rather than billing zero — otherwise an unparseable response
// would be served free.
func billableImageCount(requested int64, decoded int, extractErr error) int64 {
	if extractErr == nil {
		// Clean parse: bill exactly what was delivered, including 0 — a valid
		// response with no images must not be charged the requested count.
		return int64(decoded)
	}
	// Could not decode/count the response: fall back to the requested count
	// rather than billing zero, otherwise an unparseable response is free.
	return requested
}

// withImageUsage returns body with a top-level `usage.output_images` set to
// imageNum, the count of images the enclave actually delivered (SPEC §7.1).
//
// It exists for the sealed path: with data[] sealed the router cannot count the
// images, so the billable count has to ride alongside as a cleartext field. It
// stays BOUND (not in unbound_fields), so it is covered by the seal AAD and the
// §8 signature — the router reads it without decrypting, and a count that does
// not match the images fails the client's verify.
//
// It sits inside `usage` (that is where a billed quantity belongs) under the
// OpenAI `input_`/`output_` convention rather than a bare "images", which would
// squat an unqualified word in a vendor-defined object and read ambiguously once
// image-editing — which has INPUT images — gets a profile.
//
// Any usage object the upstream already sent is preserved (a token-billed model
// such as gpt-image-1 populates it); only "output_images" is overridden, since
// the broker's decoded count is the authority (an upstream may report the
// requested n rather than what it clamped to).
func withImageUsage(body []byte, imageNum int64) ([]byte, error) {
	var resp map[string]json.RawMessage
	if err := json.Unmarshal(body, &resp); err != nil || resp == nil {
		return nil, fmt.Errorf("attach usage.output_images: image response is not a JSON object: %w", err)
	}

	// Adopt the upstream's usage only when it decodes to an actual object.
	// Anything else — a string, a number, or `null` — is replaced rather than
	// failing the request: the field is the broker's to publish here, and the
	// image count is what matters.
	//
	// `null` is the case worth naming. Unmarshalling it into a map sets the map to
	// its ZERO VALUE and returns NO error, so decoding in place would leave a nil
	// map that the write below panics on ("assignment to entry in nil map") — and
	// the inference engine runs without gin.Recovery(), so that panic kills the
	// connection rather than producing an error: truncated response, no billing,
	// no failure attribution. Providers that always serialize `usage` emit exactly
	// this. Decoding into a separate variable makes the nil unreachable by
	// construction rather than something a later edit has to remember.
	usage := map[string]json.RawMessage{}
	if raw, ok := resp["usage"]; ok {
		var upstream map[string]json.RawMessage
		if err := json.Unmarshal(raw, &upstream); err == nil && upstream != nil {
			usage = upstream
		}
	}
	count, err := json.Marshal(imageNum)
	if err != nil {
		return nil, fmt.Errorf("attach usage.output_images: encode count: %w", err)
	}
	usage["output_images"] = count

	merged, err := json.Marshal(usage)
	if err != nil {
		return nil, fmt.Errorf("attach usage.output_images: encode usage: %w", err)
	}
	resp["usage"] = merged

	out, err := json.Marshal(resp)
	if err != nil {
		return nil, fmt.Errorf("attach usage.output_images: encode response: %w", err)
	}
	return out, nil
}

// GetTextToImageInputFeeAndImageNum gets input fee and imageNum for text-to-image generation
func (c *Ctrl) GetTextToImageInputFeeAndImageNum(reqBody []byte) (string, int64, error) {
	var request map[string]interface{}
	if err := json.Unmarshal(reqBody, &request); err != nil {
		return "", 0, errors.Wrap(err, "failed to unmarshal request body")
	}

	// Get imageNum parameter (prefer "imageNum", fallback to "num_inference_imageNum")
	imageNum := int64(1) // default to 1
	if imageNumVal, exists := request["n"]; exists {
		if imageNumFloat, ok := imageNumVal.(float64); ok {
			imageNum = int64(imageNumFloat)
		}
	}

	// Input fee is fixed at 0 (like zgStorage)
	expectedInputFee := "0"

	return expectedInputFee, imageNum, nil
}

// handleTextToImageResponse handles image generation response.
//
// Flow:
//  1. Read the provider response (always b64_json — enforced in PrepareHTTPRequest).
//  2. Decode each image and sign sha256(originalClientReq):sha256(img0),...
//  3. If the original client requested URL format, persist images to the local
//     image store and rewrite the response with broker-served URLs before sending
//     to the client.  Otherwise pass the b64 response through unchanged.
func (c *Ctrl) handleTextToImageResponse(ctx *gin.Context, resp *http.Response, account model.User, outputPrice string, reqBody []byte, reqModel model.Request) error {
	defer resp.Body.Close()

	// chatKey is always generated: it keys the image store and the broker-served
	// image URLs (buildURLResponse) regardless of signing. But ZG-Res-Key — the
	// signature-lookup handle — is only advertised when the response is actually
	// signed (broker-in-network or centralized routing proof). A standard/
	// TargetSeparated provider produces no signature, so emitting ZG-Res-Key would
	// point clients at a signature endpoint that only 404s.
	//
	// E2EE sealed is the third case (mirroring handleChargingResponse): the broker
	// TEE always signs the §8 ciphertext binding, so the client can fetch the
	// signature even from a TargetSeparated provider.
	chatKey := uuid.NewString()
	_, e2eeSealed := e2eeSealedRequest(ctx)
	if !c.Service.TargetSeparated || c.Service.IsCentralized() || e2eeSealed {
		ctx.Writer.Header().Set("ZG-Res-Key", chatKey)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.handleBrokerError(ctx, err, "read image response body")
		return err
	}

	// For forwarder providers, strip #184 upstream identity/cost leak fields before
	// the envelope is used for extraction, URL rewrite, signing, or forwarding —
	// sanitizing first keeps the (later) signature bound to the bytes the client
	// receives. Decode a compressed body first (the sync path forces identity
	// upstream; an upstream that ignores it would otherwise slip the leak past the
	// JSON parse). sanitizeResponseBody preserves data[] (image payloads).
	if c.Service.IsForwarder() {
		body = c.sanitizeForwarderResponseBody(ctx, body, resp.Header.Get("Content-Encoding"))
	}

	// Resolve the original client request body (pre-b64 rewrite) for signing.
	sigReqBody := reqBody
	if v, ok := ctx.Get("clientReqBody"); ok {
		if orig, ok := v.([]byte); ok {
			sigReqBody = orig
		}
	}

	// Extract decoded image bytes; fall back to signing the full response body
	// if the provider did not return b64_json (e.g. multipart provider quirks).
	// Cap at the request's declared output count so a compromised provider
	// cannot OOM the broker by returning a giant data array.
	images, extractErr := extractB64Images(body, int(reqModel.OutputCount))

	// Determine what the client originally asked for.
	originalFormat, _ := ctx.Get("clientResponseFormat")
	wantURL := originalFormat == "url"

	// E2EE (SPEC §7.1): url mode has the broker persist the images and hand back
	// broker-served URLs, which puts the plaintext images OUTSIDE the sealed
	// channel — anyone who can reach the URL reads them, defeating the point of
	// sealing. Refuse rather than silently downgrading to b64: the client asked
	// for a format this mode cannot honour, and it must learn that.
	//
	// Belt and braces, not the primary enforcement. `response_format` is a pinned
	// cleartext field of the image profile, so wire.OpenRequestFor already refused
	// this request at unseal time, in the proxy, before it ever reached a
	// provider — which is where a leak is PREVENTED rather than caught after the
	// images exist. By the time clientResponseFormat is read here it was taken
	// from the reconstructed plaintext, so for a sealed request it is "b64_json"
	// and this branch is unreachable. It stays because the two live in different
	// packages and nothing but this comment ties them together: if the pin is ever
	// relaxed, this is what keeps the images out of the clear.
	// Attributed to the client (via ignoreError), unlike the guards below: asking
	// for a format this mode cannot honour is the caller's error, not upstream's.
	if e2eeSealed && wantURL {
		ctx.Set("ignoreError", true)
		err := fmt.Errorf("e2ee: response_format=url is not supported for sealed requests (the images would be served in the clear); use b64_json")
		c.handleBrokerError(ctx, err, "sealed image response")
		return err
	}

	// E2EE: with data[] sealed the router cannot count the images, so the billable
	// count travels as cleartext usage.output_images (SPEC §7.1) and MUST be the
	// count actually delivered. If the response cannot be decoded there is no honest
	// count to publish — the plaintext path bills the requested count in that
	// case, which under sealing would ask the router to bill a number the enclave
	// never verified. Refuse instead — the same shape as the undecodable-response
	// guard directly below, which refuses for the url path for its own reason.
	//
	// This returns before UpdateRequestFeesAndCount, so the request is NOT billed,
	// and that is deliberate rather than incidental. billableImageCount's fallback
	// ("bill the requested count rather than nothing, else an unparseable response
	// is free") applies when the broker still SERVES the response; here it serves
	// nothing. Charging for a response the client never received, on the strength
	// of a count nobody verified, is the worse of the two errors — and the guard
	// below already sets that precedent for the url path. The cost lands on the
	// provider, which is also who caused it: the failure is attributed upstream,
	// so a provider cannot quietly absorb a degradation by emitting undecodable
	// 200s.
	if e2eeSealed && (extractErr != nil || len(images) == 0) {
		ctx.Set("ignoreError", true)
		// Undecodable 200 from the provider: an upstream fault, not a client one,
		// so override the client default ignoreError implies — same attribution as
		// the undecodable-response guard below, and deliberately NOT the same as
		// the sealed-url guard above, where the client is at fault.
		ctx.Set(monitor.CtxKeyFailureSource, monitor.FailureSourceUpstream)
		err := fmt.Errorf("e2ee: provider returned no decodable b64 images, refusing to seal a response with no verifiable image count: %w", extractErr)
		c.handleBrokerError(ctx, err, "sealed image response")
		return err
	}

	// If the client asked for url but the provider returned something we can't
	// decode (non-b64 envelope, empty array), refuse the response rather than
	// passing provider bytes through — they may contain LAN-private URLs.
	if wantURL && (extractErr != nil || len(images) == 0) {
		ctx.Set("ignoreError", true)
		// The provider returned a 200 with an envelope the broker can't decode —
		// that is the provider misbehaving, not the client. Attribute it to
		// upstream (overriding the ignoreError-derived client default) so it trips
		// the upstream-fault alert and a provider can't mask a degradation by
		// emitting undecodable bodies instead of a non-2xx.
		ctx.Set(monitor.CtxKeyFailureSource, monitor.FailureSourceUpstream)
		err := fmt.Errorf("provider returned non-b64 image response, refusing to forward (may contain LAN-private URLs): %w", extractErr)
		c.handleBrokerError(ctx, err, "image response for response_format=url")
		return err
	}

	// If the client asked for url but we have no image store, the URL contract
	// can't be honoured. Silently serving b64 instead would violate the
	// explicitly-requested format without any per-request signal — fail-closed
	// so operators correlate with the "image store disabled" startup warning.
	if wantURL && c.imageStore == nil {
		ctx.Set("ignoreError", true)
		// A disabled image store is a broker startup/config failure, not a client
		// error — keep it in the broker bucket (overriding the ignoreError-derived
		// client default) so the broker-fault alert fires while every URL request
		// is failing.
		ctx.Set(monitor.CtxKeyFailureSource, monitor.FailureSourceBroker)
		err := fmt.Errorf("response_format=url requested but image store is disabled (check startup logs for newImageStore error)")
		c.logger.Errorf("text-to-image URL request while imageStore is nil")
		c.handleBrokerError(ctx, err, "image response for response_format=url")
		return err
	}

	// Build the body to send to the client. store + rewrite; any failure here
	// downgrades to b64 (safe — body is confirmed b64 above, and the client
	// gets a legitimate response just in the wrong format). Log at warn so
	// the degradation is visible in operator logs.
	clientBody := body
	if wantURL {
		if storeErr := c.imageStore.store(chatKey, images); storeErr != nil {
			c.logger.Warnf("Failed to store images for URL rewrite, sending b64: %v", storeErr)
		} else {
			rewritten, buildErr := buildURLResponse(body, chatKey, len(images), c.Service.ServingURL)
			if buildErr != nil {
				c.logger.Warnf("Failed to build URL response, sending b64: %v", buildErr)
			} else {
				clientBody = rewritten
			}
		}
	}

	// Bill by the number of images actually delivered, not the requested count, so
	// a provider that returns fewer than requested (silent n clamp) does not
	// over-charge the user (router#354). Computed here rather than after the flush
	// because the sealed path publishes it to the router as cleartext
	// usage.output_images.
	imageNum := billableImageCount(reqModel.OutputCount, len(images), extractErr)

	// E2EE (SPEC §7 / §7.1): seal data[] to the client's ephemeral key and publish
	// the billable count as cleartext usage.output_images, so the router bills
	// without holding the images. clientBody stays PLAINTEXT for billing below; the §8
	// signature binds the on-wire aad‖ciphertext of the sealed frame instead.
	outBody := clientBody
	if e2eeSealed {
		withUsage, usageErr := withImageUsage(clientBody, imageNum)
		if usageErr != nil {
			c.handleBrokerError(ctx, usageErr, "sealed image response")
			return usageErr
		}
		sealed, _, respBindHash, sealErr := c.maybeSealNonStreamResponse(ctx, withUsage)
		if sealErr != nil {
			// Fail-closed: never forward plaintext images for a sealed request.
			c.handleBrokerError(ctx, sealErr, "seal image response")
			return sealErr
		}
		outBody = sealed

		reqBindHash, ok := e2eeReqBindHash(ctx)
		if !ok {
			err := fmt.Errorf("e2ee image response: request binding hash missing from context")
			c.handleBrokerError(ctx, err, "sign image response")
			return err
		}
		// Cache the signature BEFORE flushing, so a client that reads ZG-Res-Key and
		// immediately fetches GET /v1/proxy/signature/{chatKey} does not race the
		// cache write (issue #619). The plaintext path below keeps its existing
		// sign-after-write order; only the sealed path is reordered.
		e2eeSignedText := proof.SignedTextE2EEFromHashes(reqBindHash, respBindHash)
		if err := c.signChatResponse(ctx, sigReqBody, outBody, chatKey, e2eeSignedText, reqModel.Upstream); err != nil {
			c.handleBrokerError(ctx, errors.Internal(err), "sign image response")
			return err
		}
	}

	// Attempt to return image to client. If client disconnected, continue to billing.
	if _, writeErr := ctx.Writer.Write(outBody); writeErr != nil {
		if c.isClientDisconnectError(writeErr) {
			ctx.Set("ignoreError", true)
			c.logger.Warnf("Client disconnected during text-to-image response, billing for completed response (%d bytes)", len(body))
		} else {
			c.handleBrokerError(ctx, writeErr, "write image response")
		}
	}

	// TEE signing is a function of the trust model, not the response shape:
	//   - E2EE sealed: already signed above (§8 ciphertext binding), before the
	//     flush. Signing again here would overwrite that cached signature with one
	//     over plaintext the client never received.
	//   - Centralized: broker cannot attest to OpenAI's content; it can only
	//     attest to the TLS path it took. Use routing proof (binds TLS cert
	//     fingerprint + provider identity + req/resp hashes).
	//   - Decentralized, LLM in broker TEE network (!TargetSeparated): broker
	//     CAN vouch for content, so sign the decoded image bytes directly.
	//   - Decentralized, TargetSeparated: the remote TEE signs its own output;
	//     the broker does not duplicate.
	switch {
	case e2eeSealed:
	case c.Service.IsCentralized():
		fingerprint := ctx.GetString(CtxKeyUpstreamCertFingerprint)
		c.logger.Debug("Centralized provider, signing text-to-image routing proof")
		// Image modalities reject modelPricing at config load, so there is no
		// per-model upstream override here; "" falls back to the service-level
		// providerIdentity inside signCentralizedRoutingProof (unchanged behaviour).
		if err := c.signCentralizedRoutingProof(sigReqBody, body, chatKey, fingerprint, ""); err != nil {
			c.logger.Errorf("routing proof not created: %v", err)
		}
	case !c.Service.TargetSeparated:
		c.logger.Debug("LLM server in the same network, signing text-to-image content")
		if extractErr == nil && len(images) > 0 {
			if err := c.signImageResponse(sigReqBody, images, chatKey); err != nil {
				c.logger.Errorf("image content signature not created (TEE verification will be unavailable): %v", err)
			}
		} else {
			c.logger.Warnf("No b64 images extracted, falling back to full-body signature: %v", extractErr)
			if err := c.signChatWithKey(sigReqBody, body, chatKey); err != nil {
				c.logger.Errorf("image full-body signature not created (TEE verification will be unavailable): %v", err)
			}
		}
	}

	// Skip billing for whitelisted users, but record whitelist traffic metrics and
	// count the images into the reconciliation rollup (they hit the upstream).
	if reqModel.IsWhitelisted {
		metricModel := c.metricModel(ctx)
		metricUpstream := c.metricUpstream(ctx)
		monitor.RecordTokens("text-to-image", metricModel, metricUpstream, 0, imageNum)
		monitor.RecordWhitelistTokens("text-to-image", metricModel, metricUpstream, 0, imageNum)
		c.recordWhitelistedUsage(reqModel, 0, imageNum, 0, 0, "")
		return nil
	}

	// Calculate output fee: imageNum × price per image
	outputFee, err := util.Multiply(outputPrice, imageNum)
	if err != nil {
		return errors.Wrap(err, "calculate output fee based on imageNum")
	}

	if err := c.db.UpdateRequestFeesAndCount(reqModel.RequestHash, outputFee.String(), outputFee.String(), imageNum); err != nil {
		return errors.Wrap(err, "update request fees and count in database")
	}

	monitor.RecordTokens("text-to-image", c.metricModel(ctx), c.metricUpstream(ctx), 0, imageNum)

	// Update IPM limiter with actual image consumption
	if ipmLimiter, exists := ctx.Get("ipmLimiter"); exists {
		if limiter, ok := ipmLimiter.(*middleware.PerUserTPMLimiter); ok {
			limiter.ConsumeTokens(reqModel.UserAddress, int(imageNum))
		}
	}
	return nil
}

// extractB64Images parses an OpenAI-style image response envelope and decodes
// each data[i].b64_json into raw bytes.
//
// maxImages is the cap on envelope length (typically the request's "n" field,
// which billing has already been committed against). Reject when the provider
// returns more entries than requested — always a bug, and decoding everything
// blindly lets a compromised provider OOM the broker via a giant data array
// of tiny b64 strings. Pass <= 0 to disable the cap (tests only; production
// callers should always supply one).
func extractB64Images(body []byte, maxImages int) ([][]byte, error) {
	var envelope imageResponseEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("unmarshal image response: %w", err)
	}
	if len(envelope.Data) == 0 {
		// A cleanly-parsed response that delivered no images: return an empty
		// (non-nil) slice with no error so callers can distinguish "0 delivered"
		// (bill 0, refuse a url-format request) from "couldn't decode". Billing
		// must not charge the requested count for images that never arrived.
		return [][]byte{}, nil
	}
	if maxImages > 0 && len(envelope.Data) > maxImages {
		return nil, fmt.Errorf("image response has %d entries, exceeds declared output count %d", len(envelope.Data), maxImages)
	}
	images := make([][]byte, 0, len(envelope.Data))
	// base64 expands raw bytes by 4/3 (plus padding); reject the encoded blob
	// before allocating the decode buffer when it would clearly exceed the
	// per-image cap. Note: by this point json.Unmarshal has already pulled
	// the full b64 string into memory, so this check does NOT bound peak
	// ingest — it bounds the additional decode allocation (~maxB64ImageBytes
	// per image) plus the downstream os.WriteFile, which is where the real
	// amplification lives. Capping the upstream body itself would need a
	// MaxBytesReader at the proxy ingress; out of scope here.
	maxEncoded := base64.StdEncoding.EncodedLen(maxB64ImageBytes)
	for i, d := range envelope.Data {
		if d.B64JSON == "" {
			return nil, fmt.Errorf("data[%d] missing b64_json field", i)
		}
		if len(d.B64JSON) > maxEncoded {
			return nil, fmt.Errorf("data[%d].b64_json encoded size %d exceeds per-image cap %d", i, len(d.B64JSON), maxEncoded)
		}
		img, err := base64.StdEncoding.DecodeString(d.B64JSON)
		if err != nil {
			return nil, fmt.Errorf("decode data[%d].b64_json: %w", i, err)
		}
		if len(img) > maxB64ImageBytes {
			return nil, fmt.Errorf("data[%d] decoded size %d exceeds per-image cap %d", i, len(img), maxB64ImageBytes)
		}
		images = append(images, img)
	}
	return images, nil
}

// buildURLResponse replaces each data[i].b64_json with a broker-served URL while
// preserving all other fields (e.g. revised_prompt, created).
//
// The URL base is derived from the operator-configured service.servingUrl so it
// matches the public URL the provider registered on-chain. Returns an error if
// servingUrl is missing or malformed; the caller falls back to b64 on error.
func buildURLResponse(body []byte, chatKey string, count int, servingURL string) ([]byte, error) {
	var envelope imageResponseEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("unmarshal image response: %w", err)
	}

	if servingURL == "" {
		return nil, fmt.Errorf("service.servingUrl is not configured")
	}
	u, err := url.Parse(servingURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("service.servingUrl %q is not a valid absolute URL", servingURL)
	}
	base := strings.TrimRight(servingURL, "/") + constant.ServicePrefix + "/images/" + chatKey + "/"

	// Callers pass count == len(extractB64Images(body)). If that diverges from
	// the envelope length (provider envelope and our decoded image list came
	// from the same body, so this should be impossible), refuse rather than
	// emit a mixed b64/url response that neither the client nor the signature
	// machinery would handle sensibly.
	if len(envelope.Data) != count {
		return nil, fmt.Errorf("envelope has %d data entries but %d images were stored", len(envelope.Data), count)
	}

	for i := range envelope.Data {
		envelope.Data[i] = imageResponseData{
			URL:           base + strconv.Itoa(i),
			RevisedPrompt: envelope.Data[i].RevisedPrompt,
		}
	}

	out, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("marshal URL response: %w", err)
	}
	return out, nil
}
