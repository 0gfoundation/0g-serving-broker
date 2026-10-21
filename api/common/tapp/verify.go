package tapp

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/go-tdx-guest/abi"
	"github.com/google/go-tdx-guest/pcs"
	pb "github.com/google/go-tdx-guest/proto/tdx"
	"github.com/google/go-tdx-guest/verify"
)

// Policy is everything Verify needs besides the evidence. Every field is an
// independent root: the chain (Signer), the caller's randomness (Nonce), the
// public reference values (ExpectedUki), the operator's TCB stance.
type Policy struct {
	// Signer is the address the TappRegistry admits for this app, lowercase
	// or not; runtime_data.signer must equal it.
	Signer string
	// Nonce must be echoed in runtime_data.nonce. Empty disables the check
	// (a cached statement served to many readers); callers verifying for
	// themselves always send one.
	Nonce []byte
	// ExpectedUki lists acceptable "measurement.uki.SHA-384" digests from
	// 0g-tapp/verifier/reference-values; the log's UKI must be one of them.
	ExpectedUki []string
	// ExpectedMrTd optionally pins the firmware measurement (MRTD, hex); empty
	// skips the check. The UKI digest is authenticated by the RTMR replay, and
	// the firmware is what measured it, so a hostile firmware is the one
	// remaining way to a log that replays yet lies. Pinning MRTD closes that at
	// the cost of tracking the cloud's TDVF releases.
	ExpectedMrTd []string
	// RequireTcb / AllowAdvisories mirror the broker's attestation config:
	// the PCS status must be listed, and every advisory must be allowed.
	RequireTcb      []string
	AllowAdvisories []string
	// PCSUpdate selects Intel's TCB evaluation track: "early" (default; what
	// tappscan reports) or "standard".
	PCSUpdate string
	// Now is the verification time (certificate and collateral validity).
	Now time.Time
	// Getter fetches a URL (Intel PCS). nil → plain https with a 20 s timeout.
	Getter func(ctx context.Context, url string) ([]byte, error)
}

// Checks is the per-item result, each one a separate root failing on its own.
type Checks struct {
	Evidence bool `json:"evidence"` // parsed, has quote + log + runtime_data
	Quote    bool `json:"quote"`    // Intel-signed TDX quote, PCK chain to Intel's root
	Binding  bool `json:"binding"`  // report_data == sha512(runtime_data)
	Nonce    bool `json:"nonce"`    // runtime_data.nonce echoes ours
	Signer   bool `json:"signer"`   // runtime_data.signer == chain signer
	TLSKey   bool `json:"tls_key"`  // runtime_data carries a 32-byte tls_public_key
	Replay   bool `json:"replay"`   // cc_eventlog replays to the quote's RTMRs
	Uki      bool `json:"uki"`      // the log's UKI digest is an expected one
	MrTd     bool `json:"mrtd"`     // firmware measurement ∈ ExpectedMrTd (true when not configured)
	Tcb      bool `json:"tcb"`      // PCS TCB status ∈ RequireTcb
	Clean    bool `json:"clean"`    // every PCS advisory ∈ AllowAdvisories
}

// Result of one verification. OK is the AND of Checks; Detail names what failed.
type Result struct {
	Checks Checks `json:"checks"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`

	Signer       string   `json:"signer,omitempty"`
	TLSPublicKey string   `json:"tls_public_key,omitempty"` // 0x-hex sha256 of the app's SPKI
	Uki          string   `json:"uki,omitempty"`            // the measured UKI digest(s), hex, comma-joined when none matched
	MrTd         string   `json:"mr_td,omitempty"`
	FMSPC        string   `json:"fmspc,omitempty"`
	TcbStatus    string   `json:"tcb_status,omitempty"`
	Advisories   []string `json:"advisories,omitempty"`
	TcbEvalNum   int      `json:"tcb_evaluation_data_number,omitempty"`
}

