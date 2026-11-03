package attestproxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"

	"github.com/0glabs/0g-serving-broker/common/tee"
)

// serveFakeKey answers /GetKey with keyForPath, the fake derivation signing_test.go uses.
func serveFakeKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &req)
	_ = json.NewEncoder(w).Encode(map[string]string{"key": keyForPath(req.Path)})
}

// reportDataFor computes, from the fake derivation, the two layouts an honest broker with
// identity id asks a quote over.
func reportDataFor(t *testing.T, id KeyIdentity) (legacy, v1 []byte) {
	t.Helper()
	key, err := SignerKeyFromMaterial(keyForPath(SignerKeyPath(id)))
	if err != nil {
		t.Fatalf("signer key: %v", err)
	}
	encPub, err := tee.EncPublicKeyFromMaterial(keyForPath(EncKeyPath(id)))
	if err != nil {
		t.Fatalf("enc key: %v", err)
	}
	addr := crypto.PubkeyToAddress(key.PublicKey)
	v1, err = tee.ReportDataV1(encPub, addr)
	if err != nil {
		t.Fatalf("v1 report_data: %v", err)
	}
	return tee.LegacyReportData(addr), v1
}

// brokerReportData is reportDataFor the identity start() runs with.
func brokerReportData(t *testing.T) (legacy, v1 []byte) {
	t.Helper()
	return reportDataFor(t, KeyIdentity{Digest: testDigest})
}

// ingressReportData is what dstack-ingress asks for: sha256(sha256sum.txt) and 32 zeros.
func ingressReportData() []byte {
	sum := sha256.Sum256([]byte("sha256sum.txt contents"))
	return append(sum[:], make([]byte, 32)...)
}

func quoteBody(rd []byte) string {
	b, _ := json.Marshal(map[string]string{"report_data": hex.EncodeToString(rd)})
	return string(b)
}

// quoteRecorder is a dstack that answers /GetKey and records every GetQuote exactly as it
// arrived — method, query and body — so a test can see what was quoted.
type quoteRecorder struct{ got []string }

func startRecording(t *testing.T, readOnly bool, identity CurrentKeyIdentityFunc) (*http.Client, *quoteRecorder) {
	t.Helper()
	dir := t.TempDir()
	rec := &quoteRecorder{}

	dstackPath := filepath.Join(dir, "dstack.sock")
	l, err := net.Listen("unix", dstackPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/GetKey" {
			serveFakeKey(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		rec.got = append(rec.got, r.Method+" "+r.URL.RequestURI()+" "+string(body))
		_, _ = w.Write([]byte(`{}`))
	})}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })

	listenPath := filepath.Join(dir, "tee.sock")
	var p *Proxy
	if readOnly {
		p = NewReadOnly(listenPath, dstackPath, testLogger(t))
	} else {
		p = New(listenPath, dstackPath, identity, testLogger(t))
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return")
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
		time.Sleep(10 * time.Millisecond)
	}
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", listenPath)
	}}}, rec
}

func fixedIdentity(id KeyIdentity) CurrentKeyIdentityFunc {
	return func(context.Context) (KeyIdentity, error) { return id, nil }
}

