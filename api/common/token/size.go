package token

import (
	"bytes"
	"fmt"
	"os/exec"

	"github.com/0glabs/0g-serving-broker/common/log"
)

type DataSetType string

const (
	Text  DataSetType = "text"
	Image DataSetType = "image"
)

func runCommand(command string, args []string, logger log.Logger) (string, error) {
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
		logger.Infof("%v stdout: %s", command, stdout)
		logger.Errorf("%v stderr: %s", command, stderr)

		return string(stdout), err
	}
}

func CheckPythonEnv(logger log.Logger) error {
	_, err := runCommand("python3", []string{"--version"}, logger)
	if err != nil {
		return err
	}

	_, err = runCommand("pip", []string{"--version"}, logger)
	if err != nil {
		return err
	}

	requiredPackages := []string{"transformers"}
	for _, packageName := range requiredPackages {
		_, err := runCommand("pip", []string{"show", packageName}, logger)
		if err != nil {
			output, err := runCommand("pip", []string{"install", packageName}, logger)
			if err != nil {
				return fmt.Errorf("%s: %w", output, err)
			}
		}
	}

	return nil
}

func CountTokens(dataSetType DataSetType, datasetPath, pretrainedModelPath string, logger log.Logger) (int64, error) {
	if dataSetType == Image {
		return 0, fmt.Errorf("image not supported yet.")
	}

	output, err := runCommand("python3", []string{"common/token/token_counter.py", datasetPath, pretrainedModelPath}, logger)
	if err != nil {
		return 0, err
	}

	var tokenCount int64
	_, err = fmt.Sscanf(string(output), "%d", &tokenCount)
	if err != nil {
		return 0, err
	}

	return tokenCount, nil
}
