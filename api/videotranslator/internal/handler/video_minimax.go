package handler

// miniMaxProvider adapts *minimax.Client + the translate package's MiniMax
// mapping functions to the Provider interface (see provider.go). It reuses
// this package's shared create-request parsing (parseCreateVideoRequest,
// maxCreateVideoBodyBytes) via GenericVideoHandler; only the vendor client
// and vendor mapping functions differ from dashScopeProvider.

import (
	"context"
	"errors"
	"net/http"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/minimax"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/translate"
)

// miniMaxProvider serves MiniMax (Hailuo / MiniMax-H3) through the shared
// Provider interface.
type miniMaxProvider struct {
	client            *minimax.Client
	defaultResolution string
	logger            log.Logger
}

// NewMiniMaxVideoHandler builds the MiniMax video handler. defaultResolution
// is the resolution sent when the client's "size" isn't itself a MiniMax
// resolution token (e.g. "2K" for an H3 deployment).
func NewMiniMaxVideoHandler(client *minimax.Client, defaultResolution string, logger log.Logger) *GenericVideoHandler {
	return NewGenericVideoHandler(&miniMaxProvider{client: client, defaultResolution: defaultResolution, logger: logger}, logger)
}

func (p *miniMaxProvider) CreateVideo(ctx context.Context, authHeader string, req translate.CreateVideoRequest) (translate.VideoResponse, error) {
	mmReq := translate.ToMiniMaxCreateRequest(req, p.defaultResolution)
	mmResp, err := p.client.CreateTask(ctx, authHeader, mmReq)
	if err != nil {
		return translate.VideoResponse{}, err
	}

	out, err := translate.FromMiniMaxCreateResponse(req, *mmResp)
	if err != nil {
		// The vendor's id cannot be expressed in the contract the broker publishes
		// (see translate.EncodeJobID). Fail here, loudly, on this vendor's FIRST
		// request rather than handing downstream a key it cannot persist. A plain
		// error (not newValidationError) — this is an upstream/vendor-shape
		// problem, not a bad client request, so it falls through
		// writeProviderError's generic 502 path.
		p.logger.Errorf("job id contract: %v", err)
		return translate.VideoResponse{}, err
	}
	return out, nil
}

// errMiniMaxNoTask is returned by GetVideo when a 200 response carries no
// task (and no base_resp error, which the client already turns into an
// *minimax.APIError). It is a plain error, not a *minimax.APIError, so
// GenericVideoHandler.writeProviderError falls through to its generic 502
// path — the broker's poll scheduler then treats this as a transient hiccup
// and reschedules, rather than a terminal "failed" that would stop polling
// and potentially serve a job that later succeeded for free.
var errMiniMaxNoTask = errors.New("minimax get-task response contained no task (malformed upstream response)")

// GetVideo handles GET /videos/{id}. taskID here is actually the PUBLIC,
// encoded job id (GenericVideoHandler passes c.Param("id") straight through
// — it has no vendor-specific encode/decode knowledge), so this decodes it
// back to MiniMax's real task id before calling the vendor.
func (p *miniMaxProvider) GetVideo(ctx context.Context, authHeader, publicID string) (translate.VideoResponse, error) {
	taskID, err := translate.DecodeJobID(publicID)
	if err != nil {
		// The only failure in this path that would otherwise leave no trace at
		// all: the client gets a message without the id, and DecodeJobID's
		// distinct causes (unknown shape / malformed payload / not a task id)
		// are discarded. Log it — this is also the path most likely to reject
		// something legitimate. Wrapped with newValidationError so
		// writeProviderError reports 400 "unknown video id", not the generic
		// 502 every other non-vendor-4xx error gets — a bad/unknown id is the
		// client's request being wrong, not a vendor or transport problem.
		p.logger.Warnf("video id %q rejected: %v", publicID, err)
		return translate.VideoResponse{}, newValidationError(errors.New("unknown video id"))
	}

	mmResp, err := p.client.GetTask(ctx, authHeader, taskID)
	if err != nil {
		return translate.VideoResponse{}, err
	}
	if mmResp.Task == nil {
		return translate.VideoResponse{}, errMiniMaxNoTask
	}
	if !translate.IsRecognizedMiniMaxStatus(mmResp.Task.Status) {
		p.logger.Errorf("minimax get task %s: unrecognized status %q, mapping to failed", taskID, mmResp.Task.Status)
	}
	return translate.FromMiniMaxGetTaskResponse(publicID, *mmResp), nil
}

// GetVideoContentURL returns MiniMax's task.content.url once populated. A
// nil task, nil content, or empty URL (task not yet completed, or upstream
// reported no asset) is reported as ok=false, not an error — the caller
// returns 404 for this case. This is deliberately NOT the same treatment
// GetVideo gives a nil task (a 502): here, the far more common cause is
// simply that generation hasn't finished yet, which is an ordinary "not
// ready" state, not a malformed response.
//
// publicID (like GetVideo's) is the client-facing encoded id, decoded to
// MiniMax's real task id here for the same reason.
func (p *miniMaxProvider) GetVideoContentURL(ctx context.Context, authHeader, publicID string) (string, bool, error) {
	taskID, err := translate.DecodeJobID(publicID)
	if err != nil {
		p.logger.Warnf("video id %q rejected: %v", publicID, err)
		return "", false, newValidationError(errors.New("unknown video id"))
	}

	mmResp, err := p.client.GetTask(ctx, authHeader, taskID)
	if err != nil {
		return "", false, err
	}
	if mmResp.Task == nil || mmResp.Task.Content == nil || mmResp.Task.Content.URL == "" {
		return "", false, nil
	}
	return mmResp.Task.Content.URL, true, nil
}

func (p *miniMaxProvider) FetchContent(ctx context.Context, contentURL string) (*http.Response, error) {
	return p.client.FetchContent(ctx, contentURL)
}
