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

// GetSpmlScan
//
//	@Description  The broker's own tappscan-backed check of the spml, signed by
//	@Description  the TEE key so it can be carried away and verified against the
//	@Description  chain (spml repo docs/spml-design.md §7.7).
//	@ID			getSpmlScan
//	@Tags		attestation
//	@Router		/attestation/spml/scan [get]
//	@Success	200
func (h *Handler) GetSpmlScan(ctx *gin.Context) {
	body, sig, err := h.ctrl.SpmlScanStatement(ctx.Query("nonce"))
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

// GetSpmlAttestation
//
//	@Description  Relays the spml verifier's identity document so a client can
//	@Description  reconcile it against the chain and tappscan without trusting
//	@Description  this broker.
//	@ID			getSpmlAttestation
//	@Tags		attestation
//	@Router		/attestation/spml [get]
//	@Success	200
func (h *Handler) GetSpmlAttestation(ctx *gin.Context) {
	// ?fresh=1 bypasses the relay cache (rate-limited in the controller). The
	// caller who just redeployed the spml, or who does not want to reason
	// about fetched_at, can ask for the document as it is now.
	fresh := ctx.Query("fresh") == "1"
	body, fetchedAt, err := h.ctrl.SpmlAttestation(ctx.Request.Context(), fresh)
	if err != nil {
		// 502, not 500: the failure is upstream of us, and saying so lets an
		// auditor tell "the broker is broken" from "the broker will not show
		// me the spml" — the second is the one worth escalating.
		ctx.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	// The spml's document verbatim under `spml`, our fetch time beside it.
	// Kept separate on purpose: we must not be able to alter a field the client
	// is going to reconcile against the chain, and nesting makes that visible.
	// What we got when we ran the same verification ourselves. Included because
	// it costs nothing (the loop runs anyway, to gate our own settlement) and
	// because its FAILURE is credible: we would be admitting the money path is
	// blocked. Its success is not evidence — see SpmlAttestationSnapshot.
	out := gin.H{
		"spml":       json.RawMessage(body),
		"relayed_by":  "broker",
		"fetched_at":  fetchedAt.Format(time.RFC3339),
		"cached_note": "This document is cached for up to 10 minutes and fetched_at says when we took it; add ?fresh=1 to make us re-fetch now (rate-limited). The cache is here because the spml's port is reachable only through us, so an uncached public endpoint would be an amplifier pointed at it.",
		"relay_note":  "What this saves you is the arguments, not the verification. Fetch spml.verify.scan_record yourself, pinned to spml.verify.params.scan_pubkey_pin, and check it against spml.verify.expect: the record is tappscan's and the reconciliation is the chain's, so nothing in this response feeds the result. The broker can refuse to answer or serve a stale copy; it cannot make a false answer verify.",
	}
	if h.ctrl.SpmlScanEnabled() {
		out["scan_endpoint"] = "/v1/attestation/spml/scan"
		out["scan_note"] = "Our tappscan-backed check of the spml, signed by our TEE key (header ZG-Scan-Signature, personal_sign over keccak256 of the body; recover it to getService(provider).teeSignerAddress). It embeds tappscan's raw record and the two values we fetched on our own — the signer the TappRegistry lists and the TLS key the spml serves right now. Pass ?nonce=<hex> to have it signed for this request."
	}
	if snap := h.ctrl.SpmlAttestationStatus(); snap != nil {
		out["broker_own_check"] = snap
		out["broker_own_check_note"] = "Our own run of the same verification. A failure here is worth acting on — the identical result gates our settlement and invoicing, so we lose money by reporting it. A pass is only a hint: we are reporting on ourselves. Fetch spml.verify.scan_record yourself to get an answer that does not depend on us."
	} else {
		out["broker_own_check"] = nil
		out["broker_own_check_note"] = "This broker is not running the attestation loop, so it verifies the spml for itself not at all — and neither gates its settlement on it. Fetch spml.verify.scan_record yourself."
	}
	ctx.JSON(http.StatusOK, out)
}
