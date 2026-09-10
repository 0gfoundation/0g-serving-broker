package ctrl

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/0glabs/0g-serving-broker/inference/config"
)

// Live smoke test against testnet + tappscan; runs only with SCAN_LIVE=1.
func TestScanLive(t *testing.T) {
	if os.Getenv("SCAN_LIVE") != "1" {
		t.Skip("SCAN_LIVE=1 to run against the network")
	}
	var cfg config.AssayAttestation
	cfg.Source = "tappscan"
	cfg.AppID = "assay-verifier"
	cfg.Registry = "0x2Ce80374318B1d7Fb3345724457a182E0ad165c9"
	cfg.RpcURL = "https://evmrpc-testnet.0g.ai"
	cfg.RequireTcb = []string{"UpToDate"}
	cfg.OnFail = "block-settlement"
	cfg.Tappscan.URL = "https://35.253.66.70"
	cfg.Tappscan.PubkeyPin = "0x7b13d1320e7ebc93a6edf809d06cf9b44704677461c6feb2c4204e92e5587e9b"
	cfg.Expected.Image = "gcp/uki/v0.7.0/dev.json"
	cfg.Expected.Uki = fxUki
	s, err := newScanSource(cfg, "https://35.225.160.143:8200", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	snap := s.verifyOnce(context.Background())
	body, _, err := s.statement("", false)
	if err != nil {
		t.Fatal(err)
	}
	var st scanStatement
	_ = json.Unmarshal(body, &st)
	st.Scan.Record = "<omitted>"
	out, _ := json.MarshalIndent(st, "", " ")
	t.Logf("snapshot ok=%v pin=%x detail=%s\n%s", snap.ok, snap.pin, snap.detail, out)
	if !snap.ok {
		t.Fatalf("live check failed: %s", snap.detail)
	}
}
