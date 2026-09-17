package audiospec

import "math"

// VendorSeedAudio is ByteDance Seed Audio 1.0's audio-generation API (BytePlus
// Voice / openspeech).
//
// Note the platform: Seed Audio is NOT on Ark/ModelArk, where its sibling
// Seedance lives. Volcengine's own ark-cli excludes voice models from every Ark
// pipeline — deploy, chat, gen, pricing, usage — on the grounds that appearing in
// the model catalog does not make a voice model callable there. That difference
// does not reach this file (rules are rules whatever the host), but it is the
// reason the client under the translator cannot be the seedance one.
const VendorSeedAudio Vendor = "seedaudio"

// SeedAudio is this vendor's rules as a concrete value.
var SeedAudio = seedAudio{}

func init() { register(VendorSeedAudio, SeedAudio) }

// seedAudio carries no state: its rules are the code below.
type seedAudio struct{}

// seedAudioMaxSeconds is Seed Audio 1.0's hard per-request output ceiling.
//
// This is the number the whole reservation design rests on, so what it is and is
// not: it is a VENDOR limit on one generation, published alongside the model, and
// the broker cannot raise it by configuration. A request asking for more does not
// get more — the vendor caps the output — so holding this much is holding enough.
//
// It is NOT a promise about any individual request's length. Most generations come
// in well under it; a script of a few sentences produces a few seconds of audio and
// is billed for a few seconds. The ceiling is what bounds the WORST case, which is
// the only thing a pre-forward balance gate needs to know.
const seedAudioMaxSeconds = 120

func (seedAudio) MaxOutputSeconds() int64 { return seedAudioMaxSeconds }

// ReserveSeconds returns the most output audio this vendor can bill for a create
// request carrying rawMax as its `max_duration`.
//
// Every path lands in (0, seedAudioMaxSeconds]:
//
//   - a readable value below the ceiling reserves that value, rounded UP. A
//     fractional max_duration is a ceiling on a quantity billed in whole seconds,
//     so rounding down would reserve less than a conforming request can be billed
//     — the one direction a bound must never go.
//   - a readable value at or above the ceiling, and anything unreadable, absent,
//     zero, negative or absurd, reserves the ceiling. ParseMaxDuration collapses
//     all of those into ok=false precisely so this function does not branch on a
//     distinction that has no different answer.
//
// Deliberately NOT clamping to a floor. Seed Audio has no documented minimum
// billable duration, and inventing one would reserve for audio the vendor may
// never produce.
func (seedAudio) ReserveSeconds(rawMax string) int64 {
	f, ok := ParseMaxDuration(rawMax)
	if !ok {
		return seedAudioMaxSeconds
	}
	// Ceil BEFORE comparing, not after: a max_duration of 119.4 rounds to 120,
	// which is the ceiling, and comparing the raw float first would return 119
	// for a request whose whole-second bound is 120.
	secs := int64(math.Ceil(f))
	if secs >= seedAudioMaxSeconds {
		return seedAudioMaxSeconds
	}
	return secs
}
