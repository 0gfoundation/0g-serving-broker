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
	"golang.org/x/exp/rand"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	constant "github.com/0glabs/0g-serving-broker/inference-router/const"
	"github.com/0glabs/0g-serving-broker/inference-router/contract"
	"github.com/0glabs/0g-serving-broker/inference-router/extractor"
	"github.com/0glabs/0g-serving-broker/inference-router/extractor/chatbot"
	"github.com/0glabs/0g-serving-broker/inference-router/extractor/zgstorage"
	"github.com/0glabs/0g-serving-broker/inference-router/model"
	"github.com/0glabs/0g-serving-broker/inference-router/zkclient/models"
)

func (c *Ctrl) IncreaseAccountNonce(providerAddress string) (model.Provider, error) {
	ret, err := c.db.GetProviderAccount(providerAddress)
	if err != nil {
		return ret, errors.Wrap(err, "get provider from db")
	}
	// The prover broker in the provider packs a certain number of requests into one
	// chunk as the minimum unit for settlement. This number, referred to as the chunk size,
	// is defined in the prover's circuit. If the number of real requests in a chunk is smaller than 40,
	// the remaining slots will be filled with mock requests with incrementing nonces. During settlement,
	// the nonce of the last request in the last chunk will replace the recorded nonce in the contract.
	//
	// Here, by setting the increment step equal to the chunk size, we can ensure that the
	// nonce in the request is always valid, as the nonce in each new request will always be larger than the value in the contract.
	*ret.Nonce += int64(c.zk.RequestLength)

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
	case "zgStorage":
		extractor = &zgstorage.UserZgStorage{SvcInfo: svc}
	case "chatbot":
		extractor = &chatbot.ChatBot{SvcInfo: svc}
	default:
		return nil, errors.New("known service type")
	}
	c.svcCache.Set(key, extractor, cache.DefaultExpiration)
	return extractor, nil
}

func (c *Ctrl) PrepareRequest(ctx *gin.Context, svc contract.Service, provider model.Provider, extractor extractor.UserReqRespExtractor, suffix string, reqBody map[string]interface{}) (*http.Request, error) {
	svcName := svc.Name

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
	previousOutputFee := *provider.LastResponseFee
	inputFee := inputCount * svc.InputPrice.Int64()
	fee := inputFee + previousOutputFee

	reqInZK := &models.Request{
		Fee:             strconv.FormatInt(fee, 10),
		Nonce:           strconv.FormatInt(*provider.Nonce, 10),
		ProviderAddress: provider.Provider,
		UserAddress:     c.contract.UserAddress,
	}
	sig, err := c.GenerateSignature(ctx, reqInZK, provider.Signer)
	if err != nil {
		return nil, err
	}
	sigJson, err := json.Marshal(sig[0])
	if err != nil {
		return nil, err
	}
	headers := map[string]string{
		"Address":             c.contract.UserAddress,
		"Fee":                 strconv.FormatInt(fee, 10),
		"Input-Fee":           strconv.FormatInt(inputFee, 10),
		"Nonce":               strconv.FormatInt(*provider.Nonce, 10),
		"Previous-Output-Fee": strconv.FormatInt(previousOutputFee, 10),
		"Service-Name":        svcName,
		"Signature":           string(sigJson),
	}
	util.SetHeaders(req, headers)

	reqInDB := model.Request{
		ProviderAddress:   provider.Provider,
		Nonce:             *provider.Nonce,
		ServiceName:       svcName,
		InputFee:          inputFee,
		PreviousOutputFee: previousOutputFee,
		Fee:               fee,
		Signature:         string(sigJson),
	}
	if err := c.CreateRequest(reqInDB); err != nil {
		return req, err
	}

	for key, values := range ctx.Request.Header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	return req, nil
}

func (c *Ctrl) ProcessRequest(ctx *gin.Context, req *http.Request, extractor extractor.UserReqRespExtractor, signingAddress string) {
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		handleBrokerError(ctx, err, "get response from provider")
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
		handleServiceError(ctx, resp.Body)
		return
	}

	secretHeader := req.Header.Get("Authorization")
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		c.handleResponse(ctx, resp, extractor, signingAddress, secretHeader)
		return
	}
	c.handleStreamResponse(ctx, resp, extractor, signingAddress, secretHeader)
}

