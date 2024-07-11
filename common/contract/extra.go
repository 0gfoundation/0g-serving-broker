package contract

import (
	"bytes"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

func (r Request) GetMessage(serviceProviderAddress string) (common.Hash, error) {
	buf := new(bytes.Buffer)
	buf.Write(common.HexToAddress(serviceProviderAddress).Bytes())
	buf.Write(r.UserAddress.Bytes())
	buf.Write([]byte(r.ServiceName))
	buf.Write(common.LeftPadBytes(r.InputCount.Bytes(), 32))
	buf.Write(common.LeftPadBytes(r.PreviousOutputCount.Bytes(), 32))
	buf.Write(common.LeftPadBytes(r.Nonce.Bytes(), 32))
	buf.Write(common.LeftPadBytes(r.CreatedAt.Bytes(), 32))

	msg := crypto.Keccak256Hash(buf.Bytes())
	prefixedMsg := crypto.Keccak256Hash([]byte("\x19Ethereum Signed Message:\n32"), msg.Bytes())

	return prefixedMsg, nil
}

func (r Request) GetSignature(keyHex string, provide string) ([]byte, error) {
	key, err := crypto.HexToECDSA(keyHex)
	if err != nil {
		return nil, err
	}

	msg, err := r.GetMessage(provide)
	if err != nil {
		return nil, err
	}
	sig, err := crypto.Sign(msg.Bytes(), key)
	if err != nil {
		return nil, err
	}

	// https://github.com/ethereum/go-ethereum/issues/19751#issuecomment-504900739
	if sig[64] == 0 || sig[64] == 1 {
		sig[64] += 27
	}

	return sig, nil
}
