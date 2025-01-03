package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/errors"
)

// getDataWithSuffix
//
// @Description  This endpoint acts as a proxy to retrieve data from various external services based on the provided `provider` and `service` parameters. The response type can vary depending on the external service being accessed. An optional `suffix` parameter can be appended to further specify the request for external services
// @ID           getDataWithSuffix
// @Tags         data
// @Router       /provider/{provider}/service/{service}/{suffix} [post]
// @Param        provider    path     string  true   "Provider address"
// @Param        service     path     string  true   "Service name"
// @Param        suffix      path     string  true  "Suffix"
// @Success      200  {string}  string             "Plain text response"
// @Success      200  {string}  binary             "Binary stream response"
func (h *Handler) GetDataWithSuffix(ctx *gin.Context) {
	providerAddress := ctx.Param("provider")
	svcName := ctx.Param("service")
	suffix := ctx.Param("suffix")
	h.getData(ctx, providerAddress, svcName, suffix)
}

// getData
//
// @Description  This endpoint allows you to retrieve data based on provider and service. This endpoint acts as a proxy to retrieve data from various external services. The response type can vary depending on the service being accessed
// @ID           getData
// @Tags         data
// @Router       /provider/{provider}/service/{service} [post]
// @Param        provider    path     string  true   "Provider address"
// @Param        service     path     string  true   "Service name"
// @Success      200  {string}  string             "Plain text response"
// @Success      200  {string}  binary             "Binary stream response"
func (h *Handler) GetData(ctx *gin.Context) {
	providerAddress := ctx.Param("provider")
	svcName := ctx.Param("service")
	h.getData(ctx, providerAddress, svcName, "")
}

func (h *Handler) getData(ctx *gin.Context, providerAddress, svcName, suffix string) {
	extractor, err := h.ctrl.GetExtractor(ctx, providerAddress, svcName)
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

	req, err := h.ctrl.PrepareRequest(ctx, extractor.GetSvcInfo(), account, extractor, suffix)
	if err != nil {
		handleBrokerError(ctx, errors.Wrap(err, "prepare request"), "get data")
		return
	}

	h.ctrl.ProcessRequest(ctx, req, extractor)
}

func (h *Handler) getChatCompletions(ctx *gin.Context) {
	providerAddress := h.presetProviderAddress
	svcName := h.serviceName
	h.getData(ctx, providerAddress, svcName, "/chat/completions")
}
