package ctrl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0glabs/0g-serving-broker/common/attest"
	"github.com/0glabs/0g-serving-broker/inference/config"
)

// validConfigContent is the smallest config the loader accepts that names an upstream.
// The config-change path takes content rather than a parsed Service, so tests of that
// path need real YAML.
const validConfigContent = "service:\n  targetUrl: http://vllm:8000/v1\n"

// recordCtrl is a Ctrl with nothing but what RecordUpstreamSet touches: the switch, the
// service config the set comes from, and an emitter that records what was written.
func recordCtrl(t *testing.T, on bool, svc config.Service, emitErr error) (*Ctrl, *opLog) {
	t.Helper()
	l := &opLog{}
	return &Ctrl{
		config:     config.ControllerConfig{RecordUpstreamSet: on},
		fullConfig: &config.Config{Service: svc},
		emitter:    &fakeEmitter{log: l, err: emitErr},
		logger:     testLogger(t),
	}, l
}

func emitted(l *opLog) []string {
	var out []string
	for _, op := range l.all() {
		if strings.HasPrefix(op, "emit "+attest.EventUpstreamSet+" ") {
			out = append(out, strings.TrimPrefix(op, "emit "+attest.EventUpstreamSet+" "))
		}
	}
	return out
}

// The default is off, and off must write NOTHING — not an empty set, not an
// invalidation. The first zg-upstream-set event a deployment emits is what makes it
// unverifiable to every reader that predates the record, so a deployment that has not
// opted in must not emit one for any reason.
func TestRecordUpstreamSetWritesNothingWhenOff(t *testing.T) {
	svc := config.Service{TargetURL: "http://vllm:8000/v1"}
	for _, tc := range []struct {
		name string
		call func(c *Ctrl) error
	}{
		{"the record", func(c *Ctrl) error { return c.RecordUpstreamSet(context.Background()) }},
		{"the config-change record", func(c *Ctrl) error { return c.RecordUpstreamSetFromContent(context.Background(), validConfigContent) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, l := recordCtrl(t, false, svc, nil)
			if err := tc.call(c); err != nil {
				t.Fatalf("with the switch off: %v", err)
			}
			if got := emitted(l); len(got) != 0 {
				t.Errorf("wrote %q with the switch off", got)
			}
		})
	}
}

// What a real deployment's config records. The five shapes here are the five distinct
// ones in the mainnet fleet as of 2026-09-09.
func TestRecordUpstreamSetRecordsWhatTheConfigPermits(t *testing.T) {
	for _, tc := range []struct {
		name string
		svc  config.Service
		want string
	}{
		{
			// The 5 self-hosted deployments: no providerIdentity, so the name is the host,
			// which is the compose service the plaintext never leaves.
			name: "an in-CVM engine with no identity",
			svc:  config.Service{TargetURL: "http://vllm:8000/v1"},
			want: "count=1\nvllm http://vllm:8000/v1",
		},
		{
			name: "a vendor with an identity",
			svc: config.Service{
				TargetURL:        "https://openrouter.ai/api/v1",
				ProviderIdentity: "openrouter",
			},
			want: "count=1\nopenrouter https://openrouter.ai/api/v1 openrouter",
		},
		{
			// Several models on one vendor is ONE destination.
			name: "many models sharing one URL",
			svc: config.Service{
				TargetURL:        "https://tokenhub.tencentcloudmaas.com/v1",
				ProviderIdentity: "tencent",
				ModelPricing: []config.ModelPricingEntry{
					{Model: "a", TargetURL: "https://tokenhub.tencentcloudmaas.com/v1", ProviderIdentity: "tencent"},
					{Model: "b", TargetURL: "https://tokenhub.tencentcloudmaas.com/v1", ProviderIdentity: "tencent"},
				},
			},
			want: "count=1\ntencent https://tokenhub.tencentcloudmaas.com/v1 tencent",
		},
		{
			// The shape this whole feature is for: one deployment, several destinations.
			name: "a fan-out over several vendors",
			svc: config.Service{
				TargetURL:        "https://openrouter.ai/api/v1",
				ProviderIdentity: "openrouter",
				ModelPricing: []config.ModelPricingEntry{
					{Model: "a", TargetURL: "https://tokenhub.tencentcloudmaas.com/v1", ProviderIdentity: "tencent"},
					{Model: "b", TargetURL: "https://api.minimax.io/v1", ProviderIdentity: "minimax"},
				},
			},
			want: "count=3\n" +
				"minimax https://api.minimax.io/v1 minimax\n" +
				"openrouter https://openrouter.ai/api/v1 openrouter\n" +
				"tencent https://tokenhub.tencentcloudmaas.com/v1 tencent",
		},
		{
			// An in-CVM engine alongside a vendor — the case where the change log matters,
			// because withdrawing the vendor would leave a set that reads as "nothing left".
			name: "an in-CVM engine and a vendor together",
			svc: config.Service{
				TargetURL: "http://0gm-sglang:8000/v1",
				ModelPricing: []config.ModelPricingEntry{
					{Model: "a", TargetURL: "https://openrouter.ai/api/v1", ProviderIdentity: "openrouter"},
				},
			},
			want: "count=2\n" +
				"0gm-sglang http://0gm-sglang:8000/v1\n" +
				"openrouter https://openrouter.ai/api/v1 openrouter",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, l := recordCtrl(t, true, tc.svc, nil)
			if err := c.RecordUpstreamSet(context.Background()); err != nil {
				t.Fatalf("RecordUpstreamSet() = %v", err)
			}
			got := emitted(l)
			if len(got) != 1 {
				t.Fatalf("emitted %d records, want 1: %q", len(got), got)
			}
			if got[0] != tc.want {
				t.Errorf("recorded\n %q\nwant\n %q", got[0], tc.want)
			}
			// That these bytes read back as the set they name is attest's property, proved
			// there by the round-trip over RenderUpstreamSet — parseUpstreamSet is private
			// to that package. What is checked here is the bytes, and the header without
			// which a reader refuses them outright.
			if !strings.HasPrefix(got[0], "count=") {
				t.Errorf("the record carries no count= header, so a reader refuses it: %q", got[0])
			}
		})
	}
}

