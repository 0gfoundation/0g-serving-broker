package attest

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// report is a GetQuote response as the broker serves it.
type report struct {
	Quote    string `json:"quote"`
	EventLog string `json:"event_log"`
	TcbInfo  string `json:"tcb_info"`
}

// tcbInfo is the subset of the report's tcb_info this package can be checked
// against. Every field here is a second copy of something readable out of the
// quote's signed bytes, which is exactly what makes it useful as a fixture: the
// offsets below are right only if the two agree.
type tcbInfo struct {
	Mrtd        string     `json:"mrtd"`
	Rtmr0       string     `json:"rtmr0"`
	Rtmr1       string     `json:"rtmr1"`
	Rtmr2       string     `json:"rtmr2"`
	Rtmr3       string     `json:"rtmr3"`
	ComposeHash string     `json:"compose_hash"`
	AppCompose  string     `json:"app_compose"`
	EventLog    []TdxEvent `json:"event_log"`
}

// goldenReport loads the real production quote in testdata.
//
// A real one because every constant in this package is a claim about a byte layout
// no test can derive from the code: a synthetic fixture would only reproduce
// whatever the code already assumes. This report is from the test CVM
// "test-without-llm" and carries no credentials.
func goldenReport(t *testing.T) ([]byte, string, tcbInfo) {
	t.Helper()

	raw, err := os.ReadFile("testdata/broker_attestation_report.json")
	if err != nil {
		t.Fatalf("reading the golden report: %v", err)
	}
	var r report
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("parsing the golden report: %v", err)
	}
	quote, err := hex.DecodeString(strings.TrimPrefix(r.Quote, "0x"))
	if err != nil {
		t.Fatalf("decoding the quote: %v", err)
	}
	var tcb tcbInfo
	if err := json.Unmarshal([]byte(r.TcbInfo), &tcb); err != nil {
		t.Fatalf("parsing tcb_info: %v", err)
	}
	return quote, r.EventLog, tcb
}

// Each offset is checked against the same report's tcb_info, which the CVM
// computed independently. A wrong offset reads a neighbouring field and disagrees.
func TestQuoteOffsetsMatchTcbInfo(t *testing.T) {
	quote, _, tcb := goldenReport(t)

	mrtd, err := MRTD(quote)
	if err != nil {
		t.Fatalf("MRTD() = %v", err)
	}
	if got := hex.EncodeToString(mrtd); got != tcb.Mrtd {
		t.Errorf("MRTD() = %s, want %s", got, tcb.Mrtd)
	}

	for i, want := range []string{tcb.Rtmr0, tcb.Rtmr1, tcb.Rtmr2, tcb.Rtmr3} {
		rtmr, err := RTMR(quote, i)
		if err != nil {
			t.Fatalf("RTMR(%d) = %v", i, err)
		}
		if got := hex.EncodeToString(rtmr); got != want {
			t.Errorf("RTMR(%d) = %s, want %s", i, got, want)
		}
	}
}

// The compose hash is what a reader compares against the deployment it expects, so
// it has to come out of the signed report body and not out of the event log — an
// application can write a compose-hash event under any value it likes.
func TestComposeHashComesFromTheSignedReportBody(t *testing.T) {
	quote, _, tcb := goldenReport(t)

	mrConfigID, err := MRConfigID(quote)
	if err != nil {
		t.Fatalf("MRConfigID() = %v", err)
	}
	composeHash, err := ComposeHashFromMRConfigID(mrConfigID)
	if err != nil {
		t.Fatalf("ComposeHashFromMRConfigID() = %v", err)
	}
	if composeHash != tcb.ComposeHash {
		t.Errorf("compose hash = %s, want %s", composeHash, tcb.ComposeHash)
	}

	// And app_compose hashes to it, which is what makes the compose file in the
	// report a trusted input rather than an assertion — no separately published
	// manifest is needed to learn which digests the deployment pinned.
	if got := hex.EncodeToString(hashOf(tcb.AppCompose)); got != composeHash {
		t.Errorf("sha256(app_compose) = %s, want the compose hash %s", got, composeHash)
	}
}

