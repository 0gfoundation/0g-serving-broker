package ctrl

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/0glabs/0g-serving-broker/common/attest"
	"github.com/0glabs/0g-serving-broker/controller/internal/attestproxy"
	"github.com/0glabs/0g-serving-broker/inference/config"
)

// bindCtrl is a Ctrl with everything the binding path touches: the switch, a service to
// derive a set from, a docker daemon that can answer what the broker runs, and a deriver.
//
// recordCtrl deliberately has none of the last two, which is why every test using it
// exercises the UNBOUND path — worth stating, because that is what made the binding
// untested when it was first written.
func bindCtrl(t *testing.T, svc config.Service, proxy bool, derr error) (*Ctrl, *opLog, *fakeDeriver) {
	t.Helper()
	if proxy {
		t.Setenv(attestproxy.SocketEnvVar, "/var/run/zg-tee/tee.sock")
	} else {
		t.Setenv(attestproxy.SocketEnvVar, "")
	}
	l := &opLog{}
	d := &fakeDeriver{log: l, err: derr}
	return &Ctrl{
		config:       config.ControllerConfig{RecordUpstreamSet: true, ImageRepo: imageRepo},
		fullConfig:   &config.Config{Service: svc},
		dockerClient: fakeChangeDaemon(t, l, okPull),
		emitter:      &fakeEmitter{log: l},
		deriver:      d,
		logger:       testLogger(t),
	}, l, d
}

func imageRecords(l *opLog) []string {
	var out []string
	for _, op := range l.all() {
		if strings.HasPrefix(op, "emit "+attest.EventImageUpdate+" ") {
			out = append(out, strings.TrimPrefix(op, "emit "+attest.EventImageUpdate+" "))
		}
	}
	return out
}

// The whole point: after recording a set, the keys are derived FOR that set, and a record
// says so.
//
// Three assertions, and each catches a different way of getting it wrong:
//
//   - the derivation was asked for with the set hash, so the key really is a function of
//     the set rather than of the image alone
//   - CurrentKeyIdentity reports it, so the attestation proxy hands the broker that same
//     key rather than the unbound one
//   - an image record carries the derived signer, so a reader can compare it against the
//     quote's report_data — binding nothing can be checked against is not binding
func TestRecordingASetBindsTheKeysToIt(t *testing.T) {
	svc := config.Service{TargetURL: "http://vllm:8000/v1"}
	c, l, d := bindCtrl(t, svc, true, nil)

	if err := c.RecordUpstreamSet(context.Background()); err != nil {
		t.Fatalf("RecordUpstreamSet() = %v", err)
	}

	// The set the record names, hashed the way attest hashes it — computed here from the
	// same members rather than copied from the implementation, so the two must agree.
	want, err := (&attest.RunningState{
		Upstreams:      []attest.Upstream{{Name: "vllm", URL: "http://vllm:8000/v1"}},
		UpstreamsState: attest.UpstreamsKnown,
	}).UpstreamSetHash()
	if err != nil {
		t.Fatalf("hashing the expected set: %v", err)
	}

	if len(d.seenIdentities) != 1 {
		t.Fatalf("derived %d times, want 1: %+v", len(d.seenIdentities), d.seenIdentities)
	}
	if got := d.seenIdentities[0].UpstreamSetHash; got != want {
		t.Errorf("derived for set hash %q, want %q: the key is not a function of the set", got, want)
	}

	id, err := c.CurrentKeyIdentity(context.Background())
	if err != nil {
		t.Fatalf("CurrentKeyIdentity() = %v", err)
	}
	if id.UpstreamSetHash != want {
		t.Errorf("CurrentKeyIdentity reports %q, want %q: the proxy would hand the broker the unbound key", id.UpstreamSetHash, want)
	}

	recs := imageRecords(l)
	if len(recs) != 1 {
		t.Fatalf("wrote %d image records, want 1: %q", len(recs), recs)
	}
	if fields := strings.Fields(recs[0]); len(fields) != 3 {
		t.Errorf("image record %q has %d fields, want <ref> <signer> <encPub>", recs[0], len(fields))
	}
}

// A different set must derive a different key, or none of the accountability follows.
func TestADifferentSetBindsADifferentKey(t *testing.T) {
	a, _, da := bindCtrl(t, config.Service{TargetURL: "http://vllm:8000/v1"}, true, nil)
	b, _, db := bindCtrl(t, config.Service{TargetURL: "http://0gm-sglang:8000/v1"}, true, nil)

	for _, c := range []*Ctrl{a, b} {
		if err := c.RecordUpstreamSet(context.Background()); err != nil {
			t.Fatalf("RecordUpstreamSet() = %v", err)
		}
	}
	if len(da.seenIdentities) != 1 || len(db.seenIdentities) != 1 {
		t.Fatalf("derived %d and %d times, want 1 each", len(da.seenIdentities), len(db.seenIdentities))
	}

	ha, hb := da.seenIdentities[0].UpstreamSetHash, db.seenIdentities[0].UpstreamSetHash
	if ha == "" || hb == "" {
		t.Fatalf("a set bound no hash: %q / %q", ha, hb)
	}
	if ha == hb {
		t.Errorf("two different sets bound the same hash %q, so changing the set would not rotate the signer", ha)
	}
	// And therefore different derivation paths, which is where the rotation actually
	// happens.
	if attestproxy.SignerKeyPath(da.seenIdentities[0]) == attestproxy.SignerKeyPath(db.seenIdentities[0]) {
		t.Error("two different sets share a signer derivation path")
	}
}

