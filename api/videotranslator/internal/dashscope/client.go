package dashscope

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
)

// defaultBaseURL is the legacy China (Beijing) domain — still fully
// functional per DashScope's docs, but only correct for that region. The
// model, endpoint, and API key must all belong to the same region (e.g.
// Singapore's dashscope-intl.aliyuncs.com, or one of the newer
// workspace-specific *.maas.aliyuncs.com domains); operators outside China
// MUST override this via DASHSCOPE_BASE_URL rather than relying on this
// default.
const (
	defaultBaseURL = "https://dashscope.aliyuncs.com"
	createPath     = "/api/v1/services/aigc/video-generation/video-synthesis"
	getTaskPathFmt = "/api/v1/tasks/%s"
)

// ContentFetchTimeout bounds a full video content download via FetchContent.
// It's deliberately longer than the API-call client's own timeout: a status
// check or job creation is small and fast, but streaming an actual video
// file can legitimately take longer. Exported so cmd/server can derive its
// inbound WriteTimeout from it with an explicit margin — GetVideoContent's
// request lifetime is GetTask's round trip plus up to this entire budget,
// so the inbound deadline must be comfortably larger than this value, not
// coincidentally equal to it.
const ContentFetchTimeout = 5 * time.Minute

// defaultTransport mirrors the tuned Transport the broker's own shared HTTP
// client uses (see inference/internal/ctrl/ctrl.go) — DashScope video jobs
// are created once then polled repeatedly, so the default
// MaxIdleConnsPerHost=2 would force a fresh TCP+TLS handshake per poll once
// more than ~2 jobs are in flight concurrently.
func defaultTransport() *http.Transport {
	return &http.Transport{
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 200,
		IdleConnTimeout:     90 * time.Second,
	}
}

// Client is a minimal DashScope video-generation client. It never reads or
// stores the vendor API key itself: the caller passes through whatever
// Authorization header value it received from the broker (see
// 0gfoundation/0g-serving-broker#582 — the broker injects the DashScope key
// via its existing AdditionalSecret config, the translator just relays it).
type Client struct {
	baseURL       string
	httpClient    *http.Client
	contentClient *http.Client
}

// NewClient builds a Client. An empty baseURL defaults to the public
// DashScope endpoint; a nil httpClient gets a 30s-timeout default. Both the
// given (or defaulted) client's Transport and a longer-timeout variant for
// FetchContent share the same underlying *http.Transport (safe for
// concurrent use across multiple http.Client wrappers), so connections to
// DashScope are pooled regardless of which timeout a given call needs.
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
// status. authHeader is forwarded as-is to DashScope's Authorization header.
func (c *Client) CreateTask(ctx context.Context, authHeader string, req CreateRequest) (*CreateResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode dashscope create request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+createPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build dashscope create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-DashScope-Async", "enable")
	if authHeader != "" {
		httpReq.Header.Set("Authorization", authHeader)
	}

	var out CreateResponse
	if err := c.do(httpReq, &out); err != nil {
		return nil, fmt.Errorf("dashscope create task: %w", err)
	}
	return &out, nil
}

// GetTask polls the status of a previously created task. taskID is
// path-escaped before being spliced into the request URL — it originates
// from the client's URL path (via the broker relaying it verbatim), and an
// unescaped "?" or "#" in it would otherwise be re-parsed by
// http.NewRequestWithContext as a real query string or fragment on the
// outbound DashScope call.
func (c *Client) GetTask(ctx context.Context, authHeader, taskID string) (*GetTaskResponse, error) {
	reqURL := c.baseURL + fmt.Sprintf(getTaskPathFmt, url.PathEscape(taskID))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build dashscope get-task request: %w", err)
	}
	if authHeader != "" {
		httpReq.Header.Set("Authorization", authHeader)
	}

	var out GetTaskResponse
	if err := c.do(httpReq, &out); err != nil {
		return nil, fmt.Errorf("dashscope get task: %w", err)
	}
	return &out, nil
}

// FetchContent streams the video bytes from a vendor-provided asset URL
// (DashScope's output.video_url — typically a pre-signed CDN link, not the
// DashScope API itself, so no Authorization header is attached). The caller
// must close the returned response's Body.
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

// APIError is returned by do() for any non-200 DashScope response. Callers
// can errors.As this to distinguish a request DashScope rejected outright
// (4xx — bad auth, bad model/parameter, quota) from a genuine transport/5xx
// failure, rather than treating every upstream error the same way (see
// handler.writeDashScopeError, the caller that matters).
type APIError struct {
	StatusCode int
	Code       string
	Message    string
	// Body is the raw response body, kept for logging when Code/Message
	// couldn't be parsed out of it (a non-JSON or differently-shaped error
	// page, e.g. from a proxy/load balancer in front of DashScope).
	Body string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("dashscope status %d: code=%q message=%q", e.StatusCode, e.Code, e.Message)
}

// errorBody is the JSON shape DashScope returns for a request-level (HTTP
// non-2xx) failure — distinct from a task-level failure, which is reported
// as output.code/output.message inside an otherwise-200 get-task response
// once task_status reaches FAILED.
type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (c *Client) do(httpReq *http.Request, out interface{}) error {
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

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
		}
		return apiErr
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
