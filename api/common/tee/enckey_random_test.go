package tee

import (
	"bytes"
	"context"
	"testing"
)

// deriveCounter fails the test if anything asks dstack for the enc key: on the local
// (controller-less) path the key must not come from GetKey at all.
type deriveCounter struct{ calls []string }

func (d *deriveCounter) TdxQuote(context.Context, []byte, bool) (string, error) { return "", nil }
func (d *deriveCounter) DeriveKey(_ context.Context, path string) (string, error) {
	d.calls = append(d.calls, path)
	return "material", nil
}

// Without the controller the enc key is generated, not derived: two starts give two keys,
// and neither asked dstack — so no other compose under the same app id can re-derive it.
func TestGetEncKey_LocalIsRandomAndNotDerived(t *testing.T) {
	c := &deriveCounter{}
	s := &TeeService{clientType: Phala}
	priv1, pub1, err := s.getEncKey(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	_, pub2, err := s.getEncKey(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.calls) != 0 {
		t.Fatalf("DeriveKey called on %v; the local enc key must not come from dstack", c.calls)
	}
	if len(pub1) != 32 || len(priv1) == 0 {
		t.Fatalf("bad key sizes: pub %d priv %d", len(pub1), len(priv1))
	}
	if bytes.Equal(pub1, pub2) {
		t.Fatal("two starts produced the same enc key")
	}
}

// The mock backend keeps deriving, so test fixtures stay reproducible.
func TestGetEncKey_MockStillDerives(t *testing.T) {
	c := &deriveCounter{}
	s := &TeeService{clientType: Mock}
	_, pub1, _ := s.getEncKey(context.Background(), c)
	_, pub2, _ := s.getEncKey(context.Background(), c)
	if len(c.calls) != 2 || !bytes.Equal(pub1, pub2) {
		t.Fatalf("mock: calls=%v equal=%t, want 2 derivations of one key", c.calls, bytes.Equal(pub1, pub2))
	}
}
