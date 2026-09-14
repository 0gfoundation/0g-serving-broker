//go:build live

// Live exercise of the engine path against a real docker daemon, a real GPU and a real
// dstack socket. Build-tagged so it never runs in CI: it pulls a multi-gigabyte image,
// downloads model weights, occupies a GPU, and appends to RTMR3 — none of which belongs
// in a unit suite.
//
// Build and ship:
//
//	GOOS=linux GOARCH=amd64 go test -c -tags live -o /tmp/engine_live.test ./controller/internal/ctrl/
//
// Run it INSIDE a container, so the docker layer can identify the controller's own
// container by hostname (selfContainerID matches os.Hostname() against container IDs);
// bare on the host every write is refused.
package ctrl

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/0glabs/0g-serving-broker/common/attest"
	commonconfig "github.com/0glabs/0g-serving-broker/common/config"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/controller/internal/docker"
	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/Dstack-TEE/dstack/sdk/go/dstack"
)

const (
	liveImage    = "lmsysorg/sglang@sha256:fc458e7940c71b89f535989a2ac83df2e48f5be9bd84a59ca30d8f211f45fe13"
	liveRepo     = "lmsysorg/sglang"
	liveModel    = "Qwen/Qwen3-0.6B"
	liveRevision = "c1899de289a04d12100db370d81485cdf75e47ca"
	liveName     = "zg-live-qwen"
	livePort     = 8000
	liveNetwork  = "zg-live"
)

// liveCtrl builds a Ctrl the way the unit tests do — by hand — but against the real
// daemon and the real dstack socket. NewCtrl is bypassed on purpose: it requires a
// contract address, an RPC URL and a wallet, none of which any part of the engine path
// touches.
func liveCtrl(t *testing.T, ignore []string) *Ctrl {
	t.Helper()

	logger, err := log.GetLogger(&commonconfig.LoggerConfig{Format: "text", Level: "info"})
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	cfg := config.ControllerConfig{
		RecordUpstreamSet: true,
		EngineNetwork:     liveNetwork,
		EngineGPUIgnore:   ignore,
		EngineVolumes:     []string{"zg-live-hfcache:/root/.cache/huggingface"},
		EngineEnv:         map[string]string{"HF_HOME": "/root/.cache/huggingface"},
		Engines: []config.EngineImage{{
			ImageRepo: liveRepo,
			// sglang's own entrypoint is nvidia_entrypoint.sh, which does exec "$@" — a
			// flag list with no program in front of it dies with `exec: --: invalid option`.
			// This is what the first live run found.
			Entrypoint:   []string{"python", "-m", "sglang.launch_server"},
			ModelFlag:    "--model-path",
			RevisionFlag: "--revision",
			PortFlag:     "--port",
			HostFlag:     "--host",
			ShmSize:      "8gb",
		}},
		// 1.41 is what the fleet's compose sets. The dev CVM's daemon caps at 1.44, so a
		// newer value is refused outright — worth pinning to the deployed one anyway.
		Docker: config.DockerConfig{Host: "unix:///var/run/docker.sock", APIVersion: "1.41"},
	}
	dc, err := docker.NewClient(cfg)
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	t.Cleanup(func() { _ = dc.Close() })

	return &Ctrl{
		config:       cfg,
		dockerClient: dc,
		emitter:      dstack.NewDstackClient(),
		logger:       logger,
	}
}

func liveSpec() EngineSpec {
	return EngineSpec{
		Name:  liveName,
		Image: liveImage,
		GPUs:  "0",
		Port:  livePort,
		Model: EngineModel{Repo: liveModel, Revision: liveRevision},
		// A 0.6B model on a 143 GB card: keep the static arena small so the test is not
		// measuring how long it takes to reserve 120 GB of KV cache.
		Args: []string{"--mem-fraction-static", "0.12", "--tp", "1"},
	}
}

// Step 1: what does the machine look like through the code's own eyes?
func TestLiveGPUTable(t *testing.T) {
	c := liveCtrl(t, nil)
	alloc, err := c.GPUAllocation(context.Background())
	if err != nil {
		t.Fatalf("GPUAllocation: %v", err)
	}
	cards := make([]string, 0, len(alloc))
	for gpu := range alloc {
		cards = append(cards, gpu)
	}
	sort.Strings(cards)
	for _, gpu := range cards {
		for _, claim := range alloc[gpu] {
			t.Logf("gpu=%-4s heldBy=%-24s engine=%-5v monitoring=%v", claim.GPU, claim.HeldBy, claim.ByEngine, claim.Monitoring)
		}
	}
	if len(alloc) == 0 {
		t.Log("no card is claimed by anything")
	}
}

