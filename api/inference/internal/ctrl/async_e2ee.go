package ctrl

// Async text-to-image E2EE (0g-pc SPEC §7.5, profile async-image): the sync image
// profile split in time. The submit is opened here, in the enclave, before the
// job is queued; the worker calls the upstream with the plaintext; at completion
// the whole poll response is sealed ONCE to the client's ephemeral key and stored
// as ciphertext, and every poll returns that stored frame. Nothing about the
// request or the images is stored or logged in the clear.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"time"

	pccrypto "github.com/0gfoundation/0g-pc-e2ee/protocol/crypto"
	"github.com/0gfoundation/0g-pc-e2ee/protocol/proof"
	"github.com/0gfoundation/0g-pc-e2ee/protocol/wire"
	"github.com/google/uuid"

	"github.com/0glabs/0g-serving-broker/common/errors"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// SealedAsyncRequest is an async submit opened in the enclave. It is held in
// memory (the job queue) only; the job row stores the sealed envelope.
type SealedAsyncRequest struct {
	// Forward is the plaintext request to send upstream, `_bind` removed.
	Forward []byte
	// ClientEphPub is the key the result is sealed to.
	ClientEphPub pccrypto.PublicKey
	// Bind is the request's `_bind`, echoed sealed in the result (SPEC §7.5.1).
	Bind []byte
	// ReqBindHash is the §8 request binding of the sealed envelope.
	ReqBindHash [32]byte
}

// Fixed error codes a sealed job's errorMessage is limited to (SPEC §7.5.2).
// The upstream error body is never stored for a sealed job: a moderation error
// can echo the prompt, and the row and its errorMessage leave the enclave.
const (
	sealedAsyncErrUpstream = "upstream_error"
	sealedAsyncErrTimeout  = "timeout"
	sealedAsyncErrBroker   = "broker_error"
)

// sealedAsyncUpstreamStatus is the code for a non-200 upstream answer: the
// status only, never the body.
func sealedAsyncUpstreamStatus(status int) string {
	return sealedAsyncErrUpstream + ":" + strconv.Itoa(status)
}

// sealedAsyncRequestErrCode is the code for a provider call that failed before
// a complete answer arrived: a timeout is worth telling apart (the client may
// retry with a smaller n); everything else is the upstream's. Both shapes of
// timeout count: the job's context deadline, and a transport timeout
// (http.Client.Timeout), which surfaces as a net.Error rather than as
// context.DeadlineExceeded.
func sealedAsyncRequestErrCode(err error) string {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return sealedAsyncErrTimeout
	}
	return sealedAsyncErrUpstream
}

// UnsealAsyncRequest opens a sealed async submit, or returns (nil, nil) for an
// unsealed one. Same checks as the synchronous proxy (checkSealedEnvelope,
// openSealedEnvelope) under the async-image profile, plus taking `_bind` out.
// Errors are fail-closed and wrap ErrE2EEKeyMismatch where the proxy's would.
//
// Opening at submit rather than at dispatch is what lets a stale key or a
// malformed envelope come back as a 409/400 on the submit, before any job row
// or billing record exists, instead of as a failed job later. The plaintext
// stays in enclave memory (the job queue), as it would at dispatch.
func (c *Ctrl) UnsealAsyncRequest(svcType string, reqBody []byte) (*SealedAsyncRequest, error) {
	if !isSealedJSON(reqBody) {
		return nil, nil
	}
	// RefuseAsync admits sealed bodies on the text-to-image route only; this is
	// the half that knows what this broker serves.
	if svcType != constant.ServiceTypeTextToImage || c.Service.Type != constant.ServiceTypeTextToImage {
		return nil, fmt.Errorf("sealed async requests are only served for text-to-image, not %q on a %q provider", svcType, c.Service.Type)
	}
	var env wire.Request
	if err := json.Unmarshal(reqBody, &env); err != nil {
		return nil, fmt.Errorf("sealed request is not a JSON object: %w", err)
	}
	e2ee, err := c.checkSealedEnvelope(env)
	if err != nil {
		return nil, err
	}
	opened, clientEphPub, reqBindHash, err := c.openSealedEnvelope(wire.ProfileAsyncImage, env, e2ee)
	if err != nil {
		return nil, err
	}
	bind, forward, err := wire.TakeAsyncBind(opened)
	if err != nil {
		return nil, fmt.Errorf("unseal request: %w", err)
	}
	plaintext, err := json.Marshal(forward)
	if err != nil {
		return nil, fmt.Errorf("re-encode unsealed request: %w", err)
	}
	return &SealedAsyncRequest{Forward: plaintext, ClientEphPub: clientEphPub, Bind: bind, ReqBindHash: reqBindHash}, nil
}

