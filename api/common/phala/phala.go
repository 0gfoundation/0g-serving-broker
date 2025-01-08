package phala

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/Dstack-TEE/dstack/sdk/go/tappd"
)

const (
	Url               = "http://localhost/prpc/Tappd.tdxQuote?json"
	SocketNetworkType = "unix"
	SocketAddress     = "/var/run/tappd.sock"
)

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

func SigningKey(ctx context.Context, reportData string) (*ecdsa.PrivateKey, error) {
	client := tappd.NewTappdClient()
	deriveKeyResp, err := client.DeriveKey(ctx, fmt.Sprintf("/%s", reportData))

	if err != nil {
		return nil, errors.Wrap(err, "new tapped client")
	}

	privateKeyBytes, err := deriveKeyResp.ToBytes(-1)
	if err != nil {
		return nil, errors.Wrap(err, "decode private key")
	}

	privateKey, err := x509.ParseECPrivateKey(privateKeyBytes)
	if err != nil {
		return nil, errors.Wrap(err, "parse EC private key")
	}

	return privateKey, nil
}

func SerializePublicKey(ctx context.Context, privateKey *ecdsa.PrivateKey) (string, error) {
	publicKeyBytes, err := x509.MarshalPKIXPublicKey(privateKey.PublicKey)
	if err != nil {
		return "", errors.Wrap(err, "serializing public key")
	}

	return hex.EncodeToString(publicKeyBytes), nil
}
