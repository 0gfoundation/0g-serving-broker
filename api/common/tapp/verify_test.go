package tapp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// Fixtures are REAL nonce-bound evidence fetched on 2026-10-21 from the two
// tapp-servers the broker deals with, plus Intel PCS's early-track tcbInfo
// for their FMSPC captured the same minute. Each negative case below breaks
// one root and expects exactly that check to fail.

const (
	fxNow          = "2026-10-21T06:00:00Z"
	fxTappscanSig  = "0x967a68f2f1b8d9ba69a585d90d08acd15ce12a35"
	fxTappscanKey  = "0x12f60bc0d2da3f3ab52152d293b35ec84b0e3bfc231fcf4f06024d38c789b0d8"
	fxTappscanNon  = "250e014a60291ea263833a985004c9a4"
	fxTappscanUki  = "9aaf061e68eaa6b35ba44cf7362b0b6987bad29c2aa76291debcf28aecc0beb08748171c7994976704461d18b15a018b" // uki/v0.10.0/prod.json
	fxVerifierSig  = "0x99ba1bdc0db57968c0371a9e5a3b19a52c90c17c"
	fxVerifierKey  = "0x9856d5734684ea96c66e5e4f90ecf1a043999d2cfd074197bb9da34e61a1073c"
	fxVerifierNon  = "1786d44b5dd1fbc83b6b940c67d39977"
	fxVerifierUki  = "1bdeb06df0fc15a1fa10c8cf61598a56531e40e81f94ab5a6a8855a5a6626d9f3898567b71b41b1ac2f4c66109fbb94f" // uki/v0.10.0/dev.json
	fxFMSPC        = "00806f050000"
	fxAdvisoryDoc  = "INTEL-DOC-10003"
	fxAdvisorySA1  = "INTEL-SA-01439"
	fxAdvisorySA2  = "INTEL-SA-01442"
	fxTcbEvalEarly = 22
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func pcsFixture(t *testing.T) func(context.Context, string) ([]byte, error) {
	body := fixture(t, "tcbinfo_00806f050000_early.json")
	return func(_ context.Context, url string) ([]byte, error) {
		if !strings.Contains(url, "fmspc="+fxFMSPC) || !strings.Contains(url, "update=early") {
			t.Fatalf("unexpected PCS url %s", url)
		}
		return body, nil
	}
}

func goodPolicy(t *testing.T, signer, nonce, uki string) Policy {
	n, _ := hex.DecodeString(nonce)
	now, _ := time.Parse(time.RFC3339, fxNow)
	return Policy{
		Signer: signer, Nonce: n, ExpectedUki: []string{uki},
		RequireTcb:      []string{"UpToDate", "OutOfDate"},
		AllowAdvisories: []string{fxAdvisoryDoc, fxAdvisorySA1, fxAdvisorySA2},
		Now:             now, Getter: pcsFixture(t),
	}
}

func TestVerifyLiveVectors(t *testing.T) {
	cases := []struct {
		file, signer, nonce, uki, key string
	}{
		{"evidence_0g-tappscan.json", fxTappscanSig, fxTappscanNon, fxTappscanUki, fxTappscanKey},
		{"evidence_assay-verifier.json", fxVerifierSig, fxVerifierNon, fxVerifierUki, fxVerifierKey},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			res := Verify(context.Background(), fixture(t, c.file), goodPolicy(t, c.signer, c.nonce, c.uki))
			if !res.OK {
				t.Fatalf("expected PASS, got %+v", res)
			}
			if res.TLSPublicKey != c.key {
				t.Errorf("tls key %s, want %s", res.TLSPublicKey, c.key)
			}
			if res.TcbStatus != "OutOfDate" || len(res.Advisories) != 3 || res.TcbEvalNum != fxTcbEvalEarly {
				t.Errorf("tcb %s %v eval %d", res.TcbStatus, res.Advisories, res.TcbEvalNum)
			}
			if res.FMSPC != fxFMSPC || res.Uki != c.uki {
				t.Errorf("fmspc %s uki %s", res.FMSPC, res.Uki)
			}
		})
	}
}