// A config this cannot express is recorded as UNREADABLE, never left unrecorded. The
// two are different answers — unrecorded means a reader must treat the deployment as
// unbounded, unknown means a record was written and says no set — and staying silent
// would report the weaker one.
func TestRecordUpstreamSetInvalidatesWhatItCannotExpress(t *testing.T) {
	for _, tc := range []struct {
		name string
		svc  config.Service
	}{
		{"a vendor with no identity, whose host cannot be a name", config.Service{
			TargetURL: "https://openrouter.ai/api/v1",
		}},
		{"two URLs deriving one name", config.Service{
			TargetURL:        "https://a.example/v1",
			ProviderIdentity: "vendor",
			ModelPricing: []config.ModelPricingEntry{
				{Model: "m", TargetURL: "https://b.example/v1", ProviderIdentity: "vendor"},
			},
		}},
		{"one URL claimed by two identities", config.Service{
			TargetURL:        "https://a.example/v1",
			ProviderIdentity: "one",
			ModelPricing: []config.ModelPricingEntry{
				{Model: "m", TargetURL: "https://a.example/v1", ProviderIdentity: "two"},
			},
		}},
		{"a URL the record's grammar refuses", config.Service{
			TargetURL:        "https://user:pw@a.example/v1",
			ProviderIdentity: "vendor",
		}},
		{"a URL with a trailing slash, which config accepts and the record does not", config.Service{
			TargetURL:        "https://a.example/v1/",
			ProviderIdentity: "vendor",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, l := recordCtrl(t, true, tc.svc, nil)
			if err := c.RecordUpstreamSet(context.Background()); err != nil {
				t.Fatalf("RecordUpstreamSet() = %v, want it to record an invalidation instead", err)
			}
			got := emitted(l)
			if len(got) != 1 || got[0] != upstreamSetInvalidated {
				t.Fatalf("recorded %q, want the invalidation %q", got, upstreamSetInvalidated)
			}
			// The whole point of this payload is that a reader cannot read it as a set, and
			// what makes that true is the missing header — attest's own tests cover that a
			// headerless payload is refused. Asserted here so an edit to the constant cannot
			// quietly turn it into something readable.
			if strings.HasPrefix(upstreamSetInvalidated, "count=") {
				t.Errorf("the invalidation payload %q would parse as a set", upstreamSetInvalidated)
			}
		})
	}
}

