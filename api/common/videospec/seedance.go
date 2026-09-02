package videospec

import (
	"math"
	"strings"
)

// VendorSeedance is ByteDance Seedance's video-generation API (BytePlus Ark).
const VendorSeedance Vendor = "seedance"

// Seedance is this vendor's rules as a concrete value — see MiniMax's
// counterpart for why a vendor's own mapper takes the concrete type.
var Seedance = seedance{}

func init() { register(VendorSeedance, Seedance) }

// seedance carries no state: its rules are the code below.
//
// This is the vendor the package doc names as the reason there is no shared
// "shape of a vendor's rules" struct — it bills in TOKENS, not seconds.
//
// What the recorded rules are used for:
//
//   - Bounding the fee BEFORE the request is forwarded. The vendor publishes how
//     its token count follows from the request, so EstimateBillableTokens — the
//     optional TokenEstimator half — turns duration and tier into an upper bound
//     the balance gate can hold. Without one, a token-billed create passes the gate
//     on the minimum locked balance alone, and concurrent creates from one wallet
//     cannot see each other at all.
//   - The TIER settlement records as rate_class. Without rules, settlement keeps
//     whatever "size" the client sent, so a client sending pixel dimensions puts
//     "1280x720" in the rate_class column of a table whose other rows say "720p" —
//     nothing can group a reconciliation by tier.
//   - Refusing an unpriceable duration BEFORE the vendor is called, instead of
//     clamping it to the ceiling and rendering the most expensive clip this model
//     can produce for a request that plainly asked for no such thing.
type seedance struct{}

// Seedance 2.5's duration range: an integer in [4,30]. The ceiling is confirmed
// live (31 is rejected, 30 accepted); the floor is carried over from 2.0.
//
// Both bounds CLAMP rather than reject, and in this vendor's case clamping is
// safe in a way it is not for MiniMax: billing is on the vendor's echoed token
// count, never on the requested duration, so a clamp cannot move the bill away
// from what was rendered. It only changes what gets generated.
const (
	SeedanceMinSeconds = 4
	SeedanceMaxSeconds = 30
)

// NormalizeSeconds reports the clip length Seedance will render.
//
// Deliberate deviation from Spec's "pass raw to your parser UNTRIMMED" rule, and
// the reason is the rule's own reason. That instruction exists so a reading here
// matches the VENDOR-SIDE reader — and for MiniMax/DashScope the raw string is
// forwarded to the vendor's parser, which does not trim, so trimming here would
// resolve a duration the vendor would not.
//
// Seedance's vendor-side reader is not the vendor at all: this integration's
// translator parses "seconds" itself and sends the vendor a JSON INTEGER it
// already normalized (see translate.parseSeedanceDuration, which calls straight
// into this function). The vendor never sees the client's string, so the reader
// this must agree with is the translator's — and it trims. Trimming here is what
// keeps the two in agreement; not trimming would be the divergence.
func (seedance) NormalizeSeconds(raw string) (int64, SecondsOutcome) {
	f, ok, rejected := ParseSeconds(strings.TrimSpace(raw))
	if rejected {
		return 0, SecondsRejected
	}
	if !ok {
		// Seedance treats duration as optional: an unreadable one is OMITTED from
		// the vendor call and the vendor applies its own default (5s), so the
		// rendered length is not determined by the request. Same shape as
		// DashScope, and inventing 5 here would be hardcoding a vendor default
		// that is not ours to promise.
		return 0, SecondsVendorDecides
	}
	// Clamp the FLOAT before converting, for the reason spelled out in MiniMax's
	// counterpart: converting an out-of-range float first is
	// implementation-defined, lands below the floor, and is then clamped UP —
	// turning the most absurd request into the shortest clip.
	if f > SeedanceMaxSeconds {
		f = SeedanceMaxSeconds
	}
	// Ceil: the vendor takes an integer, and a fractional request yields the next
	// whole second of rendered output.
	d := int64(math.Ceil(f))
	if d < SeedanceMinSeconds {
		d = SeedanceMinSeconds
	}
	return d, SecondsResolved
}

