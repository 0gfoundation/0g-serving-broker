package videospec

import (
	"math"
	"strings"
)

// VendorMiniMax is MiniMax's video-generation API (H3 and successors).
const VendorMiniMax Vendor = "minimax"

// MiniMax is this vendor's rules as a concrete value. Get() hands out the Spec
// interface for callers that work across vendors; this is for MiniMax's own
// translate mapper, which is entitled to the whole of its own vendor's surface
// (ResolutionToken, the bounds) rather than just the shared contract.
var MiniMax = miniMax{}

func init() { register(VendorMiniMax, MiniMax) }

// miniMax carries no state: its rules are the code below. They are written out
// rather than filled into a shared struct because they are not the same SHAPE as
// any other vendor's — see the package doc.
type miniMax struct{}

// H3's duration is an integer in [4,15]. Confirmed live against
// api.minimax.io/v2/video_generation, not read off the published description: 3
// and 16 are rejected with the vendor enumerating exactly 4s..15s, integers only.
// The published description once said 5s was the floor; it is 4 — which is why
// this is observed rather than documented.
//
// An absent or unreadable duration renders the 4s floor. That is also OpenAI's
// own documented default (its seconds enum is {4,8,12}), so the most common call
// shape is the cheapest one and nothing is clamped upward behind the caller's
// back.
//
// The floor matters for money in the other direction too: a request BELOW it is
// rendered — and billed — at 4s. A reader that ignored it would resolve 1 and
// under-count by 75%.
const (
	MiniMaxMinSeconds = 4
	MiniMaxMaxSeconds = 15
)

func (miniMax) NormalizeSeconds(raw string) (int64, SecondsOutcome) {
	f, ok, rejected := ParseSeconds(raw)
	if rejected {
		return 0, SecondsRejected
	}
	if !ok {
		// H3 requires a duration, so the vendor never gets to choose: an
		// unreadable one renders the floor. SecondsVendorDecides cannot happen here.
		return MiniMaxMinSeconds, SecondsResolved
	}
	// Clamp the FLOAT before converting. Converting first would be
	// implementation-defined for an out-of-range value, landing below the floor
	// and being clamped UP to it — silently turning the longest possible request
	// into the shortest clip. (ParseSeconds has already refused the values where
	// that is reachable; the order is kept because it is what makes the refusal
	// unnecessary rather than merely redundant.)
	if f > MiniMaxMaxSeconds {
		f = MiniMaxMaxSeconds
	}
	// Ceil: H3 takes an integer, and a fractional request yields the next whole
	// second of rendered output.
	d := int64(math.Ceil(f))
	if d < MiniMaxMinSeconds {
		d = MiniMaxMinSeconds
	}
	return d, SecondsResolved
}

// miniMaxResolutionTokens is the SET of "size" values MiniMax reads as a
// resolution tier — a list, not a mapping, because that is all it is: the
// canonical spelling is the element itself.
//
// It covers non-H3 models too: H3 itself accepts only 2K (the same live probe
// established that), so 768P/1080P here will be REJECTED by H3. That is
// deliberate — being recognised means being forwarded and refused loudly, while
// going unrecognised would mean being read as pixel dimensions and rendered at
// the deployment's own tier, with the caller paying for a tier they did not ask
// for and never told.
var miniMaxResolutionTokens = []string{"512P", "720P", "768P", "1080P", "2K", "4K"}

// ResolutionToken reports whether a "size" is one of this vendor's tier tokens,
// returning the canonical spelling if so and "" if not.
//
// The "" answer is what callers actually need: "size" carries either pixel
// dimensions or a tier, and everything downstream — which code path, which price
// row — depends on telling them apart. The canonical spelling is a smaller
// benefit, and only reaches one place that cares: the value sent upstream, where
// it avoids betting on how tolerant the vendor's own parser is.
//
// On the concrete type, not in Spec: a tier vocabulary is this vendor's notion,
// and putting it in the contract would decide for vendors that have none.
func (miniMax) ResolutionToken(size string) string {
	s := strings.TrimSpace(size)
	for _, tok := range miniMaxResolutionTokens {
		if strings.EqualFold(tok, s) {
			return tok
		}
	}
	return ""
}

// MiniMaxDefaultTier is what H3 renders when the request names no tier.
//
// It is a constant rather than a deployment setting because H3 serves exactly one
// resolution — confirmed live: it rejects 768P while naming 2K as its whole
// supported set. There was never a choice to configure.
//
// It used to be one anyway: the translator read MINIMAX_RESOLUTION (defaulting to
// this same value, and commented out in the shipped compose file) while the
// broker was told the tier again through its own config. Two copies of one fact,
// in different files in different containers, with nothing keeping them equal —
// and a mismatch prices every request at a tier the vendor never renders, in
// silence, forever. Stating the fact once removes the failure instead of watching
// for it.
//
// A MiniMax model that serves several tiers would need this per model rather than
// per vendor; it would not need it back as an operator setting.
const MiniMaxDefaultTier = "2K"

// Tier: a pixel-dimension "size" does NOT select a tier for MiniMax. It only sets
// the aspect ratio. Only a token addresses a tier directly — and deriving one
// from pixels would produce a value H3 rejects, since it serves a single tier.
func (m miniMax) Tier(size string) string {
	if tok := m.ResolutionToken(size); tok != "" {
		return tok
	}
	return MiniMaxDefaultTier
}

// Duration states the bounds NormalizeSeconds applies above. An unreadable value
// renders the FLOOR here — H3 requires a duration, so the vendor never chooses.
func (miniMax) Duration() DurationSpec {
	return DurationSpec{
		Min:         MiniMaxMinSeconds,
		Max:         MiniMaxMaxSeconds,
		OutOfRange:  OutOfRangeClamp,
		Unspecified: UnspecifiedMin,
		Rounding:    RoundingCeil,
	}
}

// Resolution states the tier vocabulary Tier resolves against. Pixel dimensions
// do NOT select a tier here — see Tier — so a caller sending "1280x720" to a
// 2K-default model gets, and pays for, a 2K clip.
func (miniMax) Resolution() ResolutionSpec {
	return ResolutionSpec{
		Tiers:     copyTokens(miniMaxResolutionTokens),
		Default:   MiniMaxDefaultTier,
		PixelSize: PixelSizeAspectRatioOnly,
	}
}
