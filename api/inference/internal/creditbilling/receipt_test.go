package creditbilling

import (
	"testing"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

// sharedVectorReceipt and sharedVectorCanonicalText are duplicated verbatim in
// the credit service (0g-credit-service internal/receipt) test. If either repo
// changes the canonical format, the constant below must change in lockstep or
// signatures stop verifying cross-repo.
var sharedVectorReceipt = Receipt{
	User:        "0x1111111111111111111111111111111111111111",
	Provider:    "0x2222222222222222222222222222222222222222",
	ReqHash:     "aabbcc",
	RespHash:    "ddeeff",
	InputCount:  100,
	OutputCount: 250,
	FeeMicroUsd: "1234",
	Nonce:       7,
	Timestamp:   1700000000,
}

const sharedVectorCanonicalText = "0g-credit-receipt-v1:0x1111111111111111111111111111111111111111:0x2222222222222222222222222222222222222222:aabbcc:ddeeff:100:250:1234:7:1700000000"

func TestCanonicalTextVector(t *testing.T) {
	if got := sharedVectorReceipt.CanonicalText(); got != sharedVectorCanonicalText {
		t.Fatalf("canonical text drift:\n got=%q\nwant=%q", got, sharedVectorCanonicalText)
	}
}

// TestSignSchemeVerifiable confirms the exact preimage a verifier reconstructs
// (Keccak256(prefix || Keccak256(text))) recovers the signing address — the same
// scheme TeeService.Sign uses internally.
func TestSignSchemeVerifiable(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	addr := crypto.PubkeyToAddress(key.PublicKey)

	messageHash := crypto.Keccak256([]byte(sharedVectorReceipt.CanonicalText()))
	prefixed := crypto.Keccak256([]byte("\x19Ethereum Signed Message:\n32"), messageHash)
	sig, err := crypto.Sign(prefixed, key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if sig[64] == 0 || sig[64] == 1 {
		sig[64] += 27
	}
	_ = hexutil.Encode(sig)

	// Recover the way the credit service does.
	v := sig[64]
	if v >= 27 {
		v -= 27
	}
	rec := make([]byte, 65)
	copy(rec, sig[:64])
	rec[64] = v
	pub, err := crypto.SigToPub(prefixed, rec)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if got := crypto.PubkeyToAddress(*pub); got != addr {
		t.Fatalf("recovered %s, want %s", got.Hex(), addr.Hex())
	}
}

func TestFeeMicroUsd(t *testing.T) {
	// $2 per million input tokens => 2_000_000 micro per million.
	// 500_000 input tokens => fee = 2_000_000 * 500_000 / 1_000_000 = 1_000_000 micro-USD ($1).
	if got, err := FeeMicroUsd(500_000, 0, 2_000_000, 0); err != nil || got != 1_000_000 {
		t.Fatalf("input fee: got %d, err %v, want 1000000", got, err)
	}
	// input 100 @ $3/M (3_000_000) + output 250 @ $6/M (6_000_000)
	// = (100*3_000_000 + 250*6_000_000)/1_000_000 = (3e8 + 1.5e9)/1e6 = 1800
	if got, err := FeeMicroUsd(100, 250, 3_000_000, 6_000_000); err != nil || got != 1800 {
		t.Fatalf("combined fee: got %d, err %v, want 1800", got, err)
	}
	// Negative input must error, not silently compute.
	if _, err := FeeMicroUsd(-1, 0, 1, 0); err == nil {
		t.Fatal("expected error for negative input count")
	}
	// Overflow must error, not wrap. maxInt64 * maxInt64 / 1e6 far exceeds int64.
	const maxInt64 = int64(9223372036854775807)
	if _, err := FeeMicroUsd(maxInt64, 0, maxInt64, 0); err == nil {
		t.Fatal("expected overflow error for huge product")
	}
}

func TestUsdDecimalToMicro(t *testing.T) {
	cases := map[string]int64{"2": 2_000_000, "0.04": 40_000, "1.5": 1_500_000, "0": 0}
	for in, want := range cases {
		got, err := UsdDecimalToMicro(in)
		if err != nil {
			t.Fatalf("UsdDecimalToMicro(%q): %v", in, err)
		}
		if got != want {
			t.Fatalf("UsdDecimalToMicro(%q): got %d, want %d", in, got, want)
		}
	}
	if _, err := UsdDecimalToMicro("-1"); err == nil {
		t.Fatal("expected error for negative USD price")
	}
	if _, err := UsdDecimalToMicro("notanumber"); err == nil {
		t.Fatal("expected error for unparseable USD price")
	}
}
