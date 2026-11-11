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

func signToken(t *testing.T, key string, expiry int64, v27 bool) string {
	t.Helper()
	k, err := crypto.HexToECDSA(key)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := crypto.Sign(accounts.TextHash([]byte(MetricsTokenMessage(expiry))), k)
	if err != nil {
		t.Fatal(err)
	}
	if v27 { // what ethers' signMessage emits
		sig[64] += 27
	}
	return fmt.Sprintf("Bearer %d.%s", expiry, hexutil.Encode(sig))
}

func TestVerifyMetricsToken(t *testing.T) {
	const readerKey = "4c0883a69102937d6231471b5dbb6204fe5129617082792ae468d01a3f362318"
	const otherKey = "8da4ef21b864d2cc526dbdb2a120bd2874c36c9d0a1fb7f8c63d7f7a8b41de8f"
	k, _ := crypto.HexToECDSA(readerKey)
	reader := crypto.PubkeyToAddress(k.PublicKey)
	now := time.Unix(1_800_000_000, 0)
	ok := now.Add(2 * time.Minute).Unix()

	cases := []struct {
		name    string
		header  string
		allowed common.Address
		wantErr bool
	}{
		{"valid, v=27/28", signToken(t, readerKey, ok, true), reader, false},
		{"valid, raw v", signToken(t, readerKey, ok, false), reader, false},
		{"no reader configured", signToken(t, readerKey, ok, true), common.Address{}, true},
		{"wrong signer", signToken(t, otherKey, ok, true), reader, true},
		{"expired", signToken(t, readerKey, now.Add(-time.Second).Unix(), true), reader, true},
		{"expiry now", signToken(t, readerKey, now.Unix(), true), reader, true},
		{"lifetime too long", signToken(t, readerKey, now.Add(MaxMetricsTokenLifetime+time.Second).Unix(), true), reader, true},
		{"missing", "", reader, true},
		{"not bearer", "Basic abc", reader, true},
		{"no dot", "Bearer 123", reader, true},
		{"bad sig hex", fmt.Sprintf("Bearer %d.0xzz", ok), reader, true},
	}
	for _, c := range cases {
		err := VerifyMetricsToken(c.header, c.allowed, now)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err=%v, wantErr=%v", c.name, err, c.wantErr)
		}
	}

	// A token for one expiry must not verify after the expiry is edited.
	h := signToken(t, readerKey, ok, true)
	tampered := fmt.Sprintf("Bearer %d.%s", ok+1, h[len(fmt.Sprintf("Bearer %d.", ok)):])
	if VerifyMetricsToken(tampered, reader, now) == nil {
		t.Error("tampered expiry verified")
	}
}
