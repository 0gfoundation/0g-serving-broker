package ctrl

import (
	"bufio"
	"bytes"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/extractor"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

func (c *Ctrl) PrepareHTTPRequest(ctx *gin.Context, targetURL, route string, reqBody []byte) (*http.Request, error) {
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
	return req, nil
}

func (c *Ctrl) ProcessHTTPRequest(ctx *gin.Context, req *http.Request, reqModel model.Request, extractor extractor.ProviderReqRespExtractor, fee, outputPrice string, charing bool) {
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		handleBrokerError(ctx, err, "call proxied service")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		handleServiceError(ctx, resp.Body)
		return
	}

	for k, v := range resp.Header {
		if k == "Content-Length" {
			continue
		}
		ctx.Writer.Header()[k] = v
	}

	ctx.Writer.Header().Add("provider", c.contract.ProviderAddress)
	ctx.Writer.Header().Add("service-name", reqModel.ServiceName)
	c.addExposeHeaders(ctx)

	ctx.Status(resp.StatusCode)

	if !charing {
		c.handleResponse(ctx, resp)
		return
	}

	oldAccount, err := c.GetOrCreateAccount(ctx, reqModel.UserAddress)
	if err != nil {
		handleBrokerError(ctx, err, "")
		return
	}
	unsettledFee, err := util.Add(fee, oldAccount.UnsettledFee)
	if err != nil {
		handleBrokerError(ctx, err, "add unsettled fee")
		return
	}

	account := model.User{
		User:             reqModel.UserAddress,
		LastRequestNonce: &reqModel.Nonce,
		UnsettledFee:     model.PtrOf(unsettledFee.String()),
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		c.handleChargingResponse(ctx, resp, extractor, account, outputPrice)
	} else {
		c.handleChargingStreamResponse(ctx, resp, extractor, account, outputPrice)
	}
}

func (c *Ctrl) handleResponse(ctx *gin.Context, resp *http.Response) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		handleBrokerError(ctx, err, "read from body")
		return
	}
	if _, err := ctx.Writer.Write(body); err != nil {
		handleBrokerError(ctx, err, "write response body")
	}
}

func (c *Ctrl) handleChargingResponse(ctx *gin.Context, resp *http.Response, extractor extractor.ProviderReqRespExtractor, account model.User, outputPrice string) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		handleBrokerError(ctx, err, "read from body")
		return
	}

	contentEncoding := resp.Header.Get("Content-Encoding")
	outputContent, err := extractor.GetRespContent(body, contentEncoding)
	if err != nil {
		handleBrokerError(ctx, err, "extract content")
		return
	}

	outputCount, err := extractor.GetOutputCount([][]byte{outputContent})
	if err != nil {
		handleBrokerError(ctx, err, "extract count")
		return
	}
	lastResponseFee, err := util.Multiply(outputPrice, outputCount)
	if err != nil {
		handleBrokerError(ctx, err, "multiply")
		return
	}

	account.LastResponseFee = model.PtrOf(lastResponseFee.String())
	if err = c.UpdateUserAccount(account.User, account); err != nil {
		handleBrokerError(ctx, err, "update user account in db")
		return
	}

	if _, err := ctx.Writer.Write(body); err != nil {
		handleBrokerError(ctx, err, "write response body")
	}
}

func (c *Ctrl) handleChargingStreamResponse(ctx *gin.Context, resp *http.Response, extractor extractor.ProviderReqRespExtractor, account model.User, outputPrice string) {
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
				handleBrokerError(ctx, err, "read from body")
				return false
			}

			chunkBuf.WriteString(line)
			if line == "\n" || line == "\r\n" {
				_, err := w.Write(chunkBuf.Bytes())
				if err != nil {
					handleBrokerError(ctx, err, "write to stream")
					return false
				}

				encoding := resp.Header.Get("Content-Encoding")
				content, err := extractor.GetRespContent(chunkBuf.Bytes(), encoding)
				if err != nil {
					handleBrokerError(ctx, err, "extract content")
					return false
				}

				completed, err := extractor.StreamCompleted(content)
				if err != nil {
					handleBrokerError(ctx, err, "check stream completed")
					return false
				}
				if completed {
					outputCount, err := extractor.GetOutputCount(output)
					if err != nil {
						handleBrokerError(ctx, err, "extract output count")
						return false
					}
					lastResponseFee, err := util.Multiply(outputPrice, outputCount)
					if err != nil {
						handleBrokerError(ctx, err, "multiply")
						return false
					}

					account.LastResponseFee = model.PtrOf(lastResponseFee.String())
					err = c.UpdateUserAccount(account.User, account)
					if err != nil {
						handleBrokerError(ctx, err, "update user account in db")
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

func (c *Ctrl) addExposeHeaders(ctx *gin.Context) {
	// Set 'Access-Control-Expose-Headers' for CORS
	exposeHeaders := []string{"Provider", "content-encoding", "service-name"}
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
