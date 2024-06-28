package proxy

import (
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-data-retrieve-agent/internal/errors"
	"github.com/0glabs/0g-data-retrieve-agent/internal/model"
)

func (p *Proxy) AddHTTPRoute(route string, targetURL string) {
	//TODO: Add a URL validation
	_, exists := p.serviceRoutes[route]

	p.serviceRoutesLock.Lock()
	p.serviceRoutes[route] = true
	p.serviceRoutesLock.Unlock()

	if exists {
		return
	}

	h := func(c *gin.Context) {
		p.proxyHTTPRequest(c, route, targetURL)
	}
	p.serviceGroup.Any(route+"/*any", h)
}

func (p *Proxy) DeleteRoute(route string) {
	p.serviceRoutesLock.Lock()
	p.serviceRoutes[route] = false
	p.serviceRoutesLock.Unlock()
}

func (p *Proxy) proxyHTTPRequest(c *gin.Context, route, targetURL string) {
	dbReq, err := getRequest(c.Request)
	if err != nil {
		errors.Response(c, err)
		return
	}
	pass, err := validate(*dbReq, p.address)
	if err != nil {
		errors.Response(c, err)
		return
	}
	if !pass {
		errors.Response(c, errors.New("invalid signature"))
		return
	}
	if ret := p.db.Create(&dbReq); ret.Error != nil {
		errors.Response(c, err)
		return
	}

	client := &http.Client{}
	targetRoute := strings.TrimPrefix(c.Request.RequestURI, servicePrefix+"/"+route)
	req, err := http.NewRequest(c.Request.Method, targetURL+targetRoute, c.Request.Body)
	if err != nil {
		errors.Response(c, err)
		return
	}

	for k, v := range c.Request.Header {
		if _, ok := requestMetaData[k]; !ok {
			req.Header.Set(k, v[0])
			continue
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		errors.Response(c, err)
		return
	}
	defer resp.Body.Close()

	for k, v := range resp.Header {
		c.Writer.Header()[k] = v
	}
	c.Writer.WriteHeader(resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		errors.Response(c, err)
		return
	}
	c.Data(resp.StatusCode, resp.Header.Get("Content-Type"), body)
}

func getRequest(req *http.Request) (*model.Request, error) {
	dbReq := &model.Request{}
	for k := range requestMetaData {
		values := req.Header.Values(k)
		if len(values) == 0 {
			return nil, errors.Wrapf(errors.New("missing Header"), "%s", k)
		}
		switch k {
		case "Address":
			dbReq.UserAddress = values[0]
		case "Nonce":
			dbReq.Nonce = values[0]
		case "Service-Name":
			dbReq.ServiceName = values[0]
		case "Token-Count":
			dbReq.InputCount = values[0]
		case "Previous-Output-Token-Count":
			dbReq.PreviousOutputCount = values[0]
		case "Signature":
			dbReq.Signature = values[0]
		case "Created-At":
			dbReq.CreatedAt = values[0]
		}
	}
	return dbReq, nil
}
