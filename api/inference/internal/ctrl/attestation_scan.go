package ctrl

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/inference/config"
)

// scanSource is the tappscan-backed attestation source
// (docs/spml-attestation-relay.md, chosen 2026-09-09). Instead of exec'ing
// tapp-cli against the shared attestation service's policy library — a piece
// of runtime state that was overwritten under us — the broker reads tappscan's
// record for the assay app, checks it against two things tappscan cannot fake
// (the signer the TappRegistry lists, and the TLS key the assay actually serves
// on its public port), and signs the whole statement with the TEE key so a
// client can carry it away and verify it against getService(provider).teeSigner.
//
// It produces the same attestedAssay snapshot the gates already consume, so
// settlement and invoicing do not know which source is behind them.
type scanSource struct {
	cfg          config.AssayAttestation
	provider     string // our provider address, echoed into the statement
	verifierHost string // host:port of the assay's https endpoint (live SPKI probe)
	scanPin      []byte
	client       *http.Client
	sign         func(hash []byte) ([]byte, error) // personal_sign by the TEE key; nil = unsigned
	logger       log.Logger

	mu   sync.Mutex
	last *scanEval
}

// scanChecks is the check table of the design's §2, in that order. Every field
// is reported, so a reader sees which one is speaking rather than a bare "ok".
type scanChecks struct {
	Pin         bool `json:"pin"`          // tappscan's TLS key matched tappscan.pubkeyPin
	Record      bool `json:"record"`       // one current signer, attested present, no error
	ChainSigner bool `json:"chain_signer"` // record signer == TappRegistry.getNodeList(app)
	SignerBound bool `json:"signer_bound"` // quote attests that same signer
	Image       bool `json:"image"`        // matched reference file == expected.image
	Uki         bool `json:"uki"`          // measured uki == expected.uki (true when not configured)
	Replay      bool `json:"replay"`       // eventlog replay reproduces the RTMRs
	Tcb         bool `json:"tcb"`          // tcb_status in requireTcb
	Clean       bool `json:"clean"`        // no note, no advisories
	TlsKey      bool `json:"tls_key"`      // attested tls key == what the assay serves right now
	Fresh       bool `json:"fresh"`        // record's checked_at within tappscan.maxAgeSeconds
}

// scanFacts are the values the broker fetched on its own, so a reader can
// re-check them without taking tappscan's word.
type scanFacts struct {
	ChainSigner   string `json:"chain_signer"`
	LiveTLS       string `json:"live_tls_pubkey_sha256"`
	ExpectedImage string `json:"expected_image"`
	ExpectedUki   string `json:"expected_uki,omitempty"`
}

type scanEval struct {
	observedAt time.Time
	raw        []byte
	recordSha  string
	checkedAt  int64
	signer     string
	tlsKey     string // attested tls pubkey, 0x-hex; the pin when ok
	image      string
	uki        string
	checks     scanChecks
	facts      scanFacts
	ok         bool
	detail     string
}

// scanInputs is everything evaluateScanRecord needs besides the record, so the
// evaluation is a pure function and the fixtures can drive it.
type scanInputs struct {
	now           time.Time
	pinOK         bool // the fetch completed through the pinned transport
	chainSigner   string
	chainErr      error
	liveTLS       string
	liveErr       error
	expectedImage string
	expectedUki   string
	requireTcb    []string
	maxRecordAge  time.Duration
}

// scanRecord is the subset of tappscan's /api/apps/<id> we read. Unknown
// fields are ignored; the raw bytes travel separately.
type scanRecord struct {
	Now     int64 `json:"now"`
	Signers []struct {
		Current bool   `json:"current"`
		Signer  string `json:"signer"`
		Status  *struct {
			Signer        string          `json:"signer"`
			CheckedAt     int64           `json:"checked_at"`
			Error         json.RawMessage `json:"error"`
			VerifierFault bool            `json:"verifier_fault"`
			Attested      *struct {
				AttestedSigner  string              `json:"attested_signer"`
				SignerOK        bool                `json:"signer_ok"`
				Image           string              `json:"image"`
				RuntimeReplayOK bool                `json:"runtime_replay_ok"`
				TcbStatus       string              `json:"tcb_status"`
				Note            string              `json:"note"`
				Advisories      []json.RawMessage   `json:"advisories"`
				TlsPublicKey    string              `json:"tls_public_key"`
				Measured        map[string][]string `json:"measured"`
			} `json:"attested"`
		} `json:"status"`
	} `json:"signers"`
}