func (c *Ctrl) handleResponse(ctx *gin.Context, resp *http.Response, extractor extractor.UserReqRespExtractor, signingAddress, secretHeader string) {
	providerAddress := extractor.GetSvcInfo().Provider.String()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		handleBrokerError(ctx, err, "read response")
		return
	}
	contentEncoding := resp.Header.Get("Content-Encoding")
	outputContent, err := extractor.GetRespContent(body, contentEncoding)
	if err != nil {
		handleBrokerError(ctx, err, "get resp content")
		return
	}
	if err := c.VerifyChat(ctx, providerAddress, extractor.GetSvcInfo().Name, [][]byte{outputContent}, signingAddress, extractor.GetSvcInfo().Model, secretHeader); err != nil {
		handleBrokerError(ctx, err, "verify chat")
		return
	}

	outputCount, err := extractor.GetOutputCount([][]byte{outputContent})
	if err != nil {
		handleBrokerError(ctx, err, "get resp output count")
		return
	}
	new := model.Provider{
		Provider:        providerAddress,
		LastResponseFee: model.PtrOf(outputCount * extractor.GetSvcInfo().OutputPrice.Int64()),
	}
	err = c.db.UpdateProviderAccount(providerAddress, new)
	if err != nil {
		handleBrokerError(ctx, err, "update provider output count in db")
		return
	}
	ctx.Data(resp.StatusCode, resp.Header.Get("Content-Type"), body)
}

func (c *Ctrl) handleStreamResponse(ctx *gin.Context, resp *http.Response, extractor extractor.UserReqRespExtractor, signingAddress, secretHeader string) {
	providerAddress := extractor.GetSvcInfo().Provider.String()
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
				handleBrokerError(ctx, err, "read from provider response")
				return false
			}

			chunkBuf.WriteString(line)
			if line == "\n" || line == "\r\n" {
				_, err := w.Write(chunkBuf.Bytes())
				if err != nil {
					handleBrokerError(ctx, err, "write to response")
					return false
				}

				encoding := resp.Header.Get("Content-Encoding")
				content, err := extractor.GetRespContent(chunkBuf.Bytes(), encoding)
				if err != nil {
					handleBrokerError(ctx, err, "get response content")
					return false
				}

				completed, err := extractor.StreamCompleted(content)
				if err != nil {
					handleBrokerError(ctx, err, "check whether stream completed")
					return false
				}
				if completed {
					if err := c.VerifyChat(ctx, providerAddress, extractor.GetSvcInfo().Name, output, signingAddress, extractor.GetSvcInfo().Model, secretHeader); err != nil {
						handleBrokerError(ctx, err, "verify chat")
						return false
					}
					outputCount, err := extractor.GetOutputCount(output)
					if err != nil {
						handleBrokerError(ctx, err, "get response output count")
						return false
					}
					new := model.Provider{
						Provider:        providerAddress,
						LastResponseFee: model.PtrOf(outputCount * extractor.GetSvcInfo().OutputPrice.Int64()),
					}
					err = c.db.UpdateProviderAccount(providerAddress, new)
					if err != nil {
						handleBrokerError(ctx, err, "update provider output count in db")
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

func (c *Ctrl) VerifyChat(ctx *gin.Context, providerAddress, svcName string, output [][]byte, signingAddress, modelName, secretHeader string) error {
	ids := []string{}
	for _, output := range output {
		var response struct {
			ID string `json:"id"`
		}
		json.Unmarshal(output, &response)
		if response.ID != "" {
			ids = append(ids, response.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	randomID := ids[rand.Intn(len(ids))]
	responseSignature, err := c.FetchSignatureByChatID(ctx, providerAddress, svcName, randomID, modelName, secretHeader)
	if err != nil {
		return err
	}

	err = c.VerifySignature(
		responseSignature.Text,
		"0x"+responseSignature.Signature,
		signingAddress,
	)
	if err != nil {
		return err
	}

	return nil
}
