package ctrl

import (
	"context"
	"errors"
	"fmt"

	"github.com/0glabs/0g-serving-broker/fine-tuning/config"
	ethcommon "github.com/ethereum/go-ethereum/common"
)

func (c *Ctrl) GetModels(ctx context.Context) ([]config.CustomizedModel, error) {
	return c.config.Service.CustomizedModels, nil
}

func (c *Ctrl) GetModel(ctx context.Context, modelNameOrHash string) (*config.CustomizedModel, error) {
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
