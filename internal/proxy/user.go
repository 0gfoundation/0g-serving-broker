package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/0glabs/0g-data-retrieve-agent/internal/errors"
	"github.com/gin-gonic/gin"
)

func (p *Proxy) GetData(c *gin.Context, url, name, provider, suffix, key string) {
	client := &http.Client{}

	var reqBody map[string]interface{}
	if err := c.ShouldBindJSON(&reqBody); err != nil {
		errors.Response(c, err)
		return
	}

	cbReq := chatBotRequest{
		CreatedAt:           time.Now().Format(time.RFC3339),
		UserAddress:         p.address,
		ServiceName:         name,
		PreviousOutputCount: 0,
	}
	if err := cbReq.generate(p.db, reqBody, key, provider); err != nil {
		errors.Response(c, err)
		return
	}

	reqBodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		errors.Response(c, err)
		return
	}

	route := url + servicePrefix + "/" + name
	if suffix != "" {
		route += suffix
	}
	req, err := http.NewRequest(c.Request.Method, route, bytes.NewBuffer(reqBodyBytes))
	if err != nil {
		errors.Response(c, err)
		return
	}

	req.Header.Set("Token-Count", strconv.FormatUint(uint64(cbReq.InputCount), 10))
	req.Header.Set("Address", cbReq.UserAddress)
	req.Header.Set("Service-Name", cbReq.ServiceName)
	req.Header.Set("Previous-Output-Token-Count", strconv.FormatUint(uint64(cbReq.PreviousOutputCount), 10))
	req.Header.Set("Created-At", cbReq.CreatedAt)
	req.Header.Set("Nonce", strconv.FormatUint(uint64(cbReq.Nonce), 10))
	req.Header.Set("Signature", cbReq.Signature)

	for key, values := range c.Request.Header {
		for _, value := range values {
			req.Header.Add(key, value)
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

	contentEncoding := resp.Header.Get("Content-Encoding")
	if err := cbReq.updateResponse(p.db, body, provider, contentEncoding); err != nil {
		errors.Response(c, err)
		return
	}

	c.Data(resp.StatusCode, resp.Header.Get("Content-Type"), body)
}
