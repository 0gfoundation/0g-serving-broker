package tee

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// remoteSigner signs through the controller's attestation proxy instead of holding a
// signing key.
//
// This is the half of the arrangement that makes a per-image signing key mean anything. The
// key is derived from the digest of the image the broker is running, so an upgrade changes
// the address an attestation names and a client holding the old attestation stops being able
// to verify. A broker that could read the key would defeat that in one step: exfiltrate it
// before the upgrade and keep signing with it afterwards, and the old attestation would go on
// verifying forever. So the key stays on the controller's side of a socket, and this type
// only ever sends a hash and receives 65 bytes.
type remoteSigner struct {
	socket string
	client *http.Client
}

const (
	remoteSignerPathSign    = "/Sign"
	remoteSignerPathAddress = "/SignerAddress"
	remoteSignerPathEncKey  = "/GetEncKey"

	// remoteSignerTimeout bounds one call. The peer is a local unix socket doing one key
	// derivation, so this is generous; it exists so a wedged controller surfaces as a failed
	// signature rather than a stuck request handler.
	remoteSignerTimeout = 10 * time.Second

	// signatureLen is the raw secp256k1 signature length: r ‖ s ‖ v.
	signatureLen = 65
)

func newRemoteSigner(socket string) *remoteSigner {
	return &remoteSigner{
		socket: socket,
		// One client, built once. A per-call http.Client leaves its Transport's idle
		// connection — and that Transport's read and write loops — reachable forever, and
		// SignHash runs once per response.
		client: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
		}},
	}
}

// SignerAddress reports the address of the key the controller signs with.
//
// Read rather than computed, because the broker never sees the key. This is the address that
// goes into the quote's report_data and on-chain, so it must come from whoever actually holds
// the key — deriving it locally would only reproduce a guess.
func (r *remoteSigner) SignerAddress(ctx context.Context) (common.Address, error) {
	var out struct {
		Address string `json:"address"`
	}
	if err := r.post(ctx, remoteSignerPathAddress, struct{}{}, &out); err != nil {
		return common.Address{}, err
	}
	if !common.IsHexAddress(out.Address) {
		return common.Address{}, fmt.Errorf("the controller returned %q, which is not an address", out.Address)
	}
	return common.HexToAddress(out.Address), nil
}

// SignHash returns the raw 65-byte signature over a 32-byte hash.
//
// Raw, with no recovery-id fixup: every caller already does that itself, and doing it here
// too would make the remote path's output differ from the local path's for the same key.
func (r *remoteSigner) SignHash(ctx context.Context, hash []byte) ([]byte, error) {
	if len(hash) != 32 {
		return nil, fmt.Errorf("hash is %d bytes, want 32", len(hash))
	}
	var out struct {
		Signature string `json:"signature"`
	}
	req := struct {
		Hash string `json:"hash"`
	}{Hash: hex.EncodeToString(hash)}
	if err := r.post(ctx, remoteSignerPathSign, req, &out); err != nil {
		return nil, err
	}
	sig, err := hex.DecodeString(strings.TrimPrefix(out.Signature, "0x"))
	if err != nil {
		return nil, fmt.Errorf("the controller returned a signature that is not hex: %w", err)
	}
	// Length-checked here rather than left to the verifier: a truncated signature would be
	// cached as a response proof and only fail much later, at whichever client tried to use
	// it, with nothing left pointing back at the controller.
	if len(sig) != signatureLen {
		return nil, fmt.Errorf("the controller returned %d signature bytes, want %d", len(sig), signatureLen)
	}
	return sig, nil
}

// EncKeyMaterial returns the enclave encryption key material for the running image.
//
// The one thing the controller does hand over, because the broker decrypts requests itself
// and no proxy can do that for it. It is per-image all the same, so an upgraded image cannot
// read what was sealed to its predecessor.
//
// Returned as the string the derivation service produced, not as decoded bytes: getEncKey
// feeds dstack's answer to deriveEncKey as []byte(material), so decoding it here would seed
// HKDF with different input and produce a key nothing sealed to.
func (r *remoteSigner) EncKeyMaterial(ctx context.Context) (string, error) {
	var out struct {
		Key string `json:"key"`
	}
	if err := r.post(ctx, remoteSignerPathEncKey, struct{}{}, &out); err != nil {
		return "", err
	}
	if out.Key == "" {
		return "", fmt.Errorf("the controller returned no enc key material")
	}
	return out.Key, nil
}

func (r *remoteSigner) post(ctx context.Context, path string, in, out interface{}) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://controller"+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("calling the controller's %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// The controller's error text says why it could not establish the running image,
		// which is the whole diagnosis for a refusal, so it is worth surfacing. It never
		// contains key material: the only value the proxy ever writes is a signature, an
		// address, or the enc key, and none of those travel on an error.
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&failure)
		return fmt.Errorf("the controller answered %d for %s: %s", resp.StatusCode, path, failure.Error)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
