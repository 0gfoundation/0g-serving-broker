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

// contentFetchTimeout bounds a full video content download via FetchContent.
// It's deliberately longer than the API-call client's own timeout: a status
// check or job creation is small and fast, but streaming an actual video
// file can legitimately take longer.
const contentFetchTimeout = 5 * time.Minute

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
	contentClient := &http.Client{Timeout: contentFetchTimeout, Transport: httpClient.Transport}
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
		return fmt.Errorf("status %d: %s", resp.StatusCode, respBody)
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
