package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/errors"
)

// getDataWithSuffix
//
// @Description  This endpoint acts as a proxy to retrieve data from various external services based on the provided `provider` parameter. The response type can vary depending on the external service being accessed. An optional `suffix` parameter can be appended to further specify the request for external services
// @ID           getDataWithSuffix
// @Tags         data
// @Router       /provider/{provider}/service/{service}/{suffix} [post]
// @Param        provider    path     string  true   "Provider address"
// @Param        suffix      path     string  true  "Suffix"
// @Success      200  {string}  string             "Plain text response"
// @Success      200  {string}  binary             "Binary stream response"
func (h *Handler) GetDataWithSuffix(ctx *gin.Context) {
	providerAddress := ctx.Param("provider")
	suffix := ctx.Param("suffix")
	h.getData(ctx, providerAddress, suffix, "", nil)
}

// getData
//
// @Description  This endpoint allows you to retrieve data based on provider and service. This endpoint acts as a proxy to retrieve data from various external services. The response type can vary depending on the service being accessed
// @ID           getData
// @Tags         data
// @Router       /provider/{provider} [post]
// @Param        provider    path     string  true   "Provider address"
// @Success      200  {string}  string             "Plain text response"
// @Success      200  {string}  binary             "Binary stream response"
func (h *Handler) GetData(ctx *gin.Context) {
	providerAddress := ctx.Param("provider")
	h.getData(ctx, providerAddress, "", "", nil)
}

func (h *Handler) getData(ctx *gin.Context, providerAddress, suffix, signingAddress string, reqBody map[string]interface{}) {
	extractor, err := h.ctrl.GetExtractor(ctx, providerAddress)
	if err != nil {
		handleBrokerError(ctx, errors.Wrap(err, "get extractor"), "get data")
		return
	}

	// TODO: Check the balance from contract
	account, err := h.ctrl.IncreaseAccountNonce(providerAddress)
	if err != nil {
		handleBrokerError(ctx, errors.Wrap(err, "increase account nonce in db"), "get data")
		return
	}

	req, err := h.ctrl.PrepareRequest(ctx, extractor.GetSvcInfo(), account, extractor, suffix, reqBody)
	if err != nil {
		handleBrokerError(ctx, errors.Wrap(err, "prepare request"), "get data")
		return
	}

	h.ctrl.ProcessRequest(ctx, req, extractor, signingAddress)
}

// All preset services should implement interfaces for compute network TEE service requirements.
func (h *Handler) getChatCompletions(ctx *gin.Context) {
	providerAddress := h.presetProviderAddress

	var reqBody map[string]interface{}

	if err := ctx.ShouldBindJSON(&reqBody); err != nil {
		handleBrokerError(ctx, errors.Wrap(err, "bind JSON"), "get chat completions")
		return
	}
	if _, ok := reqBody["model"].(string); !ok {
		handleBrokerError(ctx, errors.New("model is required"), "get chat completions")
		return
	}

	signingAddress, err := h.ctrl.GetSigningAddress(ctx, providerAddress, reqBody["model"].(string))
	if err != nil {
		handleBrokerError(ctx, err, "get signing address")
		return
	}

	h.getData(ctx, providerAddress, "/chat/completions", signingAddress, reqBody)
}

func (h *Handler) GetAttestationReport(ctx *gin.Context) {
	model := ctx.Query("model")
	if model == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "Model parameter is required"})
		return
	}

	providerAddress := h.presetProviderAddress

	body, err := h.ctrl.FetchAttestationReport(ctx, providerAddress, model)
	if err != nil {
		handleBrokerError(ctx, err, "fetch attestation report")
		return
	}

	for k, v := range ctx.Request.Header {
		ctx.Writer.Header().Set(k, v[0])
	}

	if _, err := ctx.Writer.Write(body); err != nil {
		handleBrokerError(ctx, err, "write response body")
	}
}
