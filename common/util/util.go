package util

import (
	"math/big"
)

func ToBigInt(value int64) *big.Int {
	return new(big.Int).SetInt64(value)
}
