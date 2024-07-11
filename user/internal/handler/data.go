package handler

import (
	"context"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-agent/chatbot"
	"github.com/0glabs/0g-serving-agent/common/errors"
	commonModel "github.com/0glabs/0g-serving-agent/common/model"
	"github.com/0glabs/0g-serving-agent/user/internal/ctrl"
)

func (h *Handler) GetData(ctx *gin.Context) {
	provider := ctx.Param("provider")
	svcName := ctx.Param("service")
	suffix := ctx.Param("suffix")

	callOpts := &bind.CallOpts{
		Context: context.Background(),
	}
	// TODO: add a cache
	svc, err := h.contract.GetService(callOpts, common.HexToAddress(provider), svcName)
	if err != nil {
		errors.Response(ctx, errors.Wrap(err, "get service from contract"))
		return
	}

	var dataFetcher ctrl.DataFetcher
	switch svc.ServiceType {
	case "chatbot":
		dataFetcher = &chatbot.ChatBot{
			DataFetcherInfo: commonModel.DataFetcherInfo{
				Url:         svc.Url,
				User:        h.userAddress,
				Provider:    provider,
				ServiceName: svcName,
				QuerySuffix: suffix,
				PrivateKey:  h.key,
			},
			DB: h.db,
		}
	default:
		errors.Response(ctx, errors.New("known service type"))
	}
	h.ctrl.GetData(ctx, dataFetcher)
}
