package handler

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-agent/common/errors"
	"github.com/0glabs/0g-serving-agent/provider/model"
)

func (h *Handler) RegisterService(ctx *gin.Context) {
	var service model.Service
	if err := service.Bind(ctx); err != nil {
		handleError(ctx, err, "bind service")
		return
	}
	switch service.Type {
	case "RPC":
	case "chatbot":
		h.proxy.AddHTTPRoute(service.Name, service.URL, service.Type)
	default:
		handleError(ctx, errors.New("invalid service type"), "register service")
		return
	}

	if err := h.ctrl.RegisterService(ctx, service); err != nil {
		h.proxy.DeleteRoute(service.Name)
		handleError(ctx, err, "register service")
		return
	}

	ctx.Status(http.StatusAccepted)
}

func (h *Handler) GetService(ctx *gin.Context) {
	name := ctx.Param("service")
	service, err := h.ctrl.GetService(name)
	if err != nil {
		handleError(ctx, err, "get service from db")
		return
	}

	ctx.JSON(http.StatusOK, service)
}

func (h *Handler) ListService(ctx *gin.Context) {
	list, err := h.ctrl.ListService()
	if err != nil {
		handleError(ctx, err, "list service")
		return
	}

	ctx.JSON(http.StatusOK, model.ServiceList{
		Metadata: model.ListMeta{Total: uint64(len(list))},
		Items:    list,
	})
}

func (h *Handler) UpdateService(ctx *gin.Context) {
	name := ctx.Param("service")

	var new model.Service
	if err := new.Bind(ctx); err != nil {
		handleError(ctx, err, "bind service")
		return
	}
	switch new.Type {
	case "RPC":
	case "chatbot":
		if err := h.proxy.UpdateRoute(name, new.URL, new.Type); err != nil {
			handleError(ctx, err, "update service route")
			return
		}
	default:
		handleError(ctx, errors.New("invalid service type"), "register service")
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
		handleError(ctx, err, "update service")
		return
	}

	ctx.Status(http.StatusAccepted)
}

func (h *Handler) DeleteService(ctx *gin.Context) {
	name := ctx.Param("service")

	if err := h.ctrl.DeleteService(ctx, name); err != nil {
		handleError(ctx, err, "delete service: "+name)
		return
	}

	h.proxy.DeleteRoute(name)

	ctx.Status(http.StatusAccepted)
}

func handleError(ctx *gin.Context, err error, context string) {
	errors.Response(ctx, errors.Wrap(err, "Provider: handle service, "+context))
}
