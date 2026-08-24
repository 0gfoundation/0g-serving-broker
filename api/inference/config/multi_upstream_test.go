package config

import (
	"strings"
	"testing"
)

// TestEffectiveTargetURL covers the per-model upstream base URL resolution that
// lets one provider front several upstreams: an entry with its own targetUrl
// wins, an entry without one (and the wildcard catch-all, and the empty/unknown
// model) falls back to the service-level targetUrl.
func TestEffectiveTargetURL(t *testing.T) {
	s := &Service{
		TargetURL: "https://svc.example.com/v1",
		ModelPricing: []ModelPricingEntry{
			{Model: "bailian-x", InputPrice: "1", OutputPrice: "2", TargetURL: "https://bailian.example.com/v1"},
			{Model: "minimax-y", InputPrice: "1", OutputPrice: "2", TargetURL: "https://minimax.example.com/v1"},
			{Model: "shared-z", InputPrice: "1", OutputPrice: "2"},
			{Model: ModelWildcard, InputPrice: "1", OutputPrice: "2"},
		},
	}
	if err := s.BuildModelPricingMap(); err != nil {
		t.Fatal(err)
	}

	cases := map[string]string{
		"bailian-x": "https://bailian.example.com/v1",
		"minimax-y": "https://minimax.example.com/v1",
		"shared-z":  "https://svc.example.com/v1", // no override → service-level
		"unknown":   "https://svc.example.com/v1", // folds onto wildcard (no override) → service-level
		"":          "https://svc.example.com/v1", // single-model/unresolved paths
	}
	for model, want := range cases {
		if got := s.EffectiveTargetURL(model); got != want {
			t.Errorf("EffectiveTargetURL(%q) = %q; want %q", model, got, want)
		}
	}

	// A wildcard entry that itself sets a targetUrl routes every unenumerated
	// model to that upstream.
	sWild := &Service{
		TargetURL: "https://svc.example.com/v1",
		ModelPricing: []ModelPricingEntry{
			{Model: ModelWildcard, InputPrice: "1", OutputPrice: "2", TargetURL: "https://catchall.example.com/v1"},
		},
	}
	if err := sWild.BuildModelPricingMap(); err != nil {
		t.Fatal(err)
	}
	if got := sWild.EffectiveTargetURL("anything"); got != "https://catchall.example.com/v1" {
		t.Errorf("wildcard EffectiveTargetURL = %q; want catch-all upstream", got)
	}

	// A single-model provider (no modelPricing) always yields the service URL.
	sSingle := &Service{TargetURL: "https://only.example.com/v1"}
	if got := sSingle.EffectiveTargetURL("whatever"); got != "https://only.example.com/v1" {
		t.Errorf("single-model EffectiveTargetURL = %q; want service URL", got)
	}
}

// TestEffectiveAdditionalSecret_TargetURLDecouple verifies the credential-leak
// guard: a model routed to its OWN (different) upstream via per-model targetUrl
// must NOT inherit the service-level API key, but one without a targetUrl (or one
// pointing at the same host) still falls back to it.
func TestEffectiveAdditionalSecret_TargetURLDecouple(t *testing.T) {
	s := &Service{
		TargetURL:        "https://svc.example.com/v1",
		AdditionalSecret: map[string]string{"Authorization": "Bearer svc-key"},
		ModelPricing: []ModelPricingEntry{
			// Different upstream, no own key → must NOT leak the service key.
			{Model: "rehomed", InputPrice: "1", OutputPrice: "2", TargetURL: "https://other.example.com/v1"},
			// Different upstream, own key → uses its own key.
			{Model: "rehomed-keyed", InputPrice: "1", OutputPrice: "2", TargetURL: "https://other.example.com/v1", AdditionalSecret: map[string]string{"Authorization": "Bearer own-key"}},
			// No targetUrl override → shares service upstream → keeps service key.
			{Model: "shared", InputPrice: "1", OutputPrice: "2"},
			// Same host as service → keeps service key.
			{Model: "same-host", InputPrice: "1", OutputPrice: "2", TargetURL: "https://svc.example.com/v1"},
		},
	}
	if err := s.BuildModelPricingMap(); err != nil {
		t.Fatal(err)
	}
	if got := s.EffectiveAdditionalSecret("rehomed"); got != nil {
		t.Errorf("rehomed (different upstream, no own key) = %v; want nil (no service-key leak)", got)
	}
	if got := s.EffectiveAdditionalSecret("rehomed-keyed"); got["Authorization"] != "Bearer own-key" {
		t.Errorf("rehomed-keyed = %v; want own key", got)
	}
	if got := s.EffectiveAdditionalSecret("shared"); got["Authorization"] != "Bearer svc-key" {
		t.Errorf("shared = %v; want service key", got)
	}
	if got := s.EffectiveAdditionalSecret("same-host"); got["Authorization"] != "Bearer svc-key" {
		t.Errorf("same-host = %v; want service key", got)
	}
}

