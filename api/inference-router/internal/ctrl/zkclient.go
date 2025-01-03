package ctrl

import (
	"context"
	"math/big"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	database "github.com/0glabs/0g-serving-broker/inference-router/internal/db"
	"github.com/0glabs/0g-serving-broker/inference-router/model"
	"github.com/0glabs/0g-serving-broker/inference-router/zkclient/client/operations"
	"github.com/0glabs/0g-serving-broker/inference-router/zkclient/models"
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

	// To align with data stored in contract
	bigIntPrivateKey, err := c.parseHexadecimalKey([2]string(ret.Payload.Privkey))
	if err != nil {
		return pair, errors.Wrap(err, "parse hexadecimal private key")
	}
	bigIntPublicKey, err := c.parseHexadecimalKey([2]string(ret.Payload.Pubkey))
	if err != nil {
		return pair, errors.Wrap(err, "parse hexadecimal public key")
	}
	pair = model.KeyPair{
		ZKPrivateKey: []string{bigIntPrivateKey[0].String(), bigIntPrivateKey[1].String()},
		ZKPublicKey:  [2]string{bigIntPublicKey[0].String(), bigIntPublicKey[1].String()},
	}
	err = c.db.AddOrUpdateKeyPair(pair)
	if err != nil {
		return pair, errors.Wrap(err, "add key pair to db")
	}
	return pair, nil
}

func (c *Ctrl) GenerateSignature(ctx context.Context, req *models.Request, signer []string) (models.Signatures, error) {
	keyPair, err := c.getOrCreateKeyPair(ctx)
	if err != nil {
		return nil, err
	}
	if len(signer) == 2 && (keyPair.ZKPublicKey[0] != signer[0] || keyPair.ZKPublicKey[1] != signer[1]) {
		return nil, errors.New("signer in db mismatches that on contract")
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

func (c *Ctrl) parseHexadecimalKey(old [2]string) ([2]*big.Int, error) {
	new0, err := util.HexadecimalStringToBigInt(old[0])
	if err != nil {
		return [2]*big.Int{}, err
	}
	new1, err := util.HexadecimalStringToBigInt(old[1])
	if err != nil {
		return [2]*big.Int{}, err
	}
	return [2]*big.Int{new0, new1}, nil
}

func (c *Ctrl) parseBigIntStringKey(old [2]string) ([2]*big.Int, error) {
	new0 := new(big.Int)
	_, success := new0.SetString(old[0], 10)
	if !success {
		return [2]*big.Int{}, errors.New("parse bigInt.string value to bigInt")
	}
	new1 := new(big.Int)
	_, success = new1.SetString(old[1], 10)
	if !success {
		return [2]*big.Int{}, errors.New("parse bigInt.string value to bigInt")
	}
	return [2]*big.Int{new0, new1}, nil
}
