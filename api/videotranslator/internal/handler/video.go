// Package handler wires the OpenAI-shaped HTTP surface to each vendor's
// native client and the translate package, via the shared Provider interface
// (provider.go). It holds no state across requests: the translator is a
// stateless, per-call protocol shim (see
// 0gfoundation/0g-serving-broker#582) — polling to completion is the
// broker's job, not this service's.
package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/dashscope"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/translate"
)

// dashScopeProvider adapts *dashscope.Client + the translate package's
// DashScope mapping functions to the Provider interface.
type dashScopeProvider struct {
	client *dashscope.Client
	logger log.Logger
}

// NewVideoHandler builds the DashScope video handler.
func NewVideoHandler(client *dashscope.Client, logger log.Logger) *GenericVideoHandler {
	return NewGenericVideoHandler(&dashScopeProvider{client: client, logger: logger}, logger)
}

func (p *dashScopeProvider) CreateVideo(ctx context.Context, authHeader string, req translate.CreateVideoRequest) (translate.VideoResponse, error) {
	dsReq := translate.ToDashScopeCreateRequest(req)
	dsResp, err := p.client.CreateTask(ctx, authHeader, dsReq)
	if err != nil {
		return translate.VideoResponse{}, err
	}

	out, err := translate.FromCreateResponse(req, *dsResp)
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

// GetVideo handles GET /videos/{id}. taskID here is actually the PUBLIC,
// encoded job id (GenericVideoHandler passes c.Param("id") straight through
// — it has no vendor-specific encode/decode knowledge), so this decodes it
// back to DashScope's real task id before calling the vendor.
func (p *dashScopeProvider) GetVideo(ctx context.Context, authHeader, publicID string) (translate.VideoResponse, error) {
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

	dsResp, err := p.client.GetTask(ctx, authHeader, taskID)
	if err != nil {
		return translate.VideoResponse{}, err
	}
	if !translate.IsRecognizedDashScopeStatus(dsResp.Output.TaskStatus) {
		p.logger.Errorf("dashscope get task %s: unrecognized task_status %q, mapping to failed", taskID, dsResp.Output.TaskStatus)
	}
	return translate.FromGetTaskResponse(publicID, *dsResp), nil
}

// GetVideoContentURL returns DashScope's video_url once populated. An empty
// video_url (task not yet completed, or upstream reported no asset) is
// reported as ok=false, not an error — the caller returns 404 for this case.
//
// publicID (like GetVideo's) is the client-facing encoded id, decoded to
// DashScope's real task id here for the same reason.
func (p *dashScopeProvider) GetVideoContentURL(ctx context.Context, authHeader, publicID string) (string, bool, error) {
	taskID, err := translate.DecodeJobID(publicID)
	if err != nil {
		p.logger.Warnf("video id %q rejected: %v", publicID, err)
		return "", false, newValidationError(errors.New("unknown video id"))
	}

	dsResp, err := p.client.GetTask(ctx, authHeader, taskID)
	if err != nil {
		return "", false, err
	}
	if dsResp.Output.VideoURL == "" {
		return "", false, nil
	}
	return dsResp.Output.VideoURL, true, nil
}

func (p *dashScopeProvider) FetchContent(ctx context.Context, contentURL string) (*http.Response, error) {
	return p.client.FetchContent(ctx, contentURL)
}

// jsonCreateVideoRequest is the JSON-shaped variant of a create request
// (the broker's own request-side parsing tolerates both multipart and JSON —
// see resolveVideoBilling/videoSecondsSizeFromRequest in inference/internal/ctrl/video.go
// — so this mirrors that). Seed has no official OpenAI Video API field — a
// client can only set it via the SDK's "undocumented request params" escape
// hatch, which the broker relays through as an ordinary extra field.
type jsonCreateVideoRequest struct {
	Model   string      `json:"model"`
	Prompt  string      `json:"prompt"`
	Seconds json.Number `json:"seconds"`
	Size    string      `json:"size"`
	Seed    json.Number `json:"seed"`
	// Watermark/Audio are genuine JSON booleans (not numbers, unlike
	// Seed/Seconds) — *bool so "absent" (nil) is distinguishable from an
	// explicit "false", converted to a plain "true"/"false" string (or ""
	// when absent) before reaching the shared, vendor-agnostic
	// CreateVideoRequest, matching how every other field here is a raw
	// string for the translate layer to parse per-vendor.
	Watermark *bool `json:"watermark"`
	Audio     *bool `json:"audio"`
	// InputReference is the OpenAI Video API image-to-video reference (first
	// frame): exactly one of image_url (public URL or data: URI) or file_id.
	InputReference *struct {
		ImageURL string `json:"image_url"`
		FileID   string `json:"file_id"`
	} `json:"input_reference"`
	// LastFrameReference is Vidu-specific (start/end-frame models): the
	// last-frame counterpart to InputReference's first frame. Only
	// image_url is supported (Vidu documents no file_id/vendor-native
	// handle scheme for media[].url, so there is nothing to prefix
	// server-side the way MiniMax's file_id is).
	LastFrameReference *struct {
		ImageURL string `json:"image_url"`
	} `json:"last_frame_reference"`
}

// maxCreateVideoBodyBytes bounds the total POST /videos request body.
// HappyHorse's own prompt limit is documented at ~5,000 non-Chinese / 2,500
// Chinese characters (a few KB at most in UTF-8), and model/seconds/size/seed
// are all tiny — this is generous relative to today's actual fields, not a
// deliberately roomy budget. Without a cap, a client sending one of these
// fields as an oversized multipart file part (filename set) forces Go's
// multipart parser down its disk-spill path with no upper bound, consuming
// real disk I/O/space for the request's duration even though the temp file
// itself is cleaned up afterward (net/http's finishRequest always calls
// MultipartForm.RemoveAll — so this bounds transient resource use during the
// request, not a leak).
//
// NOTE: raise this (and ParseMultipartForm's in-memory budget alongside it)
// if/when image-to-video support adds a real file upload to this same
// request (e.g. an "input_reference" image, mirroring the real OpenAI Video
// API's field of the same name) — don't preemptively raise it now, since
// DashScope's own size limit for a reference image isn't confirmed yet.
// This is OUR cap, not the vendor's: MiniMax H3 allows a 64 MB total body and
// images up to 30 MB each. We stay well under that deliberately — the cap is
// sized to match the router's media-tier /videos limit (24 MiB), so the router
// stays the binding gate and a body it accepted is never rejected here. An
// inline first-frame reference (data: URI or multipart file part) fits; a large
// frame should use a public URL or mm_file:// file_id instead, which keeps the
// body tiny and is what the vendor's own guide recommends (Base64 inflates
// payloads ~1/3). ParseMultipartForm's 32 MiB in-memory budget is not a size
// gate — MaxBytesReader below trips first.
const maxCreateVideoBodyBytes = 24 << 20 // 24 MiB

// parseCreateVideoRequest reads a create request from either a
// multipart/form-data body (the broker's live /v1/videos transport) or a
// plain JSON body.
func parseCreateVideoRequest(r *http.Request) (translate.CreateVideoRequest, error) {
	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "multipart/form-data") {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			return translate.CreateVideoRequest{}, err
		}
		req := translate.CreateVideoRequest{
			Model:     r.FormValue("model"),
			Prompt:    r.FormValue("prompt"),
			Seconds:   r.FormValue("seconds"),
			Size:      r.FormValue("size"),
			Seed:      r.FormValue("seed"),
			Watermark: r.FormValue("watermark"),
			Audio:     r.FormValue("audio"),
		}
		// input_reference (image-to-video): a plain form value carries a URL;
		// a file part carries the image bytes → encode as a data: URI so the
		// downstream mapping is transport-agnostic. (For large frames a public
		// URL / file_id is preferable — data: URIs inflate the body ~1/3.)
		if v := r.FormValue("input_reference"); v != "" {
			req.InputReferenceImageURL = v
		} else if dataURI, ok := multipartFileDataURI(r, "input_reference"); ok {
			req.InputReferenceImageURL = dataURI
		}
		// last_frame_reference (Vidu start/end-frame models): same
		// plain-value-or-file-part handling as input_reference, no file_id
		// counterpart (see CreateVideoRequest.LastFrameReferenceImageURL doc).
		if v := r.FormValue("last_frame_reference"); v != "" {
			req.LastFrameReferenceImageURL = v
		} else if dataURI, ok := multipartFileDataURI(r, "last_frame_reference"); ok {
			req.LastFrameReferenceImageURL = dataURI
		}
		return req, nil
	}

	var jr jsonCreateVideoRequest
	if err := json.NewDecoder(r.Body).Decode(&jr); err != nil {
		return translate.CreateVideoRequest{}, err
	}
	req := translate.CreateVideoRequest{
		Model:   jr.Model,
		Prompt:  jr.Prompt,
		Seconds: jr.Seconds.String(),
		Size:    jr.Size,
		Seed:    jr.Seed.String(),
	}
	if jr.Watermark != nil {
		req.Watermark = strconv.FormatBool(*jr.Watermark)
	}
	if jr.Audio != nil {
		req.Audio = strconv.FormatBool(*jr.Audio)
	}
	if jr.InputReference != nil {
		req.InputReferenceImageURL = jr.InputReference.ImageURL
		req.InputReferenceFileID = jr.InputReference.FileID
	}
	if jr.LastFrameReference != nil {
		req.LastFrameReferenceImageURL = jr.LastFrameReference.ImageURL
	}
	return req, nil
}

// multipartFileDataURI reads a named multipart file part and returns it as a
// base64 data: URI (with the part's declared content type, defaulting to
// image/png). Returns ("", false) when the part is absent or unreadable.
func multipartFileDataURI(r *http.Request, field string) (string, bool) {
	if r.MultipartForm == nil || len(r.MultipartForm.File[field]) == 0 {
		return "", false
	}
	fh := r.MultipartForm.File[field][0]
	f, err := fh.Open()
	if err != nil {
		return "", false
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return "", false
	}
	ct := fh.Header.Get("Content-Type")
	if ct == "" {
		ct = "image/png"
	}
	return "data:" + ct + ";base64," + base64.StdEncoding.EncodeToString(data), true
}
