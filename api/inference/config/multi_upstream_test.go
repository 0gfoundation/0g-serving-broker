package config

import (
	"strings"
	"testing"
	"time"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
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
			// zhipu (the DEARER entry) is listed FIRST on purpose: the identity-less
			// resolution below must return aliyun, which is only possible if the pick
			// is by price and not by config order.
			{Model: "glm-5.2", ProviderIdentity: "zhipu", InputPrice: "3", OutputPrice: "4", TargetURL: "https://zhipu.example.com/v1"},
			{Model: "glm-5.2", ProviderIdentity: "aliyun", InputPrice: "1", OutputPrice: "2", TargetURL: "https://aliyun.example.com/v1"},
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

	// No identity on a multi-upstream model resolves to the CHEAPEST entry rather
	// than being rejected: no OpenAI-compatible client sends X-0G-Upstream, so a
	// direct caller must still be able to reach the model — and must not be billed
	// above the lowest price the id advertises. aliyun is listed SECOND, so getting
	// it back proves the pick is by price, not config order.
	e, resolved, err = s.ResolveRequestedModel("glm-5.2", "")
	if err != nil || e == nil || resolved != "glm-5.2" || e.ProviderIdentity != "aliyun" {
		t.Fatalf("resolve(glm-5.2, \"\") = %+v, %q, %v; want the cheapest (aliyun) entry", e, resolved, err)
	}
	// Routing follows the same entry, so the request is billed at and sent to one
	// upstream — not billed at aliyun and forwarded to zhipu.
	if got := s.EffectiveTargetURLFor("glm-5.2", ""); got != "https://aliyun.example.com/v1" {
		t.Errorf("EffectiveTargetURLFor(glm-5.2, \"\") = %q; want the cheapest entry's upstream", got)
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

// TestSameModelUpstreamRequestRewritersSelectByIdentity proves the per-entry
// request-path rewriters resolve by the SELECTED upstream, not the first entry:
// supportedFormats (enforceRequestFormat), injectBodyFields, and stripBodyFields.
// aliyun is FIRST and openai-only; zhipu also serves the anthropic surface and
// carries different inject/strip fields. The pre-fix model-keyed lookup returned
// aliyun for every glm-5.2 request, wrongly rejecting an anthropic request routed
// to zhipu and mis-applying aliyun's inject/strip on it.
func TestSameModelUpstreamRequestRewritersSelectByIdentity(t *testing.T) {
	s := &Service{
		TargetURL:        "https://svc.example.com/v1",
		ProviderIdentity: "aliyun",
		ModelPricing: []ModelPricingEntry{
			{Model: "glm-5.2", ProviderIdentity: "aliyun", InputPrice: "1", OutputPrice: "2",
				ModelInfo:        &ModelInfo{SupportedFormats: []string{"openai"}},
				InjectBodyFields: map[string]interface{}{"provider": "aliyun"},
				StripBodyFields:  []string{"logprobs"}},
			{Model: "glm-5.2", ProviderIdentity: "zhipu", InputPrice: "3", OutputPrice: "4",
				ModelInfo:        &ModelInfo{SupportedFormats: []string{"openai", "anthropic"}},
				InjectBodyFields: map[string]interface{}{"provider": "zhipu"},
				StripBodyFields:  []string{"top_logprobs"}},
			{Model: "solo-x", InputPrice: "5", OutputPrice: "6",
				ModelInfo:        &ModelInfo{SupportedFormats: []string{"openai"}},
				InjectBodyFields: map[string]interface{}{"provider": "solo"},
				StripBodyFields:  []string{"seed"}},
		},
	}
	if err := s.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}

	// supportedFormats: zhipu declares anthropic; the bug rejected it by reading
	// aliyun's openai-only set.
	if got := s.SupportedFormatsFor("glm-5.2", "zhipu"); len(got) != 2 || got[1] != "anthropic" {
		t.Errorf("SupportedFormatsFor(glm-5.2, zhipu) = %v; want zhipu's [openai anthropic]", got)
	}
	if got := s.SupportedFormatsFor("glm-5.2", "aliyun"); len(got) != 1 || got[0] != "openai" {
		t.Errorf("SupportedFormatsFor(glm-5.2, aliyun) = %v; want aliyun's [openai]", got)
	}

	// injectBodyFields: each upstream injects ITS provider tag.
	if got := s.EffectiveInjectBodyFieldsFor("glm-5.2", "zhipu")["provider"]; got != "zhipu" {
		t.Errorf("EffectiveInjectBodyFieldsFor(glm-5.2, zhipu)[provider] = %v; want zhipu", got)
	}
	if got := s.EffectiveInjectBodyFieldsFor("glm-5.2", "aliyun")["provider"]; got != "aliyun" {
		t.Errorf("EffectiveInjectBodyFieldsFor(glm-5.2, aliyun)[provider] = %v; want aliyun", got)
	}

	// stripBodyFields: each upstream strips ITS own field.
	if got := s.EffectiveStripBodyFieldsFor("glm-5.2", "zhipu"); len(got) != 1 || got[0] != "top_logprobs" {
		t.Errorf("EffectiveStripBodyFieldsFor(glm-5.2, zhipu) = %v; want [top_logprobs]", got)
	}
	if got := s.EffectiveStripBodyFieldsFor("glm-5.2", "aliyun"); len(got) != 1 || got[0] != "logprobs" {
		t.Errorf("EffectiveStripBodyFieldsFor(glm-5.2, aliyun) = %v; want [logprobs]", got)
	}

	// Backward compat: empty identity on a single-entry model is byte-identical to
	// the bare accessors (the pre-multi-upstream behavior).
	if got := s.SupportedFormatsFor("solo-x", ""); len(got) != 1 || got[0] != "openai" {
		t.Errorf("SupportedFormatsFor(solo-x, \"\") = %v; want [openai]", got)
	}
	if s.EffectiveInjectBodyFieldsFor("solo-x", "")["provider"] != s.EffectiveInjectBodyFields("solo-x")["provider"] {
		t.Error("EffectiveInjectBodyFieldsFor(solo-x, \"\") diverged from the bare accessor")
	}
	strip := s.EffectiveStripBodyFieldsFor("solo-x", "")
	if len(strip) != 1 || strip[0] != "seed" {
		t.Errorf("EffectiveStripBodyFieldsFor(solo-x, \"\") = %v; want [seed]", strip)
	}
}

// TestModelExpirationForGatesMultiUpstream proves Fix 1: an EXPIRED same-model
// entry is correctly gated when the request's upstream identity selects it,
// while per-entry metadata (ModelInfo) resolves to that same selected entry.
// The bare, identity-less ModelExpiration resolves ambiguous on a multi-upstream
// model and returns ok=false — the pre-fix fail-OPEN that let an expired
// multi-upstream model keep serving.
func TestModelExpirationForGatesMultiUpstream(t *testing.T) {
	past := "2000-01-01T00:00:00Z"
	wantExp, _ := time.Parse(time.RFC3339, past)

	zhipuMI := validModelInfo()
	zhipuMI.ExpirationDate = past
	if err := zhipuMI.Validate("chatbot"); err != nil {
		t.Fatalf("zhipu ModelInfo.Validate: %v", err)
	}
	s := &Service{
		ModelPricing: []ModelPricingEntry{
			{Model: "glm-5.2", ProviderIdentity: "aliyun", InputPrice: "1", OutputPrice: "2"},
			{Model: "glm-5.2", ProviderIdentity: "zhipu", InputPrice: "3", OutputPrice: "4", ModelInfo: zhipuMI},
		},
	}
	if err := s.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}

	// Fix 1: identity selects the expired zhipu entry, so the 410 gate fires.
	got, ok := s.ModelExpirationFor("glm-5.2", "zhipu")
	if !ok || !got.Equal(wantExp) {
		t.Errorf("ModelExpirationFor(glm-5.2, zhipu) = %v, ok=%v; want %v, true", got, ok, wantExp)
	}
	// EffectiveModelInfoFor resolves to the SAME selected entry — this is what keeps
	// per-model max_tokens capping / reasoning translation applying (the other half
	// of Fix 1), instead of being dropped on an ambiguous resolve.
	if mi := s.EffectiveModelInfoFor("glm-5.2", "zhipu"); mi != zhipuMI {
		t.Errorf("EffectiveModelInfoFor(glm-5.2, zhipu) = %v; want the zhipu entry's ModelInfo", mi)
	}
	// The aliyun entry has no expiry — identity keeps the two entries' metadata distinct.
	if _, ok := s.ModelExpirationFor("glm-5.2", "aliyun"); ok {
		t.Error("ModelExpirationFor(glm-5.2, aliyun) ok=true; want false (no expiry on that entry)")
	}
	// Identity-less: the bare accessor answers for the CHEAPEST entry. Here that is
	// aliyun, which carries no expiry, so the gate stays open — but only because the
	// expiry sits on the dearer entry. The companion test below pins the arrangement
	// that actually matters.
	if _, ok := s.ModelExpiration("glm-5.2"); ok {
		t.Error("ModelExpiration(glm-5.2) ok=true; want false (cheapest entry has no expiry)")
	}
}

