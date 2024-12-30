package ctrl

import (
	"context"

	"github.com/ethereum/go-ethereum/common"
	"github.com/patrickmn/go-cache"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/inference/contract"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/0glabs/0g-serving-broker/inference/zkclient/client/operations"
	"github.com/0glabs/0g-serving-broker/inference/zkclient/models"
)

func (c *Ctrl) CheckSignatures(ctx context.Context, req *models.Request, sigs models.Signatures) ([]bool, error) {
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
			Requests:   []*models.Request{req},
			Signatures: sigs,
		}),
	)
	if err != nil {
		return nil, errors.Wrap(err, "check signature from zk server")
	}

	return ret.Payload, nil
}

func (c *Ctrl) GenerateSolidityCalldata(ctx context.Context, reqs []*models.Request, sigs models.Signatures) (*operations.GenerateSolidityCalldataCombinedOKBody, error) {
	if len(reqs) == 0 {
		return nil, nil
	}
	userAccount, err := c.contract.GetUserAccount(ctx, common.HexToAddress(reqs[0].UserAddress))
	if err != nil {
		return nil, err
	}
	ret, err := c.zk.Operation.GenerateSolidityCalldataCombined(
		operations.NewGenerateSolidityCalldataCombinedParamsWithContext(ctx).WithBackend(model.PtrOf("rust")).WithBody(operations.GenerateSolidityCalldataCombinedBody{
			L:          int64(c.zk.RequestLength),
			Pubkey:     []string{userAccount.Signer[0].String(), userAccount.Signer[1].String()},
			Requests:   reqs,
			Signatures: sigs,
		}),
	)
	if err != nil {
		return nil, errors.Wrap(err, "generate proof input from zk server")
	}
	return ret.Payload, nil
}
