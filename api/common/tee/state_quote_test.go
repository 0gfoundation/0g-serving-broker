package tee

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

const testStateDoc = "zg-state-v2\nimage x\nconfig y\nupstreams unrecorded\nengines z\nledger unavailable"

// stateControllerSocket is a controller with an attestation proxy: the signing operations,
// GetQuote (answering with the report_data it was asked for) and, unless stateStatus is 0,
// /State answering stateStatus with testStateDoc.
func stateControllerSocket(t *testing.T, stateStatus int) string {
	t.Helper()
	key, err := crypto.HexToECDSA(testSignerKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "tee.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/SignerAddress":
			_ = json.NewEncoder(w).Encode(map[string]string{"address": crypto.PubkeyToAddress(key.PublicKey).Hex()})
		case "/GetEncKey":
			_ = json.NewEncoder(w).Encode(map[string]string{"key": "deadbeef"})
		case "/Sign":
			var req struct {
				Hash string `json:"hash"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			h, _ := hex.DecodeString(req.Hash)
			sig, _ := crypto.Sign(h, key)
			_ = json.NewEncoder(w).Encode(map[string]string{"signature": hex.EncodeToString(sig)})
		case "/GetQuote":
			var req struct {
				ReportData string `json:"report_data"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			_ = json.NewEncoder(w).Encode(map[string]string{"quote": "q", "event_log": "[]", "report_data": req.ReportData})
		case "/Info":
			_ = json.NewEncoder(w).Encode(map[string]string{"tcb_info": "{}"})
		case "/State":
			if stateStatus == 0 {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(stateStatus)
			_, _ = w.Write([]byte(testStateDoc))
		default:
			http.NotFound(w, r)
		}
	})}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return path
}

func syncedService(t *testing.T, stateStatus int) *TeeService {
	t.Helper()
	t.Setenv(teeSocketEnvVar, stateControllerSocket(t, stateStatus))
	s, err := NewTeeService(Phala, testLogger(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SyncQuote(context.Background(), false); err != nil {
		t.Fatalf("SyncQuote() = %v", err)
	}
	return s
}

// The v2 quote is over enc_pub ‖ SHA-256(the document the controller served), carries that
// document, and is signed like the other two.
func TestSyncQuoteBuildsTheStateQuote(t *testing.T) {
	s := syncedService(t, http.StatusOK)

	quote, sig := s.GetStateQuote()
	if quote == "" {
		t.Fatal("no v2 quote")
	}
	var body struct {
		ReportData    []byte `json:"report_data"`
		StateDocument string `json:"state_document"`
	}
	if err := json.Unmarshal([]byte(quote), &body); err != nil {
		t.Fatalf("v2 quote is not JSON: %v", err)
	}
	sum := sha256.Sum256([]byte(testStateDoc))
	want := append(append([]byte{}, s.EncPublicKey...), sum[:]...)
	if hex.EncodeToString(body.ReportData) != hex.EncodeToString(want) {
		t.Errorf("report_data %x, want enc_pub ‖ sha256(document) %x", body.ReportData, want)
	}
	if body.StateDocument != testStateDoc {
		t.Errorf("state_document %q, want the controller's document", body.StateDocument)
	}
	if sig == nil {
		t.Error("v2 quote unsigned")
	}
	if s.Quote == "" || s.QuoteV2 == "" {
		t.Error("the legacy and §4.2 quotes must still be built")
	}
}

// A controller that predates /State, or cannot build the document, leaves the v2 quote empty
// and everything else as it was.
func TestSyncQuoteWithoutAStateDocument(t *testing.T) {
	for name, status := range map[string]int{"no /State": 0, "refused": http.StatusServiceUnavailable} {
		t.Run(name, func(t *testing.T) {
			s := syncedService(t, status)
			if q, _ := s.GetStateQuote(); q != "" {
				t.Errorf("v2 quote %q, want none", q)
			}
			if s.Quote == "" || s.QuoteV2 == "" {
				t.Error("the legacy and §4.2 quotes must still be built")
			}
		})
	}
}
