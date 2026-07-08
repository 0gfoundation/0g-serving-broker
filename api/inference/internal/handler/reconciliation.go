package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Reconcile returns the broker's own usage for an upstream over a period (in the given
// timezone), so an operator can compare it against the upstream provider's statement
// themselves. Report-only: it does not take the vendor's numbers and does not judge a
// tolerance. Read-only; never mutates billing state.
//
// Auth reuses the admin whitelist gate (session token AND whitelisted), matching
// /admin/usage/daily.
//
//	@Description  Broker usage report for an upstream over a period (whitelist-gated)
//	@ID           reconcile
//	@Tags         admin
//	@Produce      json
//	@Param        upstream  query  string  false  "Upstream vendor label (e.g. minimax); omit for all upstreams"
//	@Param        start     query  string  true   "Period start date (YYYY-MM-DD, in timezone)"
//	@Param        end       query  string  true   "Period end date, inclusive (YYYY-MM-DD, in timezone)"
//	@Param        timezone  query  string  false  "Fixed UTC offset of the period (+08:00, -05:00, Z); defaults to UTC"
//	@Router       /admin/reconciliation [get]
//	@Success      200  {object}  ctrl.ReconciliationReport
func (h *Handler) Reconcile(ctx *gin.Context) {
	// 1. Authenticate the session (proves possession of the private key).
	authedUser, err := h.ctrl.ValidateSession(ctx)
	if err != nil {
		ctx.Set("ignoreError", true)
		handleBrokerError(ctx, err, "validate session")
		return
	}

	// 2. Authorize: only whitelisted addresses may read usage reports.
	if !h.ctrl.IsWhitelistedUser(authedUser) {
		ctx.JSON(http.StatusForbidden, gin.H{
			"error": "not authorized to read reconciliation reports",
		})
		return
	}

	start := ctx.Query("start")
	end := ctx.Query("end")
	if start == "" || end == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{
			"error": "start and end are required",
		})
		return
	}

	// upstream is optional: empty means "all upstreams" (grouped in the report).
	report, err := h.ctrl.Reconcile(ctx.Query("upstream"), start, end, ctx.Query("timezone"))
	if err != nil {
		// Errors are dominated by bad input (dates/timezone/period); echo with a 400.
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, report)
}
