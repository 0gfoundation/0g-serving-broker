package ctrl

import (
	"context"
	"log"
	"time"

	"github.com/0glabs/0g-serving-agent/common/contract"
	"github.com/0glabs/0g-serving-agent/common/errors"
	"github.com/0glabs/0g-serving-agent/common/util"
	"github.com/0glabs/0g-serving-agent/provider/model"
)

func (c *Ctrl) SettleFees(ctx context.Context) error {
	reqs, err := c.db.ListRequest()
	if err != nil {
		return errors.Wrap(err, "list request from db")
	}
	if len(reqs) == 0 {
		return errors.Wrap(c.db.ResetUnsettledFee(), "reset unsettled fee in db")
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
	if err := c.db.UpdateRequest(); err != nil {
		return errors.Wrap(err, "update request in db")
	}
	if err := c.SyncUserAccounts(ctx); err != nil {
		return errors.Wrap(err, "synchronize accounts from the contract to the database")
	}

	return errors.Wrap(c.db.ResetUnsettledFee(), "reset unsettled fee in db")
}

func (c Ctrl) ProcessSettlement(ctx context.Context) error {
	accounts, err := c.db.ListUserAccount(&model.UserListOptions{
		LowBalanceRisk:  model.PtrOf(time.Now().Add(-c.contract.LockTime - c.AutoSettleBufferTime)),
		MinUnsettledFee: model.PtrOf(int64(0)),
	})
	if err != nil {
		return errors.Wrap(err, "list accounts that need to be settled in db")
	}
	if len(accounts) == 0 {
		return nil
	}
	// Verify the available balance in the contract.
	// If it exceeds the fee, no settlement is necessary;
	// the balance is sufficient for at least the next lock period.
	if err := c.SyncUserAccounts(ctx); err != nil {
		return errors.Wrap(err, "synchronize accounts from the contract to the database")
	}
	accounts, err = c.db.ListUserAccount(&model.UserListOptions{
		MinUnsettledFee: model.PtrOf(int64(0)),
		LowBalanceRisk:  model.PtrOf(time.Now()),
	})
	if err != nil {
		return errors.Wrap(err, "list accounts that need to be settled in db")
	}
	if len(accounts) == 0 {
		return nil
	}
	log.Print("Accounts at risk of having insufficient funds and will be settled immediately.")
	return errors.Wrap(c.SettleFees(ctx), "settle fees")
}
