package constant

import (
	"time"

	"github.com/ethereum/go-ethereum/crypto"
)

// Service type constants matching on-chain service type values.
const (
	ServiceTypeChatbot         = "chatbot"
	ServiceTypeTextToImage     = "text-to-image"
	ServiceTypeImageEditing    = "image-editing"
	ServiceTypeSpeechToText    = "speech-to-text"
	ServiceTypeVideoGeneration = "video-generation"
)

// Provider type constants for distinguishing between decentralized GPU providers
// and centralized API providers (e.g., OpenAI, Anthropic).
const (
	ProviderTypeDecentralized = "decentralized"
	ProviderTypeCentralized   = "centralized"
)

// Billing unit constants for the reconciliation rollup (Request.Unit /
// HourlyUsageStat.Unit). They label what InputCount/OutputCount are measured in, which
// varies by service type — and, for speech-to-text, by the response shape (whisper bills
// by seconds, gpt-4o-transcribe by tokens). See docs/design/provider-reconciliation.md.
const (
	BillingUnitTokens  = "tokens"
	BillingUnitSeconds = "seconds"
	BillingUnitImages  = "images"
)

// UpstreamSelf labels a request served by the provider's own engine (decentralized /
// TeeML), i.e. no external vendor to reconcile against.
const UpstreamSelf = "self"

// DefaultBillingUnitForService returns the billing unit a service type bills in by
// default. Speech-to-text defaults to seconds (whisper); the token-billed
// gpt-4o-transcribe path overrides it to tokens where the counts are finalized.
func DefaultBillingUnitForService(serviceType string) string {
	switch serviceType {
	case ServiceTypeTextToImage, ServiceTypeImageEditing:
		return BillingUnitImages
	case ServiceTypeSpeechToText:
		return BillingUnitSeconds
	default:
		// chatbot and video-generation bill in tokens.
		return BillingUnitTokens
	}
}

// Known centralized provider identities.
const (
	CentralizedProviderOpenAI    = "openai"
	CentralizedProviderAnthropic = "anthropic"
)

// Price denomination modes for provider-configured service prices.
// NATIVE: inputPrice/outputPrice are configured directly in wei (0G) and written
//         to chain as-is; existing behavior.
// USD:    inputPriceUSDPerMillionTokens/outputPriceUSDPerMillionTokens are configured in USD and converted to
//         wei by the PriceUpdateProcessor using a live 0G/USDT rate.
const (
	PriceDenominationNative = "NATIVE"
	PriceDenominationUSD    = "USD"
)

// KnownCentralizedProviderURLs maps provider identity to their default API base URLs.
var KnownCentralizedProviderURLs = map[string]string{
	CentralizedProviderOpenAI:    "https://api.openai.com",
	CentralizedProviderAnthropic: "https://api.anthropic.com",
}

var (
	ServicePrefix = "/v1/proxy"

	TargetRoute = map[string]struct{}{
		"/messages":             {}, // LiteLLM/Claude API format
		"/v1/messages":          {}, // For Claude Code client compatibility
		"/chat/completions":     {},
		"/images/edits":         {},
		"/images/generations":   {},
		"/audio/transcriptions": {},
		"/videos":               {}, // Video generation (OpenAI Video API)
	}

	// FreePrefixes defines path prefixes that can be accessed without charging
	// These are typically metadata or system endpoints that don't consume GPU resources
	// Note: Paths here should NOT include /v1/proxy prefix (it's already stripped)
	FreePrefixes = []string{
		"/attestation", // TEE attestation endpoints (e.g., /attestation/report)
		"/signature",   // TEE signature endpoints (e.g., /signature/{chatID})
	}

	// AuthRequiredPrefixes defines path prefixes that require session validation
	// but do not require billing. These are typically async status/retrieval endpoints
	// where the initial creation was already billed.
	// Note: Paths here should NOT include /v1/proxy prefix (it's already stripped)
	AuthRequiredPrefixes = []string{
		"/videos/", // Video status and content retrieval (e.g., /videos/{id}, /videos/{id}/content)
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

	// Response fee reservation factor for balance adequacy validation: text-to-image, video-generation
	ResponseFeeReservationFactorForImage = int64(100)

	// MinimumLockedBalance is the fixed minimum locked balance required for all service types (1 0G in neuron).
	// This replaces the dynamic per-service-type calculation in balance adequacy validation.
	MinimumLockedBalance = "1000000000000000000"

	// TEE settlement batch size to avoid gas limit issues
	TEESettlementBatchSize = 50

	SkipUntilDuration = 1 * time.Hour

	// EIP-712 constants matching the contract
	// DOMAIN_TYPEHASH = keccak256("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)")
	DomainTypehash = crypto.Keccak256Hash([]byte("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"))

	// SETTLEMENT_TYPEHASH = keccak256("TEESettlement(bytes32 requestsHash,uint256 nonce,address provider,address user,uint256 totalFee)")
	SettlementTypehash = crypto.Keccak256Hash([]byte("TEESettlement(bytes32 requestsHash,uint256 nonce,address provider,address user,uint256 totalFee)"))

	// Domain constants
	DomainName    = "0G Inference Serving"
	DomainVersion = "1"
)
