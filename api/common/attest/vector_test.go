package attest

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The vectors under testdata/vectors are real GetQuote responses from CVMs this project
// deployed, with the values a reader must recover from them. They exist for two readers: this
// package, and an independent implementation of doc/rtmr3-records.md that must not import this
// package — both run the same files, and a disagreement between them is a bug in one of the
// two or in the document.
//
// Each directory holds quote_response.json (quote hex, the GetQuote event_log as served, and
// tcb_info — the NVIDIA payload stripped) and expected.json.
type vectorResponse struct {
	Quote    string          `json:"quote"`
	EventLog json.RawMessage `json:"event_log"`
	TcbInfo  json.RawMessage `json:"tcb_info"`
}

type vectorExpected struct {
	QuoteRTMR3         string     `json:"quote_rtmr3"`
	ComposeHash        string     `json:"compose_hash"`
	ReportDataEncPub   string     `json:"report_data_enc_pub"`
	ReportDataSigner   string     `json:"report_data_signer"`
	RuntimeEventsAfter []string   `json:"runtime_events_after_system_ready"`
	BrokerDigest       string     `json:"broker_digest"`
	BrokerDigestSource string     `json:"broker_digest_source"`
	BrokerSigner       string     `json:"broker_signer"`
	BrokerEncPub       string     `json:"broker_enc_pub"`
	Upstreams          [][]string `json:"upstreams"`
}

func loadVectors(t *testing.T) map[string]struct {
	resp vectorResponse
	want vectorExpected
} {
	t.Helper()
	dirs, err := filepath.Glob(filepath.Join("testdata", "vectors", "*"))
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no vectors under testdata/vectors: %v", err)
	}
	out := map[string]struct {
		resp vectorResponse
		want vectorExpected
	}{}
	for _, dir := range dirs {
		var v struct {
			resp vectorResponse
			want vectorExpected
		}
		raw, err := os.ReadFile(filepath.Join(dir, "quote_response.json"))
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		if err := json.Unmarshal(raw, &v.resp); err != nil {
			t.Fatalf("%s: quote_response.json: %v", dir, err)
		}
		raw, err = os.ReadFile(filepath.Join(dir, "expected.json"))
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		if err := json.Unmarshal(raw, &v.want); err != nil {
			t.Fatalf("%s: expected.json: %v", dir, err)
		}
		out[filepath.Base(dir)] = v
	}
	return out
}

// The replay is the anchor: the served event_log — in dstack's GetQuote form, RTMR3 entries
// without digests — must fold into exactly the RTMR3 the signed quote carries, and so must the
// full copy tcb_info serves. A reader that trusted the entries' own digest fields would fail
// the first and pass the second, which is the mistake this test exists to catch.
func TestVectorEventLogReplaysIntoQuoteRTMR3(t *testing.T) {
	for name, v := range loadVectors(t) {
		t.Run(name, func(t *testing.T) {
			quote, err := hex.DecodeString(strings.TrimPrefix(v.resp.Quote, "0x"))
			if err != nil {
				t.Fatal(err)
			}
			fromQuote, err := RTMR3FromQuote(quote)
			if err != nil {
				t.Fatal(err)
			}
			if got := hex.EncodeToString(fromQuote[:]); got != v.want.QuoteRTMR3 {
				t.Fatalf("RTMR3 in quote = %s, expected.json says %s", got, v.want.QuoteRTMR3)
			}

			var tcb struct {
				EventLog json.RawMessage `json:"event_log"`
			}
			if err := json.Unmarshal(v.resp.TcbInfo, &tcb); err != nil {
				t.Fatal(err)
			}
			for logName, log := range map[string]json.RawMessage{"GetQuote event_log": v.resp.EventLog, "tcb_info.event_log": tcb.EventLog} {
				replayed, err := ReplayRTMR3(log)
				if err != nil {
					t.Fatalf("%s: %v", logName, err)
				}
				if replayed != fromQuote {
					t.Errorf("%s replays to %x, quote carries %x", logName, replayed, fromQuote)
				}
			}
		})
	}
}

// With the log anchored, the reader recovers what expected.json records: the keys in
// report_data, the records after system-ready, the broker's digest and keys, the upstream set.
func TestVectorResolvesToExpectedState(t *testing.T) {
	for name, v := range loadVectors(t) {
		t.Run(name, func(t *testing.T) {
			quote, err := hex.DecodeString(strings.TrimPrefix(v.resp.Quote, "0x"))
			if err != nil {
				t.Fatal(err)
			}
			const offsetReportData = QuoteRTMR3Offset + RTMRLen
			verified := VerifiedQuote{
				ComposeHash:  v.want.ComposeHash,
				ReportData:   quote[offsetReportData : offsetReportData+reportDataLen],
				EventLogJSON: v.resp.EventLog,
			}
			if got, _ := EncPubFromReportData(verified.ReportData); got != v.want.ReportDataEncPub {
				t.Errorf("enc_pub from report_data = %s, want %s", got, v.want.ReportDataEncPub)
			}
			if got, _ := SignerFromReportData(verified.ReportData); got != v.want.ReportDataSigner {
				t.Errorf("signer from report_data = %s, want %s", got, v.want.ReportDataSigner)
			}

			state, err := ResolveRunningState(verified, v.resp.TcbInfo, "0g-serving-provider-broker")
			if err != nil {
				t.Fatalf("ResolveRunningState() = %v", err)
			}
			if state.BrokerDigest != v.want.BrokerDigest || state.DigestSource != v.want.BrokerDigestSource {
				t.Errorf("broker digest = %s (%s), want %s (%s)", state.BrokerDigest, state.DigestSource, v.want.BrokerDigest, v.want.BrokerDigestSource)
			}
			if state.BrokerSigner != v.want.BrokerSigner || state.BrokerEncPub != v.want.BrokerEncPub {
				t.Errorf("broker keys = %s / %s, want %s / %s", state.BrokerSigner, state.BrokerEncPub, v.want.BrokerSigner, v.want.BrokerEncPub)
			}
			if state.UpstreamsState != UpstreamsKnown || len(state.Upstreams) != len(v.want.Upstreams) {
				t.Fatalf("upstreams = %s %v, want known %v", state.UpstreamsState, state.Upstreams, v.want.Upstreams)
			}
			for i, u := range state.Upstreams {
				if u.Name != v.want.Upstreams[i][0] || u.URL != v.want.Upstreams[i][1] {
					t.Errorf("upstream %d = %s %s, want %v", i, u.Name, u.URL, v.want.Upstreams[i])
				}
			}
			// state.Events carries every runtime entry, dstack's boot facts included; the
			// records this project wrote are the zg- ones after system-ready.
			var names []string
			for _, e := range state.Events {
				if strings.HasPrefix(e.Event, EventNamespace) {
					names = append(names, e.Event)
				}
			}
			if strings.Join(names, ",") != strings.Join(v.want.RuntimeEventsAfter, ",") {
				t.Errorf("zg- events = %v, want %v", names, v.want.RuntimeEventsAfter)
			}
		})
	}
}
