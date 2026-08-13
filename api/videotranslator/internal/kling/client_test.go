package kling

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
	t.Run("empty baseURL is NOT defaulted (Kling has no universal endpoint)", func(t *testing.T) {
		if c := NewClient("", &http.Client{}); c.baseURL != "" {
			t.Errorf("baseURL = %q, want empty (must not silently default anywhere)", c.baseURL)
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
		if got := r.Header.Get("X-DashScope-Async"); got != "enable" {
			t.Errorf("X-DashScope-Async = %q, want enable (Kling has no sync mode)", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("content-type = %q, want application/json", got)
		}
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"output":{"task_status":"PENDING","task_id":"abc123"},"request_id":"req-1"}`)
	}))
	defer srv.Close()

	resp, err := NewClient(srv.URL, srv.Client()).CreateTask(context.Background(), "Bearer k", CreateRequest{Model: "kling/kling-v3-video-generation"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if resp.Output.TaskID != "abc123" {
		t.Errorf("task_id = %q", resp.Output.TaskID)
	}
	if resp.Output.TaskStatus != TaskStatusPending {
		t.Errorf("task_status = %q, want PENDING", resp.Output.TaskStatus)
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"code":"InvalidParameter","message":"duration must be between 3 and 15","request_id":"req-123"}`)
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL, srv.Client()).CreateTask(context.Background(), "Bearer bad", CreateRequest{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *APIError, got %v", err)
	}
	if apiErr.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d, want 400", apiErr.StatusCode)
	}
	if apiErr.Code != "InvalidParameter" || !strings.Contains(apiErr.Message, "duration must be between") {
		t.Errorf("unexpected APIError: %+v", apiErr)
	}
	if apiErr.RequestID != "req-123" {
		t.Errorf("RequestID = %q, want req-123", apiErr.RequestID)
	}
}

func TestCreateTask_UnparseableErrorBodyKeepsRawBody(t *testing.T) {
	const body = `not json at all`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		io.WriteString(w, body)
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL, srv.Client()).CreateTask(context.Background(), "Bearer k", CreateRequest{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *APIError, got %v", err)
	}
	if apiErr.StatusCode != http.StatusBadGateway {
		t.Errorf("StatusCode = %d, want 502", apiErr.StatusCode)
	}
	if apiErr.Body != body {
		t.Errorf("raw body must be kept for the unparseable case, got %q", apiErr.Body)
	}
	if apiErr.Code != "" || apiErr.Message != "" {
		t.Errorf("Code/Message should stay empty for an unparseable body, got code=%q message=%q", apiErr.Code, apiErr.Message)
	}
}

func TestGetTask_EscapesTaskID(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"output":{"task_id":"x","task_status":"SUCCEEDED"}}`)
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL, srv.Client()).GetTask(context.Background(), "", "a/b?c#d")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if strings.ContainsAny(strings.TrimPrefix(gotPath, "/api/v1/tasks/"), "?#") {
		t.Errorf("task id not escaped in path: %q", gotPath)
	}
}

func TestGetTask_ParsesUsageAndContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{
			"output": {
				"task_id": "abc123",
				"task_status": "SUCCEEDED",
				"video_url": "https://cdn.example/x.mp4",
				"watermark_video_url": "https://cdn.example/x-wm.mp4",
				"orig_prompt": "a cat"
			},
			"usage": {"duration": 5, "size": "1280*720", "fps": 24, "SR": "720", "audio": false, "video_count": 1},
			"request_id": "req-2"
		}`)
	}))
	defer srv.Close()

	resp, err := NewClient(srv.URL, srv.Client()).GetTask(context.Background(), "Bearer k", "abc123")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if resp.Output.TaskStatus != TaskStatusSucceeded {
		t.Errorf("task_status = %q, want SUCCEEDED", resp.Output.TaskStatus)
	}
	if resp.Output.VideoURL != "https://cdn.example/x.mp4" {
		t.Fatalf("video_url = %q", resp.Output.VideoURL)
	}
	if resp.Usage == nil || resp.Usage.Duration.String() != "5" {
		t.Fatalf("usage = %+v, want duration=5", resp.Usage)
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

func TestFetchContent_NonOKIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if _, err := NewClient("", srv.Client()).FetchContent(context.Background(), srv.URL); err == nil {
		t.Fatal("want error for a non-200 content fetch")
	}
}
