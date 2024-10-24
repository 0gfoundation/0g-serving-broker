package imageGeneration

type ProviderImageGeneration struct{}

func (c *ProviderImageGeneration) GetInputCount(reqBody []byte) (int64, error) {
	return 1, nil
}

func (c *ProviderImageGeneration) GetOutputCount(outputs [][]byte) (int64, error) {
	return 0, nil
}

func (c *ProviderImageGeneration) StreamCompleted(output []byte) (bool, error) {
	return false, nil
}

func (c *ProviderImageGeneration) GetRespContent(resp []byte, encodingType string) ([]byte, error) {
	return resp, nil
}