// seedanceResolutionTokens is the SET of "size" values Seedance reads as a
// resolution tier — a list, not a mapping, because the canonical spelling is the
// element itself (lowercase, as the vendor spells it).
//
// 1080p was NOT here originally: an early live probe had it rejected with
// InvalidParameter, and the vendor has since opened it (its published rate card
// now prices a 1080p row for dreamina-seedance-2-5-260628). 4k stays out —
// still rejected, and unlike MiniMax's list this one holds no forward-looking
// entries: being recognised here means being FORWARDED, so a tier is added the
// day the vendor serves it and not before.
var seedanceResolutionTokens = []string{"480p", "720p", "1080p"}

// SeedanceDefaultTier is what this integration sends when the request names no
// recognisable tier.
//
// Unlike MiniMax's, this default exists because the vendor call must carry an
// EXACT token — the vendor validates strictly, so omitting the field is not an
// option, and there is no "let the vendor choose" case for resolution the way
// there is for duration.
const SeedanceDefaultTier = "720p"

// seedanceTierMaxSides is each tier's documented longer-side pixel count, for
// nearest-match snapping.
//
// Nearest-match, NOT a fixed cutover, and that is a money decision rather than a
// stylistic one: a naive "<=640 is 480p, else 720p" threshold misclassifies this
// codebase's own documented standard 480p size — 832x480, longer side 832 — as
// 720p, billing a client who asked for the cheap tier at the expensive one.
//
// Order is load-bearing on an exact tie (longer side 1056 is equidistant from
// 480p and 720p, 1600 from 720p and 1080p): the first entry wins, so a tie
// snaps DOWN to the cheaper tier, and a reordering silently reprices it. Pinned
// by a test for exactly that reason.
var seedanceTierMaxSides = []struct {
	token   string
	maxSide float64
}{
	{"480p", 832},
	{"720p", 1280},
	{"1080p", 1920},
}

// ResolutionToken reports whether a "size" is one of this vendor's tier tokens,
// returning the canonical spelling if so and "" if not — see the MiniMax sibling
// for why the "" answer is the load-bearing one.
func (seedance) ResolutionToken(size string) string {
	s := strings.TrimSpace(size)
	for _, tok := range seedanceResolutionTokens {
		if strings.EqualFold(tok, s) {
			return tok
		}
	}
	return ""
}

// Tier reports the tier Seedance will render at. Unlike its two siblings this
// NEVER returns "": the vendor call must name an exact token, so an unrecognisable
// size resolves to SeedanceDefaultTier rather than leaving the choice open.
//
// A pixel-dimension size snaps to the nearest tier by longer side; a token
// addresses one directly.
func (s seedance) Tier(size string) string {
	if tok := s.ResolutionToken(size); tok != "" {
		return tok
	}
	w, h, ok := ParsePixelSize(size)
	if !ok {
		return SeedanceDefaultTier
	}
	maxSide := float64(w)
	if float64(h) > maxSide {
		maxSide = float64(h)
	}
	best := SeedanceDefaultTier
	bestDiff := math.MaxFloat64
	for _, r := range seedanceTierMaxSides {
		if diff := math.Abs(r.maxSide - maxSide); diff < bestDiff {
			bestDiff = diff
			best = r.token
		}
	}
	return best
}

// The vendor publishes both halves of what this needs: a formula,
//
//	tokens = (input video duration + output video duration) × width × height × fps / 1024
//
// and a per-second price for each resolution tier, which is the formula already
// evaluated for us. Dividing the published rate by the published per-token rate
// gives the only numbers this file actually wants:
//
//	Dreamina Seedance 2.5, input without video, 16:9:
//	  480p  $0.103/s ÷ $10.7037 per 1M  =  9,626 tokens/s
//	  720p  $0.231/s ÷ $10.7037 per 1M  = 21,590 tokens/s
//
// Taking them from the price table rather than from the formula is deliberate: the
// formula needs each tier's rendered pixel count, which the vendor does NOT
// publish and which changed between 2.0 and 2.5. Reconstructing it means picking a
// frame size from third-party measurements that disagree — a guess this file would
// then carry as if it were a fact. The price table needs no such guess.
//
// (For the record, the two routes agree: 1280×720×24/1024 = 21,600, within 0.05%
// of the 21,590 above. The residual is the price table's three decimals.)
//
// This integration exposes no video-reference input, so the formula's input term
// is 0 and both numbers are pure output-duration rates. IF THAT CHANGES — if a
// video-reference input is ever exposed — these become severe UNDER-estimates,
// silently, because the term they drop is the one that would grow. Whoever exposes
// it must update this together with the mapping.
const (
	// seedance480pTokensPerSecond and seedance720pTokensPerSecond: see above.
	seedance480pTokensPerSecond = 9626
	seedance720pTokensPerSecond = 21590
	// seedance1080pTokensPerSecond comes from the FORMULA, not the price table,
	// because the vendor publishes no per-second figure for this tier — only its
	// per-1M-token rate (11.70 without video input, against 10.70 for the two
	// tiers above). 1920x1080x24/1024 = 48,600 exactly.
	//
	// That is the guess the comment above avoids for the other two, so it is worth
	// saying why it is tolerable here: the same formula, run on 720p's documented
	// 1280x720, gives 21,600 against the price-derived 21,590 — 0.05% out. The
	// only free variable is the rendered frame size, and 1080p is 1920x1080 by
	// definition of the tier. Replace this with a price-derived figure the day the
	// vendor publishes a 1080p per-second price.
	seedance1080pTokensPerSecond = 48600
)

