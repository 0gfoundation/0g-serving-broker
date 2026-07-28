//go:generate protoc --go_out=../../../ --go-grpc_out=../../../ --go_opt=module=github.com/0glabs/0g-serving-broker --go-grpc_opt=module=github.com/0glabs/0g-serving-broker proto/tapp_service.proto

package alicloud

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/0glabs/0g-serving-broker/common/errors"
	pb "github.com/0glabs/0g-serving-broker/common/tee/alicloud/proto"
)

type AliCloudClient struct{}

func (c *AliCloudClient) TdxQuote(ctx context.Context, reportData []byte, nvQuote bool) (string, error) {
	// Get TAPP service URL from environment variable
	tappServiceURL := os.Getenv("TAPP_SERVICE_URL")
	if tappServiceURL == "" {
		return "", errors.New("TAPP_SERVICE_URL environment variable is required for AliCloud TEE mode")
	}

	// Parse the URL to extract host and port
	u, err := url.Parse(tappServiceURL)
	if err != nil {
		return "", errors.Wrap(err, "failed to parse TAPP_SERVICE_URL")
	}

	target := u.Host
	if target == "" {
		return "", errors.New("invalid TAPP_SERVICE_URL: missing host")
	}

	// report_data is the 64-byte §4.2 payload (enc_pub ‖ signer_addr ‖ version ‖
	// reserved); carry it raw. The proto ReportData field is bytes.
	if len(reportData) > 64 {
		return "", errors.New("report data is too large, it should be at most 64 bytes")
	}

	// Create gRPC connection with insecure credentials for TCP service
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return "", errors.Wrap(err, "failed to connect to TCP gRPC service")
	}
	defer conn.Close()

	// Create gRPC client
	client := pb.NewTappServiceClient(conn)

	// Prepare the request - report_data is carried as raw bytes (proto handles encoding)
	req := &pb.GetEvidenceRequest{
		ReportData: reportData, // This matches the proto bytes field
	}

	// Call the GetEvidence RPC
	resp, err := client.GetEvidence(ctx, req)
	if err != nil {
		return "", errors.Wrap(err, "failed to call GetEvidence")
	}

	if !resp.Success {
		return "", errors.New(fmt.Sprintf("failed to get evidence: %s", resp.Message))
	}

	appId := os.Getenv("TAPP_APP_ID")
	if appId == "" {
		return "", errors.New("TAPP_APP_ID environment variable is required for AliCloud TEE mode")
	}
	// Call the GetAppInfo RPC
	appInfoReq := &pb.GetAppInfoRequest{
		AppId: appId, // Use the appId from the environment variable
	}

	appInfoResp, err := client.GetAppInfo(ctx, appInfoReq)
	if err != nil {
		return "", errors.Wrap(err, "failed to call GetAppInfo")
	}

	if !appInfoResp.Success {
		return "", errors.New(fmt.Sprintf("failed to get app info: %s", appInfoResp.Message))
	}

	// Create a combined JSON response with ComposeContent and Evidence
	combinedResponse := struct {
		ComposeContent string `json:"compose_content"`
		Evidence       []byte `json:"evidence"`
	}{
		ComposeContent: appInfoResp.ComposeContent,
		Evidence:       resp.Evidence,
	}

	// Convert to JSON string
	jsonBytes, err := json.Marshal(combinedResponse)
	if err != nil {
		return "", errors.Wrap(err, "failed to marshal combined response to JSON")
	}

	return string(jsonBytes), nil
}

func (c *AliCloudClient) DeriveKey(ctx context.Context, path string) (string, error) {
	keyFilePath := "/data/tee_key"
	if data, err := os.ReadFile(keyFilePath); err == nil && len(data) > 0 {
		return string(data), nil
	}

	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", errors.Wrap(err, "failed to generate ECDSA private key")
	}
	dHex := hex.EncodeToString(privateKey.D.Bytes())

	_ = os.WriteFile(keyFilePath, []byte(dHex), 0600)

	return dHex, nil
}
