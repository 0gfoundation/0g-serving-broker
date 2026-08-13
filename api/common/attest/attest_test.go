package attest

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type report struct {
	Quote    string `json:"quote"`
	EventLog string `json:"event_log"`
	TcbInfo  string `json:"tcb_info"`
}

// tcbInfo is the subset of the golden report's tcb_info these tests use.
//
// compose_hash and app_compose are the pair this package still anchors itself — a
// dstack-verifier response reports the compose hash out of the signed report body, and nothing
// has tied app_compose to the quote until it hashes to that. The rest of the measurements are
// the verifier's business now and appear here only to build a realistic VerifiedQuote.
type tcbInfo struct {
	Rtmr3       string     `json:"rtmr3"`
	ComposeHash string     `json:"compose_hash"`
	AppCompose  string     `json:"app_compose"`
	EventLog    []TdxEvent `json:"event_log"`
}

// goldenReport loads the real production GetQuote response in testdata.
//
// It is a real one on purpose. What this package does with an attestation is small now, but the
// two things it still reads — dstack's event log format and the 64 bytes of report_data — are
// both formats it does not own, so a synthetic fixture would only prove this code agrees with
// itself.
func goldenReport(t *testing.T) (report, tcbInfo) {
	t.Helper()

	raw, err := os.ReadFile("testdata/broker_attestation_report.json")
	if err != nil {
		t.Fatalf("reading the golden report: %v", err)
	}
	var r report
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("parsing the golden report: %v", err)
	}
	var tcb tcbInfo
	if err := json.Unmarshal([]byte(r.TcbInfo), &tcb); err != nil {
		t.Fatalf("parsing the golden report's tcb_info: %v", err)
	}
	return r, tcb
}

// goldenVerified builds the VerifiedQuote a caller would construct from a dstack-verifier
// response for the golden report: the compose hash out of app_info, report_data verbatim, and
// the event log the verifier reported event_log_verified for.
//
// report_data is taken from the quote at the offset the TDX v4 format fixes for it, which is
// the one piece of quote parsing left in these tests — and only in the tests, because in
// production the verifier hands those bytes back and the caller passes them straight in.
func goldenVerified(t *testing.T, r report, tcb tcbInfo) VerifiedQuote {
	t.Helper()

	quote, err := hex.DecodeString(strings.TrimPrefix(r.Quote, "0x"))
	if err != nil {
		t.Fatalf("decoding the golden quote: %v", err)
	}
	const offsetReportData = 568 // report_data sits at 520 in the TD report body
	if len(quote) < offsetReportData+reportDataLen {
		t.Fatalf("golden quote is %d bytes, too short to hold report_data", len(quote))
	}
	return VerifiedQuote{
		ComposeHash:  tcb.ComposeHash,
		ReportData:   quote[offsetReportData : offsetReportData+reportDataLen],
		EventLogJSON: []byte(r.EventLog),
	}
}

// The signer this package reads out of report_data is the one a real production broker
// published, in the pre-§4.2 layout it still serves by default.
//
// This is the check that the layout handling is right against something nobody here wrote: the
// bytes come from hardware, and the older layout is an ASCII address the hardware zero-padded.
func TestSignerFromRealReportData(t *testing.T) {
	r, tcb := goldenReport(t)
	v := goldenVerified(t, r, tcb)

	signer, err := SignerFromReportData(v.ReportData)
	if err != nil {
		t.Fatalf("SignerFromReportData() = %v", err)
	}
	if !addressPattern.MatchString(signer) {
		t.Errorf("signer = %q, want a lowercase 0x-prefixed address", signer)
	}

	// The same bytes read as the §4.2 layout must answer "" rather than inventing a key: this
	// report predates that layout, so its version field is zero.
	encPub, err := EncPubFromReportData(v.ReportData)
	if err != nil {
		t.Fatalf("EncPubFromReportData() = %v", err)
	}
	if encPub != "" {
		t.Errorf("EncPubFromReportData() = %q on a legacy report_data, want empty", encPub)
	}
}

// The event log a real CVM serves parses, and the entries this package keeps are exactly the
// ones an application wrote to RTMR3 — dstack's boot facts, in this report, since it has never
// been changed through the controller.
func TestRuntimeEventsAgainstARealLog(t *testing.T) {
	r, _ := goldenReport(t)

	events, err := RuntimeEvents([]byte(r.EventLog))
	if err != nil {
		t.Fatalf("RuntimeEvents() = %v", err)
	}
	if len(events) == 0 {
		t.Fatal("no runtime events in a real log")
	}

	// system-ready is the boundary everything downstream depends on. A real log has it.
	var found bool
	for _, e := range events {
		if e.Event == eventSystemReady {
			found = true
		}
	}
	if !found {
		t.Errorf("no %s in a real log; the boundary between dstack's events and an application's would be unknowable", eventSystemReady)
	}
}

