package proxy

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/internal/ctrl"
)

// Start refuses any service type missing from proxiedServiceTypes, and main panics
// on that error — which is exactly what happened to audio-generation while every
// other layer accepted it. Pinned against the constants so the next modality cannot
// ship the same way.
func TestProxiedServiceTypesCoverEveryServiceType(t *testing.T) {
	for _, svcType := range []string{
		"zgStorage",
		constant.ServiceTypeChatbot,
		constant.ServiceTypeTextToImage,
		constant.ServiceTypeImageEditing,
		constant.ServiceTypeSpeechToText,
		constant.ServiceTypeEmbedding,
		constant.ServiceTypeVideoGeneration,
		constant.ServiceTypeAudioGeneration,
	} {
		if !proxiedServiceTypes[svcType] {
			t.Errorf("Start would refuse service type %q — a broker configured with it panics at boot", svcType)
		}
	}
}

// The proxy's CORS config names the audio headers as literals (New's `ctrl`
// parameter shadows the package), so pin them to the constants they stand for: a
// rename of either constant would otherwise leave browsers unable to read the bill.
func TestAudioHeadersAreCORSExposed(t *testing.T) {
	if ctrl.AudioDurationHeader != "X-0G-Audio-Duration-Seconds" || ctrl.AudioFeeHeader != "X-0G-Fee" {
		t.Fatalf("audio header constants changed (%q, %q); update the ExposeHeaders literals in proxy.New to match",
			ctrl.AudioDurationHeader, ctrl.AudioFeeHeader)
	}
}

func TestBillableRouteServes(t *testing.T) {
	tests := []struct {
		path, svcType string
		want          bool
	}{
		{"/audio/speech", constant.ServiceTypeAudioGeneration, true},

		// The audio route on any other broker would be billed by a case that cannot
		// read an audio response — served, and billed nothing.
		{"/audio/speech", constant.ServiceTypeSpeechToText, false},
		{"/audio/speech", constant.ServiceTypeChatbot, false},
		{"/audio/speech", constant.ServiceTypeVideoGeneration, false},

		// And an audio broker serves nothing else billable: its billing case would
		// reserve against, and forward to, an adaptor that has no such route.
		{"/chat/completions", constant.ServiceTypeAudioGeneration, false},
		{"/audio/transcriptions", constant.ServiceTypeAudioGeneration, false},

		// Everything else keeps its existing behaviour.
		{"/chat/completions", constant.ServiceTypeChatbot, true},
		{"/audio/transcriptions", constant.ServiceTypeSpeechToText, true},
		{"/videos", constant.ServiceTypeVideoGeneration, true},
	}
	for _, tt := range tests {
		if got := billableRouteServes(tt.path, tt.svcType); got != tt.want {
			t.Errorf("billableRouteServes(%q, %q) = %v, want %v", tt.path, tt.svcType, got, tt.want)
		}
	}
}

// One wallet's gate must be exclusive: a second holder waits for the first. This
// is what stops two concurrent requests both reading the unsettled sum before
// either inserts its reserve.
func TestWalletGateLocksAreExclusivePerWallet(t *testing.T) {
	var locks walletGateLocks
	var inside int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := locks.lock("0xAbCdEf0000000000000000000000000000000001")
			if n := atomic.AddInt32(&inside, 1); n != 1 {
				t.Errorf("%d holders inside one wallet's gate at once", n)
			}
			time.Sleep(time.Millisecond)
			atomic.AddInt32(&inside, -1)
			unlock()
		}()
	}
	wg.Wait()
}

// Two spellings of one address must share a stripe, or a caller could sidestep
// the lock by changing the case of its own address.
func TestWalletGateLocksIgnoreAddressCase(t *testing.T) {
	var locks walletGateLocks
	unlock := locks.lock("0xABCDEF0000000000000000000000000000000001")
	acquired := make(chan struct{})
	go func() {
		release := locks.lock("0xabcdef0000000000000000000000000000000001")
		close(acquired)
		release()
	}()
	select {
	case <-acquired:
		t.Fatal("a differently-cased spelling of the same wallet took the gate while it was held")
	case <-time.After(50 * time.Millisecond):
	}
	unlock()
	<-acquired
}
