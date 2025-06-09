package tee

import (
	"context"
)

// MockTappdClient is a mock implementation of TappdClient for testing.
type MockTappdClient struct{}

func (c *MockTappdClient) TdxQuote(ctx context.Context, jsonData []byte) (*TdxQuoteResponse, error) {
	return &TdxQuoteResponse{
		Quote:    "mock",
		EventLog: "",
	}, nil
}

func (c *MockTappdClient) DeriveKey(ctx context.Context, path string) (string, error) {
	return "4c0883a69102937d6231471b5dbb6204fe512961708279b7e1a8d7d7a3c2b9e3", nil
}
