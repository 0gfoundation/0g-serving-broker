package monitor

import (
	"fmt"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

func signToken(t *testing.T, key string, provider common.Address, expiry int64, v27 bool) string {
	t.Helper()
	k, err := crypto.HexToECDSA(key)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := crypto.Sign(accounts.TextHash([]byte(MetricsTokenMessage(provider, expiry))), k)
	if err != nil {
		t.Fatal(err)
	}
	if v27 { // what ethers' signMessage emits
		sig[64] += 27
	}
	return fmt.Sprintf("Bearer %d.%s", expiry, hexutil.Encode(sig))
}

func TestMetricsTokenMessage(t *testing.T) {
	// The scraper (0g-router cmd/metrics-signer) signs this exact text.
	got := MetricsTokenMessage(common.HexToAddress("0x158468A43ae7FEfDB53aDF0d6081d1959AaB1333"), 1800000300)
	if want := "0g-metrics:0x158468a43ae7fefdb53adf0d6081d1959aab1333:1800000300"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestVerifyMetricsToken(t *testing.T) {
	const readerKey = "4c0883a69102937d6231471b5dbb6204fe5129617082792ae468d01a3f362318"
	const otherKey = "8da4ef21b864d2cc526dbdb2a120bd2874c36c9d0a1fb7f8c63d7f7a8b41de8f"
	k, _ := crypto.HexToECDSA(readerKey)
	reader := crypto.PubkeyToAddress(k.PublicKey)
	me := common.HexToAddress("0x158468A43ae7FEfDB53aDF0d6081d1959AaB1333")
	other := common.HexToAddress("0x787A66B5dB48F23494aA7384f51EDfEF1911341D")
	now := time.Unix(1_800_000_000, 0)
	ok := now.Add(2 * time.Minute).Unix()

	cases := []struct {
		name    string
		header  string
		allowed common.Address
		wantErr bool
	}{
		{"valid, v=27/28", signToken(t, readerKey, me, ok, true), reader, false},
		{"valid, raw v", signToken(t, readerKey, me, ok, false), reader, false},
		{"token for another provider", signToken(t, readerKey, other, ok, true), reader, true},
		{"no reader configured", signToken(t, readerKey, me, ok, true), common.Address{}, true},
		{"wrong signer", signToken(t, otherKey, me, ok, true), reader, true},
		{"expired", signToken(t, readerKey, me, now.Add(-time.Second).Unix(), true), reader, true},
		{"expiry now", signToken(t, readerKey, me, now.Unix(), true), reader, true},
		{"lifetime too long", signToken(t, readerKey, me, now.Add(MaxMetricsTokenLifetime+time.Second).Unix(), true), reader, true},
		{"missing", "", reader, true},
		{"not bearer", "Basic abc", reader, true},
		{"no dot", "Bearer 123", reader, true},
		{"bad sig hex", fmt.Sprintf("Bearer %d.0xzz", ok), reader, true},
	}
	for _, c := range cases {
		err := VerifyMetricsToken(c.header, c.allowed, me, now)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err=%v, wantErr=%v", c.name, err, c.wantErr)
		}
	}

	// A token for one expiry must not verify after the expiry is edited.
	h := signToken(t, readerKey, me, ok, true)
	tampered := fmt.Sprintf("Bearer %d.%s", ok+1, h[len(fmt.Sprintf("Bearer %d.", ok)):])
	if VerifyMetricsToken(tampered, reader, me, now) == nil {
		t.Error("tampered expiry verified")
	}
}
