package monitor

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestExporterHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var gotMethod string
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		switch r.URL.Path {
		case "/metrics":
			w.Header().Set("Content-Type", "text/plain; version=0.0.4")
			_, _ = w.Write([]byte("sglang_num_running_reqs 3\n"))
		case "/redirect":
			http.Redirect(w, r, "http://169.254.169.254/", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer engine.Close()

	r := gin.New()
	r.GET("/metrics/:name", ExporterHandler(map[string]string{
		"engine": engine.URL + "/metrics",
		"bounce": engine.URL + "/redirect",
		"gone":   engine.URL + "/nope",
	}))
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w
	}

	if w := get("/metrics/engine"); w.Code != 200 || w.Body.String() != "sglang_num_running_reqs 3\n" ||
		w.Header().Get("Content-Type") != "text/plain; version=0.0.4" || gotMethod != http.MethodGet {
		t.Fatalf("engine: %d %q %q method=%s", w.Code, w.Body.String(), w.Header().Get("Content-Type"), gotMethod)
	}
	if w := get("/metrics/unknown"); w.Code != 404 {
		t.Errorf("unknown exporter: %d", w.Code)
	}
	if w := get("/metrics/bounce"); w.Code != 502 { // redirect not followed
		t.Errorf("redirect: %d %s", w.Code, w.Body.String())
	}
	if w := get("/metrics/gone"); w.Code != 502 {
		t.Errorf("upstream 404: %d", w.Code)
	}
}
