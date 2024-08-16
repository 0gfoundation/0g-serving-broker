package util

import (
	"errors"
	"math/big"
	"net/http"
)

// e.g. 0x9536de7dab6e2942965234edbe20450
func HexadecimalStringToBigInt(hexString string) (*big.Int, error) {
	bigInt := new(big.Int)

	_, success := bigInt.SetString(hexString[2:], 16) // Remove "0x" prefix
	if !success {
		return nil, errors.New("failed to convert hex string to big.Int")
	}
	return bigInt, nil
}

func Max(old, new *int64) *int64 {
	if new == nil {
		return old
	}
	if old == nil {
		return new
	}
	if *new > *old {
		return new
	}
	return old
}

func SetHeaders(req *http.Request, headers map[string]string) {
	for key, value := range headers {
		req.Header.Set(key, value)
	}
}