func newScanSource(cfg config.AssayAttestation, verifierURL string, sign func([]byte) ([]byte, error), logger log.Logger) (*scanSource, error) {
	pin, err := hex.DecodeString(strings.TrimPrefix(cfg.Tappscan.PubkeyPin, "0x"))
	if err != nil || len(pin) != sha256.Size {
		return nil, fmt.Errorf("assay.attestation.tappscan.pubkeyPin must be 32 bytes of hex (sha256 of tappscan's SPKI), got %q", cfg.Tappscan.PubkeyPin)
	}
	u, err := url.Parse(verifierURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("assay.verifierUrl %q: cannot take a host for the live TLS probe", verifierURL)
	}
	host := u.Host
	if u.Port() == "" {
		host += ":443"
	}
	s := &scanSource{cfg: cfg, verifierHost: host, scanPin: pin, sign: sign, logger: logger}
	s.client = &http.Client{
		Timeout:   20 * time.Second,
		Transport: &http.Transport{TLSClientConfig: pinnedTLSConfig(func() []byte { return s.scanPin })},
	}
	return s, nil
}

func (s *scanSource) recordURL() string {
	return strings.TrimSuffix(s.cfg.Tappscan.URL, "/") + "/api/apps/" + url.PathEscape(s.cfg.AppID)
}

func (s *scanSource) fetch(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.recordURL(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tappscan answered %d", resp.StatusCode)
	}
	return body, nil
}

var tappRegistryABI = mustABI(`[{"name":"getNodeList","type":"function","stateMutability":"view",
	"inputs":[{"name":"appId","type":"string"}],"outputs":[{"name":"","type":"address[]"}]}]`)

func mustABI(s string) abi.ABI {
	a, err := abi.JSON(strings.NewReader(s))
	if err != nil {
		panic(err)
	}
	return a
}

// chainSigner reads the app's current node list off the TappRegistry. Exactly
// one node is accepted: with several, one bad quote could hide behind a good one.
func (s *scanSource) chainSigner(ctx context.Context) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cl, err := ethclient.DialContext(cctx, s.cfg.RpcURL)
	if err != nil {
		return "", fmt.Errorf("rpc: %w", err)
	}
	defer cl.Close()
	data, err := tappRegistryABI.Pack("getNodeList", s.cfg.AppID)
	if err != nil {
		return "", err
	}
	to := common.HexToAddress(s.cfg.Registry)
	out, err := cl.CallContract(cctx, ethereum.CallMsg{To: &to, Data: data}, nil)
	if err != nil {
		return "", fmt.Errorf("getNodeList: %w", err)
	}
	vals, err := tappRegistryABI.Unpack("getNodeList", out)
	if err != nil {
		return "", fmt.Errorf("getNodeList decode: %w", err)
	}
	nodes, _ := vals[0].([]common.Address)
	if len(nodes) != 1 {
		return "", fmt.Errorf("registry lists %d nodes for %s; exactly 1 accepted", len(nodes), s.cfg.AppID)
	}
	return strings.ToLower(nodes[0].Hex()), nil
}

// liveSPKI opens a TLS connection to the assay's public port and returns
// sha256 of the leaf SubjectPublicKeyInfo. No verification here on purpose:
// this observes what is served; the comparison happens in the evaluation.
func liveSPKI(ctx context.Context, host string) (string, error) {
	d := tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // observation only, compared against the attested key by the caller
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, err := d.DialContext(cctx, "tcp", host)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", errors.New("peer presented no certificate")
	}
	sum := sha256.Sum256(certs[0].RawSubjectPublicKeyInfo)
	return "0x" + hex.EncodeToString(sum[:]), nil
}

func eqHex(a, b string) bool {
	a = strings.ToLower(strings.TrimPrefix(a, "0x"))
	b = strings.ToLower(strings.TrimPrefix(b, "0x"))
	return a != "" && a == b
}