// TestModelExpirationIdentitylessFollowsCheapestEntry pins the known gap the
// cheapest-entry pick leaves in the expiry gate: retiring the CHEAP upstream by
// back-dating its expirationDate takes the model away from every header-less
// caller, even though the dearer sibling is still live and still advertised.
//
// This is a gap in the new capability, not a regression — before the cheapest
// pick, an identity-less request for a multi-upstream model was rejected outright
// as an ambiguous upstream, so it never reached the upstream either. Closing it
// would mean making the pick expiry-aware, which is a per-request (wall-clock)
// condition, so every identity-agnostic accessor would have to become expiry-aware
// with it or price, routing, secret and label would stop naming one entry — the
// invariant this whole mechanism rests on. The test exists so the behaviour is
// pinned and visible rather than discovered in production.
func TestModelExpirationIdentitylessFollowsCheapestEntry(t *testing.T) {
	past := "2000-01-01T00:00:00Z"
	wantExp, _ := time.Parse(time.RFC3339, past)

	cheapMI := validModelInfo()
	cheapMI.ExpirationDate = past
	if err := cheapMI.Validate("chatbot"); err != nil {
		t.Fatalf("ModelInfo.Validate: %v", err)
	}
	s := &Service{
		ModelPricing: []ModelPricingEntry{
			// The dearer entry is live and listed FIRST; the retired one is cheapest.
			{Model: "glm-5.2", ProviderIdentity: "zhipu", InputPrice: "3", OutputPrice: "4"},
			{Model: "glm-5.2", ProviderIdentity: "aliyun", InputPrice: "1", OutputPrice: "2", ModelInfo: cheapMI},
		},
	}
	if err := s.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}

	got, ok := s.ModelExpiration("glm-5.2")
	if !ok || !got.Equal(wantExp) {
		t.Errorf("ModelExpiration(glm-5.2) = %v, ok=%v; want %v, true — the identity-less gate must follow the cheapest entry, expired or not", got, ok, wantExp)
	}
	// The live sibling is still reachable when the caller names it, which is what
	// keeps this a gap for header-less callers only.
	if _, ok := s.ModelExpirationFor("glm-5.2", "zhipu"); ok {
		t.Error("ModelExpirationFor(glm-5.2, zhipu) ok=true; want false (that entry is live)")
	}
}

