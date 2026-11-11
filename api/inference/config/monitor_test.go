package config

import "testing"

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
		{"exporters without enable", mk(false, ok, map[string]string{"engine": "http://sglang:8000/metrics"}, false), true},
		{"gpu without enable", mk(false, ok, nil, true), true},
	}
	for _, c := range cases {
		if err := validateMonitor(c.cfg); (err != nil) != c.bad {
			t.Errorf("%s: err=%v, want bad=%v", c.name, err, c.bad)
		}
	}
}