// sealedAsyncResult is what a completed sealed job stores.
type sealedAsyncResult struct {
	// frame is the poll response with `_e2ee.ciphertext` emptied; ciphertext is
	// those bytes decoded. Stored apart so the images are not base64'd twice
	// (SPEC §7.5 / plan §3.9: ≈1.33× raw instead of ≈1.78×).
	frame      []byte
	ciphertext []byte
	// respBindHash is the §8 response binding of the full frame.
	respBindHash [32]byte
}

// sealAsyncImageResult builds the completed poll response for a sealed job and
// seals it (SPEC §7.5.2): `data` (the provider's image JSON) and `_bind` sealed;
// jobId, status, createdAt, updatedAt and usage.output_images bound cleartext.
// Built once; every poll returns it.
func sealAsyncImageResult(req *SealedAsyncRequest, jobID string, createdAt, completedAt time.Time, providerResp []byte, outputImages int) (*sealedAsyncResult, error) {
	if !json.Valid(providerResp) {
		return nil, fmt.Errorf("provider response is not JSON")
	}
	fields := map[string]any{
		"jobId":     jobID,
		"status":    string(model.AsyncJobStatusCompleted),
		"createdAt": createdAt,
		"updatedAt": completedAt,
		"usage":     map[string]int{"output_images": outputImages},
		"data":      json.RawMessage(providerResp),
	}
	result := make(wire.Response, len(fields))
	for k, v := range fields {
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("encode %s: %w", k, err)
		}
		result[k] = raw
	}
	frame, err := wire.SealAsyncImageResult(req.ClientEphPub, result, req.Bind)
	if err != nil {
		return nil, fmt.Errorf("seal result: %w", err)
	}
	respBindHash, err := proof.FrameBindingHash(frame)
	if err != nil {
		return nil, fmt.Errorf("seal result binding: %w", err)
	}
	stored, ciphertext, err := splitSealedFrame(frame)
	if err != nil {
		return nil, err
	}
	return &sealedAsyncResult{frame: stored, ciphertext: ciphertext, respBindHash: respBindHash}, nil
}

// splitSealedFrame takes the ciphertext out of a sealed frame: the frame comes
// back with `_e2ee.ciphertext` empty, and the ciphertext as raw bytes.
func splitSealedFrame(frame wire.Response) (stored, ciphertext []byte, err error) {
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(frame[e2eeBodyMarker], &meta); err != nil || meta == nil {
		return nil, nil, fmt.Errorf("sealed frame has no %q object", e2eeBodyMarker)
	}
	var ct string
	if err := json.Unmarshal(meta["ciphertext"], &ct); err != nil {
		return nil, nil, fmt.Errorf("sealed frame ciphertext: %w", err)
	}
	ciphertext, err = base64.RawURLEncoding.DecodeString(ct)
	if err != nil {
		return nil, nil, fmt.Errorf("sealed frame ciphertext: %w", err)
	}
	meta["ciphertext"] = json.RawMessage(`""`)
	out := make(wire.Response, len(frame))
	for k, v := range frame {
		out[k] = v
	}
	if out[e2eeBodyMarker], err = json.Marshal(meta); err != nil {
		return nil, nil, err
	}
	if stored, err = json.Marshal(out); err != nil {
		return nil, nil, err
	}
	return stored, ciphertext, nil
}

