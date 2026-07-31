package config

import (
	"fmt"
	"log"
	"math"
	"math/big"
	"strings"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
)

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
	// the service priceDenomination is USD AND the modality bills per token
	// (chatbot / speech-to-text). Converted to wei per token at billing time using
	// the live 0G/USD rate, exactly like the service-level USD price.
	//
	// NOTE: for a USD video-generation model these two fields are NOT set by the
	// operator (video has no token unit) — they are DERIVED at config load from
	// OutputPriceUSDPerSecond as the per-1M-unit normalization the shared USD
	// pipeline (price feed, on-chain ceiling, per-model wei conversion) consumes;
	// the "unit" is the effective output second and the pipeline's ÷1e6 quantum
	// cancels the ×1e6 normalization to yield wei-per-second. See loadConfig.
	InputPriceUSDPerMillionTokens  string `yaml:"inputPriceUSDPerMillionTokens"`
	OutputPriceUSDPerMillionTokens string `yaml:"outputPriceUSDPerMillionTokens"`

	// OutputPriceUSDPerSecond is the USD price per effective output second for a
	// USD-denominated video-generation model (decimal string). Required iff the
	// service priceDenomination is USD and type is video-generation; forbidden
	// otherwise. The effective output second already folds in the resolution
	// multiplier (videoOutputUnits), so this is "USD per billed unit". At config
	// load it is normalized into OutputPriceUSDPerMillionTokens (×1e6) so the
	// existing token-shaped USD machinery prices and advertises it unchanged.
	OutputPriceUSDPerSecond string `yaml:"outputPriceUSDPerSecond"`

	// Tiers is optional per-model input-length tiered pricing. When empty, the
	// service-level tieredPricing applies (if enabled). Same semantics and
	// validation as TieredPricingConfig.Tiers.
	Tiers []PricingTier `yaml:"tiers"`

	// UpstreamModel, when set, is the model id sent to the upstream targetUrl for
	// this entry; Model stays the id advertised on-chain and accepted on incoming
	// requests. It is the per-model counterpart of service.UpstreamModel (which is
	// rejected alongside modelPricing): a multi-model centralized provider can
	// expose a stable public id (e.g. "zai-org/GLM-5-FP8") while forwarding to an
	// upstream that uses a different id (e.g. OpenRouter's "z-ai/glm-5"). Empty
	// means "forward Model as-is". Only supported for chatbot services (the JSON
	// request path is the only one that rewrites the body before forwarding).
	UpstreamModel string `yaml:"upstreamModel"`

	// ModelAliases are additional legacy model ids accepted on incoming requests
	// and resolved to this entry (billed at this entry's price, forwarded as
	// UpstreamModel/Model). The per-model counterpart of service.ModelAliases: it
	// lets an operator rename the advertised Model without breaking clients still
	// sending the old id. Aliases must be globally unique across all entries' Model
	// ids and aliases, and must not be the "*" wildcard. Only supported for chatbot
	// services. NOTE: aliases are accepted but NOT advertised in GET /v1/models
	// (only Model/CanonicalID are) — they exist for backward-compatible renames,
	// not as separately discoverable model names.
	ModelAliases []string `yaml:"modelAliases"`

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

	// ModelInfo is optional per-model metadata surfaced in GET /v1/models
	// (architecture, context length, supported/default parameters, …). A
	// multi-model provider is usually heterogeneous, so each model can carry its
	// own metadata here. Resolution at render time: this entry's ModelInfo wins;
	// when nil the service-level service.modelInfo is used as a fallback (covers
	// a same-family catalog without repeating the block per entry). When set it
	// must be COMPLETE — the same required fields as service.modelInfo — so a
	// half-filled entry can never advertise a misleading capability set.
	ModelInfo *ModelInfo `yaml:"modelInfo"`

	// Billing optionally selects a non-token billing shape for this model
	// (per-image / per-video-second / per-unit-table). Empty/nil means per_token
	// (the existing chat/STT token billing). The fee is OutputUnits × OutputPrice
	// (the entry's price, in the service denomination); see
	// docs/design/multimodal-billing.md. NOTE (P1, incremental): the engine and
	// schema below are validated and unit-tested, but image/video request paths
	// are not yet wired — loadConfig still rejects modelPricing on those service
	// types, so a Billing block only takes effect once that wiring lands.
	Billing *BillingConfig `yaml:"billing"`

	// CacheTokenBilling optionally overrides the service-level cacheTokenBilling
	// discount for THIS model. nil falls back to the service-level config (so a
	// homogeneous catalog needs no per-model block). Lets a heterogeneous
	// multi-vendor provider apply each model's own cache-read discount (e.g.
	// Anthropic ~10% vs OpenAI ~25–50% of the input price). Surfaced per-model in
	// GET /v1/models. Same validation as the service-level block: divisor >= 1
	// when enabled.
	CacheTokenBilling *CacheTokenBillingConfig `yaml:"cacheTokenBilling"`

	// InjectBodyFields is the per-model counterpart of service.injectBodyFields:
	// top-level key/value pairs merged into the forwarded chat body for requests
	// resolved to THIS model. It is deep-merged ON TOP of the service-level map
	// (this entry wins on leaf conflicts; see Service.EffectiveInjectBodyFields),
	// so a multi-model service can share routing (e.g. provider.sort) at the
	// service level while each model adds its own override — the canonical use is
	// a per-model OpenRouter provider.max_price cap, which a single service-level
	// map cannot express when models have different price floors. Same load-time
	// rules as the service-level field: broker-critical keys are rejected, the map
	// is normalized and verified JSON-serializable, and it is only supported for
	// the chatbot service type. Empty/unset means the service-level map applies
	// unchanged.
	InjectBodyFields map[string]interface{} `yaml:"injectBodyFields"`

	// StripBodyFields is the per-model counterpart of service.stripBodyFields:
	// top-level keys removed from the forwarded chat body for requests resolved to
	// THIS model. It is UNIONed with the service-level list (not deep-merged): the
	// effective strip set is every key named at either level (see
	// Service.EffectiveStripBodyFields), so a multi-model service can strip a param
	// globally while one model strips an extra param its backend rejects. Same
	// load-time rules as the service-level field: broker-critical keys are rejected
	// and it is only supported for the chatbot service type. Empty/unset means the
	// service-level list applies unchanged.
	StripBodyFields []string `yaml:"stripBodyFields"`

	// AdditionalSecret is the per-model counterpart of service.additionalSecret:
	// outbound header name/value pairs (typically the upstream API key, e.g.
	// Authorization: "Bearer sk-...") attached to requests resolved to THIS model.
	// When set it REPLACES the service-level map wholesale for this model (NOT a
	// key-by-key merge) — see Service.EffectiveAdditionalSecret for why — so a
	// per-model block must list every header that model's upstream needs.
	// Empty/unset means the service-level map applies. The motivating case is an
	// upstream (e.g. dgrid) that requires a DIFFERENT API key per model, where the
	// service-level key can no longer be shared. json:"-" keeps the secret out of
	// any accidental struct marshal (defense-in-depth; nothing marshals it today).
	AdditionalSecret map[string]string `yaml:"additionalSecret" json:"-"`
}

