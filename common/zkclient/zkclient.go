package zkclient

import (
	"github.com/0glabs/0g-serving-agent/common/zkclient/client"
	"github.com/0glabs/0g-serving-agent/common/zkclient/client/operations"
)

//go:generate swagger generate client --target . --spec ./swagger.yml --skip-validation

type ZKClient operations.ClientService

func NewZKClient(host string) ZKClient {
	return client.NewHTTPClientWithConfig(
		nil, client.DefaultTransportConfig().WithHost(host),
	).Operations
}
