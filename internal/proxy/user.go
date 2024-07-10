package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/0glabs/0g-data-retrieve-agent/internal/errors"
	"github.com/0glabs/0g-data-retrieve-agent/internal/model"
	"github.com/0glabs/0g-data-retrieve-agent/internal/proxy/chatbot"
	"github.com/gin-gonic/gin"
)

func (p *Proxy) GetData(c *gin.Context, url, name, provider, suffix, key string) {
	client := &http.Client{}

	var reqBody map[string]interface{}
	if err := c.ShouldBindJSON(&reqBody); err != nil {
		errors.Response(c, err)
		return
	}

	cbReq := chatbot.ChatBotRequest{
		CreatedAt:           time.Now().Format(time.RFC3339),
		UserAddress:         p.address,
		ServiceName:         name,
		PreviousOutputCount: 0,
		InputCount:          int64(0),
	}
	if err := cbReq.Generate(p.db, reqBody, key, provider); err != nil {
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

	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			errors.Response(c, err)
			return
		}

		contentEncoding := resp.Header.Get("Content-Encoding")
		res, err := chatbot.GetContent(body, contentEncoding)
		if err != nil {
			errors.Response(c, err)
			return
		}
		err = p.updateTokenCount(provider, res.Choices[0].Message.Content)
		if err != nil {
			errors.Response(c, err)
			return
		}
		c.Data(resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}

	c.Stream(func(w io.Writer) bool {
		var chunkBuf bytes.Buffer
		var output string
		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				if err == io.EOF {
					return false
				}
				errors.Response(c, err)
				return false
			}

			chunkBuf.WriteString(line)
			if line == "\n" || line == "\r\n" {
				_, err := w.Write(chunkBuf.Bytes())
				if err != nil {
					errors.Response(c, err)
					return false
				}

				encoding := resp.Header.Get("Content-Encoding")
				content, err := chatbot.GetContent(chunkBuf.Bytes(), encoding)
				if err != nil {
					errors.Response(c, err)
					return false
				}

				if content.Choices[0].FinishReason != nil {
					err = p.updateTokenCount(provider, output)
					if err != nil {
						errors.Response(c, err)
					}
					return false
				}
				output += content.Choices[0].Delta.Content
				c.Writer.Flush()
				chunkBuf.Reset()
			}
		}
	})
}

func (p *Proxy) updateTokenCount(provider, content string) error {
	count := int64(len(strings.Fields(content)))
	ret := p.db.Model(&model.Account{}).
		Where(&model.Account{Provider: provider, User: p.address}).
		Updates(model.Account{LastResponseTokenCount: count})

	return errors.Wrap(ret.Error, "update in db")
}