// Verify checks one evidence blob against the policy. It never returns an
// error: every failure is a false check with its reason in Detail, so the
// caller can publish the table instead of a bare verdict.
func Verify(ctx context.Context, raw []byte, pol Policy) Result {
	var res Result
	var failed []string
	fail := func(label, why string) { failed = append(failed, label+": "+why) }
	finish := func() Result {
		res.OK = len(failed) == 0
		if res.OK {
			res.Detail = fmt.Sprintf("tapp evidence verified: signer %s, tcb %s", res.Signer, res.TcbStatus)
		} else {
			res.Detail = strings.Join(failed, "; ")
		}
		return res
	}
	now := pol.Now
	if now.IsZero() {
		now = time.Now()
	}

	ev, err := ParseEvidence(raw)
	if err != nil {
		fail("evidence", err.Error())
		return finish()
	}
	res.Checks.Evidence = true

	// --- the quote: Intel signed it, for a genuine TD ---
	anyQuote, err := abi.QuoteToProto(ev.Quote)
	if err != nil {
		fail("quote", "cannot parse: "+err.Error())
		return finish()
	}
	quote, ok := anyQuote.(*pb.QuoteV4)
	if !ok {
		fail("quote", fmt.Sprintf("unsupported quote type %T", anyQuote))
		return finish()
	}
	body := quote.GetTdQuoteBody()
	// GetCollateral=false: the PCK certificate chain is still verified down to
	// Intel's embedded root and the quote signature against the attestation
	// key; what is skipped is the library's own TCB check, which refuses
	// anything but UpToDate. TCB status is evaluated below under our policy.
	opts := verify.DefaultOptions()
	opts.GetCollateral = false
	opts.Now = now
	if err := verify.TdxQuote(quote, opts); err != nil {
		fail("quote", err.Error())
	} else {
		res.Checks.Quote = true
	}

	// --- report_data binds runtime_data; runtime_data names signer, nonce, key ---
	want := ReportData(ev.RuntimeData)
	res.Checks.Binding = bytes.Equal(body.GetReportData(), want[:])
	if !res.Checks.Binding {
		fail("binding", "report_data is not sha512(runtime_data)")
	}
	rt, err := ev.Runtime()
	if err != nil {
		fail("binding", err.Error())
	}
	res.Signer = strings.ToLower(rt.Signer)
	res.TLSPublicKey = strings.ToLower(rt.TLSPublicKey)
	res.Checks.Nonce = len(pol.Nonce) == 0 || hexEq(rt.Nonce, hex.EncodeToString(pol.Nonce))
	if !res.Checks.Nonce {
		fail("nonce", fmt.Sprintf("sent %x, evidence echoes %q", pol.Nonce, rt.Nonce))
	}
	res.Checks.Signer = hexEq(rt.Signer, pol.Signer)
	if !res.Checks.Signer {
		fail("signer", fmt.Sprintf("evidence %q vs chain %q", rt.Signer, pol.Signer))
	}
	keyHex := strings.TrimPrefix(strings.ToLower(rt.TLSPublicKey), "0x")
	if kb, err := hex.DecodeString(keyHex); err == nil && len(kb) == 32 {
		res.Checks.TLSKey = true
		res.TLSPublicKey = "0x" + keyHex
	} else {
		fail("tls_key", fmt.Sprintf("runtime_data.tls_public_key %q is not a 32-byte hex digest", rt.TLSPublicKey))
	}

	// --- the event log is the one the quote measured ---
	events, err := ParseCCEventLog(ev.EventLog)
	if err != nil {
		fail("replay", err.Error())
	} else {
		mr := ReplayRTMRs(events)
		rtmrs := body.GetRtmrs()
		res.Checks.Replay = len(rtmrs) == 4
		for i := 0; i < 4 && i < len(rtmrs); i++ {
			if !bytes.Equal(rtmrs[i], mr[i][:]) {
				res.Checks.Replay = false
				var q [rtmrLen]byte
				copy(q[:], rtmrs[i])
				fail("replay", fmtMismatch(i, q, mr[i]))
			}
		}
		if len(rtmrs) != 4 {
			fail("replay", fmt.Sprintf("quote carries %d RTMRs", len(rtmrs)))
		}
		// --- and it names a genuine image ---
		ukis := BootComponents(events)["uki"]
		for _, u := range ukis {
			if slices.ContainsFunc(pol.ExpectedUki, func(e string) bool { return hexEq(e, u) }) {
				res.Checks.Uki, res.Uki = true, u
				break
			}
		}
		if !res.Checks.Uki {
			res.Uki = strings.Join(ukis, ",")
			fail("uki", fmt.Sprintf("measured [%s] vs expected %v", res.Uki, pol.ExpectedUki))
		}
	}

	res.MrTd = hex.EncodeToString(body.GetMrTd())
	res.Checks.MrTd = len(pol.ExpectedMrTd) == 0 || slices.ContainsFunc(pol.ExpectedMrTd, func(e string) bool { return hexEq(e, res.MrTd) })
	if !res.Checks.MrTd {
		fail("mrtd", fmt.Sprintf("mr_td %s not in %v", res.MrTd, pol.ExpectedMrTd))
	}

	// --- the platform TCB, from Intel PCS, under our policy ---
	status, advisories, fmspc, evalNum, err := tcbStatus(ctx, quote, pol, now)
	res.FMSPC, res.TcbStatus, res.Advisories, res.TcbEvalNum = fmspc, status, advisories, evalNum
	if err != nil {
		fail("tcb", err.Error())
	} else {
		res.Checks.Tcb = slices.Contains(pol.RequireTcb, status)
		if !res.Checks.Tcb {
			fail("tcb", fmt.Sprintf("tcb_status=%s not in %v", status, pol.RequireTcb))
		}
		var unlisted []string
		for _, a := range advisories {
			if !slices.Contains(pol.AllowAdvisories, a) {
				unlisted = append(unlisted, a)
			}
		}
		res.Checks.Clean = len(unlisted) == 0
		if !res.Checks.Clean {
			fail("clean", fmt.Sprintf("unlisted advisories=%v", unlisted))
		}
	}
	return finish()
}