func hashOf(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

// The formula, against hardware. Both halves are pinned: each event's own digest,
// and the fold of all of them.
//
// Production does not check the per-event digests — a bad one cannot survive the
// fold — but asserting them here is what proves the digest formula itself, rather
// than proving only that some formula produces the right final value.
func TestReplayRTMR3AgainstRealHardware(t *testing.T) {
	quote, eventLogJSON, tcb := goldenReport(t)

	events, err := RuntimeEvents([]byte(eventLogJSON))
	if err != nil {
		t.Fatalf("RuntimeEvents() = %v", err)
	}
	if len(events) == 0 {
		t.Fatal("RuntimeEvents() = none; the golden report should carry dstack's boot events")
	}

	// Same order, same set — the runtime entries of the log the report carries.
	var declared []TdxEvent
	for _, entry := range tcb.EventLog {
		if entry.EventType == RuntimeEventType {
			declared = append(declared, entry)
		}
	}
	if len(declared) != len(events) {
		t.Fatalf("RuntimeEvents() returned %d events, tcb_info carries %d runtime entries", len(events), len(declared))
	}
	for i, event := range events {
		digest := event.Digest()
		if got, want := hex.EncodeToString(digest[:]), declared[i].Digest; got != want {
			t.Errorf("event %d (%q) digest = %s, want %s", i, event.Event, got, want)
		}
		if declared[i].IMR != 3 {
			t.Errorf("event %d (%q) is in IMR %d, want 3", i, event.Event, declared[i].IMR)
		}
	}

	replayed := ReplayRTMR3(events)
	rtmr3, err := RTMR(quote, 3)
	if err != nil {
		t.Fatalf("RTMR(3) = %v", err)
	}
	if got, want := hex.EncodeToString(replayed[:]), hex.EncodeToString(rtmr3); got != want {
		t.Errorf("ReplayRTMR3() = %s, want the quote's rtmr3 %s", got, want)
	}
}

// The byte order of the type tag is the one thing about the digest formula that
// cannot be seen in a passing test, because both orders are four bytes in the same
// place. dstack writes it with Rust's to_ne_bytes(), so a reader assuming
// big-endian silently never matches.
func TestEventDigestIsLittleEndianTagged(t *testing.T) {
	event := RuntimeEvent{Event: "zg-image-update", Payload: []byte("ghcr.io/x@sha256:" + strings.Repeat("a", 64))}

	got := event.Digest()
	// Same input under the wrong order. Not a hardcoded expected digest: this
	// fails if the implementation flips, and needs no update when the formula's
	// other inputs are reshaped.
	wrong := taggedDigest(t, []byte{0x08, 0x00, 0x00, 0x01}, event)
	if hex.EncodeToString(got[:]) == hex.EncodeToString(wrong[:]) {
		t.Error("Digest() matches a big-endian tag, want the little-endian one dstack writes")
	}
}

func taggedDigest(t *testing.T, tag []byte, event RuntimeEvent) []byte {
	t.Helper()
	h := sha512.New384()
	h.Write(tag)
	h.Write([]byte(":"))
	h.Write([]byte(event.Event))
	h.Write([]byte(":"))
	h.Write(event.Payload)
	return h.Sum(nil)
}

// An empty log folds to the reset value, which is what "nothing has been recorded"
// has to look like: a caller reads it as "still running what the compose pinned",
// so a nonzero answer here would send it looking for an event that does not exist.
func TestReplayRTMR3OfNothingIsTheResetValue(t *testing.T) {
	var zero [48]byte
	if got := ReplayRTMR3(nil); got != zero {
		t.Errorf("ReplayRTMR3(nil) = %x, want 48 zero bytes", got)
	}
}

func TestQuoteTooShort(t *testing.T) {
	short := make([]byte, offsetRTMR0)

	if _, err := MRTD(make([]byte, 10)); err == nil {
		t.Error("MRTD() on a 10-byte quote = nil, want an error rather than a panic")
	}
	if _, err := MRConfigID(make([]byte, 10)); err == nil {
		t.Error("MRConfigID() on a 10-byte quote = nil, want an error")
	}
	// Long enough for mrtd and mr_config_id, too short for any rtmr — the case a
	// bounds check written once at the top of the file would miss.
	if _, err := RTMR(short, 0); err == nil {
		t.Error("RTMR(0) on a truncated quote = nil, want an error")
	}
}

func TestRTMRIndexOutOfRange(t *testing.T) {
	quote, _, _ := goldenReport(t)

	for _, i := range []int{-1, 4, 100} {
		if _, err := RTMR(quote, i); err == nil {
			t.Errorf("RTMR(%d) = nil, want an error", i)
		}
	}
}

func TestComposeHashFromMRConfigID(t *testing.T) {
	hash := strings.Repeat("ab", composeHashLen)
	hashBytes, err := hex.DecodeString(hash)
	if err != nil {
		t.Fatalf("building the fixture: %v", err)
	}

	v1 := make([]byte, measurementLen)
	v1[0] = mrConfigVersionV1
	copy(v1[1:], hashBytes)

	got, err := ComposeHashFromMRConfigID(v1)
	if err != nil {
		t.Fatalf("ComposeHashFromMRConfigID(v1) = %v", err)
	}
	if got != hash {
		t.Errorf("ComposeHashFromMRConfigID(v1) = %s, want %s", got, hash)
	}

	// V2 is refused, not decoded. Its 32 bytes are a keccak256 over the compose
	// hash and three other values, so returning them as a compose hash would hand
	// a caller a value that compares equal to nothing and looks like a mismatch.
	v2 := make([]byte, measurementLen)
	v2[0] = mrConfigVersionV2
	copy(v2[1:], hashBytes)
	if _, err := ComposeHashFromMRConfigID(v2); err == nil {
		t.Error("ComposeHashFromMRConfigID(v2) = nil, want an error")
	}

	rejected := map[string][]byte{
		"unknown version": func() []byte { b := make([]byte, measurementLen); b[0] = 9; return b }(),
		"version zero":    make([]byte, measurementLen),
		"too short":       make([]byte, measurementLen-1),
		"too long":        make([]byte, measurementLen+1),
		// A nonzero tail means the field is not laid out as assumed, so the 32
		// bytes read as a hash are part of something else.
		"nonzero padding": func() []byte {
			b := make([]byte, measurementLen)
			b[0] = mrConfigVersionV1
			copy(b[1:], hashBytes)
			b[measurementLen-1] = 0xff
			return b
		}(),
	}
	for name, mrConfigID := range rejected {
		if _, err := ComposeHashFromMRConfigID(mrConfigID); err == nil {
			t.Errorf("ComposeHashFromMRConfigID(%s) = nil, want an error", name)
		}
	}
}

func TestRuntimeEventsRejectsMalformedLogs(t *testing.T) {
	if _, err := RuntimeEvents([]byte("not json")); err == nil {
		t.Error("RuntimeEvents() on garbage = nil, want an error")
	}
	// A payload that is not hex cannot be measured, and treating it as an empty
	// one would compute a digest for an event nobody wrote.
	badPayload := `[{"imr":3,"event_type":134217729,"event":"zg-image-update","event_payload":"nothex"}]`
	if _, err := RuntimeEvents([]byte(badPayload)); err == nil {
		t.Error("RuntimeEvents() with a non-hex payload = nil, want an error")
	}
	// Firmware entries are skipped rather than rejected: RTMR0-2 carry plenty of
	// them and they do not extend RTMR3.
	firmwareOnly := `[{"imr":0,"event_type":2147483659,"event":"","event_payload":"0954"}]`
	events, err := RuntimeEvents([]byte(firmwareOnly))
	if err != nil {
		t.Fatalf("RuntimeEvents() with firmware entries = %v, want nil", err)
	}
	if len(events) != 0 {
		t.Errorf("RuntimeEvents() = %v, want no runtime events", events)
	}
}
