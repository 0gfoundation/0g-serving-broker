package ctrl

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"
	"github.com/patrickmn/go-cache"

	constant "github.com/0glabs/0g-serving-agent/common/const"
	"github.com/0glabs/0g-serving-agent/common/contract"
	"github.com/0glabs/0g-serving-agent/common/errors"
	"github.com/0glabs/0g-serving-agent/common/util"
	"github.com/0glabs/0g-serving-agent/common/zkclient/models"
	"github.com/0glabs/0g-serving-agent/extractor"
	"github.com/0glabs/0g-serving-agent/extractor/chatbot"
	"github.com/0glabs/0g-serving-agent/user/model"
)

func (c *Ctrl) IncreaseAccountNonce(providerAddress string) (model.Provider, error) {
	ret, err := c.db.GetProviderAccount(providerAddress)
	if err != nil {
		return ret, errors.Wrap(err, "get provider from db")
	}
	*ret.Nonce += 1

	return ret, c.db.UpdateProviderAccount(providerAddress, ret)
}

func (c *Ctrl) GetExtractor(ctx context.Context, providerAddress, svcName string) (extractor.UserReqRespExtractor, error) {
	key := providerAddress + svcName
	value, found := c.svcCache.Get(key)
	if found {
		extractor, ok := value.(extractor.UserReqRespExtractor)
		if !ok {
			return nil, errors.New("cached object does not implement UserReqRespExtractor")
		}
		return extractor, nil
	}

	svc, err := c.contract.GetService(ctx, common.HexToAddress(providerAddress), svcName)
	if err != nil {
		return nil, errors.Wrap(err, "get service from contract")
	}

	var extractor extractor.UserReqRespExtractor
	switch svc.ServiceType {
	case "chatbot":
		extractor = &chatbot.ChatBot{SvcInfo: svc}
	default:
		return nil, errors.New("known service type")
	}
	c.svcCache.Set(key, extractor, cache.DefaultExpiration)
	return extractor, nil
}

func (c *Ctrl) PrepareRequest(ctx *gin.Context, svc contract.Service, provider model.Provider, extractor extractor.UserReqRespExtractor) (*http.Request, error) {
	svcName := ctx.Param("service")
	suffix := ctx.Param("suffix")

	var reqBody map[string]interface{}
	if err := ctx.ShouldBindJSON(&reqBody); err != nil {
		return nil, err
	}

	reqBodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	targetURL := svc.Url + constant.ServicePrefix + "/" + svcName
	if suffix != "" {
		targetURL += suffix
	}
	req, err := http.NewRequest(ctx.Request.Method, targetURL, bytes.NewBuffer(reqBodyBytes))
	if err != nil {
		return nil, err
	}

	inputCount, err := extractor.GetInputCount(reqBodyBytes)
	if err != nil {
		return nil, err
	}
	previousOutputCount := *provider.LastResponseTokenCount
	fee := inputCount*svc.InputPrice.Int64() + previousOutputCount*svc.OutputPrice.Int64()

	reqInZK := &models.Request{
		Fee:             fee,
		Nonce:           *provider.Nonce,
		ProviderAddress: provider.Provider,
		UserAddress:     c.contract.UserAddress,
	}
	sig, err := c.GenerateSignature(ctx, reqInZK)
	if err != nil {
		return nil, err
	}
	sigJson, err := json.Marshal(sig[0])
	if err != nil {
		return nil, err
	}
	headers := map[string]string{
		"Address":               c.contract.UserAddress,
		"Fee":                   strconv.FormatInt(fee, 10),
		"Input-Count":           strconv.FormatInt(inputCount, 10),
		"Nonce":                 strconv.FormatInt(*provider.Nonce, 10),
		"Previous-Output-Count": strconv.FormatInt(previousOutputCount, 10),
		"Service-Name":          svcName,
		"Signature":             string(sigJson),
	}
	util.SetHeaders(req, headers)

	for key, values := range ctx.Request.Header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	return req, nil
}

func (c *Ctrl) ProcessRequest(ctx *gin.Context, req *http.Request, extractor extractor.UserReqRespExtractor) {
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		handleError(ctx, err, "get response from provider")
		return
	}
	defer resp.Body.Close()

	for k, v := range resp.Header {
		if k == "Content-Length" {
			continue
		}
		ctx.Writer.Header()[k] = v
	}
	ctx.Writer.WriteHeader(resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		handleError(ctx, extractor.ErrMsg(resp.Body), "get response from provider")
		return
	}

	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		c.handleResponse(ctx, resp, extractor)
		return
	}
	c.handleStreamResponse(ctx, resp, extractor)
}

func (c *Ctrl) handleResponse(ctx *gin.Context, resp *http.Response, extractor extractor.UserReqRespExtractor) {
	providerAddress := ctx.Param("provider")
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		handleError(ctx, err, "read response")
		return
	}
	contentEncoding := resp.Header.Get("Content-Encoding")
	outputContent, err := extractor.GetRespContent(body, contentEncoding)
	if err != nil {
		handleError(ctx, err, "get resp content")
		return
	}
	outputCount, err := extractor.GetOutputCount([][]byte{outputContent})
	if err != nil {
		handleError(ctx, err, "get resp output count")
		return
	}
	new := model.Provider{
		Provider:               providerAddress,
		LastResponseTokenCount: &outputCount,
	}
	err = c.db.UpdateProviderAccount(providerAddress, new)
	if err != nil {
		handleError(ctx, err, "update provider output count in db")
		return
	}
	ctx.Data(resp.StatusCode, resp.Header.Get("Content-Type"), body)
}

func (c *Ctrl) handleStreamResponse(ctx *gin.Context, resp *http.Response, extractor extractor.UserReqRespExtractor) {
	providerAddress := ctx.Param("provider")
	ctx.Stream(func(w io.Writer) bool {
		var chunkBuf bytes.Buffer
		var output [][]byte
		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				if err == io.EOF {
					return false
				}
				handleError(ctx, err, "read from provider response")
				return false
			}

			chunkBuf.WriteString(line)
			if line == "\n" || line == "\r\n" {
				_, err := w.Write(chunkBuf.Bytes())
				if err != nil {
					handleError(ctx, err, "write to response")
					return false
				}

				encoding := resp.Header.Get("Content-Encoding")
				content, err := extractor.GetRespContent(chunkBuf.Bytes(), encoding)
				if err != nil {
					handleError(ctx, err, "get response content")
					return false
				}

				completed, err := extractor.StreamCompleted(content)
				if err != nil {
					handleError(ctx, err, "check whether stream completed")
					return false
				}
				if completed {
					outputCount, err := extractor.GetOutputCount(output)
					if err != nil {
						handleError(ctx, err, "get response output count")
						return false
					}
					new := model.Provider{
						Provider:               providerAddress,
						LastResponseTokenCount: &outputCount,
					}
					err = c.db.UpdateProviderAccount(providerAddress, new)
					if err != nil {
						handleError(ctx, err, "update provider output count in db")
						return false
					}
				}
				output = append(output, content)
				ctx.Writer.Flush()
				chunkBuf.Reset()
			}
		}
	})
}
