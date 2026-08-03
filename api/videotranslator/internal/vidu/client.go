package vidu

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

// createPath / getTaskPathFmt are the only region documented by the vendor
// (China North 2 / Beijing) — the workspace-specific hostname
// ({WorkspaceId}.cn-beijing.maas.aliyuncs.com) is supplied via baseURL,
// which is REQUIRED configuration with no working default (see
// config.GetConfig's VIDU_BASE_URL handling) — unlike MiniMax's
// api.minimax.io, there is no single sensible universal default the
// hostname could fall back to, since it embeds a per-customer workspace ID.
const (
	createPath     = "/api/v1/services/aigc/video-generation/video-synthesis"
	getTaskPathFmt = "/api/v1/tasks/%s"
)

// ContentFetchTimeout bounds a full video content download via FetchContent
// — longer than the API-call client's own timeout, since streaming an MP4
// can legitimately take longer than a small status/create call. Mirrors
// dashscope.ContentFetchTimeout / minimax.ContentFetchTimeout.
const ContentFetchTimeout = 5 * time.Minute

// defaultTransport mirrors the tuned Transport the broker's shared HTTP
// client uses: a video job is created once then polled repeatedly (the
// vendor recommends ~15s intervals over a typical 1-5 minute generation), so
// the stdlib default MaxIdleConnsPerHost=2 would force a fresh TCP+TLS
// handshake per poll once more than ~2 jobs are in flight concurrently.
func defaultTransport() *http.Transport {
	return &http.Transport{
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 200,
		IdleConnTimeout:     90 * time.Second,
	}
}

// Client is a minimal Vidu video-generation client. It never reads or
// stores the vendor API key itself: the caller passes through whatever
// Authorization header value it received from the broker (the broker
// injects the DashScope-family key via its AdditionalSecret config; the
// translator just relays it).
type Client struct {
	baseURL       string
	httpClient    *http.Client
	contentClient *http.Client
}

// NewClient builds a Client. baseURL must be the deployment's real
// workspace-specific Vidu endpoint — there is no default (see the createPath
// doc comment above); a nil httpClient gets a 30s-timeout default. The API
// client and a longer-timeout variant for FetchContent share one underlying
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
// status. authHeader is forwarded as-is to the vendor's Authorization
// header.
func (c *Client) CreateTask(ctx context.Context, authHeader string, req CreateRequest) (*CreateResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode vidu create request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+createPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build vidu create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	// Unconditional, not configurable: Vidu has no synchronous mode. Per the
	// vendor's own docs, omitting this header fails with "current user api
	// does not support synchronous calls".
	httpReq.Header.Set("X-DashScope-Async", "enable")
	if authHeader != "" {
		httpReq.Header.Set("Authorization", authHeader)
	}

	var out CreateResponse
	if err := c.do(httpReq, &out); err != nil {
		return nil, fmt.Errorf("vidu create task: %w", err)
	}
	// A 200 with no task_id (an error page / unexpected shape that
	// unmarshalled into an empty struct) must be a hard error, not a
	// well-formed "queued" job with an untrackable, never-billed empty
	// provider job id — mirrors the identical guard in dashscope/minimax.
	if out.Output.TaskID == "" {
		return nil, fmt.Errorf("vidu create task: response contained no task_id (unexpected response shape)")
	}
	return &out, nil
}

// GetTask polls the status of a previously created task. taskID is
// path-escaped before being spliced into the request URL — it originates
// from the client's URL path (via the broker relaying it verbatim), and an
// unescaped "?" or "#" in it would otherwise be re-parsed by
// http.NewRequestWithContext as a real query string or fragment on the
// outbound call.
//
// This method does not itself distinguish Vidu's three documented
// error-envelope shapes (see internal/translate/vidu.go) beyond the
// generic non-200 case below — a 200 response, however shaped, is decoded
// as-is into GetTaskResponse, whose Output/Code/Message fields are then
// inspected by the translate layer to determine which shape was returned.
func (c *Client) GetTask(ctx context.Context, authHeader, taskID string) (*GetTaskResponse, error) {
	reqURL := c.baseURL + fmt.Sprintf(getTaskPathFmt, url.PathEscape(taskID))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build vidu get-task request: %w", err)
	}
	if authHeader != "" {
		httpReq.Header.Set("Authorization", authHeader)
	}

	var out GetTaskResponse
	if err := c.do(httpReq, &out); err != nil {
		return nil, fmt.Errorf("vidu get task: %w", err)
	}
	return &out, nil
}

// FetchContent streams the video bytes from a vendor-provided asset URL
// (output.video_url — a pre-signed CDN link, not the vendor API itself, so
// no Authorization header is attached). The caller must close the returned
// response's Body.
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

// APIError is returned by do() for any non-200 vendor response. Callers can
// errors.As this to distinguish a request the vendor rejected outright
// (4xx — bad auth, bad model/parameter, quota) from a genuine transport/5xx
// failure (see handler.writeProviderError, the caller that matters).
type APIError struct {
	StatusCode int
	Code       string
	Message    string
	// Body is the raw response body, kept for logging when Code/Message
	// couldn't be parsed out of it (a non-JSON or differently-shaped error
	// page, e.g. from a proxy/load balancer in front of the vendor).
	Body string
	// RequestID is the vendor's own correlation id — what their support asks
	// for. Mirrors dashscope.APIError/minimax.APIError's field.
	RequestID string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("vidu status %d: code=%q message=%q", e.StatusCode, e.Code, e.Message)
}

// errorBody is the flat JSON shape a request-level (HTTP non-2xx) failure
// returns — {code, message, request_id} — the same shape CreateFailure
// documents for a create-time rejection.
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
	// See the mirror of this line in internal/dashscope/client.go and
	// internal/minimax/client.go: this hop is where the TLS a centralized
	// routing proof attests to actually happens, so report the vendor
	// certificate back to the inbound request's capture.
	teeutil.CertCaptureFromContext(httpReq.Context()).Observe(resp.TLS)

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		apiErr := &APIError{StatusCode: resp.StatusCode, Body: string(respBody)}
		var eb errorBody
		if json.Unmarshal(respBody, &eb) == nil {
			apiErr.Code = eb.Code
			apiErr.Message = eb.Message
			apiErr.RequestID = eb.RequestID
		}
		return apiErr
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
