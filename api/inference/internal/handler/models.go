package handler

import (
	"encoding/json"
	"math/big"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/inference/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/internal/ctrl"
	"github.com/0glabs/0g-serving-broker/inference/internal/pricefeed"
)

// ModelObject represents a single model in the OpenAI-compatible /v1/models response.
type ModelObject struct {
	ID                  string                    `json:"id"`
	CanonicalID         string                    `json:"canonical_id,omitempty"`
	Object              string                    `json:"object"`
	Created             int64                     `json:"created"`
	OwnedBy             string                    `json:"owned_by"`
	Name                string                    `json:"name,omitempty"`
	Description         string                    `json:"description,omitempty"`
	Type                string                    `json:"type"`
	ContextLength       int                       `json:"context_length,omitempty"`
	MaxCompletionTokens int                       `json:"max_completion_tokens,omitempty"`
	Architecture        *config.ModelArchitecture `json:"architecture,omitempty"`
	SupportedParameters []string                  `json:"supported_parameters,omitempty"`
	SupportedFormats    []string                  `json:"supported_formats,omitempty"`
	DefaultParameters   map[string]interface{}    `json:"default_parameters,omitempty"`
	Pricing             *ModelPricing             `json:"pricing,omitempty"`
	PricingUSD          *ModelPricingUSD          `json:"pricing_usd,omitempty"`
	Verifiability       string                    `json:"verifiability,omitempty"`
	TeeAttested         bool                      `json:"tee_attested"`
	TeeType             string                    `json:"tee_type,omitempty"`
	TeeVerifier         string                    `json:"tee_verifier,omitempty"`
	ExpirationDate      string                    `json:"expiration_date,omitempty"`
	ProviderType        string                    `json:"provider_type,omitempty"`
	ProviderIdentity    string                    `json:"provider_identity,omitempty"`
	// ProviderName is an optional human-readable display name (e.g. "OpenAI",
	// "Aliyun (CN)"). Freeform presentation companion to ProviderIdentity (the
	// lowercase machine key); use this for UI labels, not for lookups.
	ProviderName string `json:"provider_name,omitempty"`
	// ProviderCountry is the provider's ISO 3166-1 alpha-2 jurisdiction code
	// (e.g. "US", "CN"). Display/discovery hint only — self-asserted by the broker
	// and not verifiable.
	ProviderCountry string `json:"provider_country,omitempty"`
	// ServingDomain is the upstream hostname (FQDN, e.g. "api.openai.com") that
	// the broker actually connects to for a centralized provider. It is the host
	// component of service.targetUrl — scheme, port, and path stripped — so it
	// matches the TLS SNI / certificate SAN seen on the upstream connection.
	//
	// This is a discovery/display hint surfaced from unsigned broker config; it is
	// NOT a verifiable claim on its own. Verification of where a request was routed
	// relies on the TEE-signed routing proof, not this field. Empty for
	// decentralized providers. Consumers prepend "https://" if they need a URL.
	ServingDomain string           `json:"serving_domain,omitempty"`
	RateLimits    *ModelRateLimits `json:"rate_limits,omitempty"`
}

// ModelRateLimits exposes per-user rate limit configuration so clients/SDKs
// can perform client-side throttling before hitting server-side 429s.
type ModelRateLimits struct {
	RequestsPerMinute int `json:"requests_per_minute,omitempty"`
	TokensPerMinute   int `json:"tokens_per_minute,omitempty"`
	ImagesPerMinute   int `json:"images_per_minute,omitempty"`
}

// ModelPricingTier represents a single tier in tiered pricing. The multipliers
// are fractions: the effective input multiplier is InputMultiplier /
// InputMultiplierDenominator. The denominator fields are omitted when 1 (the
// integer-multiple case) so output for legacy integer-only tiers is unchanged;
// a consumer that ignores them must treat a missing denominator as 1.
type ModelPricingTier struct {
	MaxInputTokens              int   `json:"max_input_tokens"`
	InputMultiplier             int64 `json:"input_multiplier"`
	OutputMultiplier            int64 `json:"output_multiplier"`
	InputMultiplierDenominator  int64 `json:"input_multiplier_denominator,omitempty"`
	OutputMultiplierDenominator int64 `json:"output_multiplier_denominator,omitempty"`
}

// toModelPricingTier maps a config tier to its /v1/models representation,
// carrying the multiplier denominators only when they are not 1 so legacy
// integer-only tiers render exactly as before.
func toModelPricingTier(t config.PricingTier) ModelPricingTier {
	inNum, inDen := t.EffectiveInputMultiplier()
	outNum, outDen := t.EffectiveOutputMultiplier()
	mt := ModelPricingTier{
		MaxInputTokens:   t.MaxInputTokens,
		InputMultiplier:  inNum,
		OutputMultiplier: outNum,
	}
	if inDen != 1 {
		mt.InputMultiplierDenominator = inDen
	}
	if outDen != 1 {
		mt.OutputMultiplierDenominator = outDen
	}
	return mt
}

