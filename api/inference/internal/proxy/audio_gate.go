package proxy

import (
	"hash/fnv"
	"strings"
	"sync"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
)

// audioSpeechRoute is the one billable route an audio-generation broker serves.
const audioSpeechRoute = "/audio/speech"

// billableRouteServes reports whether a broker of svcType may bill targetPath, for
// the routes whose meaning is tied to one service type.
//
// constant.TargetRoute is a single global set, so adding "/audio/speech" to it made
// the path billable on EVERY broker, routed into whatever billing case that broker's
// own service type has. Neither of those cases can bill an audio response: a
// single-model speech-to-text broker fronting OpenAI would forward a TTS body to
// OpenAI on the provider's key, stream the audio back, fail to parse it as a
// transcription, and bill nothing. A chatbot broker with a wildcard model entry does
// the same through its unmarshal failure. So the audio route is bound to the audio
// service type in both directions:
//
//   - /audio/speech is refused on any broker that is not audio-generation;
//   - an audio-generation broker refuses every OTHER billable route, which its
//     audio billing case would otherwise reserve against and forward to an adaptor
//     that serves nothing else.
//
// The other TargetRoute entries keep their existing behaviour; this does not try to
// fix the global set in general.
func billableRouteServes(targetPath, svcType string) bool {
	isAudioRoute := targetPath == audioSpeechRoute
	isAudioService := svcType == constant.ServiceTypeAudioGeneration
	return isAudioRoute == isAudioService
}

// audioGateStripes is how many independent locks walletGateLocks spreads wallets
// over. A fixed array rather than a per-wallet map, so memory is bounded no matter
// how many wallets call and nothing has to be evicted. Two wallets sharing a stripe
// only serialize their (millisecond) gate step with each other, which costs latency,
// never correctness.
const audioGateStripes = 64

// walletGateLocks serializes the audio balance gate and the request-row INSERT per
// wallet.
//
// The in-flight reserve only works if a request's gate sees every earlier request's
// reserve. The gate reads SUM(fee) over the wallet's unprocessed rows; the reserve
// becomes visible when the row is inserted. Without a lock held across both, two
// requests arriving together both read the sum before either inserts and both pass
// against the same balance — and the 0G router is ONE wallet that sends bursts of
// concurrent requests, so "arriving together" is the normal case, not a corner.
//
// Per process. A deployment running several broker replicas against one database
// would need a database lock instead; brokers run as a single instance today.
//
// Only the audio path takes it. Its reserve is a true bound worth protecting
// exactly; the other modalities' gates are estimates backstopped by the minimum
// locked balance, and serializing them is a separate decision.
type walletGateLocks struct {
	stripes [audioGateStripes]sync.Mutex
}

// lock takes the stripe for userAddress and returns its unlock. Case-insensitive,
// so two spellings of one address cannot land on different stripes.
func (w *walletGateLocks) lock(userAddress string) func() {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.ToLower(userAddress)))
	m := &w.stripes[h.Sum32()%audioGateStripes]
	m.Lock()
	return m.Unlock
}
