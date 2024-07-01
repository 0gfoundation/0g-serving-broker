package handler

import (
	"log"
	"math/big"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-data-retrieve-agent/internal/contract"
	"github.com/0glabs/0g-data-retrieve-agent/internal/errors"
	"github.com/0glabs/0g-data-retrieve-agent/internal/model"
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
	case "HTTP":
		h.proxy.AddHTTPRoute(service.Name, service.URL)
	default:
		errors.Response(ctx, errors.New("invalid service type"))
		return
	}

	if ret := h.db.Create(&service); ret.Error != nil {
		errors.Response(ctx, errors.Wrap(ret.Error, "create service in db"))
		return
	}

	doFunc := func() error {
		_, err := h.contract.AddOrUpdateService(
			h.contract.CreateTransactOpts(),
			service.Name,
			service.Type,
			h.servingUrl,
			toBigInt(service.InputPrice),
			toBigInt(service.OutputPrice),
		)
		return errors.Wrap(err, "add service")
	}
	if err := doFunc(); err != nil {
		log.Println("failed to add service, rolling back...")
		h.proxy.DeleteRoute(service.Name)
		errRollback := h.db.Delete(&model.Service{}, service.Name)
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
	ret := h.db.Where("name = ?", name).Delete(&model.Service{})
	if ret.Error != nil {
		errors.Response(ctx, errors.Wrapf(ret.Error, "delete service %s in db", name))
		return
	}

	_, err := h.contract.RemoveService(h.contract.CreateTransactOpts(), name)
	if err != nil {
		errors.Response(ctx, err)
		return
	}
	h.proxy.DeleteRoute(name)

	ctx.Status(http.StatusAccepted)
}

func (h *Handler) SettleFees(ctx *gin.Context) {
	reqs := []model.Request{}
	ret := h.db.Model(model.Request{}).
		Where("processed = ?", false).
		Order("nonce ASC").Find(&reqs)
	if ret.Error != nil {
		errors.Response(ctx, errors.Wrap(ret.Error, "list request in db"))
		return
	}

	categorizedTraces := make(map[string]*contract.RequestTrace)
	for _, req := range reqs {
		cReq, err := contract.ConvertFromDB(req)
		if err != nil {
			errors.Response(ctx, err)
			return
		}
		_, ok := categorizedTraces[req.UserAddress]
		if ok {
			categorizedTraces[req.UserAddress].Requests = append(categorizedTraces[req.UserAddress].Requests, cReq)
			continue
		}
		categorizedTraces[req.UserAddress] = &contract.RequestTrace{
			Requests: []contract.Request{cReq},
		}
	}

	traces := []contract.RequestTrace{}
	for _, t := range categorizedTraces {
		traces = append(traces, *t)
	}

	tx, err := h.contract.SettleFees(h.contract.CreateTransactOpts(), traces)
	if err != nil {
		errors.Response(ctx, err)
		return
	}

	reci, err := h.contract.WaitForReceipt(tx.Hash(), true)
	if err != nil {
		errors.Response(ctx, err)
		return
	}
	log.Println("reci", reci)

	ret = h.db.Model(&model.Request{}).
		Where("processed = ?", false).
		Updates(model.Request{Processed: true})

	if ret.Error != nil {
		errors.Response(ctx, err)
		return
	}

	ctx.Status(http.StatusAccepted)
}

func toBigInt(in int64) *big.Int {
	ret := big.NewInt(0)
	ret.SetInt64(in)
	return ret
}