// Step 2: with nothing declared non-occupying, does the machine's history block a create?
func TestLiveRefusesWithoutIgnoreList(t *testing.T) {
	c := liveCtrl(t, nil)
	err := c.CreateEngine(context.Background(), liveSpec())
	if err == nil {
		t.Fatal("CreateEngine succeeded; this machine has no leftover GPU claim, so step 3's ignore list is unnecessary")
	}
	t.Logf("refused as expected: %v", err)
}

// Step 3: the real thing. Pull, record, create, start, and serve.
func TestLiveCreateAndServe(t *testing.T) {
	ignore := strings.Split(os.Getenv("ZG_LIVE_IGNORE"), ",")
	if len(ignore) == 1 && ignore[0] == "" {
		ignore = nil
	}
	c := liveCtrl(t, ignore)

	// Leave nothing behind from an earlier run.
	_, _ = c.RemoveEngine(context.Background(), liveName)

	start := time.Now()
	if err := c.CreateEngine(context.Background(), liveSpec()); err != nil {
		t.Fatalf("CreateEngine: %v", err)
	}
	t.Logf("created in %s", time.Since(start).Round(time.Second))

	engines, err := c.ListEngines(context.Background())
	if err != nil {
		t.Fatalf("ListEngines: %v", err)
	}
	for _, e := range engines {
		t.Logf("engine name=%s state=%s gpus=%s image=%s", e.Name, e.State, e.GPUs, e.Image)
	}

	// The whole point of the network alias: the broker reaches the engine by container
	// name. This test container is on the same network, so the same URL a config would
	// carry is the one used here.
	url := fmt.Sprintf("http://%s:%d/v1/models", liveName, livePort)
	deadline := time.Now().Add(12 * time.Minute)
	for {
		if time.Now().After(deadline) {
			logs, _ := c.dockerClient.GetContainerLogs(context.Background(), liveName, "60")
			t.Fatalf("%s never served within the deadline; last logs:\n%s", url, logs)
		}
		resp, err := http.Get(url)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				t.Logf("served after %s: %s", time.Since(start).Round(time.Second), strings.TrimSpace(string(body)))
				break
			}
		}
		time.Sleep(10 * time.Second)
	}

	// And an actual completion, which is what the record claims the destination can do.
	body := `{"model":"` + liveModel + `","messages":[{"role":"user","content":"say ok"}],"max_tokens":8}`
	resp, err := http.Post(fmt.Sprintf("http://%s:%d/v1/chat/completions", liveName, livePort),
		"application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("completion: %v", err)
	}
	out, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	t.Logf("completion status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(out)))
	if resp.StatusCode != http.StatusOK {
		t.Errorf("completion returned %d", resp.StatusCode)
	}
}

// Step 4: remove it, and report what the config would still be routing at it.
func TestLiveRemove(t *testing.T) {
	ignore := strings.Split(os.Getenv("ZG_LIVE_IGNORE"), ",")
	if len(ignore) == 1 && ignore[0] == "" {
		ignore = nil
	}
	c := liveCtrl(t, ignore)

	routed, err := c.RemoveEngine(context.Background(), liveName)
	if err != nil {
		t.Fatalf("RemoveEngine: %v", err)
	}
	t.Logf("removed; still routed by %v", routed)

	engines, err := c.ListEngines(context.Background())
	if err != nil {
		t.Fatalf("ListEngines: %v", err)
	}
	t.Logf("engines now: %d", len(engines))
}

