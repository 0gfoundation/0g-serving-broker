package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	teeutil "github.com/0glabs/0g-serving-broker/common/tee"
	"github.com/0glabs/0g-serving-broker/imagetranslator/internal/kling"
)

// TestUpstreamTLSReport_ReportsVendorCert is the end-to-end check for the whole
// point of this middleware (mirrors videotranslator's identically-named test):
// a real TLS handshake to the vendor happens inside the sidecar, and its leaf
// certificate must come back out on the response header the broker binds into
// a centralized routing proof. Without it Kling cannot be `centralized` at all.
//
// Kling's client.go previously had no teeutil.CertCaptureFromContext(...).Observe
// call, and imagetranslator had no UpstreamTLSReport middleware at all — a
// Kling deployment with targetTLSProxy: true would have silently produced no
// routing proof for every response.
//
// Exercises client.CreateTask directly through a minimal handler rather than the
// full CreateImage (create + poll-to-terminal + image fetch) — CreateTask makes
// the same do() call every step of that flow shares, and CertCapture keeps only
// the FIRST TLS observation, so this is the same mechanism under test.
func TestUpstreamTLSReport_ReportsVendorCert(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"output":{"task_id":"t1","task_status":"PENDING"}}`))
	}))
	defer upstream.Close()

	client := kling.NewClient(upstream.URL, upstream.Client(), 30_000_000_000)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(UpstreamTLSReport())
	engine.GET("/probe", func(c *gin.Context) {
		// Request content is irrelevant to this test — the fixture upstream
		// ignores the body and always returns the same canned response. Only
		// the round trip's TLS handshake matters here.
		if _, err := client.CreateTask(c.Request.Context(), "", kling.CreateRequest{}); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		c.Status(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/probe", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	want := teeutil.CertFingerprintFromX509(upstream.Certificate())
	if got := rec.Header().Get(teeutil.HeaderUpstreamCertFingerprint); got != want {
		t.Errorf("reported fingerprint %q, want the vendor's leaf cert %q", got, want)
	}
}

// TestUpstreamTLSReport_NoHeaderWithoutTLS: a plaintext upstream produces no
// evidence, so no header — the broker then refuses to sign rather than emitting
// a routing proof with a fabricated or empty fingerprint.
func TestUpstreamTLSReport_NoHeaderWithoutTLS(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"output":{"task_id":"t1","task_status":"PENDING"}}`))
	}))
	defer upstream.Close()

	client := kling.NewClient(upstream.URL, upstream.Client(), 30_000_000_000)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(UpstreamTLSReport())
	engine.GET("/probe", func(c *gin.Context) {
		// Request content is irrelevant to this test — the fixture upstream
		// ignores the body and always returns the same canned response. Only
		// the round trip's TLS handshake matters here.
		if _, err := client.CreateTask(c.Request.Context(), "", kling.CreateRequest{}); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		c.Status(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/probe", nil))

	if got := rec.Header().Get(teeutil.HeaderUpstreamCertFingerprint); got != "" {
		t.Errorf("plaintext upstream reported %q", got)
	}
}
