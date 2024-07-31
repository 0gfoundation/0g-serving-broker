package ctrl

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gin-gonic/gin"

	constant "github.com/0glabs/0g-serving-agent/common/const"
	"github.com/0glabs/0g-serving-agent/common/errors"
	commonModel "github.com/0glabs/0g-serving-agent/common/model"
	"github.com/0glabs/0g-serving-agent/common/util"
	"github.com/0glabs/0g-serving-agent/provider/model"
)

func (c *Ctrl) CreateRequest(req commonModel.Request) error {
	return errors.Wrap(c.db.CreateRequest(req), "create request in db")
}

func (c *Ctrl) GetFromHTTPRequest(ctx *gin.Context) (commonModel.Request, error) {
	var req commonModel.Request
	for k := range constant.RequestMetaData {
		values := ctx.Request.Header.Values(k)
		if len(values) == 0 {
			return req, errors.Wrapf(errors.New("missing Header"), "%s", k)
		}
		switch k {
		case "Address":
			req.UserAddress = values[0]
		case "Nonce":
			num, err := strconv.ParseInt(values[0], 10, 64)
			if err != nil {
				return req, errors.Wrapf(err, "parse nonce %s", values[0])
			}
			req.Nonce = num
		case "Service-Name":
			req.ServiceName = values[0]
		case "Token-Count":
			num, err := strconv.ParseInt(values[0], 10, 64)
			if err != nil {
				return req, errors.Wrapf(err, "parse inputCount %s", values[0])
			}
			req.InputCount = num
		case "Previous-Output-Token-Count":
			num, err := strconv.ParseInt(values[0], 10, 64)
			if err != nil {
				return req, errors.Wrapf(err, "parse previousOutputCount %s", values[0])
			}
			req.PreviousOutputCount = num
		case "Signature":
			req.Signature = values[0]
		case "Created-At":
			createAt, err := time.Parse(time.RFC3339, values[0])
			if err != nil {
				return req, errors.Wrapf(err, "parse createAt %s", values[0])
			}
			req.CreatedAt = model.PtrOf(createAt)
		}
	}

	return req, nil
}

func (c *Ctrl) ValidateRequest(ctx *gin.Context, req commonModel.Request, fee, inputCount int64) error {
	account, err := c.GetOrCreateAccount(ctx, req.UserAddress)
	if err != nil {
		return err
	}

	err = c.validateSig(req)
	if err != nil {
		return err
	}

	err = c.validateInputToken(req, inputCount)
	if err != nil {
		return err
	}

	err = c.validatePreviousOutputCount(req, account)
	if err != nil {
		return err
	}

	err = c.validateNonce(req, *account.LastRequestNonce)
	if err != nil {
		return err
	}

	err = c.validateFee(ctx, account, fee)
	if err != nil {
		return err
	}
	return nil
}

func (c *Ctrl) validateSig(request commonModel.Request) error {
	cReq, err := util.ToContractRequest(request)
	if err != nil {
		return errors.Wrap(err, "convert request from db schema to contract schema")
	}

	// https://github.com/ethereum/go-ethereum/issues/19751#issuecomment-504900739
	// Transform yellow paper V from 27/28 to 0/1
	if cReq.Signature[64] == 27 || cReq.Signature[64] == 28 {
		cReq.Signature[64] -= 27
	}

	prefixedHash, err := cReq.GetMessage(c.contract.ProviderAddress)
	if err != nil {
		return errors.Wrap(err, "Get Message")
	}

	recovered, err := crypto.SigToPub(prefixedHash.Bytes(), cReq.Signature)
	if err != nil {
		return errors.Wrap(err, "SigToPub")
	}

	recoveredAddr := crypto.PubkeyToAddress(*recovered)
	if recoveredAddr != cReq.UserAddress {
		return errors.New("recovered address signature not match the real address")
	}

	return nil
}

func (c *Ctrl) validateInputToken(actual commonModel.Request, inputCount int64) error {
	if inputCount != actual.InputCount {
		return fmt.Errorf("invalid inputCount, expected %d, but received %d", inputCount, actual.InputCount)
	}

	return nil
}

func (c *Ctrl) validatePreviousOutputCount(actual commonModel.Request, account model.User) error {
	if account.LastResponseTokenCount == nil {
		return nil
	}
	if actual.PreviousOutputCount >= *account.LastResponseTokenCount {
		return nil
	}
	return fmt.Errorf("invalid previousOutputCount, expected %d, but received %d", *account.LastResponseTokenCount, actual.PreviousOutputCount)
}

func (c *Ctrl) validateNonce(actual commonModel.Request, lastRequestNonce int64) error {
	if actual.Nonce > lastRequestNonce {
		return nil
	}
	return fmt.Errorf("invalid nonce, received nonce %d not greater than the previous nonce: %d", actual.Nonce, lastRequestNonce)
}

func (c *Ctrl) validateFee(ctx context.Context, account model.User, fee int64) error {
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
	return fmt.Errorf("insufficient balance, total fee of %d exceeds the available balance of %d", fee, *newAccount.LockBalance)
}
