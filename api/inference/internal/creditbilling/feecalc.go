package creditbilling

import (
	"math/big"

	"github.com/pkg/errors"
)

// microPerMillion is the divisor relating "micro-USD per million tokens" prices
// to micro-USD fees: feeMicroUsd = priceMicroUsdPerMillion * count / 1_000_000.
var microPerMillion = big.NewInt(1_000_000)

// FeeMicroUsd computes the integer micro-USD fee for a request from token counts
// and per-million-token prices expressed in micro-USD.
//
//	feeMicroUsd = (inputCount*inPriceMicroPerMillion + outputCount*outPriceMicroPerMillion) / 1_000_000
//
// big.Int is used for the intermediate product to avoid int64 overflow; the
// result is floored (truncated) to an integer micro-USD. It returns an error if
// any input is negative or if the computed fee does not fit in int64 — billing
// money must never silently wrap (a wrapped value could undercharge, or go
// negative and credit the user).
func FeeMicroUsd(inputCount, outputCount, inPriceMicroPerMillion, outPriceMicroPerMillion int64) (int64, error) {
	if inputCount < 0 || outputCount < 0 || inPriceMicroPerMillion < 0 || outPriceMicroPerMillion < 0 {
		return 0, errors.Errorf("fee inputs must be non-negative: in=%d out=%d inPrice=%d outPrice=%d",
			inputCount, outputCount, inPriceMicroPerMillion, outPriceMicroPerMillion)
	}
	in := new(big.Int).Mul(big.NewInt(inputCount), big.NewInt(inPriceMicroPerMillion))
	out := new(big.Int).Mul(big.NewInt(outputCount), big.NewInt(outPriceMicroPerMillion))
	sum := new(big.Int).Add(in, out)
	sum.Quo(sum, microPerMillion)
	if !sum.IsInt64() {
		return 0, errors.Errorf("computed fee overflows int64: %s micro-USD", sum.String())
	}
	return sum.Int64(), nil
}

// UsdDecimalToMicro converts a decimal USD string (e.g. "2", "0.04", "1.5") to an
// integer micro-USD value (USD * 1_000_000), truncating beyond micro precision.
// Used to normalize configured USD-per-million prices into the integer domain.
// Returns an error on parse failure, negative input, or int64 overflow.
func UsdDecimalToMicro(s string) (int64, error) {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return 0, errors.Errorf("parse USD decimal %q: invalid syntax", s)
	}
	if r.Sign() < 0 {
		return 0, errors.Errorf("USD price must be non-negative: %q", s)
	}
	r.Mul(r, new(big.Rat).SetInt64(1_000_000))
	// Floor to integer micro-USD.
	micro := new(big.Int).Quo(r.Num(), r.Denom())
	if !micro.IsInt64() {
		return 0, errors.Errorf("USD price %q overflows int64 micro-USD", s)
	}
	return micro.Int64(), nil
}
