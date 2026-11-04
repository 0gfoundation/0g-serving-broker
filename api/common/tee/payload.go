package tee

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/util"
)

type NvidiaPayload struct {
	Nonce        string        `json:"nonce"`
	EvidenceList []GPUEvidence `json:"evidence_list"`
	Arch         string        `json:"arch"`
}

type GPUEvidence struct {
	Certificate string `json:"certificate"`
	Evidence    string `json:"evidence"`
	Arch        string `json:"arch"`
}

func GpuPayload(publicKey string, logger log.Logger) (*NvidiaPayload, error) {
	args := []string{"common/tee/payload.py", "--public_key", publicKey}

	output, err := util.RunCommand("python3", args, logger)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) > 0 {
		return parseGpuPayload(lines[len(lines)-1])
	} else {
		return nil, fmt.Errorf("No output from payload script")
	}
}

// parseGpuPayload decodes the payload script's output and sets the payload's arch from its
// evidence.
//
// payload.py writes a fixed top-level "arch": "HOPPER", while each evidence item carries the
// architecture nvtrust actually read off the GPU. NVIDIA's attestation service (NRAS) checks
// the certificate chain against the top-level arch, so on a Blackwell GPU (B300) every
// evidence item failed with INVALID_CERTIFICATE_CHAIN and the overall result was false —
// measured on a B300 CVM on dstack 0.6.1 (2026-11-04); the same evidence with the top-level
// arch set to BLACKWELL passes (measres success, hwmodel GB110). When every item names one
// architecture, that is the payload's; otherwise the script's value stands.
func parseGpuPayload(line string) (*NvidiaPayload, error) {
	var result NvidiaPayload
	if err := json.Unmarshal([]byte(line), &result); err != nil {
		return nil, err
	}
	arch := ""
	for i, e := range result.EvidenceList {
		if i == 0 {
			arch = e.Arch
		} else if e.Arch != arch {
			arch = ""
			break
		}
	}
	if arch != "" {
		result.Arch = arch
	}
	return &result, nil
}
