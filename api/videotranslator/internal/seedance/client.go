package seedance

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

// defaultBaseURL is scheme+host ONLY — the API version lives in the path
// consts below (the same convention MiniMax's client uses). Confirmed host:
// ark.ap-southeast.bytepluses.com, region ap-southeast-1 (the design doc's
// §3.1). A eu-west-1 host exists but carries no video-generation models, so
// Seedance is ap-southeast-only.
//
// SEEDANCE_BASE_URL, if an operator overrides it, MUST likewise be
// scheme+host only (no path): its host must equal the on-chain
// upstreamDomain, or the broker's routing-proof domain check fails. A
// stray "/api/v3" suffix would double the path prefix on every call.
const (
	defaultBaseURL = "https://ark.ap-southeast.bytepluses.com"
	createPath     = "/api/v3/contents/generations/tasks"
	getTaskPathFmt = "/api/v3/contents/generations/tasks/%s"
)

// ContentFetchTimeout bounds a full video content download via FetchContent —
// longer than the API-call client's own timeout, since streaming an MP4 can
// legitimately take longer than a small status/create call. Exported so
// cmd/server can derive its inbound WriteTimeout from it with an explicit
// margin. Mirrors dashscope.ContentFetchTimeout / minimax.ContentFetchTimeout.
const ContentFetchTimeout = 5 * time.Minute

// defaultTransport mirrors the tuned Transport the broker's shared HTTP
// client uses: a video job is created once then polled repeatedly, so the
// stdlib default MaxIdleConnsPerHost=2 would force a fresh TCP+TLS handshake
// per poll once more than ~2 jobs are in flight concurrently.
func defaultTransport() *http.Transport {
	return &http.Transport{
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 200,
		IdleConnTimeout:     90 * time.Second,
	}
}

// Client is a minimal Seedance video-generation client. It never reads or
// stores the vendor API key itself: the caller passes through whatever
// Authorization header value it received from the broker (which injects the
// ARK_API_KEY via its AdditionalSecret config; the translator just relays
// it).
type Client struct {
	baseURL       string
	httpClient    *http.Client
	contentClient *http.Client
}

// NewClient builds a Client. An empty baseURL defaults to the BytePlus
// ap-southeast-1 endpoint; a nil httpClient gets a 30s-timeout default. The
// API client and a longer-timeout variant for FetchContent share one
// underlying *http.Transport (safe for concurrent use across http.Client
// wrappers) so connections are pooled regardless of which timeout a given
// call needs.
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

// CreateTask submits a video-generation job and returns the initial task id.
// authHeader is forwarded as-is to Seedance's Authorization header (no AK/SK
// signing — a simple Bearer token).
func (c *Client) CreateTask(ctx context.Context, authHeader string, req CreateRequest) (*CreateResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode seedance create request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+createPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build seedance create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		httpReq.Header.Set("Authorization", authHeader)
	}

	var out CreateResponse
	if err := c.do(httpReq, &out); err != nil {
		return nil, fmt.Errorf("seedance create task: %w", err)
	}
	// A 200 with no id is a malformed/unexpected response shape — surface it as
	// a failure rather than handing the translator (and the broker) a job it
	// can never poll. Mirrors the MiniMax/DashScope TaskID=="" guard.
	if out.ID == "" {
		return nil, fmt.Errorf("seedance create task: response contained no id (unexpected response shape)")
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
		return nil, fmt.Errorf("build seedance get-task request: %w", err)
	}
	if authHeader != "" {
		httpReq.Header.Set("Authorization", authHeader)
	}

	var out GetTaskResponse
	if err := c.do(httpReq, &out); err != nil {
		return nil, fmt.Errorf("seedance get task: %w", err)
	}
	return &out, nil
}

// FetchContent streams the video bytes from a Seedance-provided asset URL
// (content.video_url — a time-limited CDN link on
// *.tos-ap-southeast-1.volces.com, not the Ark API itself, so no
// Authorization header is attached). The caller must close the returned
// response's Body.
//
// Deliberately does NOT report its TLS certificate to the request's
// CertCapture, unlike do(): this URL is vendor-supplied (a CDN host), while
// the broker's routing proof must bind the API endpoint that authenticated
// our key and produced the response being signed. Do not add an Observe call
// here for symmetry (mirrors the identical comment on the MiniMax/DashScope
// clients).
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

// APIError is returned for any Seedance request-level failure — a non-200
// HTTP status. Callers can errors.As this to distinguish a request Seedance
// rejected outright (bad auth, bad parameter, content moderation, quota)
// from a genuine transport/5xx failure (see handler.writeSeedanceError).
type APIError struct {
	StatusCode int
	Code       string
	Message    string
	// Body is the raw response body, kept for logging when Code/Message
	// couldn't be parsed out of it.
	Body string
	// RequestID is the vendor's own correlation id, when present, threaded
	// through for support-ticket correlation.
	RequestID string
}

func (e *APIError) Error() string {
	if e.RequestID != "" {
		return fmt.Sprintf("seedance status %d: code=%q message=%q request_id=%q", e.StatusCode, e.Code, e.Message, e.RequestID)
	}
	return fmt.Sprintf("seedance status %d: code=%q message=%q", e.StatusCode, e.Code, e.Message)
}

// errorBody is a best-effort parse of a non-200 Seedance body. The
// documented task-level failure shape is a top-level {"error":{"code":...,
// "message":...}} envelope (the design doc's §3.7); the standalone HTTP
// "Error codes" reference wasn't available to confirm the HTTP-level error
// envelope is identical, so this is applied defensively to any non-200 —
// a body that doesn't match simply leaves Code/Message empty, falling back
// to the raw Body for logging (see vendorErrorDetail in the handler
// package), never a hard failure to parse.
type errorBody struct {
	Error     *TaskError `json:"error"`
	RequestID string     `json:"request_id"`
}

func (c *Client) do(httpReq *http.Request, out interface{}) error {
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// Report the vendor's TLS certificate to whoever is handling this inbound
	// request, so the broker can bind it into a centralized routing proof:
	// this hop is where the TLS the proof attests to actually happens (the
	// broker's own hop to this sidecar is plaintext HTTP inside the CVM).
	// No-op when no capture is installed on the context. DO NOT OMIT — this is
	// one of the two mandatory TEE-routing-proof halves (the other is
	// handler.NewEngine() in cmd/server/seedance.go).
	teeutil.CertCaptureFromContext(httpReq.Context()).Observe(resp.TLS)

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		apiErr := &APIError{StatusCode: resp.StatusCode, Body: string(respBody)}
		var eb errorBody
		// Error deliberately ignored: this is a best-effort parse of a shape the
		// vendor does not guarantee at the HTTP-error level (only the task-level
		// failure shape is documented). encoding/json fills every field it CAN
		// before returning a type error, so gating on `== nil` would discard an
		// envelope that decoded fine just because some sibling key had an
		// unexpected type.
		_ = json.Unmarshal(respBody, &eb)
		apiErr.RequestID = eb.RequestID
		if eb.Error != nil {
			apiErr.Code = eb.Error.Code
			apiErr.Message = eb.Error.Message
		}
		return apiErr
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