// AssembleSealedAsyncFrame is splitSealedFrame's inverse, run on every poll of a
// completed sealed job: the stored frame with its ciphertext put back. The
// output is the same bytes on every poll (encoding/json sorts map keys).
func AssembleSealedAsyncFrame(stored, ciphertext []byte) ([]byte, error) {
	var frame map[string]json.RawMessage
	if err := json.Unmarshal(stored, &frame); err != nil || frame == nil {
		return nil, fmt.Errorf("stored sealed frame is not a JSON object")
	}
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(frame[e2eeBodyMarker], &meta); err != nil || meta == nil {
		return nil, fmt.Errorf("stored sealed frame has no %q object", e2eeBodyMarker)
	}
	ct, err := json.Marshal(base64.RawURLEncoding.EncodeToString(ciphertext))
	if err != nil {
		return nil, err
	}
	meta["ciphertext"] = ct
	if frame[e2eeBodyMarker], err = json.Marshal(meta); err != nil {
		return nil, err
	}
	return json.Marshal(frame)
}

// signSealedAsyncResult caches the §8 signature over the sealed request and the
// stored frame under a fresh chat key, as the sync sealed path does. For a
// centralized provider it carries the routing proof too (see signChatResponse).
//
// Returns "" when signing fails: like the plaintext async path, a missing
// signature is not fatal. The receiver authenticates a sealed result by its
// `_bind` (SPEC §7.5.1), not by §8.
func (c *Ctrl) signSealedAsyncResult(jobID string, req *SealedAsyncRequest, res *sealedAsyncResult, fingerprint, providerIdentity string) string {
	text := proof.SignedTextE2EEFromHashes(req.ReqBindHash, res.respBindHash)
	var buildRouting func() *RoutingProof
	if c.Service.IsCentralized() {
		buildRouting = func() *RoutingProof {
			routing, err := c.buildSealedRoutingProof(text, fingerprint, providerIdentity)
			if err != nil {
				c.logger.Warnf("Async job %s: sealed result carries no routing proof: %v", jobID, err)
				return nil
			}
			return routing
		}
	}
	chatKey := uuid.NewString()
	if err := c.signChatE2EE(text, chatKey, buildRouting); err != nil {
		c.logger.Warnf("Async job %s: §8 signature not created: %v", jobID, err)
		return ""
	}
	return chatKey
}

// errSealedAsync is the result of a sealed worker step that must fail the job:
// code is what the row stores and the client sees; detail is for the log and
// must never carry upstream text.
type errSealedAsync struct {
	code   string
	detail string
}

func (e *errSealedAsync) Error() string { return e.code + ": " + e.detail }

// completeSealedAsyncJob turns a 200 upstream image response into the stored
// frame. It returns the delivered image count, which is what the job is billed
// on and what the frame publishes as usage.output_images.
func (c *Ctrl) completeSealedAsyncJob(params asyncJobParams, providerResp []byte, fingerprint string) (chatKey string, outputImages int64, err error) {
	jobID := params.JobID
	// The billable count must be the count actually delivered (SPEC §7.1): with
	// `data` sealed, the router bills on it and nothing else. No decodable
	// images means no honest count, so refuse rather than seal one.
	images, exErr := extractB64Images(providerResp, int(params.BillingReq.OutputCount))
	if exErr != nil || len(images) == 0 {
		return "", 0, &errSealedAsync{sealedAsyncErrUpstream, fmt.Sprintf("provider returned no decodable b64 images (%d decoded): %v", len(images), exErr)}
	}
	job, err := c.asyncDB.GetAsyncJob(jobID)
	if err != nil {
		return "", 0, &errSealedAsync{sealedAsyncErrBroker, "read job row: " + err.Error()}
	}
	createdAt := time.Now()
	if job.CreatedAt != nil {
		createdAt = *job.CreatedAt
	}
	res, err := sealAsyncImageResult(params.Sealed, jobID, createdAt, time.Now(), providerResp, len(images))
	if err != nil {
		return "", 0, &errSealedAsync{sealedAsyncErrBroker, err.Error()}
	}
	if err := c.asyncDB.StoreSealedAsyncResult(jobID, res.frame, res.ciphertext); err != nil {
		return "", 0, &errSealedAsync{sealedAsyncErrBroker, "store sealed result: " + err.Error()}
	}
	chatKey = c.signSealedAsyncResult(jobID, params.Sealed, res, fingerprint, params.BillingReq.Upstream)
	return chatKey, int64(len(images)), nil
}
