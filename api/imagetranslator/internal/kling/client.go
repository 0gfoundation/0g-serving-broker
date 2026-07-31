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
)

// createPath / getTaskPathFmt are the vendor's only documented region
// (China North 2 / Beijing). The workspace-specific hostname
// ({WorkspaceId}.cn-beijing.maas.aliyuncs.com) is supplied via baseURL,
// which is REQUIRED configuration with no working default (see
// config.GetConfig's KLING_BASE_URL handling) — the hostname embeds a
// per-customer workspace ID, so there is no single sensible universal
// default the way MiniMax's api.minimax.io is one.
const (
	createPath     = "/api/v1/services/aigc/image-generation/generation"
	getTaskPathFmt = "/api/v1/tasks/%s"
)

// defaultTransport mirrors the tuned Transport the broker's shared HTTP
// client uses: a job is created once then polled repeatedly (this package's
// own poll loop, every klingPollInterval — see internal/handler/image.go),
// so the stdlib default MaxIdleConnsPerHost=2 would force a fresh TCP+TLS
// handshake per poll once more than ~2 jobs are in flight concurrently.
func defaultTransport() *http.Transport {
	return &http.Transport{
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 200,
		IdleConnTimeout:     90 * time.Second,
	}
}

// Client is a minimal Kling image-generation client. It never reads or
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
// workspace-specific Kling endpoint — there is no default (see the
// createPath doc comment above); a nil httpClient gets a 30s-timeout
// default. contentTimeout bounds each individual FetchImage call — a
// separate, typically shorter budget than the vendor API client's own
// timeout, since a single small PNG doesn't need the many-minutes budget a
// video content download would.
func NewClient(baseURL string, httpClient *http.Client, contentTimeout time.Duration) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second, Transport: defaultTransport()}
	}
	contentClient := &http.Client{Timeout: contentTimeout, Transport: httpClient.Transport}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), httpClient: httpClient, contentClient: contentClient}
}

// CreateTask submits an image-generation job and returns the initial task
// status. authHeader is forwarded as-is to the vendor's Authorization
// header. X-DashScope-Async: enable is set unconditionally — Kling has no
// synchronous mode, so this is not a configurable policy, just a hardcoded
// header (per the vendor's own documented error otherwise: "current user
// api does not support synchronous calls").
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
	// A 200 with no task_id (an error page / unexpected shape that
	// unmarshalled into an empty struct) must be a hard error, not a
	// well-formed "queued" job with an untrackable, never-billed empty
	// provider job id — mirrors the identical guard in dashscope/minimax/vidu.
	if out.Output.TaskID == "" {
		return nil, fmt.Errorf("kling create task: response contained no task_id (unexpected response shape)")
	}
	return &out, nil
}

// GetTask polls the status of a previously created task. taskID is
// path-escaped before being spliced into the request URL — it originates
// from the sidecar's own poll loop (which itself received it from the
// create response), and an unescaped "?" or "#" in it would otherwise be
// re-parsed by http.NewRequestWithContext as a real query string or
// fragment on the outbound call.
//
// This method does not itself distinguish Kling's three documented
// error-envelope shapes (see internal/translate/kling.go) beyond the
// generic non-200 case below — a 200 response, however shaped, is decoded
// as-is into GetTaskResponse, whose Output/Code/Message fields are then
// inspected by the translate layer to determine which shape was returned.
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

// FetchImage streams one generated image's bytes from a vendor-provided
// asset URL (a generated content[].image URL — a CDN link, not the vendor
// API itself, so no Authorization header is attached). The caller must
// close the returned response's Body, and must read it under an
// io.LimitReader cap (see internal/handler/image.go's klingMaxImageBytes) —
// this method does not itself bound response size.
func (c *Client) FetchImage(ctx context.Context, imageURL string) (*http.Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build image fetch request: %w", err)
	}

	resp, err := c.contentClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("fetch image: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("fetch image: status %d", resp.StatusCode)
	}
	return resp, nil
}

// APIError is returned by do() for any non-200 vendor response. Callers can
// errors.As this to distinguish a request the vendor rejected outright
// (4xx — bad auth, bad model/parameter, quota) from a genuine transport/5xx
// failure (see handler.writeKlingError, the caller that matters).
type APIError struct {
	StatusCode int
	Code       string
	Message    string
	// Body is the raw response body, kept for logging when Code/Message
	// couldn't be parsed out of it (a non-JSON or differently-shaped error
	// page, e.g. from a proxy/load balancer in front of the vendor).
	Body string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("kling status %d: code=%q message=%q", e.StatusCode, e.Code, e.Message)
}

// errorBody is the flat JSON shape a request-level (HTTP non-2xx) failure
// returns — {code, message, request_id}, the same shape the vendor's
// documented "task-level failure response" example also uses.
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
