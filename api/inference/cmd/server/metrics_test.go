package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gin-gonic/gin"

	cfg "github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

const (
	testReaderKey = "4c0883a69102937d6231471b5dbb6204fe5129617082792ae468d01a3f362318"
	primaryAddr   = "0x158468A43ae7FEfDB53aDF0d6081d1959AaB1333"
	extraAddr     = "0x787A66B5dB48F23494aA7384f51EDfEF1911341D"
)

func testReader() string {
	k, _ := crypto.HexToECDSA(testReaderKey)
	return crypto.PubkeyToAddress(k.PublicKey).Hex()
}

func withMonitor(on bool, addr string) *instance {
	c := &cfg.Config{}
	c.Monitor.Enable = on
	c.Monitor.MetricsReader = testReader()
	return &instance{config: c, providerAddress: addr}
}

func bearer(t *testing.T, provider string) string {
	t.Helper()
	k, _ := crypto.HexToECDSA(testReaderKey)
	exp := time.Now().Add(2 * time.Minute).Unix()
	sig, err := crypto.Sign(accounts.TextHash([]byte(monitor.MetricsTokenMessage(common.HexToAddress(provider), exp))), k)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("Bearer %d.%s", exp, hexutil.Encode(sig))
}

func get(engine *gin.Engine, auth string) int {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	engine.ServeHTTP(w, r)
	return w.Code
}

// Scrapes always reach the primary, so /metrics is there whenever any instance
// exports metrics — including when the primary itself does not. Mounted, it
// answers only a token signed by the primary's reader for the primary's address.
func TestMountMetricsOnThePrimaryWhenAnyInstanceMonitors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name      string
		instances []*instance
		mounted   bool
	}{
		{"single instance, on", []*instance{withMonitor(true, primaryAddr)}, true},
		{"single instance, off", []*instance{withMonitor(false, primaryAddr)}, false},
		{"primary off, extra on", []*instance{withMonitor(false, primaryAddr), withMonitor(true, extraAddr)}, true},
		{"all off", []*instance{withMonitor(false, primaryAddr), withMonitor(false, extraAddr)}, false},
	} {
		engine := gin.New()
		mountMetrics(engine, tc.instances, t.Logf)
		if !tc.mounted {
			if c := get(engine, bearer(t, primaryAddr)); c != http.StatusNotFound {
				t.Errorf("%s: GET /metrics = %d, want 404", tc.name, c)
			}
			continue
		}
		if c := get(engine, ""); c != http.StatusUnauthorized {
			t.Errorf("%s: no token = %d, want 401", tc.name, c)
		}
		if c := get(engine, bearer(t, primaryAddr)); c != http.StatusOK {
			t.Errorf("%s: primary token = %d, want 200", tc.name, c)
		}
		// A token for any other provider — an extra instance in this process
		// included — does not open the process's metrics.
		if c := get(engine, bearer(t, extraAddr)); c != http.StatusUnauthorized {
			t.Errorf("%s: extra-address token = %d, want 401", tc.name, c)
		}
	}
}

func TestExtraInstanceRefusesProcessWideMetricsKeys(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*cfg.Config)
		bad  bool
	}{
		{"plain monitoring", func(c *cfg.Config) { c.Monitor.Enable = true }, false},
		{"metricsReader", func(c *cfg.Config) { c.Monitor.MetricsReader = testReader() }, true},
		{"exporters", func(c *cfg.Config) { c.Monitor.Exporters = map[string]string{"x": "http://x:1/metrics"} }, true},
		{"gpuMetrics", func(c *cfg.Config) { c.Monitor.GPUMetrics = true }, true},
	} {
		c := &cfg.Config{}
		tc.set(c)
		if err := checkExtraMonitor("laya", c); (err != nil) != tc.bad {
			t.Errorf("%s: err=%v, want bad=%v", tc.name, err, tc.bad)
		}
	}
}

func TestMountMetricsWithoutReaderRefusesEverything(t *testing.T) {
	gin.SetMode(gin.TestMode)
	p := withMonitor(true, primaryAddr)
	p.config.Monitor.MetricsReader = ""
	engine := gin.New()
	mountMetrics(engine, []*instance{p}, t.Logf)
	if c := get(engine, bearer(t, primaryAddr)); c != http.StatusUnauthorized {
		t.Errorf("no reader configured: GET /metrics = %d, want 401", c)
	}
}
