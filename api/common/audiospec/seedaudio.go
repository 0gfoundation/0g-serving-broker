package audiospec

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
// not: it is a VENDOR limit on one generation, published alongside the model —
// the reference describes original_duration, the billing figure, as "the duration
// used for billing, with a maximum of 120s" — and the broker cannot raise it by
// configuration. The vendor caps the output, so holding this much is holding
// enough.
//
// It is also the WHOLE reserve for every request, not just the worst case of one.
// The request has no field that lowers it: Seed Audio takes no length parameter,
// so a short script bounds nothing the broker can rely on before the audio exists.
// Most generations come in well under it and are billed for what they produced;
// the ceiling is only what is held while that is unknown.
const seedAudioMaxSeconds = 120

func (seedAudio) MaxOutputSeconds() int64 { return seedAudioMaxSeconds }