// ModelCacheTokenBilling holds cache token billing info for display.
// The default (5-minute) and 1-hour write-multiplier fractions are each omitted
// when not configured (those cache-write tokens then bill at the default tier, or
// at full input price when no premium applies).
type ModelCacheTokenBilling struct {
	Divisor                      int64 `json:"divisor"`
	WriteMultiplierNumerator     int64 `json:"write_multiplier_numerator,omitempty"`
	WriteMultiplierDenominator   int64 `json:"write_multiplier_denominator,omitempty"`
	Write1hMultiplierNumerator   int64 `json:"write_1h_multiplier_numerator,omitempty"`
	Write1hMultiplierDenominator int64 `json:"write_1h_multiplier_denominator,omitempty"`
}

// newModelCacheTokenBilling builds the display struct from resolved cache-billing
// config, returning nil when caching is disabled or unset (so the field is omitted
// from GET /v1/models). It surfaces the EFFECTIVE fractions billing actually
// applies: the default (5-minute) fraction when configured, and — because an unset
// 1-hour tier falls back to the default multiplier at billing time (see
// computeInputFee) — the 1-hour fraction resolves to the explicit 1-hour value if
// set, else the default. Both 1-hour fields are omitted only when no default tier
// is set either (then 1-hour writes bill at full input price). This keeps the
// advertised cache-write prices equal to what is charged, so consumers never have
// to re-derive the fallback rule.
func newModelCacheTokenBilling(cfg config.CacheTokenBillingConfig) *ModelCacheTokenBilling {
	if !cfg.Enabled || cfg.Divisor <= 0 {
		return nil
	}
	out := &ModelCacheTokenBilling{Divisor: cfg.Divisor}
	if cfg.WriteMultiplierEnabled() {
		out.WriteMultiplierNumerator = cfg.WriteMultiplierNumerator
		out.WriteMultiplierDenominator = cfg.WriteMultiplierDenominator
	}
	switch {
	case cfg.Write1hMultiplierEnabled():
		out.Write1hMultiplierNumerator = cfg.Write1hMultiplierNumerator
		out.Write1hMultiplierDenominator = cfg.Write1hMultiplierDenominator
	case cfg.WriteMultiplierEnabled():
		// 1-hour falls back to the default multiplier.
		out.Write1hMultiplierNumerator = cfg.WriteMultiplierNumerator
		out.Write1hMultiplierDenominator = cfg.WriteMultiplierDenominator
	}
	return out
}

// ModelPricing holds per-token pricing in the smallest unit (wei).
type ModelPricing struct {
	Prompt            string                  `json:"prompt"`
	Completion        string                  `json:"completion"`
	Image             string                  `json:"image,omitempty"`
	Video             string                  `json:"video,omitempty"`
	TieredPricing     []ModelPricingTier      `json:"tiered_pricing,omitempty"`
	CacheTokenBilling *ModelCacheTokenBilling `json:"cache_token_billing,omitempty"`
	// Variants lists NATIVE (wei) per-resolution/per-bucket prices for a
	// non-flat billing shape (video-generation today). Omitted when the model
	// has no per-model billing config with resolution/table variance — see
	// ModelPriceVariant.
	Variants []ModelPriceVariant `json:"variants,omitempty"`
}

// ModelPricingUSD holds per-token USD pricing as trimmed decimal strings.
// Present only when the provider is USD-denominated; omitted in NATIVE mode.
// Values are derived from the configured USD-per-1M-tokens by exact big.Rat
// division — no float precision loss.
type ModelPricingUSD struct {
	Prompt     string `json:"prompt"`
	Completion string `json:"completion"`
	// Image is the USD price per generated image for a USD-denominated
	// image-generation / image-editing model (decimal string), derived from
	// OutputPriceUSDPerMillionTokens with the same ÷1e6 the wei conversion uses,
	// so it stays consistent with the native pricing.image value. Set alongside
	// Prompt/Completion (both reported as "0", not omitted — an image bills per
	// image, not per token, so there's no per-token rate to report).
	Image string `json:"image,omitempty"`
	// Video is the USD price per effective output second for a USD-denominated
	// video-generation model (decimal string). Set alongside Prompt/Completion
	// (both reported as "0", not omitted), same convention as Image above.
	Video string `json:"video,omitempty"`
	// Variants is the USD counterpart of ModelPricing.Variants — same rows,
	// UnitPrice expressed as a USD decimal string instead of wei.
	Variants []ModelPriceVariant `json:"variants,omitempty"`
}

