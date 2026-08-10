package videospec

import (
	"math"
	"strings"
)

// VendorDashScope is Alibaba DashScope's video-generation API (HappyHorse).
const VendorDashScope Vendor = "dashscope"

// DashScope is this vendor's rules as a concrete value — see MiniMax's
// counterpart for why a vendor's own mapper takes the concrete type.
var DashScope = dashScope{}

func init() { register(VendorDashScope, DashScope) }

// dashScope carries no state: its rules are the code below. Note how little it
// shares with the MiniMax sibling — no floor, no ceiling, no fallback, and a tier
// derived from the request rather than from configuration. That is why neither
// is expressed as data filled into a common struct.
type dashScope struct{}

// DashScope treats duration as optional. An unreadable one is OMITTED from the
// vendor call and the vendor applies its own default, so the rendered length is
// not knowable from the request at all — SecondsVendorDecides. That is the one
// asymmetry against MiniMax that matters most to a caller trying to price a
// request before sending it, and inventing a number for it would be guessing at
// the vendor's default.
//
// No floor and no ceiling: whatever is asked for is what is sent.
func (dashScope) NormalizeSeconds(raw string) (int64, SecondsOutcome) {
	f, ok, rejected := ParseSeconds(raw)
	if rejected {
		return 0, SecondsRejected
	}
	if !ok {
		return 0, SecondsVendorDecides
	}
	return int64(math.Ceil(f)), SecondsResolved
}

// dashScopeResolutionTokens is HappyHorse's own two-tier enum — a list, not a
// mapping: the canonical spelling is the element itself.
//
// Honouring the token form matters for money: without it a client sending
// size="720P" fails pixel parsing, resolution is omitted, the vendor renders at
// ITS default (1080P) — and the caller is still charged the 720p row. That is the
// one direction that underbills the provider.
var dashScopeResolutionTokens = []string{"720P", "1080P"}

// dashScopePixelThreshold is the larger-side pixel count at or below which a
// pixel-dimension size snaps to 720P (above it, 1080P). HappyHorse accepts only
// this coarse enum, never exact pixel dimensions, and 1280 is where OpenAI's own
// documented video sizes split cleanly: the 720x1280/1280x720 pair (larger side
// 1280) to 720P, the 1024x1792/1792x1024 pair to 1080P.
const dashScopePixelThreshold = 1280

// ResolutionToken reports whether a "size" is one of this vendor's tier tokens —
// see the MiniMax sibling for why the "" answer is the load-bearing one.
func (dashScope) ResolutionToken(size string) string {
	s := strings.TrimSpace(size)
	for _, tok := range dashScopeResolutionTokens {
		if strings.EqualFold(tok, s) {
			return tok
		}
	}
	return ""
}

// Tier: unlike MiniMax, this vendor DERIVES a tier from pixel dimensions, so it
// never needed a configured one either. Nothing recognisable yields "" — the
// vendor then applies its own default, which the request did not determine.
func (d dashScope) Tier(size string) string {
	if tok := d.ResolutionToken(size); tok != "" {
		return tok
	}
	w, h, ok := ParsePixelSize(size)
	if !ok {
		return ""
	}
	if w > dashScopePixelThreshold || h > dashScopePixelThreshold {
		return "1080P"
	}
	return "720P"
}
