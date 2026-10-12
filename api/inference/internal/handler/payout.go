package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/internal/ctrl"
)

// GetPayoutVoucher relays a GPU node's PayoutVoucher from the assay.
//
// GPU nodes have no route to the assay — it publishes nothing they can reach
// — so this is how a node that missed the assay's push collects its voucher.
// The broker forwards the assay's signed bytes untouched; it neither issues
// vouchers nor submits the claim, which the node still sends to the contract
// itself (see ctrl.RelayAssayVoucher for the full trust argument).
//
// Auth is the node's own payout key: it signs
//
//	assay-voucher-fetch-v1|<provider address, lowercase>|<unix seconds>
//
// and sends the signature in ZG-Node-Sig with the timestamp in ZG-Node-Ts.
// The recovered address selects the voucher, so a node can only ever read the
// one that pays it, and the broker needs no configured roster of nodes.
func (h *Handler) GetPayoutVoucher(ctx *gin.Context) {
	if !h.ctrl.AssayPayoutEnabled() {
		ctx.JSON(http.StatusNotFound, gin.H{
			"error": "assay payout is not enabled on this broker",
		})
		return
	}

	node, err := h.ctrl.AuthenticateVoucherFetch(
		ctx.GetHeader(constant.HeaderZGNodeTs),
		ctx.GetHeader(constant.HeaderZGNodeSig),
	)
	if err != nil {
		// Deliberately terse to the caller and logged in full here: the detail
		// helps the node's operator debug, and is not something to hand an
		// unauthenticated prober.
		h.logger.Warnf("Payout relay: rejected voucher fetch: %v", err)
		// The provider address is echoed because it is part of the payload the
		// node has to sign and is public on chain anyway — a node can discover
		// it here instead of carrying it in its own config. Signing whatever a
		// broker names is a slightly weaker binding than a pinned provider
		// address (it lets broker A mint a signature valid at broker B, which
		// reveals that node's earnings at B and nothing else), so claim.py
		// treats this as a convenience and says so.
		ctx.JSON(http.StatusUnauthorized, gin.H{
			"error": "voucher fetch must be signed by the node's payout key " +
				"(" + constant.HeaderZGNodeTs + " + " + constant.HeaderZGNodeSig + ")",
			"provider":       h.ctrl.ProviderAddress(),
			"payload_format": constant.AssayVoucherFetchDomain + "|<provider lowercase>|<unix seconds>",
		})
		return
	}

	entry, err := h.ctrl.RelayAssayVoucher(ctx.Request.Context(), node)
	if errors.Is(err, ctrl.ErrNoVoucher) {
		// Not an error condition: a node that has served work but has not been
		// invoiced yet lands here every time it polls.
		ctx.JSON(http.StatusNotFound, gin.H{
			"error": "the assay holds no voucher for " + node.Hex(),
			"node":  node.Hex(),
		})
		return
	}
	if err != nil {
		// 502, not 500: the failure is upstream at the assay, and the node
		// should retry rather than treat its own request as malformed.
		h.logger.Errorf("Payout relay: fetching voucher for %s failed: %v", node.Hex(), err)
		ctx.JSON(http.StatusBadGateway, gin.H{
			"error": "cannot reach the assay to fetch this voucher",
			"node":  node.Hex(),
		})
		return
	}

	ctx.JSON(http.StatusOK, entry)
}
