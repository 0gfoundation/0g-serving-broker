package util

import (
	"bytes"
	"fmt"
	"os/exec"

	"github.com/0glabs/0g-serving-broker/common/log"
)

var TrainingPackages = []string{"transformers", "datasets"}
var NvTrustPackages = []string{"nv_attestation_sdk", "nv-local-gpu-verifier"}

func RunCommand(command string, args []string, logger log.Logger) (string, error) {
	cmd := exec.Command(command, args...)
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err := cmd.Run()

	stdout := stdoutBuf.String()
	stderr := stderrBuf.String()

	if err != nil {
		return "", fmt.Errorf("Error executing script: %v, stderr %s", err, stderr)
	} else {
		if logger != nil {
			logger.Info(command, args, " stdout: ", stdout)
			if len(stderr) > 0 {
				logger.Error(command, args, " stderr: ", stderr)
			}
		}

		return string(stdout), err
	}
}

func CheckPythonEnv(requiredPackages []string, logger log.Logger) error {
	_, err := RunCommand("python3", []string{"--version"}, logger)
	if err != nil {
		return err
	}

	_, err = RunCommand("pip", []string{"--version"}, logger)
	if err != nil {
		return err
	}

	for _, packageName := range requiredPackages {
		_, err := RunCommand("pip", []string{"show", packageName}, logger)
		if err != nil {
			output, err := RunCommand("pip", []string{"install", packageName}, logger)
			if err != nil {
				return fmt.Errorf("%s: %w", output, err)
			}
		}
	}

	return nil
}
