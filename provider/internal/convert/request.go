package convert

import (
	"math/big"
	"time"

	"github.com/0glabs/0g-serving-agent/common/contract"
	"github.com/0glabs/0g-serving-agent/common/errors"
	"github.com/0glabs/0g-serving-agent/common/util"
	"github.com/0glabs/0g-serving-agent/provider/model"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

func ToContractRequest(req model.Request) (contract.Request, error) {
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
