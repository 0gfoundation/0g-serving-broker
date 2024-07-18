package handler

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/gin-gonic/gin"

	constant "github.com/0glabs/0g-serving-agent/common/const"
	"github.com/0glabs/0g-serving-agent/common/errors"
	commonModel "github.com/0glabs/0g-serving-agent/common/model"
	"github.com/0glabs/0g-serving-agent/common/util"
	"github.com/0glabs/0g-serving-agent/extractor"
	"github.com/0glabs/0g-serving-agent/extractor/chatbot"
	"github.com/0glabs/0g-serving-agent/user/model"
)

func (h *Handler) GetData(ctx *gin.Context) {
	provider := ctx.Param("provider")
	svcName := ctx.Param("service")

	callOpts := &bind.CallOpts{
		Context: context.Background(),
	}
	// TODO: add a cache
	svc, err := h.contract.GetService(callOpts, common.HexToAddress(provider), svcName)
	if err != nil {
		errors.Response(ctx, errors.Wrap(err, "get service from contract"))
		return
	}

	var extractor extractor.UserReqRespExtractor
	switch svc.ServiceType {
	case "chatbot":
		extractor = &chatbot.ChatBot{}
	default:
		errors.Response(ctx, errors.New("known service type"))
		return
	}

	account := model.Account{}
	if ret := h.db.Where(&model.Account{Provider: provider, User: h.userAddress}).First(&account); ret.Error != nil {
		errors.Response(ctx, errors.Wrap(ret.Error, "get account from db"))
		return
	}
	account.Nonce += 1

	req, err := h.prepareRequest(ctx, svc.Url, account, extractor)
	if err != nil {
		errors.Response(ctx, err)
		return
	}

	ret := h.db.Model(&model.Account{}).
		Where(&model.Account{Provider: provider, User: h.userAddress}).
		Updates(model.Account{Nonce: account.Nonce})
	if ret.Error != nil {
		errors.Response(ctx, errors.Wrap(ret.Error, "update in db"))
		return
	}

	h.processRequest(ctx, req, extractor)
}

func (h *Handler) prepareRequest(ctx *gin.Context, url string, account model.Account, extractor extractor.UserReqRespExtractor) (*http.Request, error) {
	provider := ctx.Param("provider")
	svcName := ctx.Param("service")
	suffix := ctx.Param("suffix")

	// prepare req
	var reqBody map[string]interface{}
	if err := ctx.ShouldBindJSON(&reqBody); err != nil {
		return nil, err
	}

	reqBodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	targetURL := url + constant.ServicePrefix + "/" + svcName
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
	reqModel := commonModel.Request{
		CreatedAt:           time.Now().Format(time.RFC3339),
		UserAddress:         h.userAddress,
		ServiceName:         svcName,
		PreviousOutputCount: account.LastResponseTokenCount,
		InputCount:          inputCount,
		Nonce:               account.Nonce,
	}
	cReq, err := util.ToContractRequest(reqModel)
	if err != nil {
		return nil, err
	}
	sig, err := cReq.GetSignature(h.key, provider)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Token-Count", strconv.FormatUint(uint64(reqModel.InputCount), 10))
	req.Header.Set("Address", reqModel.UserAddress)
	req.Header.Set("Service-Name", reqModel.ServiceName)
	req.Header.Set("Previous-Output-Token-Count", strconv.FormatUint(uint64(reqModel.PreviousOutputCount), 10))
	req.Header.Set("Created-At", reqModel.CreatedAt)
	req.Header.Set("Nonce", strconv.FormatUint(uint64(reqModel.Nonce), 10))
	req.Header.Set("Signature", hexutil.Encode(sig))

	for key, values := range ctx.Request.Header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	return req, nil
}

func (h *Handler) processRequest(ctx *gin.Context, req *http.Request, extractor extractor.UserReqRespExtractor) {
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		errors.Response(ctx, err)
		return
	}
	defer resp.Body.Close()

	for k, v := range resp.Header {
		ctx.Writer.Header()[k] = v
	}
	ctx.Writer.WriteHeader(resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		errors.Response(ctx, extractor.ErrMsg(resp.Body))
		return
	}

	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		h.handleResponse(ctx, resp, extractor)
	} else {
		h.handleStreamResponse(ctx, resp, extractor)
	}
}

func (h *Handler) handleResponse(ctx *gin.Context, resp *http.Response, extractor extractor.UserReqRespExtractor) {
	provider := ctx.Param("provider")
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		errors.Response(ctx, err)
		return
	}

	contentEncoding := resp.Header.Get("Content-Encoding")
	outputContent, err := extractor.GetRespContent(body, contentEncoding)
	if err != nil {
		errors.Response(ctx, err)
		return
	}
	outputCount, err := extractor.GetOutputCount([][]byte{outputContent})
	if err != nil {
		errors.Response(ctx, err)
		return
	}
	err = h.updateResponseInDB(provider, outputCount)
	if err != nil {
		errors.Response(ctx, err)
		return
	}
	ctx.Data(resp.StatusCode, resp.Header.Get("Content-Type"), body)
}

func (h *Handler) handleStreamResponse(ctx *gin.Context, resp *http.Response, extractor extractor.UserReqRespExtractor) {
	provider := ctx.Param("provider")
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
				errors.Response(ctx, err)
				return false
			}

			chunkBuf.WriteString(line)
			if line == "\n" || line == "\r\n" {
				_, err := w.Write(chunkBuf.Bytes())
				if err != nil {
					errors.Response(ctx, err)
					return false
				}

				encoding := resp.Header.Get("Content-Encoding")
				content, err := extractor.GetRespContent(chunkBuf.Bytes(), encoding)
				if err != nil {
					errors.Response(ctx, err)
					return false
				}

				completed, err := extractor.StreamCompleted(content)
				if err != nil {
					errors.Response(ctx, err)
					return false
				}
				if completed {
					outputCount, err := extractor.GetOutputCount(output)
					if err != nil {
						errors.Response(ctx, err)
						return false
					}
					err = h.updateResponseInDB(provider, outputCount)
					if err != nil {
						errors.Response(ctx, err)
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

func (h *Handler) updateResponseInDB(provider string, outputCount int64) error {
	ret := h.db.Model(&model.Account{}).
		Where(&model.Account{Provider: provider, User: h.userAddress}).
		Updates(model.Account{LastResponseTokenCount: outputCount})

	return errors.Wrap(ret.Error, "update in db")
}
