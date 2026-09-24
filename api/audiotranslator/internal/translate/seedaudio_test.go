package translate

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/0glabs/0g-serving-broker/audiotranslator/internal/seedaudio"
)

func f64(v float64) *float64 { return &v }

// The single most important mapping in the integration. The vendor returns two
// durations that DIVERGE under speed adjustment, and its reference names
// original_duration as the billing figure twice. Reading `duration` instead would
// bill a speed:2.0 request for the clip the listener hears rather than the audio
// the model produced — roughly half.
func TestBillableSecondsPrefersOriginalDuration(t *testing.T) {
	resp := seedaudio.CreateResponse{
		Duration:         json.Number("24"), // what a 2x-speed listener hears
		OriginalDuration: json.Number("48"), // what the model produced — bills
	}
	got, source, ok := BillableSeconds(resp)
	if !ok || got != 48 {
		t.Fatalf("got %v (ok=%v), want 48 — duration must never win over original_duration", got, ok)
	}
	if source != DurationSourceOriginal {
		t.Errorf("source = %q, want %q", source, DurationSourceOriginal)
	}
}

// Falling back to `duration` beats falling through to the broker's reserved
// ceiling, which over-bills by construction.
func TestBillableSecondsFallsBackToDuration(t *testing.T) {
	got, source, ok := BillableSeconds(seedaudio.CreateResponse{Duration: json.Number("12.5")})
	if !ok || got != 12.5 {
		t.Fatalf("got %v (ok=%v), want 12.5", got, ok)
	}
	// The source is what makes this fallback visible. It still populates the
	// duration header, so without it the broker bills normally and every
	// fallback metric on both hops stays green while speed-adjusted requests
	// are under-billed.
	if source != DurationSourcePostProcessed {
		t.Errorf("source = %q, want %q — the caller logs on this value", source, DurationSourcePostProcessed)
	}
	if _, source, ok := BillableSeconds(seedaudio.CreateResponse{}); ok || source != DurationSourceNone {
		t.Errorf("an empty response reported ok=%v source=%q", ok, source)
	}
	for _, bad := range []string{"0", "-5", "abc"} {
		if _, _, ok := BillableSeconds(seedaudio.CreateResponse{OriginalDuration: json.Number(bad), Duration: json.Number(bad)}); ok {
			t.Errorf("%q was accepted as a duration", bad)
		}
	}
}

// OpenAI's speed is a MULTIPLIER (1.0 unchanged); the vendor's speech_rate is a
// percentage OFFSET (0 unchanged). Forwarding the multiplier verbatim would read
// 2.0 as "+2%" — imperceptible where the caller asked for double speed.
func TestSpeedBecomesAPercentageOffset(t *testing.T) {
	tests := []struct {
		name  string
		speed *float64
		want  *int
	}{
		{name: "2x → +100", speed: f64(2.0), want: intp(100)},
		{name: "0.5x → -50", speed: f64(0.5), want: intp(-50)},
		{name: "1.5x → +50", speed: f64(1.5), want: intp(50)},
		{name: "1.0 is unchanged → omitted", speed: f64(1.0), want: nil},
		{name: "absent → omitted", speed: nil, want: nil},
		// Beyond the vendor's range: clamped, not rejected. The vendor caps the
		// effect anyway, and refusing would fail a request it would have served.
		{name: "10x clamps to +100", speed: f64(10), want: intp(100)},
		{name: "0.01x clamps to -50", speed: f64(0.01), want: intp(-50)},
		{name: "zero is nonsense → omitted", speed: f64(0), want: nil},
		{name: "negative is nonsense → omitted", speed: f64(-1), want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := ToCreateRequest(SpeechRequest{Input: "hi", Speed: tt.speed})
			var got *int
			if out.AudioConfig != nil {
				got = out.AudioConfig.SpeechRate
			}
			switch {
			case tt.want == nil && got != nil:
				t.Errorf("speech_rate = %d, want omitted", *got)
			case tt.want != nil && got == nil:
				t.Errorf("speech_rate omitted, want %d", *tt.want)
			case tt.want != nil && *got != *tt.want:
				t.Errorf("speech_rate = %d, want %d", *got, *tt.want)
			}
		})
	}
}

