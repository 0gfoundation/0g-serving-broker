package handler

import (
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/inference-router/model"
)

// addProviderAccount
//
//	@ID			addProviderAccount
//	@Tags		provider
//	@Router		/provider [post]
//	@Param		body	body	model.Provider	true	"body"
//	@Success	204		"No Content - success without response body"
func (h *Handler) AddProviderAccount(ctx *gin.Context) {
	var account model.Provider
	if err := account.Bind(ctx); err != nil {
		handleBrokerError(ctx, err, "bind account")
		return
	}
	if account.Provider == "" {
		handleBrokerError(ctx, errors.New("missing field Provider"), "create account")
	}

	if err := h.ctrl.CreateProviderAccount(ctx, common.HexToAddress(account.Provider), account); err != nil {
		handleBrokerError(ctx, err, "create account")
		return
	}
	ctx.Status(http.StatusNoContent)
}

// listProviderAccount
//
//	@ID			listProviderAccount
//	@Tags		provider
//	@Router		/provider [get]
//	@Success	200	{object}	model.ProviderList
func (h *Handler) ListProviderAccount(ctx *gin.Context) {
	accounts, err := h.ctrl.ListProviderAccount(ctx)
	if err != nil {
		handleBrokerError(ctx, err, "list account")
		return
	}
	ctx.JSON(http.StatusOK, model.ProviderList{
		Metadata: model.ListMeta{Total: uint64(len(accounts))},
		Items:    accounts,
	})
}

// getProviderAccount
//
//	@ID			getProviderAccount
//	@Tags		provider
//	@Router		/provider/{provider} [get]
//	@Param		provider	path	string	true	"Provider address"
//	@Success	200	{object}	model.Provider
func (h *Handler) GetProviderAccount(ctx *gin.Context) {
	providerAddress := ctx.Param("provider")
	account, err := h.ctrl.GetProviderAccount(ctx, common.HexToAddress(providerAddress))
	if err != nil {
		handleBrokerError(ctx, err, "get account from db")
		return
	}

	ctx.JSON(http.StatusOK, account)
}

// charge
//
//	@Description  This endpoint allows you to add fund to an account
//	@ID			charge
//	@Tags		provider
//	@Router		/provider/{provider}/charge [post]
//	@Param		provider	path	string	true	"Provider address"
//	@Param		body	body	model.Provider	true	"body"
//	@Success	202
func (h *Handler) Charge(ctx *gin.Context) {
	providerAddress := ctx.Param("provider")
	var account model.Provider
	if err := account.Bind(ctx); err != nil {
		handleBrokerError(ctx, err, "bind account")
		return
	}

	if err := h.ctrl.ChargeProviderAccount(ctx, common.HexToAddress(providerAddress), account); err != nil {
		handleBrokerError(ctx, err, "charge account")
		return
	}
	ctx.Status(http.StatusAccepted)
}

// syncProviderAccounts
//
//	@Description  This endpoint allows you to synchronize information of all accounts from the contract
//	@ID			syncProviderAccounts
//	@Tags		provider
//	@Router		/sync [post]
//	@Success	202
func (h *Handler) SyncProviderAccounts(ctx *gin.Context) {
	if err := h.ctrl.SyncProviderAccounts(ctx); err != nil {
		handleBrokerError(ctx, err, "sync all data")
		return
	}

	ctx.Status(http.StatusAccepted)
}

// syncProviderAccount
//
//	@Description  This endpoint allows you to synchronize information of single account from the contract
//	@ID			syncProviderAccount
//	@Tags		provider
//	@Router		/provider/{provider}/sync [post]
//	@Param		provider	path	string	true	"Provider address"
//	@Success	202
func (h *Handler) SyncProviderAccount(ctx *gin.Context) {
	providerAddress := ctx.Param("provider")
	if err := h.ctrl.SyncProviderAccount(ctx, common.HexToAddress(providerAddress)); err != nil {
		handleBrokerError(ctx, err, "sync data")
		return
	}

	ctx.Status(http.StatusAccepted)
}
