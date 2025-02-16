package zgstorage

type ProviderZgStorage struct{}

func (c *ProviderZgStorage) GetInputCount(reqBody []byte) (int64, error) {
	return int64(len(reqBody)), nil
}
