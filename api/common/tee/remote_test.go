package tee

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

// testSignerKeyHex is the key the fake controller signs with, so a test can compute what
// local signing would have produced for the same hash.
const testSignerKeyHex = "4c0883a69102937d6231471b5dbb6204fe512961708279f2e3a0c1f0e4b1a4b1"

// fakeController stands in for the controller's attestation proxy on a unix socket, answering
// the three operations a remoteSigner calls. handler lets a test override one of them.
func fakeController(t *testing.T, handler http.HandlerFunc) *remoteSigner {
	t.Helper()

	path := filepath.Join(t.TempDir(), "tee.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listening on the fake controller socket: %v", err)
	}
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() { _ = srv.Close() })

	return newRemoteSigner(path)
}

// signingController answers /Sign, /SignerAddress and /GetEncKey the way the real controller
// does: with testSignerKeyHex's signature, address, and some enc key material.
func signingController(t *testing.T) *remoteSigner {
	t.Helper()

	key, err := crypto.HexToECDSA(testSignerKeyHex)
	if err != nil {
		t.Fatalf("parsing the test key: %v", err)
	}
	return fakeController(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Sign":
			var req struct {
				Hash string `json:"hash"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			hash, err := hex.DecodeString(req.Hash)
			if err != nil || len(hash) != 32 {
				http.Error(w, "bad hash", http.StatusBadRequest)
				return
			}
			sig, err := crypto.Sign(hash, key)
			if err != nil {
				http.Error(w, "sign failed", http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"signature": hex.EncodeToString(sig)})
		case "/SignerAddress":
			_ = json.NewEncoder(w).Encode(map[string]string{
				"address": crypto.PubkeyToAddress(key.PublicKey).Hex(),
			})
		case "/GetEncKey":
			_ = json.NewEncoder(w).Encode(map[string]string{"key": "deadbeef"})
		default:
			http.NotFound(w, r)
		}
	})
}

// The signature the controller returns must be the signature local signing would have
// produced for the same key and hash, byte for byte.
//
// This is what lets the recovery-id fixup stay at each call site and lets one deployment
// verify signatures produced by the other: if the remote path applied the fixup itself, or
// re-encoded, or reordered r/s/v, every existing verifier would need to know which path
// signed — and none of them can tell.
func TestRemoteSigningIsByteIdenticalToLocalSigning(t *testing.T) {
	key, err := crypto.HexToECDSA(testSignerKeyHex)
	if err != nil {
		t.Fatalf("parsing the test key: %v", err)
	}
	hash := crypto.Keccak256([]byte("a response worth attesting to"))

	local := &TeeService{ProviderSigner: key}
	want, err := local.SignHash(hash)
	if err != nil {
		t.Fatalf("local SignHash: %v", err)
	}

	remote := &TeeService{remote: signingController(t)}
	got, err := remote.SignHash(hash)
	if err != nil {
		t.Fatalf("remote SignHash: %v", err)
	}

	if !bytes.Equal(got, want) {
		t.Errorf("remote signature %x, local %x — they must be identical", got, want)
	}
	if len(got) != signatureLen {
		t.Errorf("signature is %d bytes, want %d", len(got), signatureLen)
	}
	// No fixup applied on the way through: v is still 0 or 1, and each call site adds 27.
	if got[64] > 1 {
		t.Errorf("recovery id is %d, want the raw 0/1 — the fixup belongs to the caller", got[64])
	}
}

// Sign and SignEIP712 are the methods everything else calls, so the seam has to hold for them
// too, not only for SignHash.
func TestSignAndSignEIP712GoThroughTheSeam(t *testing.T) {
	key, err := crypto.HexToECDSA(testSignerKeyHex)
	if err != nil {
		t.Fatalf("parsing the test key: %v", err)
	}
	local := &TeeService{ProviderSigner: key}
	remote := &TeeService{remote: signingController(t)}

	for _, tc := range []struct {
		name string
		call func(*TeeService) ([]byte, error)
	}{
		{"Sign", func(s *TeeService) ([]byte, error) { return s.Sign(crypto.Keccak256([]byte("m"))) }},
		{"SignEIP712", func(s *TeeService) ([]byte, error) { return s.SignEIP712(crypto.Keccak256([]byte("d"))) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, err := tc.call(local)
			if err != nil {
				t.Fatalf("local: %v", err)
			}
			got, err := tc.call(remote)
			if err != nil {
				t.Fatalf("remote: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("remote %x, local %x", got, want)
			}
			// Both paths must still carry the Ethereum 27/28 recovery id.
			if got[64] != 27 && got[64] != 28 {
				t.Errorf("recovery id is %d, want 27 or 28", got[64])
			}
		})
	}
}

// A local deployment must not acquire a remote signer by accident: with TEE_SOCKET unset the
// key stays in this process and the signature is exactly what it was before this change
// existed. That is the invariant that keeps every controller-less deployment untouched.
func TestNoSocketMeansTheKeyStaysLocal(t *testing.T) {
	t.Setenv(teeSocketEnvVar, "")

	key, err := crypto.HexToECDSA(testSignerKeyHex)
	if err != nil {
		t.Fatalf("parsing the test key: %v", err)
	}
	s := &TeeService{ProviderSigner: key}
	if s.remote != nil {
		t.Fatal("a TeeService with no socket has a remote signer")
	}

	hash := crypto.Keccak256([]byte("unchanged"))
	got, err := s.SignHash(hash)
	if err != nil {
		t.Fatalf("SignHash: %v", err)
	}
	want, err := crypto.Sign(hash, key)
	if err != nil {
		t.Fatalf("crypto.Sign: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("SignHash %x, crypto.Sign %x — the local path must be unchanged", got, want)
	}
}

// With no key and no controller there is nothing to sign with, and that has to be an error
// rather than a panic: the nil check moved out of Sign/SignEIP712 into the seam, so this is
// the one place still enforcing it.
func TestSigningWithNeitherKeyNorControllerFails(t *testing.T) {
	s := &TeeService{}
	for _, tc := range []struct {
		name string
		call func() ([]byte, error)
	}{
		{"SignHash", func() ([]byte, error) { return s.SignHash(make([]byte, 32)) }},
		{"Sign", func() ([]byte, error) { return s.Sign(make([]byte, 32)) }},
		{"SignEIP712", func() ([]byte, error) { return s.SignEIP712(make([]byte, 32)) }},
	} {
		if _, err := tc.call(); err == nil {
			t.Errorf("%s with no signer returned no error", tc.name)
		}
	}
}

// The address must come from whoever holds the key. A malformed answer has to be refused
// rather than turned into the zero address, which would otherwise be published in report_data
// and on-chain as this provider's signer.
func TestSignerAddressRejectsAMalformedAnswer(t *testing.T) {
	key, err := crypto.HexToECDSA(testSignerKeyHex)
	if err != nil {
		t.Fatalf("parsing the test key: %v", err)
	}

	good := signingController(t)
	addr, err := good.SignerAddress(context.Background())
	if err != nil {
		t.Fatalf("SignerAddress: %v", err)
	}
	if addr != crypto.PubkeyToAddress(key.PublicKey) {
		t.Errorf("address %s, want %s", addr, crypto.PubkeyToAddress(key.PublicKey))
	}

	for _, answer := range []string{`{"address":""}`, `{"address":"not-an-address"}`, `{"address":"0x1234"}`, `{}`} {
		r := fakeController(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(answer))
		})
		if _, err := r.SignerAddress(context.Background()); err == nil {
			t.Errorf("SignerAddress accepted %s", answer)
		}
	}
}

// A signature of the wrong length must be refused at the boundary. Left through, it would be
// cached as a response proof and fail much later at whichever client tried to use it, with
// nothing left pointing at the controller.
func TestSignHashRejectsAMalformedSignature(t *testing.T) {
	for _, answer := range []string{`{"signature":""}`, `{"signature":"abcd"}`, `{"signature":"zz"}`, `{}`} {
		r := fakeController(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(answer))
		})
		if _, err := r.SignHash(context.Background(), make([]byte, 32)); err == nil {
			t.Errorf("SignHash accepted %s", answer)
		}
	}

	// A hash that is not 32 bytes never reaches the controller at all.
	r := signingController(t)
	if _, err := r.SignHash(context.Background(), make([]byte, 31)); err == nil {
		t.Error("SignHash accepted a 31-byte hash")
	}
}

// The enc key material must reach deriveEncKey as the same bytes on both paths. The local path
// passes dstack's answer through as []byte(material) — the hex STRING, not decoded bytes — so
// hex-decoding it on the remote path would seed HKDF differently and produce a key that
// unseals nothing a client sealed.
func TestEncKeyMaterialIsPassedThroughUnmodified(t *testing.T) {
	r := signingController(t)
	material, err := r.EncKeyMaterial(context.Background())
	if err != nil {
		t.Fatalf("EncKeyMaterial: %v", err)
	}
	if material != "deadbeef" {
		t.Fatalf("material %q, want the controller's answer verbatim", material)
	}

	// The same material must give the same keypair whichever side fetched it.
	wantPriv, wantPub, err := deriveEncKey([]byte("deadbeef"))
	if err != nil {
		t.Fatalf("deriveEncKey: %v", err)
	}
	gotPriv, gotPub, err := deriveEncKey([]byte(material))
	if err != nil {
		t.Fatalf("deriveEncKey: %v", err)
	}
	if !bytes.Equal(gotPriv, wantPriv) || !bytes.Equal(gotPub, wantPub) {
		t.Error("the remote material derived a different enc keypair than the local material")
	}

	// An empty answer is a refusal, not an empty key.
	empty := fakeController(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"key":""}`))
	})
	if _, err := empty.EncKeyMaterial(context.Background()); err == nil {
		t.Error("EncKeyMaterial accepted an empty key")
	}
}

// A controller that refuses — because it cannot establish which image is running — must
// surface as an error. Signing with anything else would produce a signature no attestation
// names, which is worse than none because it would still verify against something.
func TestARefusingControllerIsAnError(t *testing.T) {
	r := fakeController(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"the broker runs \"repo:latest\", which resolves to no digest"}`))
	})

	if _, err := r.SignHash(context.Background(), make([]byte, 32)); err == nil {
		t.Error("SignHash succeeded against a refusing controller")
	} else if !bytes.Contains([]byte(err.Error()), []byte("resolves to no digest")) {
		t.Errorf("error %q drops the controller's reason", err)
	}
	if _, err := r.SignerAddress(context.Background()); err == nil {
		t.Error("SignerAddress succeeded against a refusing controller")
	}
	if _, err := r.EncKeyMaterial(context.Background()); err == nil {
		t.Error("EncKeyMaterial succeeded against a refusing controller")
	}
}