// `voice` is a REFERENCE entry at the vendor, not a top-level field — and it must
// not be sent alongside audio clips, since each entry takes exactly one of
// audio_url / audio_data / speaker and a cloning request already said which voice
// it wants.
func TestVoiceMapsToASpeakerReference(t *testing.T) {
	out := ToCreateRequest(SpeechRequest{Input: "hi", Voice: "zh_female_01"})
	if len(out.References) != 1 || out.References[0].Speaker != "zh_female_01" {
		t.Fatalf("references = %+v, want one speaker entry", out.References)
	}

	withClips := ToCreateRequest(SpeechRequest{
		Input:          "@Audio1 hi",
		Voice:          "zh_female_01",
		ReferenceAudio: []string{"https://example.com/a.mp3"},
	})
	for _, ref := range withClips.References {
		if ref.Speaker != "" {
			t.Error("speaker was sent alongside an audio reference; the vendor takes exactly one per entry")
		}
	}
}

// The OpenAI SDK requires `voice`, so an SDK caller sends an OpenAI preset like
// "alloy". That is not a Seed Audio speaker, so it must not be forwarded as one:
// the request goes out with no speaker entry and the model's default voice applies.
// A real speaker id still goes through.
func TestOpenAIPresetVoicesSelectTheDefaultVoice(t *testing.T) {
	for _, preset := range []string{"alloy", "Nova", "  SHIMMER "} {
		out := ToCreateRequest(SpeechRequest{Input: "hi", Voice: preset})
		if len(out.References) != 0 {
			t.Errorf("voice %q was forwarded as %+v; an OpenAI preset is not a vendor speaker", preset, out.References)
		}
	}
	out := ToCreateRequest(SpeechRequest{Input: "hi", Voice: "zh_female_vv_uranus_bigtts"})
	if len(out.References) != 1 || out.References[0].Speaker != "zh_female_vv_uranus_bigtts" {
		t.Errorf("a vendor speaker id was not forwarded: %+v", out.References)
	}
}

// A data: URI carries bytes inline and belongs in audio_data; anything else is a
// URL the vendor fetches.
func TestReferenceRoutingByScheme(t *testing.T) {
	out := ToCreateRequest(SpeechRequest{
		Input:          "@Audio1 @Audio2 hi",
		ReferenceAudio: []string{"https://example.com/a.mp3", "data:audio/mp3;base64,AAAA"},
	})
	if out.References[0].AudioURL == "" || out.References[0].AudioData != "" {
		t.Errorf("an https reference did not become audio_url: %+v", out.References[0])
	}
	if out.References[1].AudioData == "" || out.References[1].AudioURL != "" {
		t.Errorf("a data: reference did not become audio_data: %+v", out.References[1])
	}
}

// audio_data / image_data take RAW base64, per the vendor reference. The data:
// prefix is not base64, so forwarding the whole URI handed the vendor a payload
// that fails to decode from its first byte. Exact equality, not "non-empty": the
// earlier test above passed while the prefix was being forwarded.
func TestInlineReferencesForwardOnlyTheBase64Payload(t *testing.T) {
	out := ToCreateRequest(SpeechRequest{
		Input:          "@Audio1 hi",
		ReferenceAudio: []string{"  DATA:audio/wav;BASE64,UklGRg==  "},
	})
	if got := out.References[0].AudioData; got != "UklGRg==" {
		t.Errorf("audio_data = %q, want the bare payload %q", got, "UklGRg==")
	}

	img := ToCreateRequest(SpeechRequest{Input: "hi", ReferenceImage: "data:image/png;base64,iVBORw0KGgo="})
	if got := img.References[0].ImageData; got != "iVBORw0KGgo=" {
		t.Errorf("image_data = %q, want the bare payload %q", got, "iVBORw0KGgo=")
	}
	if img.References[0].ImageURL != "" {
		t.Errorf("an inline image also populated image_url: %+v", img.References[0])
	}
}

func TestInlineBase64(t *testing.T) {
	tests := []struct {
		raw    string
		want   string
		wantOK bool
	}{
		{raw: "data:audio/wav;base64,AAAA", want: "AAAA", wantOK: true},
		// Parameters before the base64 marker are legal in a data: URI.
		{raw: "data:audio/wav;rate=24000;base64,AAAA", want: "AAAA", wantOK: true},
		// Only the FIRST comma separates metadata from payload.
		{raw: "data:audio/wav;base64,AA,AA", want: "AA,AA", wantOK: true},

		{raw: "data:audio/wav,%52%49%46%46", wantOK: false}, // percent-encoded, not base64
		{raw: "data:audio/wav;base64", wantOK: false},       // no comma
		{raw: "data:audio/wav;base64,", wantOK: false},      // empty payload
		{raw: "data:audio/wav;base64x,AAAA", wantOK: false}, // marker must end the metadata
		{raw: "https://example.com/a.wav", wantOK: false},   // not inline at all
	}
	for _, tt := range tests {
		got, ok := inlineBase64(tt.raw)
		if ok != tt.wantOK || got != tt.want {
			t.Errorf("inlineBase64(%q) = (%q, %v), want (%q, %v)", tt.raw, got, ok, tt.want, tt.wantOK)
		}
	}
}