// The hole this closes (doc/design/report-data-v2.md §4.1): a caller holding the broker's
// socket asks for a quote over its OWN enc_pub beside the real signer. Before, that was
// forwarded and came back as a genuine quote whose signer matched the chain — and a client
// sealing to the enc_pub in a verified quote sealed the prompt to the attacker. Now only the
// two layouts the broker computes for the current keys are quoted.
func TestBrokerSocketQuotesOnlyTheCurrentKeys(t *testing.T) {
	id := KeyIdentity{Digest: testDigest}
	c, rec := startRecording(t, false, fixedIdentity(id))
	legacy, v1 := reportDataFor(t, id)

	for name, rd := range map[string][]byte{"legacy": legacy, "v1": v1} {
		if code, body := send(t, c, http.MethodPost, "/GetQuote", quoteBody(rd)); code != http.StatusOK {
			t.Errorf("%s: POST /GetQuote = %d %q, want 200", name, code, body)
		}
	}
	if len(rec.got) != 2 {
		t.Fatalf("dstack saw %v, want both honest layouts quoted", rec.got)
	}

	attacker := append([]byte(nil), v1...)
	attacker[0] ^= 0xff // someone else's enc_pub, the real signer, version 1
	otherLegacy, otherV1 := reportDataFor(t, KeyIdentity{Digest: otherDigest})
	refused := map[string][]byte{
		"attacker enc_pub beside the real signer": attacker,
		"another image's legacy layout":           otherLegacy,
		"another image's v1 layout":               otherV1,
		"ingress-shaped":                          ingressReportData(),
		"all zero":                                make([]byte, 64),
	}
	for name, rd := range refused {
		if code, _ := send(t, c, http.MethodPost, "/GetQuote", quoteBody(rd)); code != http.StatusForbidden {
			t.Errorf("%s: POST /GetQuote = %d, want 403", name, code)
		}
		if code, _ := post(t, c, http.MethodGet, "/GetQuote?report_data="+hex.EncodeToString(rd)); code != http.StatusForbidden {
			t.Errorf("%s: GET /GetQuote = %d, want 403", name, code)
		}
	}
	if len(rec.got) != 2 {
		t.Errorf("dstack saw %v after the refused requests, want none of them through", rec.got[2:])
	}
}

// The keys follow the identity, so a report_data that was honest a moment ago is refused
// once the identity moves on — the quote of a previous image is not something a caller can
// keep producing. Built with a set hash too, so the check is the bound path and not only the
// digest-only one.
func TestBrokerSocketFollowsTheIdentity(t *testing.T) {
	setHash := strings.Repeat("ab", 32)
	id := KeyIdentity{Digest: testDigest, UpstreamSetHash: setHash}
	current := id
	c, rec := startRecording(t, false, func(context.Context) (KeyIdentity, error) { return current, nil })

	_, before := reportDataFor(t, id)
	if code, _ := send(t, c, http.MethodPost, "/GetQuote", quoteBody(before)); code != http.StatusOK {
		t.Fatalf("v1 for the current identity = %d, want 200", code)
	}

	current = KeyIdentity{Digest: testDigest} // the set changed (here: unrecorded)
	if code, _ := send(t, c, http.MethodPost, "/GetQuote", quoteBody(before)); code != http.StatusForbidden {
		t.Errorf("v1 for the previous identity = %d, want 403", code)
	}
	_, after := reportDataFor(t, current)
	if code, _ := send(t, c, http.MethodPost, "/GetQuote", quoteBody(after)); code != http.StatusOK {
		t.Errorf("v1 for the new identity = %d, want 200", code)
	}
	if len(rec.got) != 2 {
		t.Errorf("dstack saw %v, want exactly the two honest quotes", rec.got)
	}
}

// Without an identity there are no allowed bytes, and the answer is to refuse — not to fall
// back to forwarding what the caller sent.
func TestBrokerSocketRefusesWithoutAnIdentity(t *testing.T) {
	c, rec := startRecording(t, false, func(context.Context) (KeyIdentity, error) {
		return KeyIdentity{}, errors.New("broker digest unknown")
	})
	_, v1 := brokerReportData(t)
	if code, _ := send(t, c, http.MethodPost, "/GetQuote", quoteBody(v1)); code != http.StatusServiceUnavailable {
		t.Errorf("POST /GetQuote = %d, want 503", code)
	}
	if len(rec.got) != 0 {
		t.Errorf("dstack saw %v, want nothing", rec.got)
	}
}

