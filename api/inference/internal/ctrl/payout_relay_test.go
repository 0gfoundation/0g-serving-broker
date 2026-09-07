package ctrl

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/0glabs/0g-serving-broker/common/tee"
	providercontract "github.com/0glabs/0g-serving-broker/inference/internal/contract"
)

const (
	testProvider = "0xf92082dC0268623849e35c9B62BD05047d27191e"
	// Throwaway keys, used only to produce signatures in this file.
	testNodeKey = "4c0883a69102937d6231471b5dbb6204fe5129617082792ae468d01a3f362318"
	testTeeKey  = "8a1f9a8f1e2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5"
)

func addr(s string) common.Address { return common.HexToAddress(s) }

// signFetch produces exactly what claim.py's fetch_from_broker sends.
func signFetch(t *testing.T, keyHex, provider string, ts int64) string {
	t.Helper()
	key, err := crypto.HexToECDSA(keyHex)
	if err != nil {
		t.Fatalf("bad test key: %v", err)
	}
	sig, err := crypto.Sign(accounts.TextHash([]byte(voucherFetchPayload(provider, ts))), key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	sig[64] += 27 // EIP-191 signers emit {27,28}
	return "0x" + hex.EncodeToString(sig)
}

func relayTestCtrl(assayURL string) *Ctrl {
	c := &Ctrl{
		logger:             &testAsyncLoggerImpl{},
		contract:           &providercontract.ProviderContract{ProviderAddress: testProvider},
		httpClient:         &http.Client{Timeout: 5 * time.Second},
		assayVerifierURL:   assayURL,
		assayPayoutEnabled: true,
	}
	key, err := crypto.HexToECDSA(testTeeKey)
	if err != nil {
		panic(err)
	}
	c.teeService = &tee.TeeService{ProviderSigner: key}
	return c
}

// teeSignerAddress is who the assay must recover from a signed fetch.
func teeSignerAddress(t *testing.T) common.Address {
	t.Helper()
	key, err := crypto.HexToECDSA(testTeeKey)
	if err != nil {
		t.Fatalf("bad tee key: %v", err)
	}
	return crypto.PubkeyToAddress(key.PublicKey)
}

// checkFetchAuth mirrors the assay's _verify_body_sig for the bodyless GET:
// personal_sign over keccak256("assay-vouchers-v1|<ts>"), recovered against
// the on-chain teeSigner. Returns the recovered address.
func checkFetchAuth(t *testing.T, r *http.Request) common.Address {
	t.Helper()
	tsHdr := r.Header.Get("ZG-Body-Ts")
	sigHdr := r.Header.Get("ZG-Body-Sig")
	if tsHdr == "" || sigHdr == "" {
		t.Fatalf("voucher fetch reached the assay unsigned (ts=%q sig=%q)", tsHdr, sigHdr)
	}
	ts, err := strconv.ParseInt(tsHdr, 10, 64)
	if err != nil {
		t.Fatalf("bad ZG-Body-Ts %q: %v", tsHdr, err)
	}
	if skew := time.Since(time.Unix(ts, 0)); skew > time.Minute || skew < -time.Minute {
		t.Fatalf("ZG-Body-Ts is %s off; the assay would reject it", skew)
	}
	sig, err := hexutil.Decode(sigHdr)
	if err != nil || len(sig) != 65 {
		t.Fatalf("bad ZG-Body-Sig %q", sigHdr)
	}
	if sig[64] >= 27 {
		sig[64] -= 27
	}
	payload := []byte("assay-vouchers-v1|" + tsHdr)
	pub, err := crypto.SigToPub(accounts.TextHash(crypto.Keccak256(payload)), sig)
	if err != nil {
		t.Fatalf("cannot recover fetch signature: %v", err)
	}
	return crypto.PubkeyToAddress(*pub)
}

// A node authenticates as itself and nobody else. That is the whole premise of
// the relay: the broker hands each node its own voucher without being told —
// or having to trust — which node is calling.
func TestAuthenticateVoucherFetch(t *testing.T) {
	key, err := crypto.HexToECDSA(testNodeKey)
	if err != nil {
		t.Fatalf("bad test key: %v", err)
	}
	want := crypto.PubkeyToAddress(key.PublicKey)
	c := relayTestCtrl("https://assay.invalid")
	now := time.Now().Unix()

	got, err := c.AuthenticateVoucherFetch(strconv.FormatInt(now, 10),
		signFetch(t, testNodeKey, testProvider, now))
	if err != nil {
		t.Fatalf("valid fetch rejected: %v", err)
	}
	if got != want {
		t.Fatalf("recovered %s, want %s", got.Hex(), want.Hex())
	}

	// Checksum casing must not matter: the payload lowercases the provider, so
	// a node that stored the address in EIP-55 form still authenticates.
	got, err = c.AuthenticateVoucherFetch(strconv.FormatInt(now, 10),
		signFetch(t, testNodeKey, "0xF92082DC0268623849E35C9B62BD05047D27191E", now))
	if err != nil || got != want {
		t.Fatalf("checksum-cased provider should verify: got %s err %v", got.Hex(), err)
	}

	// A signature made for a DIFFERENT provider's broker must not authenticate
	// this node here. secp256k1 always recovers *some* address, so the check
	// that matters is that it is not this node's — otherwise binding the
	// provider into the payload buys nothing.
	got, err = c.AuthenticateVoucherFetch(strconv.FormatInt(now, 10),
		signFetch(t, testNodeKey, "0x000000000000000000000000000000000000dEaD", now))
	if err == nil && got == want {
		t.Fatal("a fetch signed for another provider authenticated here")
	}

	skewSecs := int64(voucherFetchMaxSkew/time.Second) + 60
	for _, tc := range []struct{ name, ts, sig string }{
		{"no headers", "", ""},
		{"no signature", strconv.FormatInt(now, 10), ""},
		{"no timestamp", "", signFetch(t, testNodeKey, testProvider, now)},
		{"unparseable ts", "not-a-number", signFetch(t, testNodeKey, testProvider, now)},
		{"stale ts", strconv.FormatInt(now-skewSecs, 10),
			signFetch(t, testNodeKey, testProvider, now-skewSecs)},
		{"future ts", strconv.FormatInt(now+skewSecs, 10),
			signFetch(t, testNodeKey, testProvider, now+skewSecs)},
		{"truncated signature", strconv.FormatInt(now, 10), "0xdeadbeef"},
		{"not hex", strconv.FormatInt(now, 10), "zzzz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := c.AuthenticateVoucherFetch(tc.ts, tc.sig); err == nil {
				t.Fatal("accepted; must fail closed")
			}
		})
	}
}

