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
