package chatbot

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-agent/common/contract"
	"github.com/0glabs/0g-serving-agent/common/errors"
	"github.com/0glabs/0g-serving-agent/common/model"
	"github.com/0glabs/0g-serving-agent/common/util"
	userModel "github.com/0glabs/0g-serving-agent/user/model"
)

// user use generate to generate the next request metadata
func (c *ChatBot) BackFillRequestHeader(req *http.Request, reqBody map[string]interface{}, account userModel.Account) error {
	reqModel := model.Request{
		CreatedAt:           time.Now().Format(time.RFC3339),
		UserAddress:         c.DataFetcherInfo.User,
		ServiceName:         c.DataFetcherInfo.ServiceName,
		PreviousOutputCount: account.LastResponseTokenCount,
		InputCount:          int64(0),
		Nonce:               account.Nonce,
	}

	// https://platform.openai.com/docs/api-reference/making-requests
	messages, ok := reqBody["messages"].([]interface{})
	if !ok || messages == nil {
		return errors.New("Missing or invalid messages field")
	}

	for _, m := range messages {
		message, ok := m.(map[string]interface{})
		if !ok || message == nil {
			return errors.New("Missing or invalid message field")
		}
		content, ok := message["content"].(string)
		if !ok || content == "" {
			return errors.New("Missing or invalid content field")
		}
		reqModel.InputCount += int64(len(strings.Fields(content)))
	}

	cReq, err := toContractRequest(reqModel)
	if err != nil {
		return err
	}
	sig, err := cReq.GetSignature(c.DataFetcherInfo.PrivateKey, c.DataFetcherInfo.Provider)
	if err != nil {
		return err
	}

	req.Header.Set("Token-Count", strconv.FormatUint(uint64(reqModel.InputCount), 10))
	req.Header.Set("Address", reqModel.UserAddress)
	req.Header.Set("Service-Name", reqModel.ServiceName)
	req.Header.Set("Previous-Output-Token-Count", strconv.FormatUint(uint64(reqModel.PreviousOutputCount), 10))
	req.Header.Set("Created-At", reqModel.CreatedAt)
	req.Header.Set("Nonce", strconv.FormatUint(uint64(reqModel.Nonce), 10))
	req.Header.Set("Signature", hexutil.Encode(sig))

	return nil
}

func toContractRequest(req model.Request) (contract.Request, error) {
	ret := contract.Request{
		UserAddress:         common.HexToAddress(req.UserAddress),
		Nonce:               util.ToBigInt(req.Nonce),
		ServiceName:         req.ServiceName,
		InputCount:          util.ToBigInt(req.InputCount),
		PreviousOutputCount: util.ToBigInt(req.PreviousOutputCount),
	}
	createdAt, err := time.Parse(time.RFC3339, req.CreatedAt)
	if err != nil {
		return ret, errors.Wrapf(err, "convert createdAt %s", req.CreatedAt)
	}
	ret.CreatedAt = big.NewInt(createdAt.Unix())

	if req.Signature == "" {
		return ret, nil
	}

	ret.Signature, err = hexutil.Decode(req.Signature)
	return ret, errors.Wrapf(err, "convert signature %s", req.Signature)
}

func (c *ChatBot) HandleResponse(ctx *gin.Context, req *http.Request) {
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
		errors.Response(ctx, errMsg(resp.Body))
		return
	}

	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			errors.Response(ctx, err)
			return
		}

		contentEncoding := resp.Header.Get("Content-Encoding")
		res, err := GetContent(body, contentEncoding)
		if err != nil {
			errors.Response(ctx, err)
			return
		}
		err = c.UpdateResponseInDB(c.DataFetcherInfo.Provider, res.Choices[0].Message.Content)
		if err != nil {
			errors.Response(ctx, err)
			return
		}
		ctx.Data(resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}

	ctx.Stream(func(w io.Writer) bool {
		var chunkBuf bytes.Buffer
		var output string
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
				content, err := GetContent(chunkBuf.Bytes(), encoding)
				if err != nil {
					errors.Response(ctx, err)
					return false
				}

				if content.Choices[0].FinishReason != nil {
					err = c.UpdateResponseInDB(c.DataFetcherInfo.Provider, output)
					if err != nil {
						errors.Response(ctx, err)
					}
					return false
				}
				output += content.Choices[0].Delta.Content
				ctx.Writer.Flush()
				chunkBuf.Reset()
			}
		}
	})
}

func (c *ChatBot) UpdateResponseInDB(provider, content string) error {
	count := int64(len(strings.Fields(content)))
	ret := c.DB.Model(&userModel.Account{}).
		Where(&userModel.Account{Provider: provider, User: c.DataFetcherInfo.User}).
		Updates(userModel.Account{LastResponseTokenCount: count})

	return errors.Wrap(ret.Error, "update in db")
}

func errMsg(body io.Reader) error {
	msg := struct {
		Error string `json:"error"`
	}{}

	if err := json.NewDecoder(body).Decode(&msg); err != nil {
		return errors.Wrap(err, "decode error message")
	}

	return errors.New(msg.Error)
}