// The relay returns the row whose voucher PAYS the caller — matched on the
// address inside the signed voucher, never on a node id. Getting this wrong
// would mail one node's earnings to another.
func TestRelayAssayVoucher(t *testing.T) {
	const (
		mine   = "0xa3DB4ecaefb8448CFeADaF1Cd76e133c96a439E6"
		theirs = "0x85573D99f73BA49964c658bfa4Bf0E744a683790"
	)
	body := fmt.Sprintf(`{
	  "signer": "0xddddD95287895Bc28DaF5ff90c849b6FBC29Bf3c",
	  "contract": "0xe81bE6Ba5183b18c0f781ba100Ebe8aFb3C92931",
	  "nodes": {
	    "node_0": {"cumulative": 56000, "epoch": 1756700000, "covered_count": 12,
	               "updated_at": 1756700005,
	               "voucher": {"node_id":"node_0","node":%q,"cumulative":"56000",
	                           "epoch":1756700000,"signature":"0xaa"}},
	    "node_1": {"cumulative": 4400, "epoch": 1756700000, "covered_count": 1,
	               "updated_at": 1756700005,
	               "voucher": {"node_id":"node_1","node":%q,"cumulative":"4400",
	                           "epoch":1756700000,"signature":"0xbb"}},
	    "node_2": {"cumulative": 0, "epoch": 0, "covered_count": 0,
	               "updated_at": null, "voucher": null}
	  }}`, theirs, mine)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/payout/vouchers" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// The assay refuses an unsigned fetch (it is an earnings report); a
		// relay that forgot to sign would fail there, far from here.
		if got := checkFetchAuth(t, r); got != teeSignerAddress(t) {
			t.Fatalf("fetch signed by %s, want the teeSigner %s", got.Hex(), teeSignerAddress(t).Hex())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c := relayTestCtrl(srv.URL)

	entry, err := c.RelayAssayVoucher(context.Background(), addr(mine))
	if err != nil {
		t.Fatalf("relay failed: %v", err)
	}
	if entry.NodeID != "node_1" {
		t.Fatalf("matched %q; selection must key on the voucher's payee address", entry.NodeID)
	}
	var v struct {
		Node       string `json:"node"`
		Cumulative string `json:"cumulative"`
	}
	if err := json.Unmarshal(entry.Voucher, &v); err != nil {
		t.Fatalf("relayed voucher is not JSON: %v", err)
	}
	if v.Node != mine || v.Cumulative != "4400" {
		t.Fatalf("relayed the wrong voucher: %+v", v)
	}
	if entry.Signer == "" || entry.Contract == "" {
		t.Fatal("signer/contract must be relayed too — the node checks them")
	}

	// A node with no voucher yet is the ordinary case, not a failure.
	if _, err := c.RelayAssayVoucher(context.Background(),
		addr("0x000000000000000000000000000000000000dEaD")); !errors.Is(err, ErrNoVoucher) {
		t.Fatalf("want ErrNoVoucher for an unknown payee, got %v", err)
	}

	// node_2's row exists but holds a null voucher; it must not crash the scan.
	if _, err := c.RelayAssayVoucher(context.Background(), addr(theirs)); err != nil {
		t.Fatalf("node_0 has a voucher and should be relayable: %v", err)
	}
}

func TestRelayAssayVoucherDisabled(t *testing.T) {
	c := relayTestCtrl("")
	if c.AssayPayoutEnabled() {
		t.Fatal("no verifier URL must read as disabled")
	}
	if _, err := c.RelayAssayVoucher(context.Background(), addr(testProvider)); err == nil {
		t.Fatal("relay must refuse when payout is not configured")
	}
}

func TestRelayAssayVoucherUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()
	c := relayTestCtrl(srv.URL)
	// An upstream failure reported as "no voucher" would tell a node to stop
	// polling exactly when it should keep trying.
	if _, err := c.RelayAssayVoucher(context.Background(), addr(testProvider)); err == nil || errors.Is(err, ErrNoVoucher) {
		t.Fatalf("upstream 500 must surface as a relay error, got %v", err)
	}
}

