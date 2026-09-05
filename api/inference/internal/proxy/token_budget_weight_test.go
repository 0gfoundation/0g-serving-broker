package proxy

import (
	"strings"
	"testing"

	"github.com/0glabs/0g-serving-broker/common/middleware"
	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/internal/ctrl"
)

// newBudgetProxy builds the minimal Proxy the weight calculation reads: a
// limiter (or none) and the service's advertised model metadata.
func newBudgetProxy(budget int64, maxCompletion int) *Proxy {
	var mi *config.ModelInfo
	if maxCompletion > 0 {
		mi = &config.ModelInfo{MaxCompletionTokens: maxCompletion}
	}
	return &Proxy{
		ctrl:               &ctrl.Ctrl{Service: config.Service{Type: "chatbot", ModelInfo: mi}},
		logger:             noopLogger{},
		tokenBudgetLimiter: middleware.NewTokenBudgetLimiter(budget),
	}
}

func TestTokenBudgetWeight_PromptEstimatePlusOutputReserve(t *testing.T) {
	p := newBudgetProxy(1_000_000, 32768)
	body := []byte(strings.Repeat("x", 40000)) // 40 KB -> 10k tokens at 4 bytes each

	weight, ok := p.tokenBudgetWeight("chatbot", body)
	if !ok {
		t.Fatal("chatbot traffic must be charged")
	}
	if want := int64(10000 + outputReserveTokens); weight != want {
		t.Fatalf("weight = %d, want %d", weight, want)
	}
}

// A model that cannot generate 4096 tokens must not be charged for them.
func TestTokenBudgetWeight_ReserveClampedToAdvertisedCap(t *testing.T) {
	p := newBudgetProxy(1_000_000, 1024)

	weight, ok := p.tokenBudgetWeight("chatbot", []byte(strings.Repeat("x", 4000)))
	if !ok {
		t.Fatal("chatbot traffic must be charged")
	}
	if want := int64(1000 + 1024); weight != want {
		t.Fatalf("weight = %d, want %d", weight, want)
	}
}

// No advertised cap means no information, not zero: the engine's own reserve is
// the honest default.
func TestTokenBudgetWeight_NoModelInfoUsesFullReserve(t *testing.T) {
	p := newBudgetProxy(1_000_000, 0)

	weight, ok := p.tokenBudgetWeight("chatbot", []byte(strings.Repeat("x", 4000)))
	if !ok {
		t.Fatal("chatbot traffic must be charged")
	}
	if want := int64(1000 + outputReserveTokens); weight != want {
		t.Fatalf("weight = %d, want %d", weight, want)
	}
}

// Image, video and audio requests do not hold KV cache; charging them would
// shrink the budget for the requests that do.
func TestTokenBudgetWeight_OnlyChatbotIsCharged(t *testing.T) {
	p := newBudgetProxy(1_000_000, 32768)

	for _, svcType := range []string{"text-to-image", "image-editing", "speech-to-text", "embedding", "video-generation", ""} {
		if _, ok := p.tokenBudgetWeight(svcType, []byte("body")); ok {
			t.Fatalf("service type %q must not be charged against the KV budget", svcType)
		}
	}
}

func TestTokenBudgetWeight_DisabledBudgetSkipsTheGate(t *testing.T) {
	p := newBudgetProxy(0, 32768)

	if _, ok := p.tokenBudgetWeight("chatbot", []byte(strings.Repeat("x", 40000))); ok {
		t.Fatal("a disabled budget must skip the acquire/release pair entirely")
	}
}

// The measured production shapes: same request count, very different weights.
func TestTokenBudgetWeight_SeparatesTheTwoTrafficShapes(t *testing.T) {
	p := newBudgetProxy(1_440_000, 32768)

	// ~150 KB body: a 40k-token agent conversation.
	shapeA, _ := p.tokenBudgetWeight("chatbot", []byte(strings.Repeat("x", 150*1024)))
	// ~1 MB body: a 250k-token long-context session.
	shapeB, _ := p.tokenBudgetWeight("chatbot", []byte(strings.Repeat("x", 1024*1024)))

	if shapeB <= shapeA*4 {
		t.Fatalf("a 250k-token request must weigh far more than a 40k one: %d vs %d", shapeB, shapeA)
	}

	fits := func(weight int64) int {
		l := middleware.NewTokenBudgetLimiter(1_440_000)
		n := 0
		for l.TryAcquire(weight) {
			n++
		}
		return n
	}
	// The count that a request-count cap of 20 would have admitted regardless.
	if got := fits(shapeA); got < 15 {
		t.Fatalf("40k-token requests admitted %d, expected the budget to hold at least 15", got)
	}
	if got := fits(shapeB); got > 6 {
		t.Fatalf("250k-token requests admitted %d, expected the budget to stop well before 20", got)
	}
}

// Images arrive as base64 inside the JSON body: a 3 MB picture reads as ~786k
// tokens against the ~1k of KV it really costs. Three orders of magnitude is
// not headroom, so a multimodal model must be left out of the gate entirely
// rather than shed traffic on a fiction.
func TestTokenBudgetWeight_MultimodalModelIsExcluded(t *testing.T) {
	p := newBudgetProxy(1_000_000, 32768)
	p.ctrl.Service.ModelInfo.Architecture = &config.ModelArchitecture{
		InputModalities:  []string{"text", "image"},
		OutputModalities: []string{"text"},
	}

	if _, ok := p.tokenBudgetWeight("chatbot", []byte(strings.Repeat("x", 40000))); ok {
		t.Fatal("a model accepting images must not be charged on body bytes")
	}
}

func TestTokenBudgetWeight_TextOnlyArchitectureIsCharged(t *testing.T) {
	p := newBudgetProxy(1_000_000, 32768)
	p.ctrl.Service.ModelInfo.Architecture = &config.ModelArchitecture{
		InputModalities:  []string{"Text"},
		OutputModalities: []string{"text"},
	}

	if _, ok := p.tokenBudgetWeight("chatbot", []byte("body")); !ok {
		t.Fatal("a text-only model must still be charged (match is case-insensitive)")
	}
}

// One request cannot hold more KV than the context window. Without this bound a
// single 32 MB body (the request size limit) weighs 8.4M tokens, exceeds any
// budget, and — since an over-budget request is admitted alone — parks the
// whole chatbot surface behind it.
func TestTokenBudgetWeight_BoundedByContextLength(t *testing.T) {
	p := newBudgetProxy(1_440_000, 32768)
	p.ctrl.Service.ModelInfo.ContextLength = 262144

	weight, ok := p.tokenBudgetWeight("chatbot", make([]byte, 32<<20))
	if !ok {
		t.Fatal("chatbot traffic must be charged")
	}
	if weight != 262144 {
		t.Fatalf("weight = %d, want it bounded by the %d context window", weight, 262144)
	}
}

// The bound must not inflate an ordinary request.
func TestTokenBudgetWeight_ContextBoundDoesNotRaiseSmallWeights(t *testing.T) {
	p := newBudgetProxy(1_440_000, 32768)
	p.ctrl.Service.ModelInfo.ContextLength = 262144

	weight, _ := p.tokenBudgetWeight("chatbot", []byte(strings.Repeat("x", 4000)))
	if want := int64(1000 + outputReserveTokens); weight != want {
		t.Fatalf("weight = %d, want %d", weight, want)
	}
}