// A deployment with no upstream at all records the empty set — a bound of zero, which
// is the strongest thing a set can say — and not an invalidation and not silence.
func TestRecordUpstreamSetRecordsTheEmptySetExplicitly(t *testing.T) {
	c, l := recordCtrl(t, true, config.Service{}, nil)
	if err := c.RecordUpstreamSet(context.Background()); err != nil {
		t.Fatalf("RecordUpstreamSet() = %v", err)
	}
	got := emitted(l)
	if len(got) != 1 || got[0] != "count=0" {
		t.Fatalf("recorded %q, want %q", got, "count=0")
	}
	// The distinction that has to hold is against an EMPTY payload, which carries no
	// header and reads as unknown rather than as a bound of zero.
	if got[0] == "" || got[0] == upstreamSetInvalidated {
		t.Errorf("the empty set was recorded as %q, which does not read as a bound of zero", got[0])
	}
}

// A failed emit is returned, not swallowed: nothing was recorded, and the caller is the
// one that decides whether a deployment which cannot describe its upstreams should run.
func TestRecordUpstreamSetReturnsAFailedEmit(t *testing.T) {
	boom := context.DeadlineExceeded
	c, _ := recordCtrl(t, true, config.Service{TargetURL: "http://vllm:8000/v1"}, boom)
	if err := c.RecordUpstreamSet(context.Background()); err == nil {
		t.Fatal("a failed emit was swallowed")
	}
	c2, _ := recordCtrl(t, true, config.Service{TargetURL: "http://vllm:8000/v1"}, boom)
	if err := c2.RecordUpstreamSetFromContent(context.Background(), validConfigContent); err == nil {
		t.Fatal("a failed emit on the config-change path was swallowed")
	}

	// And on the INVALIDATION, which is a different emit and was not covered: the two
	// cases above both reach the record path, so the fallback's error return had no test
	// and a mutation swallowing it survived.
	//
	// It matters because of what the caller does with the answer. ApplyCoreConfig aborts
	// the change when this returns an error; swallowing it lets the new config be written
	// while the ledger still holds the OLD set — a bound that is no longer the
	// deployment's, which is the one outcome the whole path exists to prevent.
	c3, _ := recordCtrl(t, true, config.Service{TargetURL: "http://vllm:8000/v1"}, boom)
	if err := c3.RecordUpstreamSetFromContent(context.Background(), "service:\n  name: unreadable\n"); err == nil {
		t.Fatal("a failed invalidation was swallowed, so the config would be written with the old set still recorded")
	}
}

// Neither path may block without bound.
//
// This is the one that could take a deployment down. main.go calls RecordUpstreamSet
// with context.Background() BEFORE it starts the attestation proxy and the HTTP server,
// and the dstack SDK adds no deadline of its own — so a hung /var/run/dstack.sock left
// the controller stuck at boot, and with it the proxy the broker needs for quotes and
// the /health endpoint that would have reported it. A record of where plaintext may go
// must not be able to take the deployment down to write itself.
//
// Asserted by inspecting the DEADLINE the emitter is handed, under the unbounded parent
// context main.go really passes — not by waiting for a hung socket to time out. Waiting
// is the obvious version and it cost 60 seconds of every run of this package for a
// property that is decided the moment the context is built; and it would have measured
// the length of the timeout rather than the existence of one, which is not the bug.
//
// It catches the same thing: with context.Background() passed straight through,
// ctx.Deadline() reports no deadline and this fails.
func TestNeitherRecordingPathBoundsItself(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(c *Ctrl) error
	}{
		{"the record", func(c *Ctrl) error { return c.RecordUpstreamSet(context.Background()) }},
		{"the config-change record", func(c *Ctrl) error { return c.RecordUpstreamSetFromContent(context.Background(), validConfigContent) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := recordCtrl(t, true, config.Service{TargetURL: "http://vllm:8000/v1"}, nil)
			seen := &deadlineCapturingEmitter{}
			c.emitter = seen

			before := time.Now()
			if err := tc.call(c); err != nil {
				t.Fatalf("%v", err)
			}
			if !seen.called {
				t.Fatal("nothing was emitted, so no deadline was observed and this proves nothing")
			}
			if !seen.hasDeadline {
				t.Fatal("the emit ran on a context with NO deadline: at boot that is a hung dstack socket wedging the controller before the attestation proxy and /health start")
			}
			// Bounded by the constant, allowing for the time between the two reads. The upper
			// bound is what matters; an implementation setting an hour would satisfy "has a
			// deadline" while still being a boot that never finishes in practice.
			if limit := before.Add(recordUpstreamSetTimeout + time.Second); seen.deadline.After(limit) {
				t.Errorf("deadline is %v past the budget: want at most %v from the call", seen.deadline.Sub(limit), recordUpstreamSetTimeout)
			}
		})
	}
}

