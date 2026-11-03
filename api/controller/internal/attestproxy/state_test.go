package attestproxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"testing"

	"github.com/0glabs/0g-serving-broker/common/tee"
)

const testDoc = "zg-state-v2\nimage x\nconfig y\nupstreams unrecorded\nengines z\nledger unavailable"

func withState(doc string, id KeyIdentity, err error) func(*Proxy) {
	return func(p *Proxy) {
		p.SetStateSource(func(context.Context) ([]byte, KeyIdentity, error) { return []byte(doc), id, err })
	}
}

// v2ReportDataFor is what an honest broker asks for: its enc_pub, then the document's hash.
func v2ReportDataFor(t *testing.T, id KeyIdentity, doc string) []byte {
	t.Helper()
	encPub, err := tee.EncPublicKeyFromMaterial(keyForPath(EncKeyPath(id)))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(doc))
	return append(append([]byte{}, encPub...), sum[:]...)
}

// The broker fetches the document and gets a quote over it — and only over it: another
// document, or the real document beside someone else's enc_pub, is refused.
func TestBrokerSocketQuotesTheCurrentStateDocument(t *testing.T) {
	id := KeyIdentity{Digest: testDigest}
	c, rec := startRecording(t, false, fixedIdentity(id), withState(testDoc, id, nil))

	code, body := send(t, c, http.MethodPost, "/State", "")
	if code != http.StatusOK || body != testDoc {
		t.Fatalf("POST /State = %d %q, want the document", code, body)
	}

	if code, _ := send(t, c, http.MethodPost, "/GetQuote", quoteBody(v2ReportDataFor(t, id, testDoc))); code != http.StatusOK {
		t.Fatalf("v2 over the current document = %d, want 200", code)
	}

	other := v2ReportDataFor(t, id, testDoc+"\ninstance x y")
	attacker := attackerOf(v2ReportDataFor(t, id, testDoc))
	stale := v2ReportDataFor(t, KeyIdentity{Digest: otherDigest}, testDoc)
	for name, rd := range map[string][]byte{"another document": other, "another enc_pub": attacker, "another identity's enc_pub": stale} {
		if code, _ := send(t, c, http.MethodPost, "/GetQuote", quoteBody(rd)); code != http.StatusForbidden {
			t.Errorf("%s: = %d, want 403", name, code)
		}
	}
	if len(rec.got) != 1 {
		t.Errorf("dstack saw %v, want only the honest v2 quote", rec.got)
	}
}

// When the document cannot be built, nothing v2 is quoted and /State says so; the legacy and
// v1 layouts are unaffected.
func TestNoStateDocumentNoV2Quote(t *testing.T) {
	id := KeyIdentity{Digest: testDigest}
	c, rec := startRecording(t, false, fixedIdentity(id), withState("", id, errors.New("set unreadable")))

	if code, _ := send(t, c, http.MethodPost, "/State", ""); code != http.StatusServiceUnavailable {
		t.Errorf("POST /State = %d, want 503", code)
	}
	if code, _ := send(t, c, http.MethodPost, "/GetQuote", quoteBody(v2ReportDataFor(t, id, ""))); code != http.StatusForbidden {
		t.Errorf("v2 = %d, want 403", code)
	}
	_, v1 := reportDataFor(t, id)
	if code, _ := send(t, c, http.MethodPost, "/GetQuote", quoteBody(v1)); code != http.StatusOK {
		t.Errorf("v1 = %d, want 200", code)
	}
	if len(rec.got) != 1 {
		t.Errorf("dstack saw %v", rec.got)
	}
}

// Without a state source there is no /State and no v2 — the proxy as it was.
func TestWithoutAStateSourceThereIsNoState(t *testing.T) {
	id := KeyIdentity{Digest: testDigest}
	c, _ := startRecording(t, false, fixedIdentity(id))
	if code, _ := send(t, c, http.MethodPost, "/State", ""); code != http.StatusNotFound {
		t.Errorf("POST /State = %d, want 404", code)
	}
	if code, _ := send(t, c, http.MethodPost, "/GetQuote", quoteBody(v2ReportDataFor(t, id, testDoc))); code != http.StatusForbidden {
		t.Errorf("v2 = %d, want 403", code)
	}
}

// The ingress socket serves no state and can quote no v2: a v2 report_data's second half is
// a hash, never zero.
func TestIngressSocketHasNoState(t *testing.T) {
	id := KeyIdentity{Digest: testDigest}
	c, _ := startRecording(t, true, nil)
	if code, _ := send(t, c, http.MethodPost, "/State", ""); code != http.StatusNotFound {
		t.Errorf("POST /State on the ingress socket = %d, want 404", code)
	}
	if code, _ := post(t, c, http.MethodGet, "/GetQuote?report_data="+hex.EncodeToString(v2ReportDataFor(t, id, testDoc))); code != http.StatusForbidden {
		t.Errorf("v2 on the ingress socket = %d, want 403", code)
	}
}
