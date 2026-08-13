package attestproxy

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"

	"github.com/0glabs/0g-serving-broker/common/tee"
)

// The operations the controller answers itself, rather than forwarding.
//
// They exist so the broker never needs a key-derivation primitive of its own. Handing it
// one would let it derive any path — including the path belonging to a previous image — and
// a signing key it can derive is a signing key it can keep across an upgrade. These give it
// exactly what it needs and nothing more: a signature under the current image's key, the
// address of that key, and the current image's encryption key (which it must hold, because
// it decrypts requests itself).
const (
	pathSign          = "/Sign"
	pathSignerAddress = "/SignerAddress"
	pathGetEncKey     = "/GetEncKey"

	// signerDerivePathSuffix keeps the signing key a leaf, beside the enclave encryption
	// key, rather than the root of the running image's whole derivation subtree.
	signerDerivePathSuffix = "/sign"
)

// CurrentImageFunc reports the digest of the image the broker is running, as
// "sha256:<64hex>".
//
// Supplied by the controller, which reads it off the broker container. An error means the
// digest could not be established, and every operation here then refuses: a signature under
// a key derived from a guess is worse than no signature, because it would verify.
type CurrentImageFunc func(ctx context.Context) (string, error)

// signerKeyPath is the dstack derivation path for the response-signing key.
//
// Per image, which is the whole point: an attestation names the address of this key, so
// changing the image changes the address and a client still holding the old attestation
// stops being able to verify. That is what stops an attestation taken before an upgrade
// from authorising an unbounded future.
//
// A sibling of encKeyPath rather than its parent: dstack derivation is hierarchical, so
// deriving the signing key at "/<digest>" would make it an ancestor of everything else under
// that digest, and holding it would be holding the subtree.
func signerKeyPath(digest string) string { return SignerKeyPath(digest) }

// SignerKeyPath is signerKeyPath, exported because the RTMR3 recorder derives the same address
// before it writes a record naming that image. One string, two callers: if they disagreed, the
// address in the ledger would not be the one signing responses and every verification would
// fail — the wrong direction, but for the wrong reason.
func SignerKeyPath(digest string) string { return "/" + digest + signerDerivePathSuffix }

// SignerKeyFromMaterial turns what the derivation service returned into the signing key.
//
// Exported alongside SignerKeyPath for the same reason, and it is the more dangerous half. Two
// callers derive this key — this proxy, to sign with it, and the RTMR3 recorder, to write its
// address into the record — and they MUST agree byte for byte. Sharing only the path left three
// steps (parse, derive the address, normalise its spelling) written out twice in two packages, so
// a fallback or a case change added to one would silently make the recorded address stop being
// the signing address. Every verification would then fail, which is the safe direction and an
// almost undiagnosable one: both copies look correct in isolation.
func SignerKeyFromMaterial(material string) (*ecdsa.PrivateKey, error) {
	key, err := crypto.HexToECDSA(strings.TrimPrefix(material, "0x"))
	if err != nil {
		return nil, fmt.Errorf("parsing the derived signing key: %w", err)
	}
	return key, nil
}

// SignerAddressOf is the one spelling of a signer address these two sides exchange.
//
// Lowercase, not EIP-55: the record carries it as text and a reader compares strings, so one
// canonical form removes a class of mismatch rather than relying on every comparison to be
// case-insensitive.
func SignerAddressOf(key *ecdsa.PrivateKey) string {
	return strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())
}

// encKeyPath is the derivation path for the enclave encryption key, per image for the same
// reason. tee.EncKeyDerivePathSuffix keeps the two sides agreeing on one string.
func encKeyPath(digest string) string { return "/" + digest + tee.EncKeyDerivePathSuffix }

// handleLocal serves the operations above, or reports that the path is not one of them.
func (p *Proxy) handleLocal(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case pathSign:
		p.serveSign(w, r)
	case pathSignerAddress:
		p.serveSignerAddress(w, r)
	case pathGetEncKey:
		p.serveEncKey(w, r)
	default:
		return false
	}
	return true
}

