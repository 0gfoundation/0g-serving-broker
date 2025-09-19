package ctrl

import (
	"fmt"
	"log"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
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
		if len(values) == 0 && k != "VLLM-Proxy" {
			return req, errors.Wrapf(errors.New("missing Header"), "%s", k)
		}
		value := values[0]

		if err := updateRequestField(&req, k, value); err != nil {
			return req, err
		}
	}

	return req, nil
}

func (c *Ctrl) ValidateRequest(ctx *gin.Context, req model.Request) error {
	contractAccount, err := c.contract.GetUserAccount(ctx, common.HexToAddress(req.UserAddress))
	if err != nil {
		return errors.Wrap(err, "get account from contract")
	}

	if c.teeService.Address != contractAccount.TeeSignerAddress {
		return errors.New("user not acknowledge the provider")
	}

	account, err := c.GetOrCreateAccount(ctx, req.UserAddress)
	if err != nil {
		return err
	}

	err = c.validateBalanceAdequacy(ctx, account, req.InputFee)
	if err != nil {
		return err
	}
	return nil
}


func (c *Ctrl) validateBalanceAdequacy(ctx *gin.Context, account model.User, fee string) error {
	if account.LockBalance == nil {
		return errors.New("nil lockBalance in account")
	}

	// Calculate response fee reservation
	responseFeeReservation, err := util.Multiply(c.Service.OutputPrice, constant.ResponseFeeReservationFactor)
	if err != nil {
		return errors.Wrap(err, "calculate response fee reservation")
	}

	// Use optimized calculation for unsettled fee using database aggregation
	unsettledFee, err := c.db.CalculateUnsettledFee(account.User, c.Service.InputPrice, c.Service.OutputPrice)
	if err != nil {
		return errors.Wrap(err, "calculate unsettled fee")
	}

	// Add input fee, unsettled fee, and response fee reservation
	totalWithInput, err := util.Add(fee, unsettledFee.String())
	if err != nil {
		return err
	}
	total, err := util.Add(totalWithInput, responseFeeReservation)
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
	
	// Recalculate unsettled fee after sync using optimized method
	unsettledFeeNew, err := c.db.CalculateUnsettledFee(account.User, c.Service.InputPrice, c.Service.OutputPrice)
	if err != nil {
		return errors.Wrap(err, "recalculate unsettled fee")
	}
	
	totalWithInputNew, err := util.Add(fee, unsettledFeeNew.String())
	if err != nil {
		return err
	}
	totalNew, err := util.Add(totalWithInputNew, responseFeeReservation)
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
	ctx.Set("ignoreError", true)
	return fmt.Errorf("insufficient balance, total fee of %s (including response reservation) exceeds the available balance of %s", totalNew.String(), *newAccount.LockBalance)
}

func updateRequestField(req *model.Request, key, value string) error {
	switch key {
	case "Address":
		req.UserAddress = value
	case "VLLM-Proxy":
		v, err := strconv.ParseBool(value)
		if err != nil {
			log.Printf("%v", err)
			v = false
		}

		req.VLLMProxy = v
	default:
		return errors.Wrapf(errors.New("unexpected Header"), "%s", key)
	}
	return nil
}
