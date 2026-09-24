package audiospec

import "testing"

func TestGetIsCaseAndWhitespaceInsensitive(t *testing.T) {
	for _, name := range []string{"seedaudio", "SeedAudio", "  SEEDAUDIO  "} {
		if _, ok := Get(Vendor(name)); !ok {
			t.Errorf("Get(%q) found no rules; lookup must not depend on how an operator spelled the vendor", name)
		}
	}
}

// A vendor nobody has recorded rules for must report ok=false, never a zero Spec.
// A caller that cannot look up a ceiling has to degrade explicitly (forward
// unreserved and count it); silently handing back a usable-looking value would
// size every reservation for that vendor at whatever the zero value implies.
func TestGetUnknownVendor(t *testing.T) {
	if _, ok := Get("no-such-vendor"); ok {
		t.Fatal("Get returned rules for an unregistered vendor")
	}
	if _, ok := Get(""); ok {
		t.Fatal("Get returned rules for the empty vendor")
	}
}

// withScratchRegistry swaps in an empty registry for one test and restores the
// real one after, so registration behaviour can be exercised without colliding
// with the vendors registered at init.
func withScratchRegistry(t *testing.T) {
	t.Helper()
	saved := specs
	specs = map[Vendor]Spec{}
	t.Cleanup(func() { specs = saved })
}

// register must store under the same spelling Get looks up. Before it normalized,
// a vendor file registering a mixed-case name was unreachable by every lookup —
// including the exact string it registered — because Get folded case and register
// did not.
func TestRegisterNormalizesTheName(t *testing.T) {
	withScratchRegistry(t)
	register(" MixedCase ", SeedAudio)
	for _, name := range []string{" MixedCase ", "MixedCase", "mixedcase"} {
		if _, ok := Get(Vendor(name)); !ok {
			t.Errorf("registered \" MixedCase \", but Get(%q) found nothing", name)
		}
	}
}

func TestRegisterRejectsEmptyVendor(t *testing.T) {
	for _, name := range []Vendor{"", "   "} {
		t.Run(string(name), func(t *testing.T) {
			withScratchRegistry(t)
			defer func() {
				if recover() == nil {
					t.Fatalf("register accepted the empty vendor name %q", name)
				}
			}()
			register(name, SeedAudio)
		})
	}
}

// Two files claiming one vendor is a merge accident, and letting the last
// registration win would make the outcome depend on file order — with the losing
// rules silently sizing every reservation. Checked on the normalized name, so two
// spellings of one vendor collide too.
func TestRegisterRejectsDuplicate(t *testing.T) {
	for _, second := range []Vendor{VendorSeedAudio, " SeedAudio "} {
		t.Run(string(second), func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("register accepted %q as a second registration of %q", second, VendorSeedAudio)
				}
			}()
			register(second, SeedAudio)
		})
	}
}

// Every registered vendor's ceiling must be positive. It is the reserve for every
// request, so a zero would hold nothing and disable the balance gate for that
// vendor — N simultaneous creates would each read 0 and every one pass against the
// same balance. Run across the registry so a vendor added later cannot skip it.
func TestEveryVendorHasAPositiveCeiling(t *testing.T) {
	if len(specs) == 0 {
		t.Fatal("no vendors registered; the reservation gate has nothing to size")
	}
	for vendor, spec := range specs {
		if max := spec.MaxOutputSeconds(); max <= 0 {
			t.Errorf("%s: MaxOutputSeconds() = %d, must be positive", vendor, max)
		}
	}
}
