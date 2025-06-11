package constant

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
		"Use-Proxy":    {},
	}

	// Should align with the topUpTriggerThreshold in the client sdk
	SettleTriggerThreshold = int64(5000)
)