// serveSign signs a 32-byte hash with the current image's key.
//
// A hash, not a message: the caller decides what it is signing over and how it is framed,
// and this stays a signing oracle for one key rather than a second opinion about formats.
// The signature is returned raw, all 65 bytes, so the caller's existing recovery-id
// handling produces byte-identical output to signing locally.
func (p *Proxy) serveSign(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Hash string `json:"hash"`
	}
	// Bounded: the other end of this socket is the component the whole arrangement declines
	// to trust, and a hash is 32 bytes.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req); err != nil {
		p.fail(w, http.StatusBadRequest, "decoding the request: %v", err)
		return
	}
	hash, err := hex.DecodeString(strings.TrimPrefix(req.Hash, "0x"))
	if err != nil {
		p.fail(w, http.StatusBadRequest, "hash is not hex: %v", err)
		return
	}
	if len(hash) != 32 {
		p.fail(w, http.StatusBadRequest, "hash is %d bytes, want 32", len(hash))
		return
	}

	key, err := p.signerKey(r.Context())
	if err != nil {
		p.fail(w, http.StatusServiceUnavailable, "%v", err)
		return
	}
	sig, err := crypto.Sign(hash, key)
	if err != nil {
		p.fail(w, http.StatusInternalServerError, "signing: %v", err)
		return
	}

	p.respond(w, map[string]string{"signature": hex.EncodeToString(sig)})
}

// serveSignerAddress reports the address of the current image's signing key, which is what
// an attestation's report_data names and a client verifies against.
func (p *Proxy) serveSignerAddress(w http.ResponseWriter, r *http.Request) {
	key, err := p.signerKey(r.Context())
	if err != nil {
		p.fail(w, http.StatusServiceUnavailable, "%v", err)
		return
	}
	p.respond(w, map[string]string{"address": SignerAddressOf(key)})
}

// serveEncKey returns the current image's encryption key material.
//
// The one thing here that does hand over key material, because the broker decrypts requests
// itself and no proxy can do that for it. Per image all the same, so an upgraded image
// cannot read what was sealed to its predecessor.
func (p *Proxy) serveEncKey(w http.ResponseWriter, r *http.Request) {
	digest, err := p.currentImage(r.Context())
	if err != nil {
		p.fail(w, http.StatusServiceUnavailable, "%v", err)
		return
	}
	material, err := p.deriveKey(r.Context(), encKeyPath(digest))
	if err != nil {
		p.fail(w, http.StatusBadGateway, "deriving the enc key: %v", err)
		return
	}
	p.respond(w, map[string]string{"key": material})
}

// signerKey derives the current image's signing key. Never returned to a caller.
func (p *Proxy) signerKey(ctx context.Context) (*ecdsa.PrivateKey, error) {
	digest, err := p.currentImage(ctx)
	if err != nil {
		return nil, err
	}
	material, err := p.deriveKey(ctx, signerKeyPath(digest))
	if err != nil {
		return nil, fmt.Errorf("deriving the signing key: %w", err)
	}
	// dstack returns hex; the broker's local path parses it the same way, so the two agree
	// on the key for a given path.
	return SignerKeyFromMaterial(material)
}

// currentImage resolves the running broker image, refusing anything it cannot pin down.
func (p *Proxy) currentImage(ctx context.Context) (string, error) {
	if p.currentImageFn == nil {
		return "", fmt.Errorf("no source for the running image digest")
	}
	digest, err := p.currentImageFn(ctx)
	if err != nil {
		return "", fmt.Errorf("establishing the running image: %w", err)
	}
	if !imageDigestPattern.MatchString(digest) {
		return "", fmt.Errorf("running image %q is not a digest", digest)
	}
	return digest, nil
}

func (p *Proxy) respond(w http.ResponseWriter, body map[string]string) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		p.logger.Errorf("[attestproxy] writing the response: %v", err)
	}
}

func (p *Proxy) fail(w http.ResponseWriter, code int, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	p.logger.Errorf("[attestproxy] %s", msg)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// deriveKey asks dstack for the key at path. The proxy holds the dstack socket; the broker
// does not, which is the arrangement this whole package exists to make possible.
func (p *Proxy) deriveKey(ctx context.Context, path string) (string, error) {
	body, err := json.Marshal(map[string]string{"path": path, "purpose": ""})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://dstack/GetKey", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.keyClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("dstack answered %d", resp.StatusCode)
	}

	var out struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Key == "" {
		return "", fmt.Errorf("dstack returned no key for %s", path)
	}
	return out.Key, nil
}
