package token

import (
	"fmt"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/util"
)

type DataSetType string

const (
	Text  DataSetType = "text"
	Image DataSetType = "image"
)

func CountTokens(dataSetType DataSetType, datasetPath, pretrainedModelPath, trainingConfig string, logger log.Logger) (int64, int64, error) {
	output, err := util.RunCommand("python3", []string{"token-counter/token_counter.py", datasetPath, string(dataSetType), pretrainedModelPath, trainingConfig}, logger)
	if err != nil {
		return 0, 0, err
	}

	var tokenCount, numTrainEpochs int64
	_, err = fmt.Sscanf(string(output), "%d %d", &tokenCount, &numTrainEpochs)
	if err != nil {
		return 0, 0, err
	}

	return tokenCount, numTrainEpochs, nil
}
