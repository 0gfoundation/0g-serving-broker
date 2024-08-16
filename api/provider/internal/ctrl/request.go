package ctrl

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"

	constant "github.com/0glabs/0g-serving-agent/common/const"
	"github.com/0glabs/0g-serving-agent/common/errors"
	"github.com/0glabs/0g-serving-agent/common/zkclient/models"
	"github.com/0glabs/0g-serving-agent/provider/model"
)

func (c *Ctrl) CreateRequest(req model.Request) error {
	return errors.Wrap(c.db.CreateRequest(req), "create request in db")
}

func (c *Ctrl) GetFromHTTPRequest(ctx *gin.Context) (model.Request, error) {
	var req model.Request
	headerMap := ctx.Request.Header

	for k := range constant.RequestMetaData {
		values := headerMap.Values(k)
		if len(values) == 0 {
			return req, errors.Wrapf(errors.New("missing Header"), "%s", k)
		}
		value := values[0]

		if err := updateRequestField(&req, k, value); err != nil {
			return req, err
		}
	}

	return req, nil
}

func (c *Ctrl) ValidateRequest(ctx *gin.Context, req model.Request, expectedFee, expectedInputCount int64) error {
	account, err := c.GetOrCreateAccount(ctx, req.UserAddress)
	if err != nil {
		return err
	}

	err = c.validateSig(ctx, req)
	if err != nil {
		return err
	}

	err = c.validateNonce(req, *account.LastRequestNonce)
	if err != nil {
		return err
	}

	err = c.validateFee(req, account, expectedFee, expectedInputCount)
	if err != nil {
		return err
	}

	err = c.validateBalanceAdequacy(ctx, account, req.Fee)
	if err != nil {
		return err
	}
	return nil
}

func (c *Ctrl) validateSig(ctx context.Context, req model.Request) error {
	reqInZK := &models.Request{
		Fee:             req.Fee,
		Nonce:           req.Nonce,
		ProviderAddress: c.contract.ProviderAddress,
		UserAddress:     req.UserAddress,
	}
	var sig []int64
	err := json.Unmarshal([]byte(req.Signature), &sig)
	if err != nil {
		return errors.New("Failed to parse signature")
	}
	ret, err := c.CheckSignatures(ctx, reqInZK, [][]int64{sig})
	if err != nil {
		return errors.Wrapf(err, "check signature")
	}
	if len(ret) == 0 || !ret[0] {
		return errors.New("invalid signature")
	}
	return nil
}

func (c *Ctrl) validateFee(actual model.Request, account model.User, expectedFee, expectedInputCount int64) error {
	if account.LastResponseTokenCount != nil && actual.PreviousOutputCount < *account.LastResponseTokenCount {
		return fmt.Errorf("invalid previousOutputCount, expected %d, but received %d", *account.LastResponseTokenCount, actual.PreviousOutputCount)
	}
	if actual.InputCount < expectedInputCount {
		return fmt.Errorf("invalid inputCount, expected %d, but received %d", expectedInputCount, actual.InputCount)
	}
	if actual.Fee < expectedFee {
		return fmt.Errorf("invalid fee, expected %d, but received %d. Please check the service price", expectedFee, actual.Fee)
	}
	return nil
}

func (c *Ctrl) validateNonce(actual model.Request, lastRequestNonce int64) error {
	if actual.Nonce > lastRequestNonce {
		return nil
	}
	return fmt.Errorf("invalid nonce, received nonce %d not greater than the previous nonce: %d", actual.Nonce, lastRequestNonce)
}

func (c *Ctrl) validateBalanceAdequacy(ctx context.Context, account model.User, fee int64) error {
	if account.UnsettledFee == nil || account.LockBalance == nil {
		return errors.New("nil unsettledFee or lockBalance in account")
	}
	if fee+*account.UnsettledFee <= *account.LockBalance {
		return nil
	}
	if err := c.SyncUserAccount(ctx, common.HexToAddress(account.User)); err != nil {
		return err
	}
	newAccount, err := c.GetOrCreateAccount(ctx, account.User)
	if err != nil {
		return err
	}
	if fee+*newAccount.UnsettledFee <= *newAccount.LockBalance {
		return nil
	}
	return fmt.Errorf("insufficient balance, total fee of %d exceeds the available balance of %d", fee+*newAccount.UnsettledFee, *newAccount.LockBalance)
}

func updateRequestField(req *model.Request, key, value string) error {
	switch key {
	case "Address":
		req.UserAddress = value
	case "Fee":
		return parseInt64Field(&req.Fee, "fee", value)
	case "Input-Count":
		return parseInt64Field(&req.InputCount, "inputCount", value)
	case "Nonce":
		return parseInt64Field(&req.Nonce, "nonce", value)
	case "Previous-Output-Count":
		return parseInt64Field(&req.PreviousOutputCount, "previousOutputCount", value)
	case "Service-Name":
		req.ServiceName = value
	case "Signature":
		req.Signature = value
	default:
		return errors.Wrapf(errors.New("unexpected Header"), "%s", key)
	}
	return nil
}

func parseInt64Field(field *int64, name, value string) error {
	num, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return errors.Wrapf(err, "parse %s %s", name, value)
	}
	*field = num
	return nil
}
