package minimax

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewClient_Defaults(t *testing.T) {
	t.Run("empty baseURL falls back to the public overseas endpoint", func(t *testing.T) {
		if c := NewClient("", &http.Client{}); c.baseURL != defaultBaseURL {
			t.Errorf("baseURL = %q, want %q", c.baseURL, defaultBaseURL)
		}
	})
	t.Run("nil httpClient gets a 30s-timeout default", func(t *testing.T) {
		c := NewClient("https://example.com", nil)
		if c.httpClient == nil || c.httpClient.Timeout != 30*time.Second {
			t.Fatalf("httpClient = %+v, want a 30s default", c.httpClient)
		}
	})
	t.Run("trailing slash trimmed", func(t *testing.T) {
		if c := NewClient("https://example.com/", &http.Client{}); c.baseURL != "https://example.com" {
			t.Errorf("baseURL = %q, want no trailing slash", c.baseURL)
		}
	})
}

func TestCreateTask_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != createPath {
			t.Errorf("path = %q, want %q", r.URL.Path, createPath)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer k" {
			t.Errorf("auth = %q, want Bearer k", got)
		}
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"task_id":"425080991981768"}`)
	}))
	defer srv.Close()

	resp, err := NewClient(srv.URL, srv.Client()).CreateTask(context.Background(), "Bearer k", CreateRequest{Model: "MiniMax-H3"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if resp.TaskID != "425080991981768" {
		t.Errorf("task_id = %q", resp.TaskID)
	}
}

func TestCreateTask_EmptyTaskIDIsError(t *testing.T) {
	// A 200 whose body isn't the expected shape (e.g. "{}") must NOT become a
	// trackable job — it has no id to poll, so the broker could never bill it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	if _, err := NewClient(srv.URL, srv.Client()).CreateTask(context.Background(), "Bearer k", CreateRequest{}); err == nil {
		t.Fatal("want error for a create response with no task_id, got nil")
	}
}

func TestCreateTask_HTTPError(t *testing.T) {
	// The V2 API uses real HTTP codes (a bad key returns 401, confirmed live).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"base_resp":{"status_code":1004,"status_msg":"invalid api key"}}`)
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL, srv.Client()).CreateTask(context.Background(), "Bearer bad", CreateRequest{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *APIError, got %v", err)
	}
	if apiErr.StatusCode != http.StatusUnauthorized || apiErr.Message != "invalid api key" {
		t.Errorf("unexpected APIError: %+v", apiErr)
	}
}

// TestCreateTask_V2ErrorEnvelope pins the shape /v2/video_generation ACTUALLY
// returns — captured verbatim from api.minimax.io. It was unparsed here, so every
// rejection from this endpoint reached the operator as `code="" message=""`, and
// a live outage (a missing required "ratio") took a packet capture to diagnose
// instead of one log line.
func TestCreateTask_V2ErrorEnvelope(t *testing.T) {
	// The live body verbatim, except that http_code is made to DISAGREE with the
	// response line. That disagreement is the point: with both at 400 the
	// assertion below passes whether or not the body is allowed to win, so it
	// would pin nothing. 429 is what a vendor would pick to make a caller back off.
	const body = `{"type":"error","error":{"type":"bad_request_error","message":"invalid params, ratio is required for t2va (text-only) and cannot be 'adaptive'; allowed: 16:9/4:3/1:1/3:4/9:16/21:9 (2013)","http_code":"429"},"request_id":"06bbd1460c7f0626b11a6df66cc4135c"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, body)
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL, srv.Client()).CreateTask(context.Background(), "Bearer k", CreateRequest{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *APIError, got %v", err)
	}
	// The response LINE decides, never the body: the handler passes this straight
	// to c.JSON, so a vendor-stated status would steer what the client is told.
	if apiErr.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d, want the response line's 400 — the body's http_code=429 must not win", apiErr.StatusCode)
	}
	if !strings.Contains(apiErr.Message, "ratio is required") {
		t.Errorf("Message = %q, want the vendor's explanation", apiErr.Message)
	}
	if apiErr.Code != "bad_request_error" {
		t.Errorf("Code = %q, want the envelope's error type", apiErr.Code)
	}
	// What MiniMax support asks for.
	if apiErr.RequestID != "06bbd1460c7f0626b11a6df66cc4135c" {
		t.Errorf("RequestID = %q, want it captured", apiErr.RequestID)
	}
	if apiErr.Body != body {
		t.Errorf("raw body must be kept for the unparseable case")
	}
}

// TestCreateTask_PartialParseStillYieldsCode: the parse is best-effort over a
// shape the vendor does not guarantee, so one unexpectedly-typed sibling key must
// not discard the envelope that decoded fine. Gating on the unmarshal error did
// exactly that, and regressed bodies that parsed before the `error` field existed.
func TestCreateTask_PartialParseStillYieldsCode(t *testing.T) {
	for name, body := range map[string]string{
		// A gateway's string-valued "error" next to MiniMax's legacy envelope.
		"error is a string": `{"error":"upstream failure","base_resp":{"status_code":1004,"status_msg":"invalid api key"}}`,
		// request_id unquoted.
		"request_id is a number": `{"request_id":12345,"base_resp":{"status_code":1004,"status_msg":"invalid api key"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				io.WriteString(w, body)
			}))
			defer srv.Close()

			_, err := NewClient(srv.URL, srv.Client()).CreateTask(context.Background(), "Bearer k", CreateRequest{})
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("want *APIError, got %v", err)
			}
			if apiErr.Code != "1004" || apiErr.Message != "invalid api key" {
				t.Errorf("code=%q message=%q — a mistyped sibling key discarded a base_resp that decoded fine", apiErr.Code, apiErr.Message)
			}
		})
	}
}

