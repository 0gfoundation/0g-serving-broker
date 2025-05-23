package ctrl

import (
	"context"
	"log"

	"github.com/ethereum/go-ethereum/common"
	"github.com/patrickmn/go-cache"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/inference/contract"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/0glabs/0g-serving-broker/inference/zkclient/client/operations"
	"github.com/0glabs/0g-serving-broker/inference/zkclient/models"
)

func (c *Ctrl) CheckSignatures(ctx context.Context, req *models.RequestResponse, sigs models.Signatures) ([]bool, error) {
	var userAccount contract.Account
	value, found := c.svcCache.Get(req.UserAddress)
	if found {
		account, ok := value.(contract.Account)
		if !ok {
			return nil, errors.New("cached object does not implement contract.Account")
		}
		userAccount = account
	} else {
		var err error
		userAccount, err = c.contract.GetUserAccount(ctx, common.HexToAddress(req.UserAddress))
		if err != nil {
			return nil, err
		}
		c.svcCache.Set(req.UserAddress, userAccount, cache.DefaultExpiration)
	}

	ret, err := c.zk.Operation.CheckSignature(
		operations.NewCheckSignatureParamsWithContext(ctx).WithBody(operations.CheckSignatureBody{
			Pubkey:     []string{userAccount.Signer[0].String(), userAccount.Signer[1].String()},
			Requests:   []*models.RequestResponse{req},
			Signatures: sigs,
		}),
	)
	if err != nil {
		log.Printf("check signature from zk server failed: %v", err)
		return nil, errors.Wrap(err, "check signature from zk server")
	}

	return ret.Payload, nil
}

func (c *Ctrl) GenerateSignatures(ctx context.Context, req *models.RequestResponse) (models.Signatures, error) {
	ret, err := c.zk.Operation.GenerateSignature(
		operations.NewGenerateSignatureParamsWithContext(ctx).WithBody(operations.GenerateSignatureBody{
			Privkey:  c.signer.PrivKey,
			Requests: []*models.RequestResponse{req},
		}),
	)
	if err != nil {
		return nil, errors.Wrap(err, "check signature from zk server")
	}

	return ret.Payload.Signatures, nil
}

func (c *Ctrl) GenerateSolidityCalldata(ctx context.Context, reqs []*models.RequestResponse, sigs, responseSignatures models.Signatures) (*operations.GenerateSolidityCalldataCombinedOKBody, error) {
	if len(reqs) == 0 {
		return nil, nil
	}
	userAccount, err := c.contract.GetUserAccount(ctx, common.HexToAddress(reqs[0].UserAddress))
	if err != nil {
		return nil, err
	}
	ret, err := c.zk.Operation.GenerateSolidityCalldataCombined(
		operations.NewGenerateSolidityCalldataCombinedParamsWithContext(ctx).WithBackend(model.PtrOf("rust")).WithBody(operations.GenerateSolidityCalldataCombinedBody{
			L:                  int64(c.zk.RequestLength),
			UserPubkey:         []string{userAccount.Signer[0].String(), userAccount.Signer[1].String()},
			Requests:           reqs,
			RequestSignatures:  sigs,
			ResponseSignatures: responseSignatures,
			TeeSignerPubkey:    []string{c.signer.PublicKey[0].String(), c.signer.PublicKey[1].String()},
		}),
	)
	if err != nil {
		return nil, errors.Wrap(err, "generate proof input from zk server")
	}
	return ret.Payload, nil
}
