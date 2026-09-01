package ctrl

import (
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"

	"github.com/0glabs/0g-serving-broker/fine-tuning/schema"
)

// validateSignature has an unused receiver, so a nil *Ctrl is enough to exercise
// it — no database, no contract client.
//
// These are unit tests on purpose: the existing task_test.go is behind
// //go:build integration, so the recovery-id and address-comparison behaviour
// this file pins was not covered by a default `go test ./...` run.
func signedTask(t *testing.T, userAddress func(signer string) string, toEthereumV bool) *schema.Task {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	id := uuid.New()
	raw, err := id.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	sig, err := crypto.Sign(accounts.TextHash(crypto.Keccak256(raw)[:]), key)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if toEthereumV {
		sig[64] += 27
	}
	signer := crypto.PubkeyToAddress(key.PublicKey).Hex()
	return &schema.Task{
		ID:          &id,
		UserAddress: userAddress(signer),
		Signature:   hexutil.Encode(sig),
	}
}

func TestValidateSignature(t *testing.T) {
	asIs := func(s string) string { return s }
	lower := strings.ToLower
	upper := func(s string) string { return "0X" + strings.ToUpper(strings.TrimPrefix(s, "0x")) }

	tests := []struct {
		name        string
		userAddress func(string) string
		ethereumV   bool
	}{
		// crypto.Sign's native output. Before the recovery-id normalisation this
		// underflowed to 229 and SigToPub refused it.
		{name: "raw 0/1 recovery id", userAddress: asIs, ethereumV: false},
		{name: "ethereum 27/28 recovery id", userAddress: asIs, ethereumV: true},
		// The client's JSON body is normalised nowhere on the path, so every
		// spelling of the same address has to verify.
		{name: "lowercase address in the body", userAddress: lower, ethereumV: true},
		{name: "uppercase address in the body", userAddress: upper, ethereumV: true},
		{name: "lowercase address with a raw recovery id", userAddress: lower, ethereumV: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := (*Ctrl)(nil).validateSignature(signedTask(t, tt.userAddress, tt.ethereumV)); err != nil {
				t.Fatalf("valid signature rejected: %v", err)
			}
		})
	}
}

func TestValidateSignatureRejectsAnotherAddress(t *testing.T) {
	other, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	task := signedTask(t, func(string) string {
		return crypto.PubkeyToAddress(other.PublicKey).Hex()
	}, true)

	if err := (*Ctrl)(nil).validateSignature(task); err == nil {
		t.Fatal("a signature from a different key must not verify")
	}
}

func TestValidateSignatureRejectsWrongLength(t *testing.T) {
	task := signedTask(t, func(s string) string { return s }, true)
	task.Signature = task.Signature[:len(task.Signature)-2]

	if err := (*Ctrl)(nil).validateSignature(task); err == nil {
		t.Fatal("a truncated signature must not verify")
	}
}