// request_id sits at the top level of every shape, so it must survive whichever
// envelope wins — not only the one whose case happens to assign it.
func TestCreateTask_RequestIDSurvivesAnyEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"base_resp":{"status_code":1004,"status_msg":"invalid api key"},"request_id":"zzz"}`)
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL, srv.Client()).CreateTask(context.Background(), "Bearer k", CreateRequest{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.RequestID != "zzz" {
		t.Errorf("RequestID = %q, want it captured on the base_resp path too", apiErr.RequestID)
	}
}

func TestCreateTask_BaseRespIn200(t *testing.T) {
	// Defensive fallback: a legacy-shaped non-zero base_resp inside an HTTP 200.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"task_id":"","base_resp":{"status_code":1008,"status_msg":"insufficient balance"}}`)
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL, srv.Client()).CreateTask(context.Background(), "Bearer k", CreateRequest{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *APIError, got %v", err)
	}
	if apiErr.StatusCode != http.StatusPaymentRequired { // 1008 -> 402
		t.Errorf("StatusCode = %d, want 402", apiErr.StatusCode)
	}
}

func TestGetTask_EscapesTaskID(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"task":{"id":"x","status":"succeeded"}}`)
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL, srv.Client()).GetTask(context.Background(), "", "a/b?c#d")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if strings.ContainsAny(strings.TrimPrefix(gotPath, "/v2/query/video_generation/"), "?#") {
		t.Errorf("task id not escaped in path: %q", gotPath)
	}
}

func TestFetchContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		io.WriteString(w, "MP4BYTES")
	}))
	defer srv.Close()

	resp, err := NewClient("", srv.Client()).FetchContent(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("FetchContent: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "MP4BYTES" {
		t.Errorf("body = %q", b)
	}
}

func TestBaseRespStatusToHTTP(t *testing.T) {
	tests := []struct {
		code int64
		want int
	}{
		{1004, http.StatusUnauthorized},
		{1008, http.StatusPaymentRequired},
		{1002, http.StatusTooManyRequests},
		{2013, http.StatusUnprocessableEntity},
		{999999, http.StatusBadGateway},
	}
	for _, tt := range tests {
		if got := baseRespStatusToHTTP(tt.code); got != tt.want {
			t.Errorf("baseRespStatusToHTTP(%d) = %d, want %d", tt.code, got, tt.want)
		}
	}
}

func TestBaseRespError_SuccessIsNil(t *testing.T) {
	if baseRespError(nil) != nil {
		t.Error("nil base_resp should yield nil error")
	}
	if baseRespError(&BaseResp{StatusCode: 0}) != nil {
		t.Error("status_code 0 should yield nil error")
	}
}
