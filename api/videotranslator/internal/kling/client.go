package kling

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	teeutil "github.com/0glabs/0g-serving-broker/common/tee"
)

// Kling is served from a WORKSPACE-SPECIFIC base URL
// (https://{WorkspaceId}.cn-beijing.maas.aliyuncs.com, China North 2 /
// Beijing only) — unlike DashScope's HappyHorse or Seedance's BytePlus Ark,
// there is no universal default host to fall back to (every workspace has
// its own subdomain). NewClient therefore does NOT default an empty baseURL
// to anything; the operator MUST set KLING_BASE_URL (see cmd/server/kling.go
// and config.go), and a client built with an empty baseURL will fail on its
// first request with a URL-construction error rather than silently talking
// to some other operator's workspace.
const (
	createPath     = "/api/v1/services/aigc/video-generation/video-synthesis"
	getTaskPathFmt = "/api/v1/tasks/%s"
)

// ContentFetchTimeout bounds a full video content download via FetchContent —
// longer than the API-call client's own timeout, since streaming an MP4 can
// legitimately take longer than a small status/create call. Exported so
// cmd/server can derive its inbound WriteTimeout from it with an explicit
// margin. Mirrors dashscope.ContentFetchTimeout / minimax.ContentFetchTimeout
// / seedance.ContentFetchTimeout.
const ContentFetchTimeout = 5 * time.Minute

// defaultTransport mirrors the tuned Transport the broker's shared HTTP client
// uses: a video job is created once then polled repeatedly, so the stdlib
// default MaxIdleConnsPerHost=2 would force a fresh TCP+TLS handshake per poll
// once more than ~2 jobs are in flight concurrently.
func defaultTransport() *http.Transport {
	return &http.Transport{
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 200,
		IdleConnTimeout:     90 * time.Second,
	}
}

// Client is a minimal Kling video-generation client. It never reads or
// stores the vendor API key itself: the caller passes through whatever
// Authorization header value it received from the broker (which injects the
// DASHSCOPE_API_KEY via its AdditionalSecret config; the translator just
// relays it).
type Client struct {
	baseURL       string
	httpClient    *http.Client
	contentClient *http.Client
}

// NewClient builds a Client. baseURL is NOT defaulted (see the package-level
// doc above) — an empty value here is a misconfiguration, not "use the
// public endpoint", because Kling has no public/universal endpoint to use. A
// nil httpClient gets a 30s-timeout default. The API client and a
// longer-timeout variant for FetchContent share one underlying
// *http.Transport (safe for concurrent use across http.Client wrappers) so
// connections are pooled regardless of which timeout a given call needs.
func NewClient(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second, Transport: defaultTransport()}
	}
	contentClient := &http.Client{Timeout: ContentFetchTimeout, Transport: httpClient.Transport}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), httpClient: httpClient, contentClient: contentClient}
}

// CreateTask submits a video-generation job and returns the initial task
// (task_id/task_status). authHeader is forwarded as-is to Kling's
// Authorization header. X-DashScope-Async: enable is REQUIRED on every
// create — Kling (like the rest of the DashScope family) has no synchronous
// mode at all, unlike DashScope's own HappyHorse video model where this
// header is merely how async is requested; here there is no alternative.
func (c *Client) CreateTask(ctx context.Context, authHeader string, req CreateRequest) (*CreateResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode kling create request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+createPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build kling create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-DashScope-Async", "enable")
	if authHeader != "" {
		httpReq.Header.Set("Authorization", authHeader)
	}

	var out CreateResponse
	if err := c.do(httpReq, &out); err != nil {
		return nil, fmt.Errorf("kling create task: %w", err)
	}
	// A 200 with no task_id is a malformed/unexpected response shape — surface
	// it as a failure rather than handing the translator (and the broker) a
	// job it can never poll. Mirrors the MiniMax/Seedance TaskID=="" guard:
	// without this, a create could silently succeed into an untrackable,
	// never-billed "ghost" job.
	if out.Output.TaskID == "" {
		return nil, fmt.Errorf("kling create task: response contained no task_id (unexpected response shape)")
	}
	return &out, nil
}

// GetTask polls the status of a previously created task. taskID is
// path-escaped before being spliced into the URL — it originates from the
// client's URL path (the broker relays it verbatim), and an unescaped "?" or
// "#" would otherwise be re-parsed as a real query string or fragment on the
// outbound call.
func (c *Client) GetTask(ctx context.Context, authHeader, taskID string) (*GetTaskResponse, error) {
	reqURL := c.baseURL + fmt.Sprintf(getTaskPathFmt, url.PathEscape(taskID))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build kling get-task request: %w", err)
	}
	if authHeader != "" {
		httpReq.Header.Set("Authorization", authHeader)
	}

	var out GetTaskResponse
	if err := c.do(httpReq, &out); err != nil {
		return nil, fmt.Errorf("kling get task: %w", err)
	}
	return &out, nil
}

