package ctrl

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/errors"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

func (c *Ctrl) PrepareHTTPRequest(ctx *gin.Context, targetURL string, reqBody []byte) (*http.Request, error) {
	req, err := http.NewRequest(ctx.Request.Method, targetURL, io.NopCloser(bytes.NewBuffer(reqBody)))
	if err != nil {
		return nil, err
	}

	for k, v := range ctx.Request.Header {
		if _, ok := constant.RequestMetaData[k]; !ok {
			req.Header.Set(k, v[0])
			continue
		}
	}

	// may need additional secret to access the target service
	if additionalSecret := c.Service.AdditionalSecret; additionalSecret != nil {
		for k, v := range additionalSecret {
			req.Header.Set(k, v)
		}
	}

	return req, nil
}

func (c *Ctrl) ProcessHTTPRequest(ctx *gin.Context, svcType string, req *http.Request, reqModel model.Request, outputPrice int64, charing bool) error {
	client := &http.Client{}

	// back up body for other usage
	body, err := io.ReadAll(req.Body)
	if err != nil {
		handleBrokerError(ctx, err, "failed to read request body")
		return err
	}
	req.Body = io.NopCloser(bytes.NewBuffer(body))

	resp, err := client.Do(req)
	if err != nil {
		handleBrokerError(ctx, err, "call proxied service")
		return err
	}
	defer resp.Body.Close()

	for k, v := range resp.Header {
		if k == "Content-Length" {
			continue
		}
		ctx.Writer.Header()[k] = v
	}

	if resp.StatusCode != http.StatusOK {
		ctx.Writer.WriteHeader(resp.StatusCode)
		handleServiceError(ctx, resp.Body)
		return err
	}

	ctx.Writer.Header().Add("provider", c.contract.ProviderAddress)
	c.addExposeHeaders(ctx)

	ctx.Status(resp.StatusCode)

	if !charing {
		return c.handleResponse(ctx, resp)
	}

	_, err = c.GetOrCreateAccount(ctx, reqModel.UserAddress)
	if err != nil {
		handleBrokerError(ctx, err, "")
		return err
	}

	account := model.User{
		User:             reqModel.UserAddress,
		UnsettledFee:     model.PtrOf(reqModel.Fee),
	}

	switch svcType {
	case "chatbot":
		return c.handleChatbotResponse(ctx, resp, account, outputPrice, body, reqModel)
	default:
		err = errors.New("unknown service type")
		handleBrokerError(ctx, err, "prepare request extractor")
		return err
	}
}

func (c *Ctrl) GetChatSignature(chatID string) (*ChatSignature, error) {
	key := c.chatCacheKey(chatID)
	log.Printf("get signature for chat: %v", chatID)
	val, exist := c.svcCache.Get(key)
	if !exist {
		return nil, errors.New("Chat id not found or expired, chat_id_not_found")
	}

	chatSignature, ok := val.(ChatSignature)
	if !ok {
		return nil, errors.New("cached object does not implement ChatSignature")
	}

	return &chatSignature, nil
}

func (c *Ctrl) handleResponse(ctx *gin.Context, resp *http.Response) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		handleBrokerError(ctx, err, "read from body")
		return err
	}
	if _, err := ctx.Writer.Write(body); err != nil {
		handleBrokerError(ctx, err, "write response body")
		return err
	}

	return nil
}

func (c *Ctrl) addExposeHeaders(ctx *gin.Context) {
	// Set 'Access-Control-Expose-Headers' for CORS
	exposeHeaders := []string{"Provider", "content-encoding"}
	existing := ctx.Writer.Header().Get("Access-Control-Expose-Headers")
	var newHeaders string
	if existing != "" {
		headerSet := make(map[string]struct{})
		for _, header := range strings.Split(existing, ",") {
			headerSet[strings.TrimSpace(header)] = struct{}{}
		}

		for _, header := range exposeHeaders {
			if _, exists := headerSet[header]; !exists {
				existing += "," + header
			}
		}

		newHeaders = existing
	} else {
		newHeaders = strings.Join(exposeHeaders, ",")
	}
	ctx.Writer.Header().Set("Access-Control-Expose-Headers", newHeaders)
}

func handleBrokerError(ctx *gin.Context, err error, context string) {
	// TODO: recorded to log system
	info := "Provider proxy: handle proxied service response"
	if context != "" {
		info += (", " + context)
	}
	errors.Response(ctx, errors.Wrap(err, info))
}

func handleServiceError(ctx *gin.Context, body io.ReadCloser) {
	respBody, err := io.ReadAll(body)
	if err != nil {
		// TODO: recorded to log system
		log.Println(err)
		return
	}
	if _, err := ctx.Writer.Write(respBody); err != nil {
		// TODO: recorded to log system
		log.Println(err)
	}
}
