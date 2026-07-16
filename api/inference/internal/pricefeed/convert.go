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

// priceQuantumWei is the coarsest granularity we snap wei-per-token prices to.
// 1e10 wei/token == 0.01 0G per million tokens — two decimal places in 0G/M
// space.  Floor-quantising here buys stable, human-readable on-chain prices
// across small rate wobbles and reduces SyncService churn, at the cost of
// at most `priceQuantumWei * rate` USD/M of drift per tick (well below
// MinOnChainUpdateBps).  Floor favours the user: the broker can never
// accidentally charge more than the unquantised conversion would yield.
var priceQuantumWei = big.NewInt(1e10)

// minPriceQuantumWei is the finest grid quantumFor will step down to.
// 1e8 wei/token == 0.0001 0G per million tokens — four decimals in 0G/M space,
// still human-readable. Cheap models (sub-1 0G/M) need a finer grid than the
// coarse default, otherwise truncation loses a large fraction of their price:
// e.g. qwen3-vl at 0.065 0G/M snapped to the 0.01 grid becomes 0.06, a ~7.5%
// under-charge (router#332).
var minPriceQuantumWei = big.NewInt(1e8)

// quantumFor returns the floor-snap grid for an unquantised wei-per-token price.
// It picks the largest power-of-ten grid that keeps the snap error within ~1%
// of the price (grid <= price/100), clamped to [minPriceQuantumWei,
// priceQuantumWei]. Expensive models keep the coarse 0.01 0G/M grid (stable,
// low churn); cheap models get a finer grid so their price survives truncation.
// The ~1% bound holds except right at the minPriceQuantumWei clamp (prices just
// above 1e8 wei), where the forced 1e8 grid can leave a larger relative floor
// error — still a floor, so it only ever under-charges, never over-charges.
//
// The grid only sizes the floor snap — it never changes the snap direction, so
// floor-favours-user still holds. SyncService churn is unaffected: on-chain
// updates remain gated by MinOnChainUpdateBps, which suppresses sub-threshold
// price moves regardless of grid resolution; a finer grid only makes the value
// that eventually syncs more precise.
func quantumFor(wei *big.Int) *big.Int {
	if wei.Sign() <= 0 {
		return new(big.Int).Set(priceQuantumWei)
	}
	target := new(big.Int).Quo(wei, big.NewInt(100))
	q := floorPowerOfTen(target)
	if q.Cmp(minPriceQuantumWei) < 0 {
		return new(big.Int).Set(minPriceQuantumWei)
	}
	if q.Cmp(priceQuantumWei) > 0 {
		return new(big.Int).Set(priceQuantumWei)
	}
	return q
}

// floorPowerOfTen returns the largest power of ten (10^k, k>=0) that is <= n.
// For n < 1 it returns 1.
func floorPowerOfTen(n *big.Int) *big.Int {
	p := big.NewInt(1)
	if n.Sign() <= 0 {
		return p
	}
	ten := big.NewInt(10)
	for {
		next := new(big.Int).Mul(p, ten)
		if next.Cmp(n) > 0 {
			return p
		}
		p = next
	}
}

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
// wei-per-token price using the supplied USD-per-0G rate, then floor-
// quantises the result to priceQuantumWei.
//
//	weiPerToken = floor( priceUSD / 1_000_000 / rateUSDPerOG * 1e18 ,  priceQuantumWei )
//
// All intermediate math uses big.Rat for exact arithmetic; the final
// result is floor-truncated and then snapped down to the nearest
// multiple of priceQuantumWei.  Returns an error if rate is zero or
// negative, which a healthy aggregator should never produce.
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

	// Floor-snap to an adaptive power-of-ten grid sized to the price, so cheap
	// models keep enough significant digits (see quantumFor): wei = (wei / Q) * Q.
	q := quantumFor(wei)
	wei.Quo(wei, q).Mul(wei, q)
	return wei, nil
}

// USDPerMillionStringToPerToken parses a USD-per-1M-tokens decimal string
// (e.g. "0.50"), divides exactly by 1_000_000, and returns the per-token
// price as a decimal string with trailing zeros trimmed (e.g. "0.0000005").
// Uses big.Rat throughout so there is no float precision loss.
//
// The formatting precision (18 decimals before trimming) matches wei-unit
// resolution — any sensible configured price has fewer significant digits
// than that, so the trimmed output is the shortest exact representation.
//
// Empty, negative, or unparseable input returns an error; the caller is
// expected to have already validated the config value upstream.
func USDPerMillionStringToPerToken(s string) (string, error) {
	perMillion, err := ParseUSDPerMillion(s)
	if err != nil {
		return "", err
	}
	perToken := new(big.Rat).Quo(perMillion, new(big.Rat).SetInt(tokensPerMillion))
	// FloatString pads to the requested precision; strip the noise.
	out := perToken.FloatString(18)
	return TrimTrailingZeros(out), nil
}

// TrimTrailingZeros removes trailing zeros after a decimal point and, if
// that leaves a bare decimal point, drops it too.  "0.500000" -> "0.5",
// "1.000000" -> "1", "10" -> "10" (unchanged). Exported so other packages
// formatting their own big.Rat-derived decimal strings (e.g. handler.models'
// per-resolution USD variant prices) get identical trimming behavior instead
// of a second, potentially-diverging implementation.
func TrimTrailingZeros(s string) string {
	if !strings.ContainsRune(s, '.') {
		return s
	}
	i := len(s)
	for i > 0 && s[i-1] == '0' {
		i--
	}
	if i > 0 && s[i-1] == '.' {
		i--
	}
	return s[:i]
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