// BillingMode selects how a model's per-request fee is computed. Empty defaults
// to per_token (existing chat/STT token billing). The others cover non-token
// modalities — see docs/design/multimodal-billing.md.
type BillingMode string

const (
	BillingModePerToken       BillingMode = "per_token"
	BillingModePerImage       BillingMode = "per_image"
	BillingModePerVideoSecond BillingMode = "per_video_second"
	BillingModePerUnitTable   BillingMode = "per_unit_table"
)

// BillingUnitTier maps a (resolution, duration) combination to a fixed billable
// unit count — used by per_unit_table mode for bucketed video pricing (e.g.
// MiniMax: 768P×6s → 6 units, 768P×10s → 10 units, 1080P×6s → 12 units).
type BillingUnitTier struct {
	Resolution string `yaml:"resolution"`
	Duration   int64  `yaml:"duration"`
	Units      int64  `yaml:"units"`
}

// BillingConfig describes how to turn request/response observables into billable
// OUTPUT units for a model; the final fee is OutputUnits × OutputPrice. Input
// tokens are unaffected (they apply to per_token only). The unit math lives in
// OutputUnits and is pure/testable; observable extraction (request vs response,
// per-vendor response normalizers) is a separate layer.
type BillingConfig struct {
	Mode BillingMode `yaml:"mode"`

	// ResolutionMultipliers maps a resolution token (e.g. "1080P" or "1280x720")
	// to a cost multiplier, for per_image / per_video_second. A resolution not in
	// the map bills at the baseline 1.0. Empty map → every resolution is 1.0.
	ResolutionMultipliers map[string]float64 `yaml:"resolutionMultipliers"`

	// Table is the (resolution, duration) → units lookup for per_unit_table.
	Table []BillingUnitTier `yaml:"table"`
}

// BillingObservables are the resolved per-request inputs to the unit math.
// Seconds is the (effective) video duration; ImageCount the number of images;
// Resolution the resolution token used to pick a multiplier / table row.
type BillingObservables struct {
	Seconds    int64
	ImageCount int64
	Resolution string
}

