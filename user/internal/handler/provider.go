package handler

import (
	"context"
	"log"
	"math/big"
	"net/http"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-agent/common/errors"
	"github.com/0glabs/0g-serving-agent/user/model"
)

func (h *Handler) AddProviderAccount(ctx *gin.Context) {
	var account model.Provider
	if err := account.Bind(ctx); err != nil {
		errors.Response(ctx, err)
		return
	}

	if ret := h.db.Create(&account); ret.Error != nil {
		errors.Response(ctx, errors.Wrap(ret.Error, "create provider account in db"))
		return
	}

	opts, err := h.contract.CreateTransactOpts()
	if err != nil {
		errors.Response(ctx, err)
		return
	}

	opts.Value = big.NewInt(0)
	opts.Value.SetString(account.Balance, 10)

	doFunc := func() error {
		_, err := h.contract.DepositFund(opts, common.HexToAddress(account.Provider))
		return errors.Wrap(err, "add provider account to contract")
	}
	if err := doFunc(); err != nil {
		log.Println("failed to add provider account, rolling back...")
		errRollback := h.db.Where("provider = ?", account.Provider).Delete(&model.Provider{})
		log.Printf("rollback result: %v", errRollback)
		errors.Response(ctx, err)
		return
	}

	ctx.Status(http.StatusAccepted)
}

func (h *Handler) ListProviderAccount(ctx *gin.Context) {
	list := []model.Provider{}

	callOpts := &bind.CallOpts{
		Context: context.Background(),
	}
	users, providers, balances, err := h.contract.GetAllUserAccounts(callOpts)
	if err != nil {
		errors.Response(ctx, errors.Wrap(err, "list account from contract"))
		return
	}

	for i, u := range users {
		if u.String() != h.userAddress {
			continue
		}
		list = append(list, model.Provider{
			Provider: providers[i].String(),
			Balance:  balances[i].String(),
		})
	}
	ctx.JSON(http.StatusOK, model.ProviderList{
		Metadata: model.ListMeta{Total: uint64(len(list))},
		Items:    list,
	})
}