// deadlineCapturingEmitter records whether the context it was handed carries a deadline,
// and when. It is the whole apparatus needed to answer "does this path bound itself",
// which a hanging socket answers far more slowly.
type deadlineCapturingEmitter struct {
	called      bool
	hasDeadline bool
	deadline    time.Time
}

func (e *deadlineCapturingEmitter) EmitEvent(ctx context.Context, event string, payload []byte) error {
	e.called = true
	e.deadline, e.hasDeadline = ctx.Deadline()
	return nil
}

// The naming rule, in isolation, because it is what a name derived from the set would
// have got wrong: it must depend only on the one upstream.
func TestUpstreamNameDependsOnNothingButTheUpstream(t *testing.T) {
	for _, tc := range []struct {
		url, identity, want string
		wantErr             bool
	}{
		{url: "http://vllm:8000/v1", want: "vllm"},
		{url: "http://0gm-sglang:8000/v1", want: "0gm-sglang"},
		{url: "http://phala-inference-guard:8000/v1", want: "phala-inference-guard"},
		{url: "http://qwavity-sia-vllm:8000/v1", want: "qwavity-sia-vllm"},
		{url: "http://api:9999/v1", want: "api"},
		{url: "https://openrouter.ai/api/v1", identity: "openrouter", want: "openrouter"},
		// An identity wins over the host, so moving a vendor's URL does not rename it.
		{url: "https://new.openrouter.ai/api/v1", identity: "openrouter", want: "openrouter"},
		// A dotted FQDN cannot be a name, and providerIdentity is the fix.
		{url: "https://openrouter.ai/api/v1", wantErr: true},
		{url: "http://[::1]:8000/v1", wantErr: true},
	} {
		t.Run(tc.url+"|"+tc.identity, func(t *testing.T) {
			got, err := upstreamName(tc.url, tc.identity)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("upstreamName(%q, %q) = %q, want a refusal", tc.url, tc.identity, got)
				}
				if !strings.Contains(err.Error(), "providerIdentity") {
					t.Errorf("the refusal does not name the fix: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("upstreamName(%q, %q) = %v", tc.url, tc.identity, err)
			}
			if got != tc.want {
				t.Errorf("upstreamName(%q, %q) = %q, want %q", tc.url, tc.identity, got, tc.want)
			}
		})
	}
}

// Adding an upstream must not rename the others. This is the property an ordinal suffix
// would break, and upstreamChanges diffs by name — so a rename it did not mean would be
// reported as a rewrite that never happened, in the one mechanism built to catch a
// deceptive rewrite.
func TestAddingAnUpstreamRenamesNothing(t *testing.T) {
	before := config.Service{
		TargetURL:        "https://m.example/v1",
		ProviderIdentity: "mmm",
		ModelPricing: []config.ModelPricingEntry{
			{Model: "b", TargetURL: "https://z.example/v1", ProviderIdentity: "zzz"},
		},
	}
	after := before
	after.ModelPricing = append([]config.ModelPricingEntry{
		// Sorts first by name AND by URL, so an ordinal scheme would renumber both others.
		{Model: "a", TargetURL: "https://a.example/v1", ProviderIdentity: "aaa"},
	}, before.ModelPricing...)

	nameOf := func(svc config.Service) map[string]string {
		members, err := upstreamsFromConfig(&svc)
		if err != nil {
			t.Fatalf("upstreamsFromConfig() = %v", err)
		}
		out := map[string]string{}
		for _, m := range members {
			out[m.URL] = m.Name
		}
		return out
	}

	was, is := nameOf(before), nameOf(after)
	for url, name := range was {
		if is[url] != name {
			t.Errorf("adding an upstream renamed %s from %q to %q", url, name, is[url])
		}
	}
	if len(is) != len(was)+1 {
		t.Errorf("the new set has %d members, want %d", len(is), len(was)+1)
	}
}

