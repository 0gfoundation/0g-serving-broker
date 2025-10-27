package alicloud

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/0glabs/0g-serving-broker/common/errors"
	pb "github.com/0glabs/0g-serving-broker/common/tee/alicloud/proto"
)

type AliCloudClient struct{}

func (c *AliCloudClient) TdxQuote(ctx context.Context, reportData string, nvQuote bool) (string, error) {
	const (
		defaultPort      = "50051"
		appIDPlaceholder = "test-broker-app" // TODO: Replace with actual APP_ID
	)

	// Get host from environment variable
	host := os.Getenv("TAPP_SERVICE_HOST")
	if host == "" {
		return "", errors.New("TAPP_SERVICE_HOST environment variable is required for AliCloud TEE mode")
	}

	target := fmt.Sprintf("%s:%s", host, defaultPort)

	// According to get_evidence.sh, signer should be 32-byte data
	// reportData is typically an Ethereum address (0x + 40 hex chars = 20 bytes)
	// Remove 0x prefix if present
	cleanReportData := reportData
	if len(reportData) >= 2 && (reportData[:2] == "0x" || reportData[:2] == "0X") {
		cleanReportData = reportData[2:]
	}

	// Convert hex string to bytes
	reportDataBytes, err := hex.DecodeString(cleanReportData)
	if err != nil {
		return "", errors.Wrap(err, "failed to decode reportData hex string")
	}

	if len(reportDataBytes) > 32 {
		return "", errors.New("report data is too large, it should be at most 32 bytes")
	}

	// Pad to 32 bytes if necessary (Ethereum address is 20 bytes, needs padding to 32)
	paddedReportData := make([]byte, 32)
	copy(paddedReportData, reportDataBytes)

	// Convert to hex string (64 characters), similar to SIGNER_HEX in get_evidence.sh
	signerHex := hex.EncodeToString(paddedReportData)
	
	// Convert hex back to bytes, then to the format expected by proto (should be bytes)
	signerBytes, err := hex.DecodeString(signerHex)
	if err != nil {
		return "", errors.Wrap(err, "failed to decode signer hex")
	}

	// Create gRPC connection with insecure credentials for TCP service
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return "", errors.Wrap(err, "failed to connect to TCP gRPC service")
	}
	defer conn.Close()

	// Create gRPC client
	client := pb.NewTappServiceClient(conn)

	// Prepare the request - signer should be the bytes (proto will handle base64 encoding)
	req := &pb.GetEvidenceRequest{
		AppId:  appIDPlaceholder,
		Signer: signerBytes, // This matches the proto bytes field
	}

	// Call the GetEvidence RPC
	resp, err := client.GetEvidence(ctx, req)
	if err != nil {
		return "", errors.Wrap(err, "failed to call GetEvidence")
	}

	if !resp.Success {
		return "", errors.New(fmt.Sprintf("failed to get evidence: %s", resp.Message))
	}

	// Return the proto response directly - it contains all necessary information
	// The String() method provides a consistent, parseable format
	return resp.String(), nil
}

func (c *AliCloudClient) DeriveKey(ctx context.Context, path string) (string, error) {
	// Similar to GCP implementation, generate a random ECDSA private key
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", errors.Wrap(err, "failed to generate ECDSA private key")
	}

	dHex := hex.EncodeToString(privateKey.D.Bytes())
	return dHex, nil
}
