package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	cfg "github.com/0glabs/0g-serving-broker/inference/config"
)

func withMonitor(on bool) *instance {
	c := &cfg.Config{}
	c.Monitor.Enable = on
	return &instance{config: c}
}

// Scrapes always reach the primary, so /metrics is there whenever any instance
// exports metrics — including when the primary itself does not.
func TestMountMetricsOnThePrimaryWhenAnyInstanceMonitors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name      string
		instances []*instance
		want      int
	}{
		{"single instance, on", []*instance{withMonitor(true)}, http.StatusOK},
		{"single instance, off", []*instance{withMonitor(false)}, http.StatusNotFound},
		{"primary off, extra on", []*instance{withMonitor(false), withMonitor(true)}, http.StatusOK},
		{"all off", []*instance{withMonitor(false), withMonitor(false)}, http.StatusNotFound},
	} {
		engine := gin.New()
		mountMetrics(engine, tc.instances)
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if w.Code != tc.want {
			t.Errorf("%s: GET /metrics = %d, want %d", tc.name, w.Code, tc.want)
		}
	}
}