// The config path records the set the NEW CONTENT permits, before it writes the file.
//
// Two properties in one test, because each alone passes for the wrong reason:
//
//   - The payload must name the upstream the NEW content names, not the one c.fullConfig
//     holds. That singleton is a once.Do over the file on disk and nothing reloads it, so
//     a version deriving from it would record the OLD set on every config change and the
//     ordering assertion below would still pass.
//   - The record must be written while the OLD file is still on disk. The op log does not
//     record os.WriteFile, so position in the log cannot tell "before the write" from
//     "after it" — an earlier version of this test asserted that position and passed with
//     the call moved. What separates them is what is on disk at the moment of the emit.
//
// The content here also has to be something the LOADER accepts. An earlier version used
// "service:\n  name: before\n", and Service has no name field — so strict unmarshalling
// refused it, the path recorded an invalidation, and the test passed while exercising the
// fallback rather than the record.
func TestConfigChangeRecordsTheNewSetBeforeWritingTheFile(t *testing.T) {
	const before = "service:\n  targetUrl: http://vllm:8000/v1\n"
	const after = "service:\n  targetUrl: http://0gm-sglang:8000/v1\n"

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatalf("seeding the config file: %v", err)
	}

	l := &opLog{}
	c := newChangeCtrl(t, l, nil, path, okPull)
	// The switch the whole feature is behind, plus a running config that names a DIFFERENT
	// upstream than the new content — which is what makes the payload assertion below
	// distinguish "derived from the content" from "derived from the running config".
	c.config.RecordUpstreamSet = true
	c.fullConfig = &config.Config{Service: config.Service{TargetURL: "http://vllm:8000/v1"}}
	watcher := &fileWatchingEmitter{log: l, path: path, at: map[string]string{}}
	c.emitter = watcher

	if err := c.ApplyCoreConfig(context.Background(), after); err != nil {
		t.Fatalf("ApplyCoreConfig() = %v", err)
	}

	got := emitted(l)
	if len(got) != 1 {
		t.Fatalf("emitted %d upstream records, want 1: %q", len(got), got)
	}
	want := "count=1\n0gm-sglang http://0gm-sglang:8000/v1"
	if got[0] != want {
		t.Errorf("recorded\n %q\nwant\n %q\nthe set must come from the new content, not from the running config", got[0], want)
	}

	onDisk, recorded := watcher.at[attest.EventUpstreamSet]
	if !recorded {
		t.Fatalf("ops = %v, want the set recorded", l.all())
	}
	if onDisk != before {
		t.Errorf("the set was recorded with %q already on disk; it must be recorded while the old config is still there, or a quote taken in between names a set the deployment has moved past", onDisk)
	}

	// The restart is what publishes the record, since a broker seals its quote at start.
	if i := indexOfOp(l.all(), "restart broker"); i < 0 {
		t.Errorf("ops = %v, want the broker restarted in the same call", l.all())
	}
	// And the new content did land, so none of the above is passing because the change
	// was aborted.
	onDiskAfter, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the config file back: %v", err)
	}
	if string(onDiskAfter) != after {
		t.Errorf("config file = %q, want %q: the change did not happen, so the ordering above proves nothing", onDiskAfter, after)
	}
}

// Content the loader would refuse is recorded as UNREADABLE, never skipped.
//
// Skipping would leave the previous record standing, and that record now describes a
// config the deployment has replaced — a bound that is no longer the deployment's, which
// a reader trusts. Unknown is the honest answer.
//
// The change itself still goes through: ApplyCoreConfig validates YAML shape and not
// schema, and refusing here would make the controller accept or reject the same config
// depending on whether this switch is on.
func TestConfigChangeRecordsUnknownWhenTheNewContentCannotBeRead(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
	}{
		// Valid YAML, and a key the Service struct does not have — which is exactly what
		// the broker's own strict loader will refuse when it restarts onto this file.
		{"a key the loader does not know", "service:\n  name: whatever\n"},
		{"a value of the wrong type", "service:\n  targetUrl:\n    nested: yes\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(path, []byte("service:\n  targetUrl: http://vllm:8000/v1\n"), 0o644); err != nil {
				t.Fatalf("seeding the config file: %v", err)
			}

			l := &opLog{}
			c := newChangeCtrl(t, l, nil, path, okPull)
			c.config.RecordUpstreamSet = true
			c.fullConfig = &config.Config{Service: config.Service{TargetURL: "http://vllm:8000/v1"}}

			if err := c.ApplyCoreConfig(context.Background(), tc.content); err != nil {
				t.Fatalf("ApplyCoreConfig() = %v, want the change to go through with the set recorded as unknown", err)
			}
			got := emitted(l)
			if len(got) != 1 || got[0] != upstreamSetInvalidated {
				t.Fatalf("recorded %q, want the invalidation %q", got, upstreamSetInvalidated)
			}
			// And specifically NOT the old set, which is what skipping would have left.
			for _, payload := range got {
				if strings.Contains(payload, "vllm") {
					t.Errorf("recorded %q, which still names the old upstream", payload)
				}
			}
		})
	}
}