// TestIdentitylessResolutionPicksCheapest pins the comparison that decides which
// entry an identity-less request lands on. Every case lists the entry that must
// WIN second, so a regression to first-wins fails rather than passes by luck.
func TestIdentitylessResolutionPicksCheapest(t *testing.T) {
	entry := func(identity, in, out string) ModelPricingEntry {
		return ModelPricingEntry{Model: "m", ProviderIdentity: identity, InputPrice: in, OutputPrice: out}
	}
	usdEntry := func(identity, in, out string) ModelPricingEntry {
		return ModelPricingEntry{Model: "m", ProviderIdentity: identity,
			InputPriceUSDPerMillionTokens: in, OutputPriceUSDPerMillionTokens: out}
	}

	cases := []struct {
		name    string
		denom   string
		entries []ModelPricingEntry
		want    string
	}{
		{
			name:    "lower output price wins",
			entries: []ModelPricingEntry{entry("dear", "1", "9"), entry("cheap", "1", "2")},
			want:    "cheap",
		},
		{
			name:    "output tie broken by input price",
			entries: []ModelPricingEntry{entry("dear", "9", "5"), entry("cheap", "1", "5")},
			want:    "cheap",
		},
		{
			// Identities chosen so config order and identity order DISAGREE: a
			// first-wins regression returns "zeta", the tie-break returns "alpha".
			name:    "full price tie broken by identity, not config order",
			entries: []ModelPricingEntry{entry("zeta", "1", "2"), entry("alpha", "1", "2")},
			want:    "alpha",
		},
		{
			// Both input prices absent must still fall through to the identity
			// tie-break, or the ordering stops being total and the pick reverts to
			// config order. Unreachable in a deployment (validateTokenModelEntry
			// requires both prices) but the invariant is stated, so it is pinned.
			name:    "equal output, both inputs absent, still broken by identity",
			entries: []ModelPricingEntry{entry("zeta", "", "5"), entry("alpha", "", "5")},
			want:    "alpha",
		},
		{
			// loadConfig validates every price before BuildModelPricingMap, so this is
			// only reachable for directly-built Services. An unparseable price must
			// never win — in EITHER position, or the pick among the parseable entries
			// would depend on where the unparseable one happens to sit.
			name:    "unparseable candidate never wins",
			entries: []ModelPricingEntry{entry("valid", "1", "9"), entry("junk", "1", "not-a-number")},
			want:    "valid",
		},
		{
			name:    "unparseable incumbent is displaced",
			entries: []ModelPricingEntry{entry("junk", "1", ""), entry("valid", "1", "9")},
			want:    "valid",
		},
		{
			// The order-independence this buys: same three entries, unparseable one
			// moved, same winner.
			name:    "unparseable position does not change the winner",
			entries: []ModelPricingEntry{entry("junk", "1", "x"), entry("dear", "1", "5"), entry("cheap", "1", "1")},
			want:    "cheap",
		},
		{
			name:    "unparseable position does not change the winner (moved)",
			entries: []ModelPricingEntry{entry("dear", "1", "5"), entry("junk", "1", "x"), entry("cheap", "1", "1")},
			want:    "cheap",
		},
		{
			// USD prices are decimal strings, and 0.4 < 0.5 only if they are compared
			// numerically — a string compare would pick "0.5" here for some orderings.
			name:    "USD service compares the USD decimal fields",
			denom:   constant.PriceDenominationUSD,
			entries: []ModelPricingEntry{usdEntry("dear", "0.1", "0.5"), usdEntry("cheap", "0.1", "0.4")},
			want:    "cheap",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Service{PriceDenomination: tc.denom, ModelPricing: tc.entries}
			if err := s.BuildModelPricingMap(); err != nil {
				t.Fatalf("BuildModelPricingMap: %v", err)
			}
			got := s.GetModelPricing("m")
			if got == nil {
				t.Fatal("GetModelPricing(m) = nil")
			}
			if got.ProviderIdentity != tc.want {
				t.Errorf("identity-less entry = %q, want %q", got.ProviderIdentity, tc.want)
			}
			// The resolution path and the pricing accessor must agree — they read the
			// same map, and a request billed at one entry must route to that entry.
			e, _, err := s.ResolveRequestedModel("m", "")
			if err != nil || e != got {
				t.Errorf("ResolveRequestedModel(m, \"\") = %+v, %v; want the same entry GetModelPricing returned", e, err)
			}
		})
	}
}

