package ctrl

import (
	"context"
	"time"

	"github.com/0glabs/0g-serving-agent/common/contract"
	"github.com/0glabs/0g-serving-agent/common/errors"
	"github.com/0glabs/0g-serving-agent/common/util"
	"github.com/0glabs/0g-serving-agent/provider/internal/db"
	"github.com/0glabs/0g-serving-agent/provider/model"
	"github.com/ethereum/go-ethereum/common"
)

func (c *Ctrl) GetOrCreateAccount(ctx context.Context, userAddress string) (model.User, error) {
	dbAccount, err := c.db.GetUserAccount(userAddress)
	if db.IgnoreNotFound(err) != nil {
		return dbAccount, errors.Wrap(err, "get account from db")
	}
	if err == nil {
		return dbAccount, nil
	}
	contractAccount, err := c.contract.GetUserAccount(ctx, common.HexToAddress(userAddress))
	if err != nil {
		return model.User{}, errors.Wrap(err, "get account from contract")
	}

	dbAccount = model.User{
		User:                 userAddress,
		LastRequestNonce:     model.PtrOf(contractAccount.Nonce.Int64()),
		LockBalance:          model.PtrOf(contractAccount.Balance.Int64() - contractAccount.PendingRefund.Int64()),
		LastBalanceCheckTime: model.PtrOf(time.Now()),
		UnsettledFee: model.PtrOf(int64(0)),
		LastResponseTokenCount: model.PtrOf(int64(0)),
	}

	return dbAccount, errors.Wrap(c.db.CreateUserAccount(dbAccount), "create account in db")
}

func (c *Ctrl) UpdateUserAccount(userAddress string, new model.User) error {
	return errors.Wrap(c.db.UpdateUserAccount(userAddress, new), "create account in db")

}

func (c *Ctrl) SettleFees(ctx context.Context) error {
	reqs, err := c.db.ListRequest()
	if err != nil {
		return errors.Wrap(err, "list request from db")
	}

	categorizedTraces := make(map[string]*contract.RequestTrace)
	for _, req := range reqs {
		cReq, err := util.ToContractRequest(req)
		if err != nil {
			return errors.Wrap(err, "convert request to contract acceptable format")
		}
		_, ok := categorizedTraces[req.UserAddress]
		if ok {
			categorizedTraces[req.UserAddress].Requests = append(categorizedTraces[req.UserAddress].Requests, cReq)
			continue
		}
		categorizedTraces[req.UserAddress] = &contract.RequestTrace{
			Requests: []contract.Request{cReq},
		}
	}

	traces := []contract.RequestTrace{}
	for _, t := range categorizedTraces {
		traces = append(traces, *t)
	}

	if err := c.contract.SettleFees(ctx, traces); err != nil {
		return errors.Wrap(err, "settle fees in contract")
	}

	return errors.Wrap(c.db.UpdateRequest(), "update service in db")
}

func (c *Ctrl) SyncAccount(ctx context.Context, userAddress common.Address) error {
	account, err := c.contract.GetUserAccount(ctx, userAddress)
	if err != nil {
		return err
	}

	new := model.User{
		LockBalance:          model.PtrOf(account.Balance.Int64() - account.PendingRefund.Int64()),
		LastBalanceCheckTime: model.PtrOf(time.Now()),
	}
	return errors.Wrap(c.db.UpdateUserAccount(userAddress.String(), new), "update in db")
}
