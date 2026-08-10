// Package videospec answers one question, per video-generation vendor: given an
// OpenAI-shaped create request, what will that vendor ACTUALLY render — how long
// a clip, and at which resolution tier.
//
// It exists because two components need that answer and must not derive it
// separately. The videotranslator applies it to build the vendor call; the broker
// needs the same answer BEFORE forwarding, to hold the right amount against the
// caller's balance — a video create is billed asynchronously, minutes later, so a
// gate that guesses wrong has already let the clip be rendered by the time the
// real bill arrives. See 0gfoundation/0g-serving-broker#628.
//
// Two independent readings of one request cannot be kept in agreement: each
// vendor's defaults, clamps and tier vocabulary are its own, and a reader that
// merely fails to parse something falls back to a smaller number — always the
// direction that loses money. So both sides call the same code.
//
// # What is shared and what is not
//
// This file holds ONLY the contract and the handful of things that are genuinely
// vendor-independent. It deliberately does NOT hold a struct describing "the
// shape of a vendor's rules": the two vendors here differ on every axis, a third
// (ByteDance Seedance) bills in tokens rather than seconds, and a shared shape
// would have to grow a field for each — changing a type every existing vendor
// depends on. Two samples looking alike is not a general form.
//
// So each vendor implements Spec in its own file and keeps its own logic, exactly
// as the translate package keeps one mapper per vendor. Adding a vendor means
// adding ITS file and touching nothing else; a vendor whose rules do not fit the
// others' shape simply writes its own, and may satisfy further optional
// interfaces without any of this changing.
package videospec

import (
	"math"
	"strconv"
	"strings"
)

// Vendor identifies a video-generation vendor. A deployment names one; it is a
// statement about which upstream is behind the translator, not a tunable.
type Vendor string

// SecondsOutcome says what a raw "seconds" field resolved to. The three cases
// need different handling and must not be collapsed: one is a number to bill on,
// one is a gap in what can be known, and one is a request to refuse.
type SecondsOutcome int

const (
	// SecondsResolved: the vendor will render exactly the returned length.
	SecondsResolved SecondsOutcome = iota
	// SecondsVendorDecides: the field is unreadable and this vendor has no fixed
	// fallback, so it picks the length itself. The returned length is 0 and means
	// nothing — callers must not substitute one of their own, and cannot price the
	// request in advance.
	SecondsVendorDecides
	// SecondsRejected: no duration can be resolved from the value at all. Refuse
	// the request: every fallback available would bill the caller for a clip they
	// plainly did not ask for.
	SecondsRejected
)

// Spec is what a vendor must be able to answer. Implementations live one per
// vendor file and share no structure — see the package doc.
type Spec interface {
	// NormalizeSeconds reports the clip length this vendor will render for a raw
	// "seconds" field, exactly as the vendor's own reader would resolve it.
	//
	// Implementations must pass raw to their parser UNTRIMMED, matching the
	// vendor-side readers: a padded value is unreadable to them, and trimming
	// would resolve a duration they would not.
	NormalizeSeconds(raw string) (int64, SecondsOutcome)

	// Tier reports the resolution tier this vendor will render at, or "" when
	// nothing determines one and the vendor will fall back to its own default.
	//
	// It takes no deployment parameter. A vendor that serves a single tier states
	// it here as the fact it is; one that derives a tier from the request derives
	// it. Neither is an operator's choice, and making it one meant the broker and
	// the translator each holding a copy of the same claim, in different files, in
	// different containers — with nothing keeping them equal and a mismatch
	// mispricing every request in silence.
	Tier(size string) string
}

// specs is the registry, filled by each vendor's own file at init. Nothing
// enumerates vendors here.
//
// A vendor absent from it has no rules recorded, which callers must treat as
// "unknown", never as "assume the common case".
var specs = map[Vendor]Spec{}

// register records one vendor's rules. Called from that vendor's own file at
// package init.
//
// It panics on a duplicate or an empty name rather than letting the last
// registration win: two files claiming one vendor is a merge accident, and which
// one wins would then depend on file order — with the losing set of rules
// silently pricing every request for that vendor.
func register(v Vendor, s Spec) {
	if v == "" {
		panic("videospec: registered a spec with no vendor name")
	}
	if _, dup := specs[v]; dup {
		panic("videospec: duplicate rules registered for vendor " + string(v))
	}
	specs[v] = s
}

// Get returns the rules recorded for a vendor. ok is false for a vendor nobody
// has recorded rules for — a caller that needs to predict what will be rendered
// must degrade explicitly there, not guess.
func Get(v Vendor) (Spec, bool) {
	s, ok := specs[Vendor(strings.ToLower(strings.TrimSpace(string(v))))]
	return s, ok
}

// maxRepresentableSeconds bounds a duration this package will resolve at all.
//
// This one IS vendor-independent, which is why it is here and not in a vendor
// file: it is not a vendor's rule but ours, guarding the float-to-int64
// conversion every implementation performs. Past it the conversion is
// implementation-defined (MinInt64 on amd64), which a vendor's floor would then
// clamp UP — quietly turning the most absurd request into the shortest clip.
const maxRepresentableSeconds = 1 << 40

// ParseSeconds reads a raw "seconds" field into a positive, representable float
// for a vendor implementation to apply its own rules to.
//
// Three outcomes, and the middle one is why this is shared: `rejected` is the
// bound above — ours, identical for everyone. `ok=false` means the vendor's own
// reader could not read it either, and what happens THEN is the vendor's business
// (a floor, or handing the choice to the vendor), so this returns rather than
// decides.
//
// raw is NOT trimmed: the vendor-side readers do not trim, so a padded value is
// unreadable to them.
func ParseSeconds(raw string) (value float64, ok, rejected bool) {
	f, err := strconv.ParseFloat(raw, 64)
	if err == nil && (math.IsInf(f, 0) || f > maxRepresentableSeconds) {
		return 0, false, true
	}
	if err != nil || !(f > 0) {
		return 0, false, false
	}
	return f, true, false
}

// ParsePixelSize parses a "WIDTHxHEIGHT" size (case-insensitive separator,
// surrounding whitespace tolerated on each side) into strictly-positive pixel
// dimensions. Shared because the format is a string convention, not a vendor
// rule — what a vendor MAKES of those dimensions is its own business, and lives
// in its own file.
func ParsePixelSize(size string) (width, height int, ok bool) {
	parts := strings.SplitN(strings.ToLower(size), "x", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	w, errW := strconv.Atoi(strings.TrimSpace(parts[0]))
	h, errH := strconv.Atoi(strings.TrimSpace(parts[1]))
	if errW != nil || errH != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}