// evaluateScanRecord runs the check table over one record. Pure: every
// external observation arrives in scanInputs.
func evaluateScanRecord(raw []byte, in scanInputs) scanEval {
	ev := scanEval{observedAt: in.now, raw: raw,
		facts: scanFacts{ChainSigner: in.chainSigner, LiveTLS: in.liveTLS,
			ExpectedImage: in.expectedImage, ExpectedUki: in.expectedUki}}
	if in.chainErr != nil {
		ev.facts.ChainSigner = "error: " + in.chainErr.Error()
	}
	if in.liveErr != nil {
		ev.facts.LiveTLS = "error: " + in.liveErr.Error()
	}
	var failed []string
	fail := func(label, why string) { failed = append(failed, label+": "+why) }

	ev.checks.Pin = in.pinOK
	if !in.pinOK {
		fail("pin", "record not fetched through the pinned transport")
	}
	if len(raw) > 0 {
		sum := sha256.Sum256(raw)
		ev.recordSha = "0x" + hex.EncodeToString(sum[:])
	}

	var rec scanRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		fail("record", "not a tappscan record: "+err.Error())
		ev.detail = strings.Join(failed, "; ")
		return ev
	}
	var cur []int
	for i, sg := range rec.Signers {
		if sg.Current {
			cur = append(cur, i)
		}
	}
	if len(cur) != 1 {
		fail("record", fmt.Sprintf("%d current signers, need exactly 1", len(cur)))
		ev.detail = strings.Join(failed, "; ")
		return ev
	}
	S := rec.Signers[cur[0]]
	st := S.Status
	if st == nil || st.Attested == nil {
		fail("record", "no attested status for the current signer (never attested, unreachable, or verifier fault)")
		ev.detail = strings.Join(failed, "; ")
		return ev
	}
	a := st.Attested
	ev.signer = strings.ToLower(st.Signer)
	ev.checkedAt = st.CheckedAt
	ev.tlsKey = strings.ToLower(a.TlsPublicKey)
	ev.image = a.Image
	if u := a.Measured["uki"]; len(u) > 0 {
		ev.uki = strings.ToLower(u[len(u)-1])
	}

	errNull := len(st.Error) == 0 || string(st.Error) == "null"
	ev.checks.Record = errNull && !st.VerifierFault
	if !ev.checks.Record {
		fail("record", fmt.Sprintf("error=%s verifier_fault=%v", string(st.Error), st.VerifierFault))
	}
	ev.checks.ChainSigner = in.chainErr == nil && eqHex(st.Signer, in.chainSigner)
	if !ev.checks.ChainSigner {
		fail("chain_signer", fmt.Sprintf("record %s vs chain %s", st.Signer, ev.facts.ChainSigner))
	}
	ev.checks.SignerBound = a.SignerOK && eqHex(a.AttestedSigner, st.Signer)
	if !ev.checks.SignerBound {
		fail("signer_bound", fmt.Sprintf("signer_ok=%v attested_signer=%s", a.SignerOK, a.AttestedSigner))
	}
	ev.checks.Image = in.expectedImage != "" && a.Image == in.expectedImage
	if !ev.checks.Image {
		fail("image", fmt.Sprintf("record %q vs expected %q", a.Image, in.expectedImage))
	}
	ev.checks.Uki = in.expectedUki == "" || eqHex(ev.uki, in.expectedUki)
	if !ev.checks.Uki {
		fail("uki", fmt.Sprintf("measured %s vs expected %s", ev.uki, in.expectedUki))
	}
	ev.checks.Replay = a.RuntimeReplayOK
	if !ev.checks.Replay {
		fail("replay", "runtime_replay_ok=false")
	}
	for _, want := range in.requireTcb {
		if a.TcbStatus == want {
			ev.checks.Tcb = true
		}
	}
	if !ev.checks.Tcb {
		fail("tcb", fmt.Sprintf("tcb_status=%s not in %v", a.TcbStatus, in.requireTcb))
	}
	ev.checks.Clean = a.Note == "" && len(a.Advisories) == 0
	if !ev.checks.Clean {
		fail("clean", fmt.Sprintf("note=%q advisories=%d", a.Note, len(a.Advisories)))
	}
	ev.checks.TlsKey = in.liveErr == nil && eqHex(a.TlsPublicKey, in.liveTLS)
	if !ev.checks.TlsKey {
		fail("tls_key", fmt.Sprintf("attested %s vs live %s", a.TlsPublicKey, ev.facts.LiveTLS))
	}
	age := in.now.Sub(time.Unix(st.CheckedAt, 0))
	ev.checks.Fresh = st.CheckedAt > 0 && age >= -5*time.Minute && age <= in.maxRecordAge
	if !ev.checks.Fresh {
		fail("fresh", fmt.Sprintf("checked_at %d is %s old, limit %s", st.CheckedAt, age.Round(time.Minute), in.maxRecordAge))
	}
	ev.ok = len(failed) == 0
	if ev.ok {
		ev.detail = fmt.Sprintf("tappscan record of %s verified at %s", time.Unix(st.CheckedAt, 0).UTC().Format(time.RFC3339), in.now.UTC().Format(time.RFC3339))
	} else {
		ev.detail = strings.Join(failed, "; ")
	}
	return ev
}

