package ctrl

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/tee"
	"github.com/0glabs/0g-serving-broker/inference/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// spmlHooks is providerType spml's addition to the shared path: the assay verifier
// signs and audits each request, and settlement and payout follow its verdicts. See
// docs/design/assay-verification-settlement.md.
//
// A pointer because New builds it before the Ctrl it serves exists (the upstream client
// needs its TLS config first) and fills c in afterwards.
type spmlHooks struct {
	c   *Ctrl
	tls *tls.Config
}

func (h *spmlHooks) upstreamTLS() *tls.Config { return h.tls }

// beforeUpstream hands the verifier this request's settlement hash, so it can fold it
// into the signed verdict (single-use signature; see recordAssayVerdict), and signs the
// body. Without the signature the verifier's /v1/chat/completions answers anyone who can
// reach the port. Fail closed: a request that cannot be signed is not sent, and the
// caller's SDK can try another provider.
func (h *spmlHooks) beforeUpstream(req *http.Request, body []byte, reqModel model.Request) error {
	if reqModel.RequestHash != "" {
		req.Header.Set(constant.HeaderZGRequestHash, reqModel.RequestHash)
	}
	if len(body) == 0 {
		return nil
	}
	if err := h.c.signAssayBody(req, body); err != nil {
		return fmt.Errorf("refusing to send an unsigned inference request to the assay: %w", err)
	}
	return nil
}

func (h *spmlHooks) onChargedResponse(resp *http.Response, reqModel model.Request) {
	h.c.recordAssayVerdict(resp, reqModel)
}

// settlementBlocked is Phase-2 gate 1: with the assay's attestation not current, its
// verdicts have no authority and money must not move on them. Every request row is
// kept; the round is retried once attestation recovers.
func (h *spmlHooks) settlementBlocked() (bool, string) {
	if h.c.assayAttestor == nil {
		return false, ""
	}
	return h.c.assayAttestor.blockSettlement()
}

// filterSettlement fetches the batch's final audit verdicts (resolving PENDING async
// audits): any REJECT voids the batch, pending audits are parked for a later round.
func (h *spmlHooks) filterSettlement(ctx context.Context, reqs []model.Request) []model.Request {
	return h.c.gateSettlementWithAssay(ctx, reqs)
}

// afterSettlement accrues each settled request's fee onto its node's cumulative and
// invoices the assay for signed PayoutVouchers. Never fails settlement.
func (h *spmlHooks) afterSettlement(ctx context.Context, outcomes []*SettlementOutcome) {
	h.c.settleAssayPayout(ctx, outcomes)
}

// spmlSetup is what New parses out of cfg.Assay for a spml provider, before the Ctrl
// exists. Misconfiguration panics at boot: a gate that silently never engages is worse
// than none.
type spmlSetup struct {
	hooks        *spmlHooks
	verifierAddr *common.Address
	attestor     *assayAttestor
}

