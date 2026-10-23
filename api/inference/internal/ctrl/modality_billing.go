package ctrl

import (
	"fmt"
	"math/big"

	"github.com/0glabs/0g-serving-broker/inference/internal/modalprice"
)

// modalityPrices blends the per-token input and output prices for this
// usage's modality split (modalprice.Factors). It rounds exactly like the
// 0g-router: floor(price × factor) per token, the caller then multiplying by
// the token counts — so the fee the broker settles with the router equals what
// the router charged the user. A flat shape returns the prices unchanged.
func modalityPrices(p modalprice.Pricing, usage *Usage, inputPrice, outputPrice string) (string, string, error) {
	if p.Empty() {
		return inputPrice, outputPrice, nil
	}
	in, out := modalprice.Factors(p, modalityUsage(usage))
	inputPrice, err := scaleWeiPrice(inputPrice, in)
	if err != nil {
		return "", "", err
	}
	outputPrice, err = scaleWeiPrice(outputPrice, out)
	if err != nil {
		return "", "", err
	}
	return inputPrice, outputPrice, nil
}

// modalityUsage is the usage as the shared fee rule reads it (the router's
// modalityUsage, field for field).
func modalityUsage(u *Usage) modalprice.Usage {
	mu := modalprice.Usage{PromptTokens: int64(u.PromptTokens), CompletionTokens: int64(u.CompletionTokens)}
	if d := u.PromptTokensDetails; d != nil {
		mu.Split = d.TextTokens+d.ImageTokens+d.VideoTokens+d.AudioTokens > 0
		mu.ImageTokens, mu.VideoTokens, mu.AudioTokens = int64(d.ImageTokens), int64(d.VideoTokens), int64(d.AudioTokens)
	}
	if cd := u.CompletionTokensDetails; cd != nil {
		mu.AudioOutputTokens = int64(cd.AudioTokens)
	}
	return mu
}

// scaleWeiPrice floors an integer wei price times f; nil f is 1x.
func scaleWeiPrice(price string, f *big.Rat) (string, error) {
	if f == nil {
		return price, nil
	}
	p, ok := new(big.Int).SetString(price, 10)
	if !ok {
		return "", fmt.Errorf("modality pricing: failed to parse price %q as big.Int", price)
	}
	p.Mul(p, f.Num())
	return p.Quo(p, f.Denom()).String(), nil
}