// TestIdentitylessAliasResolvesLikeTheCanonicalID guards the invariant the whole
// cheapest-entry mechanism rests on, on the path this PR newly opened: an alias
// request with no X-0G-Upstream must be admitted as the SAME entry every
// identity-agnostic accessor will re-resolve to.
//
// The alias is declared on the DEARER entry, which is also listed first. If
// resolution returned the declaring entry (the intuitive reading of "an alias is
// entry-scoped"), the request would be admitted as zhipu — and forwarded under
// zhipu's upstreamModel — while the route, the credential, the price and the
// expiry gate all came from aliyun. Aliyun would be asked for a model id it does
// not serve, at the wrong price.
func TestIdentitylessAliasResolvesLikeTheCanonicalID(t *testing.T) {
	s := &Service{
		TargetURL:        "https://svc.example.com/v1",
		ProviderIdentity: "svc",
		ModelPricing: []ModelPricingEntry{
			{Model: "glm-5.2", ProviderIdentity: "zhipu", InputPrice: "3", OutputPrice: "4",
				TargetURL: "https://zhipu.example.com/v1", UpstreamModel: "glm-4.6",
				ModelAliases:     []string{"glm-latest"},
				AdditionalSecret: map[string]string{"Authorization": "Bearer zhipu-key"}},
			{Model: "glm-5.2", ProviderIdentity: "aliyun", InputPrice: "1", OutputPrice: "2",
				TargetURL:        "https://aliyun.example.com/v1",
				AdditionalSecret: map[string]string{"Authorization": "Bearer aliyun-key"}},
		},
	}
	if err := s.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}

	entry, resolved, err := s.ResolveRequestedModel("glm-latest", "")
	if err != nil || entry == nil {
		t.Fatalf("resolve(glm-latest, \"\") err=%v entry=%v", err, entry)
	}
	if resolved != "glm-5.2" {
		t.Errorf("resolved = %q; want the canonical glm-5.2", resolved)
	}
	if entry.ProviderIdentity != "aliyun" {
		t.Fatalf("admitted entry = %q; want aliyun (the cheapest), not the alias declarer", entry.ProviderIdentity)
	}
	// What the admitted entry decides: the id forwarded upstream.
	if got := entry.UpstreamModelFor(); got != "glm-5.2" {
		t.Errorf("UpstreamModelFor = %q; want glm-5.2 — forwarding zhipu's glm-4.6 to aliyun asks it for a model it does not serve", got)
	}
	// What the accessors decide, all keyed off the canonical id with no identity.
	// Every one must name the same entry as the admission above.
	if got := s.EffectiveTargetURLFor(resolved, ""); got != "https://aliyun.example.com/v1" {
		t.Errorf("EffectiveTargetURLFor = %q; want aliyun's upstream", got)
	}
	if got := s.EffectiveAdditionalSecretFor(resolved, "")["Authorization"]; got != "Bearer aliyun-key" {
		t.Errorf("EffectiveAdditionalSecretFor = %q; want aliyun's key", got)
	}
	if got := s.GetModelPricingFor(resolved, ""); got != entry {
		t.Errorf("GetModelPricingFor returned a different entry than admission (%v vs %v)", got, entry)
	}
	if got := s.EffectiveProviderIdentityFor(resolved, ""); got != "aliyun" {
		t.Errorf("EffectiveProviderIdentityFor = %q; want aliyun", got)
	}
	// Naming an upstream explicitly still reaches it through the alias.
	e, _, err := s.ResolveRequestedModel("glm-latest", "zhipu")
	if err != nil || e == nil || e.ProviderIdentity != "zhipu" {
		t.Errorf("resolve(glm-latest, zhipu) = %v, %v; want the zhipu entry", e, err)
	}
}

