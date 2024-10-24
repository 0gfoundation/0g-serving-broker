package proxy

import (
	"io"
	"strings"
	"sync"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"

	constant "github.com/0glabs/0g-serving-broker/common/const"
	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/extractor"
	"github.com/0glabs/0g-serving-broker/extractor/chatbot"
	imageGeneration "github.com/0glabs/0g-serving-broker/extractor/image-generation"
	"github.com/0glabs/0g-serving-broker/extractor/zgstorage"
	"github.com/0glabs/0g-serving-broker/provider/internal/ctrl"
	"github.com/0glabs/0g-serving-broker/provider/model"
)

type Proxy struct {
	ctrl *ctrl.Ctrl

	allowOrigins      []string
	serviceRoutes     map[string]bool
	serviceRoutesLock sync.RWMutex
	serviceTargets    map[string]string
	serviceTypes      map[string]string
	serviceGroup      *gin.RouterGroup
}

func New(ctrl *ctrl.Ctrl, engine *gin.Engine, allowOrigins []string) *Proxy {
	p := &Proxy{
		allowOrigins:   allowOrigins,
		ctrl:           ctrl,
		serviceRoutes:  make(map[string]bool),
		serviceTargets: make(map[string]string),
		serviceTypes:   make(map[string]string),
		serviceGroup:   engine.Group(constant.ServicePrefix),
	}

	p.serviceGroup.Use(cors.New(cors.Config{
		AllowOrigins: p.allowOrigins,
		AllowMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"},
		AllowHeaders: []string{"*"},
	}))
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
		case "zgStorage", "chatbot", "image-generation":
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
		handleBrokerError(ctx, errors.New("route is invalid"), "route filter middleware")
		return
	}

	p.serviceRoutesLock.RLock()
	valid, exists := p.serviceRoutes[segments[0]]
	p.serviceRoutesLock.RUnlock()
	if !exists {
		handleBrokerError(ctx, errors.New("route is not exist"), "route filter middleware")
		return
	}
	if !valid {
		handleBrokerError(ctx, errors.New("route is deleted"), "route filter middleware")
		return
	}
	ctx.Next()
}

func (p *Proxy) AddHTTPRoute(route, targetURL, svcType string) {
	//TODO: Add a URL validation
	_, exists := p.serviceRoutes[route]

	p.serviceRoutesLock.Lock()
	p.serviceRoutes[route] = true
	p.serviceTargets[route] = targetURL
	p.serviceTypes[route] = svcType
	p.serviceRoutesLock.Unlock()

	if exists {
		return
	}

	h := func(ctx *gin.Context) {
		p.proxyHTTPRequest(ctx, route)
	}
	p.serviceGroup.Any(route+"/*any", h)
}

func (p *Proxy) DeleteRoute(route string) {
	p.serviceRoutesLock.Lock()
	p.serviceRoutes[route] = false
	delete(p.serviceTargets, route)
	delete(p.serviceTypes, route)
	p.serviceRoutesLock.Unlock()
}

func (p *Proxy) UpdateRoute(route string, newTargetURL, newSvcType string) error {
	//TODO: Add a URL validation
	valid, exists := p.serviceRoutes[route]
	if !exists {
		return errors.New("route is not exist")
	}
	if !valid {
		return errors.New("route is deleted")
	}

	p.serviceRoutesLock.Lock()
	p.serviceRoutes[route] = true
	p.serviceTargets[route] = newTargetURL
	p.serviceTypes[route] = newSvcType
	p.serviceRoutesLock.Unlock()

	return nil
}

func (p *Proxy) proxyHTTPRequest(ctx *gin.Context, route string) {
	p.serviceRoutesLock.RLock()
	targetURL := p.serviceTargets[route]
	svcType := p.serviceTypes[route]
	p.serviceRoutesLock.RUnlock()

	targetRoute := strings.TrimPrefix(ctx.Request.RequestURI, constant.ServicePrefix+"/"+route)
	if targetRoute != "/" {
		targetURL += targetRoute
	}
	reqBody, err := io.ReadAll(ctx.Request.Body)
	if err != nil {
		handleBrokerError(ctx, err, "read request body")
		return
	}
	// handle endpoints not need to be charged
	if _, ok := constant.TargetRoute[targetRoute]; !ok {
		httpReq, err := p.ctrl.PrepareHTTPRequest(ctx, targetURL, route, reqBody)
		if err != nil {
			handleBrokerError(ctx, err, "prepare HTTP request")
			return
		}
		p.ctrl.ProcessHTTPRequest(ctx, httpReq, model.Request{}, nil, 0, 0, false)
		return
	}

	var extractor extractor.ProviderReqRespExtractor
	switch svcType {
	case "zgStorage":
		extractor = &zgstorage.ProviderZgStorage{}
	case "chatbot":
		extractor = &chatbot.ProviderChatBot{}
	case "image-generation":
		extractor = &imageGeneration.ProviderImageGeneration{}
	default:
		handleBrokerError(ctx, errors.New("unknown service type"), "prepare request extractor")
		return
	}
	svc, err := p.ctrl.GetService(route)
	if err != nil {
		handleBrokerError(ctx, err, "get service")
		return
	}
	req, err := p.ctrl.GetFromHTTPRequest(ctx)
	if err != nil {
		handleBrokerError(ctx, err, "get model.request from HTTP request")
		return
	}
	fee := req.InputFee + req.PreviousOutputFee

	inputCount, err := extractor.GetInputCount(reqBody)
	if err != nil {
		handleBrokerError(ctx, err, "get input count")
		return
	}
	if err := p.ctrl.ValidateRequest(ctx, req, fee, inputCount*svc.InputPrice); err != nil {
		handleBrokerError(ctx, err, "validate request")
		return
	}
	if err := p.ctrl.CreateRequest(req); err != nil {
		handleBrokerError(ctx, err, "create request")
		return
	}

	httpReq, err := p.ctrl.PrepareHTTPRequest(ctx, targetURL, route, reqBody)
	if err != nil {
		handleBrokerError(ctx, err, "prepare HTTP request")
		return
	}
	p.ctrl.ProcessHTTPRequest(ctx, httpReq, req, extractor, fee, svc.OutputPrice, true)
}

func handleBrokerError(ctx *gin.Context, err error, context string) {
	info := "Provider proxy: handle proxied service"
	if context != "" {
		info += (", " + context)
	}
	errors.Response(ctx, errors.Wrap(err, info))
}
