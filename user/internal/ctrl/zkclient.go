package ctrl

import (
	"context"
	"math/big"

	"github.com/0glabs/0g-serving-agent/common/errors"
	"github.com/0glabs/0g-serving-agent/common/util"
	"github.com/0glabs/0g-serving-agent/common/zkclient/client/operations"
	"github.com/0glabs/0g-serving-agent/common/zkclient/models"
	database "github.com/0glabs/0g-serving-agent/user/internal/db"
	"github.com/0glabs/0g-serving-agent/user/model"
)

func (c *Ctrl) getOrCreateKeyPair(ctx context.Context) (model.KeyPair, error) {
	pair, err := c.db.GetKeyPair("keypair")
	if database.IgnoreNotFound(err) != nil {
		return pair, errors.Wrap(err, "get key pair from db")
	}
	if err == nil {
		return pair, nil
	}

	ret, err := c.zk.Operation.GenerateKeyPair(
		operations.NewGenerateKeyPairParamsWithContext(ctx),
	)
	if err != nil {
		return pair, errors.Wrap(err, "generate key pair from zk server")
	}
	pair = model.KeyPair{
		ZKPrivateKey: ret.Payload.Privkey,
		ZKPublicKey:  [2]string(ret.Payload.Pubkey),
	}
	err = c.db.AddOrUpdateKeyPair(pair)
	if err != nil {
		return pair, errors.Wrap(err, "add key pair to db")
	}
	return pair, nil
}

func (c *Ctrl) GenerateSignature(ctx context.Context, req *models.Request) (models.Signatures, error) {
	keyPair, err := c.getOrCreateKeyPair(ctx)
	if err != nil {
		return nil, err
	}
	ret, err := c.zk.Operation.GenerateSignature(
		operations.NewGenerateSignatureParamsWithContext(ctx).WithBody(operations.GenerateSignatureBody{
			Privkey:  keyPair.ZKPrivateKey,
			Requests: []*models.Request{req},
		}),
	)
	if err != nil {
		return nil, errors.Wrap(err, "generate key pair from zk server")
	}

	return ret.Payload.Signatures, nil
}

func (c *Ctrl) getSigner(ctx context.Context) ([2]*big.Int, error) {
	keyPair, err := c.getOrCreateKeyPair(ctx)
	if err != nil {
		return [2]*big.Int{}, err
	}
	pubKey0, err := util.HexadecimalStringToBigInt(keyPair.ZKPublicKey[0])
	if err != nil {
		return [2]*big.Int{}, err
	}
	pubKey1, err := util.HexadecimalStringToBigInt(keyPair.ZKPublicKey[1])
	if err != nil {
		return [2]*big.Int{}, err
	}
	return [2]*big.Int{pubKey0, pubKey1}, nil
}
