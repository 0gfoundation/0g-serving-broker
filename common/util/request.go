package util

import (
	"math/big"

	"github.com/0glabs/0g-serving-agent/common/contract"
	"github.com/0glabs/0g-serving-agent/common/errors"
	"github.com/0glabs/0g-serving-agent/common/model"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

func ToContractRequest(req model.Request) (contract.Request, error) {
	ret := contract.Request{
		UserAddress:         common.HexToAddress(req.UserAddress),
		Nonce:               ToBigInt(req.Nonce),
		ServiceName:         req.ServiceName,
		InputCount:          ToBigInt(req.InputCount),
		PreviousOutputCount: ToBigInt(req.PreviousOutputCount),
	}
	ret.CreatedAt = big.NewInt(req.CreatedAt.Unix())
	if req.Signature == "" {
		return ret, nil
	}

	sig, err := hexutil.Decode(req.Signature)
	if err != nil {
		return ret, errors.Wrapf(err, "convert signature %s", req.Signature)
	}
	ret.Signature = sig
	return ret, nil
}
