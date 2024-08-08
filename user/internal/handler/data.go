package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-agent/common/errors"
)

func (h *Handler) GetData(ctx *gin.Context) {
	providerAddress := ctx.Param("provider")
	svcName := ctx.Param("service")

	extractor, err := h.ctrl.GetExtractor(ctx, providerAddress, svcName)
	if err != nil {
		handleError(ctx, errors.Wrap(err, "get extractor"), "get data")
		return
	}

	// TODO: Check the balance from contract
	account, err := h.ctrl.IncreaseAccountNonce(providerAddress)
	if err != nil {
		handleError(ctx, errors.Wrap(err, "increase account nonce in db"), "get data")
		return
	}

	req, err := h.ctrl.PrepareRequest(ctx, extractor.GetSvcInfo(), account, extractor)
	if err != nil {
		handleError(ctx, errors.Wrap(err, "prepare request"), "get data")
		return
	}

	h.ctrl.ProcessRequest(ctx, req, extractor)
}
