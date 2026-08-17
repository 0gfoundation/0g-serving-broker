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

// Assay (Immaculate/LDD) verifiable-inference integration.
const (
	// HeaderZGVerdict is the response header the Assay verifier sets with the
	// per-request LDD audit result (one of the AssayVerdict* values below).
	HeaderZGVerdict = "ZG-Verdict"

	// HeaderZGVerdictSig is the verifier's Ed25519 signature (base64) over
	// AssayVerdictDomain + "|" + verdict + "|" + requestHash. It binds the
	// verdict to this broker request so the settlement-affecting header can't
	// be forged or replayed by whatever sits at targetUrl.
	HeaderZGVerdictSig = "ZG-Verdict-Sig"

	// HeaderZGRequestHash is sent BY the broker ON the upstream request: the
	// request's settlement hash. The verifier folds it into the signed verdict
	// payload, making each signature single-use.
	HeaderZGRequestHash = "ZG-Request-Hash"

	// AssayVerdictDomain domain-separates verdict signatures from the GPU
	// node's commitment signatures ("assay-commitment-v1").
	AssayVerdictDomain = "assay-verdict-v1"

	// Assay verdict values reported via HeaderZGVerdict. UNVERIFIED means the
	// request was sampled out of auditing; REJECT and INVALID_SIG are acted on
	// at settlement.
	AssayVerdictPass       = "PASS"
	AssayVerdictReject     = "REJECT"
	AssayVerdictUnverified = "UNVERIFIED"
	// AssayVerdictPending is the verifier's non-blocking response verdict: the
	// request was sampled for audit and the LDD recompute is still running in
	// the background. The final verdict is fetched from the verifier's
	// POST /v1/settlement/check before the request may settle.
	AssayVerdictPending = "PENDING"
	// AssayVerdictUnknown is returned by the verifier's settlement check for a
	// request hash it has no record of (e.g. verifier restart; the verdict
	// store is in-memory). Treated as no-information: fail-open outside strict
	// mode, kept pending in strict mode.
	AssayVerdictUnknown = "UNKNOWN"
	// AssayVerdictInvalidSig is recorded locally (never sent by the verifier)
	// when assay.strictVerdict is on and a response's verdict is missing or
	// fails signature verification — such requests are excluded from
	// settlement like REJECTs.
	AssayVerdictInvalidSig = "INVALID_SIG"
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

	// AssayPendingRetryDelay parks a request whose Assay audit is still
	// PENDING at settlement time. Much shorter than SkipUntilDuration: the
	// audit backlog usually drains in seconds, so the request should be
	// re-evaluated on the next settlement cycle, not an hour later.
	AssayPendingRetryDelay = 5 * time.Minute

	// EIP-712 constants matching the contract
	// DOMAIN_TYPEHASH = keccak256("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)")
	DomainTypehash = crypto.Keccak256Hash([]byte("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"))

	// SETTLEMENT_TYPEHASH = keccak256("TEESettlement(bytes32 requestsHash,uint256 nonce,address provider,address user,uint256 totalFee)")
	SettlementTypehash = crypto.Keccak256Hash([]byte("TEESettlement(bytes32 requestsHash,uint256 nonce,address provider,address user,uint256 totalFee)"))

	// Domain constants
	DomainName    = "0G Inference Serving"
	DomainVersion = "1"
)
