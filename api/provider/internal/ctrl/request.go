package ctrl

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"

	constant "github.com/0glabs/0g-serving-broker/common/const"
	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/common/zkclient/models"
	"github.com/0glabs/0g-serving-broker/provider/model"
)

func (c *Ctrl) CreateRequest(req model.Request) error {
	return errors.Wrap(c.db.CreateRequest(req), "create request in db")
}

func (c *Ctrl) ListRequest(q model.RequestListOptions) ([]model.Request, int, error) {
	list, fee, err := c.db.ListRequest(q)
	if err != nil {
		return nil, 0, errors.Wrap(err, "list service from db")
	}
	return list, fee, nil
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

func (c *Ctrl) ValidateRequest(ctx *gin.Context, req model.Request, expectedFee, expectedInputFee string) error {
	account, err := c.GetOrCreateAccount(ctx, req.UserAddress)
	if err != nil {
		return err
	}

	err = c.validateSig(ctx, req)
	if err != nil {
		return err
	}

	err = c.validateNonce(req, account.LastRequestNonce)
	if err != nil {
		return err
	}

	err = c.validateFee(req, account, expectedFee, expectedInputFee)
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

func (c *Ctrl) validateFee(actual model.Request, account model.User, expectedFee, expectedInputFee string) error {
	if account.LastResponseFee != nil {
		cmp1, err := util.Compare(actual.PreviousOutputFee, account.LastResponseFee)
		if err != nil {
			return err
		}
		if cmp1 < 0 {
			return fmt.Errorf("invalid previousOutputFee, expected %d, but received %d", *account.LastResponseFee, actual.PreviousOutputFee)
		}
	}
	cmp2, err := util.Compare(actual.InputFee, expectedInputFee)
	if err != nil {
		return err
	}
	if cmp2 < 0 {
		return fmt.Errorf("invalid inputFee, expected %d, but received %d", expectedInputFee, actual.InputFee)
	}
	cmp3, err := util.Compare(actual.Fee, expectedFee)
	if err != nil {
		return err
	}
	if cmp3 < 0 {
		return fmt.Errorf("invalid fee, expected %d, but received %d. Please check the service price", expectedFee, actual.Fee)
	}
	return nil
}

func (c *Ctrl) validateNonce(actual model.Request, lastRequestNonce *string) error {
	cmp, err := util.Compare(actual.Nonce, lastRequestNonce)
	if err != nil {
		return err
	}
	if cmp > 0 {
		return nil
	}
	return fmt.Errorf("invalid nonce, received nonce %d not greater than the previous nonce: %d", actual.Nonce, lastRequestNonce)
}

func (c *Ctrl) validateBalanceAdequacy(ctx context.Context, account model.User, fee string) error {
	if account.UnsettledFee == nil || account.LockBalance == nil {
		return errors.New("nil unsettledFee or lockBalance in account")
	}
	total, err := util.Add(fee, account.UnsettledFee)
	if err != nil {
		return err
	}
	cmp1, err := util.Compare(total, account.LockBalance)
	if err != nil {
		return err
	}
	if cmp1 <= 0 {
		return nil
	}

	// reload account and repeat the check
	if err := c.SyncUserAccount(ctx, common.HexToAddress(account.User)); err != nil {
		return err
	}
	newAccount, err := c.GetOrCreateAccount(ctx, account.User)
	if err != nil {
		return err
	}
	totalNew, err := util.Add(fee, account.UnsettledFee)
	if err != nil {
		return err
	}
	cmp2, err := util.Compare(totalNew, newAccount.LockBalance)
	if err != nil {
		return err
	}
	if cmp2 <= 0 {
		return nil
	}
	return fmt.Errorf("insufficient balance, total fee of %s exceeds the available balance of %s", totalNew.String(), *newAccount.LockBalance)
}

func updateRequestField(req *model.Request, key, value string) error {
	switch key {
	case "Address":
		req.UserAddress = value
	case "Fee":
		req.Fee = value
	case "Input-Fee":
		req.InputFee = value
	case "Nonce":
		req.Nonce = value
	case "Previous-Output-Fee":
		req.PreviousOutputFee = value
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
