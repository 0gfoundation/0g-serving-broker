package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/prometheus/client_golang/prometheus"
)

// gpuSampleScript reads every GPU through NVML. pynvml (nvidia-ml-py) is
// already in the broker image for the nvGPU attestation SDK, so this adds no
// dependency and replaces running dcgm-exporter in the CVM.
const gpuSampleScript = `
import json, pynvml as n
s = lambda x: x.decode() if isinstance(x, bytes) else x
n.nvmlInit()
out = []
for i in range(n.nvmlDeviceGetCount()):
    h = n.nvmlDeviceGetHandleByIndex(i)
    m = n.nvmlDeviceGetMemoryInfo(h)
    out.append(dict(gpu=i, uuid=s(n.nvmlDeviceGetUUID(h)), name=s(n.nvmlDeviceGetName(h)),
        util=n.nvmlDeviceGetUtilizationRates(h).gpu, power_mw=n.nvmlDeviceGetPowerUsage(h),
        fb_used=m.used // 1048576, fb_free=m.free // 1048576,
        temp=n.nvmlDeviceGetTemperature(h, n.NVML_TEMPERATURE_GPU)))
print(json.dumps(out))
`

type gpuSample struct {
	GPU     int     `json:"gpu"`
	UUID    string  `json:"uuid"`
	Name    string  `json:"name"`
	Util    float64 `json:"util"`
	PowerMW float64 `json:"power_mw"`
	FBUsed  float64 `json:"fb_used"`
	FBFree  float64 `json:"fb_free"`
	Temp    float64 `json:"temp"`
}

// gpuGauges uses dcgm-exporter's metric names and units so the existing
// GPU dashboards (serving-load, sglang-engine) read these unchanged.
type gpuGauges struct {
	util, power, fbUsed, fbFree, temp *prometheus.GaugeVec
}

func newGPUGauges(constLabels prometheus.Labels) *gpuGauges {
	g := func(name, help string) *prometheus.GaugeVec {
		return prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: name, Help: help, ConstLabels: constLabels},
			[]string{"gpu", "UUID", "modelName"})
	}
	return &gpuGauges{
		util:   g("DCGM_FI_DEV_GPU_UTIL", "GPU utilization (in %), read through NVML by the broker."),
		power:  g("DCGM_FI_DEV_POWER_USAGE", "Power draw (in W), read through NVML by the broker."),
		fbUsed: g("DCGM_FI_DEV_FB_USED", "Framebuffer memory used (in MiB), read through NVML by the broker."),
		fbFree: g("DCGM_FI_DEV_FB_FREE", "Framebuffer memory free (in MiB), read through NVML by the broker."),
		temp:   g("DCGM_FI_DEV_GPU_TEMP", "GPU temperature (in C), read through NVML by the broker."),
	}
}

func (g *gpuGauges) all() []*prometheus.GaugeVec {
	return []*prometheus.GaugeVec{g.util, g.power, g.fbUsed, g.fbFree, g.temp}
}

// apply replaces every series with the sample. A failed read (samples == nil)
// clears them, so a dashboard shows a gap instead of the last reading forever.
func (g *gpuGauges) apply(samples []gpuSample) {
	for _, v := range g.all() {
		v.Reset()
	}
	for _, s := range samples {
		l := prometheus.Labels{"gpu": fmt.Sprint(s.GPU), "UUID": s.UUID, "modelName": s.Name}
		g.util.With(l).Set(s.Util)
		g.power.With(l).Set(s.PowerMW / 1000)
		g.fbUsed.With(l).Set(s.FBUsed)
		g.fbFree.With(l).Set(s.FBFree)
		g.temp.With(l).Set(s.Temp)
	}
}

func parseGPUSamples(out []byte) ([]gpuSample, error) {
	var s []gpuSample
	if err := json.Unmarshal(out, &s); err != nil {
		return nil, fmt.Errorf("parse nvml sample: %w", err)
	}
	return s, nil
}

// StartGPUMetrics registers the GPU gauges and refreshes them every interval
// until ctx ends. Call after PrometheusInit (it reuses the const labels).
//
// ponytail: one python3 process per tick (~0.2s CPU every 15s); keep a
// long-lived sampler process if that ever shows up in a profile.
func StartGPUMetrics(ctx context.Context, interval time.Duration, logger log.Logger) {
	g := newGPUGauges(promConstLabels)
	for _, v := range g.all() {
		prometheus.MustRegister(v)
	}
	tick := func() {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		// -I: isolated mode, so a json.py / pynvml.py in the working directory
		// can never be imported in place of the real module.
		out, err := exec.CommandContext(cctx, "python3", "-I", "-c", gpuSampleScript).Output()
		var samples []gpuSample
		if err == nil {
			samples, err = parseGPUSamples(out)
		}
		if err != nil {
			logger.Warnf("GPU metrics: NVML read failed: %v", err)
		}
		g.apply(samples)
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for tick(); ; {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				tick()
			}
		}
	}()
}
