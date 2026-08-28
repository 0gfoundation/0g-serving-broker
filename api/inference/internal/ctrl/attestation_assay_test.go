package ctrl

import (
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

// The fixtures under testdata/ are REAL tapp-cli v0.7.0 outputs captured on
// 2026-08-27 against the live assay-verifier app (testplan P2-1..P2-6). The
// two failure fixtures both exited 0 and printed "ALL PASS ✅" — that is the
// trap this parser exists to not fall into.

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return string(b)
}

func TestParseVerifyAppGood(t *testing.T) { // P2-1
	pin, err := parseVerifyAppOutput(fixture(t, "good.txt"), []string{"UpToDate"})
	if err != nil {
		t.Fatalf("good output rejected: %v", err)
	}
	want, _ := hex.DecodeString("b914d74752fcd1b9bfa85f63b769ae9610f75e26230b766539814ed5b84a7aea")
	if string(pin) != string(want) {
		t.Fatalf("pin = %x, want %x", pin, want)
	}
}

func TestParseVerifyAppBadPolicy(t *testing.T) { // P2-2
	out := fixture(t, "bad_policy.txt")
	if !strings.Contains(out, "ALL PASS") {
		t.Fatal("fixture lost its deceptive ALL PASS line — recapture it")
	}
	if _, err := parseVerifyAppOutput(out, []string{"UpToDate"}); err == nil {
		t.Fatal("wrong --policy-ids output accepted — the exit-code trap struck")
	}
}

func TestParseVerifyAppBadAsPubkey(t *testing.T) { // P2-3
	if _, err := parseVerifyAppOutput(fixture(t, "bad_aspubkey.txt"), []string{"UpToDate"}); err == nil {
		t.Fatal("unauthenticated-AS output accepted")
	}
}

func TestParseVerifyAppTcbNotAllowed(t *testing.T) { // P2-5
	out := strings.Replace(fixture(t, "good.txt"), "tcb_status=UpToDate", "tcb_status=OutOfDate", 1)
	if _, err := parseVerifyAppOutput(out, []string{"UpToDate"}); err == nil {
		t.Fatal("tcb_status=OutOfDate accepted with allow-set [UpToDate]")
	}
	if _, err := parseVerifyAppOutput(out, []string{"UpToDate", "OutOfDate"}); err != nil {
		t.Fatalf("tcb_status=OutOfDate rejected despite being allowed: %v", err)
	}
}

func TestParseVerifyAppEmptyTlsKey(t *testing.T) { // P2-6
	var kept []string
	for _, line := range strings.Split(fixture(t, "good.txt"), "\n") {
		if strings.Contains(line, "tls key") {
			continue
		}
		kept = append(kept, line)
	}
	if _, err := parseVerifyAppOutput(strings.Join(kept, "\n"), []string{"UpToDate"}); err == nil {
		t.Fatal("output without a tls key line accepted — an empty pin must be refused")
	}
}

func TestParseVerifyAppMultiNode(t *testing.T) {
	out := strings.Replace(fixture(t, "good.txt"), "(1 node(s))", "(2 node(s))", 1)
	if _, err := parseVerifyAppOutput(out, []string{"UpToDate"}); err == nil {
		t.Fatal("multi-node output accepted — one bad quote could hide behind a good one")
	}
}
