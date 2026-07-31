package minimax

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

// defaultBaseURL is MiniMax's overseas endpoint, the one the H3 integration
// guide documents. An account provisioned on the domestic site
// (api.minimaxi.com) MUST override this via MINIMAX_BASE_URL — the key,
// endpoint, and model all belong to the same site.
const (
	defaultBaseURL = "https://api.minimax.io"
	createPath     = "/v2/video_generation"
	getTaskPathFmt = "/v2/query/video_generation/%s"
)

// ContentFetchTimeout bounds a full video content download via FetchContent —
// longer than the API-call client's own timeout, since streaming an MP4 can
// legitimately take longer than a small status/create call. Exported so
// cmd/server can derive its inbound WriteTimeout from it with an explicit
// margin. Mirrors dashscope.ContentFetchTimeout.
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

// Client is a minimal MiniMax video-generation client. It never reads or
// stores the vendor API key itself: the caller passes through whatever
// Authorization header value it received from the broker (which injects the
// MiniMax key via its AdditionalSecret config; the translator just relays it).
type Client struct {
	baseURL       string
	httpClient    *http.Client
	contentClient *http.Client
}

// NewClient builds a Client. An empty baseURL defaults to the public overseas
// endpoint; a nil httpClient gets a 30s-timeout default. The API client and a
// longer-timeout variant for FetchContent share one underlying *http.Transport
// (safe for concurrent use across http.Client wrappers) so connections are
// pooled regardless of which timeout a given call needs.
func NewClient(baseURL string, httpClient *http.Client) *Client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second, Transport: defaultTransport()}
	}
	contentClient := &http.Client{Timeout: ContentFetchTimeout, Transport: httpClient.Transport}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), httpClient: httpClient, contentClient: contentClient}
}

// CreateTask submits a video-generation job and returns the initial task
// (task_id). authHeader is forwarded as-is to MiniMax's Authorization header.
func (c *Client) CreateTask(ctx context.Context, authHeader string, req CreateRequest) (*CreateResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode minimax create request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+createPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build minimax create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		httpReq.Header.Set("Authorization", authHeader)
	}

	var out CreateResponse
	if err := c.do(httpReq, &out); err != nil {
		return nil, fmt.Errorf("minimax create task: %w", err)
	}
	if apiErr := baseRespError(out.BaseResp); apiErr != nil {
		return nil, fmt.Errorf("minimax create task: %w", apiErr)
	}
	// A 200 with no task_id (and no base_resp error) is a malformed response —
	// an error page or unexpected shape that unmarshalled cleanly into an empty
	// struct. Surface it as a failure rather than letting the translator return
	// a "queued" job the broker can never poll (empty provider job id). Without
	// this the create silently succeeds into an untrackable, never-billed job.
	if out.TaskID == "" {
		return nil, fmt.Errorf("minimax create task: response contained no task_id (unexpected response shape)")
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
		return nil, fmt.Errorf("build minimax get-task request: %w", err)
	}
	if authHeader != "" {
		httpReq.Header.Set("Authorization", authHeader)
	}

	var out GetTaskResponse
	if err := c.do(httpReq, &out); err != nil {
		return nil, fmt.Errorf("minimax get task: %w", err)
	}
	if apiErr := baseRespError(out.BaseResp); apiErr != nil {
		return nil, fmt.Errorf("minimax get task: %w", apiErr)
	}
	return &out, nil
}

// FetchContent streams the video bytes from a MiniMax-provided asset URL
// (task.content.url — a time-limited public CDN link, not the MiniMax API
// itself, so no Authorization header is attached). The caller must close the
// returned response's Body.
//
// Deliberately does NOT report its TLS certificate to the request's CertCapture,
// unlike do(): this URL is vendor-supplied (a CDN host), while the broker's routing
// proof must bind the API endpoint that authenticated our key and produced the
// response being signed. Do not add an Observe call here for symmetry.
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

// APIError is returned for any MiniMax request-level failure — a non-200 HTTP
// status, or a 200 carrying a non-zero base_resp.status_code. Callers can
// errors.As this to distinguish a request MiniMax rejected outright (bad auth,
// bad parameter, quota) from a genuine transport/5xx failure (see
// handler.writeMiniMaxError).
type APIError struct {
	StatusCode int
	Code       string
	Message    string
	// Body is the raw response body, kept for logging when Code/Message
	// couldn't be parsed out of it.
	Body string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("minimax status %d: code=%q message=%q", e.StatusCode, e.Code, e.Message)
}

// baseRespStatusToHTTP maps a non-zero MiniMax base_resp.status_code to the
// HTTP status the handler surfaces. The V2 API primarily uses real HTTP codes,
// so this only fires on the legacy base_resp-in-200 fallback; known
// client-caused codes map to 4xx (so an OpenAI-SDK client classifies them as
// auth/rate/param errors), everything else to 502 (an upstream problem with no
// reliable client-actionable detail).
func baseRespStatusToHTTP(code int64) int {
	switch code {
	case 1004: // invalid/expired api key
		return http.StatusUnauthorized
	case 1008: // insufficient balance
		return http.StatusPaymentRequired
	case 1002, 1039: // rate limited / tokens-per-minute limited
		return http.StatusTooManyRequests
	case 1027, 2013: // content risk / invalid params
		return http.StatusUnprocessableEntity
	default:
		return http.StatusBadGateway
	}
}

// baseRespError converts a non-zero base_resp into an *APIError, or returns nil
// when base_resp is absent or reports success (status_code == 0).
func baseRespError(br *BaseResp) *APIError {
	if br == nil || br.StatusCode == 0 {
		return nil
	}
	return &APIError{
		StatusCode: baseRespStatusToHTTP(br.StatusCode),
		Code:       fmt.Sprintf("%d", br.StatusCode),
		Message:    br.StatusMsg,
	}
}

// errorBody is a best-effort parse of a non-200 MiniMax body. The V2 error
// shape isn't fully documented, so both a base_resp envelope and a top-level
// {status_code,status_msg} are attempted; whichever is present wins.
type errorBody struct {
	BaseResp   *BaseResp `json:"base_resp"`
	StatusCode int64     `json:"status_code"`
	StatusMsg  string    `json:"status_msg"`
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
	// is installed on the context.
	teeutil.CertCaptureFromContext(httpReq.Context()).Observe(resp.TLS)

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		apiErr := &APIError{StatusCode: resp.StatusCode, Body: string(respBody)}
		var eb errorBody
		if json.Unmarshal(respBody, &eb) == nil {
			switch {
			case eb.BaseResp != nil && eb.BaseResp.StatusCode != 0:
				apiErr.Code = fmt.Sprintf("%d", eb.BaseResp.StatusCode)
				apiErr.Message = eb.BaseResp.StatusMsg
			case eb.StatusCode != 0:
				apiErr.Code = fmt.Sprintf("%d", eb.StatusCode)
				apiErr.Message = eb.StatusMsg
			}
		}
		return apiErr
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
