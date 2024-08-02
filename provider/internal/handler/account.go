package handler

import (
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-agent/provider/model"
)

func (h *Handler) ListUserAccount(ctx *gin.Context) {
	list, err := h.ctrl.ListUserAccount(ctx, true)
	if err != nil {
		handleError(ctx, err, "list accounts")
		return
	}

	ctx.JSON(http.StatusOK, model.UserList{
		Metadata: model.ListMeta{Total: uint64(len(list))},
		Items:    list,
	})
}

func (h *Handler) GetUserAccount(ctx *gin.Context) {
	userAddress := ctx.Param("user")
	account, err := h.ctrl.GetUserAccount(ctx, common.HexToAddress(userAddress))
	if err != nil {
		handleError(ctx, err, "get account from db")
		return
	}

	ctx.JSON(http.StatusOK, account)
}

func (h *Handler) SyncUserAccounts(ctx *gin.Context) {
	if err := h.ctrl.SyncUserAccounts(ctx); err != nil {
		handleError(ctx, err, "synchronize accounts from the contract to the database")
		return
	}

	ctx.Status(http.StatusAccepted)
}

func (h *Handler) SyncUserAccount(ctx *gin.Context) {
	userAddress := ctx.Param("user")
	if err := h.ctrl.SyncUserAccount(ctx, common.HexToAddress(userAddress)); err != nil {
		handleError(ctx, err, "synchronize account from the contract to the database")
		return
	}

	ctx.Status(http.StatusAccepted)
}