// TestEqualPriceSiblingsPickIsOrderIndependent pins the tie-break. Two hosts of
// one vendor at ONE price is the natural capacity fan-out config, and it is
// exactly where an operator reorders the yaml; without a stable final key that
// reorder would silently move all header-less traffic — route, credential and
// provider_identity label — to the other host.
func TestEqualPriceSiblingsPickIsOrderIndependent(t *testing.T) {
	build := func(t *testing.T, first, second string) *ModelPricingEntry {
		t.Helper()
		s := &Service{ModelPricing: []ModelPricingEntry{
			{Model: "m", ProviderIdentity: first, InputPrice: "1", OutputPrice: "2"},
			{Model: "m", ProviderIdentity: second, InputPrice: "1", OutputPrice: "2"},
		}}
		if err := s.BuildModelPricingMap(); err != nil {
			t.Fatalf("BuildModelPricingMap: %v", err)
		}
		return s.GetModelPricing("m")
	}
	a := build(t, "zhipu", "aliyun")
	b := build(t, "aliyun", "zhipu")
	if a == nil || b == nil {
		t.Fatal("GetModelPricing(m) = nil")
	}
	if a.ProviderIdentity != b.ProviderIdentity {
		t.Errorf("reordering equal-priced siblings moved the pick: %q vs %q", a.ProviderIdentity, b.ProviderIdentity)
	}
	if a.ProviderIdentity != "aliyun" {
		t.Errorf("tie-break picked %q; want the lexicographically lowest identity (aliyun)", a.ProviderIdentity)
	}
}

