package monitor

import (
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// maxExporterBody caps one exporter scrape. sglang's /metrics with many
// histograms is a few MB; anything far past that is a bug, not data.
const maxExporterBody = 32 << 20

// ExporterHandler serves GET /metrics/:name by fetching the in-CVM exporter the
// config names (monitor.exporters) and returning its body verbatim.
//
// It replaces a reverse proxy in front of the exporters: the engine's port also
// serves its inference and admin API, so the engine itself must never be
// reachable from outside — only this one path of it, through the broker, behind
// RequireMetricsReader. The URL comes only from config (validated to a bare
// compose service name), redirects are not followed, and only GET is sent.
func ExporterHandler(exporters map[string]string, warnf func(string, ...interface{})) gin.HandlerFunc {
	client := &http.Client{
		Timeout: 10 * time.Second,
		// No proxy: an HTTP(S)_PROXY in the environment must not carry this
		// in-CVM hop off the machine.
		Transport:     &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return func(c *gin.Context) {
		target, ok := exporters[c.Param("name")]
		if !ok {
			c.Set("ignoreError", true)
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "unknown exporter"})
			return
		}
		req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, target, nil)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "exporter request"})
			return
		}
		resp, err := client.Do(req)
		if err != nil {
			c.Set("ignoreError", true)
			c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{"error": "exporter unreachable"})
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			c.Set("ignoreError", true)
			c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{"error": "exporter returned " + resp.Status})
			return
		}
		// Fixed type: never serve whatever the exporter claims (e.g. text/html)
		// from the broker's origin.
		c.Header("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		c.Status(http.StatusOK)
		if n, _ := io.Copy(c.Writer, io.LimitReader(resp.Body, maxExporterBody)); n == maxExporterBody {
			warnf("metrics exporter %s hit the %d-byte cap; the scrape is truncated", c.Param("name"), maxExporterBody)
		}
	}
}