// fileWatchingEmitter records what the config file held at the moment each event was
// emitted, which is what separates "recorded before the write" from "recorded after
// it" — os.WriteFile leaves no op in the log.
//
// One entry per event name, which is enough here: the config path emits each of the two
// records once. A path emitting one twice would overwrite, and this would need a slice.
type fileWatchingEmitter struct {
	log  *opLog
	path string
	at   map[string]string
	err  error
}

func (e *fileWatchingEmitter) EmitEvent(ctx context.Context, event string, payload []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	content, readErr := os.ReadFile(e.path)
	if readErr != nil {
		// Recorded as the empty string rather than ignored, so a test asserting the old
		// content fails loudly instead of comparing against a value nobody read.
		content = nil
	}
	e.at[event] = string(content)
	e.log.add("emit " + event + " " + string(payload))
	return e.err
}

// A deployment that never recorded a set must not emit the invalidation either: it has
// nothing to supersede, and that record would move it from "no record was written" to
// "a record was written and could not be read" — while being the first zg-upstream-set
// event it ever emitted, which is the hard-fail the switch exists to keep off.
func TestConfigChangeEmitsNoUpstreamRecordWhenOff(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("service:\n  name: before\n"), 0o644); err != nil {
		t.Fatalf("seeding the config file: %v", err)
	}

	l := &opLog{}
	c := newChangeCtrl(t, l, nil, path, okPull)
	c.fullConfig = &config.Config{Service: config.Service{TargetURL: "http://vllm:8000/v1"}}

	if err := c.ApplyCoreConfig(context.Background(), "service:\n  name: after\n"); err != nil {
		t.Fatalf("ApplyCoreConfig() = %v", err)
	}
	for _, op := range l.all() {
		if strings.Contains(op, attest.EventUpstreamSet) {
			t.Errorf("emitted %q with the switch off", op)
		}
	}
}

// indexOfOp is opLog.indexOf over an already-taken snapshot, so the ordering assertions
// above compare positions within ONE snapshot rather than re-reading the log between
// them.
func indexOfOp(ops []string, want string) int {
	for i, op := range ops {
		if op == want {
			return i
		}
	}
	return -1
}

// The same config must record the same bytes however its entries are ordered, because a
// writer re-emits its whole table at every boot and RTMR3 is cleared at every one — a
// payload that varied would read as a change that did not happen.
func TestRecordIsAFunctionOfTheConfigNotItsOrder(t *testing.T) {
	a := config.Service{
		TargetURL:        "https://m.example/v1",
		ProviderIdentity: "mmm",
		ModelPricing: []config.ModelPricingEntry{
			{Model: "x", TargetURL: "https://z.example/v1", ProviderIdentity: "zzz"},
			{Model: "y", TargetURL: "https://a.example/v1", ProviderIdentity: "aaa"},
		},
	}
	b := a
	b.ModelPricing = []config.ModelPricingEntry{a.ModelPricing[1], a.ModelPricing[0]}

	ca, la := recordCtrl(t, true, a, nil)
	cb, lb := recordCtrl(t, true, b, nil)
	if err := ca.RecordUpstreamSet(context.Background()); err != nil {
		t.Fatalf("a: %v", err)
	}
	if err := cb.RecordUpstreamSet(context.Background()); err != nil {
		t.Fatalf("b: %v", err)
	}
	if ga, gb := emitted(la), emitted(lb); len(ga) != 1 || len(gb) != 1 || ga[0] != gb[0] {
		t.Errorf("the same config recorded two ways:\n %q\n %q", ga, gb)
	}
}
