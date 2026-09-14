// Package seedaudio holds the wire types and client for ByteDance Seed Audio
// 1.0's SYNCHRONOUS audio-generation API on BytePlus Voice:
// POST /api/v3/tts/create. Field names are taken from BytePlus's own reference,
// not from a reseller's wrapper.
//
// # Not Ark, and not async
//
// Seed Audio's sibling Seedance is on Ark (ark.cn-beijing.volces.com, bearer
// auth, async submit-then-poll). Seed Audio is on BytePlus Voice: a different
// host, different auth (X-Api-Key), and ONE request that returns the audio. There
// is no task id, no status enum and no poll endpoint — the words "task", "poll"
// and "async" do not appear in its reference. Assuming otherwise from the sibling
// is the error docs/design/seed-audio-generation.md records at length.
package seedaudio

import "encoding/json"

// CreateRequest is the body for POST /api/v3/tts/create.
//
// Every JSON tag is explicit. Go's case-insensitive unmarshal matching does not
// cross underscores, so an untagged snake_case field would silently stay at its
// zero value — the bug the seedance package records catching on its usage block,
// i.e. on the money path.
type CreateRequest struct {
	Model string `json:"model"`
	// TextPrompt is the prompt or the text to synthesize, up to 3000 characters.
	// With audio references it carries @Audio1..@Audio3 markers naming which
	// reference speaks where; with an image reference it may carry only the text.
	TextPrompt string `json:"text_prompt"`
	// References is the reference-media list. The vendor infers the generation
	// mode from its contents: empty means text-only, audio entries mean
	// reference-audio generation, an image entry means image-guided. Audio and
	// image references cannot be mixed.
	References  []Reference  `json:"references,omitempty"`
	AudioConfig *AudioConfig `json:"audio_config,omitempty"`
	// AIGCWatermark adds an audible rhythm marker at the end of the output.
	AIGCWatermark *bool `json:"aigc_watermark,omitempty"`
}

// Reference is one entry of the references array.
//
// An AUDIO reference carries exactly one of AudioURL, AudioData or Speaker — the
// vendor rejects more than one. An IMAGE reference carries exactly one of
// ImageURL or ImageData. Up to three audio clips (≤30s, ≤10MB each) or one image
// (≤10MB); never both kinds in the same request.
type Reference struct {
	AudioURL  string `json:"audio_url,omitempty"`
	AudioData string `json:"audio_data,omitempty"`
	// Speaker is a preset Doubao TTS 2.0 voice id or a cloned voice id. It is a
	// REFERENCE entry rather than a top-level field, which is why OpenAI's `voice`
	// maps into this array instead of sitting beside the prompt.
	Speaker   string `json:"speaker,omitempty"`
	ImageURL  string `json:"image_url,omitempty"`
	ImageData string `json:"image_data,omitempty"`
}

// AudioConfig is the output configuration block.
type AudioConfig struct {
	// Format is wav/mp3/pcm/ogg_opus. The vendor defaults to wav.
	Format string `json:"format,omitempty"`
	// SampleRate is one of 8000/16000/24000/32000/44100/48000. The vendor
	// defaults to 40000 for wav and pcm, 44100 for mp3.
	SampleRate int `json:"sample_rate,omitempty"`
	// SpeechRate, LoudnessRate: [-50, 100], where 100 is 2.0x and -50 is 0.5x.
	// PitchRate: [-12, 12]. All default to 0 meaning unchanged.
	//
	// Pointers, not plain ints: 0 is a MEANINGFUL value (unchanged) and also the
	// Go zero value, so a plain int with omitempty could never send an explicit
	// 0 — indistinguishable from "not set", which is the same thing here but
	// would stop being so if the vendor ever changed its default.
	SpeechRate   *int `json:"speech_rate,omitempty"`
	LoudnessRate *int `json:"loudness_rate,omitempty"`
	PitchRate    *int `json:"pitch_rate,omitempty"`
	// EnableSubtitle adds word- and utterance-level timestamps to the response.
	EnableSubtitle *bool `json:"enable_subtitle,omitempty"`
}

// CreateResponse is the response to POST /api/v3/tts/create. Synchronous: the
// audio is in this response, not behind a poll.
type CreateResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	// Audio is the synthesized audio, base64-encoded. The adaptor decodes it and
	// returns the raw bytes, because OpenAI's /v1/audio/speech contract is that
	// the body IS the audio.
	Audio string `json:"audio"`
	// URL is the same audio behind a link that expires after 2 hours.
	// Deliberately unused: serving it would expose the vendor's asset host to a
	// client, and it would rot.
	URL string `json:"url"`
	// Duration is the POST-PROCESSED length. NOT the billing figure.
	Duration json.Number `json:"duration"`
	// OriginalDuration is the model's original output length, and the reference
	// names it as the billing figure twice over:
	//
	//	"This is also the duration used for billing, with a maximum of 120s."
	//	"[duration] may differ from original_duration when speed adjustment or
	//	 post-processing is applied; billing is based on original_duration."
	//
	// So this, never Duration, is what the adaptor reports upstream. Using
	// Duration would bill a speed:2.0 request for the clip the listener hears
	// rather than the audio the model produced — roughly half.
	OriginalDuration json.Number `json:"original_duration"`
	// Subtitle is present only when AudioConfig.EnableSubtitle was set. Carried
	// for completeness; nothing in this integration reads it.
	Subtitle json.RawMessage `json:"subtitle,omitempty"`
}
