package lora

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/log"
)

// SLLMClient wraps the ServerlessLLM HTTP API for deploying and managing LoRA adapters.
type SLLMClient struct {
	baseURL    string
	httpClient *http.Client
	logger     log.Logger
}

type SLLMDeployRequest struct {
	Model       string            `json:"model"`
	Backend     string            `json:"backend,omitempty"`
	LoraAdapters map[string]string `json:"lora_adapters,omitempty"`
}

type SLLMModelInfo struct {
	Model  string `json:"model"`
	Status string `json:"status"`
}

// NewSLLMClient creates an HTTP client for the ServerlessLLM adapter management API.
func NewSLLMClient(baseURL string, logger log.Logger) *SLLMClient {
	return &SLLMClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 2 * time.Minute,
		},
		logger: logger,
	}
}

// DeployAdapter registers a LoRA adapter with ServerlessLLM.
func (c *SLLMClient) DeployAdapter(ctx context.Context, baseModel, adapterName, adapterPath string) error {
	reqBody := SLLMDeployRequest{
		Model:   baseModel,
		Backend: "vllm",
		LoraAdapters: map[string]string{
			adapterName: adapterPath,
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return errors.Wrap(err, "marshal deploy request")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/models/deploy", bytes.NewBuffer(body))
	if err != nil {
		return errors.Wrap(err, "create deploy request")
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return errors.Wrap(err, "execute deploy request")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("deploy adapter failed (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	c.logger.Infof("deployed LoRA adapter %s (base: %s, path: %s)", adapterName, baseModel, adapterPath)
	return nil
}

// DeleteAdapter removes a LoRA adapter from ServerlessLLM.
func (c *SLLMClient) DeleteAdapter(ctx context.Context, adapterName string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/v1/models/"+url.PathEscape(adapterName), nil)
	if err != nil {
		return errors.Wrap(err, "create delete request")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return errors.Wrap(err, "execute delete request")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete adapter failed (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	c.logger.Infof("deleted LoRA adapter %s from ServerlessLLM", adapterName)
	return nil
}

// ListModels returns currently loaded models from ServerlessLLM.
func (c *SLLMClient) ListModels(ctx context.Context) ([]SLLMModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/models", nil)
	if err != nil {
		return nil, errors.Wrap(err, "create list models request")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "execute list models request")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list models failed (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Data []SLLMModelInfo `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, errors.Wrap(err, "decode list models response")
	}

	return result.Data, nil
}

// HealthCheck returns true if ServerlessLLM is responding.
func (c *SLLMClient) HealthCheck(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return false
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
}
