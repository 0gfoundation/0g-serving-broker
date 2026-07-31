package config

import (
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/0glabs/0g-serving-broker/common/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"gopkg.in/yaml.v2"
)

// validProviderIdentity matches lowercase alphanumeric identifiers with optional hyphens.
// This prevents issues with the colon-delimited routing proof text format.
var validProviderIdentity = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// normalizeProviderIdentity lowercases *identity in place and validates it
// against validProviderIdentity. Shared by the centralized (required) and
// standard (optional) providerType branches so both apply the identical
// format rule instead of drifting.
func normalizeProviderIdentity(identity *string) error {
	*identity = strings.ToLower(*identity)
	if !validProviderIdentity.MatchString(*identity) {
		return fmt.Errorf("invalid config: service.providerIdentity must be lowercase alphanumeric with optional hyphens (e.g., 'openai', 'anthropic'), got '%s'", *identity)
	}
	return nil
}

// validProviderCountry matches a two-letter ISO 3166-1 alpha-2 country code.
// Format-only check (not validated against the full code list) — the value is a
// display hint, so enforcing two ASCII letters is enough to keep it well-formed.
var validProviderCountry = regexp.MustCompile(`^[A-Z]{2}$`)

// validCanonicalID matches a bare lowercase canonical model identifier
// (e.g. "glm-5.1", "deepseek-v3", "whisper-large-v3"). Disallows slashes
// so namespaced names like "zai-org/GLM-5-FP8" cannot be set as canonical
// — those belong in ModelType (the on-chain advertised name) instead.
var validCanonicalID = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-]*$`)

// knownTokenBilledSTTModels enumerates the bare canonical names of STT
// models whose usage upstream is shaped as input/output tokens (rather than
// duration seconds). The list is intentionally exhaustive — pattern matching
// against a substring like "gpt-4o" would catch unrelated multimodal models.
// When OpenAI ships a new token-billed transcription model, add it here.
var knownTokenBilledSTTModels = map[string]struct{}{
	"gpt-4o-transcribe":      {},
	"gpt-4o-mini-transcribe": {},
}

// tokenBilledSTTCanonicalName returns the recognized bare canonical name if
// the given STT service is configured against a known token-billed model, or
// "" otherwise. Checks every field that might carry a model identifier
// (ModelType, UpstreamModel, CanonicalID, ModelAliases) and strips any
// "org/" namespace prefix so e.g. "openai/gpt-4o-transcribe" matches.
func tokenBilledSTTCanonicalName(svc *Service) string {
	candidates := []string{svc.ModelType, svc.UpstreamModel, svc.CanonicalID}
	candidates = append(candidates, svc.ModelAliases...)
	for _, c := range candidates {
		if c == "" {
			continue
		}
		bare := c
		if i := strings.LastIndex(c, "/"); i >= 0 {
			bare = c[i+1:]
		}
		if _, ok := knownTokenBilledSTTModels[bare]; ok {
			return bare
		}
	}
	return ""
}

// ModelArchitecture describes the model's input/output modalities.
type ModelArchitecture struct {
	Modality         string   `yaml:"modality" json:"modality"`                    // Required. e.g., "text->text", "text+image->text"
	InputModalities  []string `yaml:"inputModalities" json:"input_modalities"`     // Required. e.g., ["text"], ["text", "image"]
	OutputModalities []string `yaml:"outputModalities" json:"output_modalities"`   // Required. e.g., ["text"]
	InstructType     string   `yaml:"instructType" json:"instruct_type,omitempty"` // Optional. e.g., "none", "alpaca", "chatml"
	Tokenizer        string   `yaml:"tokenizer" json:"tokenizer,omitempty"`        // Optional. Tokenizer identifier, e.g., "cl100k_base", "o200k_base", "llama3"
}

// Validate checks that all required ModelArchitecture fields are set.
func (a *ModelArchitecture) Validate() error {
	if a.Modality == "" {
		return fmt.Errorf("service.modelInfo.architecture.modality is required")
	}
	if len(a.InputModalities) == 0 {
		return fmt.Errorf("service.modelInfo.architecture.inputModalities is required")
	}
	if len(a.OutputModalities) == 0 {
		return fmt.Errorf("service.modelInfo.architecture.outputModalities is required")
	}
	return nil
}

// API surface format identifiers used in ModelInfo.SupportedFormats. They name
// the request surface a chat model accepts: APIFormatOpenAI is the OpenAI
// /chat/completions shape, APIFormatAnthropic is the Anthropic /v1/messages
// shape. The request path enforces these against the resolved model (see
// ctrl.enforceRequestFormat) so a client can't hit a surface whose usage the
// upstream doesn't report (which would silently misbill).
const (
	APIFormatOpenAI    = "openai"
	APIFormatAnthropic = "anthropic"
)

// ModelInfo holds optional metadata for the /v1/models endpoint.
// These fields enrich the on-chain service data with static model details.
// When provided, name, description, contextLength, architecture, and supportedParameters are required.
type ModelInfo struct {
	Name                string                 `yaml:"name"`                // Required. Human-readable display name
	Description         string                 `yaml:"description"`         // Required. Model description
	ContextLength       int                    `yaml:"contextLength"`       // Required. Max context window size in tokens
	MaxCompletionTokens int                    `yaml:"maxCompletionTokens"` // Optional. Max output tokens
	Architecture        *ModelArchitecture     `yaml:"architecture"`        // Required. Model architecture details
	SupportedParameters []string               `yaml:"supportedParameters"` // Required. e.g., ["temperature", "top_p", "max_tokens"]
	SupportedFormats    []string               `yaml:"supportedFormats"`    // Optional. API surfaces this model accepts: "openai" (/chat/completions) and/or "anthropic" (/v1/messages). When omitted the model is unconstrained and accepts every surface (backward compatible); when set, requests on an undeclared surface are rejected (see ctrl.enforceRequestFormat).
	DefaultParameters   map[string]interface{} `yaml:"defaultParameters"`   // Optional. Default values for parameters, e.g., {"temperature": 0.7, "top_p": 0.9}
	TeeType             string                 `yaml:"teeType"`             // Optional. TEE hardware type, e.g., "TDX", "SEV", "SGX", "H100"
	ExpirationDate      string                 `yaml:"expirationDate"`      // Optional. Model availability expiration in RFC3339 format, e.g., "2026-12-31T00:00:00Z". After this instant the broker rejects requests for the model with HTTP 410.

	// VideoSizeRatios maps output resolution (e.g., "1280x720") to a cost multiplier.
	// Used for video generation billing: fee = seconds × sizeRatio × outputPrice.
	// Defaults are applied if not configured (see DefaultVideoSizeRatios).
	VideoSizeRatios map[string]float64 `yaml:"videoSizeRatios"`

	// expiresAt is the parsed form of ExpirationDate, populated by Validate at
	// load time so request-path expiration checks never re-parse the string.
	// Zero value means no expiration is configured.
	expiresAt time.Time `yaml:"-"`
}

// Validate checks that all required ModelInfo fields are set.
// serviceType is the service type (e.g., "chatbot", "video-generation") and controls
// which fields are required. For video-generation, contextLength is optional since
// video models don't have a token context window.
func (m *ModelInfo) Validate(serviceType string) error {
	if m.Name == "" {
		return fmt.Errorf("service.modelInfo.name is required")
	}
	if m.Description == "" {
		return fmt.Errorf("service.modelInfo.description is required")
	}
	if serviceType != "video-generation" && m.ContextLength <= 0 {
		return fmt.Errorf("service.modelInfo.contextLength is required and must be positive")
	}
	if m.Architecture == nil {
		return fmt.Errorf("service.modelInfo.architecture is required")
	}
	if err := m.Architecture.Validate(); err != nil {
		return err
	}
	if len(m.SupportedParameters) == 0 {
		return fmt.Errorf("service.modelInfo.supportedParameters is required")
	}
	// supportedFormats is optional, but when set every entry must name a surface
	// the request path knows how to enforce — a typo (e.g. "anthropc") would
	// otherwise silently block ALL traffic on that surface at request time.
	for _, f := range m.SupportedFormats {
		switch strings.ToLower(strings.TrimSpace(f)) {
		case APIFormatOpenAI, APIFormatAnthropic:
		default:
			return fmt.Errorf("service.modelInfo.supportedFormats contains unknown format %q (allowed: %q, %q)", f, APIFormatOpenAI, APIFormatAnthropic)
		}
	}
	// Parse the optional expiration once at load time so the request path never
	// re-parses it. An unparseable value is a config error (fail fast) rather
	// than a silently ignored string.
	if m.ExpirationDate != "" {
		t, err := time.Parse(time.RFC3339, m.ExpirationDate)
		if err != nil {
			return fmt.Errorf("service.modelInfo.expirationDate %q is not valid RFC3339 (e.g. \"2026-12-31T00:00:00Z\"): %w", m.ExpirationDate, err)
		}
		m.expiresAt = t
	}
	return nil
}

// ModelExpiration resolves the availability expiration for the given model,
// returning false when none is configured. Resolution mirrors /v1/models
// metadata: a per-model pricing entry's own ModelInfo wins wholesale (no
// field-level fallback), otherwise the service-level ModelInfo applies.
//
// In multi-model mode a model that resolves to no pricing entry (unknown, with
// no wildcard) returns false rather than inheriting the service-level
// expiration — such a request is not one this service serves, so it is left to
// the allowlist to reject as "not supported" instead of being mislabeled
// "expired".
func (s *Service) ModelExpiration(model string) (time.Time, bool) {
	mi := s.ModelInfo
	if s.HasMultiModelPricing() {
		// Resolve through the same path as the request allowlist (exact id, then
		// alias, then wildcard) so a request using a legacy alias is subject to the
		// SAME expiration gate as the canonical id — GetModelPricing alone would
		// miss aliases and let an alias bypass a model's 410-expiry.
		entry, _, ok := s.ResolveRequestedModel(model)
		if !ok || entry == nil {
			return time.Time{}, false
		}
		if entry.ModelInfo != nil {
			mi = entry.ModelInfo
		}
	}
	if mi != nil && !mi.expiresAt.IsZero() {
		return mi.expiresAt, true
	}
	return time.Time{}, false
}

// EffectiveModelInfo resolves the ModelInfo that applies to a request for the
// given model, or nil when none is configured. Resolution mirrors ModelExpiration
// and the /v1/models metadata: in multi-model mode a per-model pricing entry's own
// ModelInfo wins wholesale (resolved alias-aware via ResolveRequestedModel, so a
// legacy alias is subject to the same metadata as its canonical id), otherwise the
// service-level ModelInfo applies; an unknown multi-model id with no wildcard entry
// resolves to nil — it is not a model this service serves. In single-model mode the
// service-level ModelInfo is returned regardless of the requested name.
func (s *Service) EffectiveModelInfo(model string) *ModelInfo {
	mi := s.ModelInfo
	if s.HasMultiModelPricing() {
		entry, _, ok := s.ResolveRequestedModel(model)
		if !ok || entry == nil {
			return nil
		}
		if entry.ModelInfo != nil {
			mi = entry.ModelInfo
		}
	}
	return mi
}

// SupportedFormatsFor returns the explicitly-configured API surface formats
// (APIFormatOpenAI / APIFormatAnthropic) for the resolved model, resolving the
// per-model ModelInfo the same way EffectiveModelInfo does. It returns nil when
// none is configured — callers treat nil as "unconstrained" so services written
// before format enforcement keep accepting every surface (backward compatible).
func (s *Service) SupportedFormatsFor(model string) []string {
	mi := s.EffectiveModelInfo(model)
	if mi == nil {
		return nil
	}
	return mi.SupportedFormats
}

type Service struct {
	ServingURL  string `yaml:"servingUrl"`
	TargetURL   string `yaml:"targetUrl"`
	InputPrice  string `yaml:"inputPrice"`
	OutputPrice string `yaml:"outputPrice"`
	Type        string `yaml:"type"`
	ModelType   string `yaml:"model"`
	// UpstreamModel, when set, is the model identifier sent to the upstream targetUrl.
	// ModelType remains the identifier advertised on-chain and enforced on incoming
	// requests. Used to bridge a provider that wants to expose a stable public model
	// name while routing to an upstream that uses a different id (e.g. a
	// fallback where the public name is "zai-org/GLM-5-FP8" but expects
	// "z-ai/glm-5"). Empty means "send ModelType upstream as-is".
	UpstreamModel string `yaml:"upstreamModel"`
	// ModelAliases are legacy model identifiers accepted on incoming requests in
	// addition to ModelType. Allows changing the advertised model name without
	// breaking clients that still send the old name. Out-of-set requests are
	// still rejected.
	ModelAliases []string `yaml:"modelAliases"`
	// CanonicalID is the canonical model identifier this service maps to in the
	// router's catalog (e.g. "glm-5.1", "deepseek-v3"). Bare lowercase, no
	// namespace. Optional — when empty, the router falls back to its own
	// registry-based mapping from ModelType. Also accepted as a valid model
	// identifier on incoming requests (in addition to ModelType and ModelAliases).
	// Not written on-chain, so changing it does not invalidate user
	// acknowledgements.
	CanonicalID      string            `yaml:"canonicalId"`
	Verifiability    string            `yaml:"verifiability"`
	AdditionalSecret map[string]string `yaml:"additionalSecret" json:"-"` // json:"-": secret headers (upstream API key) never marshaled
	VerifierURL      string            `yaml:"verifierUrl"`
	TargetTeeAddress string            `yaml:"targetTeeAddress"`
	TargetSeparated  bool              `yaml:"targetSeparated"`
	ProviderStake    string            `yaml:"providerStake"` // Stake amount for first-time service registration (default: 100000000000000000000 = 100 0G)
	OwnedBy          string            `yaml:"ownedBy"`       // Optional. Organization name for the owned_by field in /v1/models (e.g., "0G Foundation")
	ModelInfo        *ModelInfo        `yaml:"modelInfo"`

	// ProviderType distinguishes between "decentralized" (GPU providers) and "centralized" (OpenAI, Anthropic).
	// Defaults to "decentralized" if not set.
	ProviderType string `yaml:"providerType"`
	// ProviderIdentity identifies the centralized provider (e.g., "openai", "anthropic").
	// Only used when ProviderType is "centralized".
	ProviderIdentity string `yaml:"providerIdentity"`
	// TargetTLSProxy declares that TargetURL is a protocol-translation sidecar
	// running INSIDE this broker's own TEE (same CVM, same TDX quote) rather than
	// the vendor endpoint itself — the deployment shape used for a vendor whose
	// wire protocol the broker doesn't speak natively (see api/videotranslator).
	//
	// It exists because a sidecar breaks the centralized routing proof's evidence
	// chain: the proof binds the leaf certificate of the connection that reached
	// the vendor, read from resp.TLS, but with a shim in front the broker's own hop
	// is plaintext HTTP on the compose network and the vendor handshake happens in
	// the shim. Setting this makes the broker take the fingerprint from the shim's
	// tee.HeaderUpstreamCertFingerprint response header instead, and waives the
	// HTTPS requirement on TargetURL.
	//
	// SECURITY: only set this when the target really is in-enclave. The header is a
	// plain string an upstream can put whatever it likes in; what makes it evidence
	// is that the shim is covered by the same attestation as the broker. Pointing
	// this at an external host would let that host dictate its own routing proof.
	// The broker never reads the header when this is false, so a rogue upstream on
	// an ordinary centralized deployment cannot forge a fingerprint.
	TargetTLSProxy bool `yaml:"targetTLSProxy"`
	// ProviderName is an optional human-readable display name for the provider
	// (e.g., "OpenAI", "Aliyun (CN)"). Surfaced as provider_name in /v1/models for
	// presentation only. Unlike ProviderIdentity (a lowercase machine key), this is
	// freeform and may carry brand casing. Applies to any provider type.
	ProviderName string `yaml:"providerName"`
	// ProviderCountry is an optional ISO 3166-1 alpha-2 country code (e.g., "US",
	// "CN") identifying the provider's jurisdiction, surfaced as provider_country
	// in /v1/models. Normalized to uppercase. Display/discovery hint only — it is
	// self-asserted by the broker and not verifiable. Applies to any provider type.
	ProviderCountry string `yaml:"providerCountry"`

	// PriceDenomination selects how input/output prices are expressed:
	//   "NATIVE" (default): InputPrice/OutputPrice are wei amounts, written to chain as-is.
	//   "USD":              InputPriceUSDPerMillionTokens/OutputPriceUSDPerMillionTokens are USD decimal strings.
	//                       The PriceUpdateProcessor converts them to wei using a live rate.
	PriceDenomination string `yaml:"priceDenomination"`
	// InputPriceUSDPerMillionTokens is the input-side price in USD per 1M
	// tokens, as a decimal string (e.g. "0.50" = $0.50 per 1M input tokens,
	// matching the convention used by OpenAI/Anthropic pricing tables).
	// Required iff PriceDenomination == "USD".
	InputPriceUSDPerMillionTokens string `yaml:"inputPriceUSDPerMillionTokens"`
	// OutputPriceUSDPerMillionTokens is the output-side price in USD per 1M
	// tokens, decimal string.  Required iff PriceDenomination == "USD".
	OutputPriceUSDPerMillionTokens string `yaml:"outputPriceUSDPerMillionTokens"`

	// OutputPriceUSDPerImage is the USD price per generated image for a
	// USD-denominated image-generation / image-editing service (decimal string,
	// e.g. "0.04" = $0.04 per image). It is REQUIRED — and the per-1M-token USD
	// fields are forbidden — for those service types under USD denomination,
	// because an image bills per image, not per token. At config load it is
	// normalized into OutputPriceUSDPerMillionTokens (×1e6, with the input side
	// fixed at "0") so the existing token-shaped USD machinery (price feed,
	// on-chain ceiling, wei conversion, /v1/models display) prices and advertises
	// it unchanged — the pipeline's ÷1e6 quantum cancels the ×1e6 normalization to
	// yield wei-per-image. Mirrors OutputPriceUSDPerSecond for video. See validate.
	OutputPriceUSDPerImage string `yaml:"outputPriceUSDPerImage"`

	// OutputPriceUSDPerSecond is the USD price per effective output second for a
	// USD-denominated single-model video-generation service (decimal string, e.g.
	// "0.4" = $0.40 per effective second). It is REQUIRED — and the per-1M-token
	// USD fields are forbidden — for that service type under USD denomination when
	// no modelPricing entries are configured, because video bills per output
	// second, not per token. At config load it is normalized into
	// OutputPriceUSDPerMillionTokens (×1e6, with the input side fixed at "0"), same
	// as OutputPriceUSDPerImage, so the existing token-shaped USD machinery prices
	// and advertises it unchanged. When modelPricing is configured, per-model
	// ModelPricingEntry.OutputPriceUSDPerSecond carries the price instead and this
	// field must be left empty. See validate.
	OutputPriceUSDPerSecond string `yaml:"outputPriceUSDPerSecond"`

	// ModelPricing defines per-model pricing for centralized providers that serve multiple models.
	// When configured, the broker validates requested models against this allowlist
	// and bills at model-specific rates instead of the single on-chain price.
	// On-chain registration uses max(model prices) as InputPrice/OutputPrice.
	// Only used when ProviderType is "centralized".
	ModelPricing []ModelPricingEntry `yaml:"modelPricing"`

	// modelPricingMap is a derived lookup map built during config validation.
	modelPricingMap map[string]*ModelPricingEntry `yaml:"-"`
	// modelAliasMap is a derived lookup from per-entry ModelAliases to the owning
	// entry, built alongside modelPricingMap. Lets the multi-model request path
	// accept a legacy model id and resolve it to its canonical pricing entry.
	modelAliasMap map[string]*ModelPricingEntry `yaml:"-"`

	// InjectBodyFields, when set, are top-level key/value pairs merged into the
	// JSON request body forwarded to a chatbot targetUrl. It lets an operator set
	// upstream defaults/overrides per provider without code changes. The canonical
	// use is OpenRouter provider routing — pin a backend while allowing fallbacks
	// on failure — but it is generic: e.g. force reasoning off on a route, or set
	// any other upstream-understood field:
	//
	//   injectBodyFields:
	//     provider:                  # OpenRouter provider-routing object
	//       order: ["DeepInfra"]
	//       allow_fallbacks: true
	//       require_parameters: true
	//     reasoning:
	//       enabled: false           # e.g. disable thinking on this route
	//
	// Server-config-wins: each injected key overwrites any client-supplied value
	// of the same name, so users cannot steer it. Key names are NOT validated
	// against an allowlist (the upstream is the authority on accepted keys), but:
	//   - a denylist of broker-critical fields (model, messages, stream,
	//     stream_options, lora_adapter_name) is REJECTED at load — overriding them
	//     would break model enforcement / usage-based billing / LoRA rewriting
	//     (see protectedInjectBodyFields);
	//   - at config load the map is normalized and checked JSON-serializable so a
	//     nested-object value (e.g. provider.max_price) cannot pass load yet fail
	//     every request at marshal time — see normalizeInjectBodyFields.
	// Empty or unset means the body is forwarded unchanged (backward compatible).
	// Only applied for the chatbot service type; rejected for others at load.
	InjectBodyFields map[string]interface{} `yaml:"injectBodyFields"`

	// StripBodyFields, when set, are top-level keys REMOVED from the JSON request
	// body before it is forwarded to a chatbot targetUrl. It is the surgical
	// counterpart of injectBodyFields: a denylist of client-supplied params the
	// upstream does not accept. The motivating case is OpenRouter returning 404 for
	// a request carrying a param the picked backend lacks (e.g. logprobs /
	// top_logprobs) — the broker strips it rather than letting the upstream reject
	// the whole request:
	//
	//   stripBodyFields:
	//     - logprobs
	//     - top_logprobs
	//
	// It is deliberately a denylist, NOT an allowlist derived from
	// modelInfo.supportedParameters: that field advertises sampling capabilities
	// for /v1/models and is not an exhaustive set of legal body keys, so using it
	// to strip would silently drop legitimate params (max_completion_tokens, n,
	// tools, response_format, …). A denylist can only remove keys the operator
	// names, so the worst case is a missed param that keeps 404-ing (loud,
	// discoverable) rather than a legitimate request silently mangled.
	//
	// The "worst case is a loud 404" guarantee holds only for params the upstream
	// REQUIRES/rejects. Stripping a param the upstream merely accepts changes
	// request behavior silently with no error — in particular do NOT strip
	// cost-affecting keys (max_tokens / max_completion_tokens, etc.): removing an
	// output cap can uncap generation and inflate the output-token bill the user
	// pays. List only params that actually cause upstream rejection.
	//
	// Matching is case-sensitive and exact: an entry must equal the JSON key the
	// client sends byte-for-byte ("logprobs", not "Logprobs"). A typo loads fine
	// but silently never matches; confirm via the per-request "stripped … field(s)"
	// log that the denylist is firing.
	//
	// Broker-critical fields (model, messages, stream, stream_options,
	// lora_adapter_name) are REJECTED at load — stripping them would break model
	// enforcement / usage-based billing / LoRA rewriting (same protected denylist
	// as injectBodyFields; see protectedInjectBodyFields). Names are otherwise NOT
	// validated against an allowlist (the upstream is the authority on accepted
	// keys). Applied BEFORE injectBodyFields, so an operator may strip a client's
	// value and then inject the server's own. Empty or unset means the body is
	// forwarded unchanged. Only applied for the chatbot service type; rejected for
	// others at load.
	StripBodyFields []string `yaml:"stripBodyFields"`
}

// IsCentralized returns true if this service routes to a centralized API provider.
func (s *Service) IsCentralized() bool {
	return s.ProviderType == constant.ProviderTypeCentralized
}

// IsStandard returns true if this service is a "standard" pure forwarder: no TEE
// verification, no response signing, and a hidden upstream (no provider identity
// or upstream domain published). See constant.ProviderTypeStandard.
func (s *Service) IsStandard() bool {
	return s.ProviderType == constant.ProviderTypeStandard
}

// IsForwarder returns true for provider types that proxy to an external upstream
// rather than co-locating the model with the broker (centralized and standard).
// These share forwarding behavior: TargetSeparated is forced, per-model pricing is
// allowed, and no LLM attestation is hosted locally.
func (s *Service) IsForwarder() bool {
	return s.IsCentralized() || s.IsStandard()
}

// IsUSDDenominated returns true if this service's prices are configured in USD
// and must be converted to wei by the price-feed subsystem.
func (s *Service) IsUSDDenominated() bool {
	return s.PriceDenomination == constant.PriceDenominationUSD
}

// PriceFeedConfig controls the 0G/USD rate feed used when service.priceDenomination == "USD".
// Rate is never persisted — it's a transient value inside each update tick. Only the derived
// wei prices are stored (in the in-memory cache and on-chain).
type PriceFeedConfig struct {
	// Sources lists the price-feed source identifiers to query in parallel.
	// Known identifiers: "coingecko", "binance".
	// The aggregator returns the median of healthy sources; at least MinQuorum
	// sources must respond successfully for an update to proceed.
	//
	// Each source's 0G trading symbol is hardcoded in the pricefeed factory
	// (see pricefeed.BuildSources); the symbols aren't an operator choice
	// because this broker only ever prices 0G.
	Sources []string `yaml:"sources"`
	// UpdateInterval is how often the processor fetches a fresh rate and
	// refreshes the in-memory wei price cache.
	UpdateInterval time.Duration `yaml:"updateInterval"`
	// StalenessThreshold rejects new requests (fail-closed) if the last
	// successful cache refresh is older than this.  Must be >= UpdateInterval.
	// Defaults to 3 × UpdateInterval when unset — three consecutive tick
	// failures (each of which runs its own retry loop first) before readers
	// start erroring, which scales naturally with the refresh cadence.
	StalenessThreshold time.Duration `yaml:"stalenessThreshold"`
	// MinOnChainUpdateBps is the drift threshold (in basis points, 1/10000)
	// between the newly-derived wei price and the currently-registered
	// on-chain price.  Drift <= threshold skips the on-chain tx; drift >
	// threshold triggers it.
	//
	// Unset / zero value is treated as "not configured" and resolves to the
	// default of 100 bps (1%).  Operators wanting "push on every change"
	// should set 1 (0.01%).
	MinOnChainUpdateBps int `yaml:"minOnChainUpdateBps"`
	// MaxRateDeviationBps is the max deviation (in bps) from the aggregated median
	// a single source may report before it's dropped as an outlier.
	MaxRateDeviationBps int `yaml:"maxRateDeviationBps"`
	// MinQuorum is the minimum number of healthy sources required to compute a
	// new rate. If fewer sources respond successfully, the tick is skipped and
	// the last good cache entry remains in use (subject to StalenessThreshold).
	MinQuorum int `yaml:"minQuorum"`
	// CoinGeckoAPIKey, when set, authenticates requests to CoinGecko so that
	// the per-key rate limit applies instead of the (much stricter) shared
	// anonymous limit.  Setting one is strongly recommended — the anonymous
	// free tier causes quorum failures regularly in production.  Whether
	// this key is a Demo (free) or Pro (paid) key is selected via
	// CoinGeckoKeyType; the two key tiers use different endpoints and
	// different request headers and are not interchangeable.
	CoinGeckoAPIKey string `yaml:"coinGeckoApiKey"`
	// CoinGeckoKeyType selects which CoinGecko key tier CoinGeckoAPIKey
	// belongs to.  Allowed values: "demo" (free tier — uses api.coingecko.com
	// with x-cg-demo-api-key) or "pro" (paid tier — uses pro-api.coingecko.com
	// with x-cg-pro-api-key).  Defaults to "demo" when CoinGeckoAPIKey is set,
	// since the free Demo tier (30 req/min) is sufficient for typical update
	// cadences.  Ignored when CoinGeckoAPIKey is empty.
	CoinGeckoKeyType string `yaml:"coinGeckoKeyType"`
	// UserAgent is the User-Agent header sent to every source.  Providers
	// using private rate-feed deployments or whitelisted plans can set a
	// stable identifier here so upstream operators can grant them higher
	// limits.  Defaults to "0g-serving-broker/pricefeed".
	UserAgent string `yaml:"userAgent"`
	// HTTPTimeout bounds per-request HTTP timeout for each source.
	HTTPTimeout time.Duration `yaml:"httpTimeout"`
}

// CacheTokenBillingConfig defines configuration for cache-aware token billing.
// When enabled, cache-READ input tokens (reported by the LLM via
// prompt_tokens_details.cached_tokens, or Anthropic's cache_read_input_tokens)
// are billed at a discounted rate: inputPrice / Divisor.
//
// Optionally, cache-WRITE tokens (cache creation) are billed at a premium in up
// to two tiers, mirroring how upstreams (Anthropic/Bedrock) price cache writes by
// TTL:
//
//   - The DEFAULT / 5-minute tier: inputPrice * WriteMultiplierNumerator /
//     WriteMultiplierDenominator. Applies to the OpenAI-path usage.cache_write_tokens,
//     to Anthropic's cache_creation.ephemeral_5m_input_tokens, and to any
//     cache-creation tokens with no TTL breakdown. Anthropic's 5-minute rate is
//     1.25x (num=5, den=4).
//   - The 1-hour tier: inputPrice * Write1hMultiplierNumerator /
//     Write1hMultiplierDenominator. Applies to Anthropic's
//     cache_creation.ephemeral_1h_input_tokens. Anthropic's 1-hour rate is 2x
//     (num=2, den=1). When left unset, 1-hour cache-write tokens fall back to the
//     default tier's multiplier (and, if that is also unset, to full input price).
//
// When the default write multiplier is unset (either field 0), cache-write tokens
// fall back to full input price (1x) — the prior behavior.
type CacheTokenBillingConfig struct {
	Enabled bool  `yaml:"enabled"` // Enable cache-aware billing (default: false)
	Divisor int64 `yaml:"divisor"` // Discount divisor for cache-read tokens (e.g., 4 means 25% of full price)
	// WriteMultiplierNumerator / WriteMultiplierDenominator express the DEFAULT
	// (5-minute) cache-write premium as a fraction of the input price
	// (fee = inputPrice * num / den). When set, the fraction must be >= 1x
	// (num >= den >= 1) since a cache write is a premium, never a discount; leaving
	// either at 0 disables the premium and bills cache-write tokens at full input
	// price.
	WriteMultiplierNumerator   int64 `yaml:"writeMultiplierNumerator"`
	WriteMultiplierDenominator int64 `yaml:"writeMultiplierDenominator"`
	// Write1hMultiplierNumerator / Write1hMultiplierDenominator express the 1-hour
	// cache-write premium the same way. Same >= 1x rule. When unset, 1-hour
	// cache-write tokens fall back to the default write multiplier above.
	Write1hMultiplierNumerator   int64 `yaml:"write1hMultiplierNumerator"`
	Write1hMultiplierDenominator int64 `yaml:"write1hMultiplierDenominator"`
}

// WriteMultiplierEnabled reports whether the default (5-minute) cache-write
// premium is configured (both fraction fields set to a usable value). Billing
// consults this before splitting out cache-write tokens; otherwise they bill at
// full input price.
func (c *CacheTokenBillingConfig) WriteMultiplierEnabled() bool {
	return c.Enabled && c.WriteMultiplierNumerator > 0 && c.WriteMultiplierDenominator > 0
}

// Write1hMultiplierEnabled reports whether a distinct 1-hour cache-write premium
// is configured. When false, 1-hour cache-write tokens fall back to the default
// write multiplier (see WriteMultiplierEnabled).
func (c *CacheTokenBillingConfig) Write1hMultiplierEnabled() bool {
	return c.Enabled && c.Write1hMultiplierNumerator > 0 && c.Write1hMultiplierDenominator > 0
}

// validateCacheTokenBilling rejects a divisor < 1 when caching is enabled: a 0
// divisor would divide-by-zero panic at billing time (fee = price*tokens/divisor)
// and a negative one would produce a garbage/negative discount. Each write
// multiplier fraction (default and 1-hour) is validated the same way: if either
// field of a fraction is set both must be >= 1 (a 0 denominator would
// divide-by-zero, a partial fraction is a config typo) and the fraction must be
// >= 1x. prefix labels the source (service-level "cacheTokenBilling" or a
// per-model entry).
func validateCacheTokenBilling(prefix string, c *CacheTokenBillingConfig) error {
	if c.Enabled && c.Divisor < 1 {
		return fmt.Errorf("invalid config: %s.divisor must be >= 1 when enabled, got %d", prefix, c.Divisor)
	}
	if err := validateWriteMultiplier(prefix, "writeMultiplier", c.WriteMultiplierNumerator, c.WriteMultiplierDenominator); err != nil {
		return err
	}
	if err := validateWriteMultiplier(prefix, "write1hMultiplier", c.Write1hMultiplierNumerator, c.Write1hMultiplierDenominator); err != nil {
		return err
	}

	defaultSet := c.WriteMultiplierNumerator != 0 || c.WriteMultiplierDenominator != 0
	write1hSet := c.Write1hMultiplierNumerator != 0 || c.Write1hMultiplierDenominator != 0

	// The 1-hour tier layers on top of the default (5-minute) tier. Configuring it
	// alone would silently bill the far more common 5-minute cache writes at full
	// input price (1x) while the upstream still charges a 5-minute premium — a
	// silent under-charge and almost never what the operator intended. Require the
	// default tier to be set explicitly (set it to 1/1 to deliberately bill
	// 5-minute cache writes at cost).
	if write1hSet && !defaultSet {
		return fmt.Errorf("invalid config: %s.write1hMultiplier is set but %s.writeMultiplier (the default/5-minute tier) is not; configure the default tier too (use 1/1 to bill 5-minute cache writes at full input price)", prefix, prefix)
	}
	// A 1-hour cache write is never cheaper than a 5-minute one upstream, so a
	// 1-hour premium below the default premium is almost certainly a transposed
	// config that would silently under-charge the 1-hour tier — the same class of
	// error the per-fraction num<den check guards against, at the cross-tier level.
	// Compare by cross-multiplication (both denominators are >= 1 once set).
	if defaultSet && write1hSet &&
		c.Write1hMultiplierNumerator*c.WriteMultiplierDenominator < c.WriteMultiplierNumerator*c.Write1hMultiplierDenominator {
		return fmt.Errorf("invalid config: %s.write1hMultiplier (%d/%d) must be >= writeMultiplier (%d/%d); a 1-hour cache write is never cheaper than a 5-minute one",
			prefix, c.Write1hMultiplierNumerator, c.Write1hMultiplierDenominator, c.WriteMultiplierNumerator, c.WriteMultiplierDenominator)
	}
	return nil
}

// validateWriteMultiplier enforces the >= 1x premium rule shared by both
// cache-write tiers. field is the yaml key stem ("writeMultiplier" or
// "write1hMultiplier") used in error messages. A fully-zero fraction is "unset"
// and passes; a partially-set or below-1x fraction is rejected.
func validateWriteMultiplier(prefix, field string, num, den int64) error {
	if num == 0 && den == 0 {
		return nil
	}
	if num < 1 || den < 1 {
		return fmt.Errorf("invalid config: %s.%sNumerator/%sDenominator must both be >= 1 when set, got %d/%d",
			prefix, field, field, num, den)
	}
	// The write multiplier is a PREMIUM (cache-write costs at least full input
	// price), so the fraction must be >= 1x. Rejecting numerator < denominator
	// catches the likely operator error of transposing the fields (e.g. writing
	// 1/2 when 2/1 was intended), which would silently DISCOUNT cache-write
	// tokens instead of surcharging them.
	if num < den {
		return fmt.Errorf("invalid config: %s.%sNumerator/%sDenominator is a premium and must be >= 1x (numerator >= denominator), got %d/%d",
			prefix, field, field, num, den)
	}
	return nil
}

// TieredPricingConfig defines input-length-based tiered pricing.
// Some models (e.g., Qwen) charge different rates based on input token count.
// The service is registered on-chain at the base (lowest tier) price.
// When actual input tokens fall into a higher tier, the base price is multiplied
// by the tier's multiplier BEFORE any other billing logic (e.g., cache token billing),
// so all downstream fee calculations use the correct tiered price.
type TieredPricingConfig struct {
	Enabled bool          `yaml:"enabled"` // Enable tiered pricing (default: false)
	Tiers   []PricingTier `yaml:"tiers"`   // Ordered list of pricing tiers (by maxInputTokens ascending)
}

// PricingTier defines a single pricing tier.
// Tiers must be ordered by MaxInputTokens ascending.
// The first tier whose MaxInputTokens >= promptTokens is selected.
// Use MaxInputTokens: 0 to represent an unbounded upper tier.
//
// The input/output multipliers are fractions: numerator (InputMultiplier) over
// denominator (InputMultiplierDenominator), so non-integer multiples like 1.5x
// (3/2) or 2.5x (5/2) are expressible. A zero/unset denominator defaults to 1,
// so a tier configured with only inputMultiplier keeps its integer-multiple
// meaning (backward compatible). Billing applies the fraction as
// price*num/den in integer big.Int to avoid precision loss (see
// applyTierMultiplier), mirroring cacheTokenBilling's writeMultiplier fraction.
type PricingTier struct {
	MaxInputTokens              int   `yaml:"maxInputTokens"`              // Upper bound of input tokens for this tier (0 = unlimited)
	InputMultiplier             int64 `yaml:"inputMultiplier"`             // Input price multiplier numerator for this tier
	OutputMultiplier            int64 `yaml:"outputMultiplier"`            // Output price multiplier numerator for this tier
	InputMultiplierDenominator  int64 `yaml:"inputMultiplierDenominator"`  // Input multiplier denominator (0/unset = 1, i.e. integer multiple)
	OutputMultiplierDenominator int64 `yaml:"outputMultiplierDenominator"` // Output multiplier denominator (0/unset = 1)
}

// EffectiveInputMultiplier returns the tier's input-price multiplier as a
// num/den fraction, with the denominator defaulting to 1 when unset (or
// non-positive) so a legacy integer-only tier keeps its meaning. den is always
// >= 1, so callers can divide without a zero check.
func (t PricingTier) EffectiveInputMultiplier() (num, den int64) {
	den = t.InputMultiplierDenominator
	if den <= 0 {
		den = 1
	}
	return t.InputMultiplier, den
}

// EffectiveOutputMultiplier is the output-side counterpart of
// EffectiveInputMultiplier. den is always >= 1.
func (t PricingTier) EffectiveOutputMultiplier() (num, den int64) {
	den = t.OutputMultiplierDenominator
	if den <= 0 {
		den = 1
	}
	return t.OutputMultiplier, den
}

// WhitelistConfig defines configuration for whitelisted users that bypass billing
// and contract verification. Whitelist users are intended for internal services
// (e.g., health checks, monitoring) that require free access without account setup.
//
// Security: Whitelist users still require valid session token authentication.
// The bypass only applies to billing, balance checks, and database logging.
//
// Expected usage: Small whitelist (< 10 addresses) for internal services only.
type WhitelistConfig struct {
	Enabled       bool     `yaml:"enabled"`       // Enable whitelist feature
	UserAddresses []string `yaml:"userAddresses"` // List of whitelisted user addresses (case-insensitive)
}

// UserUsageStatsConfig gates the per-wallet daily usage feature: the
// user_daily_stat table written inside settlement and the read-only
// GET /v1/admin/usage/daily endpoint the Router pulls from. Defaults to
// disabled so the settlement hot-path is unchanged and the route is not
// registered (404) until an operator opts in. See
// docs/wallet-direct-usage-design.md (router repo) for the full contract.
type UserUsageStatsConfig struct {
	// Enabled turns on both the settlement-time per-wallet upsert and the
	// read endpoint. When false the broker behaves exactly as before.
	Enabled bool `yaml:"enabled"`
	// RetentionDays bounds the unbounded-growth of user_daily_stat: rows with
	// date older than now-RetentionDays are pruned by a background worker.
	// The Router keeps its own permanent copy, so the broker only needs to
	// retain enough history to cover the Router's pull window. 0 disables
	// pruning (keep forever). Defaults to 90 when Enabled and left unset.
	RetentionDays int `yaml:"retentionDays"`
	// PruneInterval is how often the pruner runs. Defaults to 24h.
	PruneInterval time.Duration `yaml:"pruneInterval"`
}

// ReconciliationConfig bounds the growth of the hourly_usage_stat rollup that backs
// broker↔provider reconciliation. Unlike user_daily_stat, the rollup is always written
// (the settlement path and whitelist path populate it unconditionally), so the pruner
// always runs; there is no enable flag. See docs/design/provider-reconciliation.md.
type ReconciliationConfig struct {
	// RetentionDays is how long to keep hourly rows. Reconciliation only needs to reach
	// back to the most recent vendor statement period, so a modest window suffices.
	// Unset (0) defaults to 90; set a negative value to disable pruning (keep forever).
	RetentionDays int `yaml:"retentionDays"`
	// PruneInterval is how often the pruner runs. Defaults to 24h.
	PruneInterval time.Duration `yaml:"pruneInterval"`
}

// LoRAConfig configures LoRA adapter serving for fine-tuned models.
// When enabled, the inference broker can serve fine-tuned LoRA adapters
// via ServerlessLLM, with per-user access control and automatic adapter
// lifecycle management driven by on-chain events.
type LoRAConfig struct {
	Enable         bool   `yaml:"enable"`
	BaseModel      string `yaml:"baseModel"`      // Base model name (e.g., "Qwen2.5-7B")
	LoraModulesDir string `yaml:"loraModulesDir"` // Local directory for LoRA adapter files
	SllmUrl        string `yaml:"sllmUrl"`        // ServerlessLLM HTTP endpoint (default: http://sllm:8343)
	// OffloadAfter is the idle time before offloading an adapter from ServerlessLLM.
	OffloadAfter time.Duration `yaml:"offloadAfter"`
	// OffloadAfterMinutes is the legacy integer-minutes form.
	// Deprecated: use OffloadAfter. Removed after config.DeprecationRemovalDate.
	OffloadAfterMinutes int `yaml:"offloadAfterMinutes,omitempty"`

	EnableColdStorage      bool   `yaml:"enableColdStorage"`         // Enable offload to 0G Storage
	FineTuningContractAddr string `yaml:"fineTuningContractAddress"` //nolint:revive
	ChainRpcUrl            string `yaml:"chainRpcUrl"`
	// PollBlockInterval is how often the watcher polls for new on-chain events.
	PollBlockInterval time.Duration `yaml:"pollBlockInterval"`
	// PollBlockIntervalSeconds is the legacy integer-seconds form.
	// Deprecated: use PollBlockInterval. Removed after config.DeprecationRemovalDate.
	PollBlockIntervalSeconds int `yaml:"pollBlockIntervalSeconds,omitempty"`

	StorageIndexerUrl      string `yaml:"storageIndexerUrl"`      // 0G Storage indexer URL for downloading adapters
	StorageTurbo           bool   `yaml:"storageTurbo"`           // Use turbo indexer for 0G Storage
	AutoDeploy             bool   `yaml:"autoDeploy"`             // If true, auto-deploy adapters to vLLM on acknowledge; if false, download only (user must call deploy API)
	FineTuningProviderAddr string `yaml:"fineTuningProviderAddr"` // Override FT provider address for event filtering (default: inference provider address)
	EciesPrivateKey        string `yaml:"-"`                      // Override ECIES private key for adapter decryption (2-CVM setup). Set via env var LORA_ECIES_PRIVATE_KEY.
}

type Config struct {
	AllowOrigins    []string `yaml:"allowOrigins"`
	ContractAddress string   `yaml:"contractAddress"`
	Database        struct {
		// DSN is the MySQL connection string used by the broker process.
		DSN string `yaml:"dsn"`
		// Provider was the misleading legacy name for DSN ("Provider" is the
		// project's GPU-side actor, not a database vendor).
		// Deprecated: use DSN. Removed after config.DeprecationRemovalDate.
		Provider string `yaml:"provider,omitempty"`
	} `yaml:"database"`
	Event struct {
		// ListenAddr is the metrics HTTP server bind address used by the
		// event process (e.g. ":8088").
		ListenAddr string `yaml:"listenAddr"`
		// ProviderAddr was the misleading legacy name (it is not a Provider
		// address — it is a local listen address).
		// Deprecated: use ListenAddr. Removed after config.DeprecationRemovalDate.
		ProviderAddr string `yaml:"providerAddr,omitempty"`
	} `yaml:"event"`
	GasPrice    string `yaml:"gasPrice"`
	MaxGasPrice string `yaml:"maxGasPrice"`
	Interval    struct {
		// All four fields used to be integer seconds; they are now
		// time.Duration (parsed from yaml strings like "60s" / "10m").
		// loadConfig restores the legacy integer-seconds semantics when
		// it detects the raw yaml value is a number — see
		// migrateDeprecated.
		AutoSettleBufferTime     time.Duration `yaml:"autoSettleBufferTime"`
		ForceSettlementProcessor time.Duration `yaml:"forceSettlementProcessor"`
		SettlementProcessor      time.Duration `yaml:"settlementProcessor"`
		ReconciliationProcessor  time.Duration `yaml:"reconciliationProcessor"`
	} `yaml:"interval"`
	Settlement struct {
		// MinSettlementFee is the minimum accumulated fee (in neuron) per user
		// before including them in a settlement batch. Users below this threshold
		// are deferred to accumulate more fees. Default "4000000000000000"
		// (0.004 A0GI) covers gas cost (~0.0006 A0GI) with ~7× margin.
		// Set to "0" to disable per-user filtering.
		MinSettlementFee string `yaml:"minSettlementFee"`
	} `yaml:"settlement"`
	RevenueTransfer struct {
		TargetAddress string        `yaml:"targetAddress"`
		ReserveAmount string        `yaml:"reserveAmount"`
		Interval      time.Duration `yaml:"interval"`
	} `yaml:"revenueTransfer"`
	Service Service    `yaml:"service"`
	LoRA    LoRAConfig `yaml:"lora"`
	// Network is the canonical single-network config (introduced by #507).
	Network config.NetworkConfig `mapstructure:"network" yaml:"network"`
	// Networks is the legacy multi-network map kept for backwards
	// compatibility. Deprecated: use Network instead. Removed after
	// config.DeprecationRemovalDate.
	Networks config.Networks `mapstructure:"networks" yaml:"networks,omitempty"` //nolint:staticcheck // intentional reference to deprecated Networks for the #507 fallback window
	Monitor  struct {
		Enable       bool   `yaml:"enable"`
		EventAddress string `yaml:"eventAddress"`
	} `yaml:"monitor"`
	ZK struct {
		// URL is the ZK service endpoint.
		URL string `yaml:"url"`
		// Provider was the misleading legacy name for URL.
		// Deprecated: use URL. Removed after config.DeprecationRemovalDate.
		Provider      string `yaml:"provider,omitempty"`
		RequestLength int    `yaml:"requestLength"`
	} `yaml:"zk"`
	ChatCacheExpiration time.Duration           `yaml:"chatCacheExpiration"`
	NvGPU               bool                    `yaml:"nvGPU"`
	Logger              *config.LoggerConfig    `yaml:"logger"`
	LogPaths            LogPathsConfig          `yaml:"logPaths"`
	Controller          ControllerConfig        `yaml:"controller"`
	CacheTokenBilling   CacheTokenBillingConfig `yaml:"cacheTokenBilling"`
	TieredPricing       TieredPricingConfig     `yaml:"tieredPricing"`
	PriceFeed           PriceFeedConfig         `yaml:"priceFeed"`
	Whitelist           WhitelistConfig         `yaml:"whitelist"`
	Async               AsyncConfig             `yaml:"async"`
	VideoPoll           VideoPollConfig         `yaml:"videoPoll"`
	ProviderHttp        ProviderHttpConfig      `yaml:"providerHttp"`
	ConcurrencyLimit    ConcurrencyLimitConfig  `yaml:"concurrencyLimit"`
	UserUsageStats      UserUsageStatsConfig    `yaml:"userUsageStats"`
	Reconciliation      ReconciliationConfig    `yaml:"reconciliation"`
	// AllowTokenBilledSpeechToText opens the billing path for token-billed
	// speech-to-text models (gpt-4o-transcribe, gpt-4o-mini-transcribe).
	// Defaults to false.
	//
	// Enforced at startup: loadConfig refuses to boot the broker if
	// service.type=="speech-to-text" is registered against a known token-
	// billed model and this flag is false. Operators must consciously
	// acknowledge the analytics trade-off (see #530) before deploying such
	// a service — the failure mode is fail-stop at boot, not a per-request
	// log line that gets lost in production.
	//
	// Also gates billSpeechToTextByTokens at runtime as defense-in-depth:
	// if an unknown model proxies an unexpected token-shape response, the
	// broker bills against real upstream usage anyway (refusing would mean
	// free GPU since the response has already shipped) but logs a loud
	// warning naming #530 so the operator can investigate.
	//
	// TODO(#530): a broker-wide flag is awkward — the unit conflation is
	// per-service. Once the schema discriminator lands, derive this from
	// the registered service's billing_unit instead of asking operators to
	// flip a global flag.
	AllowTokenBilledSpeechToText bool `yaml:"allowTokenBilledSpeechToText"`
}

// ConcurrencyLimitConfig defines concurrency limiting for backend protection.
// Global limit caps total in-flight requests to the backend (should match GPU capacity).
// Per-user limit prevents a single user from monopolizing all slots.
type ConcurrencyLimitConfig struct {
	MaxGlobalConcurrent  int `yaml:"maxGlobalConcurrent"`  // Max total concurrent requests to backend (default: 20)
	MaxPerUserConcurrent int `yaml:"maxPerUserConcurrent"` // Max concurrent requests per user (default: 5, whitelisted users are exempt)
	PerUserRPM           int `yaml:"perUserRPM"`           // Max requests per minute per user (default: 30, 0 = disabled)
	PerUserBurst         int `yaml:"perUserBurst"`         // Max burst size for per-user rate limit (default: 5)
	PerUserTPM           int `yaml:"perUserTPM"`           // Max tokens per minute per user (default: 0 = disabled, chatbot/speech-to-text)
	PerUserTPMBurst      int `yaml:"perUserTPMBurst"`      // Max burst size for per-user TPM limit (default: 0)
	PerUserIPM           int `yaml:"perUserIPM"`           // Max images per minute per user (default: 0 = disabled, text-to-image/image-editing)
	PerUserIPMBurst      int `yaml:"perUserIPMBurst"`      // Max burst size for per-user IPM limit (default: 0)

	// PerUserOverrides grants specific addresses different per-user limits than
	// the shared defaults above, without changing the limit for everyone else.
	// Intended for heavy partners who legitimately need more headroom. Overrides
	// only take effect for dimensions that are globally enabled (e.g. an RPM
	// override is ignored when PerUserRPM == 0). Overrides never raise the global
	// concurrency cap (MaxGlobalConcurrent), so a single user still cannot exceed
	// total backend capacity.
	PerUserOverrides []PerUserLimitOverride `yaml:"perUserOverrides"`
}

// PerUserLimitOverride sets the per-user limits for a single address. A zero
// field inherits the corresponding global default, so an operator can override
// only the dimension they care about (e.g. just MaxConcurrent). A positive
// value below the default lowers the cap for that user; because 0 means
// inherit, a limit cannot be set to exactly 0 (to throttle hard, use a small
// positive value). Negative values are invalid and treated as inherit (with a
// startup warning). Addresses are matched case-insensitively; a malformed
// address is rejected at startup with a warning.
type PerUserLimitOverride struct {
	UserAddress   string `yaml:"userAddress"`   // Address this override applies to (case-insensitive)
	MaxConcurrent int    `yaml:"maxConcurrent"` // Per-user concurrency cap (0 = inherit MaxPerUserConcurrent)
	RPM           int    `yaml:"rpm"`           // Requests per minute (0 = inherit PerUserRPM)
	Burst         int    `yaml:"burst"`         // RPM burst size (0 = inherit PerUserBurst)
	TPM           int    `yaml:"tpm"`           // Tokens per minute (0 = inherit PerUserTPM)
	TPMBurst      int    `yaml:"tpmBurst"`      // TPM burst size (0 = inherit PerUserTPMBurst)
	IPM           int    `yaml:"ipm"`           // Images per minute (0 = inherit PerUserIPM)
	IPMBurst      int    `yaml:"ipmBurst"`      // IPM burst size (0 = inherit PerUserIPMBurst)
}

// AsyncConfig defines configuration for async job processing.
type AsyncConfig struct {
	Enabled           bool `yaml:"enabled"`           // Enable async endpoints (default: true)
	MaxConcurrentJobs int  `yaml:"maxConcurrentJobs"` // Max concurrent worker goroutines (default: 10)
	MaxQueueSize      int  `yaml:"maxQueueSize"`      // Max pending jobs waiting for a worker (default: 100)

	// ResultTTL: how long to keep completed results.
	ResultTTL time.Duration `yaml:"resultTTL"`
	// Deprecated: use ResultTTL. Removed after config.DeprecationRemovalDate.
	ResultTTLMinutes int `yaml:"resultTTLMinutes,omitempty"`

	// CleanupInterval: interval for expired job cleanup.
	CleanupInterval time.Duration `yaml:"cleanupInterval"`
	// Deprecated: use CleanupInterval. Removed after config.DeprecationRemovalDate.
	CleanupIntervalSeconds int `yaml:"cleanupIntervalSeconds,omitempty"`

	// JobTimeout: per-job HTTP request timeout.
	JobTimeout time.Duration `yaml:"jobTimeout"`
	// Deprecated: use JobTimeout. Removed after config.DeprecationRemovalDate.
	JobTimeoutMinutes int `yaml:"jobTimeoutMinutes,omitempty"`
}

// ZeroOutputRequestPruneThreshold is how old a zero-output Request row must be before
// ctrl.SettleFeesWithTEE's periodic prune pass deletes it (db.PruneRequest). Exported here,
// rather than left as a local literal in settlement_tee.go, for discoverability.
//
// VideoPollConfig.MaxPollDuration does NOT need to stay under this value: a still in-flight
// (pending/polling) VideoPollJob's Request row is excluded from this prune sweep
// unconditionally, regardless of age — see db.PruneRequest's doc comment. An earlier boot-time
// invariant tied MaxPollDuration to this constant before that exclusion existed; it was
// removed once the exclusion made it redundant.
const ZeroOutputRequestPruneThreshold = 1 * time.Hour

// VideoJobOwnerRetention is how old a model.VideoJobOwner row (the provider job id -> creator
// address mapping gating GET /videos/{id} and .../content, see issue #591) must be before
// ctrl.SettleFeesWithTEE's periodic prune pass deletes it (db.DeleteExpiredVideoJobOwners).
// Exported as a constant for now rather than a config field — a config knob is a planned
// follow-up once real usage data suggests operators need to tune it.
//
// 7 days is a deliberate multiple of the client-facing retrieval window mainstream
// video-generation vendors document today: the target vendor for this integration (Alibaba
// HappyHorse/DashScope-style) expires both the task id and the result URL at 24 hours, and
// every other mainstream vendor surveyed (OpenAI Sora, Google Veo, Kling, MiniMax/Hailuo,
// Runway) clusters in the same 24-48 hour range for how long a client can still query/download
// a result — none of that is a config knob here, it's the provider's own contract, so setting
// this shorter than that window would mean the broker's own bookkeeping locks out a legitimate
// creator before the provider itself would have. The row is small (two varchar columns + an
// index), so the cost of keeping generous margin is negligible.
const VideoJobOwnerRetention = 7 * 24 * time.Hour

// VideoPollConfig defines the background scheduler that polls a video-generation job to
// completion when its create response is non-terminal (queued/in_progress), billing the
// actual delivered duration instead of the requested one. See
// docs/design/video-generation-async-billing.md.
type VideoPollConfig struct {
	Enabled bool `yaml:"enabled"` // Enable the poll-to-completion scheduler (default: true)

	// MaxConcurrentPolls: worker pool size for the poll scheduler (default: 10). Feeds a GORM
	// Limit(n) in db.ClaimDueVideoPollJobs; boot-validated positive below (n<=0 either stalls
	// the scheduler or removes the LIMIT clause entirely).
	MaxConcurrentPolls int `yaml:"maxConcurrentPolls"`

	// PollInterval: fixed delay between poll attempts for a given job. A fixed interval is
	// sufficient given providers already recommend one (e.g. DashScope's 5-15s);
	// exponential backoff is unneeded complexity for a bounded-attempts poll. Boot-validated
	// positive below.
	PollInterval time.Duration `yaml:"pollInterval"`

	// MaxPollDuration: ceiling from job creation to forced timed_out. Not bounded against
	// ZeroOutputRequestPruneThreshold — a still in-flight (pending/polling) job's Request row
	// is excluded from that prune sweep unconditionally regardless of age (db.PruneRequest), so
	// this can be set as high as a slow provider needs without risking the row being pruned out
	// from under it.
	MaxPollDuration time.Duration `yaml:"maxPollDuration"`

	// ScanInterval: how often the scheduler queries for due rows. Feeds time.NewTicker
	// directly (video_poll.go's runVideoPollScanner); boot-validated positive below — a
	// non-positive duration panics the ticker in an unrecovered background goroutine.
	ScanInterval time.Duration `yaml:"scanInterval"`

	// LeaseWindow: how far into the future a claimed row's NextPollAt is pushed while a poll
	// round-trip is in flight. A row whose lease expires without a status update (worker
	// crash) becomes claimable again — see db.ClaimDueVideoPollJobs. Must stay comfortably
	// above PollRequestTimeout plus the time to parse/bill/write the result — if the two are
	// close, an ordinary slow (not crashed) provider response can let a second worker reclaim
	// and re-poll the same job while the first is still finishing. The terminal-write
	// attempts-fenced guards (db.RescheduleVideoPollJob et al.) make this race safe against
	// double-billing regardless, but it still wastes a duplicate provider round-trip — see the
	// boot-time validation enforcing LeaseWindow > PollRequestTimeout below.
	LeaseWindow time.Duration `yaml:"leaseWindow"`

	// PollRequestTimeout: per-poll-attempt HTTP timeout (context deadline for the single GET
	// to the provider/translator). Was previously a value hardcoded in video_poll.go with only
	// a comment tying it to LeaseWindow; now config-driven and boot-validated against
	// LeaseWindow directly (see loadConfig), the same treatment MaxPollDuration already gets
	// against the settlement prune threshold — a doc-only coupling between two independently
	// configurable values is exactly the kind of footgun an operator can silently drift apart.
	PollRequestTimeout time.Duration `yaml:"pollRequestTimeout"`

	// RetentionTTL: how long to keep terminal (completed/failed/timed_out) rows before the
	// cleanup pass deletes them.
	RetentionTTL time.Duration `yaml:"retentionTTL"`

	// CleanupInterval: how often the retention sweep (DeleteExpiredVideoPollJobs) runs. Feeds
	// time.NewTicker directly (video_poll.go's runVideoPollCleanup); boot-validated positive
	// below — see ScanInterval's doc comment for why.
	CleanupInterval time.Duration `yaml:"cleanupInterval"`
}

// ProviderHttpConfig defines HTTP client timeouts for broker→provider communication.
// Providers can tune these values based on their GPU capacity and model complexity.
type ProviderHttpConfig struct {
	// TotalTimeout: overall HTTP request timeout.
	TotalTimeout time.Duration `yaml:"totalTimeout"`
	// Deprecated: use TotalTimeout. Removed after config.DeprecationRemovalDate.
	TotalTimeoutMinutes int `yaml:"totalTimeoutMinutes,omitempty"`

	// ResponseHeaderTimeout: max time to wait for provider to start responding.
	ResponseHeaderTimeout time.Duration `yaml:"responseHeaderTimeout"`
	// Deprecated: use ResponseHeaderTimeout. Removed after config.DeprecationRemovalDate.
	ResponseHeaderTimeoutMinutes int `yaml:"responseHeaderTimeoutMinutes,omitempty"`
}

type LogPathsConfig struct {
	BrokerLogDir string `yaml:"brokerLogDir"`
	EventLogDir  string `yaml:"eventLogDir"`
}

// ControllerConfig Controller service configuration
type ControllerConfig struct {
	Enable         bool                 `yaml:"enable"`         // Enable controller service
	Port           int                  `yaml:"port"`           // HTTP service port, default 3090
	AdminAddresses []string             `yaml:"adminAddresses"` // Authorized admin wallet addresses
	AllowedIPs     []string             `yaml:"allowedIPs"`     // IP whitelist, empty means allow all
	Image          string               `yaml:"image"`          // Image for broker/event containers, default ghcr.io/0gfoundation/0g-serving-broker:latest
	Docker         DockerConfig         `yaml:"docker"`         // Docker connection config
	Containers     ContainersConfig     `yaml:"containers"`     // All managed containers
	Logger         *config.LoggerConfig `yaml:"logger"`         // Logger config
	ConfigFile     string               `yaml:"-"`              // Resolved config file path (set at runtime, not from yaml)
}

// DockerConfig Docker connection configuration
type DockerConfig struct {
	Host       string `yaml:"host"`       // Docker socket path, default unix:///var/run/docker.sock
	APIVersion string `yaml:"apiVersion"` // Docker API version, default 1.41
}

// ContainersConfig all managed containers configuration
type ContainersConfig struct {
	Broker         string `yaml:"broker"`         // Broker container name, default "0g-serving-provider-broker"
	Event          string `yaml:"event"`          // Event container name, default "0g-serving-provider-event"
	Ingress        string `yaml:"ingress"`        // Ingress container name, default "broker-ingress"
	PrometheusInit string `yaml:"prometheusInit"` // Prometheus init container name, default "prometheus-init"
	Prometheus     string `yaml:"prometheus"`     // Prometheus container name, default "prometheus"
}

// IngressAllowedEnvKeys whitelist of environment variables that can be modified for ingress
var IngressAllowedEnvKeys = []string{
	"CLOUDFLARE_API_TOKEN",
	"DOMAIN",
	"TARGET_ENDPOINT",
	"CERTBOT_EMAIL",
	"GATEWAY_DOMAIN",
	"SET_CAA",
	"PORT",
}

// validateUSDPriceString rejects a USD-denominated price string that isn't a
// non-negative decimal.  Duplicates the minimal subset of
// pricefeed.ParseUSDPerMillion needed at config-load time; kept in-package
// to avoid a config → pricefeed import cycle (factory.go imports config).
func validateUSDPriceString(field, value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fmt.Errorf("invalid config: %s is empty", field)
	}
	r, ok := new(big.Rat).SetString(trimmed)
	if !ok {
		return fmt.Errorf("invalid config: %s=%q is not a valid decimal", field, value)
	}
	if r.Sign() < 0 {
		return fmt.Errorf("invalid config: %s=%q must be non-negative", field, value)
	}
	return nil
}

// normalizeUSDPerUnitPrice validates a USD-per-unit decimal string (fieldPath
// names it for error messages, e.g. "service.outputPriceUSDPerImage") and
// normalizes it into the per-1M-unit representation the shared USD pipeline
// (price feed, on-chain ceiling, wei conversion) consumes: value × 1e6. Shared
// by every per-unit (not per-token) USD price — image, single-model video,
// and per-model video — so the normalization math lives in exactly one place.
// Parses the TRIMMED value (validateUSDPriceString trims before validating,
// so a whitespace-padded string that passes validation can't slip a nil into
// the multiply).
func normalizeUSDPerUnitPrice(fieldPath, raw string) (string, error) {
	if err := validateUSDPriceString(fieldPath, raw); err != nil {
		return "", err
	}
	perUnit, ok := new(big.Rat).SetString(strings.TrimSpace(raw))
	if !ok {
		return "", fmt.Errorf("invalid config: %s %q is not a valid decimal", fieldPath, raw)
	}
	return ratToDecimalString(new(big.Rat).Mul(perUnit, big.NewRat(1_000_000, 1))), nil
}

// validatePricingTiers validates an ordered tier list: multipliers >= 1,
// maxInputTokens >= 0, the unbounded (0) tier last, and strictly ascending
// order. An empty slice is valid (no tiers). prefix labels errors (e.g.
// "tieredPricing.tiers" or "service.modelPricing[0].tiers").
func validatePricingTiers(prefix string, tiers []PricingTier) error {
	for i, tier := range tiers {
		if err := validateTierMultiplier(prefix, i, "input", tier.InputMultiplier, tier.InputMultiplierDenominator); err != nil {
			return err
		}
		if err := validateTierMultiplier(prefix, i, "output", tier.OutputMultiplier, tier.OutputMultiplierDenominator); err != nil {
			return err
		}
		if tier.MaxInputTokens < 0 {
			return fmt.Errorf("invalid config: %s[%d].maxInputTokens must be >= 0, got %d", prefix, i, tier.MaxInputTokens)
		}
		// MaxInputTokens == 0 (unbounded) must be the last tier.
		if tier.MaxInputTokens == 0 && i != len(tiers)-1 {
			return fmt.Errorf("invalid config: %s[%d].maxInputTokens=0 (unbounded) must be the last tier", prefix, i)
		}
		// Ensure ascending order.
		if i > 0 && tier.MaxInputTokens != 0 {
			prev := tiers[i-1]
			if prev.MaxInputTokens != 0 && tier.MaxInputTokens <= prev.MaxInputTokens {
				return fmt.Errorf("invalid config: %s must be ordered by maxInputTokens ascending, %s[%d]=%d <= %s[%d]=%d",
					prefix, prefix, i, tier.MaxInputTokens, prefix, i-1, prev.MaxInputTokens)
			}
		}
	}
	return nil
}

// maxTierMultiplier caps the effective tier price ratio (numerator/denominator).
// It mirrors the 0g-router's clampTierMultiplier cap so the broker never accepts
// a config the router will silently clamp — otherwise the broker would settle at
// the configured ratio while the router charges the user the capped ratio, and
// the operator eats the difference. Real tiers express small context-length
// ratios (~2–8x), so 1000x is far above any legitimate value.
const maxTierMultiplier = 1000

// maxTierMultiplierDenominator bounds the fraction denominator. Real fractions
// have tiny denominators (3/2, 5/2, 10/3); the bound keeps num (<= den*1000) and
// den small enough that the int64 cross-multiplication in maxTierMultipliers
// (n*inDen vs inNum*d) cannot overflow.
const maxTierMultiplierDenominator = 1_000_000

// validateTierMultiplier enforces the tier multiplier fraction rules for one
// side (input/output): the denominator, when set, must be >= 1 (0/unset means 1)
// and within maxTierMultiplierDenominator; and the effective fraction must be
// >= 1x (numerator >= denominator) and <= maxTierMultiplier. A tier is a
// surcharge over the base (lowest-tier) price, never a discount below it — the
// base price is what registers on-chain, so a sub-1x tier would bill below the
// advertised floor. Rejecting numerator < denominator also catches a transposed
// fraction (e.g. 2/3 when 3/2 was intended). The upper bound keeps the config in
// lockstep with the router's billing cap. Mirrors validateWriteMultiplier for
// the cache-write premium.
func validateTierMultiplier(prefix string, i int, side string, num, den int64) error {
	if num < 1 {
		return fmt.Errorf("invalid config: %s[%d].%sMultiplier must be >= 1, got %d", prefix, i, side, num)
	}
	if den < 0 {
		return fmt.Errorf("invalid config: %s[%d].%sMultiplierDenominator must be >= 0 (0 = unset = 1), got %d", prefix, i, side, den)
	}
	if den > maxTierMultiplierDenominator {
		return fmt.Errorf("invalid config: %s[%d].%sMultiplierDenominator %d exceeds max %d", prefix, i, side, den, maxTierMultiplierDenominator)
	}
	effDen := den
	if effDen == 0 {
		effDen = 1
	}
	if num < effDen {
		return fmt.Errorf("invalid config: %s[%d].%sMultiplier fraction is a surcharge and must be >= 1x (numerator >= denominator), got %d/%d", prefix, i, side, num, den)
	}
	if num > effDen*maxTierMultiplier {
		return fmt.Errorf("invalid config: %s[%d].%sMultiplier effective ratio must be <= %dx, got %d/%d", prefix, i, side, maxTierMultiplier, num, den)
	}
	return nil
}

// protectedInjectBodyFields are top-level request-body keys the broker sets or
// depends on for correctness and must never be overridden by injectBodyFields:
//   - model: enforced per service for billing integrity (users pay for the
//     advertised model; see EnforceConfiguredModel/ValidateModelAllowlist).
//   - messages: the actual request content.
//   - stream / stream_options: the broker forces stream_options.include_usage so
//     streaming responses report usage for billing (see EnsureStreamOptions).
//   - lora_adapter_name: the broker derives this from an ft-* model during LoRA
//     request rewriting (see RewriteLoRARequest); injecting it would clobber the
//     resolved adapter.
//
// Injecting any of these is rejected at config load (fail loud, not silent).
var protectedInjectBodyFields = map[string]struct{}{
	"model":             {},
	"messages":          {},
	"stream":            {},
	"stream_options":    {},
	"lora_adapter_name": {},
}

// normalizeInjectBodyFields rejects broker-critical keys, then makes the map safe
// to json.Marshal into the forwarded request body at runtime and verifies
// serializability so a broken value shape fails loud at load instead of on every
// request.
//
// yaml.v2 decodes nested mappings as map[interface{}]interface{}, which
// json.Marshal rejects. So a nested-object value — e.g. OpenRouter's documented
// provider.max_price: {prompt, completion} — would otherwise pass config load
// (the field is map[string]interface{} at the top level) yet fail EVERY chatbot
// request at runtime inside ctrl.InjectBodyFields. We recursively convert nested
// maps to map[string]interface{} and re-check json.Marshal.
//
// This intentionally does NOT validate key names against an allowlist (beyond the
// protected denylist): upstream-accepted keys are the gateway's vocabulary, not
// ours, and a closed allowlist both rejects legitimate keys the moment the
// upstream adds one and gives false confidence. We only guarantee the value is
// forwardable and does not clobber a field the broker relies on.
// fieldPath names the config location for error messages (e.g.
// "service.injectBodyFields" or "service.modelPricing[0].injectBodyFields") so a
// rejection points the operator at the exact block.
func normalizeInjectBodyFields(fieldPath string, fields map[string]interface{}) (map[string]interface{}, error) {
	normalized := make(map[string]interface{}, len(fields))
	for k, v := range fields {
		if _, protected := protectedInjectBodyFields[k]; protected {
			return nil, fmt.Errorf("invalid config: %s must not override broker-critical field %q (protected: model, messages, stream, stream_options, lora_adapter_name)", fieldPath, k)
		}
		normalized[k] = normalizeYAMLValue(v)
	}
	if _, err := json.Marshal(normalized); err != nil {
		return nil, fmt.Errorf("invalid config: %s is not JSON-serializable (check the value shapes): %w", fieldPath, err)
	}
	return normalized, nil
}

// normalizeStripBodyFields trims and de-duplicates a stripBodyFields list and
// rejects broker-critical keys, reusing the same protected denylist as
// injectBodyFields — stripping model/messages/stream/stream_options/
// lora_adapter_name would break model enforcement, usage-based billing, or LoRA
// rewriting, so it fails loud at load instead of silently mangling every request.
// Blank entries are dropped. fieldPath names the config location for error
// messages (e.g. "service.stripBodyFields" or
// "service.modelPricing[0].stripBodyFields").
func normalizeStripBodyFields(fieldPath string, fields []string) ([]string, error) {
	seen := make(map[string]struct{}, len(fields))
	normalized := make([]string, 0, len(fields))
	for _, k := range fields {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if _, protected := protectedInjectBodyFields[k]; protected {
			return nil, fmt.Errorf("invalid config: %s must not strip broker-critical field %q (protected: model, messages, stream, stream_options, lora_adapter_name)", fieldPath, k)
		}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		normalized = append(normalized, k)
	}
	return normalized, nil
}

// EffectiveStripBodyFields returns the top-level body keys to strip for a request
// resolved to the given model: the UNION of the service-level stripBodyFields and
// the resolved model's per-entry stripBodyFields. Unlike injectBodyFields (which
// deep-merges so a per-model leaf overrides the service value), stripping is
// additive — a key removed at either level should be removed — so the two lists
// are combined rather than overridden. Returns nil when neither level is
// configured. The result is order-stable (service entries first, then any
// model-only entries) and de-duplicated.
func (s *Service) EffectiveStripBodyFields(model string) []string {
	svc := s.StripBodyFields
	var entry []string
	if model != "" {
		if e := s.GetModelPricing(model); e != nil {
			entry = e.StripBodyFields
		}
	}
	if len(entry) == 0 {
		return svc
	}
	if len(svc) == 0 {
		return entry
	}
	seen := make(map[string]struct{}, len(svc)+len(entry))
	out := make([]string, 0, len(svc)+len(entry))
	for _, k := range append(append([]string{}, svc...), entry...) {
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return out
}

// EffectiveInjectBodyFields returns the body fields to inject for a request
// resolved to the given model: the service-level injectBodyFields with the
// resolved model's per-entry injectBodyFields deep-merged ON TOP (the entry wins
// on leaf conflicts). This lets a multi-model service share routing at the
// service level (e.g. provider.sort) while each model adds its own override
// (e.g. provider.max_price). Returns nil when neither level is configured.
//
// The returned map is freshly allocated at every merged level and never shares a
// mutable node with the stored config, so callers (and json.Marshal) cannot
// corrupt the config maps that are reused across concurrent requests. When only
// one level is set, its (read-only) map is returned directly.
func (s *Service) EffectiveInjectBodyFields(model string) map[string]interface{} {
	svc := s.InjectBodyFields
	var entry map[string]interface{}
	if model != "" {
		if e := s.GetModelPricing(model); e != nil {
			entry = e.InjectBodyFields
		}
	}
	switch {
	case len(entry) == 0:
		return svc
	case len(svc) == 0:
		return entry
	default:
		return deepMergeInjectFields(svc, entry)
	}
}

// EffectiveAdditionalSecret returns the outbound secret headers for a request
// resolved to the given model. Resolution is all-or-nothing, NOT a key-by-key
// merge: if the resolved model's per-entry additionalSecret is non-empty it is
// used wholesale (the service-level map does not contribute); otherwise the
// service-level service.additionalSecret applies. A "" model (single-model
// providers, or paths with no resolved model) yields the service-level map.
// Returns nil when the applicable level configures no header.
//
// Wholesale replacement is deliberate for a credentials field: a merge would
// (a) let a stale service-level header (e.g. Authorization) ride along to a
// model whose per-entry block sets a differently-named auth header, and (b)
// interact badly with HTTP header canonicalization — a service-level
// "Authorization" and a per-model "authorization" are distinct Go map keys but
// collapse to one header at Set time, making which value wins non-deterministic.
// Replacing wholesale means a per-model block is self-contained: it must list
// EVERY header that model's upstream needs.
//
// Wildcard note: GetModelPricing folds any unenumerated model onto the "*"
// entry, so on a serve-all deployment every unlisted model shares the wildcard
// entry's secret (or the service-level map if the wildcard sets none) —
// per-model keys are only possible for explicitly enumerated entries.
func (s *Service) EffectiveAdditionalSecret(model string) map[string]string {
	if model != "" {
		if e := s.GetModelPricing(model); e != nil && len(e.AdditionalSecret) > 0 {
			return e.AdditionalSecret
		}
	}
	return s.AdditionalSecret
}

// deepMergeInjectFields returns a new map equal to base with override applied on
// top: nested map[string]interface{} values are merged recursively (override's
// leaf wins), every other value type (scalars, slices) is replaced wholesale by
// override's. Neither input is mutated. Both inputs are already normalized to
// string-keyed maps by normalizeInjectBodyFields, so only map[string]interface{}
// nesting needs handling.
func deepMergeInjectFields(base, override map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(base)+len(override))
	for k, v := range base {
		out[k] = v
	}
	for k, ov := range override {
		if bv, ok := out[k]; ok {
			if bm, isBaseMap := bv.(map[string]interface{}); isBaseMap {
				if om, isOverrideMap := ov.(map[string]interface{}); isOverrideMap {
					out[k] = deepMergeInjectFields(bm, om)
					continue
				}
			}
		}
		out[k] = ov
	}
	return out
}

// normalizeYAMLValue recursively rewrites yaml.v2's map[interface{}]interface{}
// nodes into map[string]interface{} (walking slices too) so the value can be
// json.Marshal-ed. Map keys are stringified; OpenRouter routing keys are always
// strings, so a non-string key indicates malformed config and simply becomes its
// string form (harmless — the upstream ignores unknown keys).
func normalizeYAMLValue(v interface{}) interface{} {
	switch val := v.(type) {
	case map[interface{}]interface{}:
		m := make(map[string]interface{}, len(val))
		for k, vv := range val {
			m[fmt.Sprintf("%v", k)] = normalizeYAMLValue(vv)
		}
		return m
	case map[string]interface{}:
		m := make(map[string]interface{}, len(val))
		for k, vv := range val {
			m[k] = normalizeYAMLValue(vv)
		}
		return m
	case []interface{}:
		s := make([]interface{}, len(val))
		for i, vv := range val {
			s[i] = normalizeYAMLValue(vv)
		}
		return s
	default:
		return val
	}
}

// validatePriceFeedConfig validates (and normalizes with defaults) the price-feed
// configuration. Only invoked when service.priceDenomination == "USD".
func validatePriceFeedConfig(pf *PriceFeedConfig) error {
	if len(pf.Sources) == 0 {
		return fmt.Errorf("invalid config: priceFeed.sources must not be empty when priceDenomination is 'USD'")
	}
	seen := make(map[string]struct{}, len(pf.Sources))
	for i, s := range pf.Sources {
		name := strings.ToLower(strings.TrimSpace(s))
		if name == "" {
			return fmt.Errorf("invalid config: priceFeed.sources[%d] is empty", i)
		}
		if _, dup := seen[name]; dup {
			return fmt.Errorf("invalid config: priceFeed.sources[%d]=%q is duplicated", i, name)
		}
		seen[name] = struct{}{}
		pf.Sources[i] = name
	}

	if pf.UpdateInterval <= 0 {
		pf.UpdateInterval = time.Hour
	}
	if pf.StalenessThreshold <= 0 {
		// 3× updateInterval: the first failed tick uses retries to absorb
		// transient issues, the second and third ticks give the feed two
		// more chances to recover before readers start failing closed.
		// Scales with the operator's chosen interval so shorter/longer
		// cadences don't need a separate staleness tune.
		pf.StalenessThreshold = 3 * pf.UpdateInterval
	}
	if pf.StalenessThreshold < pf.UpdateInterval {
		return fmt.Errorf("invalid config: priceFeed.stalenessThreshold (%s) must be >= priceFeed.updateInterval (%s)", pf.StalenessThreshold, pf.UpdateInterval)
	}
	if pf.MinOnChainUpdateBps < 0 || pf.MinOnChainUpdateBps > 10000 {
		return fmt.Errorf("invalid config: priceFeed.minOnChainUpdateBps must be in [0, 10000], got %d", pf.MinOnChainUpdateBps)
	}
	if pf.MinOnChainUpdateBps == 0 {
		pf.MinOnChainUpdateBps = 100 // 1% default
	}
	if pf.MaxRateDeviationBps < 0 || pf.MaxRateDeviationBps > 10000 {
		return fmt.Errorf("invalid config: priceFeed.maxRateDeviationBps must be in [0, 10000], got %d", pf.MaxRateDeviationBps)
	}
	if pf.MaxRateDeviationBps == 0 {
		pf.MaxRateDeviationBps = 500 // 5% default
	}
	if pf.MinQuorum < 0 {
		return fmt.Errorf("invalid config: priceFeed.minQuorum must be >= 0, got %d", pf.MinQuorum)
	}
	if pf.MinQuorum == 0 {
		// Default: require >= 2 when multiple sources configured, else 1.
		if len(pf.Sources) >= 2 {
			pf.MinQuorum = 2
		} else {
			pf.MinQuorum = 1
		}
	}
	if pf.MinQuorum > len(pf.Sources) {
		return fmt.Errorf("invalid config: priceFeed.minQuorum (%d) cannot exceed len(priceFeed.sources) (%d)", pf.MinQuorum, len(pf.Sources))
	}
	if pf.HTTPTimeout <= 0 {
		pf.HTTPTimeout = 10 * time.Second
	}
	if pf.UserAgent == "" {
		pf.UserAgent = "0g-serving-broker/pricefeed"
	}
	if pf.CoinGeckoAPIKey != "" {
		keyType := strings.ToLower(strings.TrimSpace(pf.CoinGeckoKeyType))
		switch keyType {
		case "":
			keyType = "demo"
		case "demo", "pro":
			// ok
		default:
			return fmt.Errorf("invalid config: priceFeed.coinGeckoKeyType must be 'demo' or 'pro', got %q", pf.CoinGeckoKeyType)
		}
		pf.CoinGeckoKeyType = keyType
	}
	return nil
}

var (
	instance *Config
	once     sync.Once
)

// migrateDeprecated copies values from deprecated yaml keys to their
// replacements when the user has populated the deprecated form. Each call to
// config.WarnDeprecated emits a one-shot stderr line so operators see exactly
// which keys still need to move before the removal deadline.
//
// Precedence rule: if the user wrote both the old and the new key, the new
// key wins and a separate "both set" warning is emitted.
func migrateDeprecated(cfg *Config, raw map[string]interface{}) error {
	// Interval / RevenueTransfer.Interval kept their yaml keys but flipped
	// from int (implicit seconds) to time.Duration. When the raw yaml value
	// is a number, migrate it to seconds; otherwise the new-style string
	// value already parsed by UnmarshalStrict is correct.
	config.MigrateIntegerSecondsDuration(raw, &cfg.Interval.AutoSettleBufferTime, time.Second, "interval", "autoSettleBufferTime")
	config.MigrateIntegerSecondsDuration(raw, &cfg.Interval.ForceSettlementProcessor, time.Second, "interval", "forceSettlementProcessor")
	config.MigrateIntegerSecondsDuration(raw, &cfg.Interval.SettlementProcessor, time.Second, "interval", "settlementProcessor")
	config.MigrateIntegerSecondsDuration(raw, &cfg.Interval.ReconciliationProcessor, time.Second, "interval", "reconciliationProcessor")
	config.MigrateIntegerSecondsDuration(raw, &cfg.RevenueTransfer.Interval, time.Second, "revenueTransfer", "interval")

	// Suffixed fields (Minutes/Seconds): old yaml key was kept as a separate
	// deprecated struct field; copy it over if the user still has the old
	// form. New key wins if both are set.
	config.MigrateDurationFromInt(raw,
		[]string{"lora", "offloadAfterMinutes"}, []string{"lora", "offloadAfter"},
		&cfg.LoRA.OffloadAfter, int64(cfg.LoRA.OffloadAfterMinutes), time.Minute)
	config.MigrateDurationFromInt(raw,
		[]string{"lora", "pollBlockIntervalSeconds"}, []string{"lora", "pollBlockInterval"},
		&cfg.LoRA.PollBlockInterval, int64(cfg.LoRA.PollBlockIntervalSeconds), time.Second)
	config.MigrateDurationFromInt(raw,
		[]string{"async", "resultTTLMinutes"}, []string{"async", "resultTTL"},
		&cfg.Async.ResultTTL, int64(cfg.Async.ResultTTLMinutes), time.Minute)
	config.MigrateDurationFromInt(raw,
		[]string{"async", "cleanupIntervalSeconds"}, []string{"async", "cleanupInterval"},
		&cfg.Async.CleanupInterval, int64(cfg.Async.CleanupIntervalSeconds), time.Second)
	config.MigrateDurationFromInt(raw,
		[]string{"async", "jobTimeoutMinutes"}, []string{"async", "jobTimeout"},
		&cfg.Async.JobTimeout, int64(cfg.Async.JobTimeoutMinutes), time.Minute)
	config.MigrateDurationFromInt(raw,
		[]string{"providerHttp", "totalTimeoutMinutes"}, []string{"providerHttp", "totalTimeout"},
		&cfg.ProviderHttp.TotalTimeout, int64(cfg.ProviderHttp.TotalTimeoutMinutes), time.Minute)
	config.MigrateDurationFromInt(raw,
		[]string{"providerHttp", "responseHeaderTimeoutMinutes"}, []string{"providerHttp", "responseHeaderTimeout"},
		&cfg.ProviderHttp.ResponseHeaderTimeout, int64(cfg.ProviderHttp.ResponseHeaderTimeoutMinutes), time.Minute)

	// Rename: database.provider → database.dsn
	config.MigrateStringRename(raw, []string{"database", "provider"}, []string{"database", "dsn"},
		&cfg.Database.DSN, cfg.Database.Provider)

	// Rename: event.providerAddr → event.listenAddr
	config.MigrateStringRename(raw, []string{"event", "providerAddr"}, []string{"event", "listenAddr"},
		&cfg.Event.ListenAddr, cfg.Event.ProviderAddr)

	// Rename: zk.provider → zk.url
	config.MigrateStringRename(raw, []string{"zk", "provider"}, []string{"zk", "url"},
		&cfg.ZK.URL, cfg.ZK.Provider)

	// Networks (map) → Network (single).
	if config.RawHasKey(raw, "networks") {
		if config.RawHasKey(raw, "network") {
			// Both keys explicitly set in yaml is genuinely ambiguous:
			// the legacy block usually carries url/chainID while the new
			// block often only carries privateKeys mid-migration.
			// Refuse to guess. (Scalar renames above stay lenient — a
			// single deprecated string field can't lose complementary
			// data the way a structured Networks block can.)
			return fmt.Errorf("invalid config: both deprecated 'networks' and new 'network' are set in yaml; delete the 'networks' block to complete the migration")
		}
		config.WarnDeprecated("networks", "network")
		picked, err := config.PickLegacyNetwork(cfg.Networks) //nolint:staticcheck // intentional reference to deprecated Networks for the #507 fallback window
		if err != nil {
			return err
		}
		cfg.Network = *picked
	}

	// Validate that, after any migration, we have a usable Network. Catches
	// the silent-empty cases: `networks: { foo: }` (nil entry) and
	// `networks: { foo: {} }` (empty struct). Skips when neither yaml key
	// was present — defaults / programmatic setup are out of scope.
	if (config.RawHasKey(raw, "network") || config.RawHasKey(raw, "networks")) && cfg.Network.URL == "" {
		return fmt.Errorf("invalid config: network.url is empty after loading; check that the 'network' (or legacy 'networks') block carries a url value")
	}

	return nil
}

func loadConfig(cfg *Config) error {
	configPath := "/etc/config/config.yaml"
	if envPath := os.Getenv("CONFIG_FILE"); envPath != "" {
		configPath = envPath
	}

	// Always set ConfigFile so Controller knows the path
	cfg.Controller.ConfigFile = configPath

	data, missing, err := config.ReadConfigFile(configPath)
	if err != nil {
		return err
	}
	if missing {
		return nil
	}

	// Two-phase parse: the raw map lets migration logic detect which keys
	// the user actually wrote (vs. which came from struct defaults). See
	// migrateDeprecated.
	raw := config.RawYAMLKeys(data)

	if err := yaml.UnmarshalStrict(data, cfg); err != nil {
		return err
	}

	if err := migrateDeprecated(cfg, raw); err != nil {
		return err
	}

	if cfg.Service.ModelInfo != nil {
		if err := cfg.Service.ModelInfo.Validate(cfg.Service.Type); err != nil {
			return fmt.Errorf("invalid config: %w", err)
		}
	}

	if cfg.Service.CanonicalID != "" && !validCanonicalID.MatchString(cfg.Service.CanonicalID) {
		return fmt.Errorf("invalid config: service.canonicalId %q must be bare lowercase (letters, digits, '-', '.'); namespaced names like 'org/model' belong in service.model instead", cfg.Service.CanonicalID)
	}

	// VideoPollConfig defaults, applied regardless of Enabled and regardless of whether the
	// caller pre-populated GetConfig()'s defaults (loadConfig is also exercised directly against
	// a bare zero-value Config by unit tests unrelated to video polling) — same
	// unset-field-gets-a-default pattern as the UserUsageStats/Reconciliation blocks above, so
	// the cross-field invariants below always see real values, never zero-value fields that
	// merely mean "caller didn't set this."
	if cfg.VideoPoll.MaxPollDuration == 0 {
		cfg.VideoPoll.MaxPollDuration = 20 * time.Minute
	}
	if cfg.VideoPoll.LeaseWindow == 0 {
		cfg.VideoPoll.LeaseWindow = 90 * time.Second
	}
	if cfg.VideoPoll.PollRequestTimeout == 0 {
		cfg.VideoPoll.PollRequestTimeout = 30 * time.Second
	}
	if cfg.VideoPoll.MaxConcurrentPolls == 0 {
		cfg.VideoPoll.MaxConcurrentPolls = 10
	}
	if cfg.VideoPoll.PollInterval == 0 {
		cfg.VideoPoll.PollInterval = 10 * time.Second
	}
	if cfg.VideoPoll.ScanInterval == 0 {
		cfg.VideoPoll.ScanInterval = 5 * time.Second
	}
	if cfg.VideoPoll.CleanupInterval == 0 {
		cfg.VideoPoll.CleanupInterval = 5 * time.Minute
	}

	// MaxPollDuration just needs to be positive (a sanity check, not a cross-field invariant —
	// see its doc comment for why it no longer needs to stay under
	// ZeroOutputRequestPruneThreshold). LeaseWindow must exceed PollRequestTimeout (see
	// LeaseWindow's doc comment) — this one IS a real cross-field invariant. Both refuse to
	// boot instead of a startup-time footgun an operator is likely to overlook, matching this
	// function's existing token-billed-STT gate above.
	//
	// Enforced unconditionally, NOT gated on cfg.VideoPoll.Enabled: deferVideoBillingToPoll
	// (video.go) uses MaxPollDuration/LeaseWindow to compute a VideoPollJob's ExpiresAt
	// regardless of whether the scheduler is currently running, so a disabled scheduler with an
	// out-of-range value would let an unvalidated ExpiresAt slip through and only bite later,
	// once the scheduler is re-enabled.
	if cfg.VideoPoll.MaxPollDuration <= 0 {
		return fmt.Errorf("invalid config: videoPoll.maxPollDuration (%v) must be positive", cfg.VideoPoll.MaxPollDuration)
	}
	if cfg.VideoPoll.LeaseWindow <= cfg.VideoPoll.PollRequestTimeout {
		return fmt.Errorf(
			"invalid config: videoPoll.leaseWindow (%v) must be greater than videoPoll.pollRequestTimeout (%v) — "+
				"otherwise an ordinary slow (not crashed) poll response can let a second worker reclaim and re-poll "+
				"the same job while the first is still finishing, wasting a duplicate provider round-trip",
			cfg.VideoPoll.LeaseWindow, cfg.VideoPoll.PollRequestTimeout,
		)
	}
	// ScanInterval/CleanupInterval feed time.NewTicker directly (video_poll.go's
	// runVideoPollScanner/runVideoPollCleanup), which panics on a non-positive duration — in an
	// unrecovered background goroutine, that crashes the whole broker process, not just the
	// video-poll feature. MaxConcurrentPolls feeds a GORM Limit(n): n==0 stalls the scheduler
	// (claims nothing, forever), and n<0 removes the LIMIT clause entirely, defeating the
	// documented bounded-concurrency guarantee. PollInterval reaching zero would busy-loop
	// rescheduling. All four must be positive for the same "refuse to boot" reason as above.
	if cfg.VideoPoll.MaxConcurrentPolls <= 0 {
		return fmt.Errorf("invalid config: videoPoll.maxConcurrentPolls (%d) must be positive", cfg.VideoPoll.MaxConcurrentPolls)
	}
	if cfg.VideoPoll.PollInterval <= 0 {
		return fmt.Errorf("invalid config: videoPoll.pollInterval (%v) must be positive", cfg.VideoPoll.PollInterval)
	}
	if cfg.VideoPoll.ScanInterval <= 0 {
		return fmt.Errorf("invalid config: videoPoll.scanInterval (%v) must be positive", cfg.VideoPoll.ScanInterval)
	}
	if cfg.VideoPoll.CleanupInterval <= 0 {
		return fmt.Errorf("invalid config: videoPoll.cleanupInterval (%v) must be positive", cfg.VideoPoll.CleanupInterval)
	}

	// Token-billed STT startup gate. Until #530 lands a per-row billing-unit
	// discriminator, deploying a known token-billed STT model without
	// explicit operator opt-in would silently mix seconds (whisper) and
	// tokens (gpt-4o-transcribe) under the same analytics aggregates. Refuse
	// to boot rather than emit a per-request warning that operators are
	// likely to overlook in production logs.
	if cfg.Service.Type == constant.ServiceTypeSpeechToText && !cfg.AllowTokenBilledSpeechToText {
		if model := tokenBilledSTTCanonicalName(&cfg.Service); model != "" {
			return fmt.Errorf(
				"invalid config: service.type=%q is registered against token-billed model %q but cfg.allowTokenBilledSpeechToText is false. "+
					"Token-billed STT (gpt-4o-transcribe family) is gated until #530 lands a per-row billing-unit discriminator — "+
					"set allowTokenBilledSpeechToText: true to acknowledge the analytics trade-off and proceed",
				cfg.Service.Type, model,
			)
		}
	}

	// Normalize and validate provider type
	if cfg.Service.ProviderType == "" {
		cfg.Service.ProviderType = constant.ProviderTypeDecentralized
	}
	if cfg.Service.ProviderType != constant.ProviderTypeDecentralized &&
		cfg.Service.ProviderType != constant.ProviderTypeCentralized &&
		cfg.Service.ProviderType != constant.ProviderTypeStandard {
		return fmt.Errorf("invalid config: service.providerType must be '%s', '%s', or '%s', got '%s'", constant.ProviderTypeDecentralized, constant.ProviderTypeCentralized, constant.ProviderTypeStandard, cfg.Service.ProviderType)
	}
	if cfg.Service.ProviderType == constant.ProviderTypeCentralized {
		if cfg.Service.ProviderIdentity == "" {
			return fmt.Errorf("invalid config: service.providerIdentity is required when providerType is 'centralized'")
		}
		if err := normalizeProviderIdentity(&cfg.Service.ProviderIdentity); err != nil {
			return err
		}
		// Centralized providers always behave as TargetSeparated (shared external backend)
		cfg.Service.TargetSeparated = true
		// Require HTTPS for centralized providers — routing proof relies on
		// resp.TLS which is only populated for HTTPS connections.
		// Waived under targetTLSProxy: the target is then an in-enclave shim that made
		// the vendor TLS connection itself and reports its fingerprint back on a
		// header, so the proof has TLS evidence even though this hop doesn't.
		if !cfg.Service.TargetTLSProxy && cfg.Service.TargetURL != "" && !strings.HasPrefix(strings.ToLower(cfg.Service.TargetURL), "https://") {
			return fmt.Errorf("invalid config: service.targetUrl must use HTTPS for centralized providers (routing proof requires TLS), got '%s' — set service.targetTLSProxy: true if this target is a protocol-translation sidecar inside this broker's own TEE", cfg.Service.TargetURL)
		}
	}
	if cfg.Service.ProviderType == constant.ProviderTypeStandard {
		// Unlike centralized, providerIdentity is OPTIONAL here, not required —
		// and setting it does NOT publish it. A standard provider still hides its
		// upstream from every EXTERNAL surface (GET /v1/models, on-chain
		// additionalInfo, the TEE-signed routing proof) — those are gated on
		// IsCentralized(), not on providerIdentity being empty, so they stay hidden
		// regardless. What setting it DOES do is flow into internal-only
		// bookkeeping that has no external gate: reconciliation's per-upstream
		// usage rollup (Ctrl.recordWhitelistedUsage) and the per-request Upstream
		// tag used for cost reconciliation (proxy.go), both of which fall back to
		// "self" when this is empty — indistinguishable from every OTHER standard
		// deployment. An operator running several standard providers behind
		// different real upstreams can set this to tell their own reconciliation
		// data apart, without it ever reaching a client or the chain.
		if cfg.Service.ProviderIdentity != "" {
			if err := normalizeProviderIdentity(&cfg.Service.ProviderIdentity); err != nil {
				return err
			}
		}
		// A standard provider forwards to an external upstream and never signs, so it
		// always behaves as TargetSeparated (no broker signature, no ZG-Res-Key).
		cfg.Service.TargetSeparated = true
		// TargetSeparated is forced on, which would otherwise publish a
		// TargetTeeAddress on-chain (see buildAdditionalInfo). A standard provider has
		// no upstream TEE, so force it empty rather than leak a stale/misleading TEE
		// address for a non-verifiable service.
		cfg.Service.TargetTeeAddress = ""
		// The upstream must be configured explicitly — a standard provider has no
		// co-located model and no known default base URL.
		if cfg.Service.TargetURL == "" {
			return fmt.Errorf("invalid config: service.targetUrl is required when providerType is 'standard'")
		}
		// Standard is non-verifiable by construction. Force the "standard" marker so
		// the on-chain verifiability can never claim a TEE mode (which would make
		// clients attempt a verification the broker never backs). Reject any
		// operator-supplied value other than the standard marker rather than
		// silently overwriting it.
		if cfg.Service.Verifiability != "" && cfg.Service.Verifiability != constant.VerifiabilityStandard {
			return fmt.Errorf("invalid config: service.verifiability must be empty or '%s' when providerType is 'standard', got '%s'", constant.VerifiabilityStandard, cfg.Service.Verifiability)
		}
		cfg.Service.Verifiability = constant.VerifiabilityStandard

		// Upstream-hiding is complete for chatbot / speech-to-text / image responses
		// (leak headers + leak-key body fields are stripped, and image assets are
		// broker-served). Video is the exception: the broker does not proxy video
		// bytes, so if the upstream returns the finished asset as a DIRECT URL in the
		// response body (e.g. {"output":{"video_url":"https://<upstream-host>/..."}}),
		// that host reaches the client — leak-key stripping does not rewrite URL
		// values. Upstreams that expose the asset via the OpenAI GET /videos/{id}/content
		// pattern (which the broker proxies) do not leak. Warn so the operator makes a
		// conscious choice. (stdlib log: the structured logger isn't up at config load.)
		if cfg.Service.Type == constant.ServiceTypeVideoGeneration {
			log.Printf("[CONFIG] providerType 'standard' with type 'video-generation': the upstream is fully hidden only when it returns the asset via GET /videos/{id}/content (broker-proxied). An upstream that returns a direct asset URL in the response body will expose that URL's host to clients — the broker does not proxy video bytes or rewrite URL values.")
		}
	}

	// Body-field injection is only applied for the chatbot service type (see
	// ctrl.InjectBodyFields). Reject it elsewhere, reject broker-critical keys,
	// and normalize, so any misconfiguration surfaces at load instead of silently
	// doing nothing (or failing every request).
	if len(cfg.Service.InjectBodyFields) > 0 {
		if cfg.Service.Type != constant.ServiceTypeChatbot {
			return fmt.Errorf("invalid config: service.injectBodyFields is only supported for service type '%s', got '%s'", constant.ServiceTypeChatbot, cfg.Service.Type)
		}
		normalized, err := normalizeInjectBodyFields("service.injectBodyFields", cfg.Service.InjectBodyFields)
		if err != nil {
			return err
		}
		cfg.Service.InjectBodyFields = normalized
	}

	// Body-field stripping mirrors injection: only meaningful on the chatbot
	// forward path, rejects broker-critical keys, and is normalized (trim/dedup) at
	// load so a misconfiguration surfaces here rather than silently doing nothing.
	if len(cfg.Service.StripBodyFields) > 0 {
		if cfg.Service.Type != constant.ServiceTypeChatbot {
			return fmt.Errorf("invalid config: service.stripBodyFields is only supported for service type '%s', got '%s'", constant.ServiceTypeChatbot, cfg.Service.Type)
		}
		normalized, err := normalizeStripBodyFields("service.stripBodyFields", cfg.Service.StripBodyFields)
		if err != nil {
			return err
		}
		cfg.Service.StripBodyFields = normalized
	}

	// Provider display metadata (applies to any provider type). Both are optional.
	cfg.Service.ProviderName = strings.TrimSpace(cfg.Service.ProviderName)
	if cfg.Service.ProviderCountry != "" {
		cfg.Service.ProviderCountry = strings.ToUpper(strings.TrimSpace(cfg.Service.ProviderCountry))
		if !validProviderCountry.MatchString(cfg.Service.ProviderCountry) {
			return fmt.Errorf("invalid config: service.providerCountry must be a two-letter ISO 3166-1 alpha-2 code (e.g., 'US', 'CN'), got '%s'", cfg.Service.ProviderCountry)
		}
	}

	// Normalize and validate price denomination / priceFeed configuration.
	if cfg.Service.PriceDenomination == "" {
		cfg.Service.PriceDenomination = constant.PriceDenominationNative
	}
	cfg.Service.PriceDenomination = strings.ToUpper(cfg.Service.PriceDenomination)
	// Image generation / editing bill per image, so under USD denomination they
	// use service.outputPriceUSDPerImage instead of the per-1M-token fields.
	isImageType := cfg.Service.Type == constant.ServiceTypeTextToImage ||
		cfg.Service.Type == constant.ServiceTypeImageEditing
	// Video generation bills per effective output second. A single-model video
	// service (no modelPricing) uses service.outputPriceUSDPerSecond instead of
	// the per-1M-token fields; a multi-model video service carries the USD price
	// per entry instead (see the multiModelUSD branch below).
	isVideoType := cfg.Service.Type == constant.ServiceTypeVideoGeneration
	multiModelUSD := len(cfg.Service.ModelPricing) > 0
	switch cfg.Service.PriceDenomination {
	case constant.PriceDenominationNative:
		if cfg.Service.InputPriceUSDPerMillionTokens != "" || cfg.Service.OutputPriceUSDPerMillionTokens != "" {
			return fmt.Errorf("invalid config: service.inputPriceUSDPerMillionTokens / service.outputPriceUSDPerMillionTokens must be empty when priceDenomination is '%s'", constant.PriceDenominationNative)
		}
		if cfg.Service.OutputPriceUSDPerImage != "" {
			return fmt.Errorf("invalid config: service.outputPriceUSDPerImage is only valid when priceDenomination is '%s'", constant.PriceDenominationUSD)
		}
		if cfg.Service.OutputPriceUSDPerSecond != "" {
			return fmt.Errorf("invalid config: service.outputPriceUSDPerSecond is only valid when priceDenomination is '%s'", constant.PriceDenominationUSD)
		}
	case constant.PriceDenominationUSD:
		if cfg.Service.InputPrice != "" || cfg.Service.OutputPrice != "" {
			return fmt.Errorf("invalid config: service.inputPrice / service.outputPrice must be empty when priceDenomination is '%s' (use the USD fields)", constant.PriceDenominationUSD)
		}
		if isImageType {
			if cfg.Service.OutputPriceUSDPerSecond != "" {
				return fmt.Errorf("invalid config: service.outputPriceUSDPerSecond is only valid for service type '%s', got '%s'", constant.ServiceTypeVideoGeneration, cfg.Service.Type)
			}
			// USD image service: outputPriceUSDPerImage is mandatory; the per-1M-token
			// fields are forbidden (image bills per image, not per token).
			if cfg.Service.InputPriceUSDPerMillionTokens != "" || cfg.Service.OutputPriceUSDPerMillionTokens != "" {
				return fmt.Errorf("invalid config: service.inputPriceUSDPerMillionTokens / service.outputPriceUSDPerMillionTokens must be empty for service type '%s' under USD denomination (use service.outputPriceUSDPerImage)", cfg.Service.Type)
			}
			if cfg.Service.OutputPriceUSDPerImage == "" {
				return fmt.Errorf("invalid config: service.outputPriceUSDPerImage is required for service type '%s' when priceDenomination is '%s'", cfg.Service.Type, constant.PriceDenominationUSD)
			}
			normalized, err := normalizeUSDPerUnitPrice("service.outputPriceUSDPerImage", cfg.Service.OutputPriceUSDPerImage)
			if err != nil {
				return err
			}
			cfg.Service.OutputPriceUSDPerMillionTokens = normalized
			cfg.Service.InputPriceUSDPerMillionTokens = "0"
		} else if isVideoType && !multiModelUSD {
			// USD single-model video service: outputPriceUSDPerSecond is mandatory; the
			// per-1M-token and per-image fields are forbidden (video bills per effective
			// output second, not per token or per image).
			if cfg.Service.InputPriceUSDPerMillionTokens != "" || cfg.Service.OutputPriceUSDPerMillionTokens != "" {
				return fmt.Errorf("invalid config: service.inputPriceUSDPerMillionTokens / service.outputPriceUSDPerMillionTokens must be empty for service type '%s' under USD denomination (use service.outputPriceUSDPerSecond)", cfg.Service.Type)
			}
			if cfg.Service.OutputPriceUSDPerImage != "" {
				return fmt.Errorf("invalid config: service.outputPriceUSDPerImage is only valid for image service types ('%s' / '%s'), got '%s'", constant.ServiceTypeTextToImage, constant.ServiceTypeImageEditing, cfg.Service.Type)
			}
			if cfg.Service.OutputPriceUSDPerSecond == "" {
				return fmt.Errorf("invalid config: service.outputPriceUSDPerSecond is required for service type '%s' when priceDenomination is '%s' and no modelPricing is configured", cfg.Service.Type, constant.PriceDenominationUSD)
			}
			normalized, err := normalizeUSDPerUnitPrice("service.outputPriceUSDPerSecond", cfg.Service.OutputPriceUSDPerSecond)
			if err != nil {
				return err
			}
			cfg.Service.OutputPriceUSDPerMillionTokens = normalized
			cfg.Service.InputPriceUSDPerMillionTokens = "0"
		} else {
			if cfg.Service.OutputPriceUSDPerImage != "" {
				return fmt.Errorf("invalid config: service.outputPriceUSDPerImage is only valid for image service types ('%s' / '%s'), got '%s'", constant.ServiceTypeTextToImage, constant.ServiceTypeImageEditing, cfg.Service.Type)
			}
			if cfg.Service.OutputPriceUSDPerSecond != "" {
				// Reaching this branch with isVideoType true already implies
				// multiModelUSD (the isVideoType && !multiModelUSD case is peeled
				// off by the "else if" above), so it's the modelPricing conflict,
				// not a wrong-service-type error.
				if isVideoType {
					return fmt.Errorf("invalid config: service.outputPriceUSDPerSecond is not valid alongside service.modelPricing (per-model entries carry the USD price instead)")
				}
				return fmt.Errorf("invalid config: service.outputPriceUSDPerSecond is only valid for service type '%s', got '%s'", constant.ServiceTypeVideoGeneration, cfg.Service.Type)
			}
			// With multi-model pricing the per-model entries carry the USD prices and
			// the service-level USD fields are derived (max-over-models) later in this
			// function, so they may legitimately be empty here.
			if !multiModelUSD && (cfg.Service.InputPriceUSDPerMillionTokens == "" || cfg.Service.OutputPriceUSDPerMillionTokens == "") {
				return fmt.Errorf("invalid config: service.inputPriceUSDPerMillionTokens and service.outputPriceUSDPerMillionTokens are required when priceDenomination is '%s'", constant.PriceDenominationUSD)
			}
			if cfg.Service.InputPriceUSDPerMillionTokens != "" {
				if err := validateUSDPriceString("service.inputPriceUSDPerMillionTokens", cfg.Service.InputPriceUSDPerMillionTokens); err != nil {
					return err
				}
			}
			if cfg.Service.OutputPriceUSDPerMillionTokens != "" {
				if err := validateUSDPriceString("service.outputPriceUSDPerMillionTokens", cfg.Service.OutputPriceUSDPerMillionTokens); err != nil {
					return err
				}
			}
		}
		if err := validatePriceFeedConfig(&cfg.PriceFeed); err != nil {
			return err
		}
	default:
		return fmt.Errorf("invalid config: service.priceDenomination must be '%s' or '%s', got '%s'", constant.PriceDenominationNative, constant.PriceDenominationUSD, cfg.Service.PriceDenomination)
	}

	// Validate tiered pricing configuration
	if cfg.TieredPricing.Enabled {
		if len(cfg.TieredPricing.Tiers) == 0 {
			return fmt.Errorf("invalid config: tieredPricing.tiers must not be empty when tieredPricing is enabled")
		}
		if err := validatePricingTiers("tieredPricing.tiers", cfg.TieredPricing.Tiers); err != nil {
			return err
		}
	}

	// Service-level cache-discount divisor (also covers the single-model path).
	if err := validateCacheTokenBilling("cacheTokenBilling", &cfg.CacheTokenBilling); err != nil {
		return err
	}

	// Validate per-model pricing, build the lookup map, and set the on-chain
	// ceiling — see model_pricing.go.
	if err := validateModelPricing(cfg); err != nil {
		return err
	}

	// Per-wallet usage stats defaults. Only meaningful when the feature is
	// enabled; leaving them unset gives a 90-day retention pruned daily.
	if cfg.UserUsageStats.Enabled {
		if cfg.UserUsageStats.RetentionDays == 0 {
			cfg.UserUsageStats.RetentionDays = 90
		}
		if cfg.UserUsageStats.PruneInterval == 0 {
			cfg.UserUsageStats.PruneInterval = 24 * time.Hour
		}
	}

	// Reconciliation hourly-rollup retention defaults. The rollup is always written, so
	// default to a bounded 90-day retention pruned daily. Unset (0) → 90; a negative
	// value disables pruning (keep forever) via the RetentionDays > 0 gate in main.
	if cfg.Reconciliation.RetentionDays == 0 {
		cfg.Reconciliation.RetentionDays = 90
	}
	if cfg.Reconciliation.PruneInterval == 0 {
		cfg.Reconciliation.PruneInterval = 24 * time.Hour
	}

	return nil
}

func GetConfig() *Config {
	once.Do(func() {
		instance = &Config{
			AllowOrigins:    []string{"*"},
			ContractAddress: "0x47340d900bdFec2BD393c626E12ea0656F938d84",
			Database: struct {
				DSN      string `yaml:"dsn"`
				Provider string `yaml:"provider,omitempty"`
			}{
				DSN: "root:123456@tcp(mysql:3306)/provider?parseTime=true",
			},
			Event: struct {
				ListenAddr   string `yaml:"listenAddr"`
				ProviderAddr string `yaml:"providerAddr,omitempty"`
			}{
				ListenAddr: ":8088",
			},
			GasPrice:    "2000000007",
			MaxGasPrice: "",
			Interval: struct {
				AutoSettleBufferTime     time.Duration `yaml:"autoSettleBufferTime"`
				ForceSettlementProcessor time.Duration `yaml:"forceSettlementProcessor"`
				SettlementProcessor      time.Duration `yaml:"settlementProcessor"`
				ReconciliationProcessor  time.Duration `yaml:"reconciliationProcessor"`
			}{
				AutoSettleBufferTime:     60 * time.Second,
				ForceSettlementProcessor: 10 * time.Minute,
				SettlementProcessor:      5 * time.Minute,
				ReconciliationProcessor:  60 * time.Second,
			},
			Settlement: struct {
				MinSettlementFee string `yaml:"minSettlementFee"`
			}{
				MinSettlementFee: "4000000000000000",
			},
			RevenueTransfer: struct {
				TargetAddress string        `yaml:"targetAddress"`
				ReserveAmount string        `yaml:"reserveAmount"`
				Interval      time.Duration `yaml:"interval"`
			}{
				TargetAddress: "",
				ReserveAmount: "10000000000000000000",
				Interval:      time.Hour,
			},
			Monitor: struct {
				Enable       bool   `yaml:"enable"`
				EventAddress string `yaml:"eventAddress"`
			}{
				Enable:       false,
				EventAddress: "0g-serving-provider-event:3081",
			},
			ZK: struct {
				URL           string `yaml:"url"`
				Provider      string `yaml:"provider,omitempty"`
				RequestLength int    `yaml:"requestLength"`
			}{
				URL:           "nginx:3001",
				RequestLength: 40,
			},
			LoRA: LoRAConfig{
				Enable:            false,
				LoraModulesDir:    "/data/lora-modules",
				SllmUrl:           "http://sllm:8343",
				OffloadAfter:      60 * time.Minute,
				EnableColdStorage: false,
				PollBlockInterval: 5 * time.Second,
				StorageTurbo:      false,
			},
			ChatCacheExpiration: time.Minute * 20,
			NvGPU:               false,
			Logger: &config.LoggerConfig{
				Format:        "text",
				Level:         "info",
				Path:          "./logs/inference.log",
				RotationCount: 7,
			},
			LogPaths: LogPathsConfig{
				BrokerLogDir: "/var/log/inference",
				EventLogDir:  "/var/log/event",
			},
			Controller: ControllerConfig{
				Enable:         false,
				Port:           3090,
				AdminAddresses: []string{},
				AllowedIPs:     []string{},
				Image:          "ghcr.io/0gfoundation/0g-serving-broker:latest",
				Docker: DockerConfig{
					Host:       "unix:///var/run/docker.sock",
					APIVersion: "1.41",
				},
				Containers: ContainersConfig{
					Broker:         "0g-serving-provider-broker",
					Event:          "0g-serving-provider-event",
					Ingress:        "broker-ingress",
					PrometheusInit: "prometheus-init",
					Prometheus:     "prometheus",
				},
				Logger: &config.LoggerConfig{
					Format:        "text",
					Level:         "info",
					Path:          "./logs/controller.log",
					RotationCount: 7,
				},
			},
			CacheTokenBilling: CacheTokenBillingConfig{
				Enabled: false,
				Divisor: 4,
			},
			TieredPricing: TieredPricingConfig{
				Enabled: false,
				Tiers:   nil,
			},
			Whitelist: WhitelistConfig{
				Enabled:       false,
				UserAddresses: []string{},
			},
			ConcurrencyLimit: ConcurrencyLimitConfig{
				MaxGlobalConcurrent:  20,
				MaxPerUserConcurrent: 5,
				PerUserRPM:           30,
				PerUserBurst:         5,
				PerUserTPM:           0, // disabled by default
				PerUserTPMBurst:      0,
				PerUserIPM:           0, // disabled by default
				PerUserIPMBurst:      0,
				PerUserOverrides:     nil,
			},
			Async: AsyncConfig{
				Enabled:           true,
				MaxConcurrentJobs: 10,
				MaxQueueSize:      100,
				ResultTTL:         30 * time.Minute,
				CleanupInterval:   60 * time.Second,
				JobTimeout:        15 * time.Minute,
			},
			VideoPoll: VideoPollConfig{
				Enabled:            true,
				MaxConcurrentPolls: 10,
				PollInterval:       10 * time.Second,
				MaxPollDuration:    20 * time.Minute,
				ScanInterval:       5 * time.Second,
				LeaseWindow:        90 * time.Second, // 3x PollRequestTimeout, leaving margin for parse/bill/write
				PollRequestTimeout: 30 * time.Second,
				RetentionTTL:       30 * time.Minute,
				CleanupInterval:    5 * time.Minute,
			},
			ProviderHttp: ProviderHttpConfig{
				TotalTimeout:          15 * time.Minute,
				ResponseHeaderTimeout: 15 * time.Minute,
			},
		}

		if err := loadConfig(instance); err != nil {
			panic(err)
		}

		instance.Network.PrivateKeyStore = config.NewPrivateKeyStore(&instance.Network)
	})

	return instance
}
