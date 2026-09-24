package audiospec

import "testing"

func TestSeedAudioMaxOutputSeconds(t *testing.T) {
	if got := SeedAudio.MaxOutputSeconds(); got != 120 {
		t.Fatalf("MaxOutputSeconds() = %d, want 120 (Seed Audio 1.0's published per-request ceiling)", got)
	}
}

// The registry must resolve to this vendor's rules, not just hold them. A
// registration that never resolves leaves the gate degrading to unreserved
// forwarding while every file in the package looks correct.
func TestSeedAudioIsReachableThroughTheRegistry(t *testing.T) {
	spec, ok := Get(VendorSeedAudio)
	if !ok {
		t.Fatal("Get(VendorSeedAudio) found no rules")
	}
	if spec.MaxOutputSeconds() != SeedAudio.MaxOutputSeconds() {
		t.Error("the registry resolved to a different spec than the exported value")
	}
}
