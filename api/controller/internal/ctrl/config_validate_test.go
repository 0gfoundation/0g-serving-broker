package ctrl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The hole this file exists for, and it was not an oversight — it was tested behaviour.
//
// ApplyCoreConfig used to validate YAML SYNTAX only, and
// TestConfigChangeRecordsUnknownWhenTheNewContentCannotBeRead asserted that content "the
// broker's own strict loader will refuse when it restarts onto this file" was accepted,
// recorded and applied. Measured on a live deployment against the zgTestnetDev contract:
//
//	PUT /v1/config/core  (service.modelPricing with no providerType)
//	  → 200 {"message":"config updated and containers restarted"}
//	  → zg-config-update and zg-upstream-set appended to RTMR3
//	  → signing key rotated to the new set's hash
//	  → config written, broker restarted
//	  → broker: panic: invalid config: service.modelPricing is only supported when
//	    providerType is 'centralized' or 'standard'
//
// The outage is the smaller half. RTMR3 only appends, so the ledger permanently named a
// config the deployment never ran; the controller derived keys for the new set while the
// chain still acknowledged the old set's signer; and the broker — the only party that
// pushes the new address — could not start. Three sources disagreeing, none retractable.
func TestConfigChangeRefusesContentTheBrokerCouldNotLoad(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		want    string
	}{{
		// The same kind of shape that took the live deployment down: modelPricing the
		// loader refuses. That exact config (modelPricing with no providerType) is
		// still refused, but now for a different reason (decentralized multi-model
		// needs an in-CVM service.targetUrl), so this uses a modality multi-model
		// pricing is refused on outright, whose message says so.
		name:    "modelPricing on a modality that cannot resolve it",
		content: "service:\n  model: m\n  type: text-to-image\n  modelPricing:\n    - model: m\n      inputPrice: \"1\"\n      outputPrice: \"1\"\n",
		want:    "modelPricing is only supported for service type",
	}, {
		name:    "a value of the wrong type",
		content: "service:\n  targetUrl:\n    nested: yes\n",
		want:    "cannot unmarshal",
	}, {
		name:    "not YAML at all",
		content: "\tthis: [is not\n",
		want:    "yaml",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			const before = "service:\n  model: before\n"
			if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
				t.Fatalf("seeding the config file: %v", err)
			}

			l := &opLog{}
			c := newChangeCtrl(t, l, nil, path, okPull)
			c.config.RecordUpstreamSet = true

			err := c.ApplyCoreConfig(context.Background(), tc.content)
			if err == nil {
				t.Fatalf("ApplyCoreConfig() = nil, want a refusal")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("ApplyCoreConfig() = %v, want it to mention %q", err, tc.want)
			}

			// Nothing recorded. This is the assertion that matters: RTMR3 only appends, so
			// a record written here could not be taken back once the broker refused to
			// start on the config it names.
			if ops := l.all(); len(ops) != 0 {
				t.Errorf("ops = %v, want a refusal to record nothing", ops)
			}
			// No key rotated. A signer derived for a set the deployment never serves is one
			// the chain's acknowledgement no longer matches.
			if hash := c.boundUpstreamSetHash(); hash != "" {
				t.Errorf("bound set hash = %q, want nothing bound", hash)
			}
			// And the file the running containers are on is untouched.
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading the config file: %v", err)
			}
			if string(got) != before {
				t.Errorf("config file = %q, want the old content %q", got, before)
			}
		})
	}
}

// The other half of the split: content that LOADS but whose upstreams cannot be expressed
// as a record still goes through, with the set recorded as unreadable.
//
// These two were conflated while the check was syntax-only, and keeping them apart is the
// point. A config that does not load must be refused; a config that loads and simply
// cannot be described in the record grammar is a deployment the operator is entitled to
// run, with a ledger that says so.
func TestConfigChangeStillAppliesWhatItCannotDescribe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("service:\n  model: before\n"), 0o644); err != nil {
		t.Fatalf("seeding the config file: %v", err)
	}

	l := &opLog{}
	c := newChangeCtrl(t, l, nil, path, okPull)
	c.config.RecordUpstreamSet = true

	// A dotted vendor FQDN with no providerIdentity: loads fine, and the host cannot be a
	// record name. Measured on five deprecated deployments, so this is a real shape.
	const content = "service:\n  model: m\n  targetUrl: https://api.red-pill.ai/v1\n"
	if err := c.ApplyCoreConfig(context.Background(), content); err != nil {
		t.Fatalf("ApplyCoreConfig() = %v, want the change to go through", err)
	}

	var sets []string
	for _, op := range l.all() {
		if strings.HasPrefix(op, "emit zg-upstream-set ") {
			sets = append(sets, strings.TrimPrefix(op, "emit zg-upstream-set "))
		}
	}
	if len(sets) != 1 || sets[0] != upstreamSetInvalidated {
		t.Errorf("recorded %q, want the invalidation %q", sets, upstreamSetInvalidated)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the config file: %v", err)
	}
	if string(got) != content {
		t.Errorf("config file = %q, want the new content", got)
	}
}

// A key the broker code does not know is ignored by the loader (config.decodeConfig), so
// the controller applies it too — when the broker runs the controller's own image, which
// is the case this fake daemon describes (every container resolves to the same digest).
// This used to be refused, which made every new config key wait for a full redeploy of
// the controller.
func TestConfigChangeAppliesKeysTheBrokerIgnores(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("service:\n  model: before\n"), 0o644); err != nil {
		t.Fatalf("seeding the config file: %v", err)
	}
	l := &opLog{}
	c := newChangeCtrl(t, l, nil, path, okPull)

	const content = "service:\n  model: after\n  futureKnob: 1\nfutureFeature:\n  enabled: true\n"
	if err := c.ApplyCoreConfig(context.Background(), content); err != nil {
		t.Fatalf("ApplyCoreConfig() = %v, want unknown keys applied when the broker runs the controller's image", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the config file: %v", err)
	}
	if string(got) != content {
		t.Errorf("config file = %q, want the new content", got)
	}
}