// Cross-language interop: this signature was produced by eth_account, the way
// claim.py's fetch_from_broker produces it. The two sides agree on a payload
// string or they agree on nothing, and a mismatch would only show up in
// production as "every node's fetch is 401" — cheap to pin down here instead.
//
// Regenerate with:
//
//	from eth_account import Account
//	from eth_account.messages import encode_defunct
//	Account.sign_message(encode_defunct(text=
//	    "assay-voucher-fetch-v1|<provider lowercase>|<ts>"), key)
func TestAuthenticateVoucherFetchPythonSignature(t *testing.T) {
	const (
		ts       = 1756700000
		fromPy   = "0xa3202088509a46f85de63bed27df99ed67c8ea8aa1459657e0518cafd90edc912a155f17052c7eea4c787c807900806d9e705e860faf7fa4dfdc97f078b7ce9e1b"
		signerPy = "0x2c7536E3605D9C16a7a3D7b1898e529396a65c23"
	)
	c := relayTestCtrl("https://assay.invalid")

	// The fixture's timestamp is fixed, so the skew check would reject it on
	// any day but one. Verify the payload+recovery directly instead.
	pubKey, err := recoverFetchSigner(t, voucherFetchPayload(testProvider, ts), fromPy)
	if err != nil {
		t.Fatalf("cannot recover the Python signature: %v", err)
	}
	if pubKey != addr(signerPy) {
		t.Fatalf("Go and claim.py disagree on the signed payload: recovered %s, want %s",
			pubKey.Hex(), signerPy)
	}

	// And the live path agrees, once the timestamp is current.
	now := time.Now().Unix()
	got, err := c.AuthenticateVoucherFetch(strconv.FormatInt(now, 10),
		signFetch(t, testNodeKey, testProvider, now))
	if err != nil {
		t.Fatalf("live path rejected a fresh fetch: %v", err)
	}
	_ = got
}

func recoverFetchSigner(t *testing.T, payload, sigHex string) (common.Address, error) {
	t.Helper()
	sig, err := hex.DecodeString(sigHex[2:])
	if err != nil {
		return common.Address{}, err
	}
	if sig[64] >= 27 {
		sig[64] -= 27
	}
	pub, err := crypto.SigToPub(accounts.TextHash([]byte(payload)), sig)
	if err != nil {
		return common.Address{}, err
	}
	return crypto.PubkeyToAddress(*pub), nil
}

// No TEE key means no signature, and an unsigned fetch is refused by the assay
// anyway — sending it would trade a clear local error for a remote 401.
func TestRelayAssayVoucherRefusesUnsigned(t *testing.T) {
	reached := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := relayTestCtrl(srv.URL)
	c.teeService = nil
	if _, err := c.RelayAssayVoucher(context.Background(), addr(testProvider)); err == nil {
		t.Fatal("relay sent a fetch it could not sign")
	}
	if reached {
		t.Fatal("an unsignable fetch must not leave the broker")
	}
}
