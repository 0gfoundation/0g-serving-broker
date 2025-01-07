package ctrl

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/0glabs/0g-serving-broker/common/errors"
)

const (
	Url               = "http://localhost/prpc/Tappd.tdxQuote?json"
	SocketNetworkType = "unix"
	SocketAddress     = "/var/run/tappd.sock"
)

type QuoteRequest struct {
	ReportData string `json:"report_data"`
}

func (c *Ctrl) ReadQuote(ctx context.Context, request QuoteRequest) (string, error) {
	jsonData, err := json.Marshal(request)
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

	c.logger.Infof("Response status: %v", resp.Status)
	return string(body), nil
}
