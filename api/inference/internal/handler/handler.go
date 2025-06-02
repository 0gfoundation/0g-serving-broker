package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/inference/internal/ctrl"
	"github.com/0glabs/0g-serving-broker/inference/internal/proxy"
	"github.com/sirupsen/logrus"
)

type Handler struct {
	ctrl   *ctrl.Ctrl
	proxy  *proxy.Proxy
	logger log.Logger
}

func New(ctrl *ctrl.Ctrl, proxy *proxy.Proxy, logger log.Logger) *Handler {
	return &Handler{
		ctrl:   ctrl,
		proxy:  proxy,
		logger: logger,
	}
}

func (h *Handler) Register(r *gin.Engine) {
	group := r.Group("/v1")

	// service
	group.GET("/service", h.GetService)

	// settle
	group.POST("/settle", h.SettleFees)

	// account
	group.GET("/user", h.ListUserAccount)
	group.GET("/user/:user", h.GetUserAccount)
	group.POST("sync-account", h.SyncUserAccounts)

	// request
	group.GET("/request", h.ListRequest)

	group.GET("/quote", h.GetQuote)
}

func (h *Handler) handleBrokerError(ctx *gin.Context, err error, context string) {
	info := "Provider"
	if context != "" {
		info += (": " + context)
	}
	h.logger.WithFields(logrus.Fields{
		"error":   err,
		"context": context,
	}).Error(info)
	errors.Response(ctx, errors.Wrap(err, info))
}
