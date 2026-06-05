package config

import (
	"fmt"
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
	SupportedFormats    []string               `yaml:"supportedFormats"`    // Optional. API formats this model supports, e.g., ["openai", "anthropic"]. Defaults to ["openai"] if omitted.
	DefaultParameters   map[string]interface{} `yaml:"defaultParameters"`   // Optional. Default values for parameters, e.g., {"temperature": 0.7, "top_p": 0.9}
	TeeType             string                 `yaml:"teeType"`             // Optional. TEE hardware type, e.g., "TDX", "SEV", "SGX", "H100"
	ExpirationDate      string                 `yaml:"expirationDate"`      // Optional. Model availability expiration in RFC3339 format, e.g., "2026-12-31T00:00:00Z"

	// VideoSizeRatios maps output resolution (e.g., "1280x720") to a cost multiplier.
	// Used for video generation billing: fee = seconds × sizeRatio × outputPrice.
	// Defaults are applied if not configured (see DefaultVideoSizeRatios).
	VideoSizeRatios map[string]float64 `yaml:"videoSizeRatios"`
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
	return nil
}

// ModelWildcard is the catch-all model id in service.modelPricing. When an
// entry uses this id, the broker serves ANY requested model (the allowlist
// becomes "allow all") and bills models not matched by an exact entry at the
// wildcard entry's price/tiers. Use it to proxy an upstream that serves many
// models without enumerating every one.
const ModelWildcard = "*"

// ModelPricingEntry defines per-model pricing for centralized multi-model providers.
// Each entry represents a model that the broker can serve with its own pricing.
// Prices are expressed in the SERVICE's priceDenomination: NATIVE entries use
// InputPrice/OutputPrice (neuron per token), USD entries use the USD-per-1M-token
// fields. The two sets are mutually exclusive within a service.
type ModelPricingEntry struct {
	Model string `yaml:"model"` // Model identifier (e.g., "qwen3-max"), or ModelWildcard ("*")

	// NATIVE-denominated per-token prices in neuron. Required iff the service
	// priceDenomination is NATIVE.
	InputPrice  string `yaml:"inputPrice"`
	OutputPrice string `yaml:"outputPrice"`

	// USD-denominated prices in USD per 1M tokens (decimal string). Required iff
	// the service priceDenomination is USD. Converted to wei per token at billing
	// time using the live 0G/USD rate, exactly like the service-level USD price.
	InputPriceUSDPerMillionTokens  string `yaml:"inputPriceUSDPerMillionTokens"`
	OutputPriceUSDPerMillionTokens string `yaml:"outputPriceUSDPerMillionTokens"`

	// Tiers is optional per-model input-length tiered pricing. When empty, the
	// service-level tieredPricing applies (if enabled). Same semantics and
	// validation as TieredPricingConfig.Tiers.
	Tiers []PricingTier `yaml:"tiers"`

	// CanonicalID is the bare-lowercase canonical model id this model maps to in
	// the router catalog (same contract as Service.CanonicalID, but per-model so
	// a multi-model provider can map each served model to its own canonical).
	// Optional; empty means the router resolves the canonical from its registry
	// by Model id instead. Surfaced per-model in GET /v1/models.
	CanonicalID string `yaml:"canonicalId"`

	// Type optionally overrides the service modality for this model. Reserved for
	// future single-process multi-modal serving; for now it must be empty or
	// equal to the service Type.
	Type string `yaml:"type"`
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
	AdditionalSecret map[string]string `yaml:"additionalSecret"`
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

	// ModelPricing defines per-model pricing for centralized providers that serve multiple models.
	// When configured, the broker validates requested models against this allowlist
	// and bills at model-specific rates instead of the single on-chain price.
	// On-chain registration uses max(model prices) as InputPrice/OutputPrice.
	// Only used when ProviderType is "centralized".
	ModelPricing []ModelPricingEntry `yaml:"modelPricing"`

	// modelPricingMap is a derived lookup map built during config validation.
	modelPricingMap map[string]*ModelPricingEntry `yaml:"-"`
}

