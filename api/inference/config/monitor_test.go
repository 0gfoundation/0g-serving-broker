package config

import "testing"

// The monitor keys must be known to the decoder: an unknown key is only
// logged ([CONFIG-IGNORED]) and the setting silently does nothing.
func TestMonitorKeysDecode(t *testing.T) {
	var c Config
	ignored, err := decodeConfig([]byte(`
monitor:
  enable: true
  eventAddress: ":3081"
  metricsReader: "0xf3a2dc0AE1Be2edB40eDA26bc5b5dC581142cAD2"
  exporters:
    sglang: http://sglang:30000/metrics
    event: http://0g-serving-provider-event:3081/metrics
  gpuMetrics: true
`), &c)
	if err != nil {
		t.Fatal(err)
	}
	if len(ignored) != 0 {
		t.Fatalf("ignored keys: %v", ignored)
	}
	m := c.Monitor
	if !m.Enable || m.EventAddress != ":3081" || m.MetricsReader != "0xf3a2dc0AE1Be2edB40eDA26bc5b5dC581142cAD2" ||
		m.Exporters["sglang"] != "http://sglang:30000/metrics" || len(m.Exporters) != 2 || !m.GPUMetrics {
		t.Fatalf("decoded %+v", m)
	}
	if err := validateMonitor(&c); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestValidateMonitor(t *testing.T) {
	mk := func(enable bool, reader string, ex map[string]string, gpu bool) *Config {
		c := &Config{}
		c.Monitor.Enable, c.Monitor.MetricsReader, c.Monitor.Exporters, c.Monitor.GPUMetrics = enable, reader, ex, gpu
		return c
	}
	ok := "0x67A594827d0FDAD224313E4FEb78d39a2685283b"
	cases := []struct {
		name string
		cfg  *Config
		bad  bool
	}{
		{"empty", mk(true, "", nil, false), false},
		{"full", mk(true, ok, map[string]string{"engine": "http://sglang:8000/metrics", "event": "http://0g-serving-provider-event:3081/metrics"}, true), false},
		{"bad reader", mk(true, "0x123", nil, false), true},
		{"public host", mk(true, ok, map[string]string{"engine": "http://evil.example.com/metrics"}, false), true},
		{"ip literal", mk(true, ok, map[string]string{"engine": "http://169.254.169.254/metrics"}, false), true},
		{"https", mk(true, ok, map[string]string{"engine": "https://sglang:8000/metrics"}, false), true},
		{"credentials", mk(true, ok, map[string]string{"engine": "http://u:p@sglang:8000/metrics"}, false), true},
		{"bad name", mk(true, ok, map[string]string{"../x": "http://sglang:8000/metrics"}, false), true},
		{"engine API path", mk(true, ok, map[string]string{"engine": "http://sglang:8000/v1/models"}, false), true},
		{"metrics with query", mk(true, ok, map[string]string{"engine": "http://sglang:8000/metrics?x=1"}, false), true},
		{"broker itself", mk(true, ok, map[string]string{"self": "http://localhost:3080/v1/proxy/chat/completions"}, false), true},
		{"exporters without enable", mk(false, ok, map[string]string{"engine": "http://sglang:8000/metrics"}, false), true},
		{"gpu without enable", mk(false, ok, nil, true), true},
	}
	for _, c := range cases {
		if err := validateMonitor(c.cfg); (err != nil) != c.bad {
			t.Errorf("%s: err=%v, want bad=%v", c.name, err, c.bad)
		}
	}
}
