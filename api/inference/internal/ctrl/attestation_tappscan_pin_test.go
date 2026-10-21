package ctrl

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/0glabs/0g-serving-broker/common/tapp"
	"github.com/0glabs/0g-serving-broker/inference/config"
)

// The pin the scan transport uses must never be weaker than the configured
// one: a failed bootstrap falls back to the static pin, a bootstrap that
// fails after a success keeps the verified key, and a success replaces both.
func TestTappscanPinsFallback(t *testing.T) {
	static := bytes.Repeat([]byte{0xaa}, 32)
	chainKey := bytes.Repeat([]byte{0xbb}, 32)
	newKey := bytes.Repeat([]byte{0xcc}, 32)

	p := &tappscanPins{static: static}
	if got := p.get(); !bytes.Equal(got, static) || p.source() != "config" {
		t.Fatalf("before any bootstrap: pin %x source %s", got, p.source())
	}

	p.set(tappscanPinState{detail: "rpc down"})
	if got := p.get(); !bytes.Equal(got, static) || p.source() != "config" {
		t.Fatalf("failed bootstrap must keep the static pin: %x %s", got, p.source())
	}

	t0 := time.Unix(1_700_000_000, 0)
	p.set(tappscanPinState{ok: true, pin: chainKey, signer: "0x01", teeURL: "https://scan:50052", checkedAt: t0, detail: "verified"})
	if got := p.get(); !bytes.Equal(got, chainKey) || p.source() != "chain" {
		t.Fatalf("successful bootstrap must win: %x %s", got, p.source())
	}

	p.set(tappscanPinState{detail: "evidence: connection refused", checkedAt: t0.Add(time.Hour)})
	st := p.state()
	if got := p.get(); !bytes.Equal(got, chainKey) || !st.ok || !st.checkedAt.Equal(t0) {
		t.Fatalf("a later failure must keep the verified key and its timestamp: %x ok=%v at=%s", got, st.ok, st.checkedAt)
	}
	if st.detail == "" || st.detail == "evidence: connection refused" {
		t.Fatalf("detail must say which key is kept and why this cycle failed: %q", st.detail)
	}

	p.set(tappscanPinState{ok: true, pin: newKey, checkedAt: t0.Add(2 * time.Hour)})
	if got := p.get(); !bytes.Equal(got, newKey) {
		t.Fatalf("a new verified key replaces the old one: %x", got)
	}

	// No static pin and no success yet: nothing to pin with, so the fetch
	// fails closed rather than connecting unpinned.
	q := &tappscanPins{}
	q.set(tappscanPinState{detail: "first cycle failed"})
	if got := q.get(); got != nil || q.source() != "none" {
		t.Fatalf("no pin available must stay nil: %x %s", got, q.source())
	}
}

// The signed statement carries where the pin came from and, under
// pinSource: chain, the bootstrap's own check table, so a reader can judge
// the anchor instead of taking "pin ok" on faith.
func TestScanStatementCarriesPinProvenance(t *testing.T) {
	var cfg config.AssayAttestation
	cfg.AppID = "assay-verifier"
	cfg.Tappscan.URL = "https://scan.example"
	cfg.Tappscan.PinSource = "chain"
	cfg.OnFail = "block-settlement"
	chainKey := bytes.Repeat([]byte{0xbb}, 32)
	s := &scanSource{cfg: cfg, provider: "0xprovider", pins: &tappscanPins{}}
	s.pins.set(tappscanPinState{ok: true, pin: chainKey, signer: "0x967a", teeURL: "https://34.122.75.6:50052",
		checkedAt: time.Unix(1_760_000_000, 0), result: &tapp.Result{OK: true, TcbStatus: "OutOfDate"}})
	ps := s.pins.state()
	s.last = &scanEval{observedAt: time.Unix(1_760_000_100, 0), ok: true,
		facts: scanFacts{TappscanPinSource: s.pins.source(), TappscanSigner: ps.signer, TappscanTeeURL: ps.teeURL,
			TappscanVerifiedAt: ps.checkedAt.Unix(), TappscanBootstrap: ps.result}}

	body, sig, err := s.statement("", false)
	if err != nil || sig != nil {
		t.Fatalf("statement: %v sig=%x", err, sig)
	}
	var st struct {
		Scan struct {
			TlsPubkey string `json:"tls_pubkey_sha256"`
		} `json:"scan"`
		Facts struct {
			Source     string `json:"tappscan_pin_source"`
			Signer     string `json:"tappscan_signer"`
			TeeURL     string `json:"tappscan_tee_url"`
			VerifiedAt int64  `json:"tappscan_verified_at"`
			Bootstrap  struct {
				OK  bool   `json:"ok"`
				Tcb string `json:"tcb_status"`
			} `json:"tappscan_bootstrap"`
		} `json:"facts"`
	}
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatal(err)
	}
	if st.Scan.TlsPubkey != "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Errorf("statement must publish the pin in use: %s", st.Scan.TlsPubkey)
	}
	f := st.Facts
	if f.Source != "chain" || f.Signer != "0x967a" || f.TeeURL != "https://34.122.75.6:50052" || f.VerifiedAt != 1_760_000_000 || !f.Bootstrap.OK || f.Bootstrap.Tcb != "OutOfDate" {
		t.Errorf("facts: %+v", f)
	}
}
