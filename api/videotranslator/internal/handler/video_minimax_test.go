package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/videotranslator/internal/minimax"
)

// TestMiniMaxGetVideo_NilTaskIsTransient501 asserts that a MiniMax 200 with a
// null task is surfaced as a 5xx (so the broker's poller reschedules rather
// than terminally failing the job and serving it free), and is NOT translated
// into a 200 "failed".
func TestMiniMaxGetVideo_NilTaskIsTransient(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"task":null}`))
	}))
	defer upstream.Close()

	client := minimax.NewClient(upstream.URL, upstream.Client())
	h := NewMiniMaxVideoHandler(client, "2K", newTestLogger(t))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/videos/:id", h.GetVideo)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/videos/task-123", nil)
	engine.ServeHTTP(rec, req)

	if rec.Code < 500 {
		t.Fatalf("nil task should surface as a 5xx (transient), got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestMiniMaxGetVideo_SucceededBills asserts the completed path returns 200
// with the OpenAI status and the billing duration renamed correctly.
func TestMiniMaxGetVideo_SucceededBills(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"task":{"id":"t1","status":"succeeded","resolution":"2K","usage":{"total_seconds":5,"input_seconds":0,"output_seconds":5}}}`))
	}))
	defer upstream.Close()

	client := minimax.NewClient(upstream.URL, upstream.Client())
	h := NewMiniMaxVideoHandler(client, "2K", newTestLogger(t))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/videos/:id", h.GetVideo)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/videos/t1", nil)
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"status":"completed"`) || !strings.Contains(body, `"output_video_duration":5`) {
		t.Fatalf("unexpected body: %s", body)
	}
}