// FetchContent streams the video bytes from a Kling-provided asset URL
// (output.video_url — a time-limited CDN link with 30-day validity per the
// vendor's docs, not the Kling API itself, so no Authorization header is
// attached). The caller must close the returned response's Body.
//
// Deliberately does NOT report its TLS certificate to the request's
// CertCapture, unlike do(): this URL is vendor-supplied (a CDN host), while
// the broker's routing proof must bind the API endpoint that authenticated
// our key and produced the response being signed. Do not add an Observe call
// here for symmetry (mirrors the identical comment on the
// DashScope/MiniMax/Seedance clients).
func (c *Client) FetchContent(ctx context.Context, videoURL string) (*http.Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, videoURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build content request: %w", err)
	}

	resp, err := c.contentClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("fetch video content: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("fetch video content: status %d", resp.StatusCode)
	}
	return resp, nil
}

// APIError is returned for any Kling request-level failure — a non-200 HTTP
// status. Confirmed via Aliyun's own Model Studio error-code documentation:
// Kling/DashScope-family requests report request-level failures with real
// HTTP status codes only, not a code buried in a 200 body, so do() below
// treats a non-200 as the error. Seedance's own client makes the same
// non-200-only assumption, but (per that file's own doc) defensively rather
// than confirmed against a dedicated error-code reference — don't treat that
// as settled fact for Seedance the way it is here for Kling. MiniMax is the
// outlier: it ALSO treats a 200 carrying a non-zero base_resp.status_code as
// a request-level failure, a defensive fallback for its older API surface
// that Kling has no equivalent of. Callers can errors.As this to distinguish
// a request Kling rejected outright (bad auth, bad parameter, quota) from a
// genuine transport/5xx failure (see handler.writeKlingError).
type APIError struct {
	StatusCode int
	Code       string
	Message    string
	// Body is the raw response body, kept for logging when Code/Message
	// couldn't be parsed out of it.
	Body string
	// RequestID is Kling's own correlation id, when present, threaded through
	// for support-ticket correlation.
	RequestID string
}

func (e *APIError) Error() string {
	if e.RequestID != "" {
		return fmt.Sprintf("kling status %d: code=%q message=%q request_id=%q", e.StatusCode, e.Code, e.Message, e.RequestID)
	}
	return fmt.Sprintf("kling status %d: code=%q message=%q", e.StatusCode, e.Code, e.Message)
}

// errorBody is a best-effort parse of a non-200 Kling body. Kling is
// DashScope-family transport, so this mirrors the same {code, message,
// request_id} shape dashscope.errorBody documents for a request-level
// failure — distinct from a task-level failure, which is reported as
// output.code/output.message inside an otherwise-200 get-task response once
// task_status reaches FAILED (see translate.FromKlingGetTaskResponse).
type errorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

func (c *Client) do(httpReq *http.Request, out interface{}) error {
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// Report the vendor's TLS certificate to whoever is handling this inbound
	// request, so the broker can bind it into a centralized routing proof: this
	// hop is where the TLS the proof attests to actually happens (the broker's own
	// hop to this sidecar is plaintext HTTP inside the CVM). No-op when no capture
	// is installed on the context. DO NOT OMIT — this is one of the two mandatory
	// TEE-routing-proof halves (the other is handler.NewEngine() in
	// cmd/server/kling.go). A different, not-yet-merged video/image integration
	// in this repository's history omitted this exact line and needed a
	// follow-up fix to add it back — it was never released without it, since it
	// never reached main; this client carries the line from its first commit
	// instead of repeating that gap.
	teeutil.CertCaptureFromContext(httpReq.Context()).Observe(resp.TLS)

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		apiErr := &APIError{StatusCode: resp.StatusCode, Body: string(respBody)}
		var eb errorBody
		// Error deliberately ignored, not checked: encoding/json fills every field
		// it CAN before returning a type error, and this is a best-effort parse of
		// a shape the vendor does not guarantee. Gating on `== nil` would throw
		// away a code/message that decoded fine just because some sibling key had
		// an unexpected type.
		_ = json.Unmarshal(respBody, &eb)
		apiErr.Code = eb.Code
		apiErr.Message = eb.Message
		apiErr.RequestID = eb.RequestID
		return apiErr
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