// unitVideoSecond and unitVideoClip are the two currently-supported values of
// ModelPriceVariant.Unit. They are the GET /v1/models projection of
// config.BillingModePerVideoSecond / config.BillingModePerUnitTable and must
// stay in sync with that enum. This is a closed vocabulary — new values
// should be added deliberately (and documented, since the router repo's
// catalog ingestion depends on recognizing them) rather than left as free
// text, because Unit tells the consumer how to USE UnitPrice (multiply by a
// requested quantity vs. treat as an already-final total), which is
// computation, not just display.
const (
	unitVideoSecond = "video_second"
	unitVideoClip   = "video_clip"
)

// ModelPriceVariant is one priced (dimension...) combination for a model
// whose billing varies by request shape (resolution, duration, ...) rather
// than a single flat rate. Modality-agnostic by design: Dimensions is an open
// map so video ("resolution", "duration_seconds") and, later, image/audio can
// each name their own axes without a schema change; Unit is a closed,
// registered vocabulary (see unitVideoSecond/unitVideoClip) telling the
// consumer how UnitPrice must be used; UnitPrice is the final, already-
// computed price for one Unit at these Dimensions — never a raw multiplier —
// so a consumer never needs to reimplement BillingConfig.OutputUnits'
// rounding/lookup rules to get an accurate number.
type ModelPriceVariant struct {
	Dimensions map[string]string `json:"dimensions"`
	Unit       string            `json:"unit"`
	UnitPrice  string            `json:"unit_price"`
}

// PriceFeedState surfaces the live 0G/USD rate and its freshness.  Present
// only when the provider is USD-denominated and the price cache has been
// populated at least once.
//
// NextUpdateTime is a hint — it's UpdatedAt plus the configured update
// interval, so SDK clients can schedule a refresh around that moment rather
// than polling on a fixed cadence.  The real tick can slip behind retries,
// so treat this as "not before", not a guarantee.
type PriceFeedState struct {
	RateUSDPerOG   string    `json:"rate_usd_per_og"`
	UpdatedAt      time.Time `json:"updated_at"`
	NextUpdateTime time.Time `json:"next_update_time"`
	IsStale        bool      `json:"is_stale"`
}

// ModelListResponse is the OpenAI-compatible response for GET /v1/models.
type ModelListResponse struct {
	Object    string          `json:"object"`
	Data      []ModelObject   `json:"data"`
	PriceFeed *PriceFeedState `json:"price_feed,omitempty"`
}

// derivePerUnitUSD derives a per-unit USD display value (per-image or
// per-effective-output-second) from the normalized per-1M-unit representation
// the shared USD pipeline uses, via the same ÷1e6 the wei conversion applies —
// shared by the image, single-model video, and multi-model video pricing_usd
// cases so the "derive, warn-and-omit on error" logic lives in one place.
// Config validation already accepted the underlying USD string, so a
// conversion failure here signals a regression, not a config error: logs a
// warning and returns ok=false rather than failing the whole /v1/models
// response. model, when non-empty, is included in the log line to identify
// which entry failed in a multi-model service.
func (h *Handler) derivePerUnitUSD(unitLabel, millionTokens, model string) (string, bool) {
	value, err := pricefeed.USDPerMillionStringToPerToken(millionTokens)
	if err != nil {
		if model != "" {
			h.logger.Warnf("GetModels: derive per-%s USD price from %q failed (omitting PricingUSD block) for model %q: %v",
				unitLabel, millionTokens, model, err)
		} else {
			h.logger.Warnf("GetModels: derive per-%s USD price from %q failed (omitting PricingUSD block): %v",
				unitLabel, millionTokens, err)
		}
		return "", false
	}
	return value, true
}

// sortVariantsByResolution orders variants by their "resolution" dimension so
// the JSON response is deterministic across requests — Go map iteration
// (BillingConfig.ResolutionMultipliers) is randomized, and per_unit_table
// rows are already in configured (slice) order so they're left as-is by
// callers that don't need this.
func sortVariantsByResolution(variants []ModelPriceVariant) {
	sort.Slice(variants, func(i, j int) bool {
		return variants[i].Dimensions["resolution"] < variants[j].Dimensions["resolution"]
	})
}