// The ingress's socket quotes its own evidence and nothing that could be read as one of
// our layouts: no zero second half exists among them, so it can produce none.
func TestIngressSocketQuotesOnlyEvidenceShapedData(t *testing.T) {
	c, rec := startRecording(t, true, nil)
	legacy, v1 := brokerReportData(t)

	evidence := ingressReportData()
	for name, path := range map[string]string{
		"padded":   "/GetQuote?report_data=" + hex.EncodeToString(evidence),
		"unpadded": "/GetQuote?report_data=" + hex.EncodeToString(evidence[:32]),
	} {
		if code, _ := post(t, c, http.MethodGet, path); code != http.StatusOK {
			t.Errorf("%s evidence: GET = %d, want 200", name, code)
		}
	}
	for name, rd := range map[string][]byte{"legacy": legacy, "v1": v1} {
		if code, _ := post(t, c, http.MethodGet, "/GetQuote?report_data="+hex.EncodeToString(rd)); code != http.StatusForbidden {
			t.Errorf("%s on the ingress socket: GET = %d, want 403", name, code)
		}
		if code, _ := send(t, c, http.MethodPost, "/GetQuote", quoteBody(rd)); code != http.StatusForbidden {
			t.Errorf("%s on the ingress socket: POST = %d, want 403", name, code)
		}
	}
	if len(rec.got) != 2 {
		t.Errorf("dstack saw %v, want only the two evidence quotes", rec.got)
	}
}

// What dstack receives is rebuilt from the validated bytes, so what was judged is what is
// quoted. Anything ambiguous — a second field, a prefix, a query on a POST — is refused
// before judging rather than guessed at.
func TestQuoteRequestIsCanonicalOrRefused(t *testing.T) {
	id := KeyIdentity{Digest: testDigest}
	c, rec := startRecording(t, false, fixedIdentity(id))
	legacy, v1 := reportDataFor(t, id)
	upper := strings.ToUpper(hex.EncodeToString(v1))

	if code, _ := send(t, c, http.MethodPost, "/GetQuote", `{"report_data":"`+upper+`"}`); code != http.StatusOK {
		t.Fatalf("uppercase hex = %d, want 200", code)
	}
	want := "POST /GetQuote " + quoteBody(v1)
	if len(rec.got) != 1 || rec.got[0] != want {
		t.Errorf("dstack saw %q, want %q", rec.got, want)
	}

	malformed := map[string][2]string{
		"second field":    {"/GetQuote", `{"report_data":"` + hex.EncodeToString(v1) + `","vm_config":"x"}`},
		"0x prefix":       {"/GetQuote", `{"report_data":"0x` + hex.EncodeToString(v1) + `"}`},
		"not hex":         {"/GetQuote", `{"report_data":"zz"}`},
		"empty":           {"/GetQuote", `{"report_data":""}`},
		"over 64 bytes":   {"/GetQuote", `{"report_data":"` + hex.EncodeToString(append(v1, 0)) + `"}`},
		"not an object":   {"/GetQuote", `"` + hex.EncodeToString(v1) + `"`},
		"query on a POST": {"/GetQuote?report_data=" + hex.EncodeToString(legacy), quoteBody(v1)},
	}
	for name, req := range malformed {
		if code, _ := send(t, c, http.MethodPost, req[0], req[1]); code != http.StatusBadRequest {
			t.Errorf("%s: = %d, want 400", name, code)
		}
	}
	for name, q := range map[string]string{
		"extra parameter":     "report_data=" + hex.EncodeToString(legacy) + "&x=1",
		"repeated parameter":  "report_data=" + hex.EncodeToString(legacy) + "&report_data=" + hex.EncodeToString(v1),
		"missing report_data": "x=1",
	} {
		if code, _ := post(t, c, http.MethodGet, "/GetQuote?"+q); code != http.StatusBadRequest {
			t.Errorf("GET %s: = %d, want 400", name, code)
		}
	}
	if len(rec.got) != 1 {
		t.Errorf("dstack saw %v after the malformed requests, want none of them through", rec.got[1:])
	}
}
