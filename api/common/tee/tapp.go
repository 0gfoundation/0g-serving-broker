package tee

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/hex"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/0glabs/0g-serving-broker/common/errors"

	pb "github.com/0glabs/0g-serving-broker/common/tee/tapp/proto"
)

type ZgTappdClient struct {
	zgTappURL string
}

func (c *ZgTappdClient) TdxQuote(ctx context.Context, jsonData []byte) (*TdxQuoteResponse, error) {
	zgTappURL := c.zgTappURL

	conn, err := grpc.NewClient(zgTappURL, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, errors.Wrapf(err, "Failed to connect to TAPP server at %s", zgTappURL)
	}
	defer conn.Close()

	client := pb.NewTappServiceClient(conn)

	quoteReq := &pb.GetQuoteRequest{
		ReportData: jsonData,
	}
	quoteResp, err := client.GetQuote(context.Background(), quoteReq)
	if err != nil {
		return nil, errors.Wrapf(err, "Failed to get quote from TAPP server at %s", zgTappURL)
	}

	return &TdxQuoteResponse{
		Quote:    string(quoteResp.QuoteData),
		EventLog: "",
	}, nil
}

func (c *ZgTappdClient) DeriveKey(ctx context.Context, path string) (string, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", errors.Wrap(err, "Failed to generate ECDSA private key")
	}

	dHex := hex.EncodeToString(privateKey.D.Bytes())
	return dHex, nil
}
