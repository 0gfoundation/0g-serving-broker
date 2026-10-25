package config

import (
	"strings"
	"testing"
)

// A single-model decentralized provider: the engine is a container in its own CVM.
const decentralizedSingleModel = `
service:
  servingUrl: "http://example.com"
  targetUrl: "%s"
  type: "chatbot"
  model: "qwen3-32b"
  verifiability: "TeeML"
  inputPrice: "100"
  outputPrice: "200"
`

func TestDecentralizedSingleModelTargetStaysInCVM(t *testing.T) {
	if _, err := loadYAML(t, strings.Replace(decentralizedSingleModel, "%s", "http://vllm:8000/v1", 1)); err != nil {
		t.Fatalf("compose service name: %v", err)
	}
	// 0g-router#728: an external API, or a private address that may be the host or a
	// LAN neighbour, would have its replies signed as this TEE's output.
	for _, target := range []string{"https://api.openai.com/v1", "http://api.example.com/v1", "http://10.0.0.5:8000/v1", "http://127.0.0.1:8000/v1"} {
		_, err := loadYAML(t, strings.Replace(decentralizedSingleModel, "%s", target, 1))
		if err == nil || !strings.Contains(err.Error(), "decentralized provider without targetSeparated") {
			t.Fatalf("%s: want an in-CVM refusal, got %v", target, err)
		}
	}
}

// TARGET_URL (measured in the compose) wins over the file, and the rule judges the
// value the broker will actually dial.
func TestDecentralizedSingleModelTargetJudgesTargetURLEnv(t *testing.T) {
	t.Setenv(targetURLEnvVar, "http://0gm-sglang:8000/v1")
	if _, err := loadYAML(t, strings.Replace(decentralizedSingleModel, "%s", "https://api.openai.com/v1", 1)); err != nil {
		t.Fatalf("in-CVM TARGET_URL over an external file value: %v", err)
	}
}

// targetSeparated declares a remote TEE that signs, so an off-box target is its point.
func TestDecentralizedSeparatedTargetMayLeaveCVM(t *testing.T) {
	body := strings.Replace(decentralizedSingleModel, "%s", "https://remote-tee.example.com/v1", 1) +
		"  targetSeparated: true\n  targetTeeAddress: \"0x1111111111111111111111111111111111111111\"\n"
	if _, err := loadYAML(t, body); err != nil {
		t.Fatalf("targetSeparated with a remote target: %v", err)
	}
}
