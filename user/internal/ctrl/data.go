package ctrl

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	constant "github.com/0glabs/0g-serving-agent/common/const"
	"github.com/0glabs/0g-serving-agent/common/errors"
	"github.com/0glabs/0g-serving-agent/user/model"
	commonModel "github.com/0glabs/0g-serving-agent/common/model"
)

type DataFetcher interface {
	BackFillRequestHeader(req *http.Request, reqBody map[string]interface{}, account model.Account) error
	HandleResponse(ctx *gin.Context, req *http.Request)
	UpdateResponseInDB(provider, content string) error
	GetDataFetcherInfo() commonModel.DataFetcherInfo
}

func (c *Ctrl) GetData(ctx *gin.Context, dataFetcher DataFetcher) {
	info := dataFetcher.GetDataFetcherInfo()

	var reqBody map[string]interface{}
	if err := ctx.ShouldBindJSON(&reqBody); err != nil {
		errors.Response(ctx, err)
		return
	}

	reqBodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		errors.Response(ctx, err)
		return
	}
	route := info.Url + constant.ServicePrefix + "/" + info.ServiceName
	if info.QuerySuffix != "" {
		route += info.QuerySuffix
	}
	req, err := http.NewRequest(ctx.Request.Method, route, bytes.NewBuffer(reqBodyBytes))
	if err != nil {
		errors.Response(ctx, err)
		return
	}

	account := model.Account{}
	if ret := c.db.Where(&model.Account{Provider: info.Provider, User: info.User}).First(&account); ret.Error != nil {
		errors.Response(ctx, errors.Wrap(ret.Error, "get account from db"))
		return
	}
	if err := dataFetcher.BackFillRequestHeader(req, reqBody, account); err != nil {
		errors.Response(ctx, err)
		return
	}

	for key, values := range ctx.Request.Header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	ret := c.db.Model(&model.Account{}).
		Where(&model.Account{Provider: info.Provider, User: info.User}).
		Updates(model.Account{Nonce: account.Nonce + 1})

	if ret.Error != nil {
		errors.Response(ctx, errors.Wrap(ret.Error, "update in db"))
		return
	}

	dataFetcher.HandleResponse(ctx, req)
}
