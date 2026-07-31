package handler

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	commonconfig "github.com/0glabs/0g-serving-broker/common/config"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/imagetranslator/internal/kling"
	"github.com/gin-gonic/gin"
)

func newTestLogger(t *testing.T) log.Logger {
	t.Helper()
	logger, err := log.GetLogger(&commonconfig.LoggerConfig{Level: "error", Format: commonconfig.LogFormat(log.TextLogFormat)})
	if err != nil {
		t.Fatalf("failed to build logger: %v", err)
	}
	return logger
}

// TestCreateImage_EndToEndSuccess exercises the full poll-loop-then-respond
// flow: create -> one PENDING poll -> SUCCEEDED with 2 images -> both images
// downloaded and base64-assembled into the broker's {data:[{b64_json}]}
// envelope.
func TestCreateImage_EndToEndSuccess(t *testing.T) {
	pollCount := 0
	var mock *httptest.Server
	mock = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/image-generation/generation"):
			if r.Header.Get("X-DashScope-Async") != "enable" {
				t.Errorf("X-DashScope-Async header missing on create call")
			}
			json.NewEncoder(w).Encode(kling.CreateResponse{
				Output: kling.CreateOutput{TaskID: "task-1", TaskStatus: kling.TaskStatusPending},
			})
		case strings.HasPrefix(r.URL.Path, "/api/v1/tasks/"):
			pollCount++
			if pollCount == 1 {
				json.NewEncoder(w).Encode(kling.GetTaskResponse{
					Output: kling.TaskOutput{TaskID: "task-1", TaskStatus: kling.TaskStatusRunning},
				})
				return
			}
			json.NewEncoder(w).Encode(kling.GetTaskResponse{
				Output: kling.TaskOutput{
					TaskID:     "task-1",
					TaskStatus: kling.TaskStatusSucceeded,
					Choices: []kling.Choice{{
						Message: kling.ChoiceMessage{Content: []kling.ImageContentItem{
							{Type: "image", Image: "http://" + mock.Listener.Addr().String() + "/img1.png"},
							{Type: "image", Image: "http://" + mock.Listener.Addr().String() + "/img2.png"},
						}},
					}},
				},
				Usage: &kling.TaskUsage{ImageCount: 2},
			})
		case r.URL.Path == "/img1.png":
			w.Write([]byte("fake-image-bytes-1"))
		case r.URL.Path == "/img2.png":
			w.Write([]byte("fake-image-bytes-2"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mock.Close()

	client := kling.NewClient(mock.URL, mock.Client(), 5*time.Second)
	h := NewKlingHandler(client, newTestLogger(t))
	// Speed the test up: override the poll interval isn't exposed, so this
	// test relies on the default 5s interval firing at most twice — bound
	// the test's own timeout generously instead of tuning internals.

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/images/generations", h.CreateImage)

	body := strings.NewReader(`{"model":"kling/kling-v3-image-generation","prompt":"a cat","n":2}`)
	req := httptest.NewRequest(http.MethodPost, "/images/generations", body)
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		engine.ServeHTTP(rec, req)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("CreateImage did not complete within 20s (poll loop likely stuck)")
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp createImageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("data length = %d, want 2", len(resp.Data))
	}
	got1, _ := base64.StdEncoding.DecodeString(resp.Data[0].B64JSON)
	got2, _ := base64.StdEncoding.DecodeString(resp.Data[1].B64JSON)
	if string(got1) != "fake-image-bytes-1" || string(got2) != "fake-image-bytes-2" {
		t.Errorf("decoded images = %q, %q", got1, got2)
	}
}

// TestCreateImage_VendorPartialSuccessNoBill asserts a SUCCEEDED response
// delivering fewer images than requested returns a 502 with no download
// attempted and no partial data[] array — the all-or-nothing billing
// policy.
func TestCreateImage_VendorPartialSuccessNoBill(t *testing.T) {
	fetchCalled := false
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/image-generation/generation"):
			json.NewEncoder(w).Encode(kling.CreateResponse{
				Output: kling.CreateOutput{TaskID: "task-2", TaskStatus: kling.TaskStatusPending},
			})
		case strings.HasPrefix(r.URL.Path, "/api/v1/tasks/"):
			json.NewEncoder(w).Encode(kling.GetTaskResponse{
				Output: kling.TaskOutput{
					TaskID:     "task-2",
					TaskStatus: kling.TaskStatusSucceeded,
					Choices: []kling.Choice{{
						Message: kling.ChoiceMessage{Content: []kling.ImageContentItem{
							{Type: "image", Image: "http://example-unreachable/img1.png"},
						}},
					}},
				},
				Usage: &kling.TaskUsage{ImageCount: 1},
			})
		default:
			fetchCalled = true
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mock.Close()

	client := kling.NewClient(mock.URL, mock.Client(), 5*time.Second)
	h := NewKlingHandler(client, newTestLogger(t))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/images/generations", h.CreateImage)

	body := strings.NewReader(`{"model":"kling/kling-v3-image-generation","prompt":"a cat","n":3}`)
	req := httptest.NewRequest(http.MethodPost, "/images/generations", body)
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		engine.ServeHTTP(rec, req)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("CreateImage did not complete within 20s")
	}

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d (vendor delivered 1 of 3 requested)", rec.Code, http.StatusBadGateway)
	}
	if fetchCalled {
		t.Error("no image download should be attempted on vendor partial success")
	}
}

// TestCreateImage_RejectsOutOfRangeCount asserts the broker-facing 400 path
// never reaches the vendor at all.
func TestCreateImage_RejectsOutOfRangeCount(t *testing.T) {
	called := false
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer mock.Close()

	client := kling.NewClient(mock.URL, mock.Client(), 5*time.Second)
	h := NewKlingHandler(client, newTestLogger(t))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/images/generations", h.CreateImage)

	body := strings.NewReader(`{"model":"kling/kling-v3-image-generation","prompt":"a cat","n":15}`)
	req := httptest.NewRequest(http.MethodPost, "/images/generations", body)
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if called {
		t.Error("kling was called despite an out-of-range n")
	}
}
