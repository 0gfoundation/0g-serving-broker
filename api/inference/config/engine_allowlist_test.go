package config

import (
	"testing"

	"gopkg.in/yaml.v2"
)

// The engine allowlist is read with the same strict unmarshalling the rest of the config
// is, so a mistyped yaml tag is not a field that quietly stays at its zero value — it is
// a boot failure of all three binaries that share this struct. This test is what catches
// such a typo here rather than on a deployment.
//
// The document below is the one in doc/controller-design.md §3.1. Keeping them the same
// is the point: a config example that does not parse is worse than none, because an
// operator copies it.
func TestEngineAllowlistParsesStrictly(t *testing.T) {
	const doc = `
controller:
  enable: true
  port: 3090
  imageRepo: "ghcr.io/0gfoundation/0g-serving-broker"
  recordUpstreamSet: false
  engineNetwork: "zg"
  engineVolumes:
    - "hfcache:/root/.cache/huggingface"
  engineGPUIgnore:
    - "dcgm-exporter"
  engineEnv:
    HF_HOME: "/root/.cache/huggingface"
  engines:
    - imageRepo: "lmsysorg/sglang"
      modelFlag: "--model-path"
      revisionFlag: "--revision"
      portFlag: "--port"
      hostFlag: "--host"
      ipcHost: true
      shmSize: "32gb"
    - imageRepo: "vllm/vllm-openai"
      modelFlag: "--model"
      revisionFlag: "--revision"
      portFlag: "--port"
      hostFlag: "--host"
      ipcHost: true
      shmSize: "32gb"
`

	var cfg Config
	if err := yaml.UnmarshalStrict([]byte(doc), &cfg); err != nil {
		t.Fatalf("the documented engine configuration does not parse: %v", err)
	}

	c := cfg.Controller
	if c.EngineNetwork != "zg" {
		t.Errorf("engineNetwork = %q, want zg", c.EngineNetwork)
	}
	if len(c.EngineVolumes) != 1 || c.EngineVolumes[0] != "hfcache:/root/.cache/huggingface" {
		t.Errorf("engineVolumes = %v", c.EngineVolumes)
	}
	if c.EngineEnv["HF_HOME"] != "/root/.cache/huggingface" {
		t.Errorf("engineEnv = %v", c.EngineEnv)
	}
	// The one key a deployment cannot omit in practice: without it dcgm-exporter, which
	// every deployment here runs with NVIDIA_VISIBLE_DEVICES=all, occupies the whole
	// machine and no engine can ever be placed.
	if len(c.EngineGPUIgnore) != 1 || c.EngineGPUIgnore[0] != "dcgm-exporter" {
		t.Errorf("engineGPUIgnore = %v", c.EngineGPUIgnore)
	}
	if len(c.Engines) != 2 {
		t.Fatalf("engines = %+v, want 2", c.Engines)
	}
	// Every field asserted, because a wrong tag on any one of them is a flag the
	// controller would silently not set — and three of the four are flags a request is
	// then no longer refused for passing.
	sglang := c.Engines[0]
	if sglang.ImageRepo != "lmsysorg/sglang" || sglang.ModelFlag != "--model-path" ||
		sglang.RevisionFlag != "--revision" || sglang.PortFlag != "--port" ||
		sglang.HostFlag != "--host" || !sglang.IPCHost || sglang.ShmSize != "32gb" {
		t.Errorf("sglang entry = %+v", sglang)
	}
	// The reason this is a table and not a template: the two engines spell the model
	// flag differently.
	if c.Engines[1].ModelFlag != "--model" {
		t.Errorf("vllm modelFlag = %q, want --model", c.Engines[1].ModelFlag)
	}
}

// An unknown key under an engine entry is refused, which is what makes a typo in the
// allowlist a boot failure rather than a permitted image with no rule.
func TestEngineAllowlistRefusesAnUnknownKey(t *testing.T) {
	const doc = `
controller:
  engines:
    - imageRepo: "lmsysorg/sglang"
      modelPath: "--model-path"
`
	var cfg Config
	if err := yaml.UnmarshalStrict([]byte(doc), &cfg); err == nil {
		t.Fatalf("modelPath parsed as %+v, want a refusal: the field is modelFlag", cfg.Controller.Engines)
	}
}