// Without the attestation proxy there is nothing to bind, so the set is recorded and left
// UNBOUND rather than bound to a key the broker will never use.
//
// A record naming a controller-derived address on such a deployment would name one no
// quote can ever match, permanently — which is what UpdateImages refuses outright for the
// digest. Here the set record still says something true on its own, so it is written.
func TestWithoutTheProxyTheSetIsRecordedButNothingIsBound(t *testing.T) {
	c, l, d := bindCtrl(t, config.Service{TargetURL: "http://vllm:8000/v1"}, false, nil)

	if err := c.RecordUpstreamSet(context.Background()); err != nil {
		t.Fatalf("RecordUpstreamSet() = %v", err)
	}
	if got := emitted(l); len(got) != 1 || !strings.HasPrefix(got[0], "count=") {
		t.Fatalf("recorded %q, want the set", got)
	}
	if recs := imageRecords(l); len(recs) != 0 {
		t.Errorf("wrote an image record with no proxy: %q — it would name an address no quote can match", recs)
	}
	if len(d.seenIdentities) != 0 {
		t.Errorf("derived %d times with no proxy: %+v", len(d.seenIdentities), d.seenIdentities)
	}
	id, err := c.CurrentKeyIdentity(context.Background())
	if err != nil {
		t.Fatalf("CurrentKeyIdentity() = %v", err)
	}
	if id.UpstreamSetHash != "" {
		t.Errorf("bound %q with no proxy, so the broker's own key would disagree with it", id.UpstreamSetHash)
	}
}

// A set that cannot be stated must leave nothing bound. Whatever was bound describes the
// set this invalidation supersedes, and a key still derived for it is a key the ledger no
// longer describes.
func TestAnInvalidationUnbinds(t *testing.T) {
	c, l, _ := bindCtrl(t, config.Service{TargetURL: "http://vllm:8000/v1"}, true, nil)
	if err := c.RecordUpstreamSet(context.Background()); err != nil {
		t.Fatalf("seeding the bound set: %v", err)
	}
	if c.boundUpstreamSetHash() == "" {
		t.Fatal("nothing was bound, so this test proves nothing")
	}

	// A config the grammar cannot express: a vendor with no identity, whose host is a
	// dotted FQDN and cannot be a member name.
	c.fullConfig = &config.Config{Service: config.Service{TargetURL: "https://openrouter.ai/api/v1"}}
	if err := c.RecordUpstreamSet(context.Background()); err != nil {
		t.Fatalf("RecordUpstreamSet() = %v, want an invalidation", err)
	}

	if got := emitted(l); len(got) == 0 || got[len(got)-1] != upstreamSetInvalidated {
		t.Fatalf("last upstream record is %q, want the invalidation", got)
	}
	if h := c.boundUpstreamSetHash(); h != "" {
		t.Errorf("still bound to %q after an invalidation", h)
	}
	id, err := c.CurrentKeyIdentity(context.Background())
	if err != nil {
		t.Fatalf("CurrentKeyIdentity() = %v", err)
	}
	if id.UpstreamSetHash != "" {
		t.Errorf("CurrentKeyIdentity still reports %q", id.UpstreamSetHash)
	}
}

// A derivation this cannot complete leaves the set recorded and nothing bound, rather than
// binding a hash whose key nothing describes.
func TestADerivationFailureLeavesNothingBound(t *testing.T) {
	c, l, _ := bindCtrl(t, config.Service{TargetURL: "http://vllm:8000/v1"}, true, errors.New("dstack is down"))

	if err := c.RecordUpstreamSet(context.Background()); err != nil {
		t.Fatalf("RecordUpstreamSet() = %v, want the set recorded unbound", err)
	}
	if got := emitted(l); len(got) != 1 || !strings.HasPrefix(got[0], "count=") {
		t.Fatalf("recorded %q, want the set", got)
	}
	if recs := imageRecords(l); len(recs) != 0 {
		t.Errorf("wrote an image record after a failed derivation: %q", recs)
	}
	if h := c.boundUpstreamSetHash(); h != "" {
		t.Errorf("bound %q after a failed derivation, so the broker would derive a key nothing describes", h)
	}
}

