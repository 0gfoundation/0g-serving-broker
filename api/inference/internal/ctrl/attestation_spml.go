package ctrl

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/inference/config"
)

// §7 of docs/spml-design.md: instead of trusting a pin frozen
// in the config, the broker periodically re-attests the spml verifier by
// reading tappscan's record for the app, cross-checking the signer against the
// TappRegistry and the TLS key against the spml's live port (attestation_scan.go).
//
// This used to exec `tapp-cli verify-app` and parse its output instead. That
// path is gone: its boot-chain answer came from a policy on a shared
// attestation service that anyone can overwrite, and on 2026-09-07 one was —
// after which the check failed a CVM that was fine. tappscan compares the
// measurement against the reference-value files in the public 0g-tapp repo, so
// the same overwrite cannot reach it.
//
// The result is an atomic snapshot consumed at three points:
//   - the TLS pin for the shared http client (a fresh pin is accepted ONLY
//     through a fully-verified snapshot — redeploy-as-MITM is dead)
//   - the settlement gate (SettleFeesWithTEE entry: no verdict authority, no
//     money movement)
//   - the invoice gate (settleSpmlPayout: no cumulative disclosure)

type attestedSpml struct {
	pin       []byte // sha256 of the verifier's TLS SPKI, from "tls key"
	checkedAt time.Time
	ok        bool
	detail    string // one-line reason, for logs and /health-style surfaces
}

type spmlAttestor struct {
	cfg    config.SpmlAttestation
	logger log.Logger
	snap   atomic.Value // attestedSpml
	// scan is the evidence source. Never nil once the loop is running.
	scan *scanSource
}

func newSpmlAttestor(cfg config.SpmlAttestation, logger log.Logger) *spmlAttestor {
	a := &spmlAttestor{cfg: cfg, logger: logger}
	a.snap.Store(attestedSpml{detail: "not yet verified"})
	return a
}

// current returns the latest snapshot, expired snapshots included — callers
// decide staleness with fresh().
func (a *spmlAttestor) current() attestedSpml {
	return a.snap.Load().(attestedSpml)
}

// fresh is true when the last successful verification is within MaxAge.
func (a *spmlAttestor) fresh() bool {
	s := a.current()
	return s.ok && time.Since(s.checkedAt) <= a.cfg.MaxAge()
}

// pin returns the attested pin when fresh, else nil (callers fall back to the
// configured static pin — the pre-Phase-2 trust level, never less).
func (a *spmlAttestor) pinOrNil() []byte {
	if s := a.current(); s.ok && len(s.pin) > 0 {
		return s.pin
	}
	return nil
}

// SpmlAttestationSnapshot is what this broker got the last time it ran the
// verification itself, for surfacing next to the command we hand a client.
//
// Read the asymmetry before trusting it. A PASS here is worth little: we are
// reporting on ourselves, and a broker that lies about this has every incentive
// to lie in exactly this direction. A FAIL is worth a lot: this same result
// gates our own settlement and invoicing, so saying "not verified" costs us
// money. Nobody pays to slander themselves.
//
// So: treat ok=false as a real signal, and ok=true as a hint that running the
// command yourself is likely to be uneventful — never as a substitute for it.
type SpmlAttestationSnapshot struct {
	OK         bool      `json:"ok"`
	CheckedAt  time.Time `json:"checked_at"`
	Detail     string    `json:"detail"`
	Fresh      bool      `json:"fresh"`
	GatesMoney bool      `json:"gates_settlement"`
}

// SpmlAttestationStatus reports our own last verification, or nil when the
// attestation loop is not configured (in which case we verify nothing and have
// nothing to report — which is itself worth saying out loud).
func (c *Ctrl) SpmlAttestationStatus() *SpmlAttestationSnapshot {
	if c.spmlAttestor == nil {
		return nil
	}
	s := c.spmlAttestor.current()
	blocked, _ := c.spmlAttestor.blockSettlement()
	return &SpmlAttestationSnapshot{
		OK:         s.ok,
		CheckedAt:  s.checkedAt,
		Detail:     s.detail,
		Fresh:      c.spmlAttestor.fresh(),
		GatesMoney: blocked,
	}
}

// blockSettlement is the gate consulted by settlement and invoicing.
func (a *spmlAttestor) blockSettlement() (bool, string) {
	if a.cfg.OnFail == "warn-only" {
		return false, ""
	}
	if a.fresh() {
		return false, ""
	}
	s := a.current()
	return true, fmt.Sprintf("spml attestation not current (ok=%v checkedAt=%s detail=%q)",
		s.ok, s.checkedAt.Format(time.RFC3339), s.detail)
}

// run loops until ctx ends: verify immediately, then every Interval — but
// while the snapshot is bad, retry every minute instead: the common causes
// (sidecar still booting, transient RPC/AS hiccup) clear in seconds, and a
// gate that stays engaged a full interval longer than necessary skips
// settlement rounds for nothing.
func (a *spmlAttestor) run(ctx context.Context) {
	for {
		a.verifyOnce(ctx)
		next := a.cfg.IntervalOrDefault()
		if !a.current().ok && next > time.Minute {
			next = time.Minute
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(next):
		}
	}
}

func (a *spmlAttestor) verifyOnce(ctx context.Context) {
	prev := a.current()
	next := a.executeAndParse(ctx)
	a.snap.Store(next)
	// Every state flip gets a loud line — it is the only operator-visible
	// signal (design §14).
	if prev.ok != next.ok {
		if next.ok {
			a.logger.Infof("Spml attestation OK: pin=%x (%s)", next.pin, next.detail)
		} else {
			a.logger.Errorf("Spml attestation FAILED — %s gates engage per onFail=%s: %s",
				a.cfg.AppID, a.cfg.OnFail, next.detail)
		}
	}
}

func (a *spmlAttestor) executeAndParse(ctx context.Context) attestedSpml {
	return a.scan.verifyOnce(ctx)
}
