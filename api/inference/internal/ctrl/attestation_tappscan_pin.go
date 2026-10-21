package ctrl

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/0glabs/0g-serving-broker/common/tapp"
	"github.com/0glabs/0g-serving-broker/inference/config"
)

// tappscan's TLS key is the one anchor of the attestation loop that used to
// be a hand-maintained config value (docs/spml-design.md §7.6): it is a
// local-mode key, so it changes whenever 0G restarts or moves tappscan, and
// until an operator edited pubkeyPin every check failed closed and settlement
// stopped. With tappscan.pinSource: chain the broker derives it itself:
//
//	TappRegistry.getNodeList("0g-tappscan") → the admitted signer
//	TappRegistry.getNode(app, signer).teeUrl → its tapp-server
//	GetEvidence(app, fresh nonce)            → quote + event log + runtime_data
//	common/tapp.Verify                        → Intel signature, report_data
//	    binding, nonce echo, signer == chain, event-log replay, UKI against the
//	    public reference values, PCS TCB under requireTcb / allowAdvisories
//	runtime_data.tls_public_key               → the pin
//
// Nothing here trusts tappscan: the registry owner admitted the signer, Intel
// signed the quote, the reference values are public, and the nonce is ours.

// tappscanPinState is one bootstrap outcome.
type tappscanPinState struct {
	ok        bool
	pin       []byte // sha256 of tappscan's SPKI when ok
	signer    string
	teeURL    string
	checkedAt time.Time
	detail    string
	result    *tapp.Result // the check table, for the statement
}

// tappscanPins is the pin the scan transport uses: the last verified
// bootstrap when there is one, else the configured fallback. A bootstrap that
// fails after an earlier success keeps the earlier key — it was verified when
// adopted, and tappscan's web port answering to it is itself the evidence it
// has not rotated; a rotation shows up as the fetch failing the pin check,
// which fails closed until the next successful bootstrap.
type tappscanPins struct {
	mu     sync.Mutex
	static []byte // tappscan.pubkeyPin, may be nil under pinSource: chain
	last   tappscanPinState
}

func (p *tappscanPins) get() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.last.ok && len(p.last.pin) > 0 {
		return p.last.pin
	}
	return p.static
}

// source names where the current pin came from: "chain", "config", or "none".
func (p *tappscanPins) source() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case p.last.ok && len(p.last.pin) > 0:
		return "chain"
	case len(p.static) > 0:
		return "config"
	}
	return "none"
}

func (p *tappscanPins) state() tappscanPinState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last
}

// set records a bootstrap outcome; a failure does not discard the last
// verified key (see the type comment).
func (p *tappscanPins) set(st tappscanPinState) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !st.ok && p.last.ok {
		st.pin = p.last.pin
		st.ok = true
		st.signer, st.teeURL, st.checkedAt = p.last.signer, p.last.teeURL, p.last.checkedAt
		st.detail = "keeping the key verified at " + p.last.checkedAt.UTC().Format(time.RFC3339) + "; this cycle: " + st.detail
	}
	p.last = st
}

var tappRegistryNodeABI = mustABI(`[{"name":"getNode","type":"function","stateMutability":"view",
	"inputs":[{"name":"appId","type":"string"},{"name":"signerAddress","type":"address"}],
	"outputs":[{"components":[{"name":"teeUrl","type":"string"},{"name":"addedAt","type":"uint256"},{"name":"stakeAmount","type":"uint256"},{"name":"composeHash","type":"bytes"},{"name":"volumesHash","type":"bytes"}],"name":"","type":"tuple"}]}]`)

// registryNodeTeeURL reads getNode(app, signer).teeUrl off the TappRegistry.
func registryNodeTeeURL(ctx context.Context, cfg config.AssayAttestation, appID, signer string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cl, err := ethclient.DialContext(cctx, cfg.RpcURL)
	if err != nil {
		return "", fmt.Errorf("rpc: %w", err)
	}
	defer cl.Close()
	data, err := tappRegistryNodeABI.Pack("getNode", appID, common.HexToAddress(signer))
	if err != nil {
		return "", err
	}
	to := common.HexToAddress(cfg.Registry)
	out, err := cl.CallContract(cctx, ethereum.CallMsg{To: &to, Data: data}, nil)
	if err != nil {
		return "", fmt.Errorf("getNode: %w", err)
	}
	// A single tuple return lands in the FIRST field of the target struct
	// (abi.Arguments.copyAtomic), hence the wrapper.
	var ret struct {
		Node struct {
			TeeUrl      string
			AddedAt     *big.Int
			StakeAmount *big.Int
			ComposeHash []byte
			VolumesHash []byte
		}
	}
	if err := tappRegistryNodeABI.UnpackIntoInterface(&ret, "getNode", out); err != nil {
		return "", fmt.Errorf("getNode decode: %w", err)
	}
	node := ret.Node
	if node.AddedAt == nil || node.AddedAt.Sign() == 0 {
		return "", fmt.Errorf("registry has no node %s for %s", signer, appID)
	}
	if strings.TrimSpace(node.TeeUrl) == "" {
		return "", fmt.Errorf("registry node %s for %s has no teeUrl", signer, appID)
	}
	return node.TeeUrl, nil
}

// bootstrapTappscanPin runs one bootstrap cycle. It never panics or blocks
// beyond the context: every failure comes back as ok=false with the reason.
func (s *scanSource) bootstrapTappscanPin(ctx context.Context) tappscanPinState {
	now := time.Now()
	st := tappscanPinState{checkedAt: now}
	appID := s.cfg.TappscanAppID()
	signer, err := s.registrySingleNode(ctx, appID)
	if err != nil {
		st.detail = "tappscan registry: " + err.Error()
		return st
	}
	st.signer = signer
	teeURL, err := registryNodeTeeURL(ctx, s.cfg, appID, signer)
	if err != nil {
		st.detail = "tappscan registry: " + err.Error()
		return st
	}
	st.teeURL = teeURL
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		st.detail = "nonce: " + err.Error()
		return st
	}
	raw, err := tapp.FetchEvidence(ctx, teeURL, appID, nonce)
	if err != nil {
		st.detail = "tappscan evidence: " + err.Error()
		return st
	}
	res := tapp.Verify(ctx, raw, tapp.Policy{
		Signer: signer, Nonce: nonce,
		ExpectedUki: s.cfg.Tappscan.Expected.Uki, ExpectedMrTd: s.cfg.Tappscan.Expected.MrTd,
		RequireTcb: s.cfg.RequireTcb, AllowAdvisories: s.cfg.AllowAdvisories,
		PCSUpdate: s.cfg.Tappscan.PCSUpdate, Now: now,
	})
	st.result = &res
	st.detail = res.Detail
	if !res.OK {
		return st
	}
	pin, err := hex.DecodeString(strings.TrimPrefix(res.TLSPublicKey, "0x"))
	if err != nil || len(pin) != 32 {
		st.detail = "tappscan evidence: tls_public_key is not a 32-byte digest"
		return st
	}
	st.ok, st.pin = true, pin
	return st
}
