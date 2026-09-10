package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/inference/internal/ctrl"
)

const scanSignatureHeader = "ZG-Scan-Signature"

// GetAssayScan
//
//	@Description  The broker's own tappscan-backed check of the assay, signed by
//	@Description  the TEE key so it can be carried away and verified against the
//	@Description  chain (docs/spml-attestation-relay.md).
//	@ID			getAssayScan
//	@Tags		attestation
//	@Router		/attestation/assay/scan [get]
//	@Success	200
func (h *Handler) GetAssayScan(ctx *gin.Context) {
	body, sig, err := h.ctrl.AssayScanStatement(ctx.Query("nonce"))
	switch {
	case errors.Is(err, ctrl.ErrScanBadNonce):
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	case errors.Is(err, ctrl.ErrScanNotReady):
		ctx.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		return
	case err != nil:
		ctx.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if sig != nil {
		ctx.Header(scanSignatureHeader, hexutil.Encode(sig))
	}
	ctx.Data(http.StatusOK, "application/json", body)
}

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
	// What we got when we ran the same verification ourselves. Included because
	// it costs nothing (the loop runs anyway, to gate our own settlement) and
	// because its FAILURE is credible: we would be admitting the money path is
	// blocked. Its success is not evidence — see AssayAttestationSnapshot.
	out := gin.H{
		"assay":      json.RawMessage(body),
		"relayed_by": "broker",
		"fetched_at": fetchedAt.Format(time.RFC3339),
		"relay_note": "What this saves you is the arguments, not the verification. Run assay.verify.command: it queries the chain and the attestation service directly, and pulls the assay's evidence from the tee_url the registry lists, so nothing in this response feeds the result. The broker can refuse to answer or serve a stale copy; it cannot make a false answer verify.",
	}
	if h.ctrl.AssayScanEnabled() {
		out["scan_endpoint"] = "/v1/attestation/assay/scan"
		out["scan_note"] = "Our tappscan-backed check of the assay, signed by our TEE key (header ZG-Scan-Signature, personal_sign over keccak256 of the body; recover it to getService(provider).teeSignerAddress). It embeds tappscan's raw record and the two values we fetched on our own — the signer the TappRegistry lists and the TLS key the assay serves right now. Pass ?nonce=<hex> to have it signed for this request."
	}
	if snap := h.ctrl.AssayAttestationStatus(); snap != nil {
		out["broker_own_check"] = snap
		out["broker_own_check_note"] = "Our own run of the same verification. A failure here is worth acting on — the identical result gates our settlement and invoicing, so we lose money by reporting it. A pass is only a hint: we are reporting on ourselves. Run assay.verify.command to get an answer that does not depend on us."
	} else {
		out["broker_own_check"] = nil
		out["broker_own_check_note"] = "This broker is not running the attestation loop, so it verifies the assay for itself not at all — and neither gates its settlement on it. Run assay.verify.command yourself."
	}
	ctx.JSON(http.StatusOK, out)
}
