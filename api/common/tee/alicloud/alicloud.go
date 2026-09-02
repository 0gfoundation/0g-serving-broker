//go:generate protoc --go_out=../../../ --go-grpc_out=../../../ --go_opt=module=github.com/0glabs/0g-serving-broker --go-grpc_opt=module=github.com/0glabs/0g-serving-broker proto/tapp_service.proto

package alicloud

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/0glabs/0g-serving-broker/common/errors"
	pb "github.com/0glabs/0g-serving-broker/common/tee/alicloud/proto"
)

// AliCloud TAPP client. RETAINED but NOT WIRED IN: no ClientType selects it and no
// NETWORK value reaches it, so nothing in the broker can construct or call it
// today. It is kept so re-integrating AliCloud is a matter of adding the wiring
// back rather than rewriting the gRPC component, and the generated bindings under
// proto/ are the bulk of that component.
//
// DeriveKey WAS DELETED, and must not be restored in its old form. It ignored its
// path argument and returned a single cached secret from /data/tee_key for every
// path, which made the secp256k1 provider signer and the X25519 HPKE recipient key
// the SAME secret: disclosing either disclosed the other, and with it every prompt
// ever sealed to that enclave. enckey.go states their independence as a MUST
// (0g-pc SPEC §4.1). Because AliCloudClient no longer has DeriveKey it no longer
// satisfies tee.TappdClient, which is deliberate — the type cannot be wired back in
// by accident, only by someone writing a derivation first.
//
// What a correct derivation would use is already in this service and was never
// called: GetAppKey / GetAppSecretKey carry a kbs_resource_uri and an
// additional_data binding field, so a Key Broker Service releases material only
// after verifying an attestation and additional_data can carry the derivation path
// for domain separation. The deleted implementation used neither; it read a plain
// file, which is why the key was reproducible on an unattested machine.
//
// See doc/removed-tee-backends.md.
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
