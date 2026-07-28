package tee

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/Dstack-TEE/dstack/sdk/go/dstack"
	"github.com/ethereum/go-ethereum/crypto"
)

type PhalaTappdClient struct{}

// QuoteResponseWithNvidia wraps the original GetQuoteResponse with nvidia_payload field
type QuoteResponseWithNvidia struct {
	Quote         string      `json:"quote"`
	EventLog      string      `json:"event_log"`
	ReportData    []byte      `json:"report_data"`
	VmConfig      string      `json:"vm_config"`
	TcbInfo       string      `json:"tcb_info,omitempty"`
	NvidiaPayload interface{} `json:"nvidia_payload,omitempty"`
}

func (c *PhalaTappdClient) TdxQuote(ctx context.Context, reportData []byte, nvQuote bool) (string, error) {
	// TODO: Custom implementation to get quote with vm_config field
	// Remove if https://pkg.go.dev/github.com/Dstack-TEE/dstack/sdk/go#DstackClient.GetQuote updated
	if len(reportData) > 64 {
		return "", errors.New("report data is too large, it should be at most 64 bytes")
	}

	payload := map[string]interface{}{
		"report_data": hex.EncodeToString(reportData),
	}

	// Send RPC request directly
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return "", errors.Wrap(err, "failed to marshal payload")
	}

	endpoint := "/var/run/dstack.sock"
	baseURL := "http://localhost"
	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", endpoint)
			},
		},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/GetQuote", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", errors.Wrap(err, "failed to create request")
	}

	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", errors.Wrap(err, "failed to send request")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", errors.Wrap(err, "failed to read response body")
	}

	var response struct {
		Quote      string `json:"quote"`
		EventLog   string `json:"event_log"`
		ReportData string `json:"report_data"`
		VmConfig   string `json:"vm_config"`
		TcbInfo    string `json:"tcb_info"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", errors.Wrap(err, "failed to unmarshal response")
	}

	// Make /Info request to get tcb_info
	infoReq, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/Info", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", errors.Wrap(err, "failed to create info request")
	}

	infoReq.Header.Set("Content-Type", "application/json")
	infoResp, err := httpClient.Do(infoReq)
	if err != nil {
		return "", errors.Wrap(err, "failed to send info request")
	}
	defer infoResp.Body.Close()

	if infoResp.StatusCode == http.StatusOK {
		infoBody, err := io.ReadAll(infoResp.Body)
		if err == nil {
			var infoResponse struct {
				TcbInfo string `json:"tcb_info"`
			}
			if err := json.Unmarshal(infoBody, &infoResponse); err == nil {
				response.TcbInfo = infoResponse.TcbInfo
			}
		}
	}
	reportDataRespBytes, err := hex.DecodeString(response.ReportData)
	if err != nil {
		return "", err
	}
	quoteResp := QuoteResponseWithNvidia{
		Quote:      response.Quote,
		EventLog:   response.EventLog,
		VmConfig:   response.VmConfig,
		TcbInfo:    response.TcbInfo,
		ReportData: reportDataRespBytes,
	}

	if nvQuote {
		if err := util.CheckPythonEnv(util.NvTrustPackages, nil); err != nil {
			panic(err)
		}

		nvidiaPayload, err := GpuPayload(hex.EncodeToString(crypto.Keccak256(reportData)), nil)
		if err != nil {
			return "", errors.Wrap(err, "failed to get NVIDIA payload")
		}
		quoteResp.NvidiaPayload = nvidiaPayload
	}

	// Marshal to JSON string
	resultJSON, err := json.Marshal(quoteResp)
	if err != nil {
		return "", errors.Wrap(err, "failed to marshal quote response to JSON")
	}

	return string(resultJSON), nil
}

func (c *PhalaTappdClient) DeriveKey(ctx context.Context, path string) (string, error) {
	client := dstack.NewDstackClient()

	res, err := client.GetKey(ctx, path, "")
	if err != nil {
		return "", errors.Wrap(err, "failed to derive key from dstack client")
	}

	return res.Key, nil
}
