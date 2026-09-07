package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// GetAssayAttestation
//
//	@Description  Relays the assay verifier's identity document so a client can
//	@Description  reconcile it against the chain and tappscan without trusting
//	@Description  this broker.
//	@ID			getAssayAttestation
//	@Tags		attestation
//	@Router		/attestation/assay [get]
//	@Success	200
func (h *Handler) GetAssayAttestation(ctx *gin.Context) {
	body, fetchedAt, err := h.ctrl.AssayAttestation(ctx.Request.Context())
	if err != nil {
		// 502, not 500: the failure is upstream of us, and saying so lets an
		// auditor tell "the broker is broken" from "the broker will not show
		// me the assay" — the second is the one worth escalating.
		ctx.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	// The assay's document verbatim under `assay`, our fetch time beside it.
	// Kept separate on purpose: we must not be able to alter a field the client
	// is going to reconcile against the chain, and nesting makes that visible.
	ctx.JSON(http.StatusOK, gin.H{
		"assay":      json.RawMessage(body),
		"relayed_by": "broker",
		"fetched_at": fetchedAt.Format(time.RFC3339),
		"relay_note": "The broker is a transparent pipe here. It can refuse or serve a stale copy; it cannot forge this document into one the chain will agree with. Reconcile `assay.signer` against TappRegistry yourself, and ecrecover a recent ZG-Verdict-Sig to confirm that key is serving you now.",
	})
}
