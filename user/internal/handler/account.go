package handler

import (
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-agent/common/errors"
	"github.com/0glabs/0g-serving-agent/user/model"
)

func (h *Handler) AddProviderAccount(ctx *gin.Context) {
	var account model.Provider
	if err := account.Bind(ctx); err != nil {
		handleError(ctx, err, "bind account")
		return
	}
	if account.Provider == "" {
		handleError(ctx, errors.New("missing field Provider"), "create account")
	}

	if err := h.ctrl.CreateProviderAccount(ctx, common.HexToAddress(account.Provider), account); err != nil {
		handleError(ctx, err, "create account")
		return
	}
	ctx.Status(http.StatusCreated)
}

func (h *Handler) ListProviderAccount(ctx *gin.Context) {
	accounts, err := h.ctrl.ListProviderAccount(ctx)
	if err != nil {
		handleError(ctx, err, "list account")
		return
	}
	ctx.JSON(http.StatusOK, model.ProviderList{
		Metadata: model.ListMeta{Total: uint64(len(accounts))},
		Items:    accounts,
	})
}

func (h *Handler) GetProviderAccount(ctx *gin.Context) {
	providerAddress := ctx.Param("provider")
	account, err := h.ctrl.GetProviderAccount(ctx, common.HexToAddress(providerAddress))
	if err != nil {
		handleError(ctx, err, "get account from db")
		return
	}

	ctx.JSON(http.StatusOK, account)
}

func (h *Handler) Charge(ctx *gin.Context) {
	providerAddress := ctx.Param("provider")
	var account model.Provider
	if err := account.Bind(ctx); err != nil {
		handleError(ctx, err, "bind account")
		return
	}

	if err := h.ctrl.ChargeProviderAccount(ctx, common.HexToAddress(providerAddress), account); err != nil {
		handleError(ctx, err, "charge account")
		return
	}
	ctx.Status(http.StatusAccepted)
}

func (h *Handler) Refund(ctx *gin.Context) {
	providerAddress := ctx.Param("provider")
	var refund model.Refund
	if err := refund.Bind(ctx); err != nil {
		handleError(ctx, err, "bind refund body")
		return
	}
	if err := h.ctrl.RequestRefund(ctx, common.HexToAddress(providerAddress), refund); err != nil {
		handleError(ctx, err, "process refund")
		return
	}

	ctx.Status(http.StatusAccepted)
}

func (h *Handler) SyncProviderAccounts(ctx *gin.Context) {
	if err := h.ctrl.SyncProviderAccounts(ctx); err != nil {
		handleError(ctx, err, "sync all data")
		return
	}

	ctx.Status(http.StatusAccepted)
}

func (h *Handler) SyncProviderAccount(ctx *gin.Context) {
	providerAddress := ctx.Param("provider")
	if err := h.ctrl.SyncProviderAccount(ctx, common.HexToAddress(providerAddress)); err != nil {
		handleError(ctx, err, "sync data")
		return
	}

	ctx.Status(http.StatusAccepted)
}