// opus is the one format whose name differs: OpenAI names the codec, the vendor
// names the container. An unknown value passes through so the vendor's own
// validation decides, rather than silently substituting wav and returning audio
// in a format nobody asked for.
func TestFormatMapping(t *testing.T) {
	for in, want := range map[string]string{
		"mp3": "mp3", "wav": "wav", "pcm": "pcm",
		"opus": "ogg_opus", "ogg_opus": "ogg_opus",
		"flac": "flac", "": "",
	} {
		if got := toVendorFormat(in); got != want {
			t.Errorf("toVendorFormat(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		req     SpeechRequest
		wantErr string
	}{
		{name: "minimal", req: SpeechRequest{Input: "hi"}},
		{name: "three clips is the limit", req: SpeechRequest{Input: "hi", ReferenceAudio: []string{
			"https://a", "https://b", "https://c"}}},

		{name: "empty input", req: SpeechRequest{Input: "  "}, wantErr: "input is required"},
		{name: "over the character limit", req: SpeechRequest{Input: strings.Repeat("x", MaxTextPromptChars+1)}, wantErr: "maximum is 3000"},
		{name: "four clips", req: SpeechRequest{Input: "hi", ReferenceAudio: []string{
			"https://a", "https://b", "https://c", "https://d"}}, wantErr: "maximum is 3"},
		// Refused rather than silently dropping one: a caller billed in full for
		// audio in the wrong voice is worse served than one told why.
		{name: "audio and image together", req: SpeechRequest{Input: "hi",
			ReferenceAudio: []string{"https://a"}, ReferenceImage: "https://b"}, wantErr: "cannot be combined"},
		// A vendor-side handle must never be client-addressable — MiniMax's
		// mm_file:// guard exists because that namespace is multi-tenant for us.
		{name: "a file handle scheme", req: SpeechRequest{Input: "hi",
			ReferenceAudio: []string{"mm_file://someone-elses-upload"}}, wantErr: "must be an http(s) URL"},
		{name: "a non-audio data URI", req: SpeechRequest{Input: "hi",
			ReferenceAudio: []string{"data:image/png;base64,AAAA"}}, wantErr: "must be an http(s) URL"},
		// A data: URI the translator cannot unwrap to raw base64 is refused here,
		// not forwarded for the vendor to fail on after routing and a reserve.
		{name: "a percent-encoded audio data URI", req: SpeechRequest{Input: "hi",
			ReferenceAudio: []string{"data:audio/wav,RIFF"}}, wantErr: "not base64-encoded"},
		{name: "an empty inline image", req: SpeechRequest{Input: "hi",
			ReferenceImage: "data:image/png;base64,"}, wantErr: "not base64-encoded"},
		{name: "an inline base64 image", req: SpeechRequest{Input: "hi",
			ReferenceImage: "data:image/png;base64,iVBORw0KGgo="}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.req.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want one containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestDecodeAudio(t *testing.T) {
	want := []byte("ID3\x04rawaudio")
	got, err := DecodeAudio(seedaudio.CreateResponse{Audio: base64.StdEncoding.EncodeToString(want)})
	if err != nil {
		t.Fatalf("DecodeAudio: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("decoded %q, want %q", got, want)
	}
	if _, err := DecodeAudio(seedaudio.CreateResponse{Audio: "not base64!!"}); err == nil {
		t.Error("invalid base64 was accepted")
	}
	if _, err := DecodeAudio(seedaudio.CreateResponse{Audio: ""}); err == nil {
		t.Error("empty audio was accepted")
	}
}

func intp(v int) *int { return &v }

// A speaker entry is an AUDIO reference, and the vendor refuses a request that
// mixes audio and image references. `voice` is REQUIRED by OpenAI's
// /v1/audio/speech, so every image-guided request from an OpenAI SDK carries
// one — and used to emit a mixed array the vendor rejected.
func TestToCreateRequestVoiceIsDroppedBesideAnImageReference(t *testing.T) {
	out := ToCreateRequest(SpeechRequest{
		Model:          "seed-audio-1.0",
		Input:          "describe this",
		Voice:          "zh_female_01",
		ReferenceImage: "https://example.com/a.png",
	})
	for i, ref := range out.References {
		if ref.Speaker != "" {
			t.Fatalf("references[%d] carries a speaker beside an image reference; the vendor refuses a mixed array", i)
		}
	}
	if len(out.References) != 1 || out.References[0].ImageURL != "https://example.com/a.png" {
		t.Fatalf("want exactly the image reference, got %+v", out.References)
	}
}

// Omitting response_format is legal — OpenAI's own default is mp3, and
// ContentTypeFor answers audio/mpeg for the empty string. The vendor's default
// is wav, so the format must be sent explicitly or the bytes and the advertised
// Content-Type disagree.
func TestToCreateRequestDefaultsFormatToMP3(t *testing.T) {
	out := ToCreateRequest(SpeechRequest{Model: "seed-audio-1.0", Input: "hi"})
	if out.AudioConfig == nil {
		t.Fatal("audio_config was dropped, so the vendor applies its wav default while we advertise audio/mpeg")
	}
	if out.AudioConfig.Format != "mp3" {
		t.Errorf("format = %q, want mp3 to match ContentTypeFor(\"\")", out.AudioConfig.Format)
	}
	if got := ContentTypeFor(""); got != "audio/mpeg" {
		t.Errorf("ContentTypeFor(\"\") = %q; the two ends must agree", got)
	}
}

// A format or sample rate the vendor cannot serve is refused before forwarding. A
// vendor rejection can arrive as a non-zero code inside an HTTP 200, which the
// adaptor reports as 502 — a provider fault the router fails over across every
// provider — so leaving these to the vendor turned one bad request into a fleet of
// failed providers.
func TestValidateFormatAndSampleRate(t *testing.T) {
	rate := func(v int) *int { return &v }
	ok := []SpeechRequest{
		{Input: "hi"},
		{Input: "hi", ResponseFormat: "MP3"},
		{Input: "hi", ResponseFormat: "opus"},
		{Input: "hi", ResponseFormat: "ogg_opus"},
		{Input: "hi", SampleRate: rate(24000)},
	}
	for _, r := range ok {
		if err := r.Validate(); err != nil {
			t.Errorf("Validate(%+v) = %v, want ok", r, err)
		}
	}
	bad := []SpeechRequest{
		{Input: "hi", ResponseFormat: "flac"},
		{Input: "hi", ResponseFormat: "aac"},
		{Input: "hi", SampleRate: rate(22050)},
		{Input: "hi", SampleRate: rate(0)},
	}
	for _, r := range bad {
		if err := r.Validate(); err == nil {
			t.Errorf("Validate(%+v) accepted a value the vendor cannot serve", r)
		}
	}
}

// Upper-case formats reach the vendor lower-cased; the vendor's names are.
func TestToVendorFormatFoldsCase(t *testing.T) {
	for in, want := range map[string]string{"MP3": "mp3", " Wav ": "wav", "OPUS": "ogg_opus"} {
		if got := toVendorFormat(in); got != want {
			t.Errorf("toVendorFormat(%q) = %q, want %q", in, got, want)
		}
	}
}

// pitch_rate is in SEMITONES, so a frequency ratio m is 12*log2(m). Through the
// percent formula speed uses, pitch 1.1 became +10 semitones — nearly an octave.
func TestPitchIsConvertedToSemitones(t *testing.T) {
	for _, tc := range []struct {
		pitch float64
		want  *int
	}{
		{pitch: 2.0, want: intp(12)},
		{pitch: 0.5, want: intp(-12)},
		{pitch: 1.1, want: intp(2)},  // 12*log2(1.1) = 1.65
		{pitch: 4.0, want: intp(12)}, // clamped
		{pitch: 1.0, want: nil},
		{pitch: 1.02, want: nil}, // rounds to 0: omitted
	} {
		p := tc.pitch
		out := ToCreateRequest(SpeechRequest{Input: "hi", Pitch: &p})
		var got *int
		if out.AudioConfig != nil {
			got = out.AudioConfig.PitchRate
		}
		switch {
		case tc.want == nil && got != nil:
			t.Errorf("pitch %v -> pitch_rate %d, want omitted", tc.pitch, *got)
		case tc.want != nil && (got == nil || *got != *tc.want):
			t.Errorf("pitch %v -> pitch_rate %v, want %d", tc.pitch, got, *tc.want)
		}
	}
}