// IsCentralized returns true if this service routes to a centralized API provider.
func (s *Service) IsCentralized() bool {
	return s.ProviderType == constant.ProviderTypeCentralized
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

// HasMultiModelPricing returns true if this service has per-model pricing configured.
func (s *Service) HasMultiModelPricing() bool {
	// Read the derived map (built in validate()), not the raw slice, so this
	// predicate cannot disagree with GetModelPricing/IsModelAllowed: if the map
	// is not built the service degrades to single-model (on-chain price) rather
	// than rejecting every request.
	return len(s.modelPricingMap) > 0
}

// GetModelPricing returns the pricing entry for a specific model. Resolution
// order: exact match first, then the wildcard ("*") entry if configured, else
// nil. This mirrors IsModelAllowed so a model that passes the allowlist always
// resolves to a pricing entry.
func (s *Service) GetModelPricing(model string) *ModelPricingEntry {
	if s.modelPricingMap == nil {
		return nil
	}
	if entry, ok := s.modelPricingMap[model]; ok {
		return entry
	}
	if entry, ok := s.modelPricingMap[ModelWildcard]; ok {
		return entry
	}
	return nil
}

// BuildModelPricingMap rebuilds the derived per-model lookup map from the
// ModelPricing slice and stores it on the Service, returning an error on
// duplicate model ids. It is the single source of truth for the lookup map:
// loadConfig calls it after per-entry validation, and tests use it to construct
// a usable multi-model Service without a full config file.
//
// It does NOT validate prices/denomination/tiers — callers that rely on the
// MaxModelPrices* helpers or on per-model billing must validate the entries
// first (loadConfig does). The map stores pointers into the ModelPricing slice,
// so the slice must not be reallocated after this call.
func (s *Service) BuildModelPricingMap() error {
	m := make(map[string]*ModelPricingEntry, len(s.ModelPricing))
	for i := range s.ModelPricing {
		entry := &s.ModelPricing[i]
		if _, ok := m[entry.Model]; ok {
			return fmt.Errorf("duplicate model %q in modelPricing", entry.Model)
		}
		m[entry.Model] = entry
	}
	s.modelPricingMap = m
	return nil
}

// HasWildcardModel reports whether a catch-all ("*") pricing entry is configured.
func (s *Service) HasWildcardModel() bool {
	if s.modelPricingMap == nil {
		return false
	}
	_, ok := s.modelPricingMap[ModelWildcard]
	return ok
}

// IsModelAllowed returns true if the model is in the pricing allowlist.
// Always returns true if multi-model pricing is not configured, or if a
// wildcard ("*") entry is present (serve-all mode).
func (s *Service) IsModelAllowed(model string) bool {
	if !s.HasMultiModelPricing() {
		return true
	}
	if _, ok := s.modelPricingMap[ModelWildcard]; ok {
		return true
	}
	_, ok := s.modelPricingMap[model]
	return ok
}

// maxTierMultipliers returns the highest input/output multipliers across the
// effective tier set for this entry (the entry's own tiers, or serviceTiers
// when the entry has none). Returns (1, 1) when no tiers apply. Used to size
// the on-chain advertised ceiling so it covers the worst-case tiered price.
func (e *ModelPricingEntry) maxTierMultipliers(serviceTiers []PricingTier) (int64, int64) {
	tiers := e.Tiers
	if len(tiers) == 0 {
		tiers = serviceTiers
	}
	maxIn, maxOut := int64(1), int64(1)
	for _, t := range tiers {
		if t.InputMultiplier > maxIn {
			maxIn = t.InputMultiplier
		}
		if t.OutputMultiplier > maxOut {
			maxOut = t.OutputMultiplier
		}
	}
	return maxIn, maxOut
}

// MaxModelPricesNative returns the maximum tier-adjusted InputPrice and
// OutputPrice (neuron) across all NATIVE model pricing entries. serviceTiers is
// the service-level tier fallback for entries without their own tiers.
// Precondition: every entry's prices have already been validated as parseable
// integers (validate() enforces this before the only call site).
func (s *Service) MaxModelPricesNative(serviceTiers []PricingTier) (maxInput, maxOutput string) {
	maxIn := big.NewInt(0)
	maxOut := big.NewInt(0)
	for i := range s.ModelPricing {
		entry := &s.ModelPricing[i]
		mulIn, mulOut := entry.maxTierMultipliers(serviceTiers)
		if v, ok := new(big.Int).SetString(entry.InputPrice, 10); ok {
			v.Mul(v, big.NewInt(mulIn))
			if v.Cmp(maxIn) > 0 {
				maxIn = v
			}
		}
		if v, ok := new(big.Int).SetString(entry.OutputPrice, 10); ok {
			v.Mul(v, big.NewInt(mulOut))
			if v.Cmp(maxOut) > 0 {
				maxOut = v
			}
		}
	}
	return maxIn.String(), maxOut.String()
}

// MaxModelUSDPrices returns the maximum tier-adjusted USD-per-1M-token input and
// output prices (decimal strings) across all USD model pricing entries. Feeds
// the service-level USD price so the existing price-feed machinery advertises an
// on-chain ceiling covering every served model at its worst-case tier.
// Precondition: every entry's USD prices have already been validated as
// non-negative decimals (validate() enforces this before the only call site).
func (s *Service) MaxModelUSDPrices(serviceTiers []PricingTier) (maxInput, maxOutput string) {
	maxIn := new(big.Rat)
	maxOut := new(big.Rat)
	for i := range s.ModelPricing {
		entry := &s.ModelPricing[i]
		mulIn, mulOut := entry.maxTierMultipliers(serviceTiers)
		if r, ok := new(big.Rat).SetString(entry.InputPriceUSDPerMillionTokens); ok {
			r.Mul(r, new(big.Rat).SetInt64(mulIn))
			if r.Cmp(maxIn) > 0 {
				maxIn = r
			}
		}
		if r, ok := new(big.Rat).SetString(entry.OutputPriceUSDPerMillionTokens); ok {
			r.Mul(r, new(big.Rat).SetInt64(mulOut))
			if r.Cmp(maxOut) > 0 {
				maxOut = r
			}
		}
	}
	return ratToDecimalString(maxIn), ratToDecimalString(maxOut)
}

// ratToDecimalString formats a non-negative big.Rat as a decimal string with
// trailing zeros trimmed (e.g. "1.6", "8"). 18 decimals of precision is more
// than any sensible USD price carries, so the trimmed output is exact.
func ratToDecimalString(r *big.Rat) string {
	s := r.FloatString(18)
	if !strings.ContainsRune(s, '.') {
		return s
	}
	i := len(s)
	for i > 0 && s[i-1] == '0' {
		i--
	}
	if i > 0 && s[i-1] == '.' {
		i--
	}
	return s[:i]
}

// DefaultVideoSizeRatios provides default cost multipliers based on pixel count
// relative to the baseline 720x1280 (921,600 pixels).
//
//	832x480   = 399,360 px → 0.5
//	480x832   = 399,360 px → 0.5
//	720x1280  = 921,600 px → 1.0 (baseline)
//	1280x720  = 921,600 px → 1.0
//	1024x1792 = 1,835,008 px → 2.0
//	1792x1024 = 1,835,008 px → 2.0
var DefaultVideoSizeRatios = map[string]float64{
	"832x480":   0.5,
	"480x832":   0.5,
	"720x1280":  1.0,
	"1280x720":  1.0,
	"1024x1792": 2.0,
	"1792x1024": 2.0,
}

// GetVideoSizeRatio returns the cost multiplier for a given resolution.
// Falls back to DefaultVideoSizeRatios if ModelInfo is nil or has no custom ratios.
// Returns the default resolution ratio (720x1280 = 1.0) if the size is unknown.
func (s *Service) GetVideoSizeRatio(size string) float64 {
	var ratios map[string]float64
	if s.ModelInfo != nil {
		ratios = s.ModelInfo.VideoSizeRatios
	}
	if len(ratios) == 0 {
		ratios = DefaultVideoSizeRatios
	}
	if ratio, ok := ratios[size]; ok {
		return ratio
	}
	// Unknown size: fall back to baseline ratio
	return 1.0
}

// CacheTokenBillingConfig defines configuration for cached token discount billing.
// When enabled, cached input tokens (reported by the LLM via prompt_tokens_details.cached_tokens)
// are billed at a discounted rate: inputPrice / Divisor.
type CacheTokenBillingConfig struct {
	Enabled bool  `yaml:"enabled"` // Enable cached token discount billing (default: false)
	Divisor int64 `yaml:"divisor"` // Discount divisor for cached tokens (e.g., 4 means 25% of full price)
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
type PricingTier struct {
	MaxInputTokens   int   `yaml:"maxInputTokens"`   // Upper bound of input tokens for this tier (0 = unlimited)
	InputMultiplier  int64 `yaml:"inputMultiplier"`  // Multiplier for input price in this tier
	OutputMultiplier int64 `yaml:"outputMultiplier"` // Multiplier for output price in this tier
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
	ProviderHttp        ProviderHttpConfig      `yaml:"providerHttp"`
	ConcurrencyLimit    ConcurrencyLimitConfig  `yaml:"concurrencyLimit"`
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

// validatePricingTiers validates an ordered tier list: multipliers >= 1,
// maxInputTokens >= 0, the unbounded (0) tier last, and strictly ascending
// order. An empty slice is valid (no tiers). prefix labels errors (e.g.
// "tieredPricing.tiers" or "service.modelPricing[0].tiers").
func validatePricingTiers(prefix string, tiers []PricingTier) error {
	for i, tier := range tiers {
		if tier.InputMultiplier < 1 {
			return fmt.Errorf("invalid config: %s[%d].inputMultiplier must be >= 1, got %d", prefix, i, tier.InputMultiplier)
		}
		if tier.OutputMultiplier < 1 {
			return fmt.Errorf("invalid config: %s[%d].outputMultiplier must be >= 1, got %d", prefix, i, tier.OutputMultiplier)
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
	if cfg.Service.ProviderType != constant.ProviderTypeDecentralized && cfg.Service.ProviderType != constant.ProviderTypeCentralized {
		return fmt.Errorf("invalid config: service.providerType must be '%s' or '%s', got '%s'", constant.ProviderTypeDecentralized, constant.ProviderTypeCentralized, cfg.Service.ProviderType)
	}
	if cfg.Service.ProviderType == constant.ProviderTypeCentralized {
		if cfg.Service.ProviderIdentity == "" {
			return fmt.Errorf("invalid config: service.providerIdentity is required when providerType is 'centralized'")
		}
		cfg.Service.ProviderIdentity = strings.ToLower(cfg.Service.ProviderIdentity)
		if !validProviderIdentity.MatchString(cfg.Service.ProviderIdentity) {
			return fmt.Errorf("invalid config: service.providerIdentity must be lowercase alphanumeric with optional hyphens (e.g., 'openai', 'anthropic'), got '%s'", cfg.Service.ProviderIdentity)
		}
		// Centralized providers always behave as TargetSeparated (shared external backend)
		cfg.Service.TargetSeparated = true
		// Require HTTPS for centralized providers — routing proof relies on
		// resp.TLS which is only populated for HTTPS connections.
		if cfg.Service.TargetURL != "" && !strings.HasPrefix(strings.ToLower(cfg.Service.TargetURL), "https://") {
			return fmt.Errorf("invalid config: service.targetUrl must use HTTPS for centralized providers (routing proof requires TLS), got '%s'", cfg.Service.TargetURL)
		}
	}

	// Normalize and validate price denomination / priceFeed configuration.
	if cfg.Service.PriceDenomination == "" {
		cfg.Service.PriceDenomination = constant.PriceDenominationNative
	}
	cfg.Service.PriceDenomination = strings.ToUpper(cfg.Service.PriceDenomination)
	switch cfg.Service.PriceDenomination {
	case constant.PriceDenominationNative:
		if cfg.Service.InputPriceUSDPerMillionTokens != "" || cfg.Service.OutputPriceUSDPerMillionTokens != "" {
			return fmt.Errorf("invalid config: service.inputPriceUSDPerMillionTokens / service.outputPriceUSDPerMillionTokens must be empty when priceDenomination is '%s'", constant.PriceDenominationNative)
		}
	case constant.PriceDenominationUSD:
		// With multi-model pricing the per-model entries carry the USD prices and
		// the service-level USD fields are derived (max-over-models) later in this
		// function, so they may legitimately be empty here.
		multiModelUSD := len(cfg.Service.ModelPricing) > 0
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
		if cfg.Service.InputPrice != "" || cfg.Service.OutputPrice != "" {
			return fmt.Errorf("invalid config: service.inputPrice / service.outputPrice must be empty when priceDenomination is '%s' (use the USD fields)", constant.PriceDenominationUSD)
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

	// Validate and build model pricing map
	if len(cfg.Service.ModelPricing) > 0 {
		if cfg.Service.ProviderType != constant.ProviderTypeCentralized {
			return fmt.Errorf("invalid config: service.modelPricing is only supported when providerType is 'centralized'")
		}
		// Per-model billing is wired only for the token-based modalities whose
		// request path resolves the request model before billing (chatbot and
		// speech-to-text). On other modalities the allowlist would never run and
		// every request would silently fall back to the on-chain max price, so
		// reject the configuration at load time rather than honour neither the
		// allowlist nor the per-model prices.
		if cfg.Service.Type != constant.ServiceTypeChatbot && cfg.Service.Type != constant.ServiceTypeSpeechToText {
			return fmt.Errorf("invalid config: service.modelPricing is only supported for service type '%s' or '%s', got '%s'", constant.ServiceTypeChatbot, constant.ServiceTypeSpeechToText, cfg.Service.Type)
		}
		// service.model is the default billed (and forwarded-upstream) model for
		// requests that omit the model field; it must be set.
		if cfg.Service.ModelType == "" {
			return fmt.Errorf("invalid config: service.model is required when service.modelPricing is configured")
		}
		isUSD := cfg.Service.IsUSDDenominated()

		hasWildcard := false
		for i := range cfg.Service.ModelPricing {
			entry := &cfg.Service.ModelPricing[i]
			if entry.Model == "" {
				return fmt.Errorf("invalid config: service.modelPricing[%d].model is required", i)
			}
			if entry.Model == ModelWildcard {
				hasWildcard = true
			}
			// Per-model modality (Type override) is reserved for future
			// single-process multi-modal serving; for now it must match the
			// service modality, otherwise billing would dispatch incorrectly.
			if entry.Type != "" && entry.Type != cfg.Service.Type {
				return fmt.Errorf("invalid config: service.modelPricing[%d].type %q must equal service.type %q (per-model modality is not yet supported)", i, entry.Type, cfg.Service.Type)
			}

			// Prices must be expressed in the service's denomination.
			if isUSD {
				if entry.InputPrice != "" || entry.OutputPrice != "" {
					return fmt.Errorf("invalid config: service.modelPricing[%d] must use the USD price fields (priceDenomination is '%s') for model '%s'", i, constant.PriceDenominationUSD, entry.Model)
				}
				if entry.InputPriceUSDPerMillionTokens == "" || entry.OutputPriceUSDPerMillionTokens == "" {
					return fmt.Errorf("invalid config: service.modelPricing[%d].inputPriceUSDPerMillionTokens / outputPriceUSDPerMillionTokens are required for model '%s'", i, entry.Model)
				}
				if err := validateUSDPriceString(fmt.Sprintf("service.modelPricing[%d].inputPriceUSDPerMillionTokens", i), entry.InputPriceUSDPerMillionTokens); err != nil {
					return err
				}
				if err := validateUSDPriceString(fmt.Sprintf("service.modelPricing[%d].outputPriceUSDPerMillionTokens", i), entry.OutputPriceUSDPerMillionTokens); err != nil {
					return err
				}
			} else {
				if entry.InputPriceUSDPerMillionTokens != "" || entry.OutputPriceUSDPerMillionTokens != "" {
					return fmt.Errorf("invalid config: service.modelPricing[%d] must use the native price fields (priceDenomination is '%s') for model '%s'", i, constant.PriceDenominationNative, entry.Model)
				}
				if entry.InputPrice == "" {
					return fmt.Errorf("invalid config: service.modelPricing[%d].inputPrice is required for model '%s'", i, entry.Model)
				}
				if entry.OutputPrice == "" {
					return fmt.Errorf("invalid config: service.modelPricing[%d].outputPrice is required for model '%s'", i, entry.Model)
				}
				if _, ok := new(big.Int).SetString(entry.InputPrice, 10); !ok {
					return fmt.Errorf("invalid config: service.modelPricing[%d].inputPrice must be a valid integer for model '%s'", i, entry.Model)
				}
				if _, ok := new(big.Int).SetString(entry.OutputPrice, 10); !ok {
					return fmt.Errorf("invalid config: service.modelPricing[%d].outputPrice must be a valid integer for model '%s'", i, entry.Model)
				}
			}

			// Per-model tiers are optional; when present they must satisfy the
			// same ordering/multiplier rules as service-level tiers.
			if err := validatePricingTiers(fmt.Sprintf("service.modelPricing[%d].tiers", i), entry.Tiers); err != nil {
				return err
			}
			// Tiered pricing is applied only on the chatbot billing path
			// (getTierMultipliers over prompt tokens). Speech-to-text bills flat,
			// so per-model tiers would be advertised in /v1/models + on-chain but
			// never enforced — reject rather than silently diverge.
			if len(entry.Tiers) > 0 && cfg.Service.Type == constant.ServiceTypeSpeechToText {
				return fmt.Errorf("invalid config: service.modelPricing[%d].tiers is not supported for service type '%s' (tiers are not applied to its billing)", i, constant.ServiceTypeSpeechToText)
			}

			if entry.CanonicalID != "" && !validCanonicalID.MatchString(entry.CanonicalID) {
				return fmt.Errorf("invalid config: service.modelPricing[%d].canonicalId %q must be bare lowercase (letters, digits, '-', '.') for model '%s'", i, entry.CanonicalID, entry.Model)
			}
		}

		// Build the derived lookup map (single source of truth; also detects
		// duplicate model ids). The per-entry validation above establishes the
		// precondition (denomination-correct, parseable prices) that the
		// MaxModelPrices* helpers rely on.
		if err := cfg.Service.BuildModelPricingMap(); err != nil {
			return fmt.Errorf("invalid config: %w", err)
		}

		// service.model is the default applied when a request omits the model
		// field; unless a wildcard ("*") entry catches all models, it must itself
		// be a priced/allowlisted model, or such requests would be rejected by the
		// allowlist (and penalized by the mismatch rate limiter).
		if !hasWildcard && cfg.Service.GetModelPricing(cfg.Service.ModelType) == nil {
			return fmt.Errorf("invalid config: service.model '%s' must be one of the service.modelPricing entries (or add a '%s' wildcard entry)", cfg.Service.ModelType, ModelWildcard)
		}

		// Auto-set the service-level on-chain price to the tier-adjusted max over
		// all models, in the service's denomination. The existing single-price
		// machinery (native registration, or the USD price feed) then advertises
		// an on-chain ceiling that covers every served model at its worst-case
		// tier — per-model billing always resolves to a price <= this ceiling.
		if isUSD {
			cfg.Service.InputPriceUSDPerMillionTokens, cfg.Service.OutputPriceUSDPerMillionTokens =
				cfg.Service.MaxModelUSDPrices(cfg.TieredPricing.Tiers)
		} else {
			cfg.Service.InputPrice, cfg.Service.OutputPrice =
				cfg.Service.MaxModelPricesNative(cfg.TieredPricing.Tiers)
		}
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
			},
			Async: AsyncConfig{
				Enabled:           true,
				MaxConcurrentJobs: 10,
				MaxQueueSize:      100,
				ResultTTL:         30 * time.Minute,
				CleanupInterval:   60 * time.Second,
				JobTimeout:        15 * time.Minute,
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
