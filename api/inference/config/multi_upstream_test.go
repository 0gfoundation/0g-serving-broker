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

// TestResolveSameModelUpstreamByIdentity covers one broker holding two entries
// for the SAME canonical model at different upstreams, disambiguated by the
// per-model providerIdentity the router sends via X-0G-Upstream. Also asserts
// the backward-compat path: a single-entry model still resolves with no identity.
func TestResolveSameModelUpstreamByIdentity(t *testing.T) {
	s := &Service{
		ModelPricing: []ModelPricingEntry{
			{Model: "glm-5.2", ProviderIdentity: "aliyun", InputPrice: "1", OutputPrice: "2", TargetURL: "https://aliyun.example.com/v1"},
			{Model: "glm-5.2", ProviderIdentity: "zhipu", InputPrice: "3", OutputPrice: "4", TargetURL: "https://zhipu.example.com/v1"},
			{Model: "solo-x", InputPrice: "1", OutputPrice: "2"},
		},
	}
	// Two same-model entries at distinct upstreams build without the dup error.
	if err := s.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: unexpected error: %v", err)
	}

	// Identity picks the matching upstream entry.
	e, resolved, err := s.ResolveRequestedModel("glm-5.2", "zhipu")
	if err != nil || e == nil || resolved != "glm-5.2" || e.TargetURL != "https://zhipu.example.com/v1" {
		t.Fatalf("resolve(glm-5.2, zhipu) = %+v, %q, %v; want zhipu entry", e, resolved, err)
	}

	// No identity on a multi-upstream model is ambiguous.
	if _, _, err := s.ResolveRequestedModel("glm-5.2", ""); err != ErrAmbiguousUpstream {
		t.Fatalf("resolve(glm-5.2, \"\") err = %v; want ErrAmbiguousUpstream", err)
	}

	// Backward compat: a single-entry model resolves with no identity, as before.
	e, resolved, err = s.ResolveRequestedModel("solo-x", "")
	if err != nil || e == nil || resolved != "solo-x" {
		t.Fatalf("resolve(solo-x, \"\") = %+v, %q, %v; want solo-x entry", e, resolved, err)
	}
}

// TestSameModelUpstreamEndToEndSelection proves the FULL selection an X-0G-Upstream
// request drives — route URL, upstream secret, billing price, and proof identity —
// resolves to the entry the identity names, via the SAME identity-aware accessors
// the request path calls (not the raw map). Two entries share canonical "glm-5.2"
// at different upstreams; identity "zhipu" must select zhipu's values throughout.
// It also pins the single-entry (empty-identity) path to the pre-multi-upstream
// behavior, so existing configs are byte-identical.
func TestSameModelUpstreamEndToEndSelection(t *testing.T) {
	s := &Service{
		TargetURL:        "https://svc.example.com/v1",
		ProviderIdentity: "aliyun",
		AdditionalSecret: map[string]string{"Authorization": "Bearer svc-key"},
		ModelPricing: []ModelPricingEntry{
			// aliyun is FIRST — the pre-fix model-keyed lookup would return this for
			// every glm-5.2 request regardless of identity.
			{Model: "glm-5.2", ProviderIdentity: "aliyun", InputPrice: "1", OutputPrice: "2",
				TargetURL: "https://aliyun.example.com/v1", AdditionalSecret: map[string]string{"Authorization": "Bearer aliyun-key"}},
			{Model: "glm-5.2", ProviderIdentity: "zhipu", InputPrice: "3", OutputPrice: "4",
				TargetURL: "https://zhipu.example.com/v1", AdditionalSecret: map[string]string{"Authorization": "Bearer zhipu-key"}},
			{Model: "solo-x", InputPrice: "5", OutputPrice: "6"},
		},
	}
	if err := s.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}

	// Resolve the request the same way the request path does, then read every
	// per-model accessor with (resolved, identity) — exactly the call shape the
	// forward/secret/billing/proof sites now use.
	entry, resolved, err := s.ResolveRequestedModel("glm-5.2", "zhipu")
	if err != nil || entry == nil {
		t.Fatalf("resolve(glm-5.2, zhipu) err=%v entry=%v", err, entry)
	}

	// Route URL (proxy.go forward site).
	if got := s.EffectiveTargetURLFor(resolved, "zhipu"); got != "https://zhipu.example.com/v1" {
		t.Errorf("EffectiveTargetURLFor = %q; want zhipu upstream", got)
	}
	// Upstream secret (proxy.go secret site).
	if got := s.EffectiveAdditionalSecretFor(resolved, "zhipu")["Authorization"]; got != "Bearer zhipu-key" {
		t.Errorf("EffectiveAdditionalSecretFor = %q; want zhipu key", got)
	}
	// Billing price (service.go resolveModelPricing site).
	if got := s.GetModelPricingFor(resolved, "zhipu"); got == nil || got.OutputPrice != "4" {
		t.Errorf("GetModelPricingFor price = %v; want zhipu OutputPrice 4", got)
	}
	// Proof identity (proxy.go UpstreamForModel → signCentralizedRoutingProof).
	if got := s.EffectiveProviderIdentityFor(resolved, "zhipu"); got != "zhipu" {
		t.Errorf("EffectiveProviderIdentityFor = %q; want zhipu", got)
	}

	// Sanity: identity "aliyun" selects aliyun's values through the same accessors.
	if got := s.EffectiveTargetURLFor("glm-5.2", "aliyun"); got != "https://aliyun.example.com/v1" {
		t.Errorf("aliyun EffectiveTargetURLFor = %q; want aliyun upstream", got)
	}
	if got := s.GetModelPricingFor("glm-5.2", "aliyun"); got == nil || got.OutputPrice != "2" {
		t.Errorf("aliyun GetModelPricingFor price = %v; want aliyun OutputPrice 2", got)
	}

	// Backward compat: empty identity resolves EXACTLY like the model-only accessors
	// (the pre-multi-upstream behavior). For a single-entry model the two agree.
	if s.EffectiveTargetURLFor("solo-x", "") != s.EffectiveTargetURL("solo-x") ||
		s.EffectiveProviderIdentityFor("solo-x", "") != s.EffectiveProviderIdentity("solo-x") {
		t.Errorf("empty-identity accessors diverged from the model-only accessors for a single-entry model")
	}
	if got := s.GetModelPricingFor("solo-x", ""); got == nil || got.OutputPrice != "6" {
		t.Errorf("solo-x GetModelPricingFor(\"\") price = %v; want OutputPrice 6", got)
	}
}
