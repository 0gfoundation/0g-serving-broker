package attestproxy

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
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

// upstreamSetHashPattern is what an UpstreamSetHash may be: hex sha256, the shape
// attest.UpstreamSetHash returns. Declared here rather than imported so this package
// keeps validating its own inputs — it is the last thing between a string and a
// derivation path.
var upstreamSetHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// KeyIdentity is what a key is derived FOR — everything a change to which must produce a
// different key.
//
// It exists because that used to be one string and is now two, and the two must come from
// ONE snapshot. Reading the digest and the set hash through separate calls would let a
// change land between them, and the key derived from the mismatched pair would be one no
// record names — a signature that verifies against nothing, which is the failure mode the
// digest binding was introduced to prevent in the first place.
type KeyIdentity struct {
	// Digest is the image the broker runs, "sha256:<64hex>".
	Digest string

	// UpstreamSetHash is attest.RunningState.UpstreamSetHash for the set this deployment
	// records, or EMPTY when it records none.
	//
	// Empty is not a placeholder for "unknown" — it is the answer for a deployment that
	// does not record its upstream set at all, which is every deployment with
	// controller.recordUpstreamSet off. That is why the empty case reproduces the old path
	// byte for byte (see SignerKeyPath): a deployment that has not opted in must keep the
	// key it already has, or merging this would rotate every signer address in the fleet
	// and reset every on-chain acknowledgement for nothing.
	//
	// A deployment that DOES record a set but cannot state it — an unreadable record —
	// must not reach here with an empty hash. That would silently derive the unbound key
	// for a deployment whose bound is unknown, which is the fail-open direction. The
	// controller answers by refusing rather than by passing "".
	UpstreamSetHash string
}

// CurrentKeyIdentityFunc reports the identity keys are currently derived for.
//
// One function rather than two for the reason KeyIdentity is one struct: the pair has to
// be consistent. An error means the identity could not be established, and every
// operation here then refuses.
type CurrentKeyIdentityFunc func(ctx context.Context) (KeyIdentity, error)

// signerKeyPath is the dstack derivation path for the response-signing key.
//
// Per image, which is the whole point: an attestation names the address of this key, so
// changing the image changes the address and a client still holding the old attestation
// stops being able to verify. That is what stops an attestation taken before an upgrade
// from authorising an unbounded future.
//
// A sibling of encKeyPath rather than its parent.
//
// Not because derivation is hierarchical — it is not. dstack runs HKDF over the FULL path
// string, so "/<digest>" and "/<digest>/e2ee-enc" are independent and holding one gives nothing
// about the other. The suffix is there so neither key is the other's prefix as a matter of
// naming, which keeps a later path added under "/<digest>" from looking like it inherits
// something. Worth stating, because the same flatness is why the legacy signer at "/" does not
// compromise any per-image key — and because someone reading the older claim would build on it.
func signerKeyPath(id KeyIdentity) string { return SignerKeyPath(id) }

// SignerKeyPath is signerKeyPath, exported because the RTMR3 recorder derives the same address
// before it writes a record naming that image. One string, two callers: if they disagreed, the
// address in the ledger would not be the one signing responses and every verification would
// fail — the wrong direction, but for the wrong reason.
//
// # The set hash is a path segment, not a suffix
//
//	/<digest>/sign                 no upstream set recorded
//	/<digest>/<set hash>/sign      a set recorded
//
// An empty UpstreamSetHash produces the FIRST form, byte for byte identical to what this
// returned before the set existed. That is the property that lets this merge without
// rotating a single key in the fleet: a deployment that does not record its set derives
// exactly what it derived yesterday, and its on-chain signer acknowledgement stands.
//
// A recorded set produces the second, so the key is a function of where plaintext may go.
// Changing the permitted set changes the signing address, which resets
// Service.teeSignerAcknowledged on chain and requires the contract owner to acknowledge
// again — that is the point, not a side effect. A deployment cannot widen where it
// forwards plaintext without a visible on-chain event.
//
// The two forms cannot collide. A digest is "sha256:" and 64 hex characters, and a set
// hash is 64 hex characters, so no digest can be mistaken for the middle segment of the
// other form — and UpstreamSetHash is domain-separated and version-tagged anyway.
func SignerKeyPath(id KeyIdentity) string {
	return "/" + id.Digest + id.setHashSegment() + signerDerivePathSuffix
}