// TestHasSiblingOutliving pins the gate on the cheapest-entry-expiry warning: it
// must fire when a sibling would still be servable past the cheapest entry's
// date, and stay quiet when the whole id is being retired — the very action the
// warning tells the operator to take instead. A warning that fires on the
// recommended fix trains operators to ignore it.
func TestHasSiblingOutliving(t *testing.T) {
	// Validate() is what parses ExpirationDate into the unexported expiresAt that
	// hasSiblingOutliving reads; loadConfig always runs it per entry, so a fixture
	// that skips it would not exercise the real shape.
	withExpiry := func(t *testing.T, identity, date string) ModelPricingEntry {
		t.Helper()
		e := ModelPricingEntry{Model: "m", ProviderIdentity: identity, InputPrice: "1", OutputPrice: "2"}
		if date != "" {
			mi := validModelInfo()
			mi.ExpirationDate = date
			if err := mi.Validate("chatbot"); err != nil {
				t.Fatalf("ModelInfo.Validate: %v", err)
			}
			e.ModelInfo = mi
		}
		return e
	}
	const (
		early = "2000-01-01T00:00:00Z"
		late  = "2100-01-01T00:00:00Z"
	)

	cases := []struct {
		name    string
		sibling ModelPricingEntry
		want    bool
	}{
		{"sibling has no expiry at all", withExpiry(t, "zzz-sibling", ""), true},
		{"sibling expires later", withExpiry(t, "zzz-sibling", late), true},
		{"whole id retired on the same date", withExpiry(t, "zzz-sibling", early), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// "aaa-cheapest" sorts first, so it wins the identity tie-break and is the
			// entry the warning is about regardless of slice order.
			s := &Service{ModelPricing: []ModelPricingEntry{tc.sibling, withExpiry(t, "aaa-cheapest", early)}}
			if err := s.BuildModelPricingMap(); err != nil {
				t.Fatalf("BuildModelPricingMap: %v", err)
			}
			cheapest := s.GetModelPricing("m")
			if cheapest == nil || cheapest.ProviderIdentity != "aaa-cheapest" {
				t.Fatalf("cheapest = %v; fixture no longer exercises what it claims", cheapest)
			}
			if got := s.hasSiblingOutliving(cheapest, s.effectiveModelInfoOf(cheapest)); got != tc.want {
				t.Errorf("hasSiblingOutliving = %v, want %v", got, tc.want)
			}
		})
	}

	// A single-entry model has no sibling to outlive it, so the warning stays quiet
	// there too — an expirationDate on the only entry is the intended retirement.
	solo := &Service{ModelPricing: []ModelPricingEntry{withExpiry(t, "solo", early)}}
	if err := solo.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}
	soloCheapest := solo.GetModelPricing("m")
	if solo.hasSiblingOutliving(soloCheapest, solo.effectiveModelInfoOf(soloCheapest)) {
		t.Error("hasSiblingOutliving = true on a single-entry model; want false")
	}
}

