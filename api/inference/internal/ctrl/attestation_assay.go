package ctrl

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/inference/config"
)

// Phase 2 of docs/spml-broker-assay-tls.md: instead of trusting a pin frozen
// in the config, the broker periodically re-attests the assay verifier by
// exec'ing `tapp-cli verify-app` (the reference implementation — ~1100 lines
// of TDX quote / eventlog / chain reconciliation we deliberately do not
// rewrite in Go) and parsing its OUTPUT. The exit code is worthless: the CLI
// exits 0 and prints "ALL PASS ✅" even when the quote is untrusted (wrong
// policy id, wrong AS pin — both captured as test fixtures).
//
// The result is an atomic snapshot consumed at three points:
//   - the TLS pin for the shared http client (a fresh pin is accepted ONLY
//     through a fully-verified snapshot — redeploy-as-MITM is dead)
//   - the settlement gate (SettleFeesWithTEE entry: no verdict authority, no
//     money movement)
//   - the invoice gate (settleAssayPayout: no cumulative disclosure)

type attestedAssay struct {
	pin       []byte // sha256 of the verifier's TLS SPKI, from "tls key"
	checkedAt time.Time
	ok        bool
	detail    string // one-line reason, for logs and /health-style surfaces
}

type assayAttestor struct {
	cfg    config.AssayAttestation
	logger log.Logger
	snap   atomic.Value // attestedAssay
}

func newAssayAttestor(cfg config.AssayAttestation, logger log.Logger) *assayAttestor {
	a := &assayAttestor{cfg: cfg, logger: logger}
	a.snap.Store(attestedAssay{detail: "not yet verified"})
	return a
}

// current returns the latest snapshot, expired snapshots included — callers
// decide staleness with fresh().
func (a *assayAttestor) current() attestedAssay {
	return a.snap.Load().(attestedAssay)
}

// fresh is true when the last successful verification is within MaxAge.
func (a *assayAttestor) fresh() bool {
	s := a.current()
	return s.ok && time.Since(s.checkedAt) <= a.cfg.MaxAge()
}

// pin returns the attested pin when fresh, else nil (callers fall back to the
// configured static pin — the pre-Phase-2 trust level, never less).
func (a *assayAttestor) pinOrNil() []byte {
	if s := a.current(); s.ok && len(s.pin) > 0 {
		return s.pin
	}
	return nil
}

// blockSettlement is the gate consulted by settlement and invoicing.
func (a *assayAttestor) blockSettlement() (bool, string) {
	if a.cfg.OnFail == "warn-only" {
		return false, ""
	}
	if a.fresh() {
		return false, ""
	}
	s := a.current()
	return true, fmt.Sprintf("assay attestation not current (ok=%v checkedAt=%s detail=%q)",
		s.ok, s.checkedAt.Format(time.RFC3339), s.detail)
}

// run loops until ctx ends: verify immediately, then every Interval — but
// while the snapshot is bad, retry every minute instead: the common causes
// (sidecar still booting, transient RPC/AS hiccup) clear in seconds, and a
// gate that stays engaged a full interval longer than necessary skips
// settlement rounds for nothing.
func (a *assayAttestor) run(ctx context.Context) {
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

func (a *assayAttestor) verifyOnce(ctx context.Context) {
	prev := a.current()
	next := a.executeAndParse(ctx)
	a.snap.Store(next)
	// Every state flip gets a loud line — it is the only operator-visible
	// signal (design §14).
	if prev.ok != next.ok {
		if next.ok {
			a.logger.Infof("Assay attestation OK: pin=%x (%s)", next.pin, next.detail)
		} else {
			a.logger.Errorf("Assay attestation FAILED — %s gates engage per onFail=%s: %s",
				a.cfg.AppID, a.cfg.OnFail, next.detail)
		}
	}
}

func (a *assayAttestor) executeAndParse(ctx context.Context) attestedAssay {
	if a.cfg.OutputFile != "" {
		return a.readAndParse()
	}
	args := []string{
		"verify-app",
		"--app-id", a.cfg.AppID,
		"--contract", a.cfg.Registry,
		"--rpc-url", a.cfg.RpcURL,
		"--as-pubkey", a.cfg.AsPubkeyPin, // NO default upstream: omitting it = encrypted-but-unauthenticated AS
	}
	for _, p := range a.cfg.PolicyIDs {
		args = append(args, "--policy-ids", p)
	}
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, a.cfg.CliPathOrDefault(), args...).CombinedOutput()
	now := time.Now()
	if err != nil {
		// A non-zero exit is a hard failure (unknown app, no binary, timeout)
		// — but the reverse is NOT true, so parsing continues on exit 0.
		return attestedAssay{checkedAt: now, ok: false,
			detail: fmt.Sprintf("verify-app failed: %v (%.400s)", err, string(out))}
	}
	pin, perr := parseVerifyAppOutput(string(out), a.cfg.RequireTcb)
	if perr != nil {
		return attestedAssay{checkedAt: now, ok: false, detail: perr.Error()}
	}
	return attestedAssay{pin: pin, checkedAt: now, ok: true,
		detail: fmt.Sprintf("verified at %s", now.Format(time.RFC3339))}
}

