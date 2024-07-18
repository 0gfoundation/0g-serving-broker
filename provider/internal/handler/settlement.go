package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-agent/common/contract"
	"github.com/0glabs/0g-serving-agent/common/errors"
	"github.com/0glabs/0g-serving-agent/common/model"
	"github.com/0glabs/0g-serving-agent/common/util"
)

func (h *Handler) SettleFees(ctx *gin.Context) {
	// TODO: remove limit after the add zk
	reqs := []model.Request{}
	ret := h.db.Model(model.Request{}).
		Where("processed = ?", false).
		Order("nonce ASC").Limit(5).Find(&reqs)
	if ret.Error != nil {
		errors.Response(ctx, errors.Wrap(ret.Error, "list request in db"))
		return
	}

	categorizedTraces := make(map[string]*contract.RequestTrace)
	for _, req := range reqs {
		cReq, err := util.ToContractRequest(req)
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

	_, err = h.contract.WaitForReceipt(tx.Hash(), true)
	if err != nil {
		errors.Response(ctx, err)
		return
	}

	ret = h.db.Model(&model.Request{}).
		Where("processed = ?", false).
		Updates(model.Request{Processed: true})

	if ret.Error != nil {
		errors.Response(ctx, err)
		return
	}

	ctx.Status(http.StatusAccepted)
}
