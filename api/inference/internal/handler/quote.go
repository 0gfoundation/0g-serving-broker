package handler

import (
	"bytes"
	"io"
	"net/http"
	"strconv"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/gin-gonic/gin"
)

// quoteSignatureHeader carries the signature over the quote response body. Must stay in
// the CORS Access-Control-Expose-Headers list in handler.go: a browser caller that cannot
// read it is told, by doc/attestation-trust-chain.md, that nvidia_payload says nothing.
const quoteSignatureHeader = "ZG-Quote-Signature"

// GetQuote
//
//	@Description  This endpoint allows you to get a quote
//	@ID			getQuote
//	@Tags		quote
//	@Param		legacy	query	bool	false	"Return the legacy ASCII signer-address report_data quote (default true). Pass legacy=false for the §4.2 enc_pub-binding quote."
//	@Router		/quote [get]
//	@Success	200	{string}	string
func (h *Handler) GetQuote(ctx *gin.Context) {
	legacy := true
	if v := ctx.Query("legacy"); v != "" {
		if parsed, err := strconv.ParseBool(v); err == nil {
			legacy = parsed
		}
	}

	quote, err := h.ctrl.GetQuote(ctx, legacy)
	if err != nil {
		handleBrokerError(ctx, err, "read quote")
		return
	}

	// Signed with the key report_data binds, over exactly these bytes. See
	// ctrl.QuoteSignature for why: nvidia_payload rides in this body and has no
	// self-authentication of its own, so without this a caller cannot tell the measured
	// image's GPU evidence from evidence substituted in transit. Computed at sync time,
	// not here — this endpoint is unauthenticated and the body is a cached constant.
	if sig := h.ctrl.QuoteSignature(legacy); sig != nil {
		ctx.Header(quoteSignatureHeader, hexutil.Encode(sig))
	}

	ctx.String(http.StatusOK, quote)
}

// GetEncKey
//
//	@Description  Returns the enclave HPKE encryption key for E2EE request sealing
//	@ID			getEncKey
//	@Tags		quote
//	@Produce	json
//	@Router		/e2ee/pubkey [get]
//	@Success	200	{object}	ctrl.EncKeyInfo
func (h *Handler) GetEncKey(ctx *gin.Context) {
	info, ok := h.ctrl.GetEncKey(ctx)
	if !ok {
		ctx.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "enclave encryption key not available yet",
		})
		return
	}
	ctx.JSON(http.StatusOK, info)
}

// VerifyGPU proxies attestation report verification to NVIDIA's service
//
//	@Description  This endpoint proxies attestation report verification requests to NVIDIA's TEE verification service
//	@ID			VerifyGPU
//	@Tags		proxy
//	@Accept		json
//	@Produce	json
//	@Param		request	body		object	true	"Raw request body to forward"
//	@Router		/quote/verify/gpu [post]
func (h *Handler) VerifyGPU(ctx *gin.Context) {
	// Read the raw request body
	body, err := io.ReadAll(ctx.Request.Body)
	if err != nil {
		handleBrokerError(ctx, err, "read request body")
		return
	}

	// Make request to NVIDIA's attestation service
	nvidiaURL := "https://nras.attestation.nvidia.com/v3/attest/gpu"
	nvidiaReq, err := http.NewRequest("POST", nvidiaURL, bytes.NewBuffer(body))
	if err != nil {
		handleBrokerError(ctx, err, "create NVIDIA request")
		return
	}

	// Forward only necessary headers, excluding CORS and browser-specific headers
	allowedHeaders := map[string]bool{
		"Content-Type":   true,
		"Accept":         true,
		"Authorization":  true,
		"User-Agent":     true,
		"Content-Length": true,
	}

	for name, values := range ctx.Request.Header {
		if allowedHeaders[name] {
			for _, value := range values {
				nvidiaReq.Header.Add(name, value)
			}
		}
	}

	client := &http.Client{}
	resp, err := client.Do(nvidiaReq)
	if err != nil {
		handleBrokerError(ctx, err, "send request to NVIDIA")
		return
	}
	defer resp.Body.Close()

	// Read the response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		handleBrokerError(ctx, err, "read NVIDIA response")
		return
	}

	// Return the proxy result directly without additional processing
	ctx.Data(resp.StatusCode, resp.Header.Get("Content-Type"), respBody)
}
