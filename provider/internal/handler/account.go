package handler

import (
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"
)

func (h *Handler) ListUserAccount(ctx *gin.Context) {
	// list := []model.Account{}

	// callOpts := &bind.CallOpts{
	// 	Context: context.Background(),
	// }
	// users, providers, balances, err := h.contract.GetAllUserAccounts(callOpts)
	// if err != nil {
	// 	errors.Response(ctx, errors.Wrap(err, "list account from contract"))
	// 	return
	// }

	// for i, u := range users {
	// 	list = append(list, model.Account{
	// 		User:     u.String(),
	// 		Provider: providers[i].String(),
	// 		Balance:  balances[i],
	// 		// PendingRefund: ,

	// 	})
	// }
	// ctx.JSON(http.StatusOK, model.AccountList{
	// 	Metadata: model.ListMeta{Total: uint64(len(list))},
	// 	Items:    list,
	// })
}

func (h *Handler) GetUserAccount(ctx *gin.Context) {
	// list := []model.Account{}

	// callOpts := &bind.CallOpts{
	// 	Context: context.Background(),
	// }
	// users, providers, balances, err := h.contract.GetAllUserAccounts(callOpts)
	// if err != nil {
	// 	errors.Response(ctx, errors.Wrap(err, "list account from contract"))
	// 	return
	// }

	// for i, u := range users {
	// 	list = append(list, model.Account{
	// 		User:     u.String(),
	// 		Provider: providers[i].String(),
	// 		Balance:  balances[i],
	// 		// PendingRefund: ,

	// 	})
	// }
	// ctx.JSON(http.StatusOK, model.AccountList{
	// 	Metadata: model.ListMeta{Total: uint64(len(list))},
	// 	Items:    list,
	// })
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
