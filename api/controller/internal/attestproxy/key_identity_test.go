package attestproxy

import (
	"context"
	"strings"
	"testing"
)

// The paths, spelled out.
//
// Hardcoded rather than composed from the same constants the implementation uses, which
// would only assert that the code agrees with itself. These strings are what dstack runs
// HKDF over, so they ARE the keys: a change to any of them rotates a signer address, and
// the empty-identity forms below are the ones every deployment in the fleet is using
// right now.
func TestDerivationPathsAreExactly(t *testing.T) {
	const digest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	const setHash = "2222222222222222222222222222222222222222222222222222222222222222"

	for _, tc := range []struct {
		name     string
		id       KeyIdentity
		wantSign string
		wantEnc  string
	}{
		{
			// THE compatibility case. A deployment that records no upstream set — every
			// deployment with controller.recordUpstreamSet off, which is all of them —
			// must derive exactly what it derived before KeyIdentity existed. If this
			// fails, merging rotates every key in the fleet and resets every on-chain
			// signer acknowledgement.
			name:     "no set recorded: byte-identical to the pre-set paths",
			id:       KeyIdentity{Digest: digest},
			wantSign: "/sha256:1111111111111111111111111111111111111111111111111111111111111111/sign",
			wantEnc:  "/sha256:1111111111111111111111111111111111111111111111111111111111111111/e2ee-enc",
		},
		{
			// The set as a middle segment, which is what makes the key a function of where
			// plaintext may go.
			name:     "a set recorded",
			id:       KeyIdentity{Digest: digest, UpstreamSetHash: setHash},
			wantSign: "/sha256:1111111111111111111111111111111111111111111111111111111111111111/2222222222222222222222222222222222222222222222222222222222222222/sign",
			wantEnc:  "/sha256:1111111111111111111111111111111111111111111111111111111111111111/2222222222222222222222222222222222222222222222222222222222222222/e2ee-enc",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := SignerKeyPath(tc.id); got != tc.wantSign {
				t.Errorf("SignerKeyPath\n got %q\nwant %q", got, tc.wantSign)
			}
			if got := EncKeyPath(tc.id); got != tc.wantEnc {
				t.Errorf("EncKeyPath\n got %q\nwant %q", got, tc.wantEnc)
			}
			// The unexported wrappers the proxy actually calls must agree with the exported
			// ones the recorder calls. That is the whole reason both exist.
			if got := signerKeyPath(tc.id); got != tc.wantSign {
				t.Errorf("signerKeyPath disagrees with SignerKeyPath: %q vs %q", got, tc.wantSign)
			}
			if got := encKeyPath(tc.id); got != tc.wantEnc {
				t.Errorf("encKeyPath disagrees with EncKeyPath: %q vs %q", got, tc.wantEnc)
			}
		})
	}
}

// A change to either half must change the key, and the signer and enc keys must never
// share a path. Every pair below has to be distinct, in both directions.
func TestEveryDerivationPathIsDistinct(t *testing.T) {
	const d1 = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	const d2 = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	const h1 = "2222222222222222222222222222222222222222222222222222222222222222"
	const h2 = "4444444444444444444444444444444444444444444444444444444444444444"

	seen := map[string]string{}
	for _, id := range []KeyIdentity{
		{Digest: d1},
		{Digest: d2},
		{Digest: d1, UpstreamSetHash: h1},
		{Digest: d1, UpstreamSetHash: h2},
		{Digest: d2, UpstreamSetHash: h1},
	} {
		for kind, path := range map[string]string{"sign": SignerKeyPath(id), "enc": EncKeyPath(id)} {
			label := kind + " " + id.Digest[7:11] + "/" + firstFour(id.UpstreamSetHash)
			if prev, dup := seen[path]; dup {
				t.Errorf("%s and %s derive the same path %q", prev, label, path)
			}
			seen[path] = label
		}
	}
	if len(seen) != 10 {
		t.Errorf("got %d distinct paths from 5 identities x 2 keys, want 10", len(seen))
	}
}

// The unbound and bound forms must not be reachable from each other, in the one way the
// segment shape could allow it: an "empty" set hash spelled as something other than "".
//
// Whitespace, a slash, a dot — none of them may produce a path that reads as either form.
// The proxy refuses these before they get here (currentKeyIdentity validates hex), so this
// is about the paths themselves being unambiguous rather than about that guard.
func TestNoSetHashSpellingCollidesWithTheUnboundForm(t *testing.T) {
	const digest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	unbound := SignerKeyPath(KeyIdentity{Digest: digest})

	for _, spelling := range []string{" ", "/", ".", "..", "sign", "e2ee-enc", "\n"} {
		got := SignerKeyPath(KeyIdentity{Digest: digest, UpstreamSetHash: spelling})
		if got == unbound {
			t.Errorf("set hash %q produced the unbound path %q", spelling, got)
		}
		if got == EncKeyPath(KeyIdentity{Digest: digest}) {
			t.Errorf("set hash %q produced the unbound ENC path %q", spelling, got)
		}
	}
}