// seedanceTokensPerSecond is the billable token rate for each tier.
//
// An ESTIMATE, not a bound, and the difference matters for what consumes it. The
// balance gate does not need a number that can never be exceeded — it needs one
// close enough that concurrent creates from one wallet see each other, with the
// minimum locked balance absorbing the rest. Being 4% out costs nothing there.
//
// What DOES depend on the number being roughly right is the drift check
// (ctrl.WarnVideoTokenEstimateDrift), and it compares with a tolerance for exactly
// this reason: a check that fired on every 0.1% of rounding would report nothing
// anyone could act on.
//
// Keyed on the tier alone. The vendor's own table supports that — it prices per
// RESOLUTION, not per (resolution, ratio), and a vendor whose 21:9 clips cost 31%
// more per second could not price that way. Weak support, since the table states
// 16:9 in its aspect-ratio column and may just be showing one shape; if it turns
// out a tier's rate varies by ratio, the fix is a different key rather than a
// bigger number, and the drift check is what would say so. An image-to-video
// request is the case no key could fix anyway: ratio="adaptive" hands the shape to
// the reference image, so nothing in the request determines it.
var seedanceTokensPerSecond = map[string]int64{
	"480p":  seedance480pTokensPerSecond,
	"720p":  seedance720pTokensPerSecond,
	"1080p": seedance1080pTokensPerSecond,
}

// EstimateBillableTokens implements TokenEstimator: the tier's per-second rate
// times the duration this vendor will actually render.
//
// ok=false when the request determines no duration (an unreadable one is omitted
// and the vendor picks, so nothing here can estimate it) or when the tier is one
// this table does not cover — never a zero estimate, which would read as "free"
// and disable the gate it feeds.
func (s seedance) EstimateBillableTokens(rawSeconds, rawSize string) (int64, bool) {
	seconds, outcome := s.NormalizeSeconds(rawSeconds)
	if outcome != SecondsResolved || seconds <= 0 {
		return 0, false
	}
	perSecond, ok := seedanceTokensPerSecond[s.Tier(rawSize)]
	if !ok {
		return 0, false
	}
	// No overflow guard, and that is not an omission: seconds is bounded by
	// SeedanceMaxSeconds (30) and the rate by the table above, so the product is at
	// most 30 × 48,600 ≈ 1.5e6 — far inside int64.
	return seconds * perSecond, true
}

// Duration states the bounds NormalizeSeconds applies above: both clamp, and an
// unreadable value is omitted so the vendor picks the length itself.
func (seedance) Duration() DurationSpec {
	return DurationSpec{
		Min:         SeedanceMinSeconds,
		Max:         SeedanceMaxSeconds,
		OutOfRange:  OutOfRangeClamp,
		Unspecified: UnspecifiedVendorDefault,
		Rounding:    RoundingCeil,
	}
}

// Resolution states the tier vocabulary Tier resolves against. Pixel dimensions
// select a tier here (nearest by longer side), unlike MiniMax.
func (seedance) Resolution() ResolutionSpec {
	return ResolutionSpec{
		Tiers:     copyTokens(seedanceResolutionTokens),
		Default:   SeedanceDefaultTier,
		PixelSize: PixelSizeSelectsTier,
	}
}
