package proxy

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/json"
	"io"
	"log"
	"strings"

	"gorm.io/gorm"

	"github.com/andybalholm/brotli"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/0glabs/0g-data-retrieve-agent/internal/contract"
	"github.com/0glabs/0g-data-retrieve-agent/internal/errors"
	"github.com/0glabs/0g-data-retrieve-agent/internal/model"
)

// https://platform.openai.com/docs/api-reference/making-requests

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Choice struct {
	Message Message `json:"message"`
}

type chatBotRequest model.Request

// Generate used by the user agent to generate the next request metadata

func (c *chatBotRequest) generate(db *gorm.DB, reqBody map[string]interface{}, key, provider string) error {
	account := model.Account{}
	if ret := db.Where(&model.Account{Provider: provider, User: c.UserAddress}).First(&account); ret.Error != nil {
		return errors.Wrap(ret.Error, "get account from db")
	}

	c.PreviousOutputCount = account.LastResponseTokenCount
	c.Nonce = account.Nonce

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
		c.InputCount += int64(len(strings.Fields(content)))
	}

	cReq, err := contract.ConvertFromDB(model.Request(*c))
	if err != nil {
		return err
	}

	sig, err := cReq.GetSignature(key, provider)
	if err != nil {
		return err
	}
	c.Signature = hexutil.Encode(sig)

	ret := db.Model(&model.Account{}).
		Where(&model.Account{Provider: provider, User: c.UserAddress}).
		Updates(model.Account{Nonce: c.Nonce + 1})

	return errors.Wrap(ret.Error, "update in db")
}

func (c *chatBotRequest) updateResponse(db *gorm.DB, resp []byte, provider, contentEncoding string) error {
	var reader io.ReadCloser
	switch contentEncoding {
	case "br":
		reader = io.NopCloser(brotli.NewReader(bytes.NewReader(resp)))
	case "gzip":
		gzipReader, err := gzip.NewReader(bytes.NewReader(resp))
		if err != nil {
			return err
		}
		defer gzipReader.Close()
		reader = gzipReader
	case "deflate":
		deflateReader, err := zlib.NewReader(bytes.NewReader(resp))
		if err != nil {
			return err
		}
		defer deflateReader.Close()
		reader = deflateReader
	default:
		reader = io.NopCloser(bytes.NewReader(resp))
	}

	decompressedBody, err := io.ReadAll(reader)
	if err != nil {
		return err
	}

	log.Println(string(decompressedBody))
	var res struct {
		Choices []Choice `json:"choices"`
	}
	if err := json.Unmarshal(decompressedBody, &res); err != nil {
		return errors.Wrap(err, "unmarshal response")
	}

	outputCount := int64(0)
	for _, content := range res.Choices {
		outputCount += int64(len(strings.Fields(content.Message.Content)))
	}

	ret := db.Model(&model.Account{}).
		Where(&model.Account{Provider: provider, User: c.UserAddress}).
		Updates(model.Account{LastResponseTokenCount: outputCount})

	return errors.Wrap(ret.Error, "update in db")
}

func validate(dbReq model.Request, provider string) (bool, error) {
	// TODO: Verify the following fields in the request header:
	//  - inputToken matches the number of input tokens in the request body.
	//  - previousOutputCount matches the number of tokens returned in the previous response.
	//  - nonce is greater than the nonce of the previous request.

	cReq, err := contract.ConvertFromDB(dbReq)
	if err != nil {
		return false, errors.Wrap(err, "convert request from db schema to contract schema")
	}

	// https://github.com/ethereum/go-ethereum/issues/19751#issuecomment-504900739
	// Transform yellow paper V from 27/28 to 0/1
	if cReq.Signature[64] == 27 || cReq.Signature[64] == 28 {
		cReq.Signature[64] -= 27
	}

	prefixedHash, err := cReq.GetMessage(provider)
	if err != nil {
		return false, errors.Wrap(err, "Get Message")
	}

	recovered, err := crypto.SigToPub(prefixedHash.Bytes(), cReq.Signature)
	if err != nil {
		return false, errors.Wrap(err, "SigToPub")
	}

	recoveredAddr := crypto.PubkeyToAddress(*recovered)

	return recoveredAddr == cReq.UserAddress, nil
}
