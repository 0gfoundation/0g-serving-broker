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
		"relay_note": "What this saves you is the arguments, not the verification. Run assay.verify.command: it queries the chain and the attestation service directly, and pulls the assay's evidence from the tee_url the registry lists, so nothing in this response feeds the result. The broker can refuse to answer or serve a stale copy; it cannot make a false answer verify.",
	})
}
