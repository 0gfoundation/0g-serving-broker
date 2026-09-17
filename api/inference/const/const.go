package constant

import (
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
)

// Service type constants matching on-chain service type values.
const (
	ServiceTypeChatbot      = "chatbot"
	ServiceTypeTextToImage  = "text-to-image"
	ServiceTypeImageEditing = "image-editing"
	ServiceTypeSpeechToText = "speech-to-text"
	// ServiceTypeEmbedding is the OpenAI Embeddings API shape (POST
	// /embeddings): synchronous (no streaming — the real OpenAI Embeddings
	// API has no `stream` parameter), billed on input tokens only — the
	// response's `usage` carries no completion_tokens.
	ServiceTypeEmbedding       = "embedding"
	ServiceTypeVideoGeneration = "video-generation"
	// ServiceTypeAudioGeneration is asynchronous audio generation (POST
	// /audio/generations, then status and content): a script in, generated audio
	// out — dialogue, music, ambience and sound effects, not only speech. Billed
	// per second of OUTPUT audio.
	//
	// It is NOT the inverse of ServiceTypeSpeechToText and must not be folded into
	// it. STT consumes audio and bills the INPUT dimension; this produces audio and
	// bills the OUTPUT dimension. The router's fanOutPrices sorts service types into
	// exactly those two buckets and refuses a model that fits neither, so the two
	// have to be distinguishable there.
	//
	// The name is "audio-generation" rather than "text-to-speech" because the first
	// vendor (ByteDance Seed Audio 1.0) generates a whole scene in one pass. Calling
	// that text-to-speech would mislead every consumer that branches on the service
	// type, starting with the router's catalog labels.
	ServiceTypeAudioGeneration = "audio-generation"
)

// Provider type constants for distinguishing between decentralized GPU providers
// and centralized API providers (e.g., OpenAI, Anthropic).
//
// ProviderTypeStandard is a pure forwarder that performs NO TEE verification and
// deliberately hides its upstream: it never signs responses (no routing proof, no
// broker signature), never publishes a provider identity or upstream domain, and
// advertises verifiability "standard" so clients skip verification instead of
// attempting it. It is the non-verifiable sibling of the TEE ("TeeML") mode.
const (
	ProviderTypeDecentralized = "decentralized"
	ProviderTypeCentralized   = "centralized"
	ProviderTypeStandard      = "standard"
)

// VerifiabilityStandard is the verifiability marker written on-chain for a
// standard (pure-forwarder, non-verifiable) service. It is intentionally NOT one
// of the client-recognized verifiability values (OpML/TeeML/ZKML), so the
// user-broker treats the service as non-verifiable and never requests a
// signature.
const VerifiabilityStandard = "standard"

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
	case ServiceTypeSpeechToText, ServiceTypeVideoGeneration, ServiceTypeAudioGeneration:
		// Video bills the raw output seconds (resolution folded into rate_class, not the
		// unit); whisper STT also bills by seconds. See docs/design/provider-reconciliation.md.
		//
		// Audio generation bills output seconds too, and reuses this unit rather than
		// introducing one: a reconciliation query grouping on "seconds" means the same
		// thing for all three. What it does NOT mean is that they can be summed — STT's
		// seconds are audio consumed, audio-generation's are audio produced. The
		// service_type column is what separates them, which is exactly why this
		// function exists rather than the unit being inferred from the count alone.
		return BillingUnitSeconds
	default:
		// chatbot bills in tokens.
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
//
//	to chain as-is; existing behavior.
//
// USD:    inputPriceUSDPerMillionTokens/outputPriceUSDPerMillionTokens are configured in USD and converted to
//
//	wei by the PriceUpdateProcessor using a live 0G/USDT rate.
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
		"/embeddings":           {}, // Text embeddings (OpenAI Embeddings API)
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

// SplitTargetRoute derives, from a raw RequestURI as gin hands it to the proxy,
// the two paths the proxy works with:
//
//   - route: the upstream path, still carrying any query string, appended to the
//     service's target URL.
//   - matchPath: the path everything MATCHES against — TargetRoute (billing keys),
//     FreePrefixes, AuthRequiredPrefixes, and the E2EE profile's route scoping.
//
// It lives here, beside the tables it is matched against, because it must exist
// exactly once. A second derivation of matchPath was a bypass: the E2EE route
// guard asked `strings.HasSuffix(URL.Path, "/audio/transcriptions")` while the
// dispatcher asked about this string, so `/v1/proxy/signature/audio/transcriptions`
// was a free route to one and the transcription endpoint to the other — enough to
// get a sealed envelope opened on a route that neither bills nor seals its reply.
// Callers that need matchPath take it from here or are handed it; they do not
// rebuild it.
//
// The /v1 collapse lets callers that hardcode it after the broker base URL
// (Anthropic SDK → /v1/messages, OpenAI SDK → /v1/chat/completions) land on the
// same upstream path as bare /messages or /chat/completions. Service.targetUrl is
// expected to carry the /v1 segment for OpenAI-compatible upstreams (vLLM,
// OpenAI, OpenRouter, DashScope, RedPill, …), and LiteLLM aliases both variants,
// so this is safe across existing deployments. It is also why /chat/completions
// and /messages — without /v1 — are the canonical TargetRoute entries above.
//
// The trailing slash is trimmed to prevent a billing bypass: `/videos/` would
// otherwise miss TargetRoute["/videos"] and fall through to the auth-only path.
func SplitTargetRoute(requestURI string) (route, matchPath string) {
	route = strings.TrimPrefix(requestURI, ServicePrefix)
	if route == "/v1" || strings.HasPrefix(route, "/v1/") {
		route = strings.TrimPrefix(route, "/v1")
		if route == "" {
			route = "/"
		}
	}

	matchPath = route
	if idx := strings.Index(matchPath, "?"); idx != -1 {
		matchPath = matchPath[:idx]
	}
	if matchPath != "/" {
		matchPath = strings.TrimRight(matchPath, "/")
	}
	return route, matchPath
}