// normalizeResolution canonicalizes a resolution token for case- and
// whitespace-insensitive matching, so an operator's "1080P" multiplier key
// matches an upstream/client-reported "1080p" (or " 1080p "). Without this a
// trivial casing mismatch silently falls through to the 1.0 baseline and
// UNDERBILLS. Orientation is deliberately NOT canonicalized: "1280x720"
// (landscape) and "720x1280" (portrait) are distinct resolutions that may
// legitimately carry different multipliers.
func normalizeResolution(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// resolutionMultiplier returns the configured cost multiplier for a resolution,
// or the baseline 1.0 when unset/unknown. Matching is case/whitespace
// insensitive (see normalizeResolution); validateBillingConfig rejects keys
// that collide under that normalization, so the first match is unambiguous.
func (b *BillingConfig) resolutionMultiplier(resolution string) float64 {
	res := normalizeResolution(resolution)
	for k, m := range b.ResolutionMultipliers {
		if normalizeResolution(k) == res {
			return m
		}
	}
	return 1.0
}

// OutputUnits computes the billable output-unit count for the resolved
// observables. per_video_second floors at 1 (a generated clip is always ≥1
// effective unit); per_image returns the raw scaled count (0 images → 0 units,
// so a failed/empty generation is not charged); per_unit_table looks up the
// exact (resolution, duration) row and errors when absent (fail rather than
// mis-bill). per_token is billed elsewhere and is not a valid input here.
func (b *BillingConfig) OutputUnits(obs BillingObservables) (int64, error) {
	switch b.Mode {
	case BillingModePerImage:
		units, err := scaledUnits(obs.ImageCount, b.resolutionMultiplier(obs.Resolution))
		if err != nil {
			return 0, err
		}
		if units < 0 {
			units = 0
		}
		return units, nil
	case BillingModePerVideoSecond:
		units, err := scaledUnits(obs.Seconds, b.resolutionMultiplier(obs.Resolution))
		if err != nil {
			return 0, err
		}
		if units < 1 {
			units = 1
		}
		return units, nil
	case BillingModePerUnitTable:
		obsRes := normalizeResolution(obs.Resolution)
		for _, t := range b.Table {
			if normalizeResolution(t.Resolution) == obsRes && t.Duration == obs.Seconds {
				return t.Units, nil
			}
		}
		return 0, fmt.Errorf("no per_unit_table billing row for resolution=%q duration=%d", obs.Resolution, obs.Seconds)
	default:
		return 0, fmt.Errorf("OutputUnits is not defined for billing mode %q (per_token is billed by token count)", b.Mode)
	}
}

// maxBillableUnits bounds the unit count any single request may produce. It is
// far above any real video length × resolution multiplier (e.g. 15s × 8 = 120),
// so it only ever trips on a garbage observable or multiplier — at which point
// failing is correct (the alternative is an int64 overflow that wraps into a
// nonsense, possibly negative, fee).
const maxBillableUnits = 1 << 40 // ~1.1e12

// scaledUnits computes ceil(count × multiplier) as an int64, failing closed on a
// non-finite product or one exceeding maxBillableUnits rather than overflowing
// the int64 conversion (which would wrap to a garbage value the post-clamps miss).
func scaledUnits(count int64, multiplier float64) (int64, error) {
	v := math.Ceil(float64(count) * multiplier)
	if math.IsNaN(v) || math.IsInf(v, 0) || v > float64(maxBillableUnits) {
		return 0, fmt.Errorf("billable units out of range (count=%d, multiplier=%v)", count, multiplier)
	}
	return int64(v), nil
}

// NextBucketUnits returns the units of the NEXT bucket up from an observed
// (resolution, duration): the row for that resolution with the smallest duration
// that is still at least the observed one. Reports whether such a row exists.
//
// Selection is by DURATION, not by price. Picking the cheapest covering row would
// assume the table is monotonic — that a longer bucket never costs less — and
// nothing enforces that. An operator who discounts long clips (2K@5s = 50 units,
// 2K@10s = 20) would have a 4-second clip billed at the 10-second row, below the
// bucket that actually neighbours it. "Round up to the next bucket" is the rule a
// bucketed price list states, and it holds whatever shape the table has.
//
// This is what a bucketed price list means: a duration between buckets rounds UP to
// the next one. It exists because the alternative on a miss is MaxTableUnits, the
// most expensive row in the entire table across every resolution — which turns a
// duration the operator simply did not tabulate into a charge for a 4K 15-second
// clip. That cliff is reachable by any untabulated duration, and a vendor whose
// minimum shifts (MiniMax H3's floor moved 5 -> 4, its default request shape) drops
// the MOST COMMON request straight into it.
//
// Still never below the table: a row that covers the observation is by definition
// priced for at least as much output, so the provider cannot be underbilled.
func (b *BillingConfig) NextBucketUnits(resolution string, seconds int64) (int64, bool) {
	res := normalizeResolution(resolution)
	var (
		best    int64
		bestDur int64
		found   bool
	)
	for _, t := range b.Table {
		if normalizeResolution(t.Resolution) != res || t.Duration < seconds {
			continue
		}
		switch {
		case !found, t.Duration < bestDur:
			best, bestDur, found = t.Units, t.Duration, true
		case t.Duration == bestDur && t.Units > best:
			// Duplicate duration rows are a misconfiguration; take the dearer one
			// rather than letting row order decide how much the provider is paid.
			best = t.Units
		}
	}
	return best, found
}

// MaxTableUnits returns the largest units value in a per_unit_table, or 0 when the
// table is empty. It is the last-resort fee basis: NextBucketUnits handles an
// observation that some bucket still covers, so this is reached only when the
// observation is LONGER than every bucket configured for its resolution, where no
// row covers it and undercharging below the table is the only alternative.
func (b *BillingConfig) MaxTableUnits() int64 {
	var max int64
	for _, t := range b.Table {
		if t.Units > max {
			max = t.Units
		}
	}
	return max
}

// validBillingModeForType reports whether a billing mode is allowed for a
// service type. per_token is valid everywhere (the default); the non-token modes
// are restricted to the modality whose output they price.
func validBillingModeForType(mode BillingMode, serviceType string) bool {
	switch mode {
	case "", BillingModePerToken:
		return true
	case BillingModePerImage:
		return serviceType == constant.ServiceTypeTextToImage || serviceType == constant.ServiceTypeImageEditing
	case BillingModePerVideoSecond, BillingModePerUnitTable:
		return serviceType == constant.ServiceTypeVideoGeneration
	default:
		return false
	}
}

// validateBillingConfig validates a per-model billing block against its service
// type. prefix labels errors (e.g. "service.modelPricing[0].billing").
func validateBillingConfig(prefix string, b *BillingConfig, serviceType string) error {
	switch b.Mode {
	case "", BillingModePerToken, BillingModePerImage, BillingModePerVideoSecond, BillingModePerUnitTable:
	default:
		return fmt.Errorf("invalid config: %s.mode %q is not a known billing mode", prefix, b.Mode)
	}
	if !validBillingModeForType(b.Mode, serviceType) {
		return fmt.Errorf("invalid config: %s.mode %q is not supported for service type %q", prefix, b.Mode, serviceType)
	}
	seenRes := make(map[string]string, len(b.ResolutionMultipliers))
	for res, mult := range b.ResolutionMultipliers {
		if mult <= 0 {
			return fmt.Errorf("invalid config: %s.resolutionMultipliers[%q] must be > 0, got %v", prefix, res, mult)
		}
		// Lookup is case/whitespace-insensitive; colliding keys would make the
		// matched multiplier depend on map iteration order, so reject at load.
		norm := normalizeResolution(res)
		if prev, dup := seenRes[norm]; dup {
			return fmt.Errorf("invalid config: %s.resolutionMultipliers has keys %q and %q that collide case/whitespace-insensitively", prefix, prev, res)
		}
		seenRes[norm] = res
	}
	if b.Mode == BillingModePerUnitTable {
		if len(b.Table) == 0 {
			return fmt.Errorf("invalid config: %s.table must not be empty for mode %q", prefix, BillingModePerUnitTable)
		}
		seen := make(map[string]struct{}, len(b.Table))
		for i, t := range b.Table {
			if t.Resolution == "" {
				return fmt.Errorf("invalid config: %s.table[%d].resolution is required", prefix, i)
			}
			if t.Duration <= 0 {
				return fmt.Errorf("invalid config: %s.table[%d].duration must be > 0, got %d", prefix, i, t.Duration)
			}
			if t.Units <= 0 {
				return fmt.Errorf("invalid config: %s.table[%d].units must be > 0, got %d", prefix, i, t.Units)
			}
			key := normalizeResolution(t.Resolution) + "\x00" + fmt.Sprint(t.Duration)
			if _, dup := seen[key]; dup {
				return fmt.Errorf("invalid config: %s.table has a duplicate (resolution=%q, duration=%d) row (resolution matched case/whitespace-insensitively)", prefix, t.Resolution, t.Duration)
			}
			seen[key] = struct{}{}
		}
	} else if len(b.Table) > 0 {
		return fmt.Errorf("invalid config: %s.table is only valid for mode %q", prefix, BillingModePerUnitTable)
	}
	return nil
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
	// Build the alias index after every model id is known, so an alias can be
	// checked against the full model-id set. An alias colliding with a model id
	// or another alias is rejected: resolution would otherwise depend on lookup
	// order and could bill/forward the wrong model.
	aliases := make(map[string]*ModelPricingEntry)
	for i := range s.ModelPricing {
		entry := &s.ModelPricing[i]
		for _, alias := range entry.ModelAliases {
			if alias == ModelWildcard {
				return fmt.Errorf("modelPricing alias %q is the reserved wildcard sentinel", alias)
			}
			if _, ok := m[alias]; ok {
				return fmt.Errorf("modelPricing alias %q collides with a model id", alias)
			}
			if _, ok := aliases[alias]; ok {
				return fmt.Errorf("duplicate modelPricing alias %q", alias)
			}
			aliases[alias] = entry
		}
	}
	s.modelPricingMap = m
	s.modelAliasMap = aliases
	return nil
}

// ResolveRequestedModel maps a client-supplied model id to its pricing entry.
// Resolution order: exact Model id, then a configured alias, then the wildcard
// ("*") catch-all. It returns the matched entry, the canonical model id to
// record for billing and metrics (entry.Model for an exact/alias match; the
// requested id for a wildcard match so wildcard attribution is preserved), and
// whether the model is allowed.
//
// When multi-model pricing is not configured the service serves a single model;
// the request id is allowed as-is and returned unchanged (entry nil).
func (s *Service) ResolveRequestedModel(requested string) (entry *ModelPricingEntry, resolved string, ok bool) {
	if !s.HasMultiModelPricing() {
		return nil, requested, true
	}
	// "*" is a pricing sentinel, never a selectable model — a literal request for
	// it would hit the wildcard entry and be forwarded verbatim upstream.
	if requested == ModelWildcard {
		return nil, requested, false
	}
	if e, hit := s.modelPricingMap[requested]; hit {
		return e, e.Model, true
	}
	if e, hit := s.modelAliasMap[requested]; hit {
		return e, e.Model, true
	}
	if e, hit := s.modelPricingMap[ModelWildcard]; hit {
		return e, requested, true
	}
	return nil, requested, false
}

// UpstreamModelFor returns the model id to forward upstream for an already
// resolved entry: the entry's UpstreamModel when set, otherwise its Model. The
// wildcard catch-all has no concrete id, so callers must forward the requested
// id verbatim for it (entry.Model == "*"); this returns "*" for that case and
// the caller is expected to skip the rewrite.
func (e *ModelPricingEntry) UpstreamModelFor() string {
	if e.UpstreamModel != "" {
		return e.UpstreamModel
	}
	return e.Model
}

// HasWildcardModel reports whether a catch-all ("*") pricing entry is configured.
func (s *Service) HasWildcardModel() bool {
	if s.modelPricingMap == nil {
		return false
	}
	_, ok := s.modelPricingMap[ModelWildcard]
	return ok
}

// ServedViaWildcard reports whether `model` is allowed ONLY because a wildcard
// ("*") catch-all entry is configured — i.e. it has no explicit per-model
// entry. Callers use it to surface wildcard-priced traffic for operator audit
// (the wildcard price is applied to a model the operator never enumerated).
func (s *Service) ServedViaWildcard(model string) bool {
	if s.modelPricingMap == nil {
		return false
	}
	if _, exact := s.modelPricingMap[model]; exact {
		return false
	}
	_, wild := s.modelPricingMap[ModelWildcard]
	return wild
}

// HasExactModelPricing reports whether model has its OWN enumerated pricing
// entry — the wildcard ("*") entry does NOT count. Metrics use this to keep
// label values bounded to operator-enumerated ids: on a serve-all (wildcard)
// deployment IsModelAllowed admits arbitrary user strings, which must never
// become Prometheus label values (unbounded series).
func (s *Service) HasExactModelPricing(model string) bool {
	if model == ModelWildcard {
		return false
	}
	_, ok := s.modelPricingMap[model]
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

// maxTierMultipliers returns the highest input/output multiplier fractions
// (each as numerator/denominator) across the effective tier set for this entry
// (the entry's own tiers, or serviceTiers when the entry has none). Returns
// (1/1, 1/1) when no tiers apply. Used to size the on-chain advertised ceiling
// so it covers the worst-case tiered price. Fractions are compared by
// cross-multiplication (all denominators are >= 1 via Effective*Multiplier).
func (e *ModelPricingEntry) maxTierMultipliers(serviceTiers []PricingTier) (inNum, inDen, outNum, outDen int64) {
	tiers := e.Tiers
	if len(tiers) == 0 {
		tiers = serviceTiers
	}
	inNum, inDen, outNum, outDen = 1, 1, 1, 1
	for _, t := range tiers {
		if n, d := t.EffectiveInputMultiplier(); n*inDen > inNum*d { // n/d > inNum/inDen
			inNum, inDen = n, d
		}
		if n, d := t.EffectiveOutputMultiplier(); n*outDen > outNum*d {
			outNum, outDen = n, d
		}
	}
	return inNum, inDen, outNum, outDen
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
		inNum, inDen, outNum, outDen := entry.maxTierMultipliers(serviceTiers)
		if v, ok := new(big.Int).SetString(entry.InputPrice, 10); ok {
			v.Mul(v, big.NewInt(inNum))
			v.Div(v, big.NewInt(inDen))
			if v.Cmp(maxIn) > 0 {
				maxIn = v
			}
		}
		if v, ok := new(big.Int).SetString(entry.OutputPrice, 10); ok {
			v.Mul(v, big.NewInt(outNum))
			v.Div(v, big.NewInt(outDen))
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
		inNum, inDen, outNum, outDen := entry.maxTierMultipliers(serviceTiers)
		if r, ok := new(big.Rat).SetString(entry.InputPriceUSDPerMillionTokens); ok {
			r.Mul(r, big.NewRat(inNum, inDen))
			if r.Cmp(maxIn) > 0 {
				maxIn = r
			}
		}
		if r, ok := new(big.Rat).SetString(entry.OutputPriceUSDPerMillionTokens); ok {
			r.Mul(r, big.NewRat(outNum, outDen))
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

// validateModelPricing validates service.modelPricing, builds the derived lookup
// map, and auto-sets the on-chain ceiling (max over models, in the service
// denomination). No-op when modelPricing is empty. Extracted from loadConfig and
// decomposed per modality so the per-entry rules stay flat and single-purpose.
func validateModelPricing(cfg *Config) error {
	if len(cfg.Service.ModelPricing) == 0 {
		return nil
	}
	svc := &cfg.Service
	if !svc.IsForwarder() {
		return fmt.Errorf("invalid config: service.modelPricing is only supported when providerType is 'centralized' or 'standard'")
	}
	// Per-model billing is wired only for the modalities whose request path
	// resolves the request model before billing: chatbot + speech-to-text (token
	// billing) and video-generation (per-effective-second billing). On other
	// modalities the allowlist would never run and every request would silently
	// fall back to the on-chain max price, so reject at load time.
	switch svc.Type {
	case constant.ServiceTypeChatbot, constant.ServiceTypeSpeechToText, constant.ServiceTypeVideoGeneration:
	default:
		return fmt.Errorf("invalid config: service.modelPricing is only supported for service type '%s', '%s', or '%s', got '%s'", constant.ServiceTypeChatbot, constant.ServiceTypeSpeechToText, constant.ServiceTypeVideoGeneration, svc.Type)
	}
	// service.model is the default billed (and forwarded-upstream) model for
	// requests that omit the model field; it must be set.
	if svc.ModelType == "" {
		return fmt.Errorf("invalid config: service.model is required when service.modelPricing is configured")
	}
	// The multi-model request path resolves and rewrites per ENTRY, never via the
	// single-model service-level knobs, so a config that sets either alongside
	// modelPricing would have it silently ignored — reject at load time and point
	// the operator at the per-entry fields instead.
	if len(svc.ModelAliases) > 0 {
		return fmt.Errorf("invalid config: service.modelAliases is not supported with service.modelPricing (set modelAliases on the individual service.modelPricing entry instead)")
	}
	if svc.UpstreamModel != "" {
		return fmt.Errorf("invalid config: service.upstreamModel is not supported with service.modelPricing (set upstreamModel on the individual service.modelPricing entry instead)")
	}

	isUSD := svc.IsUSDDenominated()
	hasWildcard := false
	for i := range svc.ModelPricing {
		entry := &svc.ModelPricing[i]
		if err := validateModelPricingEntry(i, entry, svc.Type, isUSD); err != nil {
			return err
		}
		if entry.Model == ModelWildcard {
			hasWildcard = true
		}
	}

	// A wildcard entry silently turns the allowlist into serve-all: every model
	// the upstream can answer is reachable and billed at the wildcard price.
	// Warn loudly at load so an operator who added "*" expecting a default price
	// for their listed models realizes they opened the service to all models.
	// (stdlib log: the structured logger isn't initialized at config-load time.)
	if hasWildcard {
		log.Printf("[CONFIG] service.modelPricing has a wildcard %q entry: the model allowlist is DISABLED — every model this upstream can serve is reachable and billed at the wildcard entry's price. Ensure that price covers the most expensive model you expose.", ModelWildcard)
	}

	// Per-model additionalSecret is all-or-nothing per entry (see
	// EffectiveAdditionalSecret): an entry with none falls back to the service-level
	// key. That fallback is correct for a shared-key catalog but indistinguishable
	// from "operator forgot this model's key" on an upstream that needs a distinct
	// key per model (e.g. dgrid). Warn loudly at load — once, not per request — for
	// every concrete entry missing its own secret when some sibling defines one, so
	// a silent per-request auth-to-wrong-account mismatch becomes an operator-visible
	// config line. A wildcard entry is exempt: it is a catch-all whose secret (or the
	// service-level map) intentionally covers every unenumerated model.
	anyEntryHasSecret := false
	for i := range svc.ModelPricing {
		if len(svc.ModelPricing[i].AdditionalSecret) > 0 && svc.ModelPricing[i].Model != ModelWildcard {
			anyEntryHasSecret = true
			break
		}
	}
	if anyEntryHasSecret {
		for i := range svc.ModelPricing {
			entry := &svc.ModelPricing[i]
			if entry.Model == ModelWildcard || len(entry.AdditionalSecret) > 0 {
				continue
			}
			log.Printf("[CONFIG] service.modelPricing model %q has no additionalSecret while other models do; it will use the service-level additionalSecret (or none) upstream — confirm this is intended, not a missing per-model key.", entry.Model)
		}
	}

	// Build the derived lookup map (single source of truth; also detects duplicate
	// model ids). Per-entry validation above establishes the precondition the
	// MaxModelPrices* helpers rely on.
	if err := svc.BuildModelPricingMap(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	// A per-entry upstreamModel must not be ambiguous with the public allowlist.
	// If it equals ANOTHER entry's public Model id (or a configured alias), a
	// request for that entry would be forwarded under a different entry's public
	// name — confusing mis-routing the operator never intended. (Equaling its OWN
	// Model id is a harmless no-op.) Two entries deliberately sharing one
	// upstreamModel is allowed but warned: it collapses two priced public ids onto
	// a single upstream model, so the client picks the price purely by public id.
	upstreamOwners := make(map[string]string, len(svc.ModelPricing))
	for i := range svc.ModelPricing {
		entry := &svc.ModelPricing[i]
		up := entry.UpstreamModel
		if up == "" {
			continue
		}
		if other, ok := svc.modelPricingMap[up]; ok && other != entry {
			return fmt.Errorf("invalid config: service.modelPricing[%d].upstreamModel %q collides with the public model id of another entry; a request would be forwarded under the wrong public id", i, up)
		}
		if _, ok := svc.modelAliasMap[up]; ok {
			return fmt.Errorf("invalid config: service.modelPricing[%d].upstreamModel %q collides with a configured model alias", i, up)
		}
		if prev, dup := upstreamOwners[up]; dup {
			log.Printf("[CONFIG] service.modelPricing entries %q and %q share upstreamModel %q: two priced public ids map to one upstream model — ensure this is intentional (clients select the price by public id).", prev, entry.Model, up)
		} else {
			upstreamOwners[up] = entry.Model
		}
	}
	// service.model is forwarded upstream verbatim for model-less requests, so it
	// must be a concrete id — never the "*" pricing sentinel.
	if svc.ModelType == ModelWildcard {
		return fmt.Errorf("invalid config: service.model must be a concrete model id, not the '%s' wildcard sentinel (the wildcard is a catch-all pricing entry only)", ModelWildcard)
	}
	// Unless a wildcard entry catches all models, the default service.model must
	// itself be a priced/allowlisted model, or model-less requests get rejected.
	if !hasWildcard && svc.GetModelPricing(svc.ModelType) == nil {
		return fmt.Errorf("invalid config: service.model '%s' must be one of the service.modelPricing entries (or add a '%s' wildcard entry)", svc.ModelType, ModelWildcard)
	}
	// Auto-set the service-level on-chain ceiling to the tier-adjusted max over all
	// models. The existing single-price machinery (native registration / USD price
	// feed) then advertises a ceiling covering every served model at worst-case tier.
	if isUSD {
		svc.InputPriceUSDPerMillionTokens, svc.OutputPriceUSDPerMillionTokens =
			svc.MaxModelUSDPrices(cfg.TieredPricing.Tiers)
	} else {
		svc.InputPrice, svc.OutputPrice =
			svc.MaxModelPricesNative(cfg.TieredPricing.Tiers)
	}
	return nil
}

// validateModelPricingEntry validates one modelPricing entry: identity + the
// per-modality price rules, then the modality-agnostic tail (canonical id,
// billing block, modelInfo).
func validateModelPricingEntry(i int, entry *ModelPricingEntry, serviceType string, isUSD bool) error {
	if entry.Model == "" {
		return fmt.Errorf("invalid config: service.modelPricing[%d].model is required", i)
	}
	// Per-model modality override is reserved for future single-process
	// multi-modal serving; for now it must match the service modality.
	if entry.Type != "" && entry.Type != serviceType {
		return fmt.Errorf("invalid config: service.modelPricing[%d].type %q must equal service.type %q (per-model modality is not yet supported)", i, entry.Type, serviceType)
	}

	if serviceType == constant.ServiceTypeVideoGeneration {
		if err := validateVideoModelEntry(i, entry, isUSD); err != nil {
			return err
		}
	} else {
		if err := validateTokenModelEntry(i, entry, serviceType, isUSD); err != nil {
			return err
		}
	}

	// Per-entry upstream rewrite / aliasing is wired only on the chatbot JSON
	// request path (ValidateModelAllowlist rewrites the body before forwarding).
	// On other modalities the body is not rewritten, so an upstreamModel/alias
	// would silently forward the wrong id — reject at load rather than mis-route.
	if serviceType != constant.ServiceTypeChatbot {
		if entry.UpstreamModel != "" {
			return fmt.Errorf("invalid config: service.modelPricing[%d].upstreamModel is only supported for service type '%s' (the body rewrite runs only on the JSON chatbot path), got '%s' for model '%s'", i, constant.ServiceTypeChatbot, serviceType, entry.Model)
		}
		if len(entry.ModelAliases) > 0 {
			return fmt.Errorf("invalid config: service.modelPricing[%d].modelAliases is only supported for service type '%s', got '%s' for model '%s'", i, constant.ServiceTypeChatbot, serviceType, entry.Model)
		}
	}
	// Per-entry injectBodyFields is applied only on the chatbot forward path
	// (same as the service-level field), so reject it on other modalities, reject
	// broker-critical keys, and normalize it (yaml.v2 nested maps → string-keyed,
	// verified JSON-serializable) so a bad shape fails loud at load instead of on
	// every request.
	if len(entry.InjectBodyFields) > 0 {
		if serviceType != constant.ServiceTypeChatbot {
			return fmt.Errorf("invalid config: service.modelPricing[%d].injectBodyFields is only supported for service type '%s', got '%s' for model '%s'", i, constant.ServiceTypeChatbot, serviceType, entry.Model)
		}
		normalized, err := normalizeInjectBodyFields(fmt.Sprintf("service.modelPricing[%d].injectBodyFields", i), entry.InjectBodyFields)
		if err != nil {
			return err
		}
		entry.InjectBodyFields = normalized
	}
	// Per-entry stripBodyFields, like injectBodyFields, only applies on the chatbot
	// forward path; reject it elsewhere, reject broker-critical keys, and normalize
	// (trim/dedup) so a bad list fails loud at load.
	if len(entry.StripBodyFields) > 0 {
		if serviceType != constant.ServiceTypeChatbot {
			return fmt.Errorf("invalid config: service.modelPricing[%d].stripBodyFields is only supported for service type '%s', got '%s' for model '%s'", i, constant.ServiceTypeChatbot, serviceType, entry.Model)
		}
		normalized, err := normalizeStripBodyFields(fmt.Sprintf("service.modelPricing[%d].stripBodyFields", i), entry.StripBodyFields)
		if err != nil {
			return err
		}
		entry.StripBodyFields = normalized
	}
	// The wildcard catch-all has no concrete id to rewrite to — ValidateModelAllowlist
	// forwards a wildcard-served model verbatim — so an upstreamModel on the "*"
	// entry would be silently dropped at runtime. Reject it rather than mislead.
	if entry.Model == ModelWildcard && entry.UpstreamModel != "" {
		return fmt.Errorf("invalid config: service.modelPricing[%d].upstreamModel is not supported on the wildcard ('%s') entry (the catch-all forwards the requested id verbatim)", i, ModelWildcard)
	}
	if entry.UpstreamModel != "" {
		// The forwarded id must be a concrete, unpadded model id: "*" would forward
		// the literal sentinel upstream, and surrounding whitespace would forward an
		// id no upstream recognizes.
		if entry.UpstreamModel == ModelWildcard {
			return fmt.Errorf("invalid config: service.modelPricing[%d].upstreamModel must be a concrete model id, not the '%s' wildcard sentinel (model '%s')", i, ModelWildcard, entry.Model)
		}
		if strings.TrimSpace(entry.UpstreamModel) != entry.UpstreamModel {
			return fmt.Errorf("invalid config: service.modelPricing[%d].upstreamModel %q must not have leading/trailing whitespace (model '%s')", i, entry.UpstreamModel, entry.Model)
		}
	}
	for _, alias := range entry.ModelAliases {
		if strings.TrimSpace(alias) == "" {
			return fmt.Errorf("invalid config: service.modelPricing[%d].modelAliases must not contain an empty id (model '%s')", i, entry.Model)
		}
		// Aliases are indexed and matched verbatim, so a padded alias (" glm-5 ")
		// would pass this check yet never match a client's "glm-5" — reject the
		// silent-no-match trap at load.
		if strings.TrimSpace(alias) != alias {
			return fmt.Errorf("invalid config: service.modelPricing[%d].modelAliases entry %q must not have leading/trailing whitespace (model '%s')", i, alias, entry.Model)
		}
	}
	if entry.CanonicalID != "" && !validCanonicalID.MatchString(entry.CanonicalID) {
		return fmt.Errorf("invalid config: service.modelPricing[%d].canonicalId %q must be bare lowercase (letters, digits, '-', '.') for model '%s'", i, entry.CanonicalID, entry.Model)
	}
	if entry.Billing != nil {
		if err := validateBillingConfig(fmt.Sprintf("service.modelPricing[%d].billing", i), entry.Billing, serviceType); err != nil {
			return err
		}
	}
	// Optional per-model metadata; if present it must be COMPLETE so /v1/models
	// never advertises a half-described model.
	if entry.ModelInfo != nil {
		if err := entry.ModelInfo.Validate(serviceType); err != nil {
			return fmt.Errorf("invalid config: service.modelPricing[%d].modelInfo (model '%s'): %w", i, entry.Model, err)
		}
	}
	// Optional per-model cache-billing override; same divisor and write-multiplier
	// rules as the service-level block (a 0 divisor/denominator would divide-by-zero
	// panic at billing).
	if entry.CacheTokenBilling != nil {
		if err := validateCacheTokenBilling(fmt.Sprintf("service.modelPricing[%d].cacheTokenBilling", i), entry.CacheTokenBilling); err != nil {
			return err
		}
	}
	return nil
}

// validateVideoModelEntry validates a video-generation entry. Billing is
// OutputUnits × per-effective-second price (NATIVE outputPrice neuron, or USD
// outputPriceUSDPerSecond); input tokens don't apply. A USD entry's per-second
// price is normalized into the per-1M-unit USD representation the shared pipeline
// consumes (see ModelPricingEntry.OutputPriceUSDPerSecond).
func validateVideoModelEntry(i int, entry *ModelPricingEntry, isUSD bool) error {
	if entry.InputPriceUSDPerMillionTokens != "" || entry.OutputPriceUSDPerMillionTokens != "" {
		return fmt.Errorf("invalid config: service.modelPricing[%d]: video-generation uses outputPrice (NATIVE) or outputPriceUSDPerSecond (USD), not the per-1M-tokens USD fields (model '%s')", i, entry.Model)
	}
	if isUSD {
		if entry.OutputPrice != "" || entry.InputPrice != "" {
			return fmt.Errorf("invalid config: service.modelPricing[%d] must use outputPriceUSDPerSecond (priceDenomination is '%s') for video model '%s'", i, constant.PriceDenominationUSD, entry.Model)
		}
		if entry.OutputPriceUSDPerSecond == "" {
			return fmt.Errorf("invalid config: service.modelPricing[%d].outputPriceUSDPerSecond is required for USD video model '%s'", i, entry.Model)
		}
		// weiPerUnit = (usdPerSec*1e6)/1e6/rate*1e18 = usdPerSec/rate*1e18: the "unit"
		// is the effective output second; input side is 0.
		normalized, err := normalizeUSDPerUnitPrice(fmt.Sprintf("service.modelPricing[%d].outputPriceUSDPerSecond", i), entry.OutputPriceUSDPerSecond)
		if err != nil {
			// %w trails (not leads) so the wrapped error's own "invalid config: ..."
			// prefix stays first, matching every other error in this file/package;
			// "for model '%s'" matches the sibling required-field check just above.
			return fmt.Errorf("%w for model '%s'", err, entry.Model)
		}
		entry.OutputPriceUSDPerMillionTokens = normalized
		entry.InputPriceUSDPerMillionTokens = "0"
	} else {
		if entry.OutputPriceUSDPerSecond != "" {
			return fmt.Errorf("invalid config: service.modelPricing[%d].outputPriceUSDPerSecond is only valid under USD denomination (model '%s')", i, entry.Model)
		}
		if entry.OutputPrice == "" {
			return fmt.Errorf("invalid config: service.modelPricing[%d].outputPrice (per effective second) is required for video model '%s'", i, entry.Model)
		}
		if _, ok := new(big.Int).SetString(entry.OutputPrice, 10); !ok {
			return fmt.Errorf("invalid config: service.modelPricing[%d].outputPrice must be a valid integer for video model '%s'", i, entry.Model)
		}
		if entry.InputPrice != "" {
			if _, ok := new(big.Int).SetString(entry.InputPrice, 10); !ok {
				return fmt.Errorf("invalid config: service.modelPricing[%d].inputPrice must be a valid integer for video model '%s'", i, entry.Model)
			}
		}
	}
	if entry.Billing == nil || (entry.Billing.Mode != BillingModePerVideoSecond && entry.Billing.Mode != BillingModePerUnitTable) {
		return fmt.Errorf("invalid config: service.modelPricing[%d].billing.mode must be '%s' or '%s' for video model '%s'", i, BillingModePerVideoSecond, BillingModePerUnitTable, entry.Model)
	}
	// Video uses billing.resolutionMultipliers, not token-length tiers.
	if len(entry.Tiers) > 0 {
		return fmt.Errorf("invalid config: service.modelPricing[%d].tiers is not supported for video-generation (use billing.resolutionMultipliers) for model '%s'", i, entry.Model)
	}
	return nil
}

// validateTokenModelEntry validates a chatbot / speech-to-text entry, whose price
// is per token in the service denomination (NATIVE neuron or USD-per-1M-tokens).
func validateTokenModelEntry(i int, entry *ModelPricingEntry, serviceType string, isUSD bool) error {
	if entry.OutputPriceUSDPerSecond != "" {
		return fmt.Errorf("invalid config: service.modelPricing[%d].outputPriceUSDPerSecond is only valid for video-generation (model '%s')", i, entry.Model)
	}
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
	// Per-model tiers: same ordering/multiplier rules as service-level tiers.
	if err := validatePricingTiers(fmt.Sprintf("service.modelPricing[%d].tiers", i), entry.Tiers); err != nil {
		return err
	}
	// Speech-to-text bills flat, so per-model tiers would be advertised but never
	// enforced — reject rather than silently diverge.
	if len(entry.Tiers) > 0 && serviceType == constant.ServiceTypeSpeechToText {
		return fmt.Errorf("invalid config: service.modelPricing[%d].tiers is not supported for service type '%s' (tiers are not applied to its billing)", i, constant.ServiceTypeSpeechToText)
	}
	return nil
}
