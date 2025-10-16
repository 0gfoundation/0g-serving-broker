package constant

var (
	ServicePrefix = "/v1/proxy"

	TargetRoute = map[string]struct{}{
		"/chat/completions": {},
	}

	// Keep this as to remove duplicate headers from incoming request
	RequestMetaDataDuplicate = map[string]struct{}{
		"Address":           {},
		"Fee":               {},
		"Input-Fee":         {},
		"Nonce":             {},
		"Request-Hash":      {},
		"Signature":         {},
		"VLLM-Proxy":        {},
		"Session-Token":     {},
		"Session-Signature": {},
	}

	RequestMetaData = map[string]struct{}{
		"Address":           {},
		"VLLM-Proxy":        {},
		"Session-Token":     {},
		"Session-Signature": {},
	}

	// Should align with the topUpTriggerThreshold in the client sdk
	SettleTriggerThreshold = int64(1000000)

	// Response fee reservation factor for balance adequacy validation
	ResponseFeeReservationFactor = int64(1000000)

	// TEE settlement batch size to avoid gas limit issues
	TEESettlementBatchSize = 50
)