// readAndParse is file mode: the sidecar owns the exec, we own the parse.
// checkedAt is the file's mtime, so a sidecar that stops refreshing ages the
// snapshot past MaxAge and the gates engage without any extra liveness logic.
func (a *assayAttestor) readAndParse() attestedAssay {
	st, err := os.Stat(a.cfg.OutputFile)
	if err != nil {
		return attestedAssay{checkedAt: time.Now(), ok: false,
			detail: fmt.Sprintf("attestation output missing (sidecar down?): %v", err)}
	}
	raw, err := os.ReadFile(a.cfg.OutputFile)
	if err != nil {
		return attestedAssay{checkedAt: time.Now(), ok: false,
			detail: fmt.Sprintf("attestation output unreadable: %v", err)}
	}
	pin, perr := parseVerifyAppOutput(string(raw), a.cfg.RequireTcb)
	if perr != nil {
		return attestedAssay{checkedAt: st.ModTime(), ok: false, detail: perr.Error()}
	}
	return attestedAssay{pin: pin, checkedAt: st.ModTime(), ok: true,
		detail: fmt.Sprintf("verified by sidecar output of %s", st.ModTime().Format(time.RFC3339))}
}

var (
	reEarStatus = regexp.MustCompile(`ear\.status=([A-Za-z-]+)`)
	reTcbStatus = regexp.MustCompile(`tcb_status=([A-Za-z-]+)`)
	reTlsKey    = regexp.MustCompile(`tls key\s*:\s*0x([0-9a-fA-F]{64})`)
	reNodeCount = regexp.MustCompile(`\((\d+) node\(s\)\)`)
)

// parseVerifyAppOutput enforces the five conditions of implplan §4.2. It
// never looks at the exit code and treats "ALL PASS ✅" as noise. Only a
// single-node app is accepted (ours is one) — with several nodes one bad
// quote must not hide behind a good one.
func parseVerifyAppOutput(out string, requireTcb []string) ([]byte, error) {
	if m := reNodeCount.FindStringSubmatch(out); m != nil && m[1] != "1" {
		return nil, fmt.Errorf("app has %s nodes; this parser only accepts exactly 1", m[1])
	}
	if strings.Contains(out, "quote untrusted") {
		return nil, fmt.Errorf("quote untrusted (wrong --policy-ids or unauthenticated AS?)")
	}
	if !strings.Contains(out, "=> reconcile PASS ; quote trusted") {
		return nil, fmt.Errorf("no 'reconcile PASS ; quote trusted' line (reconcile failure or unexpected output)")
	}
	m := reEarStatus.FindStringSubmatch(out)
	if m == nil || m[1] != "affirming" {
		got := "-"
		if m != nil {
			got = m[1]
		}
		return nil, fmt.Errorf("ear.status=%s, need affirming", got)
	}
	m = reTcbStatus.FindStringSubmatch(out)
	if m == nil {
		return nil, fmt.Errorf("no tcb_status in output")
	}
	allowed := false
	for _, want := range requireTcb {
		if m[1] == want {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, fmt.Errorf("tcb_status=%s not in allowed set %v", m[1], requireTcb)
	}
	if !strings.Contains(out, "boot-chain : ✓") {
		return nil, fmt.Errorf("boot-chain not verified (missing 'boot-chain : ✓')")
	}
	km := reTlsKey.FindStringSubmatch(out)
	if km == nil {
		return nil, fmt.Errorf("no attested 'tls key' in output — refusing an empty pin")
	}
	pin, err := hex.DecodeString(km[1])
	if err != nil {
		return nil, fmt.Errorf("bad tls key hex: %w", err)
	}
	return pin, nil
}
