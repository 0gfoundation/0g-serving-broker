// Package translate maps the OpenAI Audio Speech API onto Seed Audio's native
// shape, and its response back. It holds no state: every function is pure, so
// the sidecar can be scaled or restarted freely.
package translate

import (
	"encoding/base64"
	"fmt"
	"math"
	"strings"

	"github.com/0glabs/0g-serving-broker/audiotranslator/internal/seedaudio"
)

// MaxReferenceAudio is the vendor's limit on reference clips per request.
const MaxReferenceAudio = 3

// MaxTextPromptChars is the vendor's limit on the prompt.
const MaxTextPromptChars = 3000

// SpeechRequest is the OpenAI /v1/audio/speech body, plus the additive
// extensions Seed Audio needs. A request omitting every extension is a plain
// OpenAI TTS request.
type SpeechRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
	// Voice is OpenAI's preset-voice field. It maps onto a vendor REFERENCE
	// entry (`speaker`), not a top-level field — see seedaudio.Reference.
	Voice          string `json:"voice"`
	ResponseFormat string `json:"response_format"`
	// Speed is OpenAI's multiplier where 1.0 is unchanged. The vendor instead
	// takes a percentage offset in [-50, 100]. See toSpeechRate.
	Speed *float64 `json:"speed"`

	// No MaxDuration. Seed Audio has no length parameter to map one onto, so the
	// field was decoded and then dropped — a client asking for 10 seconds could
	// still receive, and be billed for, the vendor's 120-second ceiling. Leaving it
	// out of the struct makes that honest: the decoder is not strict, so a client
	// still sending it is not refused, and nothing here pretends to act on it.
	SampleRate *int `json:"sample_rate"`
	// ReferenceAudio carries up to three https URLs or data: URIs for voice
	// cloning, referenced from Input as @Audio1..@Audio3.
	ReferenceAudio []string `json:"reference_audio"`
	// ReferenceImage is one image URL or data: URI. Mutually exclusive with
	// ReferenceAudio at the vendor.
	ReferenceImage string `json:"reference_image"`
	// Pitch and Loudness are multipliers like Speed, converted the same way.
	Pitch    *float64 `json:"pitch"`
	Loudness *float64 `json:"loudness"`
}

// Validate rejects a request the vendor would refuse, BEFORE it is forwarded.
//
// Pre-flight rather than letting the vendor 400: a local named failure costs
// nothing, while a vendor rejection arrives only after the request has been
// routed and a balance reserve taken. Mirrors
// translate.ValidateSeedanceCreateRequest.
func (r SpeechRequest) Validate() error {
	if strings.TrimSpace(r.Input) == "" {
		return fmt.Errorf("input is required")
	}
	if len([]rune(r.Input)) > MaxTextPromptChars {
		return fmt.Errorf("input is %d characters; the maximum is %d", len([]rune(r.Input)), MaxTextPromptChars)
	}
	if len(r.ReferenceAudio) > MaxReferenceAudio {
		return fmt.Errorf("reference_audio has %d entries; the maximum is %d", len(r.ReferenceAudio), MaxReferenceAudio)
	}
	// The vendor cannot mix them, so this is refused rather than silently
	// dropping one — a client that gets audio in the wrong voice, billed in
	// full, is worse served than one told why its request was rejected.
	if len(r.ReferenceAudio) > 0 && strings.TrimSpace(r.ReferenceImage) != "" {
		return fmt.Errorf("reference_audio and reference_image cannot be combined")
	}
	for i, ref := range r.ReferenceAudio {
		if !isAllowedReferenceScheme(ref, "audio") {
			return fmt.Errorf("reference_audio[%d] must be an http(s) URL or a data:audio/ URI", i)
		}
		if isDataURI(ref) {
			if _, ok := inlineBase64(ref); !ok {
				return fmt.Errorf("reference_audio[%d] is a data: URI that is not base64-encoded; send data:audio/<type>;base64,<payload>", i)
			}
		}
	}
	if img := strings.TrimSpace(r.ReferenceImage); img != "" {
		if !isAllowedReferenceScheme(img, "image") {
			return fmt.Errorf("reference_image must be an http(s) URL or a data:image/ URI")
		}
		if isDataURI(img) {
			if _, ok := inlineBase64(img); !ok {
				return fmt.Errorf("reference_image is a data: URI that is not base64-encoded; send data:image/<type>;base64,<payload>")
			}
		}
	}
	return nil
}

// isDataURI reports whether a reference carries its bytes inline.
func isDataURI(raw string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), "data:")
}

