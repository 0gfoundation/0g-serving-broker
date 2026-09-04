package videospec

// Describer is an OPTIONAL interface a vendor may satisfy alongside Spec, for a
// consumer that needs to STATE this vendor's rules rather than apply them — the
// /v1/models catalog, and the model detail pages built from it.
//
// It is optional, and separate from Spec, for the same reason TokenEstimator is:
// Spec answers "what will this vendor render for THIS request", which every
// vendor must be able to do, while this answers "what are the bounds at all",
// which a vendor that has none (DashScope: no floor, no ceiling) can only
// describe by saying so. A vendor that does not implement it is one whose rules
// are not published — the catalog then omits the block, which is exactly the
// state every consumer already handles, since no broker published one before
// this change.
//
// The values returned here MUST be the same constants Spec's own methods branch
// on. That is the whole point: the description on a model page and the clamp the
// billing path applies are then one fact, not two that can drift. A vendor whose
// Describer disagrees with its NormalizeSeconds is publishing documentation for
// a model it does not serve.
type Describer interface {
	// Duration reports this vendor's rules for the "seconds" request field.
	Duration() DurationSpec
	// Resolution reports this vendor's rules for the "size" request field.
	Resolution() ResolutionSpec
}

// OutOfRange values: what a vendor does with a duration outside its range.
const (
	// OutOfRangeClamp: the value is silently moved to the nearest bound. The
	// caller is billed for the clip actually rendered, not the one requested.
	OutOfRangeClamp = "clamp"
	// OutOfRangePassThrough: the vendor has no range here and gets the value as
	// asked. Whatever it does with it is its own business, not a rule we record.
	OutOfRangePassThrough = "pass_through"
)

// Unspecified values: what a vendor renders when "seconds" is absent or
// unreadable. These are the SecondsOutcome cases, named for a reader.
const (
	// UnspecifiedVendorDefault: the field is omitted from the vendor call and the
	// vendor picks a length. NOT knowable from the request (SecondsVendorDecides).
	UnspecifiedVendorDefault = "vendor_default"
	// UnspecifiedMin: the vendor requires a duration, so an absent one renders the
	// floor — and is billed at it.
	UnspecifiedMin = "min"
)

// Rounding values: how a fractional duration becomes the integer a vendor takes.
const (
	// RoundingCeil: the next whole second of rendered output — and of billing.
	RoundingCeil = "ceil"
)

// PixelSize values: what a pixel-dimension "size" ("1280x720") selects. The two
// readings are not interchangeable and the difference is money: on a vendor that
// reads pixels as aspect ratio only, a "1280x720" request on a 2K-default model
// renders — and bills — a 2K clip.
const (
	// PixelSizeSelectsTier: pixel dimensions resolve to one of Tiers.
	PixelSizeSelectsTier = "selects_tier"
	// PixelSizeAspectRatioOnly: pixel dimensions set the aspect ratio and nothing
	// else; only a tier token from Tiers selects a tier.
	PixelSizeAspectRatioOnly = "aspect_ratio_only"
)

// DurationSpec is one vendor's rules for the "seconds" request field.
//
// Min and Max are omitted (zero) when the vendor has no bound, which is a
// distinct statement from "the bound is zero" — a consumer must read the
// presence, not the value. OutOfRange says so too: a vendor with no bounds
// reports OutOfRangePassThrough, so the two can never be read as contradicting.
type DurationSpec struct {
	Min         int64
	Max         int64
	OutOfRange  string
	Unspecified string
	Rounding    string
}

// ResolutionSpec is one vendor's rules for the "size" request field.
//
// Default is omitted when the vendor applies its own default rather than one
// this integration names — again a presence question, not a value question.
type ResolutionSpec struct {
	// RecognizedTiers is the vendor's tier VOCABULARY — the tokens its reader
	// treats as a tier rather than as pixel dimensions. It is NOT the set a given
	// model serves, and the two differ: miniMaxResolutionTokens carries 768P and
	// 1080P precisely so H3 REFUSES them loudly instead of silently rendering at
	// the deployment's own tier. Publishing this as "what you may send" would ship
	// a menu whose entries 400.
	//
	// A caller that needs the servable set must intersect this with what the
	// deployment actually prices — which is config the vendor cannot see, so it
	// cannot be done here.
	RecognizedTiers []string
	Default         string
	PixelSize       string
}

// copyTokens hands out a COPY of a vendor's tier list. The originals are read by
// the billing path (ResolutionToken, Tier); handing out the backing array would
// let a catalog consumer's append or sort silently repoint what gets forwarded
// to a vendor and charged for.
func copyTokens(tokens []string) []string {
	return append([]string(nil), tokens...)
}
