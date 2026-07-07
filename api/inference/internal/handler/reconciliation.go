package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/inference/internal/ctrl"
)

// Reconcile compares an upstream provider's billing statement against the broker's
// hourly usage rollup for the statement's period and timezone, returning a per-dimension
// diff. Read-only; never mutates billing state.
//
// Auth reuses the admin whitelist gate (session token AND whitelisted), matching
// /admin/usage/daily.
//
//	@Description  Reconcile a provider statement against broker usage (whitelist-gated)
//	@ID           reconcile
//	@Tags         admin
//	@Accept       json
//	@Produce      json
//	@Router       /admin/reconciliation [post]
//	@Success      200  {object}  ctrl.ReconciliationReport
func (h *Handler) Reconcile(ctx *gin.Context) {
	// 1. Authenticate the session (proves possession of the private key).
	authedUser, err := h.ctrl.ValidateSession(ctx)
	if err != nil {
		ctx.Set("ignoreError", true)
		handleBrokerError(ctx, err, "validate session")
		return
	}

	// 2. Authorize: only whitelisted addresses may run reconciliation.
	if !h.ctrl.IsWhitelistedUser(authedUser) {
		ctx.JSON(http.StatusForbidden, gin.H{
			"error": "not authorized to run reconciliation",
		})
		return
	}

	var in ctrl.ReconciliationInput
	if err := ctx.ShouldBindJSON(&in); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	report, err := h.ctrl.Reconcile(in)
	if err != nil {
		// Reconcile's errors are dominated by bad input (dates/timezone/period). This is
		// an Admin-facing endpoint, so echo the message with a 400 rather than
		// distinguishing the rarer DB-fault case.
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, report)
}