// setHashSegment is the middle of the path: "/<hash>" when there is one, nothing when
// there is not.
//
// One place, so the signer and enc paths cannot disagree about the shape. They must not:
// the two are derived independently, but a deployment whose signer is bound to the set
// while its enc key is not would let a request sealed under one bound be opened under
// another.
func (id KeyIdentity) setHashSegment() string {
	if id.UpstreamSetHash == "" {
		return ""
	}
	return "/" + id.UpstreamSetHash
}

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
func encKeyPath(id KeyIdentity) string { return EncKeyPath(id) }

// EncKeyPath is encKeyPath, exported for the same reason SignerKeyPath is: the RTMR3 recorder
// derives this key's public half before writing a record that binds it. One string, three
// callers — this proxy, the recorder, and (through tee.EncKeyDerivePathSuffix) the broker.
//
// # Bound to the set as well, and why that is not scope creep
//
// The set hash enters here too, on the same segment SignerKeyPath uses. Binding only the
// signer would leave a request sealed to the enc key openable after the set widened — a
// client seals BEFORE any response signature exists, so the enc key is the half that
// decides where the plaintext it is protecting may end up. A signer bound to the set and
// an enc key that is not would say two different things about one deployment.
//
// The cost is real and worth naming: a set change invalidates the enc key, so a request
// sealed just before it cannot be opened after it and fails. The window is one config
// change, clients re-fetch the quote to get the current key, and the alternative is a
// request sealed under one bound being processed under another.
//
// Note the third caller. The broker derives this itself when it has dstack's socket
// mounted (tee.getEncKey's local branch, on the bare suffix with no digest at all), and
// nothing the controller does reaches that path. So this binding — like the digest
// binding before it — exists only for deployments running the attestation proxy, and the
// controller has to refuse to record a set it cannot bind. Measured on the mainnet fleet:
// 2 of 13, and both are the self-hosted in-CVM engine deployments this is aimed at.
func EncKeyPath(id KeyIdentity) string {
	return "/" + id.Digest + id.setHashSegment() + tee.EncKeyDerivePathSuffix
}

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
	id, err := p.currentKeyIdentity(r.Context())
	if err != nil {
		p.fail(w, http.StatusServiceUnavailable, "%v", err)
		return
	}
	material, err := p.deriveKey(r.Context(), encKeyPath(id))
	if err != nil {
		p.fail(w, http.StatusBadGateway, "deriving the enc key: %v", err)
		return
	}
	p.respond(w, map[string]string{"key": material})
}

// signerKey derives the current image's signing key. Never returned to a caller.
func (p *Proxy) signerKey(ctx context.Context) (*ecdsa.PrivateKey, error) {
	id, err := p.currentKeyIdentity(ctx)
	if err != nil {
		return nil, err
	}
	material, err := p.deriveKey(ctx, signerKeyPath(id))
	if err != nil {
		return nil, fmt.Errorf("deriving the signing key: %w", err)
	}
	// dstack returns hex; the broker's local path parses it the same way, so the two agree
	// on the key for a given path.
	return SignerKeyFromMaterial(material)
}

// currentKeyIdentity resolves what keys are derived for, refusing anything it cannot pin
// down.
//
// Both halves are checked against a shape, and both refusals are the same judgement the
// digest check has always made: a key derived from a value this could not verify is worse
// than no key, because the signature under it would verify against a record naming
// something else.
//
// The set hash is checked as a hex sha256 rather than trusted, even though the controller
// computes it from attest.UpstreamSetHash. It arrives here as a bare string across a
// package boundary, and a malformed one would silently become a path segment — deriving a
// key for a set nobody can name, which no record could ever match.
func (p *Proxy) currentKeyIdentity(ctx context.Context) (KeyIdentity, error) {
	if p.currentIdentityFn == nil {
		return KeyIdentity{}, fmt.Errorf("no source for the identity keys are derived for")
	}
	id, err := p.currentIdentityFn(ctx)
	if err != nil {
		return KeyIdentity{}, fmt.Errorf("establishing what keys are derived for: %w", err)
	}
	if !imageDigestPattern.MatchString(id.Digest) {
		return KeyIdentity{}, fmt.Errorf("running image %q is not a digest", id.Digest)
	}
	// Empty is the deployment that records no set, which is legitimate — see KeyIdentity.
	if id.UpstreamSetHash != "" && !upstreamSetHashPattern.MatchString(id.UpstreamSetHash) {
		return KeyIdentity{}, fmt.Errorf("upstream set hash %q is not a hex sha256", id.UpstreamSetHash)
	}
	return id, nil
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
