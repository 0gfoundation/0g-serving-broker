package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/middleware"
	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/internal/ctrl"
)

// newBudgetProxy builds the minimal Proxy the weight calculation reads: a
// limiter (or none) and the service's advertised model metadata.
// promptBody builds a chat request whose prompt content is n bytes, so weight
// assertions track the prompt rather than the JSON envelope.
func promptBody(n int) []byte {
	return []byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("x", n) + `"}]}`)
}

// newBudgetCtx is a bare gin context; the weight path reads only the upstream
// identity header from it, which these cases leave unset.
func newBudgetCtx() *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return c
}

// requireBuildPricing derives the multi-model lookup maps the resolver reads,
// which config validation normally builds at load.
func requireBuildPricing(t *testing.T, svc *config.Service) {
	t.Helper()
	if err := svc.BuildModelPricingMap(); err != nil {
		t.Fatalf("BuildModelPricingMap: %v", err)
	}
	if !svc.HasMultiModelPricing() {
		t.Fatal("setup: service should be multi-model")
	}
}

// defaultTestContextLength keeps fixtures inside the gate: a model with no
// contextLength is skipped entirely, which is its own (separately tested)
// behaviour and would make every weight assertion vacuous.
const defaultTestContextLength = 1 << 20

func newBudgetProxy(budget int64, maxCompletion int) *Proxy {
	mi := &config.ModelInfo{
		MaxCompletionTokens: maxCompletion,
		ContextLength:       defaultTestContextLength,
	}
	return &Proxy{
		ctrl:               &ctrl.Ctrl{Service: config.Service{Type: "chatbot", ModelInfo: mi}},
		logger:             noopLogger{},
		tokenBudgetLimiter: middleware.NewTokenBudgetLimiter(budget),
	}
}

func TestTokenBudgetWeight_PromptEstimatePlusOutputReserve(t *testing.T) {
	p := newBudgetProxy(1_000_000, 32768)
	body := promptBody(40000) // 40 KB of prompt -> 10k tokens at 4 bytes each

	weight, ok := p.tokenBudgetWeight("chatbot", "glm-5.3", newBudgetCtx(), body)
	if !ok {
		t.Fatal("chatbot traffic must be charged")
	}
	// The role string and the per-message scaffolding are prompt as well, hence
	// the extra 20 bytes over the content itself.
	if want := int64((40000+20)/bytesPerTokenEstimate + outputReserveTokens); weight != want {
		t.Fatalf("weight = %d, want %d", weight, want)
	}
}

// A model that cannot generate 4096 tokens must not be charged for them.
func TestTokenBudgetWeight_ReserveClampedToAdvertisedCap(t *testing.T) {
	p := newBudgetProxy(1_000_000, 1024)

	weight, ok := p.tokenBudgetWeight("chatbot", "glm-5.3", newBudgetCtx(), promptBody(4000))
	if !ok {
		t.Fatal("chatbot traffic must be charged")
	}
	if want := int64((4000+20)/bytesPerTokenEstimate + 1024); weight != want {
		t.Fatalf("weight = %d, want %d", weight, want)
	}
}

// Without usable modelInfo there is no basis for either guard: textOnlyInput
// would wave a vision model through on a byte count that is orders of magnitude
// wrong, and with no context length the weight has no ceiling. Charging on an
// estimate nothing bounds is worse than not charging.
func TestTokenBudgetWeight_NoUsableModelInfoSkipsTheGate(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		p := newBudgetProxy(1_000_000, 0)
		p.ctrl.Service.ModelInfo = nil
		if _, ok := p.tokenBudgetWeight("chatbot", "glm-5.3", newBudgetCtx(), promptBody(4000)); ok {
			t.Fatal("a model with no metadata must skip the gate")
		}
	})
	// The condition must match unmanagedBudgetModels' bounded(), or the startup
	// warning would name a different set of models than the gate actually skips.
	t.Run("no context length", func(t *testing.T) {
		p := newBudgetProxy(1_000_000, 32768)
		p.ctrl.Service.ModelInfo.ContextLength = 0
		if _, ok := p.tokenBudgetWeight("chatbot", "glm-5.3", newBudgetCtx(), promptBody(4000)); ok {
			t.Fatal("a model with no contextLength has no ceiling, so it must skip the gate too")
		}
	})
}

