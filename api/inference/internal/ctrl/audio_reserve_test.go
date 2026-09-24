package ctrl

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/audiospec"
	"github.com/0glabs/0g-serving-broker/inference/config"
)

// newAudioReserveTestCtrl builds a multi-model audio Ctrl whose single model names
// the given vendor, priced natively (1000 neuron per generated second) so no rate
// feed is involved.
func newAudioReserveTestCtrl(t *testing.T, vendor string) (*Ctrl, *gin.Context) {
	t.Helper()
	c := &Ctrl{logger: testLogger()}
	c.Service.Type = "audio-generation"
	c.Service.ModelType = "seed-audio-1.0"
	c.Service.ModelPricing = []config.ModelPricingEntry{{
		Model:       "seed-audio-1.0",
		OutputPrice: "1000",
		Billing: &config.BillingConfig{
			Mode:   config.BillingModePerAudioSecond,
			Vendor: vendor,
		},
	}}
	if err := c.Service.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}
	ctx := &gin.Context{}
	ctx.Set(CtxKeyResolvedModel, "seed-audio-1.0")
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/proxy/audio/speech", strings.NewReader(""))
	ctx.Request.Header.Set("Content-Type", "application/json")
	return c, ctx
}

// The reserve is the vendor's whole ceiling, whatever the request says. A body
// asking for 10 seconds used to reserve 10, but Seed Audio takes no length
// parameter — the request could still produce, and be billed for, 120 — so the
// gate was holding a twelfth of what it let the caller spend.
func TestAudioCreateReserve_IsTheCeilingWhateverTheRequestSays(t *testing.T) {
	c, ctx := newAudioReserveTestCtrl(t, string(audiospec.VendorSeedAudio))
	const want = "120000" // 120 s × 1000 neuron/s

	for _, body := range []string{
		`{"model":"seed-audio-1.0","input":"hi"}`,
		`{"model":"seed-audio-1.0","input":"hi","max_duration":10}`,
		`{"model":"seed-audio-1.0","input":"hi","max_duration":0.5}`,
		`{"model":"seed-audio-1.0","input":"hi","max_duration":"10"}`,
		`{"model":"seed-audio-1.0","input":"hi","max_duration":1e300}`,
		`not json at all`,
	} {
		got, err := c.AudioCreateReserve(ctx, []byte(body))
		if err != nil {
			t.Fatalf("AudioCreateReserve(%s): %v", body, err)
		}
		if got != want {
			t.Errorf("AudioCreateReserve(%s) = %s, want the ceiling fee %s", body, got, want)
		}
	}
}

// Vendor lookup folds case and whitespace, so an operator's spelling in config
// cannot silently turn the reserve off.
func TestAudioCreateReserve_VendorSpellingDoesNotMatter(t *testing.T) {
	c, ctx := newAudioReserveTestCtrl(t, "  SeedAudio ")
	got, err := c.AudioCreateReserve(ctx, []byte(`{"input":"hi"}`))
	if err != nil {
		t.Fatalf("AudioCreateReserve: %v", err)
	}
	if got != "120000" {
		t.Errorf("reserve = %s, want 120000 — a differently-spelled vendor disabled the gate", got)
	}
}

// The one case with no reserve: no rules recorded for the vendor. It goes out
// unreserved (and metered), not at a guessed ceiling.
func TestAudioCreateReserve_UnknownVendorForwardsUnreserved(t *testing.T) {
	for _, vendor := range []string{"", "no-such-vendor"} {
		c, ctx := newAudioReserveTestCtrl(t, vendor)
		got, err := c.AudioCreateReserve(ctx, []byte(`{"input":"hi"}`))
		if err != nil {
			t.Fatalf("vendor %q: AudioCreateReserve: %v", vendor, err)
		}
		if got != "0" {
			t.Errorf("vendor %q: reserve = %s, want 0 — an unrecorded vendor must not be reserved at a guessed ceiling", vendor, got)
		}
	}
}

// An empty body is rejected upstream and generates nothing, so nothing is held.
func TestAudioCreateReserve_EmptyBodyReservesNothing(t *testing.T) {
	c, ctx := newAudioReserveTestCtrl(t, string(audiospec.VendorSeedAudio))
	got, err := c.AudioCreateReserve(ctx, nil)
	if err != nil || got != "0" {
		t.Errorf("AudioCreateReserve(empty) = (%q, %v), want (\"0\", nil)", got, err)
	}
}
