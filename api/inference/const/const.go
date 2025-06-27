package constant

import "time"

var (
	ServicePrefix = "/v1/proxy"

	TargetRoute = map[string]struct{}{
		"/chat/completions": {},
	}

	RequestMetaData = map[string]struct{}{
		"Address":      {},
		"Fee":          {},
		"Input-Fee":    {},
		"Nonce":        {},
		"Request-Hash": {},
		"Signature":    {},
		"VLLM-Proxy":   {},
	}

	// Should align with the topUpTriggerThreshold in the client sdk
	SettleTriggerThreshold = int64(5000)

	// Threshold for nonce time difference to be considered valid (nano seconds, 1 minute)
	NonceTimeThreshold = time.Duration(60 * time.Second)
)
