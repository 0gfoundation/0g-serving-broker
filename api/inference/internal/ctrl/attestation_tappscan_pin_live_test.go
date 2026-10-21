package ctrl

import (
	"context"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/0glabs/0g-serving-broker/inference/config"
)

// Live bootstrap against the real TappRegistry, tappscan's tapp-server and
// Intel PCS. Opt in with SPML_LIVE=1; the fixtures in common/tapp cover the
// verification logic offline, this covers the network path end to end.
func TestLiveTappscanBootstrap(t *testing.T) {
	if os.Getenv("SPML_LIVE") == "" {
		t.Skip("set SPML_LIVE=1 to run against the live testnet")
	}
	var cfg config.AssayAttestation
	cfg.Registry = "0x2Ce80374318B1d7Fb3345724457a182E0ad165c9"
	cfg.RpcURL = "https://evmrpc-testnet.0g.ai"
	cfg.RequireTcb = []string{"UpToDate", "OutOfDate"}
	cfg.AllowAdvisories = []string{"INTEL-DOC-10003", "INTEL-SA-01439", "INTEL-SA-01442"}
	cfg.Tappscan.PinSource = "chain"
	cfg.Tappscan.Expected.Uki = []string{"9aaf061e68eaa6b35ba44cf7362b0b6987bad29c2aa76291debcf28aecc0beb08748171c7994976704461d18b15a018b"}
	s := &scanSource{cfg: cfg, pins: &tappscanPins{}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	st := s.bootstrapTappscanPin(ctx)
	t.Logf("signer=%s teeUrl=%s ok=%v pin=%x detail=%s", st.signer, st.teeURL, st.ok, st.pin, st.detail)
	if st.result != nil {
		t.Logf("checks=%+v tcb=%s advisories=%v fmspc=%s mrtd=%s", st.result.Checks, st.result.TcbStatus, st.result.Advisories, st.result.FMSPC, st.result.MrTd)
	}
	if !st.ok {
		t.Fatalf("bootstrap failed: %s", st.detail)
	}
	if hex.EncodeToString(st.pin) != "12f60bc0d2da3f3ab52152d293b35ec84b0e3bfc231fcf4f06024d38c789b0d8" {
		t.Errorf("pin %x differs from tappscan's current key", st.pin)
	}
}
