package proxy

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"gorm.io/gorm"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gin-gonic/gin"

	constant "github.com/0glabs/0g-serving-agent/common/const"
	"github.com/0glabs/0g-serving-agent/common/contract"
	"github.com/0glabs/0g-serving-agent/common/errors"
	commonModel "github.com/0glabs/0g-serving-agent/common/model"
	"github.com/0glabs/0g-serving-agent/common/util"
	"github.com/0glabs/0g-serving-agent/extractor"
	"github.com/0glabs/0g-serving-agent/provider/internal/db"
	"github.com/0glabs/0g-serving-agent/provider/model"
)

type requestValidator struct {
	db       *gorm.DB
	contract *contract.ServingContract

	extractor extractor.ProviderReqRespExtractor

	request                *commonModel.Request
	provider               string
	inputPrice             int64
	outputPrice            int64
	lockBalance            int64
	unsettledFee           int64
	lastRequestNonce       int64
	lastResponseTokenCount int64
}

func (r *requestValidator) backFillMetadata(ctx *gin.Context, provider string) error {
	r.provider = provider
	for k := range constant.RequestMetaData {
		values := ctx.Request.Header.Values(k)
		if len(values) == 0 {
			return errors.Wrapf(errors.New("missing Header"), "%s", k)
		}
		switch k {
		case "Address":
			r.request.UserAddress = values[0]
		case "Nonce":
			num, err := strconv.ParseInt(values[0], 10, 64)
			if err != nil {
				return errors.Wrapf(err, "parse nonce from string %s", values[0])
			}
			r.request.Nonce = num
		case "Service-Name":
			r.request.ServiceName = values[0]
		case "Token-Count":
			num, err := strconv.ParseInt(values[0], 10, 64)
			if err != nil {
				return errors.Wrapf(err, "parse inputCount from string %s", values[0])
			}
			r.request.InputCount = num
		case "Previous-Output-Token-Count":
			num, err := strconv.ParseInt(values[0], 10, 64)
			if err != nil {
				return errors.Wrapf(err, "parse previousOutputCount from string %s", values[0])
			}
			r.request.PreviousOutputCount = num
		case "Signature":
			r.request.Signature = values[0]
		case "Created-At":
			r.request.CreatedAt = values[0]
		}
	}

	svc := model.Service{}
	if ret := r.db.Where(&model.Service{Name: r.request.ServiceName, DeletedAt: 0}).First(&svc); ret.Error != nil {
		return errors.Wrap(ret.Error, "get service from db")
	}
	r.inputPrice = svc.InputPrice
	r.outputPrice = svc.OutputPrice

	if err := r.getOrCreateAccount(ctx); err != nil {
		return err
	}

	return nil
}

func (r *requestValidator) validate(reqBody []byte) error {
	err := r.validateSig()
	if err != nil {
		return err
	}

	err = r.validateInputToken(reqBody)
	if err != nil {
		return err
	}

	err = r.validatePreviousOutputCount()
	if err != nil {
		return err
	}

	err = r.validateNonce()
	if err != nil {
		return err
	}

	err = r.validateFee()
	if err != nil {
		return err
	}
	return nil
}

func (r *requestValidator) validateSig() error {
	cReq, err := util.ToContractRequest(*r.request)
	if err != nil {
		return errors.Wrap(err, "convert request from db schema to contract schema")
	}

	// https://github.com/ethereum/go-ethereum/issues/19751#issuecomment-504900739
	// Transform yellow paper V from 27/28 to 0/1
	if cReq.Signature[64] == 27 || cReq.Signature[64] == 28 {
		cReq.Signature[64] -= 27
	}

	prefixedHash, err := cReq.GetMessage(r.provider)
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

func (r *requestValidator) validateInputToken(reqBody []byte) error {
	inputCount, err := r.extractor.GetInputCount(reqBody)
	if err != nil {
		return err
	}

	if inputCount != r.request.InputCount {
		return fmt.Errorf("invalid inputCount, expected %d, but received %d", inputCount, r.request.InputCount)
	}

	return nil
}

func (r *requestValidator) validatePreviousOutputCount() error {
	if r.request.PreviousOutputCount == r.lastResponseTokenCount {
		return nil
	}
	return fmt.Errorf("invalid previousOutputCount, expected %d, but received %d", r.lastResponseTokenCount, r.request.PreviousOutputCount)
}

func (r *requestValidator) validateNonce() error {
	if r.request.Nonce > r.lastRequestNonce {
		return nil
	}
	return fmt.Errorf("invalid nonce, received nonce %d not greater than the previous nonce: %d", r.request.Nonce, r.lastRequestNonce)
}

func (r *requestValidator) validateFee() error {
	fee := r.getUnsettleFee()
	if fee <= r.lockBalance {
		return nil
	}

	err := r.syncAccount()
	if err != nil {
		return err
	}
	if fee <= r.lockBalance {
		return nil
	}
	return fmt.Errorf("insufficient balance, total fee of %d exceeds the available balance of %d", fee, r.lockBalance)
}

func (r *requestValidator) getUnsettleFee() int64 {
	return r.inputPrice*r.request.InputCount + r.outputPrice*r.request.PreviousOutputCount + r.unsettledFee
}

func (r *requestValidator) getOrCreateAccount(ctx *gin.Context) error {
	dbAccount := model.Account{}
	ret := r.db.Where(&model.Account{Provider: r.provider, User: r.request.UserAddress}).First(&dbAccount)
	if db.IgnoreNotFound(ret.Error) != nil {
		return errors.Wrap(ret.Error, "get account from db")
	}
	if ret.RowsAffected > 0 {
		r.lockBalance = *dbAccount.LockBalance
		r.unsettledFee = *dbAccount.UnsettledFee
		r.lastRequestNonce = *dbAccount.LastRequestNonce
		r.lastResponseTokenCount = *dbAccount.LastResponseTokenCount
		return nil
	}

	callOpts := &bind.CallOpts{
		Context: ctx,
	}
	account, err := r.contract.GetUserAccount(
		callOpts,
		common.HexToAddress(r.request.UserAddress),
		common.HexToAddress(r.provider),
	)
	if err != nil {
		return errors.Wrap(err, "get account from contract")
	}

	now := time.Now()
	nonce := account.Nonce.Int64()
	r.lockBalance = account.Balance.Int64() - account.PendingRefund.Int64()
	dbAccount = model.Account{
		Provider:             r.provider,
		User:                 r.request.UserAddress,
		LockBalance:          &r.lockBalance,
		LastRequestNonce:     &nonce,
		LastBalanceCheckTime: &now,
	}

	if ret := r.db.Create(&dbAccount); ret.Error != nil {
		return errors.Wrap(ret.Error, "create account in db")
	}
	return nil
}

func (r *requestValidator) syncAccount() error {
	callOpts := &bind.CallOpts{
		Context: context.Background(),
	}
	account, err := r.contract.GetUserAccount(
		callOpts,
		common.HexToAddress(r.provider),
		common.HexToAddress(r.request.UserAddress),
	)
	if err != nil {
		return err
	}

	r.lockBalance = account.Balance.Int64() - account.PendingRefund.Int64()
	now := time.Now()
	ret := r.db.Where(
		&model.Account{
			Provider: r.provider,
			User:     r.request.UserAddress,
		}).Updates(
		model.Account{
			LockBalance:          &r.lockBalance,
			LastBalanceCheckTime: &now,
		})
	return errors.Wrap(ret.Error, "update in db")
}
