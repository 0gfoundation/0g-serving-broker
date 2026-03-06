package constant

import (
	"time"

	"github.com/ethereum/go-ethereum/crypto"
)

// Service type constants matching on-chain service type values.
const (
	ServiceTypeChatbot      = "chatbot"
	ServiceTypeTextToImage  = "text-to-image"
	ServiceTypeImageEditing = "image-editing"
	ServiceTypeSpeechToText = "speech-to-text"
)

var (
	ServicePrefix = "/v1/proxy"

	TargetRoute = map[string]struct{}{
		"/messages":             {}, // LiteLLM/Claude API format
		"/v1/messages":          {}, // For Claude Code client compatibility
		"/chat/completions":     {},
		"/images/edits":         {},
		"/images/generations":   {},
		"/audio/transcriptions": {},
	}

	// FreePrefixes defines path prefixes that can be accessed without charging
	// These are typically metadata or system endpoints that don't consume GPU resources
	// Note: Paths here should NOT include /v1/proxy prefix (it's already stripped)
	FreePrefixes = []string{
		"/attestation", // TEE attestation endpoints (e.g., /attestation/report)
		"/signature",   // TEE signature endpoints (e.g., /signature/{chatID})
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

	// EIP-712 constants matching the contract
	// DOMAIN_TYPEHASH = keccak256("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)")
	DomainTypehash = crypto.Keccak256Hash([]byte("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"))

	// SETTLEMENT_TYPEHASH = keccak256("TEESettlement(bytes32 requestsHash,uint256 nonce,address provider,address user,uint256 totalFee)")
	SettlementTypehash = crypto.Keccak256Hash([]byte("TEESettlement(bytes32 requestsHash,uint256 nonce,address provider,address user,uint256 totalFee)"))

	// Domain constants
	DomainName    = "0G Inference Serving"
	DomainVersion = "1"
)
