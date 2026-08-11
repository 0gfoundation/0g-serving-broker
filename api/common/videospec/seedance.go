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
// "shape of a vendor's rules" struct — it bills in TOKENS, not seconds. That
// changes what registering it buys, and it is worth being precise about, because
// the obvious expectation is wrong: it does NOT produce a pre-flight reserve. A
// per_video_token model's fee is a vendor-computed token count that no reading of
// the request can predict, so the broker still forwards those creates unreserved
// (metered as reason=unpredictable_units — see monitor.VideoReserveSkipUnpredictableUnits).
//
// What it does buy is the other two things the recorded rules are used for:
//
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
// 2.5 serves ONLY these two: 1080p and 4k are live-confirmed rejected with
// InvalidParameter, so unlike MiniMax's list this one holds no forward-looking
// entries. A future model that serves more would need its own list, not an
// addition to this one — being recognised here means being forwarded.
var seedanceResolutionTokens = []string{"480p", "720p"}

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
// both): the first entry wins, so a reordering changes which tier a tied size
// snaps to. Pinned by a test for exactly that reason.
var seedanceTierMaxSides = []struct {
	token   string
	maxSide float64
}{
	{"480p", 832},
	{"720p", 1280},
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