// Step 5: read the ledger back out of a real quote, through the package's own reader.
//
// This is the half no unit test can reach: the payloads have to survive a real EmitEvent
// and come back out of the quote's event log, parsed by the same attest.RuntimeEvents a
// verifier would use. ReplayRTMRs is checked too — if the log does not reproduce the
// quote's registers, nothing read out of it means anything.
func TestLiveReadRTMR3(t *testing.T) {
	client := dstack.NewDstackClient()
	quote, err := client.GetQuote(context.Background(), make([]byte, 32))
	if err != nil {
		t.Fatalf("GetQuote: %v", err)
	}

	replayed, err := quote.ReplayRTMRs()
	if err != nil {
		t.Fatalf("ReplayRTMRs: %v", err)
	}
	t.Logf("RTMR3 replayed from the log: %s", replayed[3])

	events, err := attest.RuntimeEvents([]byte(quote.EventLog))
	if err != nil {
		t.Fatalf("RuntimeEvents: %v", err)
	}
	t.Logf("%d runtime events on RTMR3", len(events))

	found := 0
	for _, e := range events {
		if !strings.HasPrefix(e.Event, "zg-") {
			continue
		}
		found++
		t.Logf("  %-20s %s", e.Event, truncate(strings.ReplaceAll(string(e.Payload), "\t", " | "), 300))
	}
	if found == 0 {
		t.Error("no zg- record in the ledger; the emits above did not land where a verifier reads")
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Step 6: let the READER decide, on real records, instead of taking the writer's word.
//
// The whole feature is one join: zg-upstream-set says plaintext may reach
// http://<name>:<port>, and zg-engine-set says <name> is a container this CVM created
// from a pinned digest. Without the second record, classifyUpstreams finds no compose
// service for that host and reports an EXTERNAL vendor — fail-closed and useless. This
// runs the real resolver over the real ledger and prints what it concludes.
//
// # What this does and does not establish
//
// VerifiedQuote is constructed by hand here, and its own doc says passing an unanchored
// log is "a lie this package cannot detect". Two of the three inputs are anchored and one
// is not:
//
//   - EventLogJSON: anchored. TestLiveReadRTMR3 checks ReplayRTMRs against the quote, which
//     is what a verifier's event_log_verified means.
//   - ReportData: the quote's own, so whatever the CVM put there.
//   - ComposeHash: taken from the guest agent's Info, NOT checked against the quote's
//     signed report body. A third party must get it from a verify response.
//
// So this exercises the resolver's logic on genuine records. It is not a verification, and
// nothing here should be read as one.
func TestLiveResolveClassifiesTheEngine(t *testing.T) {
	client := dstack.NewDstackClient()

	info, err := client.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	quote, err := client.GetQuote(context.Background(), make([]byte, 64))
	if err != nil {
		t.Fatalf("GetQuote: %v", err)
	}

	state, err := attest.ResolveRunningState(attest.VerifiedQuote{
		ComposeHash:  info.ComposeHash,
		ReportData:   quote.ReportData,
		EventLogJSON: []byte(quote.EventLog),
		// The compose service the resolver identifies as the broker. A dev CVM is not a
		// broker deployment — this one defines a single service called "app" — and
		// ResolveRunningState hard-fails on a name its compose does not define, which is
		// correct: it cannot report a broker digest it cannot find.
	}, []byte(info.TcbInfo), brokerService())
	if err != nil {
		// Skipped, not failed: the resolver is all-or-nothing on purpose — it refuses to
		// answer ANY question about a CVM whose broker image it cannot pin, rather than
		// answer some of them. A dev CVM is not a broker deployment, so it usually cannot
		// satisfy that: this one's single compose service runs
		// leechael/phala-cloud-nextjs-starter:latest, a tag. Exercising the classification
		// therefore needs a real broker deployment, and the refusal below is the resolver
		// working, not failing.
		t.Skipf("this CVM cannot satisfy the resolver's preconditions, so the classification cannot be exercised here: %v", err)
	}

	t.Logf("upstreams state=%q err=%v", state.UpstreamsState, state.UpstreamsErr)
	for _, u := range state.Upstreams {
		t.Logf("  upstream name=%s url=%s identity=%s composeService=%q pinnedImage=%q imageSource=%q",
			u.Name, u.URL, u.Identity, u.ComposeService, u.PinnedImage, u.ImageSource)
	}
	t.Logf("engines state=%q err=%v", state.EnginesState, state.EnginesErr)
	for _, e := range state.Engines {
		t.Logf("  engine name=%s image=%s gpus=%s", e.Name, e.Image, e.GPUs)
	}
	for _, ch := range state.EngineChanges {
		t.Logf("  engine change: %s", ch)
	}
	hash, err := state.UpstreamSetHash()
	t.Logf("upstream set hash=%s err=%v", hash, err)

	// The claim this whole PR exists for: the destination is not an external vendor.
	if len(state.Upstreams) == 0 {
		t.Fatal("no upstream in the ledger; nothing to classify")
	}
	for _, u := range state.Upstreams {
		if u.Name != liveName {
			continue
		}
		if u.ImageSource != attest.ImageSourceRecord {
			t.Errorf("upstream %s classified as %q, want %q — without it a reader cannot tell this destination from an external vendor",
				u.Name, u.ImageSource, attest.ImageSourceRecord)
		}
		if u.PinnedImage != liveImage {
			t.Errorf("upstream %s reports image %q, want the pinned %q", u.Name, u.PinnedImage, liveImage)
		}
	}
}

// brokerService names the compose service the resolver should treat as the broker.
func brokerService() string {
	if v := os.Getenv("ZG_LIVE_BROKER_SERVICE"); v != "" {
		return v
	}
	return "0g-serving-provider-broker"
}
