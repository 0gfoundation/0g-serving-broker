package handler

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/provider/model"
)

// registerService
//
//	@ID			registerService
//	@Tags		service
//	@Router		/service [post]
//	@Param		body	body	model.Service	true	"body"
//	@Success	204		"No Content - success without response body"
func (h *Handler) RegisterService(ctx *gin.Context) {
	var service model.Service
	if err := service.Bind(ctx); err != nil {
		handleBrokerError(ctx, err, "bind service")
		return
	}
	switch service.Type {
	case "zgStorage", "chatbot", "image-generation":
		h.proxy.AddHTTPRoute(service.Name, service.URL, service.Type)
	default:
		handleBrokerError(ctx, errors.New("invalid service type"), "register service")
		return
	}
	svc, _ := h.ctrl.GetContractService(ctx, service.Name)
	if svc.Name != "" {
		handleBrokerError(ctx, errors.New("service already exists in contract"), "register service")
		return
	}
	if err := h.ctrl.RegisterService(ctx, service); err != nil {
		h.proxy.DeleteRoute(service.Name)
		handleBrokerError(ctx, err, "register service")
		return
	}

	ctx.Status(http.StatusNoContent)
}

// getService
//
//	@ID			getService
//	@Tags		service
//	@Router		/service/{service} [get]
//	@Param		service	path	string	true	"Service name"
//	@Success	200	{object}	model.Service
func (h *Handler) GetService(ctx *gin.Context) {
	name := ctx.Param("service")
	service, err := h.ctrl.GetService(name)
	if err != nil {
		handleBrokerError(ctx, err, "get service from db")
		return
	}

	ctx.JSON(http.StatusOK, service)
}

// listService
//
//	@ID			listService
//	@Tags		service
//	@Router		/service [get]
//	@Success	200	{object}	model.ServiceList
func (h *Handler) ListService(ctx *gin.Context) {
	list, err := h.ctrl.ListService()
	if err != nil {
		handleBrokerError(ctx, err, "list service")
		return
	}

	ctx.JSON(http.StatusOK, model.ServiceList{
		Metadata: model.ListMeta{Total: uint64(len(list))},
		Items:    list,
	})
}

// updateService
//
//	@ID			updateService
//	@Tags		service
//	@Router		/service/{service} [put]
//	@Param		service	path	string	true	"Service name"
//	@Param		body	body	model.Service	true	"body"
//	@Success	202
func (h *Handler) UpdateService(ctx *gin.Context) {
	name := ctx.Param("service")

	var new model.Service
	if err := new.Bind(ctx); err != nil {
		handleBrokerError(ctx, err, "bind service")
		return
	}
	old, err := h.ctrl.GetService(name)
	if err != nil {
		handleBrokerError(ctx, err, "get old service")
		return
	}
	if err := model.ValidateUpdateService(old, new); err != nil {
		handleBrokerError(ctx, err, "")
		return
	}
	switch new.Type {
	case "zgStorage", "chatbot", "image-generation":
		if err := h.proxy.UpdateRoute(name, new.URL, new.Type); err != nil {
			handleBrokerError(ctx, err, "update service route")
			return
		}
	default:
		handleBrokerError(ctx, errors.New("invalid service type"), "register service")
		return
	}
	if err := h.ctrl.UpdateService(ctx, new); err != nil {
		old, rollBackErr := h.ctrl.GetService(name)
		if rollBackErr != nil {
			log.Printf("rolling back operation in route: %s", rollBackErr.Error())
		}
		if rollBackErr := h.proxy.UpdateRoute(name, old.URL, old.Type); rollBackErr != nil {
			log.Printf("rolling back operation in route: %s", rollBackErr.Error())
		}
		handleBrokerError(ctx, err, "update service")
		return
	}

	ctx.Status(http.StatusAccepted)
}

// deleteService
//
//	@ID			deleteService
//	@Tags		service
//	@Router		/service/{service} [delete]
//	@Param		service	path	string	true	"Service name"
//	@Success	202
func (h *Handler) DeleteService(ctx *gin.Context) {
	name := ctx.Param("service")

	if err := h.ctrl.DeleteService(ctx, name); err != nil {
		handleBrokerError(ctx, err, "delete service: "+name)
		return
	}

	h.proxy.DeleteRoute(name)

	ctx.Status(http.StatusAccepted)
}

// syncServices
//
//	@Description  This endpoint allows you to synchronize all services from local database to the contract
//	@ID			syncServices
//	@Tags		service
//	@Router		/sync-service [post]
//	@Success	202
func (h *Handler) SyncServices(ctx *gin.Context) {
	if err := h.ctrl.SyncServices(ctx); err != nil {
		handleBrokerError(ctx, err, "synchronize service from the database to the contract")
		return
	}

	ctx.Status(http.StatusAccepted)
}