// tcbStatusRank orders Intel's statuses from best to worst, so two levels
// (platform and TDX module) collapse to the worse one, as tappscan reports.
var tcbStatusRank = []string{"UpToDate", "SWHardeningNeeded", "ConfigurationNeeded",
	"ConfigurationAndSWHardeningNeeded", "OutOfDate", "OutOfDateConfigurationNeeded", "Revoked"}

func worse(a, b string) string {
	ia, ib := slices.Index(tcbStatusRank, a), slices.Index(tcbStatusRank, b)
	if ib > ia {
		return b
	}
	return a
}

func defaultGetter(ctx context.Context, url string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// tcbStatus evaluates the quote's platform and TDX-module TCB against Intel
// PCS's tcbInfo for its FMSPC, the way go-tdx-guest's collateral check does —
// except the status is returned for the policy to judge instead of failing
// on anything below UpToDate.
//
// ponytail: tcbInfo is read over TLS to api.trustedservices.intel.com without
// checking the JSON's own ECDSA signature (TCB-Info-Issuer-Chain); a wrong
// answer here needs a forged Intel TLS certificate. Verify the signature if
// the broker ever reads PCS through a cache or mirror.
func tcbStatus(ctx context.Context, quote *pb.QuoteV4, pol Policy, now time.Time) (status string, advisories []string, fmspc string, evalNum int, err error) {
	chain := quote.GetSignedData().GetCertificationData().GetQeReportCertificationData().GetPckCertificateChainData().GetPckCertChain()
	blk, _ := pem.Decode(chain)
	if blk == nil {
		return "", nil, "", 0, errors.New("quote carries no PCK certificate")
	}
	pck, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return "", nil, "", 0, fmt.Errorf("PCK certificate: %w", err)
	}
	ext, err := pcs.PckCertificateExtensions(pck)
	if err != nil {
		return "", nil, "", 0, fmt.Errorf("PCK extensions: %w", err)
	}
	fmspc = ext.FMSPC
	url := pcs.TcbInfoURL(fmspc)
	if pol.PCSUpdate == "" || pol.PCSUpdate == "early" {
		url += "&update=early"
	}
	get := pol.Getter
	if get == nil {
		get = defaultGetter
	}
	raw, err := get(ctx, url)
	if err != nil {
		return "", nil, fmspc, 0, fmt.Errorf("Intel PCS tcbInfo: %w", err)
	}
	var doc struct {
		TcbInfo pcs.TcbInfo `json:"tcbInfo"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", nil, fmspc, 0, fmt.Errorf("tcbInfo JSON: %w", err)
	}
	ti := doc.TcbInfo
	evalNum = ti.TcbEvaluationDataNumber
	if !strings.EqualFold(ti.Fmspc, fmspc) {
		return "", nil, fmspc, evalNum, fmt.Errorf("tcbInfo is for FMSPC %s, quote is %s", ti.Fmspc, fmspc)
	}
	if !ti.NextUpdate.IsZero() && now.After(ti.NextUpdate) {
		return "", nil, fmspc, evalNum, fmt.Errorf("tcbInfo expired at %s", ti.NextUpdate.Format(time.RFC3339))
	}
	body := quote.GetTdQuoteBody()
	teeSvn := body.GetTeeTcbSvn()
	svnOK := func(have []byte, want []pcs.TcbComponent) bool {
		if len(have) < len(want) {
			return false
		}
		for i, c := range want {
			if have[i] < c.Svn {
				return false
			}
		}
		return true
	}
	var level *pcs.TcbLevel
	for i := range ti.TcbLevels {
		l := &ti.TcbLevels[i]
		if svnOK(ext.TCB.CPUSvnComponents, l.Tcb.SgxTcbcomponents) && ext.TCB.PCESvn >= l.Tcb.Pcesvn && svnOK(teeSvn, l.Tcb.TdxTcbcomponents) {
			level = l
			break
		}
	}
	if level == nil {
		return "", nil, fmspc, evalNum, errors.New("no tcbInfo level matches the quote's SVNs")
	}
	status = string(level.TcbStatus)
	advisories = append(advisories, level.AdvisoryIDs...)
	if len(teeSvn) > 1 && teeSvn[1] > 0 {
		id := fmt.Sprintf("TDX_%02d", teeSvn[1])
		found := false
		for _, m := range ti.TdxModuleIdentities {
			if m.ID != id {
				continue
			}
			for _, l := range m.TcbLevels {
				if uint32(teeSvn[0]) >= l.Tcb.Isvsvn {
					status = worse(status, string(l.TcbStatus))
					advisories = append(advisories, l.AdvisoryIDs...)
					found = true
					break
				}
			}
		}
		if !found {
			return "", nil, fmspc, evalNum, fmt.Errorf("no tcbInfo level for TDX module %s isvsvn %d", id, teeSvn[0])
		}
	}
	slices.Sort(advisories)
	return status, slices.Compact(advisories), fmspc, evalNum, nil
}