// Skipping is not free — such a model is served while escaping the budget — so
// it has to be visible at startup rather than only as a rejection counter that
// never moves.
func TestUnmanagedBudgetModels(t *testing.T) {
	single := config.Service{Type: "chatbot", ModelType: "glm-5.3"}
	if got := unmanagedBudgetModels(&single); len(got) != 1 {
		t.Fatalf("a single-model chatbot with no modelInfo must be reported, got %v", got)
	}

	single.ModelInfo = &config.ModelInfo{ContextLength: 262144}
	if got := unmanagedBudgetModels(&single); len(got) != 0 {
		t.Fatalf("a bounded single model must not be reported, got %v", got)
	}

	multi := config.Service{Type: "chatbot", ModelPricing: []config.ModelPricingEntry{
		{Model: "bounded", InputPrice: "1", OutputPrice: "1", ModelInfo: &config.ModelInfo{ContextLength: 1000}},
		{Model: "no-info", InputPrice: "1", OutputPrice: "1"},
		{Model: config.ModelWildcard, InputPrice: "1", OutputPrice: "1"},
	}}
	requireBuildPricing(t, &multi)
	got := unmanagedBudgetModels(&multi)
	if len(got) != 2 {
		t.Fatalf("both the metadata-less entry and the wildcard must be reported, got %v", got)
	}

	// A service-level block covers entries that carry none of their own.
	multi.ModelInfo = &config.ModelInfo{ContextLength: 1000}
	if got := unmanagedBudgetModels(&multi); len(got) != 0 {
		t.Fatalf("a service-level modelInfo must cover entries without one, got %v", got)
	}

	nonChatbot := config.Service{Type: "text-to-image"}
	if got := unmanagedBudgetModels(&nonChatbot); got != nil {
		t.Fatalf("a non-chatbot service is not budgeted at all, got %v", got)
	}
}

// Image, video and audio requests do not hold KV cache; charging them would
// shrink the budget for the requests that do.
func TestTokenBudgetWeight_OnlyChatbotIsCharged(t *testing.T) {
	p := newBudgetProxy(1_000_000, 32768)

	for _, svcType := range []string{"text-to-image", "image-editing", "speech-to-text", "embedding", "video-generation", ""} {
		if _, ok := p.tokenBudgetWeight(svcType, "glm-5.3", newBudgetCtx(), []byte("body")); ok {
			t.Fatalf("service type %q must not be charged against the KV budget", svcType)
		}
	}
}

func TestTokenBudgetWeight_DisabledBudgetSkipsTheGate(t *testing.T) {
	p := newBudgetProxy(0, 32768)

	if _, ok := p.tokenBudgetWeight("chatbot", "glm-5.3", newBudgetCtx(), promptBody(40000)); ok {
		t.Fatal("a disabled budget must skip the acquire/release pair entirely")
	}
}

// The measured production shapes: same request count, very different weights.
func TestTokenBudgetWeight_SeparatesTheTwoTrafficShapes(t *testing.T) {
	p := newBudgetProxy(1_440_000, 32768)

	// ~150 KB body: a 40k-token agent conversation.
	shapeA, _ := p.tokenBudgetWeight("chatbot", "glm-5.3", newBudgetCtx(), promptBody(150*1024))
	// ~1 MB body: a 250k-token long-context session.
	shapeB, _ := p.tokenBudgetWeight("chatbot", "glm-5.3", newBudgetCtx(), promptBody(1024*1024))

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

	if _, ok := p.tokenBudgetWeight("chatbot", "glm-5.3", newBudgetCtx(), promptBody(40000)); ok {
		t.Fatal("a model accepting images must not be charged on body bytes")
	}
}

