package videospec

import (
	"math"
	"strings"
)

// VendorSeedance is ByteDance Seedance's video-generation API (BytePlus Ark).
//
// This constant names the 2.5 wire model family specifically (see
// VendorSeedance20 below for 2.0): both share the same client
// (videotranslator/internal/seedance), the same wire request/response shapes,
// and the same async create+poll protocol, differing only in the
// duration/resolution/pricing RULES this file records — so both are
// implemented by the same seedance Go type, distinguished by which profile a
// given value carries (see that type's doc).
const VendorSeedance Vendor = "seedance"

// Seedance is 2.5's rules as a concrete value — see MiniMax's counterpart for
// why a vendor's own mapper takes the concrete type. Its zero-value profile
// (nil) means "use this file's original, 2.5-specific package-level
// constants below" — see the seedance type's doc for why that indirection
// exists instead of a plain second copy of this file.
var Seedance = seedance{}

func init() { register(VendorSeedance, Seedance) }

// seedance carries one thing: which version's rules to apply. A nil profile
// (Seedance, 2.5) reads the original package-level constants declared right
// below this type — untouched since before this field existed, so 2.5's
// behavior is provably unchanged. A non-nil profile (Seedance20) reads its
// own set instead. Every method below branches on this once, through the
// small accessor methods further down, rather than duplicating
// NormalizeSeconds/ResolutionToken/Tier/EstimateBillableTokens per version:
// those four methods' LOGIC does not vary by version at all, only the
// numbers they read do.
//
// This is the vendor the package doc names as the reason there is no shared
// "shape of a vendor's rules" struct ACROSS VENDORS — it bills in TOKENS, not
// seconds. Within this one vendor, though, 2.0 and 2.5 differ only in the
// numbers (duration bounds, resolution set, per-tier token rate), which is
// exactly the "shape" a profile can hold — see profile's own doc for why
// that is a narrower claim than the package-doc's warning.
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
type seedance struct {
	profile *seedanceProfile
}

// seedanceProfile is the handful of numbers that actually differ between
// Seedance 2.0 and 2.5: duration bounds, the resolution-tier vocabulary (and
// its nearest-match snapping table), the default tier, and the per-tier
// token/second rate. Everything else about the vendor — the wire request/
// response shapes, the async create+poll protocol, the client — is identical
// and lives once, in internal/seedance and this package's methods.
type seedanceProfile struct {
	minSeconds, maxSeconds int64
	resolutionTokens       []string
	tierMaxSides           []seedanceTierMaxSide
	defaultTier            string
	tokensPerSecond        map[string]int64
}

// seedanceTierMaxSide is one entry of a tier-snapping table: a resolution
// token paired with its documented longer-side pixel count. A named type
// (rather than the anonymous struct this field used before profiles
// existed) so both 2.5's and 2.0's tables can share one element type.
type seedanceTierMaxSide struct {
	token   string
	maxSide float64
}

// bounds/resolutionTokens/tierMaxSides/defaultTier/tokensPerSecondTable are
// the only places that branch on s.profile. Every exported method below
// calls through these instead of reading the package-level 2.5 constants (or
// a profile's fields) directly, so there is exactly one place that could get
// the nil-check wrong, not four.
//
// This contract extends to ANY future method added to seedance, not just
// the four current ones: a value-receiver method (func (seedance) Foo())
// that reads seedanceResolutionTokens/SeedanceMinSeconds/etc. directly
// would silently ignore Seedance20's profile and report 2.5's numbers for
// both versions. New methods must take a value receiver of this type (func
// (s seedance) Foo()) and route through these five accessors, exactly like
// NormalizeSeconds/ResolutionToken/Tier/EstimateBillableTokens already do.
func (s seedance) bounds() (min, max int64) {
	if s.profile != nil {
		return s.profile.minSeconds, s.profile.maxSeconds
	}
	return SeedanceMinSeconds, SeedanceMaxSeconds
}

func (s seedance) resolutionTokenSet() []string {
	if s.profile != nil {
		return s.profile.resolutionTokens
	}
	return seedanceResolutionTokens
}

func (s seedance) tierMaxSideTable() []seedanceTierMaxSide {
	if s.profile != nil {
		return s.profile.tierMaxSides
	}
	return seedanceTierMaxSides
}

func (s seedance) defaultTierToken() string {
	if s.profile != nil {
		return s.profile.defaultTier
	}
	return SeedanceDefaultTier
}

