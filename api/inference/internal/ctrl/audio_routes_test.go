package ctrl

import (
	"strings"
	"testing"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
)

// The create is the only billable call. Status and content must be authenticated
// but UNBILLED, or a client polling its own job is charged per poll — and audio
// polls every few seconds.
func TestAudioRoutesAreRegisteredWithTheRightBillingIntent(t *testing.T) {
	if _, billable := constant.TargetRoute["/audio/generations"]; !billable {
		t.Error("/audio/generations is not in TargetRoute, so the create is not billable")
	}

	var found bool
	for _, p := range constant.AuthRequiredPrefixes {
		if p == "/audio/generations/" {
			found = true
		}
	}
	if !found {
		t.Error("/audio/generations/ is not in AuthRequiredPrefixes, so status and content polls would be billed")
	}

	// The trailing slash is load-bearing: without it the prefix would also match the
	// create route itself, which would make the billable call free.
	for _, p := range constant.AuthRequiredPrefixes {
		if p == "/audio/generations" {
			t.Error("AuthRequiredPrefixes contains the create route without a trailing slash; that would make the create unbilled")
		}
	}
	if strings.HasPrefix("/audio/generations", "/audio/generations/") {
		t.Error("the create route matches the unbilled prefix")
	}
}

// The path is deliberately NOT /audio/speech. OpenAI's speech endpoint promises raw
// audio bytes synchronously, so returning a job envelope there does not fail loudly
// — it hands the client a corrupt audio file with no error anywhere in the chain.
// Leaving it unclaimed also keeps it free for a future synchronous TTS model that
// can actually honour the contract.
func TestAudioSpeechPathIsNotClaimed(t *testing.T) {
	if _, claimed := constant.TargetRoute["/audio/speech"]; claimed {
		t.Error("/audio/speech is registered; a job envelope on that path produces a corrupt audio file at an OpenAI SDK client")
	}
}

// E2EE is unavailable for this modality, deliberately rather than by oversight.
// profileForRequest's own comment names the case ("video-generation, and whatever
// service type is added next") and explains why a default arm must not guess
// ProfileChat: it would apply chat's sealing rules to a request shape nobody
// analyzed. This pins the decision so a future default arm cannot silently capture
// audio.
func TestAudioGenerationIsNotSealable(t *testing.T) {
	for _, surface := range []string{"", "openai", "anthropic"} {
		profile, sealable := profileForRequest(constant.ServiceTypeAudioGeneration, surface)
		if sealable {
			t.Errorf("surface %q: audio-generation reports sealable with profile %q; no wire profile has been analyzed for it", surface, profile)
		}
	}
}
