package constant

var (
	ServicePrefix = "/v1/proxy"

	TargetRoute = map[string]struct{}{
		"/chat/completions": {},
	}

	SettleFeeRoute = "/settle-fee"

	RequestMetaData = map[string]struct{}{
		"Address":             {},
		"Fee":                 {},
		"Input-Fee":           {},
		"Nonce":               {},
		"Previous-Output-Fee": {},
		"Signature":           {},
	}

	// Should align with the topUpTriggerThreshold in the client sdk
	SettleTriggerThreshold = int64(5000)
)
