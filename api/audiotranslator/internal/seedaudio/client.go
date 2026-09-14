package seedaudio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	teeutil "github.com/0glabs/0g-serving-broker/common/tee"
)

const (
	// DefaultBaseURL is BytePlus Voice's ap-southeast-1 endpoint. Scheme+host
	// only: the /api/v3 version lives in createPath, so a base carrying a path
	// would double-prefix every call.
	DefaultBaseURL = "https://voice.ap-southeast-1.bytepluses.com"

	createPath = "/api/v3/tts/create"

	// HeaderAPIKey and HeaderRequestID are BytePlus Voice's auth and tracing
	// headers. NOT Authorization: this platform does not use bearer tokens,
	// unlike Ark, which is what Seedance speaks.
	HeaderAPIKey    = "X-Api-Key"
	HeaderRequestID = "X-Api-Request-Id"
)

// Client talks to Seed Audio. It holds no per-request state.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient returns a client for baseURL, defaulting to the public endpoint when
// empty. Trailing slashes are trimmed so a base with one does not produce a
// double slash in the path.
func NewClient(baseURL string, httpClient *http.Client) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 2 * time.Minute}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), httpClient: httpClient}
}

// Create synthesizes audio. One round trip: the response carries the audio.
//
// credentials is the full header SET the broker forwards, not a single value.
// BytePlus Voice authenticates with X-Api-Key (and accepts a client-supplied
// X-Api-Request-Id for tracing), so a single-string authHeader parameter — which
// is what the seedance client takes — would silently drop everything but the
// first header and fail every call with what looks like a bad key.
func (c *Client) Create(ctx context.Context, credentials map[string]string, req CreateRequest) (*CreateResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal create request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+createPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range credentials {
		httpReq.Header.Set(k, v)
	}

	var out CreateResponse
	if err := c.do(httpReq, &out); err != nil {
		return nil, err
	}
	// A 200 with a non-zero code is a vendor-level failure. Treated as an error
	// rather than returned as an empty success, so the broker never bills a
	// request that produced no audio.
	if out.Code != 0 {
		return nil, &APIError{StatusCode: http.StatusOK, Code: out.Code, Message: out.Message}
	}
	if out.Audio == "" {
		return nil, &APIError{StatusCode: http.StatusOK, Message: "vendor returned no audio"}
	}
	return &out, nil
}

func (c *Client) do(httpReq *http.Request, out interface{}) error {
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Report the vendor's TLS certificate to whoever is handling this inbound
	// request, so the broker can bind it into a centralized routing proof: this
	// hop is where the TLS the proof attests to actually happens (the broker's
	// own hop to this sidecar is plaintext HTTP inside the CVM). No-op when no
	// capture is installed. DO NOT OMIT — this is one of the two mandatory
	// TEE-routing-proof halves; the other is handler.NewEngine() in cmd/server.
	teeutil.CertCaptureFromContext(httpReq.Context()).Observe(resp.TLS)

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		apiErr := &APIError{StatusCode: resp.StatusCode, Body: string(respBody)}
		// Best-effort: the vendor documents its error shape at the task level,
		// not for HTTP-level failures, so a parse failure here is expected and
		// the raw body is kept either way.
		var eb CreateResponse
		_ = json.Unmarshal(respBody, &eb)
		apiErr.Code = eb.Code
		apiErr.Message = eb.Message
		return apiErr
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// APIError is a vendor failure, whether reported by HTTP status or by a non-zero
// code in a 200 body.
type APIError struct {
	StatusCode int
	Code       int
	Message    string
	Body       string
}

func (e *APIError) Error() string {
	switch {
	case e.Message != "":
		return fmt.Sprintf("seedaudio: http %d, code %d: %s", e.StatusCode, e.Code, e.Message)
	case e.Body != "":
		return fmt.Sprintf("seedaudio: http %d: %s", e.StatusCode, truncate(e.Body, 256))
	default:
		return fmt.Sprintf("seedaudio: http %d", e.StatusCode)
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}