// videoPriceVariantsNative builds the NATIVE (wei) `variants` rows for a
// video-generation model's per-model billing config, or nil when none is
// configured (defensive — config validation requires Billing on every
// multi-model video entry, see validateVideoModelEntry, but this must not
// panic if that invariant is ever violated).
//
// per_video_second yields one row per configured resolution multiplier:
// UnitPrice = floor(multiplier * basePriceWei), a per-effective-second RATE.
// Floor matches the "never overstate what will be charged" direction already
// used by pricefeed.USDPerMillionToWeiPerToken's quantization. This is a
// display rate, not a request quote — the request-time billing path applies
// ceil() once over the WHOLE requested duration (see
// BillingConfig.OutputUnits), which is a different, per-request computation.
//
// per_unit_table yields one row per configured (resolution, duration)
// bucket: UnitPrice = units * basePriceWei exactly — units is already an
// integer, so no rounding is involved.
func videoPriceVariantsNative(billing *config.BillingConfig, basePriceWei string) []ModelPriceVariant {
	if billing == nil || basePriceWei == "" {
		return nil
	}
	base, ok := new(big.Int).SetString(basePriceWei, 10)
	if !ok {
		return nil
	}
	switch billing.Mode {
	case config.BillingModePerVideoSecond:
		if len(billing.ResolutionMultipliers) == 0 {
			return nil
		}
		variants := make([]ModelPriceVariant, 0, len(billing.ResolutionMultipliers))
		for res, mult := range billing.ResolutionMultipliers {
			rat := new(big.Rat).SetFloat64(mult)
			if rat == nil {
				// mult is NaN/Inf — config validation already rejects mult <= 0,
				// so this should be unreachable; skip rather than panic.
				continue
			}
			rat.Mul(rat, new(big.Rat).SetInt(base))
			price := new(big.Int).Quo(rat.Num(), rat.Denom())
			variants = append(variants, ModelPriceVariant{
				Dimensions: map[string]string{"resolution": res},
				Unit:       unitVideoSecond,
				UnitPrice:  price.String(),
			})
		}
		sortVariantsByResolution(variants)
		return variants
	case config.BillingModePerUnitTable:
		if len(billing.Table) == 0 {
			return nil
		}
		variants := make([]ModelPriceVariant, 0, len(billing.Table))
		for _, t := range billing.Table {
			price := new(big.Int).Mul(base, big.NewInt(t.Units))
			variants = append(variants, ModelPriceVariant{
				Dimensions: map[string]string{
					"resolution":       t.Resolution,
					"duration_seconds": strconv.FormatInt(t.Duration, 10),
				},
				Unit:      unitVideoClip,
				UnitPrice: price.String(),
			})
		}
		return variants
	default:
		return nil
	}
}

// videoPriceVariantsUSD is the USD counterpart of videoPriceVariantsNative. It
// operates on the already-derived per-unit USD decimal string (e.g. "0.4",
// from derivePerUnitUSD) rather than a wei integer, and formats results with
// pricefeed.TrimTrailingZeros so a variant's unit_price renders identically to
// the base pricing_usd.video value for the same magnitude.
func videoPriceVariantsUSD(billing *config.BillingConfig, baseUSD string) []ModelPriceVariant {
	if billing == nil || baseUSD == "" {
		return nil
	}
	base, ok := new(big.Rat).SetString(baseUSD)
	if !ok {
		return nil
	}
	switch billing.Mode {
	case config.BillingModePerVideoSecond:
		if len(billing.ResolutionMultipliers) == 0 {
			return nil
		}
		variants := make([]ModelPriceVariant, 0, len(billing.ResolutionMultipliers))
		for res, mult := range billing.ResolutionMultipliers {
			rat := new(big.Rat).SetFloat64(mult)
			if rat == nil {
				continue
			}
			rat.Mul(rat, base)
			variants = append(variants, ModelPriceVariant{
				Dimensions: map[string]string{"resolution": res},
				Unit:       unitVideoSecond,
				UnitPrice:  pricefeed.TrimTrailingZeros(rat.FloatString(18)),
			})
		}
		sortVariantsByResolution(variants)
		return variants
	case config.BillingModePerUnitTable:
		if len(billing.Table) == 0 {
			return nil
		}
		variants := make([]ModelPriceVariant, 0, len(billing.Table))
		for _, t := range billing.Table {
			rat := new(big.Rat).Mul(base, new(big.Rat).SetInt64(t.Units))
			variants = append(variants, ModelPriceVariant{
				Dimensions: map[string]string{
					"resolution":       t.Resolution,
					"duration_seconds": strconv.FormatInt(t.Duration, 10),
				},
				Unit:      unitVideoClip,
				UnitPrice: pricefeed.TrimTrailingZeros(rat.FloatString(18)),
			})
		}
		return variants
	default:
		return nil
	}
}

