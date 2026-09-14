package ctrl

import (
	"testing"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
)

// The billable route is OpenAI's real TTS path.
//
// This test previously asserted the OPPOSITE — that /audio/speech must stay
// unclaimed and /audio/generations must be the billable route — because the vendor
// API was believed to be an async submit-poll job, and a job envelope on
// /audio/speech hands an OpenAI SDK client a corrupt audio file. BytePlus's own
// reference says the API is a single synchronous POST, so there is no envelope: the
// adaptor returns the audio bytes that contract promises, and claiming the path is
// correct rather than harmful.
//
// Kept as a test rather than deleted because the hazard it guarded is real and
// returns the moment anything on this path stops returning bytes.
func TestAudioSpeechIsTheBillableRoute(t *testing.T) {
	if _, billable := constant.TargetRoute["/audio/speech"]; !billable {
		t.Error("/audio/speech is not in TargetRoute, so audio generation is not billable")
	}
	if _, stale := constant.TargetRoute["/audio/generations"]; stale {
		t.Error("/audio/generations is still registered; the async shape it was chosen for does not exist for this vendor")
	}
}

// Synchronous means there is nothing to poll, so no unbilled passthrough prefix
// should exist for this modality. An AuthRequiredPrefixes entry here would be worse
// than useless: "/audio/speech" has no sub-paths, and a prefix that matched it would
// make the one billable call free.
func TestAudioHasNoUnbilledPollPrefix(t *testing.T) {
	for _, p := range constant.AuthRequiredPrefixes {
		if p == "/audio/generations/" || p == "/audio/speech" || p == "/audio/speech/" {
			t.Errorf("AuthRequiredPrefixes carries %q; audio is synchronous, so there is no poll to exempt — and a prefix matching the create route makes the billable call free", p)
		}
	}
}

// E2EE is unavailable for this modality, deliberately rather than by oversight.
// profileForRequest's own comment names the case ("video-generation, and whatever
// service type is added next") and explains why a default arm must not guess
// ProfileChat: it would apply chat's sealing rules to a request shape nobody
// analyzed. Unaffected by the sync/async correction — the reason is about the
// request SHAPE, not the transport.
func TestAudioGenerationIsNotSealable(t *testing.T) {
	for _, surface := range []string{"", "openai", "anthropic"} {
		profile, sealable := profileForRequest(constant.ServiceTypeAudioGeneration, surface)
		if sealable {
			t.Errorf("surface %q: audio-generation reports sealable with profile %q; no wire profile has been analyzed for it", surface, profile)
		}
	}
}
