package handler

import (
	"net/http"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"
)

// // listUserAccount
// //
// //	@Description	This endpoint allows you to list all users who have created accounts for your service
// //	@ID			listUserAccount
// //	@Tags		user
// //	@Router		/user [get]
// //	@Success	200	{object}	model.UserList
// func (h *Handler) ListUserAccount(ctx *gin.Context) {
// 	list, err := h.ctrl.ListUserAccount(ctx, true)
// 	if err != nil {
// 		handleBrokerError(ctx, err, "list accounts")
// 		return
// 	}

// 	ctx.JSON(http.StatusOK, model.UserList{
// 		Metadata: model.ListMeta{Total: uint64(len(list))},
// 		Items:    list,
// 	})
// }

// // getUserAccount
// //
// //	@Description	This endpoint allows you to get account by user address
// //	@ID			getUserAccount
// //	@Tags		user
// //	@Router		/user/{user} [get]
// //	@Param		user	path	string	true	"User address"
// //	@Success	200	{object}	model.User
// func (h *Handler) GetUserAccount(ctx *gin.Context) {
// 	userAddress := ctx.Param("user")
// 	account, err := h.ctrl.GetUserAccount(ctx, common.HexToAddress(userAddress))
// 	if err != nil {
// 		handleBrokerError(ctx, err, "get account from db")
// 		return
// 	}

// 	ctx.JSON(http.StatusOK, account)
// }

// GetUnsettledFee returns the total unsettled fee for the authenticated user.
// The caller must provide a valid session token (Authorization header).
// The user address in the URL path must match the authenticated user.
//
//	@Description  Get the total unsettled fee for a specific user
//	@ID           getUnsettledFee
//	@Tags         user
//	@Produce      json
//	@Param        userAddress  path  string  true  "User address (0x...)"
//	@Router       /user/{userAddress}/unsettledfee [get]
//	@Success      200  {object}  map[string]string
func (h *Handler) GetUnsettledFee(ctx *gin.Context) {
	// Validate session token to get authenticated user address
	authedUser, err := h.ctrl.ValidateSession(ctx)
	if err != nil {
		ctx.Set("ignoreError", true)
		handleBrokerError(ctx, err, "validate session")
		return
	}

	// Verify the authenticated user matches the requested user address
	requestedUser := ctx.Param("userAddress")
	if !strings.EqualFold(authedUser, requestedUser) {
		ctx.JSON(http.StatusForbidden, gin.H{
			"error": "you can only query your own unsettled fee",
		})
		return
	}

	unsettledFee, err := h.ctrl.GetUnsettledFee(authedUser)
	if err != nil {
		handleBrokerError(ctx, err, "calculate unsettled fee")
		return
	}

	ctx.JSON(http.StatusOK, gin.H{
		"userAddress":  authedUser,
		"unsettledFee": unsettledFee.String(),
	})
}

// syncUserAccounts
//
//	@Description  This endpoint allows you to synchronize information of all accounts from the contract
//	@ID			syncUserAccounts
//	@Tags		user
//	@Router		/sync-account [post]
//	@Success	202
func (h *Handler) SyncUserAccounts(ctx *gin.Context) {
	if err := h.ctrl.SyncUserAccounts(ctx); err != nil {
		handleBrokerError(ctx, err, "synchronize accounts from the contract to the database")
		return
	}

	ctx.Status(http.StatusAccepted)
}

// syncUserAccount
//
//	@Description  This endpoint allows you to synchronize information of single account from the contract
//	@ID			syncUserAccount
//	@Tags		user
//	@Router		/user/{user}/sync [post]
//	@Param		user	path	string	true	"User address"
//	@Success	202
func (h *Handler) SyncUserAccount(ctx *gin.Context) {
	userAddress := ctx.Param("user")
	if err := h.ctrl.SyncUserAccount(ctx, common.HexToAddress(userAddress)); err != nil {
		handleBrokerError(ctx, err, "synchronize account from the contract to the database")
		return
	}

	ctx.Status(http.StatusAccepted)
}
