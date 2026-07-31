package handler

// viduProvider adapts *vidu.Client + the translate package's Vidu mapping
// functions to the Provider interface (see provider.go). Vidu is the THIRD
// vendor sharing this handler package (after DashScope and MiniMax) — the
// concrete trigger the MiniMax PR's own author flagged for extracting
// handler.Provider ("Extract a handler.Provider interface ... when a third
// vendor lands"), which is why this adapter is small: all the shared
// request-parsing/response-writing plumbing already lives in provider.go.

import (
	"context"
	"net/http"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/translate"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/vidu"
)

// viduProvider serves Vidu (start/end-frame-to-video) through the shared
// Provider interface.
type viduProvider struct {
	client *vidu.Client
	logger log.Logger
}

// NewViduVideoHandler builds the Vidu video handler.
func NewViduVideoHandler(client *vidu.Client, logger log.Logger) *GenericVideoHandler {
	return NewGenericVideoHandler(&viduProvider{client: client, logger: logger}, logger)
}

func (p *viduProvider) CreateVideo(ctx context.Context, authHeader string, req translate.CreateVideoRequest) (translate.VideoResponse, error) {
	// Vidu-specific pre-flight validation (both reference frames present,
	// audio not requested on an unsupported model). Wrapped with
	// newValidationError so GenericVideoHandler.writeProviderError reports it
	// as 400, matching the sibling Kling handler's identical-class check
	// (translate.ValidateKlingCreateRequest, checked directly in
	// imagetranslator's CreateImage handler) — an invalid request should
	// never surface as a generic 502, which an OpenAI-SDK client could
	// mistake for a transient outage and retry, when the same request can
	// never succeed.
	if err := translate.ValidateViduCreateRequest(req); err != nil {
		return translate.VideoResponse{}, newValidationError(err)
	}

	vdReq := translate.ToViduCreateRequest(req)
	vdResp, err := p.client.CreateTask(ctx, authHeader, vdReq)
	if err != nil {
		return translate.VideoResponse{}, err
	}
	return translate.FromViduCreateResponse(req, *vdResp), nil
}

func (p *viduProvider) GetVideo(ctx context.Context, authHeader, taskID string) (translate.VideoResponse, error) {
	vdResp, err := p.client.GetTask(ctx, authHeader, taskID)
	if err != nil {
		return translate.VideoResponse{}, err
	}
	if !translate.IsRecognizedViduStatus(vdResp.Output.TaskStatus) {
		p.logger.Errorf("vidu get task %s: unrecognized task_status %q, mapping to failed", taskID, vdResp.Output.TaskStatus)
	}
	return translate.FromViduGetTaskResponse(*vdResp), nil
}

// GetVideoContentURL returns Vidu's video_url once populated. An empty
// video_url (task not yet completed, or upstream reported no asset) is
// reported as ok=false, not an error — the caller returns 404 for this case.
func (p *viduProvider) GetVideoContentURL(ctx context.Context, authHeader, taskID string) (string, bool, error) {
	vdResp, err := p.client.GetTask(ctx, authHeader, taskID)
	if err != nil {
		return "", false, err
	}
	if vdResp.Output.VideoURL == "" {
		return "", false, nil
	}
	return vdResp.Output.VideoURL, true, nil
}

func (p *viduProvider) FetchContent(ctx context.Context, contentURL string) (*http.Response, error) {
	return p.client.FetchContent(ctx, contentURL)
}