// TestEffectiveProviderIdentity covers per-model upstream identity resolution
// used by the routing proof and the reconciliation rollup.
func TestEffectiveProviderIdentity(t *testing.T) {
	s := &Service{
		ProviderIdentity: "aliyun",
		ModelPricing: []ModelPricingEntry{
			{Model: "bailian-x", InputPrice: "1", OutputPrice: "2"},                              // no override → service-level
			{Model: "minimax-y", InputPrice: "1", OutputPrice: "2", ProviderIdentity: "minimax"}, // override
		},
	}
	if err := s.BuildModelPricingMap(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"bailian-x": "aliyun",
		"minimax-y": "minimax",
		"unknown":   "aliyun",
		"":          "aliyun",
	}
	for model, want := range cases {
		if got := s.EffectiveProviderIdentity(model); got != want {
			t.Errorf("EffectiveProviderIdentity(%q) = %q; want %q", model, got, want)
		}
	}
}

// TestValidateModelUpstream exercises the per-model upstream override validation.
func TestValidateModelUpstream(t *testing.T) {
	tests := []struct {
		name          string
		entry         ModelPricingEntry
		serviceType   string // "" defaults to chatbot
		isCentralized bool
		wantErr       string // substring; "" means expect success
		wantIdentity  string // expected normalized ProviderIdentity after validate
	}{
		{
			name:          "valid https upstream, centralized",
			entry:         ModelPricingEntry{Model: "m", TargetURL: "https://up.example.com/v1"},
			isCentralized: true,
		},
		{
			name:    "http allowed for non-centralized forwarder",
			entry:   ModelPricingEntry{Model: "m", TargetURL: "http://up.internal/v1"},
			wantErr: "",
		},
		{
			name:          "http rejected for centralized (needs TLS)",
			entry:         ModelPricingEntry{Model: "m", TargetURL: "http://up.example.com/v1"},
			isCentralized: true,
			wantErr:       "must use HTTPS",
		},
		{
			name:    "relative url rejected",
			entry:   ModelPricingEntry{Model: "m", TargetURL: "/v1/chat"},
			wantErr: "absolute http(s) URL",
		},
		{
			name:    "whitespace rejected",
			entry:   ModelPricingEntry{Model: "m", TargetURL: " https://up.example.com "},
			wantErr: "whitespace",
		},
		{
			name:          "provider identity normalized to lowercase",
			entry:         ModelPricingEntry{Model: "m", ProviderIdentity: "MiniMax"},
			isCentralized: true,
			wantIdentity:  "minimax",
		},
		{
			name:          "invalid provider identity rejected",
			entry:         ModelPricingEntry{Model: "m", ProviderIdentity: "bad ident!"},
			isCentralized: true,
			wantErr:       "providerIdentity",
		},
		{
			name:  "both empty is a no-op",
			entry: ModelPricingEntry{Model: "m"},
		},
		{
			// Per-model targetUrl is unsupported for video: the poll/content paths
			// use the SERVICE targetUrl, so this would leak the per-model secret to
			// the wrong host and never bill. Reject at load.
			name:        "per-model targetUrl rejected for video",
			entry:       ModelPricingEntry{Model: "m", TargetURL: "https://up.example.com/v1"},
			serviceType: "video-generation",
			wantErr:     "not supported for service type 'video-generation'",
		},
		{
			// Per-model providerIdentity is silently dropped on the video proof/
			// reconciliation path (service-level identity only). Reject to avoid mislabel.
			name:        "per-model providerIdentity rejected for video",
			entry:       ModelPricingEntry{Model: "m", ProviderIdentity: "minimax"},
			serviceType: "video-generation",
			wantErr:     "not supported for service type 'video-generation'",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := tt.entry
			st := tt.serviceType
			if st == "" {
				st = "chatbot"
			}
			err := validateModelUpstream(0, &e, st, tt.isCentralized)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if tt.wantIdentity != "" && e.ProviderIdentity != tt.wantIdentity {
					t.Fatalf("ProviderIdentity = %q; want normalized %q", e.ProviderIdentity, tt.wantIdentity)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v; want substring %q", err, tt.wantErr)
			}
		})
	}
}
