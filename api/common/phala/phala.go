package phala

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/0glabs/0g-serving-broker/common/errors"
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

	socket, err := net.Dial(SocketNetworkType, SocketAddress)
	if err != nil {
		return "", errors.Wrap(err, "creating socket")
	}
	defer socket.Close()

	transport := &http.Transport{
		Dial: func(network, addr string) (net.Conn, error) {
			return socket, nil
		},
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}

	req, err := http.NewRequest(http.MethodPost, Url, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", errors.Wrap(err, "creating request")
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", errors.Wrap(err, "sending request")
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", errors.Wrap(err, "reading response body")
	}

	return hex.EncodeToString(body), nil
}

func SigningKey(ctx context.Context) (*ecdsa.PrivateKey, error) {
	// Todo: Uncomment this code
	// client := tappd.NewTappdClient()

	// deriveKeyResp, err := client.DeriveKey(ctx, "/")

	// if err != nil {
	// 	return nil, errors.Wrap(err, "new tapped client")
	// }
	key, err := crypto.GenerateKey()
	if err != nil {
		return nil, err
	}

	privateKeyBytes := crypto.FromECDSA(key)
	if len(privateKeyBytes) != 32 {
		return nil, errors.New("Error: private key must be 32 bytes long")
	}

	privateKey, err := crypto.ToECDSA(privateKeyBytes)
	if err != nil {
		return nil, errors.Wrap(err, "converting to ECDSA private key")
	}

	return privateKey, nil
}
