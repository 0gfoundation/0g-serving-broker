package ctrl

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/0glabs/0g-serving-broker/inference/config"
)

// The fixture is the REAL tappscan record for assay-verifier captured on
// 2026-09-09 (signer 0xe4bb…2470, image gcp/uki/v0.7.0/dev.json, tls key
// 0xb914…7aea). Every negative case below mutates one thing and expects
// exactly that check to fail.

const (
	fxSigner = "0xe4bbf6bfae12b2a048e5c0555af03146721f2470"
	fxTLS    = "0xb914d74752fcd1b9bfa85f63b769ae9610f75e26230b766539814ed5b84a7aea"
	fxImage  = "gcp/uki/v0.7.0/dev.json"
	fxUki    = "d46fa2f61b17d71f665ae44267b5d5eb5b528fb489ced362e5098dceb9b3aad5febe7dda89953ceae153b352305e66fa"
)

func loadScanFixture(t *testing.T) ([]byte, map[string]any) {
	t.Helper()
	raw, err := os.ReadFile("testdata/tappscan_assay_verifier.json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return raw, m
}

func currentStatus(m map[string]any) map[string]any {
	for _, s := range m["signers"].([]any) {
		sg := s.(map[string]any)
		if sg["current"] == true {
			return sg["status"].(map[string]any)
		}
	}
	return nil
}

func goodInputs(m map[string]any) scanInputs {
	st := currentStatus(m)
	return scanInputs{
		now:           time.Unix(int64(st["checked_at"].(float64)), 0).Add(30 * time.Minute),
		pinOK:         true,
		chainSigner:   fxSigner,
		liveTLS:       fxTLS,
		expectedImage: fxImage,
		expectedUki:   fxUki,
		requireTcb:    []string{"UpToDate"},
		maxRecordAge:  6 * time.Hour,
	}
}

func remarshal(t *testing.T, m map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestScanFixturePasses(t *testing.T) {
	raw, m := loadScanFixture(t)
	ev := evaluateScanRecord(raw, goodInputs(m))
	if !ev.ok {
		t.Fatalf("expected ok, got %q", ev.detail)
	}
	if ev.tlsKey != fxTLS {
		t.Fatalf("pin %s", ev.tlsKey)
	}
	if !strings.HasPrefix(ev.recordSha, "0x") || len(ev.recordSha) != 66 {
		t.Fatalf("record sha %q", ev.recordSha)
	}
	if ev.uki != fxUki || ev.image != fxImage || ev.signer != fxSigner {
		t.Fatalf("facts %+v", ev)
	}
}

func TestScanUkiOptional(t *testing.T) {
	raw, m := loadScanFixture(t)
	in := goodInputs(m)
	in.expectedUki = ""
	if ev := evaluateScanRecord(raw, in); !ev.ok || !ev.checks.Uki {
		t.Fatalf("uki unset must pass: %q", ev.detail)
	}
}

// Each row: mutate, expect ok=false and the named check false, all others true.
func TestScanEachCheckFails(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(in *scanInputs, m map[string]any)
		want   string
	}{
		{"pin", func(in *scanInputs, m map[string]any) { in.pinOK = false }, "pin"},
		{"chain_signer_mismatch", func(in *scanInputs, m map[string]any) { in.chainSigner = "0x0000000000000000000000000000000000000001" }, "chain_signer"},
		{"chain_error", func(in *scanInputs, m map[string]any) { in.chainErr = errors.New("rpc down") }, "chain_signer"},
		{"image", func(in *scanInputs, m map[string]any) { in.expectedImage = "gcp/uki/v0.7.0/prod.json" }, "image"},
		{"uki", func(in *scanInputs, m map[string]any) { in.expectedUki = "00" + fxUki[2:] }, "uki"},
		{"tcb", func(in *scanInputs, m map[string]any) { in.requireTcb = []string{"SWHardeningNeeded"} }, "tcb"},
		{"live_tls_mismatch", func(in *scanInputs, m map[string]any) { in.liveTLS = "0x" + strings.Repeat("ab", 32) }, "tls_key"},
		{"live_tls_error", func(in *scanInputs, m map[string]any) { in.liveErr = errors.New("dial timeout") }, "tls_key"},
		{"stale", func(in *scanInputs, m map[string]any) { in.now = in.now.Add(7 * time.Hour) }, "fresh"},
		{"record_error", func(in *scanInputs, m map[string]any) { currentStatus(m)["error"] = "evidence fetch failed" }, "record"},
		{"verifier_fault", func(in *scanInputs, m map[string]any) { currentStatus(m)["verifier_fault"] = true }, "record"},
		{"signer_bound", func(in *scanInputs, m map[string]any) {
			currentStatus(m)["attested"].(map[string]any)["signer_ok"] = false
		}, "signer_bound"},
		{"replay", func(in *scanInputs, m map[string]any) {
			currentStatus(m)["attested"].(map[string]any)["runtime_replay_ok"] = false
		}, "replay"},
		{"note", func(in *scanInputs, m map[string]any) {
			currentStatus(m)["attested"].(map[string]any)["note"] = "freshness unproven"
		}, "clean"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, m := loadScanFixture(t)
			in := goodInputs(m)
			tc.mutate(&in, m)
			ev := evaluateScanRecord(remarshal(t, m), in)
			if ev.ok {
				t.Fatalf("expected failure")
			}
			b, _ := json.Marshal(ev.checks)
			var flags map[string]bool
			_ = json.Unmarshal(b, &flags)
			for k, v := range flags {
				if k == tc.want && v {
					t.Fatalf("check %s should be false: %s", k, ev.detail)
				}
				if k != tc.want && !v {
					t.Fatalf("only %s should fail, but %s did: %s", tc.want, k, ev.detail)
				}
			}
			if !strings.Contains(ev.detail, tc.want) {
				t.Fatalf("detail %q does not name %s", ev.detail, tc.want)
			}
		})
	}
}