// The register is part of what makes a record ours, not just the event type.
//
// A caller reaches RuntimeEvents holding a log a verifier vouched for — but event_log_verified
// covers all four registers, so an entry carrying the runtime type on IMR 0-2 is exactly as
// "verified" as one on RTMR3. Only RTMR3 is extendable after boot, so only RTMR3 can hold a
// record a container wrote. Without the register filter, an entry among the firmware's
// measurements would be read as ours.
func TestRuntimeEventsTakeOnlyTheRegisterAContainerCanWrite(t *testing.T) {
	log := `[
	  {"imr":0,"event_type":134217729,"digest":"","event":"zg-image-update","event_payload":"6265666f7265"},
	  {"imr":3,"event_type":134217729,"digest":"","event":"zg-image-update","event_payload":"6d696e65"},
	  {"imr":2,"event_type":134217729,"digest":"","event":"zg-image-update","event_payload":"616674657200"},
	  {"imr":3,"event_type":2147483659,"digest":"","event":"firmware","event_payload":"00"}
	]`

	events, err := RuntimeEvents([]byte(log))
	if err != nil {
		t.Fatalf("RuntimeEvents() = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("RuntimeEvents() returned %d events, want only the RTMR3 one: %+v", len(events), events)
	}
	if got := string(events[0].Payload); got != "mine" {
		t.Errorf("payload = %q, want the RTMR3 entry's", got)
	}
}

// A caller cannot hand this package unverified inputs by accident: every field of VerifiedQuote
// is something a dstack-verifier response supplies, and a zero value is refused rather than
// treated as "nothing to check".
func TestVerifiedQuoteRefusesWhatCannotHaveComeFromAVerifier(t *testing.T) {
	r, tcb := goldenReport(t)
	good := goldenVerified(t, r, tcb)

	for name, v := range map[string]VerifiedQuote{
		"no compose hash":      {ReportData: good.ReportData, EventLogJSON: good.EventLogJSON},
		"compose hash not hex": {ComposeHash: strings.Repeat("z", 64), ReportData: good.ReportData, EventLogJSON: good.EventLogJSON},
		"compose hash short":   {ComposeHash: "abcd", ReportData: good.ReportData, EventLogJSON: good.EventLogJSON},
		"no report_data":       {ComposeHash: good.ComposeHash, EventLogJSON: good.EventLogJSON},
		"short report_data":    {ComposeHash: good.ComposeHash, ReportData: make([]byte, 32), EventLogJSON: good.EventLogJSON},
		"no event log":         {ComposeHash: good.ComposeHash, ReportData: good.ReportData},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ResolveRunningState(v, []byte(r.TcbInfo), "0g-serving-provider-broker"); err == nil {
				t.Error("resolved an input no verify response could have produced")
			}
		})
	}
}

// tcb_info is anchored here and nowhere else. dstack-verifier's request carries quote,
// event_log and vm_config — never app_compose — so the compose file is only trustworthy once it
// hashes to the compose hash the verifier reported out of the signed report body.
func TestAppComposeIsAnchoredToTheVerifiedComposeHash(t *testing.T) {
	r, tcb := goldenReport(t)
	v := goldenVerified(t, r, tcb)

	// The real pair agrees, so resolution gets past the anchor. It stops later, at the image
	// step, because this deployment's compose names a tag — checked in resolve_test.go.
	if _, err := ResolveRunningState(v, []byte(r.TcbInfo), "0g-serving-provider-broker"); err != nil &&
		!strings.Contains(err.Error(), "pins no digest") && !strings.Contains(err.Error(), "no digest") {
		t.Fatalf("ResolveRunningState() = %v, want it past the compose-hash anchor", err)
	}

	// A compose hash that does not match the manifest must refuse, even though the verifier
	// vouched for the hash itself: the mismatch means this tcb_info belongs to another quote.
	other := v
	other.ComposeHash = strings.Repeat("ab", 32)
	_, err := ResolveRunningState(other, []byte(r.TcbInfo), "0g-serving-provider-broker")
	if err == nil {
		t.Fatal("resolved with a compose hash the manifest does not hash to")
	}
	// The reason matters: this report's compose names a tag, so it refuses either way, and an
	// assertion on "did it error" would pass with the anchor removed.
	if !strings.Contains(err.Error(), "app_compose") {
		t.Errorf("refused with %q, want the compose-hash mismatch — a bare error here would also fire with the anchor gone", err)
	}
}

// A malformed event log is refused. The verifier said the log replays, but "replays" is not
// "parses into what this package expects", and a caller can pass the wrong log by mistake.
func TestRuntimeEventsRejectsMalformedLogs(t *testing.T) {
	for name, log := range map[string]string{
		"not json":           `{`,
		"not an array":       `{"imr":3}`,
		"payload not hex":    `[{"imr":3,"event_type":134217729,"event":"x","event_payload":"zz"}]`,
		"odd-length payload": `[{"imr":3,"event_type":134217729,"event":"x","event_payload":"abc"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := RuntimeEvents([]byte(log)); err == nil {
				t.Errorf("RuntimeEvents(%s) = nil, want an error", log)
			}
		})
	}
}