// verifyOnce is the loop body: fetch, observe, evaluate, remember.
func (s *scanSource) verifyOnce(ctx context.Context) attestedAssay {
	now := time.Now()
	raw, ferr := s.fetch(ctx)
	in := scanInputs{now: now, pinOK: ferr == nil,
		expectedImage: s.cfg.Expected.Image, expectedUki: s.cfg.Expected.Uki,
		requireTcb: s.cfg.RequireTcb, maxRecordAge: s.cfg.TappscanMaxAge()}
	in.chainSigner, in.chainErr = s.chainSigner(ctx)
	in.liveTLS, in.liveErr = liveSPKI(ctx, s.verifierHost)
	ev := evaluateScanRecord(raw, in)
	if ferr != nil {
		ev.detail = "tappscan fetch: " + ferr.Error() + "; " + ev.detail
	}
	s.mu.Lock()
	s.last = &ev
	s.mu.Unlock()
	var pin []byte
	if ev.ok {
		pin, _ = hex.DecodeString(strings.TrimPrefix(ev.tlsKey, "0x"))
	}
	return attestedAssay{pin: pin, checkedAt: now, ok: ev.ok, detail: ev.detail}
}

// scanStatement is the signed document served by /v1/attestation/assay/scan.
// Field order is the wire order; the signature covers the marshalled bytes.
type scanStatement struct {
	V          int    `json:"v"`
	AppID      string `json:"app_id"`
	Provider   string `json:"provider"`
	ObservedAt int64  `json:"observed_at"`
	Nonce      string `json:"nonce,omitempty"`
	Scan       struct {
		URL          string `json:"url"`
		TlsPubkey    string `json:"tls_pubkey_sha256"`
		RecordSha256 string `json:"record_sha256"`
		Record       string `json:"record"`
		CheckedAt    int64  `json:"checked_at"`
	} `json:"scan"`
	Checks scanChecks `json:"checks"`
	OK     bool       `json:"ok"`
	Detail string     `json:"detail"`
	Facts  scanFacts  `json:"facts"`
	Gate   struct {
		OnFail          string `json:"on_fail"`
		GatesSettlement bool   `json:"gates_settlement"`
		MaxAgeSeconds   int    `json:"max_age_seconds"`
	} `json:"gate"`
}

var (
	ErrScanNotReady = errors.New("tappscan attestation has not run yet")
	ErrScanBadNonce = errors.New("nonce must be hex, at most 32 bytes")
)

// statement builds and signs the current statement. With a nonce it is built
// and signed per call, which proves the signature is of this moment; the
// evaluation inside is still the loop's latest, dated by observed_at.
func (s *scanSource) statement(nonce string, gatesSettlement bool) ([]byte, []byte, error) {
	if nonce != "" {
		n, err := hex.DecodeString(strings.TrimPrefix(nonce, "0x"))
		if err != nil || len(n) == 0 || len(n) > 32 {
			return nil, nil, ErrScanBadNonce
		}
		nonce = "0x" + hex.EncodeToString(n)
	}
	s.mu.Lock()
	ev := s.last
	s.mu.Unlock()
	if ev == nil {
		return nil, nil, ErrScanNotReady
	}
	st := scanStatement{V: 1, AppID: s.cfg.AppID, Provider: s.provider,
		ObservedAt: ev.observedAt.Unix(), Nonce: nonce,
		Checks: ev.checks, OK: ev.ok, Detail: ev.detail, Facts: ev.facts}
	st.Scan.URL = s.recordURL()
	st.Scan.TlsPubkey = "0x" + hex.EncodeToString(s.scanPin)
	st.Scan.RecordSha256 = ev.recordSha
	st.Scan.Record = string(ev.raw)
	st.Scan.CheckedAt = ev.checkedAt
	st.Gate.OnFail = s.cfg.OnFail
	st.Gate.GatesSettlement = gatesSettlement
	st.Gate.MaxAgeSeconds = int(s.cfg.MaxAge() / time.Second)
	body, err := json.Marshal(st)
	if err != nil {
		return nil, nil, err
	}
	if s.sign == nil {
		return body, nil, nil
	}
	sig, err := s.sign(crypto.Keccak256(body))
	if err != nil {
		return nil, nil, fmt.Errorf("sign statement: %w", err)
	}
	return body, sig, nil
}

// AssayScanStatement serves /v1/attestation/assay/scan: the broker's own
// tappscan-backed check, signed by the TEE key. ErrScanNotReady before the
// first loop run; an error wrapping errNoScanSource when the source is not
// configured.
func (c *Ctrl) AssayScanStatement(nonce string) ([]byte, []byte, error) {
	if c.assayAttestor == nil || c.assayAttestor.scan == nil {
		return nil, nil, errNoScanSource
	}
	blocked, _ := c.assayAttestor.blockSettlement()
	return c.assayAttestor.scan.statement(nonce, blocked)
}

// AssayScanEnabled reports whether the tappscan source is the active one, so
// the relay endpoint can point at /scan.
func (c *Ctrl) AssayScanEnabled() bool {
	return c.assayAttestor != nil && c.assayAttestor.scan != nil
}

var errNoScanSource = errors.New("attestation source is not tappscan")