// A digest this cannot pin down leaves nothing bound, and that guard is not redundant with
// the one below it.
//
// CurrentKeyIdentity resolves the digest too, so while the lookup keeps failing the proxy
// refuses anyway and a bound hash would go unused. What makes this matter is the lookup
// recovering: a transient failure at record time, resolvable a moment later, would leave
// the hash bound with NO image record written — so the broker derives the bound key while
// the ledger names the previous signer, or none. That mismatch is unrecoverable for the
// boot, because RTMR3 only appends.
func TestAnUnresolvableDigestLeavesNothingBound(t *testing.T) {
	t.Setenv(attestproxy.SocketEnvVar, "/var/run/zg-tee/tee.sock")

	// The same fixture running_digest_test.go uses for "built locally and never pushed":
	// a container whose image carries no RepoDigests, so no digest can be established.
	c, _ := digestCtrl(t, "0g-serving-broker:dev", runningImageID, map[string][]string{runningImageID: {}})
	l := &opLog{}
	d := &fakeDeriver{log: l}
	c.config = config.ControllerConfig{RecordUpstreamSet: true, ImageRepo: imageRepo}
	c.fullConfig = &config.Config{Service: config.Service{TargetURL: "http://vllm:8000/v1"}}
	c.emitter = &fakeEmitter{log: l}
	c.deriver = d
	c.logger = testLogger(t)

	if err := c.RecordUpstreamSet(context.Background()); err != nil {
		t.Fatalf("RecordUpstreamSet() = %v, want the set recorded unbound", err)
	}
	if got := emitted(l); len(got) != 1 || !strings.HasPrefix(got[0], "count=") {
		t.Fatalf("recorded %q, want the set", got)
	}
	if recs := imageRecords(l); len(recs) != 0 {
		t.Errorf("wrote an image record without a digest: %q", recs)
	}
	if len(d.seenIdentities) != 0 {
		t.Errorf("derived %d times without a digest: %+v", len(d.seenIdentities), d.seenIdentities)
	}
	if h := c.boundUpstreamSetHash(); h != "" {
		t.Errorf("bound %q with no digest, so a recovered lookup would derive a key no record names", h)
	}
}

// A failed image record must unbind and report, because the alternative is a broker
// publishing an address no record names.
func TestAFailedImageRecordUnbindsAndReports(t *testing.T) {
	c, _, _ := bindCtrl(t, config.Service{TargetURL: "http://vllm:8000/v1"}, true, nil)
	// The set record has to succeed and the image record has to fail, so the emitter fails
	// only on the second event.
	l := &opLog{}
	c.emitter = &failNthEmitter{log: l, failOn: attest.EventImageUpdate}

	err := c.RecordUpstreamSet(context.Background())
	if err == nil {
		t.Fatal("a failed image record was swallowed")
	}
	if !strings.Contains(err.Error(), "binding") {
		t.Errorf("error %q does not say the binding failed", err)
	}
	if h := c.boundUpstreamSetHash(); h != "" {
		t.Errorf("bound %q after the record failed, so the broker would publish an address no record names", h)
	}
}

// failNthEmitter records everything and refuses one event name, which is what separates
// "the set was recorded" from "the binding was recorded".
type failNthEmitter struct {
	log    *opLog
	failOn string
}

func (e *failNthEmitter) EmitEvent(ctx context.Context, event string, payload []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	e.log.add("emit " + event + " " + string(payload))
	if event == e.failOn {
		return errors.New("refused")
	}
	return nil
}

// The bound hash must be the one attest computes for the same set — the writer and the
// reader deriving different hashes would mean two keys for one deployment, which is the
// failure the whole encoding is arranged to prevent.
func TestTheBoundHashIsTheOneAttestComputes(t *testing.T) {
	svc := config.Service{
		TargetURL:        "https://openrouter.ai/api/v1",
		ProviderIdentity: "openrouter",
		ModelPricing: []config.ModelPricingEntry{
			{Model: "a", TargetURL: "http://0gm-sglang:8000/v1", ProviderIdentity: "sglang"},
		},
	}
	c, _, _ := bindCtrl(t, svc, true, nil)
	if err := c.RecordUpstreamSet(context.Background()); err != nil {
		t.Fatalf("RecordUpstreamSet() = %v", err)
	}

	members, err := upstreamsFromConfig(&svc)
	if err != nil {
		t.Fatalf("upstreamsFromConfig() = %v", err)
	}
	want, err := (&attest.RunningState{Upstreams: members, UpstreamsState: attest.UpstreamsKnown}).UpstreamSetHash()
	if err != nil {
		t.Fatalf("UpstreamSetHash() = %v", err)
	}
	if got := c.boundUpstreamSetHash(); got != want {
		t.Errorf("bound %q, want %q", got, want)
	}
}
