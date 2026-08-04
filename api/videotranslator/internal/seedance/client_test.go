package seedance

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
	t.Run("empty baseURL falls back to the ap-southeast-1 endpoint", func(t *testing.T) {
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
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("content-type = %q, want application/json", got)
		}
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"id":"cgt-20260606160057-6bbjd"}`)
	}))
	defer srv.Close()

	resp, err := NewClient(srv.URL, srv.Client()).CreateTask(context.Background(), "Bearer k", CreateRequest{Model: "dreamina-seedance-2-0-260128"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if resp.ID != "cgt-20260606160057-6bbjd" {
		t.Errorf("id = %q", resp.ID)
	}
}

func TestCreateTask_EmptyIDIsError(t *testing.T) {
	// A 200 whose body isn't the expected shape (e.g. "{}") must NOT become a
	// trackable job — it has no id to poll, so the broker could never bill it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	if _, err := NewClient(srv.URL, srv.Client()).CreateTask(context.Background(), "Bearer k", CreateRequest{}); err == nil {
		t.Fatal("want error for a create response with no id, got nil")
	}
}

func TestCreateTask_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"code":"InvalidParameter","message":"resolution must be one of 480p,720p,1080p,4k"},"request_id":"req-123"}`)
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
	if apiErr.Code != "InvalidParameter" || !strings.Contains(apiErr.Message, "resolution must be one of") {
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
		io.WriteString(w, `{"id":"x","status":"succeeded"}`)
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL, srv.Client()).GetTask(context.Background(), "", "a/b?c#d")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if strings.ContainsAny(strings.TrimPrefix(gotPath, "/api/v3/contents/generations/tasks/"), "?#") {
		t.Errorf("task id not escaped in path: %q", gotPath)
	}
}

func TestGetTask_ParsesUsageAndContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{
			"id": "cgt-20260606160057-6bbjd",
			"model": "dreamina-seedance-2-0-260128",
			"status": "succeeded",
			"content": {"video_url": "https://cdn.example/x.mp4"},
			"usage": {"completion_tokens": 246840, "total_tokens": 246840},
			"resolution": "1080p",
			"ratio": "16:9",
			"duration": 5,
			"framespersecond": 24
		}`)
	}))
	defer srv.Close()

	resp, err := NewClient(srv.URL, srv.Client()).GetTask(context.Background(), "Bearer k", "cgt-20260606160057-6bbjd")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if resp.Status != TaskStatusSucceeded {
		t.Errorf("status = %q, want succeeded", resp.Status)
	}
	if resp.Content == nil || resp.Content.VideoURL != "https://cdn.example/x.mp4" {
		t.Fatalf("content = %+v", resp.Content)
	}
	if resp.Usage == nil || resp.Usage.CompletionTokens.String() != "246840" {
		t.Fatalf("usage = %+v, want completion_tokens=246840", resp.Usage)
	}
	if resp.Duration.String() != "5" {
		t.Errorf("duration = %q, want 5", resp.Duration.String())
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
