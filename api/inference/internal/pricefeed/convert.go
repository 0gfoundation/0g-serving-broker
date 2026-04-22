package pricefeed

import (
	"fmt"
	"math/big"
	"strings"
)

// weiPerOG is 10^18 — one 0G token in wei. Matches the precision used by the
// serving contract for on-chain prices.
var weiPerOG = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)

// tokensPerMillion is the denominator for "USD per 1M tokens" pricing.
var tokensPerMillion = big.NewInt(1_000_000)

// ParseUSDPerMillion parses a USD-per-million-tokens decimal string into a
// big.Rat.  Accepts plain decimal notation (e.g. "0.50", "1.234567").
// Rejects negative numbers and empty strings.
func ParseUSDPerMillion(s string) (*big.Rat, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("usd price is empty")
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, fmt.Errorf("usd price %q is not a valid decimal", s)
	}
	if r.Sign() < 0 {
		return nil, fmt.Errorf("usd price %q must be non-negative", s)
	}
	return r, nil
}

// USDPerMillionToWeiPerToken converts a USD-per-1M-tokens price to a
// wei-per-token price using the supplied USD-per-0G rate.
//
//	weiPerToken = priceUSD / 1_000_000 / rateUSDPerOG * 1e18
//
// All intermediate math uses big.Rat for exact arithmetic; the final
// result is truncated to a non-negative big.Int (rounding down favours
// the user by at most 1 wei per token).  Returns an error if rate is
// zero or negative, which a healthy aggregator should never produce.
func USDPerMillionToWeiPerToken(priceUSDPerMillion, rateUSDPerOG *big.Rat) (*big.Int, error) {
	if priceUSDPerMillion == nil {
		return nil, fmt.Errorf("priceUSDPerMillion is nil")
	}
	if rateUSDPerOG == nil {
		return nil, fmt.Errorf("rateUSDPerOG is nil")
	}
	if rateUSDPerOG.Sign() <= 0 {
		return nil, fmt.Errorf("rateUSDPerOG must be > 0, got %s", rateUSDPerOG.FloatString(12))
	}

	// priceUSDPerToken = priceUSDPerMillion / 1_000_000
	// ogPerToken       = priceUSDPerToken / rateUSDPerOG
	// weiPerToken      = ogPerToken * 1e18
	out := new(big.Rat).Quo(priceUSDPerMillion, new(big.Rat).SetInt(tokensPerMillion))
	out.Quo(out, rateUSDPerOG)
	out.Mul(out, new(big.Rat).SetInt(weiPerOG))

	// big.Rat → big.Int, floor (non-negative by construction)
	num := out.Num()
	den := out.Denom()
	wei := new(big.Int).Quo(num, den)
	if wei.Sign() < 0 {
		wei.SetInt64(0)
	}
	return wei, nil
}

// DriftBps returns the absolute difference between `current` and `reference`,
// expressed in basis points (1/10000) of `reference`.  When reference is zero
// any non-zero change is treated as infinite drift (returns math.MaxInt).
func DriftBps(current, reference *big.Int) int {
	if reference == nil || reference.Sign() == 0 {
		if current == nil || current.Sign() == 0 {
			return 0
		}
		return int(^uint(0) >> 1) // math.MaxInt
	}
	diff := new(big.Int).Sub(current, reference)
	diff.Abs(diff)
	diff.Mul(diff, big.NewInt(10000))
	diff.Quo(diff, new(big.Int).Abs(reference))
	if !diff.IsInt64() {
		return int(^uint(0) >> 1)
	}
	v := diff.Int64()
	if v > int64(int(^uint(0)>>1)) {
		return int(^uint(0) >> 1)
	}
	return int(v)
}
