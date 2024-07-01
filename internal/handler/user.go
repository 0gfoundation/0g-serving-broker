package handler

import (
	"context"
	"log"
	"math/big"
	"net/http"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-data-retrieve-agent/internal/errors"
	"github.com/0glabs/0g-data-retrieve-agent/internal/model"
)

func (h *Handler) AddAccount(ctx *gin.Context) {
	var account model.Account
	if err := account.Bind(ctx); err != nil {
		errors.Response(ctx, err)
		return
	}

	if ret := h.db.Create(&account); ret.Error != nil {
		errors.Response(ctx, errors.Wrap(ret.Error, "create account in db"))
		return
	}

	opts := h.contract.CreateTransactOpts()
	opts.Value = big.NewInt(0)
	opts.Value.SetString(account.Balance, 10)

	doFunc := func() error {
		_, err := h.contract.DepositFund(opts, common.HexToAddress(account.Provider))
		return errors.Wrap(err, "add account to contract")
	}
	if err := doFunc(); err != nil {
		log.Println("failed to add account, rolling back...")
		errRollback := h.db.Where("provider = ?", account.Provider).Delete(&model.Account{}, account.Provider)
		log.Printf("rollback result: %v", errRollback)
		errors.Response(ctx, err)
		return
	}

	ctx.Status(http.StatusAccepted)
}

func (h *Handler) ListAccount(ctx *gin.Context) {
	list := []model.Account{}

	callOpts := &bind.CallOpts{
		Context: context.Background(),
	}
	users, providers, balances, err := h.contract.GetAllUserAccounts(callOpts)
	if err != nil {
		errors.Response(ctx, errors.Wrap(err, "list account from contract"))
		return
	}

	for i, u := range users {
		list = append(list, model.Account{
			User:     u.String(),
			Provider: providers[i].String(),
			Balance:  balances[i].String(),
		})
	}
	ctx.JSON(http.StatusOK, model.AccountList{
		Metadata: model.ListMeta{Total: uint64(len(list))},
		Items:    list,
	})
}

func (h *Handler) GetData(ctx *gin.Context) {
	provider := ctx.Param("provider")
	svcName := ctx.Param("service")

	callOpts := &bind.CallOpts{
		Context: context.Background(),
	}
	svc, err := h.contract.GetService(callOpts, common.HexToAddress(provider), svcName)
	if err != nil {
		errors.Response(ctx, errors.Wrap(err, "get service from contract"))
		return
	}
	h.proxy.GetData(ctx, svc.Url, svcName, provider, h.key)
}
