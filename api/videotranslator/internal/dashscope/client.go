package dashscope

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	defaultBaseURL = "https://dashscope.aliyuncs.com"
	createPath     = "/api/v1/services/aigc/video-generation/video-synthesis"
	getTaskPathFmt = "/api/v1/tasks/%s"
)

// Client is a minimal DashScope video-generation client. It never reads or
// stores the vendor API key itself: the caller passes through whatever
// Authorization header value it received from the broker (see
// 0gfoundation/0g-serving-broker#582 — the broker injects the DashScope key
// via its existing AdditionalSecret config, the translator just relays it).
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient builds a Client. An empty baseURL defaults to the public
// DashScope endpoint; a nil httpClient gets a 30s-timeout default.
func NewClient(baseURL string, httpClient *http.Client) *Client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), httpClient: httpClient}
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

// GetTask polls the status of a previously created task.
func (c *Client) GetTask(ctx context.Context, authHeader, taskID string) (*GetTaskResponse, error) {
	url := c.baseURL + fmt.Sprintf(getTaskPathFmt, taskID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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
