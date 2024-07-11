package proxy

import (
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	constant "github.com/0glabs/0g-serving-agent/common/const"
	"github.com/0glabs/0g-serving-agent/common/contract"
	"github.com/0glabs/0g-serving-agent/common/errors"
	"github.com/0glabs/0g-serving-agent/provider/model"
)

type Proxy struct {
	db       *gorm.DB
	router   *gin.Engine
	contract *contract.ServingContract

	address    string
	privateKey string

	serviceRoutes     map[string]bool
	serviceRoutesLock sync.RWMutex
	serviceGroup      *gin.RouterGroup
}

func New(db *gorm.DB, router *gin.Engine, c *contract.ServingContract, address, privateKey string) *Proxy {
	p := &Proxy{
		db:            db,
		router:        router,
		contract:      c,
		address:       address,
		privateKey:    privateKey,
		serviceRoutes: make(map[string]bool),
		serviceGroup:  router.Group(constant.ServicePrefix),
	}

	p.serviceGroup.Use(p.routeFilterMiddleware)
	return p
}

func (p *Proxy) Start() error {
	tx := p.db.Model(model.Service{})
	services := []model.Service{}
	if ret := tx.Find(&services); ret.Error != nil {
		return ret.Error
	}
	for _, svc := range services {
		switch svc.Type {
		case "RPC":
			// TODO: Add p.AddRPCRoute
		case "chatbot":
			p.AddHTTPRoute(svc.Name, svc.URL)
		default:
			return errors.New("invalid service type")
		}
	}
	return nil
}

func (p *Proxy) routeFilterMiddleware(c *gin.Context) {
	route := strings.TrimPrefix(c.Request.URL.Path, constant.ServicePrefix+"/")
	segments := strings.Split(route, "/")
	if len(segments) == 0 || segments[0] == "" {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	p.serviceRoutesLock.RLock()
	valid, exists := p.serviceRoutes[segments[0]]
	p.serviceRoutesLock.RUnlock()
	if !exists || !valid {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.Next()
}
