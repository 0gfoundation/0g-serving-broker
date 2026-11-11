package monitor

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestGPUGauges(t *testing.T) {
	g := newGPUGauges(prometheus.Labels{"server": "s"})
	samples, err := parseGPUSamples([]byte(`[{"gpu":0,"uuid":"GPU-a","name":"NVIDIA H200","util":87,"power_mw":512345,"fb_used":100,"fb_free":40,"temp":61}]`))
	if err != nil {
		t.Fatal(err)
	}
	g.apply(samples)
	want := `
# HELP DCGM_FI_DEV_POWER_USAGE Power draw (in W), read through NVML by the broker.
# TYPE DCGM_FI_DEV_POWER_USAGE gauge
DCGM_FI_DEV_POWER_USAGE{UUID="GPU-a",gpu="0",modelName="NVIDIA H200",server="s"} 512.345
`
	if err := testutil.CollectAndCompare(g.power, strings.NewReader(want)); err != nil {
		t.Error(err)
	}
	if v := testutil.ToFloat64(g.util.WithLabelValues("0", "GPU-a", "NVIDIA H200")); v != 87 {
		t.Errorf("util = %v", v)
	}

	// A failed read clears every series rather than freezing the last value.
	g.apply(nil)
	for _, v := range g.all() {
		if n := testutil.CollectAndCount(v); n != 0 {
			t.Errorf("%d series left after a failed read", n)
		}
	}
	if _, err := parseGPUSamples([]byte("Traceback ...")); err == nil {
		t.Error("garbage parsed")
	}
}
