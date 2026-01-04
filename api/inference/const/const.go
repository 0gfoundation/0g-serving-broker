package constant

import "time"

var (
	ServicePrefix = "/v1/proxy"

	TargetRoute = map[string]struct{}{
		"/chat/completions":     {},
		"/images/edits": {},
		"/images/generations":   {},
		"/audio/transcriptions": {},
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

	// Response fee reservation factor for balance adequacy validation:  chatbot, speech-to-text,
	ResponseFeeReservationFactor = int64(1000000)

	// Response fee reservation factor for balance adequacy validation: text-to-image
	ResponseFeeReservationFactorForImage = int64(100)

	// TEE settlement batch size to avoid gas limit issues
	TEESettlementBatchSize = 50

	SkipUntilDuration = 8 * time.Hour
)
