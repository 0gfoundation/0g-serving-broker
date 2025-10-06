package proxy

import (
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-contrib/cors"
	"github.com/google/uuid"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/log"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/internal/ctrl"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

type Proxy struct {
	ctrl   *ctrl.Ctrl
	logger log.Logger

	allowOrigins      []string
	serviceRoutesLock sync.RWMutex
	serviceTarget     string
	serviceType       string
	serviceGroup      *gin.RouterGroup
}

func New(ctrl *ctrl.Ctrl, engine *gin.Engine, allowOrigins []string, enableMonitor bool, logger log.Logger) *Proxy {
	// Ensure allowOrigins is not empty
	if len(allowOrigins) == 0 {
		allowOrigins = []string{"*"}
	}

	p := &Proxy{
		allowOrigins: allowOrigins,
		ctrl:         ctrl,
		logger:       logger,
		serviceGroup: engine.Group(constant.ServicePrefix),
	}

	p.serviceGroup.Use(cors.New(cors.Config{
		AllowOrigins: p.allowOrigins,
		AllowMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"},
		AllowHeaders: []string{"*"},
	}))

	if enableMonitor {
		p.serviceGroup.Use(monitor.TrackMetrics())
	}

	return p
}

func (p *Proxy) Start() error {
	switch p.ctrl.Service.Type {
	case "zgStorage", "chatbot":
		p.AddHTTPRoute(p.ctrl.Service.TargetURL, p.ctrl.Service.Type)
	default:
		return errors.New("invalid service type")
	}
	return nil
}

func (p *Proxy) AddHTTPRoute(targetURL, svcType string) {
	//TODO: Add a URL validation
	exists := p.serviceTarget == targetURL

	p.serviceRoutesLock.Lock()
	p.serviceTarget = targetURL
	p.serviceType = svcType
	p.serviceRoutesLock.Unlock()

	if exists {
		return
	}

	h := func(ctx *gin.Context) {
		p.proxyHTTPRequest(ctx)
	}
	p.serviceGroup.Any("*any", h)
}

func (p *Proxy) proxyHTTPRequest(ctx *gin.Context) {
	p.serviceRoutesLock.RLock()
	targetURL := p.serviceTarget
	svcType := p.serviceType
	p.serviceRoutesLock.RUnlock()

	targetRoute := strings.TrimPrefix(ctx.Request.RequestURI, constant.ServicePrefix)
	if targetRoute != "/" {
		targetURL += targetRoute
	}
	reqBody, err := io.ReadAll(ctx.Request.Body)
	if err != nil {
		p.handleBrokerError(ctx, err, "read request body")
		return
	}

	// handle endpoints not need to be charged
	if _, ok := constant.TargetRoute[targetRoute]; !ok {
		if p.handleSignatureRoute(ctx, targetRoute) {
			return
		}

		httpReq, err := p.ctrl.PrepareHTTPRequest(ctx, targetURL, reqBody)
		if err != nil {
			p.handleBrokerError(ctx, err, "prepare HTTP request")
			return
		}
		p.ctrl.ProcessHTTPRequest(ctx, svcType, httpReq, model.Request{}, 0, false)
		return
	}
	req, err := p.ctrl.GetFromHTTPRequest(ctx)
	if err != nil {
		p.handleBrokerError(ctx, err, "get model.request from HTTP request")
		return
	}

	var expectedInputFee string
	var inputCount int64
	switch svcType {
	case "zgStorage":
		expectedInputFee = "0"
		inputCount = 0
	case "chatbot":
		expectedInputFee, inputCount, err = p.ctrl.GetChatbotInputFeeAndCount(reqBody)
		if err != nil {
			p.handleBrokerError(ctx, err, "get input fee and count")
			return
		}
	default:
		p.handleBrokerError(ctx, errors.New("unknown service type"), "prepare request extractor")
		return
	}

	req.InputFee = expectedInputFee
	req.Fee = req.InputFee
	req.InputCount = inputCount
	req.OutputCount = 0 // Will be updated when response is processed
	req.Nonce = uuid.New().String()
	req.RequestHash = req.Nonce

	if err := p.ctrl.ValidateRequest(ctx, req); err != nil {
		p.handleBrokerError(ctx, err, "validate request")
		return
	}
	if err := p.ctrl.CreateRequest(req); err != nil {
		p.handleBrokerError(ctx, err, "create request")
		return
	}

	httpReq, err := p.ctrl.PrepareHTTPRequest(ctx, targetURL, reqBody)
	if err != nil {
		p.handleBrokerError(ctx, err, "prepare HTTP request")
		return
	}

	if err := p.ctrl.ProcessHTTPRequest(ctx, svcType, httpReq, req, p.ctrl.Service.OutputPrice, true); err != nil {
		p.logger.Errorf("process http request failed: %v", err)
	}
}

func (p *Proxy) handleSignatureRoute(ctx *gin.Context, targetRoute string) bool {
	if !strings.HasPrefix(strings.ToLower(targetRoute), "/signature/") {
		return false
	}

	vllmProxy := ctx.GetHeader("VLLM-Proxy")
	relativePath := strings.ToLower(ctx.Param("any"))
	chatID := strings.TrimPrefix(relativePath, "/signature/")

	if strings.ToLower(vllmProxy) != "true" {
		sig, err := p.ctrl.GetChatSignature(chatID)
		if err != nil {
			p.handleBrokerError(ctx, err, "prepare HTTP request")
			return true
		}

		ctx.JSON(http.StatusOK, sig)
		return true
	}

	return false
}

func (p *Proxy) handleBrokerError(ctx *gin.Context, err error, context string) {
	p.logger.Errorf("Proxy broker error: %v, context: %s", err, context)
	info := "Provider proxy: handle proxied service"
	if context != "" {
		info += (", " + context)
	}
	errors.Response(ctx, errors.Wrap(err, info))
}
