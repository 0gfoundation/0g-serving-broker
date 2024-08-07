package ctrl

import (
	"context"

	"github.com/0glabs/0g-serving-agent/common/errors"
	commonModel "github.com/0glabs/0g-serving-agent/common/model"
	"github.com/0glabs/0g-serving-agent/common/zkclient/client/operations"
	database "github.com/0glabs/0g-serving-agent/user/internal/db"
)

func (c *Ctrl) getOrCreateKeyPair(ctx context.Context) (commonModel.KeyPair, error) {
	pair, err := c.db.GetKeyPair("keypair")
	if database.IgnoreNotFound(err) != nil {
		return pair, errors.Wrap(err, "get key pair from db")
	}
	if err == nil {
		return pair, nil
	}

	ret, err := c.zkclient.GenerateKeyPair(
		operations.NewGenerateKeyPairParams().WithContext(ctx),
	)
	if err != nil {
		return pair, errors.Wrap(err, "generate key pair from zk server")
	}
	pair = commonModel.KeyPair{
		ZKPrivateKey: ret.Payload.Privkey,
		ZKPublicKey:  [2][]int64(ret.Payload.Pubkey),
	}
	err = c.db.AddOrUpdateKeyPair(pair)
	if err != nil {
		return pair, errors.Wrap(err, "add key pair to db")
	}
	return pair, nil
}
