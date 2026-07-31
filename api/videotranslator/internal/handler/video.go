// Package handler wires the OpenAI-shaped HTTP surface to the DashScope
// client and the translate package. It holds no state across requests: the
// translator is a stateless, per-call protocol shim (see
// 0gfoundation/0g-serving-broker#582) — polling to completion is the
// broker's job, not this service's.
package handler

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/dashscope"
	"github.com/0glabs/0g-serving-broker/videotranslator/internal/translate"
)

// VideoHandler serves the OpenAI Video API surface the broker expects,
// translating each call 1:1 to/from DashScope.
type VideoHandler struct {
	client *dashscope.Client
	logger log.Logger
}

// NewVideoHandler builds a VideoHandler.
func NewVideoHandler(client *dashscope.Client, logger log.Logger) *VideoHandler {
	return &VideoHandler{client: client, logger: logger}
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
	// InputReference is the OpenAI Video API image-to-video reference (first
	// frame): exactly one of image_url (public URL or data: URI) or file_id.
	InputReference *struct {
		ImageURL string `json:"image_url"`
		FileID   string `json:"file_id"`
	} `json:"input_reference"`
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

// CreateVideo handles POST /videos.
func (h *VideoHandler) CreateVideo(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxCreateVideoBodyBytes)

	req, err := parseCreateVideoRequest(c.Request)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": err.Error()}})
		return
	}

	authHeader := c.GetHeader("Authorization")
	dsReq := translate.ToDashScopeCreateRequest(req)
	dsResp, err := h.client.CreateTask(c.Request.Context(), authHeader, dsReq)
	if err != nil {
		h.writeDashScopeError(c, "dashscope create task failed", "failed to create video generation task", err)
		return
	}

	out, err := translate.FromCreateResponse(req, *dsResp)
	if err != nil {
		// The vendor's id cannot be expressed in the contract the broker publishes
		// (see translate.EncodeJobID). Fail here, loudly, on this vendor's FIRST
		// request rather than handing downstream a key it cannot persist.
		h.logger.Errorf("job id contract: %v", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": "upstream returned an unusable job id"}})
		return
	}
	c.JSON(http.StatusOK, out)
}

// GetVideo handles GET /videos/{id}.
func (h *VideoHandler) GetVideo(c *gin.Context) {
	publicID := c.Param("id")
	taskID, err := translate.DecodeJobID(publicID)
	if err != nil {
		// The only failure in these handlers that would otherwise leave no trace at
		// all: the client gets a message without the id, and DecodeJobID's three
		// distinct causes (unknown shape / malformed payload / not a task id) are
		// discarded. Log it — this is also the path most likely to reject something
		// legitimate.
		h.logger.Warnf("video id %q rejected: %v", publicID, err)
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "unknown video id"}})
		return
	}
	authHeader := c.GetHeader("Authorization")

	dsResp, err := h.client.GetTask(c.Request.Context(), authHeader, taskID)
	if err != nil {
		h.writeDashScopeError(c, fmt.Sprintf("dashscope get task failed for %s", taskID), "failed to get video generation task", err)
		return
	}
	if !translate.IsRecognizedDashScopeStatus(dsResp.Output.TaskStatus) {
		h.logger.Errorf("dashscope get task %s: unrecognized task_status %q, mapping to failed", taskID, dsResp.Output.TaskStatus)
	}

	c.JSON(http.StatusOK, translate.FromGetTaskResponse(publicID, *dsResp))
}

// GetVideoContent handles GET /videos/{id}/content: it looks up the task's
// current state to find DashScope's asset URL, then streams the video bytes
// back through the translator rather than redirecting the client to it —
// keeping the vendor's asset host hidden from the client, consistent with
// this service never exposing DashScope directly.
func (h *VideoHandler) GetVideoContent(c *gin.Context) {
	publicID := c.Param("id")
	taskID, err := translate.DecodeJobID(publicID)
	if err != nil {
		// The only failure in these handlers that would otherwise leave no trace at
		// all: the client gets a message without the id, and DecodeJobID's three
		// distinct causes (unknown shape / malformed payload / not a task id) are
		// discarded. Log it — this is also the path most likely to reject something
		// legitimate.
		h.logger.Warnf("video id %q rejected: %v", publicID, err)
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "unknown video id"}})
		return
	}
	authHeader := c.GetHeader("Authorization")

	dsResp, err := h.client.GetTask(c.Request.Context(), authHeader, taskID)
	if err != nil {
		h.writeDashScopeError(c, fmt.Sprintf("dashscope get task failed for %s", taskID), "failed to get video generation task", err)
		return
	}
	if dsResp.Output.VideoURL == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"message": "video content not available (task not completed, or upstream reported no asset)"}})
		return
	}

	contentResp, err := h.client.FetchContent(c.Request.Context(), dsResp.Output.VideoURL)
	if err != nil {
		h.logger.Errorf("fetch video content failed for %s: %v", taskID, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": "failed to fetch video content"}})
		return
	}
	defer contentResp.Body.Close()

	contentType := contentResp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "video/mp4"
	}
	c.Header("Content-Type", contentType)
	c.Status(http.StatusOK)
	if _, err := io.Copy(c.Writer, contentResp.Body); err != nil {
		h.logger.Warnf("stream video content failed for %s: %v", taskID, err)
	}
}

// writeDashScopeError maps a dashscope client error to the HTTP response
// the caller sees. A DashScope 4xx (*dashscope.APIError with a 4xx status —
// the vendor rejected the request outright: bad auth, bad model/parameter,
// quota) surfaces the vendor's own status/code/message, since that's the
// caller's own request being rejected, not a translator or connectivity
// problem — this also lets an OpenAI-SDK client classify it correctly
// (e.g. 401 -> AuthenticationError, 429 -> RateLimitError), matching how
// FromGetTaskResponse already propagates Code/Message for a task that fails
// asynchronously. Anything else (5xx, or a plain transport/connectivity
// error with no structured vendor response at all) is reported as 502
// without vendor detail — there isn't any reliable detail to give.
func (h *VideoHandler) writeDashScopeError(c *gin.Context, logContext, fallbackMessage string, err error) {
	var apiErr *dashscope.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode >= 400 && apiErr.StatusCode < 500 {
		message := apiErr.Message
		if message == "" {
			message = fmt.Sprintf("dashscope rejected the request (status %d)", apiErr.StatusCode)
		}
		h.logger.Errorf("%s: dashscope rejected request: status %d %s", logContext, apiErr.StatusCode, vendorErrorDetail(apiErr.Code, apiErr.Message, apiErr.Body, ""))
		c.JSON(apiErr.StatusCode, gin.H{"error": gin.H{"code": apiErr.Code, "message": message}})
		return
	}
	h.logger.Errorf("%s: %v", logContext, err)
	c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": fallbackMessage}})
}

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
			Model:   r.FormValue("model"),
			Prompt:  r.FormValue("prompt"),
			Seconds: r.FormValue("seconds"),
			Size:    r.FormValue("size"),
			Seed:    r.FormValue("seed"),
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
	if jr.InputReference != nil {
		req.InputReferenceImageURL = jr.InputReference.ImageURL
		req.InputReferenceFileID = jr.InputReference.FileID
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
