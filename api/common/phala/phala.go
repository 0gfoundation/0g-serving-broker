package phala

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/json"
	"encoding/pem"
	"os"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/Dstack-TEE/dstack/sdk/go/tappd"
	"github.com/ethereum/go-ethereum/crypto"
)

const (
	Url               = "http://localhost/prpc/Tappd.tdxQuote?json"
	SocketNetworkType = "unix"
	SocketAddress     = "/var/run/tappd.sock"
)

// TODO: remove this function
func QuoteMock(ctx context.Context, reportData string) (string, error) {
	return "mock", nil
}

func Quote(ctx context.Context, reportData string) (string, error) {
	jsonData, err := json.Marshal(map[string]interface{}{
		"report_data": reportData,
	})
	if err != nil {
		return "", errors.Wrap(err, "encoding json")
	}

	client := tappd.NewTappdClient()
	tdxQuoteResp, err := client.TdxQuote(context.Background(), jsonData)
	if err != nil {
		return "", errors.Wrap(err, "tdx quote")
	}

	return tdxQuoteResp.Quote, nil
}

func SigningKey(ctx context.Context) (*ecdsa.PrivateKey, error) {
	var keyHex string
	var privateKeyBytes []byte
	if os.Getenv("NETWORK") == "hardhat" {
		keyHex = "4c0883a69102937d6231471b5dbb6204fe512961708279b7e1a8d7d7a3c2b9e3"
		key, err := crypto.HexToECDSA(keyHex)
		if err != nil {
			return nil, errors.Wrap(err, "converting hex to ECDSA key")
		}

		privateKeyBytes = crypto.FromECDSA(key)
		if len(privateKeyBytes) != 32 {
			return nil, errors.New("Error: private key must be 32 bytes long")
		}
	} else {
		client := tappd.NewTappdClient()

		deriveKeyResp, err := client.DeriveKey(ctx, "/")

		if err != nil {
			return nil, errors.Wrap(err, "new tapped client")
		}

		block, _ := pem.Decode([]byte(deriveKeyResp.Key))
		if block == nil || block.Type != "PRIVATE KEY" {
			return nil, errors.New("Error: failed to decode PEM block containing the key")
		}

		hash := sha256.Sum256(block.Bytes)
		privateKeyBytes = hash[:]
	}

	privateKey, err := crypto.ToECDSA(privateKeyBytes)
	if err != nil {
		return nil, errors.Wrap(err, "converting to ECDSA private key")
	}

	return privateKey, nil
}