// mutate re-encodes one base64 field of the evidence JSON after f changed it.
func mutate(t *testing.T, raw []byte, field string, f func([]byte) []byte) []byte {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	var s string
	if err := json.Unmarshal(m[field], &s); err != nil {
		t.Fatal(err)
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	enc, _ := json.Marshal(base64.StdEncoding.EncodeToString(f(b)))
	m[field] = enc
	out, _ := json.Marshal(m)
	return out
}

func TestVerifyEachRootFailsAlone(t *testing.T) {
	raw := fixture(t, "evidence_0g-tappscan.json")
	flip := func(b []byte, at int) []byte {
		c := append([]byte(nil), b...)
		c[at] ^= 0x01
		return c
	}
	cases := []struct {
		name   string
		raw    func() []byte
		pol    func(Policy) Policy
		failed string // the check that must be false
		detail string
	}{
		{"wrong nonce", func() []byte { return raw },
			func(p Policy) Policy { p.Nonce = []byte{1, 2, 3}; return p }, "nonce", "evidence echoes"},
		{"wrong signer", func() []byte { return raw },
			func(p Policy) Policy { p.Signer = fxVerifierSig; return p }, "signer", "vs chain"},
		{"wrong uki", func() []byte { return raw },
			func(p Policy) Policy { p.ExpectedUki = []string{fxVerifierUki}; return p }, "uki", "measured"},
		{"advisory not allowed", func() []byte { return raw },
			func(p Policy) Policy { p.AllowAdvisories = []string{fxAdvisoryDoc}; return p }, "clean", "unlisted advisories"},
		{"tcb status not allowed", func() []byte { return raw },
			func(p Policy) Policy { p.RequireTcb = []string{"UpToDate"}; return p }, "tcb", "not in"},
		{"tcbInfo expired", func() []byte { return raw },
			func(p Policy) Policy { p.Now = p.Now.AddDate(0, 2, 0); return p }, "tcb", "expired"},
		{"tampered runtime_data", func() []byte {
			return mutate(t, raw, "runtime_data", func(b []byte) []byte { return flip(b, len(b)-3) })
		}, nil, "binding", "sha512"},
		{"tampered event log digest", func() []byte {
			// flip one byte of the UKI digest itself: the log no longer replays
			// to the quote's RTMR1 (and the uki no longer matches either)
			uki, _ := hex.DecodeString(fxTappscanUki)
			return mutate(t, raw, "cc_eventlog", func(b []byte) []byte { return flip(b, bytes.Index(b, uki)+5) })
		}, nil, "replay", "RTMR"},
		{"relabelled event data", func() []byte {
			// renaming the boot application in the (unmeasured) event data leaves
			// every digest intact, so replay still passes — but no event is
			// recognised as the UKI any more, and the uki check fails on its own
			return mutate(t, raw, "cc_eventlog", func(b []byte) []byte {
				out := bytes.Replace(b, utf16("BOOTX64.EFI"), utf16("XOOTX64.EFI"), -1)
				if bytes.Equal(out, b) {
					t.Fatal("fixture has no BOOTX64.EFI event")
				}
				return out
			})
		}, nil, "uki", "measured"},
		{"wrong mrtd", func() []byte { return raw },
			func(p Policy) Policy { p.ExpectedMrTd = []string{strings.Repeat("00", 48)}; return p }, "mrtd", "mr_td"},
		{"tampered quote signature", func() []byte {
			return mutate(t, raw, "quote", func(b []byte) []byte { return flip(b, 48+584+8) })
		}, nil, "quote", ""},
		{"no runtime_data", func() []byte {
			var m map[string]json.RawMessage
			_ = json.Unmarshal(raw, &m)
			delete(m, "runtime_data")
			out, _ := json.Marshal(m)
			return out
		}, nil, "evidence", "predates"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pol := goodPolicy(t, fxTappscanSig, fxTappscanNon, fxTappscanUki)
			if c.pol != nil {
				pol = c.pol(pol)
			}
			res := Verify(context.Background(), c.raw(), pol)
			if res.OK {
				t.Fatalf("expected %s to fail, got PASS: %+v", c.failed, res)
			}
			checks, _ := json.Marshal(res.Checks)
			var m map[string]bool
			_ = json.Unmarshal(checks, &m)
			if m[c.failed] {
				t.Errorf("check %s still true; checks=%s detail=%s", c.failed, checks, res.Detail)
			}
			if c.detail != "" && !strings.Contains(res.Detail, c.detail) {
				t.Errorf("detail %q lacks %q", res.Detail, c.detail)
			}
		})
	}
}

func TestReplayMapsLogIndexToRTMR(t *testing.T) {
	// Index 1,2,4 of the CC log reproduce RTMR0,1,3 of the quote (RTMR2 is
	// untouched by these images); index 0 is MRTD and is ignored.
	raw := fixture(t, "evidence_assay-verifier.json")
	ev, err := ParseEvidence(raw)
	if err != nil {
		t.Fatal(err)
	}
	events, err := ParseCCEventLog(ev.EventLog)
	if err != nil {
		t.Fatal(err)
	}
	mr := ReplayRTMRs(events)
	body := ev.Quote[48 : 48+584]
	for i := 0; i < 4; i++ {
		want := body[328+48*i : 328+48*i+48]
		if hex.EncodeToString(want) != hex.EncodeToString(mr[i][:]) {
			t.Errorf("RTMR%d: quote %x… replay %x…", i, want[:8], mr[i][:8])
		}
	}
	if got := BootComponents(events)["uki"]; len(got) != 1 || got[0] != fxVerifierUki {
		t.Errorf("uki components %v", got)
	}
}

func TestParseEvidenceResponse(t *testing.T) {
	// success=1, message="", evidence="{}" hand-encoded
	ok := []byte{0x08, 0x01, 0x1a, 0x02, '{', '}'}
	if ev, err := parseEvidenceResponse(ok); err != nil || string(ev) != "{}" {
		t.Fatalf("got %q %v", ev, err)
	}
	refused := []byte{0x08, 0x00, 0x12, 0x03, 'n', 'o', 'p'}
	if _, err := parseEvidenceResponse(refused); err == nil || !strings.Contains(err.Error(), "nop") {
		t.Fatalf("refusal not surfaced: %v", err)
	}
}

// utf16 is how EFI device paths spell file names inside event data.
func utf16(s string) []byte {
	out := make([]byte, 0, 2*len(s))
	for _, c := range s {
		out = append(out, byte(c), 0)
	}
	return out
}
