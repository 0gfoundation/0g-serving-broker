package contract

import (
	"time"

	"github.com/openweb3/web3go"
	"github.com/openweb3/web3go/interfaces"
	"github.com/openweb3/web3go/signers"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

func MustNewWeb3(url, key string) *web3go.Client {
	client, err := NewWeb3(url, key)
	if err != nil {
		logrus.WithError(err).WithField("url", url).Fatal("Failed to connect to full node")
	}

	return client
}

func NewWeb3(url, key string) (*web3go.Client, error) {
	sm := signers.MustNewSignerManagerByPrivateKeyStrings([]string{key})

	option := new(web3go.ClientOption).
		WithRetry(3, time.Second).
		WithTimout(5 * time.Second).
		WithSignerManager(sm)

	option = option.WithLooger(logrus.StandardLogger().Out)

	return web3go.NewClientWithOption(url, *option)
}

func defaultSigner(clientWithSigner *web3go.Client) (interfaces.Signer, error) {
	sm, err := clientWithSigner.GetSignerManager()
	if err != nil {
		return nil, errors.WithMessage(err, "Failed to get signer manager from client")
	}

	if sm == nil {
		return nil, errors.New("Signer not specified")
	}

	signers := sm.List()
	if len(signers) == 0 {
		return nil, errors.WithMessage(err, "Account not configured in signer manager")
	}

	return signers[0], nil
}