func TestScanRecordShapeFailures(t *testing.T) {
	_, m := loadScanFixture(t)
	in := goodInputs(m)
	// Not JSON at all (fetch failed → empty raw).
	if ev := evaluateScanRecord(nil, in); ev.ok || ev.checks.Record {
		t.Fatalf("empty raw must fail record: %+v", ev.checks)
	}
	// Two current signers.
	for _, s := range m["signers"].([]any) {
		s.(map[string]any)["current"] = true
	}
	if ev := evaluateScanRecord(remarshal(t, m), in); ev.ok || !strings.Contains(ev.detail, "current signers") {
		t.Fatalf("two current signers must fail: %q", ev.detail)
	}
	// No attested block.
	_, m = loadScanFixture(t)
	delete(currentStatus(m), "attested")
	if ev := evaluateScanRecord(remarshal(t, m), in); ev.ok || !strings.Contains(ev.detail, "no attested") {
		t.Fatalf("missing attested must fail: %q", ev.detail)
	}
}

// The statement is signed the way ZG-Body-Sig and ZG-Quote-Signature are:
// personal_sign over keccak256(body). A client recovers it with the same
// code it already has for the quote.
func TestScanStatementSignsAndRecovers(t *testing.T) {
	raw, m := loadScanFixture(t)
	key, _ := crypto.GenerateKey()
	sign := func(hash []byte) ([]byte, error) { return crypto.Sign(accounts.TextHash(hash), key) }
	var cfg config.AssayAttestation
	cfg.AppID = "assay-verifier"
	cfg.OnFail = "block-settlement"
	cfg.MaxAgeSeconds = 3600
	cfg.Tappscan.URL = "https://35.253.66.70"
	cfg.Tappscan.PubkeyPin = "0x7b13d1320e7ebc93a6edf809d06cf9b44704677461c6feb2c4204e92e5587e9b"
	cfg.Expected.Image = fxImage
	s, err := newScanSource(cfg, "https://35.225.160.143:8200", sign, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.provider = "0xf92082dC0268623849e35c9B62BD05047d27191e"
	if _, _, err := s.statement("", false); !errors.Is(err, ErrScanNotReady) {
		t.Fatalf("before first run: %v", err)
	}
	ev := evaluateScanRecord(raw, goodInputs(m))
	s.last = &ev
	if _, _, err := s.statement("zz", false); !errors.Is(err, ErrScanBadNonce) {
		t.Fatalf("bad nonce: %v", err)
	}
	body, sig, err := s.statement("0a0b", true)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := crypto.SigToPub(accounts.TextHash(crypto.Keccak256(body)), sig)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := crypto.PubkeyToAddress(*pub), crypto.PubkeyToAddress(key.PublicKey); got != want {
		t.Fatalf("recovered %s, want %s", got.Hex(), want.Hex())
	}
	var st scanStatement
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatal(err)
	}
	if st.Nonce != "0x0a0b" || !st.OK || !st.Gate.GatesSettlement || st.Gate.OnFail != "block-settlement" {
		t.Fatalf("statement %+v", st)
	}
	if st.Scan.Record != string(raw) || st.Scan.RecordSha256 != ev.recordSha {
		t.Fatalf("record not embedded verbatim")
	}
	if st.Facts.ChainSigner != fxSigner || st.Facts.LiveTLS != fxTLS {
		t.Fatalf("facts %+v", st.Facts)
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(st.Scan.TlsPubkey, "0x")); err != nil || st.Scan.TlsPubkey != cfg.Tappscan.PubkeyPin {
		t.Fatalf("scan pin %s", st.Scan.TlsPubkey)
	}
	// Tampering with one byte breaks recovery to the same address.
	body[len(body)-3] ^= 1
	pub, _ = crypto.SigToPub(accounts.TextHash(crypto.Keccak256(body)), sig)
	if pub != nil && crypto.PubkeyToAddress(*pub) == crypto.PubkeyToAddress(key.PublicKey) {
		t.Fatalf("tampered body still recovers")
	}
}
