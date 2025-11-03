package tee

import (
	"context"
	"encoding/base64"
	"encoding/json"
)

// MockTappdClient is a mock implementation of TappdClient for testing.
type MockTappdClient struct{}

func (c *MockTappdClient) TdxQuote(ctx context.Context, reportData string, nvQuote bool) (string, error) {
	encodedReportData := base64.StdEncoding.EncodeToString([]byte(reportData))
	mockResp := map[string]interface{}{
		"report_data": encodedReportData,
		"intel_quote":     "mock_intel_quote",
		"nvidia_payload":  "mock_nvidia_payload",
		"event_log":       []interface{}{},
		"info": map[string]interface{}{
			"app_id":   "mock_app_id",
			"tcb_info": `{"mock": "tcb_info"}`,
		},
	}

	jsonData, err := json.Marshal(mockResp)
	if err != nil {
		return "", err
	}

	return string(jsonData), nil
}

func (c *MockTappdClient) DeriveKey(ctx context.Context, path string) (string, error) {
	return "4c0883a69102937d6231471b5dbb6204fe512961708279b7e1a8d7d7a3c2b9e3", nil
}
