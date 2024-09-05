package ctrl

import (
	"bufio"
	"bytes"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	constant "github.com/0glabs/0g-serving-agent/common/const"
	"github.com/0glabs/0g-serving-agent/common/errors"
	"github.com/0glabs/0g-serving-agent/extractor"
	"github.com/0glabs/0g-serving-agent/provider/model"
)

func (c *Ctrl) PrepareHTTPRequest(ctx *gin.Context, targetURL, route string, reqBody []byte) (*http.Request, error) {
	targetRoute := strings.TrimPrefix(ctx.Request.RequestURI, constant.ServicePrefix+"/"+route)
	if targetRoute != "/" {
		targetURL += targetRoute
	}
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

func (c *Ctrl) ProcessHTTPRequest(ctx *gin.Context, req *http.Request, reqModel model.Request, extractor extractor.ProviderReqRespExtractor, fee, outputPrice int64) {
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		handleAgentError(ctx, err, "call proxied service")
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
	ctx.Writer.WriteHeader(resp.StatusCode)

	oldAccount, err := c.GetOrCreateAccount(ctx, reqModel.UserAddress)
	if err != nil {
		handleAgentError(ctx, err, "")
		return
	}
	account := model.User{
		User:             reqModel.UserAddress,
		LastRequestNonce: &reqModel.Nonce,
		UnsettledFee:     model.PtrOf(fee + *oldAccount.UnsettledFee),
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		c.handleResponse(ctx, resp, extractor, account, outputPrice)
	} else {
		c.handleStreamResponse(ctx, resp, extractor, account, outputPrice)
	}
}

func (c *Ctrl) handleResponse(ctx *gin.Context, resp *http.Response, extractor extractor.ProviderReqRespExtractor, account model.User, outputPrice int64) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		handleAgentError(ctx, err, "read from body")
		return
	}

	contentEncoding := resp.Header.Get("Content-Encoding")
	outputContent, err := extractor.GetRespContent(body, contentEncoding)
	if err != nil {
		handleAgentError(ctx, err, "extract content")
		return
	}

	outputCount, err := extractor.GetOutputCount([][]byte{outputContent})
	if err != nil {
		handleAgentError(ctx, err, "extract count")
		return
	}

	account.LastResponseFee = model.PtrOf(outputCount * outputPrice)
	if err = c.UpdateUserAccount(account.User, account); err != nil {
		handleAgentError(ctx, err, "update user account in db")
		return
	}

	ctx.Data(http.StatusOK, resp.Header.Get("Content-Type"), body)
}

func (c *Ctrl) handleStreamResponse(ctx *gin.Context, resp *http.Response, extractor extractor.ProviderReqRespExtractor, account model.User, outputPrice int64) {
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
				handleAgentError(ctx, err, "read from body")
				return false
			}

			chunkBuf.WriteString(line)
			if line == "\n" || line == "\r\n" {
				_, err := w.Write(chunkBuf.Bytes())
				if err != nil {
					handleAgentError(ctx, err, "write to stream")
					return false
				}

				encoding := resp.Header.Get("Content-Encoding")
				content, err := extractor.GetRespContent(chunkBuf.Bytes(), encoding)
				if err != nil {
					handleAgentError(ctx, err, "extract content")
					return false
				}

				completed, err := extractor.StreamCompleted(content)
				if err != nil {
					handleAgentError(ctx, err, "check stream completed")
					return false
				}
				if completed {
					outputCount, err := extractor.GetOutputCount(output)
					if err != nil {
						handleAgentError(ctx, err, "extract output count")
						return false
					}

					account.LastResponseFee = model.PtrOf(outputCount * outputPrice)
					err = c.UpdateUserAccount(account.User, account)
					if err != nil {
						handleAgentError(ctx, err, "update user account in db")
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

func handleAgentError(ctx *gin.Context, err error, context string) {
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
	ctx.Writer.Write(respBody)
}
