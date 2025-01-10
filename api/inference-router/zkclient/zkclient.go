package zkclient

import (
	"github.com/0glabs/0g-serving-broker/inference-router/zkclient/client"
	"github.com/0glabs/0g-serving-broker/inference-router/zkclient/client/operations"
)

//go:generate swagger generate client --target . --spec ./swagger.yml --skip-validation

type ZKClient struct {
	Operation     operations.ClientService
	RequestLength int
}

func NewZKClient(host string, requestLength int) ZKClient {
	return ZKClient{
		Operation: client.NewHTTPClientWithConfig(
			nil, client.DefaultTransportConfig().WithHost(host),
		).Operations,
		RequestLength: requestLength,
	}
}
