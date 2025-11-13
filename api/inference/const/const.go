package constant

var (
	ServicePrefix = "/v1/proxy"

	TargetRoute = map[string]struct{}{
		"/chat/completions":   {},
		"/images/generations": {},
	}

	// Keep this as to remove duplicate headers from incoming request
	RequestMetaDataDuplicate = map[string]struct{}{
		"Address":           {},
		"Fee":               {},
		"Input-Fee":         {},
		"Nonce":             {},
		"Request-Hash":      {},
		"Signature":         {},
		"Session-Token":     {},
		"Session-Signature": {},
		"Authorization":     {},
	}

	// Should align with the topUpTriggerThreshold in the client sdk
	SettleTriggerThreshold = int64(1000000)

	// Response fee reservation factor for balance adequacy validation
	ResponseFeeReservationFactor = int64(1000000)

	// TEE settlement batch size to avoid gas limit issues
	TEESettlementBatchSize = 50
)
