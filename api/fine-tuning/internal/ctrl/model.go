package ctrl

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/0glabs/0g-serving-broker/fine-tuning/config"
	ethcommon "github.com/ethereum/go-ethereum/common"
)

type Model struct {
	Name           string `json:"name"`
	Hash           string `json:"hash"`
	Image          string `json:"image"`
	DataType       string `json:"dataType"`
	TrainingScript string `json:"trainingScript"`
	Description    string `json:"description"`
	Tokenizer      string `json:"tokenizer"`
}

func (c *Ctrl) GetModels(ctx context.Context) ([]Model, error) {
	models := make([]Model, 0, len(c.config.Service.CustomizedModels))
	for _, v := range c.config.Service.CustomizedModels {
		models = append(models, Model{
			Name:           v.Name,
			Hash:           v.Hash,
			Image:          v.Image,
			DataType:       v.DataType.String(),
			TrainingScript: v.TrainingScript,
			Description:    v.Description,
			Tokenizer:      v.Tokenizer,
		})
	}

	return models, nil
}

func (c *Ctrl) GetModel(ctx context.Context, modelNameOrHash string) (*Model, error) {
	v, err := c.getModel(ctx, modelNameOrHash)
	if err != nil {
		return nil, err
	}

	return &Model{
		Name:           v.Name,
		Hash:           v.Hash,
		Image:          v.Image,
		DataType:       v.DataType.String(),
		TrainingScript: v.TrainingScript,
		Description:    v.Description,
		Tokenizer:      v.Tokenizer,
	}, nil
}

func (c *Ctrl) GetModelDesc(ctx context.Context, modelNameOrHash string) (string, error) {
	m, err := c.getModel(ctx, modelNameOrHash)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(m.UsageFile)
	if err != nil || info.IsDir() {
		return "", errors.New(fmt.Sprintf("Model %v detail usage file not found", modelNameOrHash))
	}

	return m.UsageFile, nil
}

func (c *Ctrl) getModel(ctx context.Context, modelNameOrHash string) (*config.CustomizedModel, error) {
	hash := ethcommon.HexToHash(modelNameOrHash)
	if hash == (ethcommon.Hash{}) {
		for _, v := range c.customizedModels {
			if v.Name == modelNameOrHash {
				return &v, nil
			}
		}
	} else {
		if v, ok := c.customizedModels[hash]; ok {
			return &v, nil
		}
	}
	return nil, errors.New(fmt.Sprintf("Model %v not found", modelNameOrHash))
}
