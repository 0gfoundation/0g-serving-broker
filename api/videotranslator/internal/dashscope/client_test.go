package dashscope

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetTask_EscapesTaskID(t *testing.T) {
	var gotPath, gotRawQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotRawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(GetTaskResponse{Output: TaskOutput{TaskID: "task-1", TaskStatus: TaskStatusSucceeded}})
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())

	// A task ID containing a literal "?" — if the outbound URL were built by
	// naive string concatenation without escaping, this would be
	// re-interpreted as a query string separator by http.NewRequestWithContext.
	maliciousID := "abc?foo=bar"
	if _, err := client.GetTask(context.Background(), "", maliciousID); err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}

	if gotRawQuery != "" {
		t.Errorf("server saw RawQuery = %q, want empty (the \"?\" should have been escaped into the path, not parsed as a query separator)", gotRawQuery)
	}
	if !strings.Contains(gotPath, "abc%3Ffoo%3Dbar") && !strings.HasSuffix(gotPath, "abc?foo=bar") {
		// net/http decodes the escaped path for r.URL.Path, so the server-side
		// Path ends up literally containing "abc?foo=bar" — the point is that
		// it landed in the PATH, not that RawQuery was empty due to a stripped
		// query. Assert both together above; this just documents the shape.
		t.Errorf("server saw Path = %q, want it to contain the literal task ID as a single path segment", gotPath)
	}
}

func TestFetchContent(t *testing.T) {
	t.Run("streams body and reports success", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("fake video bytes"))
		}))
		defer server.Close()

		client := NewClient(server.URL, server.Client())
		resp, err := client.FetchContent(context.Background(), server.URL+"/asset.mp4")
		if err != nil {
			t.Fatalf("FetchContent() error = %v", err)
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if string(body) != "fake video bytes" {
			t.Errorf("body = %q, want %q", body, "fake video bytes")
		}
	})

	t.Run("non-200 upstream status is an error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		client := NewClient(server.URL, server.Client())
		if _, err := client.FetchContent(context.Background(), server.URL+"/missing.mp4"); err == nil {
			t.Error("expected an error for a non-200 content fetch, got nil")
		}
	})
}
