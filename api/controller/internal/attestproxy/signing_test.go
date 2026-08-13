package attestproxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
)

// otherDigest is a second image, so a test can show the derived key follows the image.
const otherDigest = "sha256:" + "2222222222222222222222222222222222222222222222222222222222222222"

// keyServingDstack answers /GetKey with a key that is a deterministic function of the
// requested derivation path, which is what the real service does — the whole reason a
// per-image path yields a per-image key. It records the paths it was asked for.
func keyServingDstack(t *testing.T, dir string, askedFor *[]string) string {
	t.Helper()

	path := filepath.Join(dir, "dstack.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listening on the fake dstack socket: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/GetKey" {
			_, _ = w.Write([]byte(`{"served":"` + r.URL.Path + `"}`))
			return
		}
		var req struct {
			Path string `json:"path"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		*askedFor = append(*askedFor, req.Path)
		_ = json.NewEncoder(w).Encode(map[string]string{"key": keyForPath(req.Path)})
	})}
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() { _ = srv.Close() })

	return path
}

// keyForPath is the fake derivation: hex of sha256(path), which is both a valid secp256k1
// scalar in practice and distinct per path.
func keyForPath(path string) string {
	sum := sha256.Sum256([]byte("fake-app-key/" + path))
	return hex.EncodeToString(sum[:])
}

// startWithDigest runs a proxy over a key-serving dstack, with the running image under the
// test's control so it can be changed mid-test.
func startWithDigest(t *testing.T, digest *string, askedFor *[]string) *http.Client {
	t.Helper()

	dir := t.TempDir()
	dstackPath := keyServingDstack(t, dir, askedFor)
	listenPath := filepath.Join(dir, "tee.sock")

	p := New(listenPath, dstackPath, func(context.Context) (string, error) { return *digest, nil }, testLogger(t))
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- p.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-served:
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return after the context was cancelled")
		}
		_ = p.Close()
	})

	deadline := time.Now().Add(5 * time.Second)
	for {
		if conn, err := net.Dial("unix", listenPath); err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the proxy socket never appeared")
		}
		time.Sleep(5 * time.Millisecond)
	}

	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", listenPath)
		},
	}}
}

func postJSON(t *testing.T, c *http.Client, path, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://controller"+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

func field(t *testing.T, body, name string) string {
	t.Helper()
	var m map[string]string
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("decoding %q: %v", body, err)
	}
	return m[name]
}

// The property the whole slice exists for: change the image, and the signer address changes.
//
// An attestation names this address, so a client holding a pre-upgrade attestation stops being
// able to verify post-upgrade responses. Without this, a signing key survives an in-place
// upgrade and an attestation taken before it keeps verifying forever.
func TestTheSignerAddressFollowsTheRunningImage(t *testing.T) {
	digest := testDigest
	var askedFor []string
	c := startWithDigest(t, &digest, &askedFor)

	code, body := postJSON(t, c, pathSignerAddress, `{}`)
	if code != http.StatusOK {
		t.Fatalf("POST %s = %d: %s", pathSignerAddress, code, body)
	}
	first := field(t, body, "address")

	digest = otherDigest
	code, body = postJSON(t, c, pathSignerAddress, `{}`)
	if code != http.StatusOK {
		t.Fatalf("POST %s after the upgrade = %d: %s", pathSignerAddress, code, body)
	}
	second := field(t, body, "address")

	if first == second {
		t.Errorf("both images derived signer %s — the address must change with the image", first)
	}
	if first == "" || second == "" {
		t.Fatalf("empty addresses: %q, %q", first, second)
	}

	// And the derivation path is the running digest's, not a fixed one.
	for _, want := range []string{testDigest, otherDigest} {
		found := false
		for _, path := range askedFor {
			if strings.Contains(path, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("no derivation asked for %s; paths were %v", want, askedFor)
		}
	}
}

// /Sign must produce exactly the signature local signing would for the same key and hash.
//
// This is the check that actually executes the derivation and crypto.Sign, rather than
// asserting a status code. If the two ever differ, every existing verifier breaks for
// whichever deployment moved, and none of them can tell which path signed.
func TestSignMatchesWhatLocalSigningWouldProduce(t *testing.T) {
	digest := testDigest
	var askedFor []string
	c := startWithDigest(t, &digest, &askedFor)

	hash := crypto.Keccak256([]byte("a response worth attesting to"))
	code, body := postJSON(t, c, pathSign, `{"hash":"`+hex.EncodeToString(hash)+`"}`)
	if code != http.StatusOK {
		t.Fatalf("POST %s = %d: %s", pathSign, code, body)
	}
	got, err := hex.DecodeString(field(t, body, "signature"))
	if err != nil {
		t.Fatalf("the signature is not hex: %v", err)
	}

	key, err := crypto.HexToECDSA(keyForPath(signerKeyPath(testDigest)))
	if err != nil {
		t.Fatalf("parsing the expected key: %v", err)
	}
	want, err := crypto.Sign(hash, key)
	if err != nil {
		t.Fatalf("crypto.Sign: %v", err)
	}

	if hex.EncodeToString(got) != hex.EncodeToString(want) {
		t.Errorf("signature %x, want %x", got, want)
	}
	if len(got) != 65 {
		t.Errorf("signature is %d bytes, want 65", len(got))
	}
	// Raw recovery id: the 27/28 fixup belongs to the caller, so remote and local match.
	if got[64] > 1 {
		t.Errorf("recovery id is %d, want the raw 0/1", got[64])
	}

	// The signature verifies under the address /SignerAddress reports, which is what ties a
	// response to the key an attestation names.
	_, addrBody := postJSON(t, c, pathSignerAddress, `{}`)
	pub, err := crypto.SigToPub(hash, got)
	if err != nil {
		t.Fatalf("recovering the public key: %v", err)
	}
	// SignerAddressOf, not .Hex(): one canonical lowercase spelling is what the record and a
	// reader compare, so the test compares the same way rather than case-insensitively.
	if recovered := strings.ToLower(crypto.PubkeyToAddress(*pub).Hex()); recovered != field(t, addrBody, "address") {
		t.Errorf("the signature recovers to %s, but /SignerAddress reports %s", recovered, field(t, addrBody, "address"))
	}
}

// The enc key is derived on the running image's path too, so an upgraded image cannot unseal
// what clients sealed to its predecessor — and it is a sibling of the signing key rather than
// a descendant, so holding one is not holding the other's subtree.
func TestTheEncKeyIsPerImageAndASiblingOfTheSigningKey(t *testing.T) {
	digest := testDigest
	var askedFor []string
	c := startWithDigest(t, &digest, &askedFor)

	code, body := postJSON(t, c, pathGetEncKey, `{}`)
	if code != http.StatusOK {
		t.Fatalf("POST %s = %d: %s", pathGetEncKey, code, body)
	}
	first := field(t, body, "key")

	digest = otherDigest
	_, body = postJSON(t, c, pathGetEncKey, `{}`)
	if second := field(t, body, "key"); second == first {
		t.Error("both images derived the same enc key — it must be per image")
	}

	signPath, encPath := signerKeyPath(testDigest), encKeyPath(testDigest)
	if signPath == encPath {
		t.Fatal("the signing and enc keys share a derivation path")
	}
	if strings.HasPrefix(encPath, signPath+"/") || strings.HasPrefix(signPath, encPath+"/") {
		t.Errorf("%q and %q are ancestor/descendant, want siblings", signPath, encPath)
	}
}

// GetKey must not be forwarded. It is a key-derivation primitive: with it the broker can
// derive any path, including the previous image's, and a key it can derive is a key it can
// keep across an upgrade — which restores exactly the hole per-image derivation closes.
func TestGetKeyIsNotForwarded(t *testing.T) {
	if forwarded["/GetKey"] {
		t.Error("/GetKey is in the forwarded set")
	}

	digest := testDigest
	var askedFor []string
	c := startWithDigest(t, &digest, &askedFor)

	// The broker asking for it directly gets nothing, even though the proxy itself uses
	// GetKey against dstack to answer /Sign.
	if code, _ := postJSON(t, c, "/GetKey", `{"path":"/"}`); code != http.StatusNotFound {
		t.Errorf("POST /GetKey = %d, want 404", code)
	}
	if len(askedFor) != 0 {
		t.Errorf("the request reached dstack's GetKey with paths %v", askedFor)
	}

	// Nor by any spelling that a router might normalize on the way.
	for _, path := range []string{"/GetKey/", "//GetKey", "/getkey", "/../GetKey"} {
		if code, _ := postJSON(t, c, path, `{"path":"/"}`); code != http.StatusNotFound {
			t.Errorf("POST %s = %d, want 404", path, code)
		}
	}
	if len(askedFor) != 0 {
		t.Errorf("a rewritten path reached dstack's GetKey: %v", askedFor)
	}
}

// The address /SignerAddress serves and the address the RTMR3 recorder writes must be the same
// value, because a reader compares them and refuses on a mismatch.
//
// They are computed by two callers in two packages, from the same derivation path. Sharing only
// the path left "parse the material, take the address, pick a spelling" written out twice, so a
// fallback or a case change added to one side would make the recorded address stop being the
// signing address — every verification failing, in the safe direction, and almost undiagnosable
// because both copies look correct on their own. This pins the two exported steps both sides now
// go through.
func TestTheServedAddressIsTheRecordedAddress(t *testing.T) {
	digest := testDigest
	var askedFor []string
	c := startWithDigest(t, &digest, &askedFor)

	// What the proxy serves to the broker.
	code, body := postJSON(t, c, pathSignerAddress, `{}`)
	if code != http.StatusOK {
		t.Fatalf("POST %s = %d: %s", pathSignerAddress, code, body)
	}
	served := field(t, body, "address")

	// What a recorder computes from the same material, through the exported steps.
	key, err := SignerKeyFromMaterial(keyForPath(SignerKeyPath(digest)))
	if err != nil {
		t.Fatalf("SignerKeyFromMaterial: %v", err)
	}
	recorded := SignerAddressOf(key)

	if served != recorded {
		t.Errorf("the proxy serves %s but a recorder would write %s", served, recorded)
	}
	// One canonical spelling, so a reader comparing strings needs no case rules.
	if served != strings.ToLower(served) {
		t.Errorf("address %s is not lowercase; the record and the quote would disagree on spelling", served)
	}
}
