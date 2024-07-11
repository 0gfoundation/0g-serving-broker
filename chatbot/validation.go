package chatbot

import (
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/0glabs/0g-serving-agent/common/errors"
	"github.com/0glabs/0g-serving-agent/common/model"
)

func Validate(dbReq model.Request, provider string) (bool, error) {
	// TODO: Verify the following fields in the request header:
	//  - inputToken matches the number of input tokens in the request body.
	//  - previousOutputCount matches the number of tokens returned in the previous response.
	//  - nonce is greater than the nonce of the previous request.

	cReq, err := toContractRequest(dbReq)
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