// GetModels returns the list of models served by this broker.
//
//	@Description  Returns models available on this broker in OpenAI-compatible format
//	@ID           getModels
//	@Tags         models
//	@Produce      json
//	@Router       /models [get]
//	@Success      200  {object}  ModelListResponse
func (h *Handler) GetModels(ctx *gin.Context) {
	svc, err := h.modelsCtrl.GetCachedService(ctx)
	if err != nil {
		handleBrokerError(ctx, err, "get service for models")
		return
	}

	cfg := h.modelsCtrl.GetServiceConfig()

	// Multi-model: return one ModelObject per configured model (including the
	// wildcard "*" entry, so clients learn the catch-all price for unlisted models).
	if cfg.HasMultiModelPricing() {
		models := make([]ModelObject, 0, len(cfg.ModelPricing))
		teeVerifier := parseTeeVerifier(svc.AdditionalInfo)
		// Serving domain is provider-level (one targetUrl for all models); compute
		// once. Only meaningful for centralized providers.
		var servingDomain string
		if cfg.IsCentralized() {
			servingDomain = parseServingDomain(cfg.TargetURL)
		}
		// Provider class is surfaced for every forwarder (centralized and standard),
		// same gate as the single-model path. A standard provider still hides its
		// upstream, so providerIdentity stays centralized-only (and servingDomain
		// above is already IsCentralized()-gated).
		var providerType, providerIdentity string
		if cfg.IsForwarder() {
			providerType = cfg.ProviderType
		}
		if cfg.IsCentralized() {
			providerIdentity = cfg.ProviderIdentity
		}
		// A standard provider performs no response attestation; its settlement TEE
		// signer being acknowledged must not surface as tee_attested (that marker
		// reflects response verifiability, which standard deliberately omits).
		teeAttested := svc.TeeSignerAcknowledged && !cfg.IsStandard()
		var created int64
		if svc.CreatedAt != nil {
			created = svc.CreatedAt.Unix()
		}
		isUSD := cfg.IsUSDDenominated()

		// Per-user rate limits are provider-level (same for every served model);
		// compute once and attach to each. Multi-model serves token-based
		// modalities (chatbot / speech-to-text), so RPM + TPM apply.
		concurrencyLimits := h.modelsCtrl.GetConcurrencyLimitConfig()
		var sharedLimits *ModelRateLimits
		if concurrencyLimits.PerUserRPM > 0 || concurrencyLimits.PerUserTPM > 0 {
			sharedLimits = &ModelRateLimits{
				RequestsPerMinute: concurrencyLimits.PerUserRPM,
				TokensPerMinute:   concurrencyLimits.PerUserTPM,
			}
		}

		// Cache token billing: service-level default, overridable per-model below
		// (matches billing's per-model resolution in GetBillingPrices).
		cacheCfg := h.modelsCtrl.GetCacheTokenBillingConfig()

		// For USD providers, fetch the rate snapshot once to convert each model's
		// USD price to wei and to surface the shared price-feed state.
		var priceFeedOut *PriceFeedState
		var ratUSDPerOG *big.Rat
		if isUSD {
			if snap, threshold, updateInterval, ok := h.modelsCtrl.GetPriceFeedSnapshot(); ok && snap.Populated && snap.RateUSDPerOG != nil {
				ratUSDPerOG = snap.RateUSDPerOG
				priceFeedOut = &PriceFeedState{
					RateUSDPerOG:   snap.RateUSDPerOG.FloatString(8),
					UpdatedAt:      snap.LastUpdate,
					NextUpdateTime: snap.LastUpdate.Add(updateInterval),
					IsStale:        snap.IsStale(threshold, time.Now()),
				}
			}
		}

		for i := range cfg.ModelPricing {
			mp := &cfg.ModelPricing[i]
			// The wildcard ("*") is a billing catch-all, not a selectable model.
			// Emitting it as a ModelObject.ID would break OpenAI-compatible clients
			// that enumerate /v1/models and treat each id as a usable model. Its
			// catch-all pricing is still published on-chain in additionalInfo.
			if mp.Model == config.ModelWildcard {
				continue
			}
			// Per-model canonical wins; fall back to the service-level canonical so
			// an operator who set only service.canonicalId still gets it applied.
			canonicalID := mp.CanonicalID
			if canonicalID == "" {
				canonicalID = cfg.CanonicalID
			}
			// Per-model cache discount: the entry's override wins; else the
			// service-level default — same resolution billing uses.
			effCache := cacheCfg
			if mp.CacheTokenBilling != nil {
				effCache = *mp.CacheTokenBilling
			}
			modelCacheBilling := newModelCacheTokenBilling(effCache)
			obj := ModelObject{
				ID:               mp.Model,
				CanonicalID:      canonicalID,
				Object:           "model",
				Created:          created,
				OwnedBy:          cfg.OwnedBy,
				Type:             svc.Type,
				Verifiability:    svc.Verifiability,
				TeeAttested:      teeAttested,
				TeeVerifier:      teeVerifier,
				Pricing:          &ModelPricing{CacheTokenBilling: modelCacheBilling},
				ProviderType:     providerType,
				ProviderIdentity: providerIdentity,
				ProviderName:     cfg.ProviderName,
				ProviderCountry:  cfg.ProviderCountry,
				ServingDomain:    servingDomain,
				RateLimits:       sharedLimits,
			}

			// Per-model metadata: the entry's own modelInfo wins; fall back to the
			// service-level modelInfo so a same-family catalog needn't repeat the
			// block per entry. Mirrors the single-model enrichment below. Config
			// validation guarantees a non-nil entry.ModelInfo is complete.
			mi := mp.ModelInfo
			if mi == nil {
				mi = cfg.ModelInfo
			}
			if mi != nil {
				obj.Name = mi.Name
				obj.Description = mi.Description
				obj.ContextLength = mi.ContextLength
				obj.MaxCompletionTokens = mi.MaxCompletionTokens
				obj.Architecture = mi.Architecture
				obj.SupportedParameters = ctrl.AdvertisedSupportedParameters(mi.SupportedParameters, mi.SupportedFormats)
				obj.SupportedFormats = mi.SupportedFormats
				obj.DefaultParameters = mi.DefaultParameters
				obj.ExpirationDate = mi.ExpirationDate
				if obj.TeeType == "" {
					obj.TeeType = mi.TeeType
				}
			}

			if isUSD && svc.Type == constant.ServiceTypeVideoGeneration {
				// USD video: bill unit is the effective output second, not a token.
				// Surface the per-second USD and, when the feed is up, the
				// rate-converted wei-per-second under `video`. Deriving the display
				// value from OutputPriceUSDPerMillionTokens via the shared ÷1e6
				// helper (rather than echoing the raw operator string) matches the
				// single-model video/image path's normalized-and-trimmed formatting,
				// so numerically identical prices render identically regardless of
				// which pricing shape (single- or multi-model) served the request.
				if video, ok := h.derivePerUnitUSD("second", mp.OutputPriceUSDPerMillionTokens, mp.Model); ok {
					obj.PricingUSD = &ModelPricingUSD{Prompt: "0", Completion: "0", Video: video}
					obj.PricingUSD.Variants = videoPriceVariantsUSD(mp.Billing, video)
				}
				if ratUSDPerOG != nil {
					if outRat, err := pricefeed.ParseUSDPerMillion(mp.OutputPriceUSDPerMillionTokens); err == nil {
						if wei, err := pricefeed.USDPerMillionToWeiPerToken(outRat, ratUSDPerOG); err == nil {
							obj.Pricing.Video = wei.String()
							obj.Pricing.Variants = videoPriceVariantsNative(mp.Billing, wei.String())
						}
					}
				}
			} else if isUSD {
				// Surface per-token USD (always) and the rate-converted wei price
				// (when the feed is available) so clients see both views. Config
				// validation already accepted these USD strings, so a conversion
				// error here signals a regression — log it (matching the
				// single-model path) rather than silently dropping PricingUSD.
				prompt, promptErr := pricefeed.USDPerMillionStringToPerToken(mp.InputPriceUSDPerMillionTokens)
				completion, completionErr := pricefeed.USDPerMillionStringToPerToken(mp.OutputPriceUSDPerMillionTokens)
				switch {
				case promptErr != nil:
					h.logger.Warnf("GetModels: derive per-token USD input price for model %q from %q failed (omitting PricingUSD): %v",
						mp.Model, mp.InputPriceUSDPerMillionTokens, promptErr)
				case completionErr != nil:
					h.logger.Warnf("GetModels: derive per-token USD output price for model %q from %q failed (omitting PricingUSD): %v",
						mp.Model, mp.OutputPriceUSDPerMillionTokens, completionErr)
				default:
					obj.PricingUSD = &ModelPricingUSD{Prompt: prompt, Completion: completion}
				}
				if ratUSDPerOG != nil {
					if inRat, err := pricefeed.ParseUSDPerMillion(mp.InputPriceUSDPerMillionTokens); err == nil {
						if wei, err := pricefeed.USDPerMillionToWeiPerToken(inRat, ratUSDPerOG); err == nil {
							obj.Pricing.Prompt = wei.String()
						}
					}
					if outRat, err := pricefeed.ParseUSDPerMillion(mp.OutputPriceUSDPerMillionTokens); err == nil {
						if wei, err := pricefeed.USDPerMillionToWeiPerToken(outRat, ratUSDPerOG); err == nil {
							obj.Pricing.Completion = wei.String()
						}
					}
				}
			} else if svc.Type == constant.ServiceTypeVideoGeneration {
				// Video bills per effective second via OutputPrice; surface it under
				// `video` (not the token `completion` field, which would mislead
				// OpenAI-compatible clients into a per-token reading).
				obj.Pricing.Video = mp.OutputPrice
				obj.Pricing.Variants = videoPriceVariantsNative(mp.Billing, mp.OutputPrice)
			} else {
				obj.Pricing.Prompt = mp.InputPrice
				obj.Pricing.Completion = mp.OutputPrice
			}

			// Per-model tiers; fall back to the service-level tieredPricing display
			// so the surfaced tiers match what billing actually applies.
			tiers := mp.Tiers
			if len(tiers) == 0 {
				if tc := h.modelsCtrl.GetTieredPricingConfig(); tc.Enabled {
					tiers = tc.Tiers
				}
			}
			if len(tiers) > 0 {
				out := make([]ModelPricingTier, len(tiers))
				for j, t := range tiers {
					out[j] = toModelPricingTier(t)
				}
				obj.Pricing.TieredPricing = out
			}

			models = append(models, obj)
		}

		ctx.JSON(http.StatusOK, ModelListResponse{
			Object:    "list",
			Data:      models,
			PriceFeed: priceFeedOut,
		})
		return
	}

	// Single-model: existing behavior
	obj := ModelObject{
		ID:            svc.ModelType,
		CanonicalID:   cfg.CanonicalID,
		Object:        "model",
		OwnedBy:       cfg.OwnedBy,
		Type:          svc.Type,
		Verifiability: svc.Verifiability,
		// A standard provider does no response attestation; don't surface its
		// settlement-signer acknowledgement as tee_attested (mirrors multi-model).
		TeeAttested: svc.TeeSignerAcknowledged && !cfg.IsStandard(),
		Pricing:     &ModelPricing{},
	}

	if svc.CreatedAt != nil {
		obj.Created = svc.CreatedAt.Unix()
	}

	// Enrich with optional config-based metadata
	if cfg.ModelInfo != nil {
		obj.Name = cfg.ModelInfo.Name
		obj.Description = cfg.ModelInfo.Description
		obj.ContextLength = cfg.ModelInfo.ContextLength
		obj.MaxCompletionTokens = cfg.ModelInfo.MaxCompletionTokens
		obj.Architecture = cfg.ModelInfo.Architecture
		obj.SupportedParameters = ctrl.AdvertisedSupportedParameters(cfg.ModelInfo.SupportedParameters, cfg.ModelInfo.SupportedFormats)
		obj.SupportedFormats = cfg.ModelInfo.SupportedFormats
		obj.DefaultParameters = cfg.ModelInfo.DefaultParameters
		obj.TeeType = cfg.ModelInfo.TeeType
		obj.ExpirationDate = cfg.ModelInfo.ExpirationDate
	}

	// Native per-unit pricing keyed by modality. Image generation / editing bill
	// per generated image (OutputPrice is the per-image price), surfaced under
	// `image`; the per-token prompt/completion fields don't apply, so they report
	// 0 rather than a misleading per-token rate. Video keeps its existing shape
	// (prompt/completion plus the per-second `video`). Token modalities use
	// prompt/completion.
	switch svc.Type {
	case constant.ServiceTypeTextToImage, constant.ServiceTypeImageEditing:
		// Image bills per generated image (under `image`); there is no per-token
		// charge — the input fee is fixed at 0 in the request path — so report both
		// prompt and completion as 0 rather than a misleading per-token rate.
		obj.Pricing.Prompt = "0"
		obj.Pricing.Completion = "0"
		obj.Pricing.Image = svc.OutputPrice
	case constant.ServiceTypeVideoGeneration:
		obj.Pricing.Prompt = svc.InputPrice
		obj.Pricing.Completion = svc.OutputPrice
		obj.Pricing.Video = svc.OutputPrice
	default:
		obj.Pricing.Prompt = svc.InputPrice
		obj.Pricing.Completion = svc.OutputPrice
	}

	// Populate tiered pricing from config
	tieredCfg := h.modelsCtrl.GetTieredPricingConfig()
	if tieredCfg.Enabled && len(tieredCfg.Tiers) > 0 {
		tiers := make([]ModelPricingTier, len(tieredCfg.Tiers))
		for i, t := range tieredCfg.Tiers {
			tiers[i] = toModelPricingTier(t)
		}
		obj.Pricing.TieredPricing = tiers
	}

	// Populate cache token billing from config
	obj.Pricing.CacheTokenBilling = newModelCacheTokenBilling(h.modelsCtrl.GetCacheTokenBillingConfig())

	// Extract TEE verifier from on-chain additionalInfo JSON
	obj.TeeVerifier = parseTeeVerifier(svc.AdditionalInfo)

	// Populate per-user rate limits from concurrency config
	concurrencyLimits := h.modelsCtrl.GetConcurrencyLimitConfig()
	rl := &ModelRateLimits{}
	hasLimits := false
	if concurrencyLimits.PerUserRPM > 0 {
		rl.RequestsPerMinute = concurrencyLimits.PerUserRPM
		hasLimits = true
	}
	switch svc.Type {
	case constant.ServiceTypeChatbot, constant.ServiceTypeSpeechToText:
		if concurrencyLimits.PerUserTPM > 0 {
			rl.TokensPerMinute = concurrencyLimits.PerUserTPM
			hasLimits = true
		}
	case constant.ServiceTypeTextToImage, constant.ServiceTypeImageEditing:
		if concurrencyLimits.PerUserIPM > 0 {
			rl.ImagesPerMinute = concurrencyLimits.PerUserIPM
			hasLimits = true
		}
	}
	if hasLimits {
		obj.RateLimits = rl
	}

	// Surface the provider class for every forwarder (centralized and standard) so
	// clients can identify the provider type. A standard provider still hides its
	// upstream: provider_identity and serving_domain remain centralized-only.
	if cfg.IsForwarder() {
		obj.ProviderType = cfg.ProviderType
	}
	if cfg.IsCentralized() {
		obj.ProviderIdentity = cfg.ProviderIdentity
		obj.ServingDomain = parseServingDomain(cfg.TargetURL)
	}

	// Provider display metadata is provider-type-agnostic; surface whenever set.
	obj.ProviderName = cfg.ProviderName
	obj.ProviderCountry = cfg.ProviderCountry

	// USD-denominated providers: surface per-token USD pricing (derived from
	// the configured per-1M-tokens value) plus the live rate-feed state.
	// Both blocks are omitted entirely in NATIVE mode.
	//
	// Config validation already rejects malformed USD strings at load time,
	// so a conversion error here indicates a programming bug or a future
	// regression that slipped past validation.  We log a warning rather
	// than fail the whole response — the caller still gets a valid model
	// list — but the log signal prevents "PricingUSD silently missing"
	// from going undiagnosed.
	var priceFeedOut *PriceFeedState
	isImageType := svc.Type == constant.ServiceTypeTextToImage || svc.Type == constant.ServiceTypeImageEditing
	isVideoType := svc.Type == constant.ServiceTypeVideoGeneration
	switch {
	case isImageType && svc.OutputPriceUSDPerMillionTokens != "":
		// Image bills per generated image, not per token: surface the per-image
		// USD under `image` (mirrors the native pricing.image) and report the
		// per-token prompt/completion as 0. Deriving with the same ÷1e6 used by
		// the wei conversion keeps pricing_usd.image consistent with pricing.image.
		if image, ok := h.derivePerUnitUSD("image", svc.OutputPriceUSDPerMillionTokens, ""); ok {
			obj.PricingUSD = &ModelPricingUSD{Prompt: "0", Completion: "0", Image: image}
		}
	case isVideoType && svc.OutputPriceUSDPerMillionTokens != "":
		// Video bills per effective output second, not per token: surface the
		// per-second USD under `video` (mirrors the native pricing.video and the
		// multi-model modelPricing path, which derives its display value the
		// same ÷1e6 way — see the isUSD && ServiceTypeVideoGeneration branch
		// above). Deriving with the same ÷1e6 used by the wei conversion keeps
		// pricing_usd.video consistent with pricing.video — same derivation as
		// the image case above.
		if video, ok := h.derivePerUnitUSD("second", svc.OutputPriceUSDPerMillionTokens, ""); ok {
			obj.PricingUSD = &ModelPricingUSD{Prompt: "0", Completion: "0", Video: video}
		}
	case svc.InputPriceUSDPerMillionTokens != "" && svc.OutputPriceUSDPerMillionTokens != "":
		prompt, promptErr := pricefeed.USDPerMillionStringToPerToken(svc.InputPriceUSDPerMillionTokens)
		completion, completionErr := pricefeed.USDPerMillionStringToPerToken(svc.OutputPriceUSDPerMillionTokens)
		switch {
		case promptErr != nil:
			h.logger.Warnf("GetModels: derive per-token USD input price from %q failed (omitting PricingUSD block): %v",
				svc.InputPriceUSDPerMillionTokens, promptErr)
		case completionErr != nil:
			h.logger.Warnf("GetModels: derive per-token USD output price from %q failed (omitting PricingUSD block): %v",
				svc.OutputPriceUSDPerMillionTokens, completionErr)
		default:
			obj.PricingUSD = &ModelPricingUSD{Prompt: prompt, Completion: completion}
		}
	}
	if snap, threshold, updateInterval, isUSD := h.modelsCtrl.GetPriceFeedSnapshot(); isUSD && snap.Populated && snap.RateUSDPerOG != nil {
		priceFeedOut = &PriceFeedState{
			RateUSDPerOG:   snap.RateUSDPerOG.FloatString(8),
			UpdatedAt:      snap.LastUpdate,
			NextUpdateTime: snap.LastUpdate.Add(updateInterval),
			IsStale:        snap.IsStale(threshold, time.Now()),
		}
	}

	ctx.JSON(http.StatusOK, ModelListResponse{
		Object:    "list",
		Data:      []ModelObject{obj},
		PriceFeed: priceFeedOut,
	})
}

// parseServingDomain extracts the bare hostname (FQDN) from a target URL,
// stripping scheme, port, and path. Returns "" if the URL is empty or cannot be
// parsed into a host. The result is meant to match the TLS SNI / certificate SAN
// of the upstream the broker connects to (e.g. "api.openai.com").
func parseServingDomain(targetURL string) string {
	if targetURL == "" {
		return ""
	}
	u, err := url.Parse(targetURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// parseTeeVerifier extracts the TEEVerifier field from the additionalInfo JSON string.
func parseTeeVerifier(additionalInfo string) string {
	if additionalInfo == "" {
		return ""
	}
	var info struct {
		TEEVerifier string `json:"TEEVerifier"`
	}
	if err := json.Unmarshal([]byte(additionalInfo), &info); err != nil {
		return ""
	}
	return info.TEEVerifier
}
