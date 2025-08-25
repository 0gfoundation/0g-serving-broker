package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/inference/internal/ctrl"
	"github.com/0glabs/0g-serving-broker/inference/internal/proxy"
)

type Handler struct {
	ctrl  *ctrl.Ctrl
	proxy *proxy.Proxy
}

func New(ctrl *ctrl.Ctrl, proxy *proxy.Proxy) *Handler {
	h := &Handler{
		ctrl:  ctrl,
		proxy: proxy,
	}
	return h
}

// corsMiddleware handles CORS for individual routes
func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE, PATCH")
		c.Writer.Header().Set("Access-Control-Max-Age", "86400")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}

func (h *Handler) Register(r *gin.Engine) {
	group := r.Group("/v1")

	// service
	group.GET("/service", corsMiddleware(), h.GetService)

	// settle
	group.POST("/settle", corsMiddleware(), h.SettleFees)

	// account
	group.GET("/user", corsMiddleware(), h.ListUserAccount)
	group.GET("/user/:user", corsMiddleware(), h.GetUserAccount)
	group.POST("sync-account", corsMiddleware(), h.SyncUserAccounts)

	// request
	group.GET("/request", corsMiddleware(), h.ListRequest)

	group.GET("/quote", corsMiddleware(), h.GetQuote)

	//nvidia TEE verification
	group.POST("/quote/verify/gpu", corsMiddleware(), h.VerifyGPU)
	group.OPTIONS("/quote/verify/gpu", corsMiddleware(), func(c *gin.Context) {
		c.Status(204)
	})
}

func handleBrokerError(ctx *gin.Context, err error, context string) {
	info := "Provider"
	if context != "" {
		info += (": " + context)
	}
	errors.Response(ctx, errors.Wrap(err, info))
}
