package videospec

import (
	"math"
	"strings"
)

// VendorKling is Kling's video-generation API (Aliyun Bailian / model-studio,
// `kling/kling-v3-video-generation`).
const VendorKling Vendor = "kling"

// Kling is this vendor's rules as a concrete value — see MiniMax's counterpart
// for why a vendor's own mapper takes the concrete type.
var Kling = kling{}

func init() { register(VendorKling, Kling) }

// kling carries no state: its rules are the code below. Kling bills per
// SECOND (usage.duration, the vendor's billed quantity) like MiniMax and
// DashScope, NOT per token like Seedance — so it implements only Spec, not
// the optional TokenEstimator half.
type kling struct{}

// Kling's documented duration range: an integer in [3,15], default 5 when the
// field is omitted. Both bounds CLAMP rather than reject: billing is on the
// vendor-reported usage.duration (the actually-rendered length), never on the
// requested duration, so a clamp cannot move the bill away from what was
// rendered — same reasoning as Seedance's identical clamp-not-reject choice,
// and unlike MiniMax H3 (which REQUIRES a duration and so forces a floor
// rather than letting the vendor apply its own default).
const (
	KlingMinSeconds = 3
	KlingMaxSeconds = 15
)

// NormalizeSeconds reports the clip length Kling will render.
//
// Kling treats duration as OPTIONAL on the wire (parameters.duration): an
// unreadable value is OMITTED from the vendor call and the vendor applies its
// own documented default (5s), so the rendered length is not determined by
// the request at all — SecondsVendorDecides, the same shape DashScope and
// Seedance use, and unlike MiniMax's forced floor.
func (kling) NormalizeSeconds(raw string) (int64, SecondsOutcome) {
	f, ok, rejected := ParseSeconds(raw)
	if rejected {
		return 0, SecondsRejected
	}
	if !ok {
		return 0, SecondsVendorDecides
	}
	// Clamp the FLOAT before converting, for the reason spelled out in
	// MiniMax's/Seedance's counterparts: converting an out-of-range float
	// first is implementation-defined, can land below the floor, and would
	// then be clamped UP — turning the most absurd request into the shortest
	// clip.
	if f > KlingMaxSeconds {
		f = KlingMaxSeconds
	}
	// Ceil: the vendor takes an integer, and a fractional request yields the
	// next whole second of rendered output.
	d := int64(math.Ceil(f))
	if d < KlingMinSeconds {
		d = KlingMinSeconds
	}
	return d, SecondsResolved
}

// klingResolutionTokens is the SET of "size" values Kling reads as a
// resolution tier — a list, not a mapping, because the canonical spelling is
// the element itself: Kling's own wire vocabulary for parameters.mode
// ("std" = 720p, "pro" = 1080p).
var klingResolutionTokens = []string{"std", "pro"}

// ResolutionToken reports whether a "size" is one of this vendor's tier
// tokens, returning the canonical spelling if so and "" if not — see the
// MiniMax sibling for why the "" answer is the load-bearing one.
func (kling) ResolutionToken(size string) string {
	s := strings.TrimSpace(size)
	for _, tok := range klingResolutionTokens {
		if strings.EqualFold(tok, s) {
			return tok
		}
	}
	return ""
}

// klingPixelThreshold is the larger-side pixel count at or below which a
// pixel-dimension size snaps to "std" (720p); above it, "pro" (1080p). Kling
// serves only these two tiers (a coarse enum, like DashScope's HappyHorse),
// so a fixed threshold — not Seedance's nearest-match snapping, which exists
// specifically to avoid misclassifying an intermediate documented size this
// vendor has no such size to misclassify — is the right shape here. 1280 is
// where OpenAI's own documented video sizes split cleanly, mirroring
// DashScope's identical threshold and reasoning.
const klingPixelThreshold = 1280

// Tier reports the tier Kling will render at, or "" when the request
// determines none. Unlike Seedance (whose vendor call must always carry an
// exact token), Kling's `mode` parameter is OPTIONAL on the wire and the
// vendor documents "pro" as its own default when omitted — so an unparsable
// or empty size yields "" here (mirrors DashScope's Tier), letting the
// translator omit `parameters.mode` entirely rather than guessing.
func (k kling) Tier(size string) string {
	if tok := k.ResolutionToken(size); tok != "" {
		return tok
	}
	w, h, ok := ParsePixelSize(size)
	if !ok {
		return ""
	}
	maxSide := w
	if h > maxSide {
		maxSide = h
	}
	if maxSide > klingPixelThreshold {
		return "pro"
	}
	return "std"
}
