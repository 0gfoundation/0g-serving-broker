package zgstorage

type ProviderZgStorage struct{}

func (c *ProviderZgStorage) GetInputCount(reqBody []byte) (int64, error) {
	return int64(len(reqBody)), nil
}

func (c *ProviderZgStorage) GetOutputCount(outputs [][]byte) (int64, error) {
	ret := 0
	for _, output := range outputs {
		ret += len(output)
	}

	return int64(ret), nil
}

func (c *ProviderZgStorage) StreamCompleted(output []byte) (bool, error) {
	return false, nil
}

func (c *ProviderZgStorage) GetRespContent(resp []byte, encodingType string) ([]byte, error) {
	return resp, nil
}
