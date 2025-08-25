package handler

import (
	"bytes"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

// GetQuote
//
//	@Description  This endpoint allows you to get a quote
//	@ID			getQuote
//	@Tags		quote
//	@Router		/quote [get]
//	@Success	200	{string}	string
func (h *Handler) GetQuote(ctx *gin.Context) {
	quote, err := h.ctrl.GetQuote(ctx)
	if err != nil {
		handleBrokerError(ctx, err, "read quote")
		return
	}

	ctx.String(http.StatusOK, quote)
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
