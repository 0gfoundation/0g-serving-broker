package providercontract

import (
	"context"
	"testing"
)

// GetImageInfo is the broker's only statement about which image it runs, and the
// contract un-acknowledges the provider's TEE signer whenever the pair changes.
// A half-populated environment must therefore read as "unknown" and not as a
// change: buildAdditionalInfo leaves the on-chain fields alone for ("", ""), and
// leaving them alone is what a deployment mid-rollout needs.
//
// The method reads no field of ProviderContract, so a zero value is the whole
// fixture.
func TestGetImageInfoFromEnv(t *testing.T) {
	const (
		repo   = "ghcr.io/0gfoundation/0g-serving-broker"
		digest = "sha256:0f2c1f4e9a5b6c7d8e9f0a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f"
	)

	tests := []struct {
		name                 string
		repoEnv, digestEnv   string
		wantName, wantDigest string
	}{
		{
			name:       "both set",
			repoEnv:    repo,
			digestEnv:  digest,
			wantName:   repo,
			wantDigest: digest,
		},
		{
			// The shape a botched recreate produces. Reporting the repo with an
			// empty digest would write a real image change on-chain out of a
			// missing variable.
			name:    "digest missing",
			repoEnv: repo,
		},
		{
			name:      "repo missing",
			digestEnv: digest,
		},
		{
			// Every deployment before this change, and the controller-disabled
			// ones after it. Must answer exactly as the removed docker lookup did
			// with no socket configured.
			name: "neither set",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(envImageRepo, tt.repoEnv)
			t.Setenv(envImageDigest, tt.digestEnv)

			name, digest := (&ProviderContract{}).GetImageInfo(context.Background())
			if name != tt.wantName || digest != tt.wantDigest {
				t.Errorf("GetImageInfo() = (%q, %q), want (%q, %q)",
					name, digest, tt.wantName, tt.wantDigest)
			}
		})
	}
}
