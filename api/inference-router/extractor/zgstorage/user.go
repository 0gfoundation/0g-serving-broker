package zgstorage

import (
	"github.com/0glabs/0g-serving-broker/inference-router/contract"
)

type UserZgStorage struct {
	SvcInfo contract.Service
}

func (c *UserZgStorage) GetSvcInfo() contract.Service {
	return c.SvcInfo
}

func (c *UserZgStorage) GetInputCount(reqBody []byte) (int64, error) {
	return int64(len(reqBody)), nil
}

func (c *UserZgStorage) GetOutputCount(outputs [][]byte) (int64, error) {
	ret := 0
	for _, output := range outputs {
		ret += len(output)
	}

	return int64(ret), nil
}

func (c *UserZgStorage) StreamCompleted(output []byte) (bool, error) {
	return false, nil
}

func (c *UserZgStorage) GetRespContent(resp []byte, encodingType string) ([]byte, error) {
	return resp, nil
}