func newSPMLSetup(cfg *config.Config, teeService *tee.TeeService, logger log.Logger) *spmlSetup {
	// Parse the Assay verifier's signing address up front: a malformed address
	// with verification "on" must fail loudly at boot, not silently downgrade
	// to trusting unauthenticated verdicts on the settlement path.
	var assayVerifierAddr *common.Address
	if cfg.Assay.VerifierAddress != "" {
		if !common.IsHexAddress(cfg.Assay.VerifierAddress) {
			panic(fmt.Sprintf("assay.verifierAddress must be a 20-byte hex Ethereum address, got %q",
				cfg.Assay.VerifierAddress))
		}
		a := common.HexToAddress(cfg.Assay.VerifierAddress)
		assayVerifierAddr = &a
	}
	if cfg.Assay.StrictVerdict && assayVerifierAddr == nil {
		panic("assay.strictVerdict requires assay.verifierAddress to be set")
	}

	// Parse the verifier TLS key pin likewise up front: a malformed pin with
	// https configured must fail at boot, not on the first settlement.
	var assayKeyPin []byte
	if cfg.Assay.VerifierKeyPin != "" {
		pin, err := hex.DecodeString(strings.TrimPrefix(cfg.Assay.VerifierKeyPin, "0x"))
		if err != nil || len(pin) != sha256.Size {
			panic(fmt.Sprintf("assay.verifierKeyPin must be 32 bytes of hex (sha256 of the verifier's SPKI), got %q",
				cfg.Assay.VerifierKeyPin))
		}
		assayKeyPin = pin
	}

	// Phase-2 attestor (docs/spml-design.en.md §7). Misconfig
	// panics at boot: a gate that silently never engages is worse than none.
	var attestor *assayAttestor
	if cfg.Assay.Attestation.Enabled {
		at := cfg.Assay.Attestation
		if at.OnFail == "" {
			at.OnFail = "block-settlement"
		}
		if at.OnFail != "block-settlement" && at.OnFail != "warn-only" {
			panic(fmt.Sprintf("assay.attestation.onFail must be block-settlement or warn-only, got %q", at.OnFail))
		}
		if len(at.RequireTcb) == 0 {
			at.RequireTcb = []string{"UpToDate"}
		}
		attestor = newAssayAttestor(at, logger)
		{
			if at.AppID == "" || at.Registry == "" || at.RpcURL == "" ||
				at.Tappscan.URL == "" || at.Tappscan.PubkeyPin == "" || at.Expected.Image == "" {
				panic("assay.attestation needs appId, registry, rpcUrl, tappscan.url, tappscan.pubkeyPin and expected.image")
			}
			var sign func([]byte) ([]byte, error)
			if teeService != nil {
				sign = teeService.Sign
			}
			scan, err := newScanSource(at, cfg.Assay.VerifierURL, sign, logger)
			if err != nil {
				panic(err)
			}
			attestor.scan = scan
		}
	}

	// The pin the TLS handshake checks: the freshest attested value when the
	// attestor has one, else the static config pin. Re-read per handshake.
	var getAssayPin func() []byte
	if attestor != nil {
		static := assayKeyPin
		getAssayPin = func() []byte {
			if p := attestor.pinOrNil(); p != nil {
				return p
			}
			return static
		}
	} else if len(assayKeyPin) > 0 {
		staticOnly := assayKeyPin
		getAssayPin = func() []byte { return staticOnly }
	}

	return &spmlSetup{
		hooks:        &spmlHooks{tls: pinnedTLSConfig(getAssayPin)},
		verifierAddr: assayVerifierAddr,
		attestor:     attestor,
	}
}

// apply fills the Ctrl's assay fields and starts the attestation loop. Called once,
// after the Ctrl is built.
func (s *spmlSetup) apply(p *Ctrl, cfg *config.Config, logger log.Logger) {
	s.hooks.c = p
	p.assayVerdictFilter = true
	p.assayAttestor = s.attestor
	p.assayVerifierAddress = s.verifierAddr
	p.assayStrictVerdict = cfg.Assay.StrictVerdict
	p.assayRejectGates = cfg.Assay.RejectGatesSettlement
	p.assayVerifierURL = strings.TrimSuffix(cfg.Assay.VerifierURL, "/")
	p.assayCheckWaitMs = cfg.Assay.SettlementCheckWaitMs
	p.assayPayoutEnabled = cfg.Assay.Payout.Enabled
	p.assayVerifierCutBps = cfg.Assay.Payout.VerifierCutBps

	if attestor := s.attestor; attestor != nil {
		if attestor.scan != nil {
			attestor.scan.provider = p.ProviderAddress()
		}
		go attestor.run(context.Background())
		logger.Infof("Assay attestation loop ON: app=%s interval=%s maxAge=%s onFail=%s",
			cfg.Assay.Attestation.AppID, cfg.Assay.Attestation.IntervalOrDefault(),
			cfg.Assay.Attestation.MaxAge(), cfg.Assay.Attestation.OnFail)
	}
}

