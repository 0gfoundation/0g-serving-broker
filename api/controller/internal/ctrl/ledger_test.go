package ctrl

import (
	"context"
	"testing"

	"github.com/0glabs/0g-serving-broker/common/attest"
	"github.com/0glabs/0g-serving-broker/inference/config"
)

func TestParseLedger(t *testing.T) {
	for in, want := range map[string]ledgerMode{"": ledgerRTMR3, "rtmr3": ledgerRTMR3, " none ": ledgerNone} {
		got, err := parseLedger(in)
		if err != nil || got != want {
			t.Errorf("parseLedger(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	// Anything else decides nothing: whether the controller's records exist is not a value
	// to guess at.
	for _, in := range []string{"None", "off", "false", "0.6"} {
		if _, err := parseLedger(in); err == nil {
			t.Errorf("parseLedger(%q) accepted", in)
		}
	}
}

// noLedgerCtrl is bindCtrl wired the way NewCtrl wires a CVM without a ledger, with the
// recordUpstreamSet switch OFF — the default every deployment has.
func noLedgerCtrl(t *testing.T, svc config.Service) (*Ctrl, *fakeDeriver) {
	t.Helper()
	c, _, d := bindCtrl(t, svc, true, nil)
	c.config.RecordUpstreamSet = false
	c.ledger = ledgerNone
	c.emitter = noLedger{}
	return c, d
}

func setHash(t *testing.T, members ...attest.Upstream) string {
	t.Helper()
	h, err := (&attest.RunningState{Upstreams: members, UpstreamsState: attest.UpstreamsKnown}).UpstreamSetHash()
	if err != nil {
		t.Fatalf("hashing the expected set: %v", err)
	}
	return h
}

// The hole on dstack 0.6 (report-data-v2.md §4.4): the binding ran only after a successful
// emit, every emit fails there, so the keys stayed unbound and changing where plaintext may
// go rotated no signer. Without a ledger the set is bound anyway — with the switch off, since
// the switch comes from the unmeasured config file (§4.5) — and the proxy derives for it.
func TestWithoutALedgerTheSetIsBoundEvenWithTheSwitchOff(t *testing.T) {
	c, d := noLedgerCtrl(t, config.Service{TargetURL: "http://vllm:8000/v1"})

	if err := c.RecordUpstreamSet(context.Background()); err != nil {
		t.Fatalf("RecordUpstreamSet() = %v", err)
	}
	want := setHash(t, attest.Upstream{Name: "vllm", URL: "http://vllm:8000/v1"})
	if got := c.boundUpstreamSetHash(); got != want {
		t.Fatalf("bound %q, want %q", got, want)
	}
	id, err := c.CurrentKeyIdentity(context.Background())
	if err != nil {
		t.Fatalf("CurrentKeyIdentity() = %v", err)
	}
	if id.UpstreamSetHash != want {
		t.Errorf("the proxy derives for %q, want the bound set %q", id.UpstreamSetHash, want)
	}
	// No image record to write, so nothing to derive here: the proxy derives on demand.
	if len(d.seenIdentities) != 0 {
		t.Errorf("derived %d times while binding, want none: %+v", len(d.seenIdentities), d.seenIdentities)
	}
}

// Binding without a ledger is storing a hash computed from the config, so it must not depend
// on the broker container — which at boot may not be running yet.
func TestWithoutALedgerBindingDoesNotNeedTheBroker(t *testing.T) {
	c, _ := digestCtrl(t, "0g-serving-broker:dev", runningImageID, map[string][]string{runningImageID: {}})
	c.fullConfig = &config.Config{Service: config.Service{TargetURL: "http://vllm:8000/v1"}}
	c.ledger = ledgerNone
	c.emitter = noLedger{}
	c.deriver = &fakeDeriver{log: &opLog{}}
	c.logger = testLogger(t)
	t.Setenv("ATTEST_PROXY_SOCKET", "/var/run/zg-tee/tee.sock")

	if err := c.RecordUpstreamSet(context.Background()); err != nil {
		t.Fatalf("RecordUpstreamSet() = %v, want it bound without a digest", err)
	}
	if c.boundUpstreamSetHash() == "" {
		t.Error("nothing bound")
	}
}

// With a ledger, an unreadable set is recorded as invalidated and a reader refuses it.
// Without one nothing would say so, and the unbound key would serve a deployment whose
// destinations nobody can name — so no key is served, until a readable set is bound.
func TestWithoutALedgerAnUnreadableSetServesNoKey(t *testing.T) {
	// A public vendor with no providerIdentity cannot be named in the set (see
	// upstreamsFromConfig), which is the invalidation path.
	c, _ := noLedgerCtrl(t, config.Service{TargetURL: "https://api.example.com/v1"})

	if err := c.RecordUpstreamSet(context.Background()); err != nil {
		t.Fatalf("RecordUpstreamSet() = %v", err)
	}
	if _, err := c.CurrentKeyIdentity(context.Background()); err == nil {
		t.Fatal("CurrentKeyIdentity() served an identity for a set nobody can state")
	}

	c.fullConfig.Service = config.Service{TargetURL: "http://vllm:8000/v1"}
	if err := c.RecordUpstreamSet(context.Background()); err != nil {
		t.Fatalf("RecordUpstreamSet() = %v", err)
	}
	if _, err := c.CurrentKeyIdentity(context.Background()); err != nil {
		t.Errorf("CurrentKeyIdentity() = %v after a readable set was bound", err)
	}
}

// With a ledger nothing changes for the deployments running today: the switch is off
// everywhere, and off still means nothing recorded and nothing bound.
func TestWithALedgerTheSwitchStillDecides(t *testing.T) {
	c, l, d := bindCtrl(t, config.Service{TargetURL: "http://vllm:8000/v1"}, true, nil)
	c.config.RecordUpstreamSet = false

	if err := c.RecordUpstreamSet(context.Background()); err != nil {
		t.Fatalf("RecordUpstreamSet() = %v", err)
	}
	if ops := l.all(); len(ops) != 0 {
		t.Errorf("did %q with the switch off", ops)
	}
	if h := c.boundUpstreamSetHash(); h != "" || len(d.seenIdentities) != 0 {
		t.Errorf("bound %q / derived %d times with the switch off", h, len(d.seenIdentities))
	}
}