// inlineBase64 returns the base64 payload of a `data:<type>;base64,<payload>` URI.
//
// The vendor's audio_data / image_data take RAW base64 — the reference describes
// them as "Base64-encoded reference audio/image", and its examples carry no
// data: prefix. Forwarding the whole URI sent the vendor a string whose first
// bytes ("data:audio/wav;base64,") are not base64 at all. Only the part after the
// first comma is the payload.
//
// ok=false for a data: URI with no comma, no ";base64" marker (percent-encoded
// data: URIs are legal but carry no base64 to forward), or an empty payload.
// Validate refuses all three before anything is sent, so the vendor never sees a
// value this could not unwrap.
func inlineBase64(raw string) (string, bool) {
	v := strings.TrimSpace(raw)
	if !isDataURI(v) {
		return "", false
	}
	comma := strings.IndexByte(v, ',')
	if comma < 0 {
		return "", false
	}
	meta := strings.ToLower(v[len("data:"):comma])
	if !strings.HasSuffix(meta, ";base64") {
		return "", false
	}
	payload := v[comma+1:]
	if payload == "" {
		return "", false
	}
	return payload, true
}

// isAllowedReferenceScheme is the scheme allowlist both existing translators
// apply to client-supplied reference media, with audio's media type added.
//
// Carried over unchanged in spirit: a vendor-side file handle must never be
// client-addressable. MiniMax rejects mm_file:// in its image_url field because
// that namespace is single-tenant upstream but multi-tenant for us — accepting a
// client-chosen handle would let one user reference another's upload. Only public
// URLs and inline data are accepted here for the same reason.
func isAllowedReferenceScheme(raw, kind string) bool {
	u := strings.ToLower(strings.TrimSpace(raw))
	return strings.HasPrefix(u, "https://") ||
		strings.HasPrefix(u, "http://") ||
		strings.HasPrefix(u, "data:"+kind+"/")
}

// ToCreateRequest maps a validated OpenAI request onto the vendor's shape.
func ToCreateRequest(r SpeechRequest) seedaudio.CreateRequest {
	out := seedaudio.CreateRequest{
		Model:      r.Model,
		TextPrompt: r.Input,
	}

	for _, ref := range r.ReferenceAudio {
		out.References = append(out.References, audioReference(ref))
	}
	if img := strings.TrimSpace(r.ReferenceImage); img != "" {
		out.References = append(out.References, imageReference(img))
	}
	// `voice` becomes a speaker reference, and ONLY when no audio reference was
	// supplied: the vendor takes exactly one of audio_url / audio_data / speaker
	// per entry, and a cloning request has already said which voice it wants by
	// supplying the clips.
	//
	// It is ALSO suppressed when an image reference was supplied, which the
	// earlier condition missed. A speaker entry is an AUDIO reference, and this
	// vendor refuses a request mixing audio and image references — so
	// `{"voice":"x","reference_image":"..."}` emitted a mixed array and was
	// rejected upstream. That combination is not exotic: OpenAI's
	// /v1/audio/speech makes `voice` a required parameter, so every
	// image-guided request issued through an OpenAI SDK hit it.
	if v := strings.TrimSpace(r.Voice); v != "" && len(r.ReferenceAudio) == 0 && strings.TrimSpace(r.ReferenceImage) == "" {
		out.References = append(out.References, seedaudio.Reference{Speaker: v})
	}

	cfg := seedaudio.AudioConfig{
		// Defaulted here rather than left empty. The response Content-Type is
		// derived from the OpenAI-side field, whose default is mp3, but an empty
		// Format let the whole audio_config block drop out and the VENDOR default
		// (wav) apply — so a request omitting response_format got wav bytes
		// labelled audio/mpeg, and a client writing them to .mp3 got a file that
		// would not play. Sending the format explicitly keeps the two ends
		// agreeing.
		Format:     toVendorFormat(defaultAudioFormat(r.ResponseFormat)),
		SampleRate: derefInt(r.SampleRate),
	}
	cfg.SpeechRate = toRateOffset(r.Speed, -50, 100)
	cfg.LoudnessRate = toRateOffset(r.Loudness, -50, 100)
	cfg.PitchRate = toRateOffset(r.Pitch, -12, 12)
	if cfg != (seedaudio.AudioConfig{}) {
		out.AudioConfig = &cfg
	}
	return out
}

// audioReference routes a raw value to the field its scheme belongs in: a data:
// URI carries the bytes inline, so its base64 PAYLOAD becomes audio_data (see
// inlineBase64 on why the prefix is stripped); anything else is a URL the vendor
// fetches. Validate has already refused every other scheme and every data: URI
// that is not base64.
func audioReference(raw string) seedaudio.Reference {
	v := strings.TrimSpace(raw)
	if isDataURI(v) {
		payload, _ := inlineBase64(v)
		return seedaudio.Reference{AudioData: payload}
	}
	return seedaudio.Reference{AudioURL: v}
}

func imageReference(raw string) seedaudio.Reference {
	v := strings.TrimSpace(raw)
	if isDataURI(v) {
		payload, _ := inlineBase64(v)
		return seedaudio.Reference{ImageData: payload}
	}
	return seedaudio.Reference{ImageURL: v}
}