// recordAssayVerdict persists the Assay/LDD audit verdict from the verifier's
// ZG-Verdict response header onto the request row. With a non-blocking
// verifier this is PENDING (audit queued) or UNVERIFIED (sampled out); the
// final PASS/REJECT is fetched by the settlement gate from the verifier's
// /v1/settlement/check (see gateSettlementWithAssay), which is what decides
// whether the batch settles. No-op when the integration is disabled, for
// whitelisted (unbilled) traffic, or when the upstream set no verdict header
// (fail-open).
//
// The verdict is a settlement input, so when assay.verifierAddress is
// configured it is only acted on if the verifier's secp256k1 signature
// (ZG-Verdict-Sig) verifies over the verdict + this request's hash — an
// unauthenticated plaintext header must not decide who gets paid. The hash
// (sent upstream as ZG-Request-Hash) makes each signature single-use, so a
// PASS captured from one response can't be replayed onto another. In strict
// mode a missing/unverifiable verdict is recorded as INVALID_SIG (excluded
// from settlement like REJECT); otherwise it is ignored (fail-open).
func (c *Ctrl) recordAssayVerdict(resp *http.Response, reqModel model.Request) {
	if !c.assayVerdictFilter || reqModel.IsWhitelisted {
		return
	}
	verdict := resp.Header.Get(constant.HeaderZGVerdict)

	// Node attribution (ZG-Node): which GPU served this request. Recorded
	// unauthenticated — the assay independently re-checks attribution against
	// its own ledger before signing any voucher (design §4 step 11), so a
	// wrong node here can shift nothing.
	if node := resp.Header.Get(constant.HeaderZGNode); node != "" {
		if err := c.db.UpdateRequestNode(reqModel.RequestHash, node); err != nil {
			c.logger.Warnf("Assay: failed to record node %q for request %s: %v", node, reqModel.RequestHash, err)
		}
	}

	if c.assayVerifierAddress != nil {
		sig := resp.Header.Get(constant.HeaderZGVerdictSig)
		if verdict == "" || !verifyAssayVerdictSig(*c.assayVerifierAddress, verdict, reqModel.RequestHash, sig) {
			if !c.assayStrictVerdict {
				if verdict != "" {
					c.logger.Warnf("Assay: dropping unauthenticated verdict %q for request %s (bad/missing %s)",
						verdict, reqModel.RequestHash, constant.HeaderZGVerdictSig)
				}
				return
			}
			c.logger.Warnf("Assay: verdict %q for request %s failed signature verification; recording %s",
				verdict, reqModel.RequestHash, constant.AssayVerdictInvalidSig)
			verdict = constant.AssayVerdictInvalidSig
		}
	}

	if verdict == "" {
		return
	}
	if err := c.db.UpdateRequestVerdict(reqModel.RequestHash, verdict); err != nil {
		c.logger.Warnf("Assay: failed to record verdict %q for request %s: %v", verdict, reqModel.RequestHash, err)
	}
}

// verifyAssayVerdictSig recovers the signer of the verifier's secp256k1
// EIP-191 signature over the domain-separated verdict payload and checks it
// against the pinned verifier address (the same recovery on-chain ecrecover
// performs). Pure so it can be unit-tested without a Ctrl. Payload layout must
// match the verifier's signer (pipeline/verifier_node/serve_verifier.py in
// 0g-assay):
//
//	"assay-verdict-v1|" + verdict + "|" + requestHash
func verifyAssayVerdictSig(signer common.Address, verdict, requestHash, sigHex string) bool {
	if sigHex == "" {
		return false
	}
	sig, err := hexutil.Decode(sigHex)
	if err != nil || len(sig) != 65 {
		return false
	}
	// go-ethereum's SigToPub wants the recovery id in {0,1}; EIP-191 signers
	// emit {27,28}.
	if sig[64] >= 27 {
		sig[64] -= 27
	}
	payload := constant.AssayVerdictDomain + "|" + verdict + "|" + requestHash
	pub, err := crypto.SigToPub(accounts.TextHash([]byte(payload)), sig)
	if err != nil {
		return false
	}
	return crypto.PubkeyToAddress(*pub) == signer
}