// The digest can never be mistaken for the set-hash segment: one carries the "sha256:"
// prefix and the other is bare hex, so no bound path can be read as an unbound one for a
// different image.
func TestADigestCannotPassForASetHash(t *testing.T) {
	const digest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	const bareHex = "1111111111111111111111111111111111111111111111111111111111111111"

	if strings.HasPrefix(bareHex, "sha256:") {
		t.Fatal("the fixture is wrong: a set hash must be bare hex")
	}
	if !upstreamSetHashPattern.MatchString(bareHex) {
		t.Fatal("the fixture is wrong: a set hash must match the pattern")
	}
	if upstreamSetHashPattern.MatchString(digest) {
		t.Error("a digest matches the set-hash pattern, so the two segments are confusable")
	}
	if imageDigestPattern.MatchString(bareHex) {
		t.Error("a bare set hash matches the digest pattern, so the two segments are confusable")
	}
}

// No identity source at all must refuse, not invent one.
//
// The guard has always been there and never had a test — a mutation replacing its error
// with a fabricated digest survived. It is the same judgement every refusal in
// currentKeyIdentity makes: a key derived from a value nobody established still produces
// signatures, and those signatures verify. Refusing is the only safe answer, and it is
// the answer a Proxy built without a source has to give rather than one it discovers by
// deriving something.
func TestNoIdentitySourceRefuses(t *testing.T) {
	p := &Proxy{}
	if id, err := p.currentKeyIdentity(t.Context()); err == nil {
		t.Fatalf("a Proxy with no identity source returned %+v, want a refusal", id)
	}
}

// Both halves are validated, and the refusals name which half was wrong — an operator
// reading the log has to be able to tell "the broker container could not be pinned down"
// from "the recorded set hash is malformed", because the two have different fixes.
func TestCurrentKeyIdentityRefusesEitherHalf(t *testing.T) {
	const goodDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	const goodHash = "2222222222222222222222222222222222222222222222222222222222222222"

	for _, tc := range []struct {
		name    string
		id      KeyIdentity
		wantErr string // "" means it must be accepted
	}{
		{"a digest and no set", KeyIdentity{Digest: goodDigest}, ""},
		{"a digest and a set", KeyIdentity{Digest: goodDigest, UpstreamSetHash: goodHash}, ""},
		{"no digest", KeyIdentity{}, "not a digest"},
		{"a tag rather than a digest", KeyIdentity{Digest: "ghcr.io/x:latest"}, "not a digest"},
		{"a truncated digest", KeyIdentity{Digest: "sha256:abc"}, "not a digest"},
		{"a set hash that is not hex", KeyIdentity{Digest: goodDigest, UpstreamSetHash: "zzzz"}, "not a hex sha256"},
		{"a short set hash", KeyIdentity{Digest: goodDigest, UpstreamSetHash: strings.Repeat("a", 63)}, "not a hex sha256"},
		{"a long set hash", KeyIdentity{Digest: goodDigest, UpstreamSetHash: strings.Repeat("a", 65)}, "not a hex sha256"},
		{"an uppercase set hash", KeyIdentity{Digest: goodDigest, UpstreamSetHash: strings.Repeat("A", 64)}, "not a hex sha256"},
		{"a set hash with a path separator", KeyIdentity{Digest: goodDigest, UpstreamSetHash: "a/b"}, "not a hex sha256"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.id
			p := &Proxy{currentIdentityFn: func(context.Context) (KeyIdentity, error) { return want, nil }}
			got, err := p.currentKeyIdentity(t.Context())
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("currentKeyIdentity() = %v, want it accepted", err)
				}
				if got != want {
					t.Errorf("currentKeyIdentity() = %+v, want %+v unchanged", got, want)
				}
				return
			}
			if err == nil {
				t.Fatalf("accepted %+v, want a refusal", tc.id)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not say %q, so it does not name which half was wrong", err, tc.wantErr)
			}
		})
	}
}

func firstFour(s string) string {
	if len(s) < 4 {
		return "none"
	}
	return s[:4]
}
