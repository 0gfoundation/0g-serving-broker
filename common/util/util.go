package util

import (
	"math/big"
	"net/http"
)

func ToBigInt(value int64) *big.Int {
	return new(big.Int).SetInt64(value)
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
