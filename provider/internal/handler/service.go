package handler

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-agent/common/errors"
	"github.com/0glabs/0g-serving-agent/common/util"
	"github.com/0glabs/0g-serving-agent/provider/model"
)

func (h *Handler) RegisterService(ctx *gin.Context) {
	var service model.Service
	if err := service.Bind(ctx); err != nil {
		errors.Response(ctx, err)
		return
	}

	switch service.Type {
	case "RPC":
		//  TODO: Add proxy.AddRPCRoute
	case "chatbot":
		h.proxy.AddHTTPRoute(service.Name, service.URL, service.Type)
	default:
		errors.Response(ctx, errors.New("invalid service type"))
		return
	}

	if ret := h.db.Create(&service); ret.Error != nil {
		errors.Response(ctx, errors.Wrap(ret.Error, "create service in db"))
		return
	}

	doFunc := func() error {
		opt, err := h.contract.CreateTransactOpts()
		if err != nil {
			return errors.Wrap(err, "create transact opts")
		}
		tx, err := h.contract.AddOrUpdateService(
			opt,
			service.Name,
			service.Type,
			h.servingUrl,
			util.ToBigInt(service.InputPrice),
			util.ToBigInt(service.OutputPrice),
		)
		if err != nil {
			return errors.Wrap(err, "add service")
		}
		_, err = h.contract.WaitForReceipt(ctx, tx.Hash())
		if err != nil {
			return errors.Wrap(err, "add service")
		}

		return errors.Wrap(err, "add service")
	}
	if err := doFunc(); err != nil {
		log.Println("failed to add service, rolling back...")
		h.proxy.DeleteRoute(service.Name)
		errRollback := h.db.Where("name = ?", service.Name).Delete(&model.Service{})
		log.Printf("rollback result: %v", errRollback)
		errors.Response(ctx, err)
		return
	}

	ctx.Status(http.StatusAccepted)
}

func (h *Handler) ListService(ctx *gin.Context) {
	list := []model.Service{}
	if ret := h.db.Model(model.Service{}).Order("created_at DESC").Find(&list); ret.Error != nil {
		errors.Response(ctx, errors.Wrap(ret.Error, "list service in db"))
		return
	}

	ctx.JSON(http.StatusOK, model.ServiceList{
		Metadata: model.ListMeta{Total: uint64(len(list))},
		Items:    list,
	})
}

func (h *Handler) DeleteService(ctx *gin.Context) {
	name := ctx.Param("name")
	opt, err := h.contract.CreateTransactOpts()
	if err != nil {
		errors.Response(ctx, err)
		return
	}

	_, err = h.contract.RemoveService(opt, name)
	if err != nil {
		errors.Response(ctx, err)
		return
	}

	ret := h.db.Where("name = ?", name).Delete(&model.Service{})
	if ret.Error != nil {
		errors.Response(ctx, errors.Wrapf(ret.Error, "delete service %s in db", name))
		return
	}

	h.proxy.DeleteRoute(name)

	ctx.Status(http.StatusAccepted)
}