// toVendorFormat maps OpenAI's response_format onto the vendor's.
//
// Only "opus" needs translating: OpenAI names the codec, the vendor names the
// container it ships it in. The rest pass through. An unrecognized value is
// passed through untouched rather than defaulted — the vendor's own validation
// is the authority, and silently substituting wav would hand back audio in a
// format the caller did not ask for.
func toVendorFormat(f string) string {
	switch strings.ToLower(strings.TrimSpace(f)) {
	case "opus":
		return "ogg_opus"
	default:
		return strings.TrimSpace(f)
	}
}

// toRateOffset converts an OpenAI-style MULTIPLIER (1.0 = unchanged) into the
// vendor's percentage OFFSET (0 = unchanged), clamped to the vendor's range.
//
// The two scales differ in kind, not just in units: OpenAI's 2.0 is the vendor's
// +100, and OpenAI's 0.5 is the vendor's -50. Forwarding the multiplier verbatim
// would read 2.0 as "+2%" — a barely perceptible change where the caller asked
// for double speed.
//
// Returns nil for an absent or 1.0 value so the field is omitted and the vendor
// applies its own default, rather than sending an explicit 0 that means the same
// thing but pins us to today's default.
func toRateOffset(multiplier *float64, min, max int) *int {
	if multiplier == nil {
		return nil
	}
	m := *multiplier
	if math.IsNaN(m) || math.IsInf(m, 0) || m <= 0 || m == 1 {
		return nil
	}
	offset := int(math.Round((m - 1) * 100))
	if offset < min {
		offset = min
	}
	if offset > max {
		offset = max
	}
	if offset == 0 {
		return nil
	}
	return &offset
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// DecodeAudio turns the vendor's base64 payload into the raw bytes the OpenAI
// contract requires in the response body.
func DecodeAudio(resp seedaudio.CreateResponse) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(resp.Audio)
	if err != nil {
		return nil, fmt.Errorf("decode vendor audio: %w", err)
	}
	if len(b) == 0 {
		return nil, fmt.Errorf("vendor audio decoded to zero bytes")
	}
	return b, nil
}

// BillableSeconds reports the duration the broker should bill, as the vendor
// defines it.
//
// original_duration, NEVER duration. The reference says so twice, and the two
// diverge exactly when speed or post-processing applies — so reading `duration`
// would bill a speed:2.0 request for the clip the listener hears rather than the
// audio the model produced, roughly halving the charge. This is the single most
// important field mapping in the integration.
//
// Falls back to `duration` only when original_duration is absent or unusable:
// some charge is closer to right than none, and the broker's own fallback
// (charging the vendor's ceiling) is strictly worse for the caller.
//
// The SOURCE is returned, not just the number, because that fallback is
// otherwise invisible. It still populates the duration header, so the broker
// takes its normal usage path and neither
// broker_audio_billing_fallback_total{source="ceiling"} nor the router's
// router_audio_billing_source_total moves off the healthy value — every
// dashboard on both hops reads green while a speed-adjusted request is billed
// for roughly half what it produced. A vendor-side change to original_duration
// would therefore discount silently and indefinitely. The caller logs on
// DurationSourcePostProcessed so the degradation has at least one signal.
type DurationSource string

const (
	// DurationSourceOriginal: original_duration, the field the vendor's own
	// reference names as the billing figure. The expected path.
	DurationSourceOriginal DurationSource = "original_duration"
	// DurationSourcePostProcessed: `duration`, the POST-PROCESSED length. Equal
	// to original_duration only when no speed or post-processing applied, so
	// this under-bills exactly the requests that adjust speed.
	DurationSourcePostProcessed DurationSource = "duration"
	// DurationSourceNone: neither field was usable; the broker will bill the
	// vendor's per-request ceiling, which over-bills.
	DurationSourceNone DurationSource = "none"
)

func BillableSeconds(resp seedaudio.CreateResponse) (float64, DurationSource, bool) {
	if f, err := resp.OriginalDuration.Float64(); err == nil && f > 0 && !math.IsInf(f, 0) && !math.IsNaN(f) {
		return f, DurationSourceOriginal, true
	}
	if f, err := resp.Duration.Float64(); err == nil && f > 0 && !math.IsInf(f, 0) && !math.IsNaN(f) {
		return f, DurationSourcePostProcessed, true
	}
	return 0, DurationSourceNone, false
}

// defaultAudioFormat pins the container when the caller named none. OpenAI's
// /v1/audio/speech defaults to mp3 and ContentTypeFor answers audio/mpeg for
// the empty string, so the vendor has to be told mp3 explicitly — its own
// default is wav.
func defaultAudioFormat(f string) string {
	if strings.TrimSpace(f) == "" {
		return "mp3"
	}
	return f
}

// ContentTypeFor maps a response_format onto the media type to return. Unknown
// formats become application/octet-stream rather than a guess: claiming
// audio/mpeg for something else would mislead a client into mis-decoding it.
func ContentTypeFor(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "mp3":
		return "audio/mpeg"
	case "wav":
		return "audio/wav"
	case "pcm":
		return "audio/L16"
	case "opus", "ogg_opus":
		return "audio/ogg"
	default:
		return "application/octet-stream"
	}
}
