package tee

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// The recorder and the broker must derive the SAME enc public key from the same material, or the
// binding is a comparison between two unrelated values.
//
// This is the one place the two sides can drift silently. The controller writes
// EncPublicKeyFromMaterial's answer into the RTMR3 record; the broker publishes what getEncKey
// derives into report_data. If either conditioned the material differently — hex-decoding it,
// trimming a prefix, hashing it first — the reader would refuse every honest deployment, and it
// would refuse them for a reason that looks exactly like an attack.
func TestTheRecorderAndTheBrokerDeriveTheSameEncKey(t *testing.T) {
	// What the derivation service returns: a hex string. Passed through as bytes, NOT decoded.
	const material = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

	recorded, err := EncPublicKeyFromMaterial(material)
	if err != nil {
		t.Fatalf("EncPublicKeyFromMaterial() = %v", err)
	}

	// Exactly what getEncKey does with the material it receives.
	_, published, err := deriveEncKey([]byte(material))
	if err != nil {
		t.Fatalf("deriveEncKey() = %v", err)
	}

	if !bytes.Equal(recorded, published) {
		t.Fatalf("the recorder derives %x but the broker publishes %x: the record would name a key no honest quote can match", recorded, published)
	}
	if len(recorded) == 0 {
		t.Fatal("derived an empty key, so the comparison above proves nothing")
	}
}

// And it must NOT be the key a hex-decode of the same material yields, since that is the
// plausible way to "fix" one side and the failure it causes is indistinguishable from an attack.
func TestConditioningTheMaterialDifferentlyDerivesADifferentKey(t *testing.T) {
	const material = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

	asBytes, err := EncPublicKeyFromMaterial(material)
	if err != nil {
		t.Fatalf("EncPublicKeyFromMaterial() = %v", err)
	}
	_, decoded, err := deriveEncKey(mustHex(t, material))
	if err != nil {
		t.Fatalf("deriveEncKey() = %v", err)
	}

	if bytes.Equal(asBytes, decoded) {
		t.Fatal("hex-decoding the material derives the same key, so the pass-through comment above guards nothing")
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("decoding %q: %v", s, err)
	}
	return b
}
