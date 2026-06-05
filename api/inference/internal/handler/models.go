package handler

import (
	"encoding/json"
	"math/big"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/inference/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
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
	RateLimits          *ModelRateLimits          `json:"rate_limits,omitempty"`
}

// ModelRateLimits exposes per-user rate limit configuration so clients/SDKs
// can perform client-side throttling before hitting server-side 429s.
type ModelRateLimits struct {
	RequestsPerMinute int `json:"requests_per_minute,omitempty"`
	TokensPerMinute   int `json:"tokens_per_minute,omitempty"`
	ImagesPerMinute   int `json:"images_per_minute,omitempty"`
}

// ModelPricingTier represents a single tier in tiered pricing.
type ModelPricingTier struct {
	MaxInputTokens   int   `json:"max_input_tokens"`
	InputMultiplier  int64 `json:"input_multiplier"`
	OutputMultiplier int64 `json:"output_multiplier"`
}

// ModelCacheTokenBilling holds cache token billing info for display.
type ModelCacheTokenBilling struct {
	Divisor int64 `json:"divisor"`
}

// ModelPricing holds per-token pricing in the smallest unit (wei).
type ModelPricing struct {
	Prompt            string                  `json:"prompt"`
	Completion        string                  `json:"completion"`
	Image             string                  `json:"image,omitempty"`
	Video             string                  `json:"video,omitempty"`
	TieredPricing     []ModelPricingTier      `json:"tiered_pricing,omitempty"`
	CacheTokenBilling *ModelCacheTokenBilling `json:"cache_token_billing,omitempty"`
}

// ModelPricingUSD holds per-token USD pricing as trimmed decimal strings.
// Present only when the provider is USD-denominated; omitted in NATIVE mode.
// Values are derived from the configured USD-per-1M-tokens by exact big.Rat
// division — no float precision loss.
type ModelPricingUSD struct {
	Prompt     string `json:"prompt"`
	Completion string `json:"completion"`
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

		// Cache token billing is provider-level; surface it on every model.
		cacheCfg := h.modelsCtrl.GetCacheTokenBillingConfig()
		var sharedCacheBilling *ModelCacheTokenBilling
		if cacheCfg.Enabled && cacheCfg.Divisor > 0 {
			sharedCacheBilling = &ModelCacheTokenBilling{Divisor: cacheCfg.Divisor}
		}

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
			obj := ModelObject{
				ID:               mp.Model,
				CanonicalID:      canonicalID,
				Object:           "model",
				Created:          created,
				OwnedBy:          cfg.OwnedBy,
				Type:             svc.Type,
				Verifiability:    svc.Verifiability,
				TeeAttested:      svc.TeeSignerAcknowledged,
				TeeVerifier:      teeVerifier,
				Pricing:          &ModelPricing{CacheTokenBilling: sharedCacheBilling},
				ProviderType:     cfg.ProviderType,
				ProviderIdentity: cfg.ProviderIdentity,
				RateLimits:       sharedLimits,
			}

			if isUSD {
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
					out[j] = ModelPricingTier{
						MaxInputTokens:   t.MaxInputTokens,
						InputMultiplier:  t.InputMultiplier,
						OutputMultiplier: t.OutputMultiplier,
					}
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
		TeeAttested:   svc.TeeSignerAcknowledged,
		Pricing: &ModelPricing{
			Prompt:     svc.InputPrice,
			Completion: svc.OutputPrice,
		},
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
		obj.SupportedParameters = cfg.ModelInfo.SupportedParameters
		obj.SupportedFormats = cfg.ModelInfo.SupportedFormats
		obj.DefaultParameters = cfg.ModelInfo.DefaultParameters
		obj.TeeType = cfg.ModelInfo.TeeType
		obj.ExpirationDate = cfg.ModelInfo.ExpirationDate
	}

	// Set image pricing from output price for image service types
	if svc.Type == constant.ServiceTypeTextToImage || svc.Type == constant.ServiceTypeImageEditing {
		obj.Pricing.Image = svc.OutputPrice
	}

	// Set video pricing from output price for video service type
	if svc.Type == constant.ServiceTypeVideoGeneration {
		obj.Pricing.Video = svc.OutputPrice
	}

	// Populate tiered pricing from config
	tieredCfg := h.modelsCtrl.GetTieredPricingConfig()
	if tieredCfg.Enabled && len(tieredCfg.Tiers) > 0 {
		tiers := make([]ModelPricingTier, len(tieredCfg.Tiers))
		for i, t := range tieredCfg.Tiers {
			tiers[i] = ModelPricingTier{
				MaxInputTokens:   t.MaxInputTokens,
				InputMultiplier:  t.InputMultiplier,
				OutputMultiplier: t.OutputMultiplier,
			}
		}
		obj.Pricing.TieredPricing = tiers
	}

	// Populate cache token billing from config
	cacheCfg := h.modelsCtrl.GetCacheTokenBillingConfig()
	if cacheCfg.Enabled && cacheCfg.Divisor > 0 {
		obj.Pricing.CacheTokenBilling = &ModelCacheTokenBilling{
			Divisor: cacheCfg.Divisor,
		}
	}

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

	// Expose centralized proxy info so SDK can choose the correct verification path
	if cfg.IsCentralized() {
		obj.ProviderType = cfg.ProviderType
		obj.ProviderIdentity = cfg.ProviderIdentity
	}

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
	if svc.InputPriceUSDPerMillionTokens != "" && svc.OutputPriceUSDPerMillionTokens != "" {
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
