package handler

import (
	"net/http"
	"regexp"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/errors"
)

// usageDailyDateRe enforces a strict YYYY-MM-DD calendar-date shape on the
// required date parameter (the value is interpolated into a MySQL DATE
// comparison via a bound parameter, but validating the shape rejects garbage
// early with a clear 400).
var usageDailyDateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

const (
	usageDailyDefaultLimit = 1000
	usageDailyMaxLimit     = 5000
)

// userDailyStatItem is one per-wallet, per-model row in the response.
type userDailyStatItem struct {
	UserAddress  string `json:"user_address"`
	Model        string `json:"model"`
	RequestCount int64  `json:"request_count"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
}

// GetUserDailyUsage returns per-wallet, per-model token usage for a single UTC
// date, for the direct (non-whitelisted) consumers of this provider's broker.
// The Router pulls this daily and materializes it into its own table.
//
// Auth reuses the existing whitelist gate: the caller must present a valid
// session token AND be whitelisted. See docs/wallet-direct-usage-design.md
// (router repo) §4.5 — this couples "bypass billing" with "read all wallets'
// usage"; acceptable for the internal/self-operated Router, revisit if any
// non-Router address is ever whitelisted for billing reasons.
//
//	@Description  Get per-wallet daily token usage for a UTC date (whitelist-gated)
//	@ID           getUserDailyUsage
//	@Tags         usage
//	@Produce      json
//	@Param        date    query  string  true   "UTC calendar date (YYYY-MM-DD)"
//	@Param        limit   query  int     false  "Page size (default 1000, max 5000)"
//	@Param        offset  query  int     false  "Page offset (default 0)"
//	@Router       /admin/usage/daily [get]
//	@Success      200  {object}  map[string]interface{}
func (h *Handler) GetUserDailyUsage(ctx *gin.Context) {
	// 1. Authenticate the session (proves possession of the private key).
	authedUser, err := h.ctrl.ValidateSession(ctx)
	if err != nil {
		ctx.Set("ignoreError", true)
		handleBrokerError(ctx, err, "validate session")
		return
	}

	// 2. Authorize: only whitelisted addresses may read all wallets' usage.
	if !h.ctrl.IsWhitelistedUser(authedUser) {
		ctx.JSON(http.StatusForbidden, gin.H{
			"error": "not authorized to read usage statistics",
		})
		return
	}

	date := ctx.Query("date")
	if !usageDailyDateRe.MatchString(date) {
		ctx.JSON(http.StatusBadRequest, gin.H{
			"error": "date is required and must be in YYYY-MM-DD format",
		})
		return
	}

	limit := usageDailyDefaultLimit
	if raw := ctx.Query("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": "limit must be a positive integer"})
			return
		}
		if v > usageDailyMaxLimit {
			v = usageDailyMaxLimit
		}
		limit = v
	}

	offset := 0
	if raw := ctx.Query("offset"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 0 {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": "offset must be a non-negative integer"})
			return
		}
		offset = v
	}

	rows, total, err := h.ctrl.ListUserDailyStat(date, limit, offset)
	if err != nil {
		// A DB failure here is a broker fault, not a client error: wrap as 500
		// so it is classified, logged, and sanitized (rather than defaulting to
		// 400 with a raw DB string, which the Router could mistake for a
		// permanent client error and skip retrying).
		handleBrokerError(ctx, errors.Internal(err), "list user daily usage")
		return
	}

	data := make([]userDailyStatItem, 0, len(rows))
	for _, r := range rows {
		data = append(data, userDailyStatItem{
			UserAddress:  r.UserAddress,
			Model:        r.Model,
			RequestCount: r.RequestCount,
			InputTokens:  r.InputTokens,
			OutputTokens: r.OutputTokens,
		})
	}

	ctx.JSON(http.StatusOK, gin.H{
		"object":           "list",
		"provider_address": h.ctrl.ProviderAddress(),
		"date":             date,
		"total":            total,
		"limit":            limit,
		"offset":           offset,
		"data":             data,
	})
}