func (s seedance) tokensPerSecondTable() map[string]int64 {
	if s.profile != nil {
		return s.profile.tokensPerSecond
	}
	return seedanceTokensPerSecond
}

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
func (s seedance) NormalizeSeconds(raw string) (int64, SecondsOutcome) {
	minSeconds, maxSeconds := s.bounds()
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
	if f > float64(maxSeconds) {
		f = float64(maxSeconds)
	}
	// Ceil: the vendor takes an integer, and a fractional request yields the next
	// whole second of rendered output.
	d := int64(math.Ceil(f))
	if d < minSeconds {
		d = minSeconds
	}
	return d, SecondsResolved
}

// seedanceResolutionTokens is the SET of "size" values Seedance 2.5 reads as a
// resolution tier — a list, not a mapping, because the canonical spelling is the
// element itself (lowercase, as the vendor spells it).
//
// 1080p was NOT here originally: an early live probe had it rejected with
// InvalidParameter, and the vendor has since opened it (its published rate card
// now prices a 1080p row for dreamina-seedance-2-5-260628). 4k stays out —
// still rejected, and unlike MiniMax's list this one holds no forward-looking
// entries: being recognised here means being FORWARDED, so a tier is added the
// day the vendor serves it and not before.
//
// (2.0 DOES serve 4k — see seedance20ResolutionTokens below. It is not a
// forward-looking entry there either: 2.0's own client, before it was retired
// in favor of 2.5, forwarded exactly this set, live-confirmed.)
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
var seedanceTierMaxSides = []seedanceTierMaxSide{
	{"480p", 832},
	{"720p", 1280},
	{"1080p", 1920},
}

// ResolutionToken reports whether a "size" is one of this vendor's tier tokens,
// returning the canonical spelling if so and "" if not — see the MiniMax sibling
// for why the "" answer is the load-bearing one.
func (s seedance) ResolutionToken(size string) string {
	trimmed := strings.TrimSpace(size)
	for _, tok := range s.resolutionTokenSet() {
		if strings.EqualFold(tok, trimmed) {
			return tok
		}
	}
	return ""
}

