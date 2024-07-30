package proxy

import (
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"

	constant "github.com/0glabs/0g-serving-agent/common/const"
	"github.com/0glabs/0g-serving-agent/common/errors"
	"github.com/0glabs/0g-serving-agent/extractor"
	"github.com/0glabs/0g-serving-agent/extractor/chatbot"
	"github.com/0glabs/0g-serving-agent/provider/internal/ctrl"
)

type Proxy struct {
	ctrl *ctrl.Ctrl

	serviceRoutes     map[string]bool
	serviceRoutesLock sync.RWMutex
	serviceGroup      *gin.RouterGroup
}

func New(ctrl *ctrl.Ctrl, engine *gin.Engine) *Proxy {
	p := &Proxy{
		ctrl:          ctrl,
		serviceRoutes: make(map[string]bool),
		serviceGroup:  engine.Group(constant.ServicePrefix),
	}

	p.serviceGroup.Use(p.routeFilterMiddleware)
	return p
}

func (p *Proxy) Start() error {
	svcs, err := p.ctrl.ListService()
	if err != nil {
		return errors.Wrap(err, "Provider: start proxy service, list service in db")
	}
	for _, svc := range svcs {
		switch svc.Type {
		case "RPC":
		case "chatbot":
			p.AddHTTPRoute(svc.Name, svc.URL, svc.Type)
		default:
			return errors.New("invalid service type")
		}
	}
	return nil
}

func (p *Proxy) routeFilterMiddleware(ctx *gin.Context) {
	route := strings.TrimPrefix(ctx.Request.URL.Path, constant.ServicePrefix+"/")
	segments := strings.Split(route, "/")
	if len(segments) == 0 || segments[0] == "" {
		ctx.AbortWithStatus(http.StatusNotFound)
		return
	}

	p.serviceRoutesLock.RLock()
	valid, exists := p.serviceRoutes[segments[0]]
	p.serviceRoutesLock.RUnlock()
	if !exists || !valid {
		ctx.AbortWithStatus(http.StatusNotFound)
		return
	}
	ctx.Next()
}

func (p *Proxy) AddHTTPRoute(route, targetURL, svcType string) {
	//TODO: Add a URL validation
	_, exists := p.serviceRoutes[route]

	p.serviceRoutesLock.Lock()
	p.serviceRoutes[route] = true
	p.serviceRoutesLock.Unlock()

	if exists {
		return
	}

	h := func(ctx *gin.Context) {
		p.proxyHTTPRequest(ctx, route, targetURL, svcType)
	}
	p.serviceGroup.Any(route+"/*any", h)
}

func (p *Proxy) DeleteRoute(route string) {
	p.serviceRoutesLock.Lock()
	p.serviceRoutes[route] = false
	p.serviceRoutesLock.Unlock()
}

func (p *Proxy) UpdateRoute(route string, targetURL, svcType string) error {
	//TODO: Add a URL validation
	valid, exists := p.serviceRoutes[route]
	if !exists || !valid {
		return errors.New("route is not valid")
	}

	p.serviceRoutesLock.Lock()
	p.serviceRoutes[route] = true
	p.serviceRoutesLock.Unlock()

	h := func(ctx *gin.Context) {
		p.proxyHTTPRequest(ctx, route, targetURL, svcType)
	}
	p.serviceGroup.Any(route+"/*any", h)
	return nil
}

func (p *Proxy) proxyHTTPRequest(ctx *gin.Context, route, targetURL, svcType string) {
	var extractor extractor.ProviderReqRespExtractor
	switch svcType {
	case "chatbot":
		extractor = &chatbot.ProviderChatBot{}
	default:
		handleError(ctx, errors.New("unknown service type"), "prepare request extractor")
		return
	}
	s := ctx.Request.Header.Values("Service-Name")
	if len(s) == 0 {
		handleError(ctx, errors.New("get service name from request header"), "")
		return
	}
	svc, err := p.ctrl.GetService(s[0])
	if err != nil {
		handleError(ctx, err, "get service")
		return
	}
	req, err := p.ctrl.GetFromHTTPRequest(ctx)
	if err != nil {
		handleError(ctx, err, "get model.request from HTTP request")
		return
	}
	fee := svc.InputPrice*req.InputCount + svc.OutputPrice*req.PreviousOutputCount
	reqBody, err := io.ReadAll(ctx.Request.Body)
	if err != nil {
		handleError(ctx, err, "read request body")
		return
	}
	inputCount, err := extractor.GetInputCount(reqBody)
	if err != nil {
		handleError(ctx, err, "get input count")
		return
	}
	if err := p.ctrl.ValidateRequest(ctx, req, fee, inputCount); err != nil {
		handleError(ctx, err, "validate request")
		return
	}
	if err := p.ctrl.CreateRequest(req); err != nil {
		handleError(ctx, err, "create request")
		return
	}

	httpReq, err := p.ctrl.PrepareHTTPRequest(ctx, targetURL, route, reqBody)
	if err != nil {
		handleError(ctx, err, "prepare HTTP request")
		return
	}

	p.ctrl.ProcessHTTPRequest(ctx, httpReq, req, extractor, fee)
}

func handleError(ctx *gin.Context, err error, context string) {
	errors.Response(ctx, errors.Wrap(err, "Provider proxy: handle proxied service, "+context))
}
