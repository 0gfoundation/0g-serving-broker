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
		var result NvidiaPayload

		lastLine := lines[len(lines)-1]
		err = json.Unmarshal([]byte(lastLine), &result)
		if err != nil {
			return nil, err
		}
		return &result, nil
	} else {
		return nil, fmt.Errorf("No output from payload script")
	}
}