// TestExpiryWarningSeesServiceLevelModelInfo covers the shape that produces the
// header-less 410 outage most easily, and that a per-entry-only check misses
// entirely: the expirationDate lives on the SERVICE-level modelInfo, the cheapest
// entry has none of its own, and the dearer sibling carries its own modelInfo with
// no expiry. ModelExpirationFor falls back to the service-level ModelInfo, so the
// gate fires — the load-time check has to look through the same fallback.
func TestExpiryWarningSeesServiceLevelModelInfo(t *testing.T) {
	const early = "2000-01-01T00:00:00Z"

	svcMI := validModelInfo()
	svcMI.ExpirationDate = early
	if err := svcMI.Validate("chatbot"); err != nil {
		t.Fatalf("service ModelInfo.Validate: %v", err)
	}
	liveMI := validModelInfo() // no ExpirationDate
	if err := liveMI.Validate("chatbot"); err != nil {
		t.Fatalf("sibling ModelInfo.Validate: %v", err)
	}

	s := &Service{
		ModelInfo: svcMI,
		ModelPricing: []ModelPricingEntry{
			// Dearer sibling, live, with its own modelInfo.
			{Model: "m", ProviderIdentity: "zhipu", InputPrice: "3", OutputPrice: "4", ModelInfo: liveMI},
			// Cheapest, NO per-entry modelInfo — it inherits the service-level expiry.
			{Model: "m", ProviderIdentity: "aliyun", InputPrice: "1", OutputPrice: "2"},
		},
	}
	if err := s.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}
	cheapest := s.GetModelPricing("m")
	if cheapest == nil || cheapest.ProviderIdentity != "aliyun" {
		t.Fatalf("cheapest = %v; fixture no longer exercises what it claims", cheapest)
	}
	if cheapest.ModelInfo != nil {
		t.Fatal("fixture broken: the cheapest entry must have no per-entry ModelInfo")
	}

	// The gate really does 410 this id for a header-less caller...
	if _, ok := s.ModelExpiration("m"); !ok {
		t.Error("ModelExpiration(m) ok=false; the service-level expiry must gate the cheapest entry")
	}
	// ...while the sibling stays live when named.
	if _, ok := s.ModelExpirationFor("m", "zhipu"); ok {
		t.Error("ModelExpirationFor(m, zhipu) ok=true; that entry has its own expiry-free ModelInfo")
	}
	// So the load-time check must see it. A per-entry-only guard reads nil here.
	mi := s.effectiveModelInfoOf(cheapest)
	if mi == nil || mi.ExpirationDate != early {
		t.Fatalf("effectiveModelInfoOf(cheapest) = %v; want the service-level expiry %q", mi, early)
	}
	if !s.hasSiblingOutliving(cheapest, mi) {
		t.Error("hasSiblingOutliving = false; the live sibling outlives the cheapest entry, so the warning must fire")
	}
}