func TestTokenBudgetWeight_TextOnlyArchitectureIsCharged(t *testing.T) {
	p := newBudgetProxy(1_000_000, 32768)
	p.ctrl.Service.ModelInfo.Architecture = &config.ModelArchitecture{
		InputModalities:  []string{"Text"},
		OutputModalities: []string{"text"},
	}

	if _, ok := p.tokenBudgetWeight("chatbot", "glm-5.3", newBudgetCtx(), []byte("body")); !ok {
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

	weight, ok := p.tokenBudgetWeight("chatbot", "glm-5.3", newBudgetCtx(), promptBody(32<<20))
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

	weight, _ := p.tokenBudgetWeight("chatbot", "glm-5.3", newBudgetCtx(), promptBody(4000))
	if want := int64((4000+20)/bytesPerTokenEstimate + outputReserveTokens); weight != want {
		t.Fatalf("weight = %d, want %d", weight, want)
	}
}

// A multi-model provider keeps its modelInfo per pricing entry and usually has
// no service-level block, so reading Service.ModelInfo returns nil there — which
// would silently treat a vision model as text AND leave the weight unbounded,
// the worst combination available. Unresolvable metadata must skip the gate.
func TestTokenBudgetWeight_MultiModelUnresolvedSkipsTheGate(t *testing.T) {
	p := newBudgetProxy(1_000_000, 32768)
	p.ctrl.Service.ModelInfo = nil
	p.ctrl.Service.ModelPricing = []config.ModelPricingEntry{{Model: "other-model", InputPrice: "1", OutputPrice: "1"}}
	requireBuildPricing(t, &p.ctrl.Service)

	if _, ok := p.tokenBudgetWeight("chatbot", "not-a-model-we-serve", newBudgetCtx(), []byte("body")); ok {
		t.Fatal("unresolvable multi-model metadata must skip the gate, not charge a guess")
	}
}

// The per-entry modelInfo is what bounds the weight on a multi-model provider,
// so it has to be the block this reads.
func TestTokenBudgetWeight_MultiModelUsesPerEntryModelInfo(t *testing.T) {
	p := newBudgetProxy(1_440_000, 0)
	p.ctrl.Service.ModelInfo = nil
	p.ctrl.Service.ModelPricing = []config.ModelPricingEntry{{
		Model:       "glm-5.3",
		InputPrice:  "1",
		OutputPrice: "1",
		ModelInfo: &config.ModelInfo{
			MaxCompletionTokens: 32768,
			ContextLength:       262144,
			Architecture:        &config.ModelArchitecture{InputModalities: []string{"text"}, OutputModalities: []string{"text"}},
		},
	}}
	requireBuildPricing(t, &p.ctrl.Service)

	weight, ok := p.tokenBudgetWeight("chatbot", "glm-5.3", newBudgetCtx(), promptBody(32<<20))
	if !ok {
		t.Fatal("a resolvable multi-model entry must be charged")
	}
	if weight != 262144 {
		t.Fatalf("weight = %d, want the per-entry context length %d", weight, 262144)
	}
}

// The multimodal exclusion must read the per-entry block too.
func TestTokenBudgetWeight_MultiModelMultimodalIsExcluded(t *testing.T) {
	p := newBudgetProxy(1_000_000, 0)
	p.ctrl.Service.ModelInfo = nil
	p.ctrl.Service.ModelPricing = []config.ModelPricingEntry{{
		Model:       "vision-model",
		InputPrice:  "1",
		OutputPrice: "1",
		ModelInfo: &config.ModelInfo{
			MaxCompletionTokens: 4096,
			ContextLength:       200000,
			Architecture:        &config.ModelArchitecture{InputModalities: []string{"text", "image"}, OutputModalities: []string{"text"}},
		},
	}}
	requireBuildPricing(t, &p.ctrl.Service)

	if _, ok := p.tokenBudgetWeight("chatbot", "vision-model", newBudgetCtx(), promptBody(40000)); ok {
		t.Fatal("a per-entry vision model must not be charged on body bytes")
	}
}

// reserveTokenBudget is what both the billed and the whitelisted branch call.
func TestReserveTokenBudget_AdmitsThenRefusesWhenFull(t *testing.T) {
	p := newBudgetProxy(20000, 32768)
	p.rejections = newRejectionAggregator(noopLogger{}, time.Minute)
	body := promptBody(40000) // ~10k prompt tokens + the 4096 reserve

	release, ok := p.reserveTokenBudget(newBudgetCtx(), "chatbot", "glm-5.3", "0xuser", body)
	if !ok {
		t.Fatal("the first request must fit an empty budget")
	}

	ctx := newBudgetCtx()
	if _, ok := p.reserveTokenBudget(ctx, "chatbot", "glm-5.3", "0xuser", body); ok {
		t.Fatal("the second request must not fit")
	}
	if w, isRec := ctx.Writer.(interface{ Status() int }); isRec && w.Status() != http.StatusTooManyRequests {
		t.Fatalf("refusal status = %d, want 429", w.Status())
	}

	// Releasing the first frees exactly its weight, so the second now fits.
	release()
	if _, ok := p.reserveTokenBudget(newBudgetCtx(), "chatbot", "glm-5.3", "0xuser", body); !ok {
		t.Fatal("released capacity must be reusable")
	}
}

// When the gate does not apply, the returned release must be safe to defer.
func TestReserveTokenBudget_NoOpReleaseWhenGateSkipped(t *testing.T) {
	p := newBudgetProxy(0, 32768) // disabled
	release, ok := p.reserveTokenBudget(newBudgetCtx(), "chatbot", "glm-5.3", "0xuser", []byte("body"))
	if !ok {
		t.Fatal("a disabled budget must admit")
	}
	release() // must not panic
}

// A body larger than the window could ever hold is clamped to the window
// whatever the parse says, so the parse — several nested decodes over the whole
// body — is skipped. At the 32 MB request limit that parse took seconds, which
// is a free denial of service on the broker's own CPU.
func TestTokenBudgetWeight_OversizedBodySkipsTheParse(t *testing.T) {
	p := newBudgetProxy(1_440_000, 32768)
	p.ctrl.Service.ModelInfo.ContextLength = 262144

	body := make([]byte, 32<<20)
	start := time.Now()
	weight, ok := p.tokenBudgetWeight("chatbot", "glm-5.3", newBudgetCtx(), body)
	elapsed := time.Since(start)

	if !ok {
		t.Fatal("chatbot traffic must be charged")
	}
	if weight != 262144 {
		t.Fatalf("weight = %d, want the context window %d", weight, 262144)
	}
	if elapsed > 100*time.Millisecond {
		t.Fatalf("took %v — the oversized short-circuit is not firing", elapsed)
	}
}

// The reserve must follow what the caller actually asked to generate, not a
// flat constant: reasoning requests declare tens of thousands of tokens and
// hold that KV to the end.
func TestTokenBudgetWeight_ReserveFollowsTheDeclaredOutput(t *testing.T) {
	p := newBudgetProxy(1_440_000, 32768)
	p.ctrl.Service.ModelInfo.ContextLength = 262144

	body := []byte(`{"max_tokens":30000,"messages":[{"role":"user","content":"hi"}]}`)
	weight, ok := p.tokenBudgetWeight("chatbot", "glm-5.3", newBudgetCtx(), body)
	if !ok {
		t.Fatal("chatbot traffic must be charged")
	}
	if weight < 30000 {
		t.Fatalf("weight = %d, want at least the 30000 output tokens the caller asked for", weight)
	}

	// And it is still bounded by what the model can actually generate.
	p.ctrl.Service.ModelInfo.MaxCompletionTokens = 4096
	capped, _ := p.tokenBudgetWeight("chatbot", "glm-5.3", newBudgetCtx(), body)
	if capped > 4096+100 {
		t.Fatalf("weight = %d, want it clamped to the advertised 4096", capped)
	}
}
