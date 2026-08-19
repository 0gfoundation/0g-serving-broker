package tee

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// TestSignRecoversToBoundAddress is the property the quote-response signature rests on:
// a caller recovers the signature to the address report_data binds, and nothing else.
// Written against TeeService.Sign rather than the handler because that is where the
// prefixing lives — a change to either the hash or the "\x19Ethereum Signed Message"
// prefix breaks recovery, and a caller following doc/attestation-trust-chain.md would
// see a valid-looking response it cannot authenticate.
func TestSignRecoversToBoundAddress(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	s := &TeeService{ProviderSigner: key, Address: crypto.PubkeyToAddress(key.PublicKey)}

	body := []byte(`{"quote":"00","event_log":"[]","nvidia_payload":{"arch":"HOPPER"}}`)
	sig, err := s.Sign(crypto.Keccak256(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(sig) != 65 {
		t.Fatalf("signature is %d bytes, want 65", len(sig))
	}
	if sig[64] != 27 && sig[64] != 28 {
		t.Fatalf("v is %d, want 27 or 28 — a caller using eth_personal recovery will fail", sig[64])
	}

	recovered := recoverAddr(t, body, sig)
	if recovered != s.Address {
		t.Fatalf("recovered %s, want the bound address %s", recovered, s.Address)
	}

	// The signature must be about these bytes. A body altered anywhere — the GPU
	// evidence being the case this exists for — must not recover to the same address.
	tampered := []byte(`{"quote":"00","event_log":"[]","nvidia_payload":{"arch":"BLACKWELL"}}`)
	if got := recoverAddr(t, tampered, sig); got == s.Address {
		t.Fatal("a tampered body recovered to the bound address")
	}
}

func recoverAddr(t *testing.T, body, sig []byte) common.Address {
	t.Helper()
	prefixed := crypto.Keccak256([]byte("\x19Ethereum Signed Message:\n32"), crypto.Keccak256(body))
	v := append(append([]byte{}, sig[:64]...), sig[64]-27)
	pub, err := crypto.SigToPub(prefixed, v)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	return crypto.PubkeyToAddress(*pub)
}