// Tier reports the tier Seedance will render at. Unlike its two siblings this
// NEVER returns "": the vendor call must name an exact token, so an unrecognisable
// size resolves to the vendor's default tier rather than leaving the choice open.
//
// A pixel-dimension size snaps to the nearest tier by longer side; a token
// addresses one directly.
func (s seedance) Tier(size string) string {
	if tok := s.ResolutionToken(size); tok != "" {
		return tok
	}
	def := s.defaultTierToken()
	w, h, ok := ParsePixelSize(size)
	if !ok {
		return def
	}
	maxSide := float64(w)
	if float64(h) > maxSide {
		maxSide = float64(h)
	}
	best := def
	bestDiff := math.MaxFloat64
	for _, r := range s.tierMaxSideTable() {
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

// seedanceTokensPerSecond is the billable token rate for each tier Seedance 2.5
// serves.
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
	perSecond, ok := s.tokensPerSecondTable()[s.Tier(rawSize)]
	if !ok {
		return 0, false
	}
	// No overflow guard, and that is not an omission: seconds is bounded by
	// SeedanceMaxSeconds (30, or Seedance20MaxSeconds=15 for 2.0) and the rate by
	// the table above, so the product is at most 30 × 194,400 (2.0's 4k rate) ≈
	// 5.8e6 — far inside int64.
	return seconds * perSecond, true
}

// ============================================================================
// Seedance 2.0 — the version 2.5 replaced (0g-serving-broker@8ef9a5e). Being
// reintroduced alongside 2.5, not instead of it: a provider deployment picks
// one, by registering under VendorSeedance20 instead of VendorSeedance in its
// billing.vendor config, exactly the way it already picks a wire model id.
// ============================================================================

// VendorSeedance20 is ByteDance Seedance 2.0 — the SAME BytePlus Ark async
// create+poll API as VendorSeedance (2.5), served by the same client and wire
// types (internal/seedance is not duplicated for this). Only the
// duration/resolution/pricing rules below differ, carried in Seedance20's
// profile — see the seedance type's own doc for why that is a profile field
// rather than a second file/package.
const VendorSeedance20 Vendor = "seedance-2.0"

// Seedance20 is 2.0's rules as a concrete value. Every method it answers
// through (NormalizeSeconds/ResolutionToken/Tier/EstimateBillableTokens) is
// the exact same code Seedance (2.5) runs — only the numbers in
// seedance20Profile differ.
var Seedance20 = seedance{profile: &seedance20Profile}

func init() { register(VendorSeedance20, Seedance20) }

// Seedance 2.0's own duration range: an integer in [4,15]. The floor is the
// same 4 as 2.5 (2.5's own floor comment above says it "is carried over from
// 2.0" — this is that value's origin). The ceiling is 15, not 30: 2.5's
// migration commit (0g-serving-broker@8ef9a5e) states explicitly it "raised"
// the ceiling "[4,15] -> [4,30]" when it replaced 2.0, and this integration's
// own prior (later retired) 2.0 client hardcoded the same 15
// (0g-serving-broker@def557a). Independently corroborated by a third-party
// Seedance 2.0 API reference (OpenRouter's model page lists the duration
// enum as exactly the 12 integers 4 through 15, nothing past 15).
const (
	Seedance20MinSeconds = 4
	Seedance20MaxSeconds = 15
)

// seedance20ResolutionTokens: 2.0 serves one tier 2.5 does not — "4k" — which
// 2.5's migration explicitly dropped (that same migration commit: "Resolution
// support narrowed to 480p/720p only (1080p/4K are rejected by the vendor for
// this model, live-API-confirmed)"). This integration's own prior 2.0 client
// forwarded exactly this four-token set, live-confirmed, and recorded the
// vendor echoing "4k" lowercase — matched here case-insensitively either way
// (ResolutionToken folds case), so the exact echo case does not matter for
// correctness, only for which spelling this integration itself sends.
var seedance20ResolutionTokens = []string{"480p", "720p", "1080p", "4k"}

// Seedance20DefaultTier: the same default 2.5 uses. Nothing about the
// vendor's own resolution fallback changed between versions — only the
// served tier SET did (2.0 additionally serves 4k; 2.5 additionally serves
// 1080p, added back after this integration's 2.0->2.5 migration).
const Seedance20DefaultTier = SeedanceDefaultTier

// seedance20TierMaxSides extends 2.5's table with one more entry, kept in
// ascending order (load-bearing for tie-breaks — see seedanceTierMaxSides's
// doc: the first entry wins an exact tie, so order is a pricing decision, not
// cosmetic). 4K UHD is conventionally 3840x2160 — longer side 3840, exactly
// double 1080p's 1920 on each axis (so 4x the pixel count, which is what the
// token-rate comment below relies on).
var seedance20TierMaxSides = []seedanceTierMaxSide{
	{"480p", 832},
	{"720p", 1280},
	{"1080p", 1920},
	{"4k", 3840},
}

// seedance4kTokensPerSecond: BytePlus's published rate card does not reach
// 4K — it prices 480p/720p/1080p only (see seedance1080pTokensPerSecond's own
// doc for why 1080p is already formula-derived, not price-table-derived, for
// the same underlying reason). 4K therefore cannot be read off a table at
// all; it is derived from the vendor's own published formula instead:
//
//	tokens = width × height × duration × fps / 1024
//
// 4K UHD (3840x2160) is exactly 2x 1080p's (1920x1080) linear dimensions on
// EACH axis, so exactly 4x the pixel count at the same fps — and therefore
// exactly 4x the token rate: 3840×2160×24/1024 = 4 × (1920×1080×24/1024) =
// 4 × 48,600 = 194,400, with no independent rounding to accumulate (unlike
// 1080p's own formula-vs-price-table cross-check, this is the SAME formula
// evaluated at an exact multiple of the same inputs, not two independent
// routes to compare).
const seedance4kTokensPerSecond = seedance1080pTokensPerSecond * 4

// seedance20TokensPerSecond reuses 2.5's own 480p/720p/1080p rates verbatim
// (only adding 4k): the commit that replaced 2.0 with 2.5 states the
// token-billing formula and architecture are IDENTICAL between the two
// versions, "empirically confirmed... via a live call"
// (0g-serving-broker@8ef9a5e) — so a tier's token rate is a property of the
// RENDERED resolution, not of which Seedance major version rendered it.
var seedance20TokensPerSecond = map[string]int64{
	"480p":  seedance480pTokensPerSecond,
	"720p":  seedance720pTokensPerSecond,
	"1080p": seedance1080pTokensPerSecond,
	"4k":    seedance4kTokensPerSecond,
}

// seedance20Profile bundles 2.0's own numbers. See seedanceProfile's doc:
// this is the only thing that differs between Seedance and Seedance20 —
// every method is shared code.
var seedance20Profile = seedanceProfile{
	minSeconds:       Seedance20MinSeconds,
	maxSeconds:       Seedance20MaxSeconds,
	resolutionTokens: seedance20ResolutionTokens,
	tierMaxSides:     seedance20TierMaxSides,
	defaultTier:      Seedance20DefaultTier,
	tokensPerSecond:  seedance20TokensPerSecond,
}
