package providercontract

import (
	"encoding/json"
	"testing"

	"github.com/0glabs/0g-serving-broker/inference/config"
)

// TestBuildAdditionalInfo_ProviderTypeGating verifies the on-chain additionalInfo
// provider-class gating: ProviderType is published for every forwarder
// (centralized and standard) so on-chain discovery / SDK can identify the class,
// while ProviderIdentity (which reveals the upstream) stays centralized-only and a
// standard provider therefore never leaks it. Decentralized publishes neither.
func TestBuildAdditionalInfo_ProviderTypeGating(t *testing.T) {
	cases := []struct {
		name            string
		service         config.Service
		wantType        string // "" means the key must be absent
		wantIdentity    string // "" means the key must be absent
		wantIdentityKey bool   // whether ProviderIdentity key should be present
	}{
		{
			name:            "centralized publishes type and identity",
			service:         config.Service{ProviderType: "centralized", ProviderIdentity: "openai"},
			wantType:        "centralized",
			wantIdentity:    "openai",
			wantIdentityKey: true,
		},
		{
			name:            "standard publishes type but hides identity",
			service:         config.Service{ProviderType: "standard", ProviderIdentity: "openai"},
			wantType:        "standard",
			wantIdentityKey: false,
		},
		{
			name:            "decentralized publishes neither",
			service:         config.Service{ProviderType: "decentralized"},
			wantType:        "",
			wantIdentityKey: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := buildAdditionalInfo(tc.service, "", "", config.TieredPricingConfig{}, config.CacheTokenBillingConfig{})
			if err != nil {
				t.Fatalf("buildAdditionalInfo: %v", err)
			}
			var info map[string]interface{}
			if err := json.Unmarshal([]byte(raw), &info); err != nil {
				t.Fatalf("unmarshal additionalInfo: %v", err)
			}

			gotType, typeExists := info["ProviderType"]
			if tc.wantType == "" {
				if typeExists {
					t.Errorf("ProviderType should be absent, got %v", gotType)
				}
			} else {
				if gotType != tc.wantType {
					t.Errorf("ProviderType = %v, want %q", gotType, tc.wantType)
				}
			}

			gotIdentity, identityExists := info["ProviderIdentity"]
			if !tc.wantIdentityKey {
				if identityExists {
					t.Errorf("ProviderIdentity should be absent (upstream hidden), got %v", gotIdentity)
				}
			} else if gotIdentity != tc.wantIdentity {
				t.Errorf("ProviderIdentity = %v, want %q", gotIdentity, tc.wantIdentity)
			}
		})
	}
}

// TestIsServiceNotFoundMessage verifies that the anchored match correctly
// classifies the RPC "service not found" sentinel and rejects sibling
// messages where the phrase appears embedded inside a longer, unrelated
// error.  False-positive classification here would cause GetService to
// return ErrServiceNotFound for a transient RPC failure, which in turn
// would drive SyncServicePrices into an unintended first-time
// registration path.
func TestIsServiceNotFoundMessage(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		{"bare sentinel", "service not found", true},
		{"revert wrapped", "execution reverted: service not found", true},
		{"multi-prefix wrapped", "contract call failed: execution reverted: service not found", true},
		{"empty", "", false},
		{"unrelated", "internal server error", false},
		{"embedded substring at start", "service not found path is invalid", false},
		{"embedded substring in middle", "nested service not found path", false},
		{"embedded substring no colon prefix", "the service not found", false},
		{"trailing space is not the sentinel", "service not found ", false},
		{"similar but different phrase", "service not funded", false},
		{"colon without space prefix", "x:service not found", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isServiceNotFoundMessage(tc.msg)
			if got != tc.want {
				t.Errorf("isServiceNotFoundMessage(%q) = %v, want %v", tc.msg, got, tc.want)
			}
		})
	}
}
