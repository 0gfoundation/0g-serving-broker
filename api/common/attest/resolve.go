package attest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// The events the controller records in RTMR3, and their payload encodings.
//
// This is the one coupling surface between the process that writes the ledger and
// the process that reads it, which is why both sides take the names from here:
// payload bytes go into an event's digest, so a change to a name or an encoding is
// a change to the measurement, and a reader that had its own copy would drift into
// explaining a log that no longer exists.
//
// Payloads are bare bytes rather than JSON because a reader has to reproduce them
// exactly, and JSON leaves key order, spacing and escaping free.
const (
	// EventImageUpdate carries "<repo>@sha256:<64hex> <0xsigner> <enc_pub>": the
	// reference an upgrade runs on, then both keys derived from that image.
	EventImageUpdate = "zg-image-update"
	// EventConfigUpdate carries hex(sha256(config file content)).
	EventConfigUpdate = "zg-config-update"

	// EventUpstreamSet carries the WHOLE set of destinations the broker may forward
	// unsealed plaintext to: a header line naming how many members follow, then one
	// member per line.
	//
	//	count=2
	//	engine1 http://engine-1:8000/v1
	//	vendor https://vendor.example/v1 openrouter
	//
	// That set is the complete list of places a request can end up, which is what the
	// config's mapping of models onto names cannot itself establish — the mapping
	// lives in the config file, and no boot measurement covers its content.
	//
	// A record is a snapshot, not a mutation, so the last one decides and the earlier
	// ones are history. See parseUpstreamSet for that, and for why the count has to
	// travel in the same payload as the members it counts.
	EventUpstreamSet = "zg-upstream-set"

	// EventNamespace prefixes every event this project writes. dstack already
	// writes app-id, compose-hash and system-ready into RTMR3, and other
	// components may add their own; an unprefixed "image-update" could collide
	// with any of them.
	EventNamespace = "zg-"

	// eventSystemReady is dstack's boundary between what it measured at boot and
	// what the application wrote afterwards. Anything after it was written by a
	// container, which can pick any name — including one of dstack's own.
	eventSystemReady = "system-ready"
)

// Where a resolved broker digest came from.
//
// The two are not equally strong. DigestSourceCompose is bound to the quote by the
// compose hash in the signed report body, so it holds against anyone. DigestSourceEvent
// rests on the RTMR3 ledger, which says what was written and not who wrote it, so it is
// worth exactly as much as the deployment's confinement of RTMR3 writers — a property the
// caller settles by pinning ComposeHash to a compose it reviewed, not one this package can
// derive. See the package doc.
const (
	DigestSourceCompose = "compose" // no upgrade recorded; the digest the deployment booted on
	DigestSourceEvent   = "event"   // the last recorded upgrade
)

// digestPattern is the digest shape an image reference must carry. Lowercase hex
// only, matching what the controller accepts as an upgrade request.
var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// hexSHA256Pattern is the bare form zg-config-update carries.
var hexSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// encPubPattern is a 32-byte X25519 public key in hex. Same shape as hexSHA256Pattern and
// deliberately a separate name: they are different values that happen to be the same width.
var encPubPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// VerifiedQuote is the part of an attestation a caller must already have established, and the
// reason this package no longer looks at raw quote bytes.
//
// Every field here comes from one dstack-verifier /verify response for one GetQuote triple.
// Constructing this type is the caller asserting that the response said is_valid, that
// quote_verified and event_log_verified were both true, and that it checked the things only it
// can decide — tcb_status, advisory_ids, os_image_is_dev, os_image_hash and which key provider
// released this CVM's keys. This package cannot check any of those and does not pretend to.
//
// Taking verified inputs rather than a quote is deliberate. The alternative was a second
// implementation of dstack's own verification — the digest format, the register offsets, the
// mr_config_id layout — kept correct in parallel with theirs, and narrower than theirs, since
// none of it says anything about the OS image or the TCB. What is left below is the part nobody
// else implements, because it is ours: the zg-* records, the §4.2 report_data layout, and the
// binding between them.
type VerifiedQuote struct {
	// ComposeHash is app_info.compose_hash from the verify response, hex. It comes out of the
	// signed report body, so it is what makes tcb_info's app_compose believable once that
	// hashes to it.
	ComposeHash string
	// ReportData is the quote's 64 bytes, verbatim. The verifier returns them; it does not
	// interpret them, because the layout inside is ours (0g-pc SPEC §4.2).
	ReportData []byte
	// EventLogJSON is the log the verifier reported event_log_verified for. Any other log is
	// unanchored, and passing one here is a lie this package cannot detect.
	EventLogJSON []byte
}

func (v VerifiedQuote) check() error {
	if !hexSHA256Pattern.MatchString(v.ComposeHash) {
		return fmt.Errorf("compose hash %q is not a hex sha256; it must be app_info.compose_hash from a verify response", v.ComposeHash)
	}
	if len(v.ReportData) != reportDataLen {
		return fmt.Errorf("report_data is %d bytes, want %d", len(v.ReportData), reportDataLen)
	}
	if len(v.EventLogJSON) == 0 {
		return errors.New("no event log; pass the one dstack-verifier reported event_log_verified for")
	}
	return nil
}

// RunningState is what a CVM is running right now, derived from its quote alone.
type RunningState struct {
	// ComposeHash identifies the deployment's static configuration — every
	// container's initial digest, the volumes, who holds the docker socket. Read
	// out of the signed report body, so it is hardware-bound.
	ComposeHash string
	// BrokerDigest is "sha256:<64hex>", the image the broker is on.
	BrokerDigest string
	// DigestSource is DigestSourceCompose or DigestSourceEvent.
	DigestSource string
	// BrokerSigner is the response-signing address the ledger binds to BrokerDigest,
	// lowercase "0x…". Set only on the event path, where the record carries it and it has been
	// checked against the quote's report_data.
	//
	// It is what turns a digest into a statement about the running process. Without it a client
	// can learn which image the ledger names and can verify signatures against report_data, but
	// not that those are the same thing — report_data is chosen by the enclave, and the
	// per-image key is derivable only inside the CVM, so any divergence between the two would
	// be accepted rather than refused.
	BrokerSigner string
	// BrokerEncPub is the enclave encryption public key the ledger binds to BrokerDigest, hex,
	// and it is non-empty ONLY when it was checked against the quote's report_data.
	//
	// So empty means "do not seal anything to this deployment with what you have" — either no
	// record bound a key (the compose path) or the quote used the older layout, which carries
	// none. It never means "unchecked but probably fine".
	//
	// A client that seals a request MUST use this rather than report_data's copy on its own.
	// report_data is chosen by the attesting process, and the address the ledger binds is
	// PUBLIC — it is in the very event log the client just read — so an image that is not the
	// recorded one can publish the recorded ADDRESS beside an enc_pub of its own. It can never
	// sign a response under that address, so the response check would eventually refuse it. By
	// then the request was already sealed: the plaintext is gone before the signature that
	// would have caught it exists.
	BrokerEncPub string
	// ConfigSHA256 is the hex SHA-256 of the config file content, from the last
	// recorded config change.
	//
	// Empty means no config change was recorded. In a deployment that mounts the config
	// read-only everywhere but the controller, that does mean the file is unchanged since
	// boot, because the controller is then its only writer and every write it makes is
	// recorded first. Where the mount is writable it means only "no change went through
	// the controller", and the container holding it could have rewritten it unrecorded.
	//
	// Either way there is nothing to compare a non-empty value against: the compose file
	// pins the config's path, not its content, so no boot measurement covers it. A reader
	// learns that the file changed and when, relative to the image records — not what it
	// changed to.
	ConfigSHA256 string
	// Upstreams is the set of destinations the ledger permits, in the order the last
	// EventUpstreamSet record listed them. Meaningful only when UpstreamsState is
	// UpstreamsKnown.
	//
	// The order is for reading the set, not for identifying it: UpstreamSetHash sorts,
	// so two deployments permitting the same destinations agree whatever order their
	// records listed them in.
	//
	// What the set is worth depends on the provider, which this package does not
	// know: for a self-hosted deployment every entry is a container inside the CVM,
	// so the set says the plaintext did not leave; for one forwarding to an external
	// vendor the plaintext leaves by definition, and the set says which vendors could
	// have received it. This package reports the set; the caller decides what it
	// establishes.
	Upstreams []Upstream
	// UpstreamsState is UpstreamsUnrecorded, UpstreamsKnown or UpstreamsUnknown, and
	// it is the single source of truth for which of the three this answer is.
	//
	// A string rather than a bool pair, and certainly rather than "is UpstreamsErr
	// nil": this type is destined for the SDK, so it gets transported, and an `error`
	// field marshals to `{}` under encoding/json. A verifier on the far side of that
	// would render an unreplayable log as "records appeared, zero permitted
	// destinations" — the fail-open direction the three states exist to prevent.
	//
	//   UpstreamsUnrecorded  no record appeared. The deployment predates these
	//                        records or never writes them, so it forwards according
	//                        to a config no measurement covers and NOTHING here
	//                        bounds where plaintext goes.
	//   UpstreamsKnown       the set is Upstreams, and it may be empty. Empty is the
	//                        opposite of unrecorded: a record said "none", which is a
	//                        bound of zero.
	//   UpstreamsUnknown     the deciding record could not be read. UpstreamsErr says
	//                        why.
	//
	// Collapsing unrecorded and empty would be worst once the set feeds the signing
	// key's derivation path: an unbounded deployment and one explicitly bounded to
	// nothing would derive the same key.
	UpstreamsState string
	// UpstreamsErr says why the set is unknown, and is empty in the other two states.
	//
	// A string and not an `error`, for the reason UpstreamsState is a string: an
	// `error` field marshals to `{}` and cannot be unmarshalled back at all, so the
	// ONE state that carries a reason — unknown — would fail to decode, and an SDK
	// consumer doing the ordinary `if err := json.Unmarshal(...)` would discard the
	// whole RunningState for precisely the log a verifier most needs to see.
	//
	// Nothing matches on this value, so no error identity is lost by flattening it.
	//
	// Unknown is reported rather than returned as a hard error because the rest of
	// this answer does not depend on it: which image the broker runs and which keys it
	// holds are established by other records, and an unreadable upstream record must
	// not take them down with it. It is also recoverable — appending a readable record
	// after it is a complete fix, exactly as it is for the image record — so failing
	// the whole call would be refusing to answer a question a live CVM can still
	// settle.
	//
	// What is NOT done is reporting the members of an unreadable record that happened
	// to parse. A partial set understates where plaintext can go, which is the
	// direction that misleads.
	UpstreamsErr string
	// UpstreamChanges is the history the final set does not show: one line for every
	// way a record differed from the record before it, in the order the records
	// appeared.
	//
	//   "<name>: added as <URL> (<identity>)"
	//   "<name>: <old URL> (<old identity>) -> <new URL> (<new identity>)"
	//   "<name>: <old URL> (<old identity>) -> withdrawn"
	//   "a superseded <event> record could not be read: <reason>"
	//
	// The first record of the boot contributes nothing — it is the baseline, not a
	// change — and neither does a record identical to its predecessor, which is what a
	// writer re-emitting its unchanged table produces. So empty is the ordinary case,
	// including on a CVM that has rebooted many times.
	//
	// The last line is the one case where an unreadable record still leaves a trace.
	// Only the last record decides the set, so a garbage record followed by a good one
	// resolves to UpstreamsKnown; without this the fact that the log held something
	// unreadable would not appear in either value a caller consumes.
	//
	// It exists because Upstreams and UpstreamSetHash — the two values a caller
	// actually consumes — describe only the final state. Telling a caller to walk
	// Events for the rest is not a mitigation: neither of those values does that.
	//
	// Both directions of change are fail-open. Rewriting a name from an external vendor
	// to something that looks like an in-CVM container makes a deployment appear to have
	// kept plaintext inside. Withdrawing an external vendor leaves a set of nothing but
	// in-CVM containers, which reads the same way — while the log itself holds the
	// evidence that plaintext could have left minutes earlier. And this is the one place
	// in this ledger where last-wins has no cross-check: an image record's signer must
	// match the quote's report_data, and nothing yet ties an upstream record to anything
	// outside the log.
	//
	// A caller treating a non-empty value as suspicious is making a judgement this
	// package does not make: a legitimate reconfiguration produces one too.
	UpstreamChanges []string
	// Events is the full runtime event sequence whose replay matched the quote.
	Events []RuntimeEvent
}

// Upstream is one permitted destination for unsealed plaintext.
type Upstream struct {
	// Name is what the config's model mapping refers to. Unique within a set: a name
	// bound to two URLs at once would make that mapping unreadable, which is the whole
	// point of recording the set, so a record naming one twice is refused outright.
	Name string
	// URL is the destination's base URL, exactly as recorded.
	URL string
	// Identity is the upstream's machine-key identity, empty when the record
	// carried none. Only meaningful for upstreams outside this CVM, where it is
	// what a routing proof attributes the request to.
	Identity string

	// The two fields below are NOT from the record. They are what the deployment's own
	// compose file says about the destination the record names, filled in by
	// ResolveRunningState — see classifyUpstreams. A hand-built Upstream has them empty,
	// and neither enters UpstreamSetHash: the compose is already bound to the quote by
	// its own hash, and folding it into the set hash would make two deployments that
	// permit the same destinations disagree because they pin different image versions.

	// ComposeService is the compose service whose name the URL's host matches, empty
	// when no service matches. It is what separates a self-deployed upstream from an
	// external one, and it is a fact rather than a judgement: non-empty means the host
	// resolves through the compose's own service DNS, so the destination is a container
	// this deployment declares and the plaintext does not leave the measured boundary
	// to reach it.
	//
	// Empty covers two different things, and the caller has to tell them apart itself:
	// a genuine external vendor, and a host that happens not to be a service name —
	// an IP literal, say, which bypasses service DNS entirely, so the compose cannot
	// say which container (if any) it addresses.
	//
	// What is NOT established here is that nothing in the compose redirects the name
	// outward: an extra_hosts entry or a custom dns: could point a service name
	// somewhere else. Both live in the compose, so a caller reviewing the manifest sees
	// them; this field says only that the name is declared as a service.
	ComposeService string
	// PinnedImage is the image reference the compose pins for ComposeService. It is set
	// exactly when ComposeService is, never one without the other.
	//
	// An earlier version of this said "or the service names no image", which describes a
	// state the code cannot reach and would have had a caller writing a dead branch:
	// PinnedImages drops an imageless service from its map entirely, so such a service
	// never enters composeServiceLookup and BOTH fields come back empty — which is what
	// TestResolveLeavesAnImagelessServiceUnclassified asserts. That is also the third
	// thing an empty ComposeService covers, alongside the two its own doc names.
	//
	// It is what the deployment BOOTED with, not a statement about what runs there now.
	// Nothing records a non-broker container's image change: zg-image-update carries
	// the broker's signer and enc_pub, so it describes the broker, and the compose only
	// pins an initial digest. Anything holding the docker socket can therefore replace
	// an engine's image without leaving a record, and this field would still name the
	// pinned one. Closing that is a separate change.
	//
	// Whether the reference pins a digest at all is left to the caller, for the reason
	// PinnedImages leaves it: "mysql:8.0" is a truthful answer to what the compose says,
	// and only the caller knows whether a tag is good enough for what it concludes.
	PinnedImage string
}

// ResolveRunningState answers "what is this CVM running", and only that.
//
// The inputs are one GetQuote response: the raw quote bytes, its event_log, and
// its tcb_info. The quote is the only trusted one of the three — the other two
// arrive over plain HTTP from the party being described — so each is anchored back
// to it before anything in it is believed:
//
//   - the event log, by replaying it into RTMR3 and requiring the quote's value;
//   - tcb_info's app_compose, by hashing it and requiring the compose hash the
//     signed report body carries.
//
// What it does not do is decide whether the answer is acceptable. It verifies no
// DCAP signature and compares nothing against an expected value; both need inputs
// only the caller has (Intel's collateral, and a list of digests that must come
// from software the user installed).
//
// Nor does it establish who wrote the records it reads. An answer carrying
// DigestSourceEvent is the ledger's claim, and any container reaching
// /var/run/dstack.sock — the broker included, in deployments as they stand — can add to
// that ledger. Settle that by comparing the returned ComposeHash against a compose you
// published and reviewed; the package doc explains why this package cannot settle it for
// you.
//
// Unknown events are handled asymmetrically on purpose. A zg- event this reader
// does not know is an error: the namespace is ours, so an unknown one means the
// writer is ahead of the reader, and skipping it would let a future event with real
// meaning pass unread. Everything else is skipped, because dstack and other
// components legitimately write into the same register. The cost is a release order
// — readers before writers.
func ResolveRunningState(v VerifiedQuote, tcbInfoJSON []byte, brokerService string) (*RunningState, error) {
	if err := v.check(); err != nil {
		return nil, err
	}
	composeHash := v.ComposeHash

	events, err := RuntimeEvents(v.EventLogJSON)
	if err != nil {
		return nil, err
	}

	// No replay here: dstack-verifier already required this log to reproduce the quote's
	// registers, which is what its event_log_verified means. Doing it again would be a second
	// implementation of dstack's digest format to keep correct, and a narrower one — the
	// verifier also checks the OS image hash, the ACPI tables and the TCB status, none of which
	// a replay says anything about.
	//
	// What is NOT delegated is the line below. The verifier never sees app_compose — its
	// request carries quote, event_log and vm_config — so nothing has tied tcb_info to this
	// quote yet, and the compose file is only trustworthy once it hashes to the compose hash
	// the verifier reported out of the signed report body.
	appCompose, err := appComposeOf(tcbInfoJSON)
	if err != nil {
		return nil, err
	}
	if sum := sha256.Sum256([]byte(appCompose)); hex.EncodeToString(sum[:]) != composeHash {
		return nil, fmt.Errorf("sha256(app_compose) is %x, but the quote's compose hash is %s", sum, composeHash)
	}

	// Everything after system-ready was written by a container, so it is the only
	// part where our events can be. Reading them from the whole log would let a
	// forged zg-image-update placed among the boot events be taken as ours —
	// dstack's own reader breaks at this same boundary for the same reason.
	ledger, err := afterSystemReady(events)
	if err != nil {
		return nil, err
	}

	// Only the last record of each kind decides, so an earlier one that named nothing
	// readable is history rather than a verdict. The writer emits exactly that when it
	// cannot establish the truth — a container it could not inspect, a file it could
	// not re-read — and treating any such record as fatal would let one transient
	// docker error make a CVM permanently unverifiable for the rest of its boot, with
	// no way for a later correct record to recover it.
	//
	// The last one being unreadable is still fatal. That is the whole point of the
	// writer emitting it: refusing beats believing the record it replaced.
	state := &RunningState{ComposeHash: composeHash, Events: events}
	var imageErr, configErr error
	// The last set that could be read, and whether there was one. Kept separately from
	// state.Upstreams because an unreadable record clears that — see the upstream case.
	var lastSet []Upstream
	var haveSet bool
	for _, event := range ledger {
		if !strings.HasPrefix(event.Event, EventNamespace) {
			continue
		}
		switch event.Event {
		case EventImageUpdate:
			digest, signer, encPub, err := imageRecord(string(event.Payload))
			state.BrokerDigest, state.BrokerSigner, state.BrokerEncPub, state.DigestSource, imageErr = digest, signer, encPub, DigestSourceEvent, err
		case EventConfigUpdate:
			sum := string(event.Payload)
			if configErr = nil; !hexSHA256Pattern.MatchString(sum) {
				configErr = fmt.Errorf("%s payload %q is not a hex sha256", EventConfigUpdate, sum)
			}
			state.ConfigSHA256 = sum
		case EventUpstreamSet:
			// One record carries the whole set, so the last one decides — the same rule as
			// the two record types above, and it holds for the same reason: RTMR3 only
			// appends, so the only way to correct a record is to write a better one after
			// it, and treating an earlier record as binding would let one bad write make a
			// CVM unverifiable for the rest of its boot with no repair available.
			//
			// Unlike the image record, an unreadable last record is reported rather than
			// returned as an error, because the answers that do not depend on it still
			// hold — see UpstreamsErr for why that asymmetry is deliberate.
			next, err := parseUpstreamSet(string(event.Payload))
			if err != nil {
				// Any earlier set is dropped along with the state, because this record
				// superseded it: reporting a set the log has already moved past would be a
				// claim about where plaintext goes now, and it would be wrong.
				state.Upstreams, state.UpstreamsState, state.UpstreamsErr = nil, UpstreamsUnknown, err.Error()
				break
			}
			if state.UpstreamsState == UpstreamsUnknown {
				// This record repairs the set, and that repair is the whole reason an earlier
				// bad record is not fatal. But it also erases the only trace of it from both
				// values a caller consumes, so the fact is kept here.
				state.UpstreamChanges = append(state.UpstreamChanges, fmt.Sprintf("a superseded %s record could not be read: %s", EventUpstreamSet, state.UpstreamsErr))
			}
			// Compared against the last set that READ, not against state.Upstreams, which an
			// unreadable record in between will have cleared. Otherwise an unreadable record
			// would suppress the change log across itself: write garbage, then the rewritten
			// set, and the rewrite goes unreported — and the garbage is written by whoever
			// writes the records, so that would be a way to hide exactly what this log
			// exists to show.
			if haveSet {
				state.UpstreamChanges = append(state.UpstreamChanges, upstreamChanges(lastSet, next)...)
			}
			lastSet, haveSet = next, true
			state.Upstreams, state.UpstreamsState, state.UpstreamsErr = next, UpstreamsKnown, ""
		default:
			return nil, fmt.Errorf("unrecognised %s event %q: this reader is older than the CVM that wrote the log, so it cannot say what is running", EventNamespace, event.Event)
		}
	}
	if imageErr != nil {
		return nil, fmt.Errorf("the last %s record says nothing readable, so what the broker runs is unknown: %w", EventImageUpdate, imageErr)
	}
	if configErr != nil {
		return nil, configErr
	}
	// Which members are containers this deployment declares, and what it pinned for them.
	//
	// Here rather than after the branch below, because the event path returns from inside
	// it — an upgraded deployment is the normal case, and it needs this as much as one
	// still on its pinned image.
	//
	// Guarded on a non-empty set, which keeps this from being able to fail any call that
	// succeeds today: nothing writes upstream records yet, so every live deployment takes
	// the skip. That matters because PinnedImages CAN fail on a compose whose hash is
	// perfectly good — an app_compose that is not a docker-compose manifest, or a compose
	// file whose YAML does not parse — and the compose path below already returns that
	// error rather than working around it.
	//
	// Failing is right for it here too, and it is the one place in the upstream handling
	// that does fail rather than report. The distinction is where the input comes from: an
	// upstream record is written by the party being described, so an unreadable one is a
	// fact about that party and gets reported. The compose is a trusted input this
	// function already verified against the signed report body — if it cannot be read,
	// the function's own premise is gone. And the alternative is worse than an error:
	// leaving every member unclassified would report a set in which nothing is inside the
	// boundary, which is a claim, and a false one.
	if state.UpstreamsState == UpstreamsKnown && len(state.Upstreams) > 0 {
		images, err := PinnedImages(tcbInfoJSON)
		if err != nil {
			return nil, fmt.Errorf("the ledger records upstreams, so the compose has to say which of them are containers it declares: %w", err)
		}
		state.Upstreams = classifyUpstreams(state.Upstreams, images)
	}

	// A record wins over the compose pin, and the two are mutually exclusive.
	//
	// Redundant today, deliberately: digestOfImageRef cannot answer with an empty digest and no
	// error, so reaching here with DigestSourceEvent means a valid digest is already set and the
	// fallback's own guard would skip it. A mutation removing this line fails no test.
	//
	// Kept because it puts the precedence in the control flow instead of leaving it implied by
	// the conjunction of two earlier conditions — and it becomes load-bearing the moment either
	// moves, which is the kind of change that happens. An earlier version of this comment
	// claimed it was preventing a fallback after an unreadable record; that case returns an
	// error twenty lines above.
	if state.DigestSource == DigestSourceEvent {
		// The record names an image AND the address the key derived from that image has.
		// Require the quote to name the same one.
		//
		// This is the step that makes the ledger a statement about the running process rather
		// than about an installation that may since have been undone. Everything that could
		// make the two disagree ends here instead of being believed: a broker publishing an
		// address of its own choosing, a controller killed between recording and recreating so
		// a later start ran the old image under the new record, a digest resolved from a
		// repository the record did not mean.
		//
		// It cannot be checked the other way round — deriving the address from the digest needs
		// the app key, which never leaves the CVM — so the controller does that derivation and
		// writes the answer into the record, where the hardware's append-only ledger keeps it.
		quoteSigner, err := SignerFromReportData(v.ReportData)
		if err != nil {
			return nil, fmt.Errorf("the ledger binds a signer, so the quote must name one: %w", err)
		}
		if !strings.EqualFold(state.BrokerSigner, quoteSigner) {
			return nil, fmt.Errorf("the last %s record binds signer %s to %s, but the quote names %s: the process holding the signing key is not the one the ledger describes",
				EventImageUpdate, state.BrokerSigner, state.BrokerDigest, quoteSigner)
		}

		// The enc_pub too, and for a reason the signer check does not cover. Checking only the
		// address leaves a client sealing its REQUEST to a key of the attesting process's
		// choosing: the address the ledger binds is public, so an image that is not the
		// recorded one can publish it beside its own enc_pub. The response signature would
		// eventually refuse that image — but the prompt is already gone by then.
		quoteEncPub, err := EncPubFromReportData(v.ReportData)
		if err != nil {
			return nil, fmt.Errorf("reading the quote's enc_pub: %w", err)
		}
		if quoteEncPub == "" {
			// The older report_data layout carries no enc_pub, so there is nothing to compare
			// the record's against. The digest and the signer still stand — but the key is
			// blanked rather than passed through unchecked, because a caller cannot tell
			// "vouched for" from "we never looked", and the one place it matters is sealing a
			// request, where using the wrong key cannot be undone by a later signature check.
			// A client that intends to seal must fetch the §4.2 quote.
			state.BrokerEncPub = ""
		} else if !strings.EqualFold(state.BrokerEncPub, quoteEncPub) {
			return nil, fmt.Errorf("the last %s record binds enc_pub %s to %s, but the quote names %s: a request sealed to the quote's key would reach code the ledger does not describe",
				EventImageUpdate, state.BrokerEncPub, state.BrokerDigest, quoteEncPub)
		}
		return state, nil
	}

	if state.BrokerDigest == "" {
		// No upgrade recorded, so the broker is still on the image the compose
		// pinned — and that file is now a trusted input, because it hashed to the
		// compose hash in the signed report body.
		//
		// This path binds no signer address, because there is no record to carry one, so
		// unlike the event path it cannot check that the key signing responses belongs to
		// the digest it reports. It is sound only while "no record" really does mean "still
		// on the pinned image", and that is not free: RTMR3 resets on every boot, so a
		// container left in place by `docker compose up` after an in-band upgrade would be
		// reported as the pinned image while running something else. What prevents it is the
		// controller invalidating the recreated container's compose config-hash label, so
		// compose recreates it on the pinned image at the next boot. Remove that and this
		// fallback starts lying.
		images, err := PinnedImages(tcbInfoJSON)
		if err != nil {
			return nil, err
		}
		ref, ok := images[brokerService]
		if !ok {
			return nil, fmt.Errorf("no service %q in the deployment's compose file; it defines %v", brokerService, slices.Sorted(maps.Keys(images)))
		}
		digest, err := digestOfImageRef(ref)
		if err != nil {
			return nil, fmt.Errorf("no image upgrade was recorded, so the running image is the one compose pinned, and %w", err)
		}
		state.BrokerDigest = digest
		state.DigestSource = DigestSourceCompose
	}

	return state, nil
}

// PinnedImages returns the image reference each service in the deployment's compose
// file names, keyed by service name. Services with no image are omitted.
//
// tcb_info.app_compose is the compose manifest, and the manifest carries the
// compose file itself — which is why no separately published mapping from compose
// hash to pinned digests is needed. A pure function of that text, touching no
// quote; but a manifest whose hash has not been checked against the quote's
// compose hash says nothing about what is running.
//
// Whether a reference pins a digest is left to the caller. "mysql:8.0" is a
// perfectly good answer to "what does the compose file say", and only the caller
// knows whether that is good enough for what it is about to conclude.
func PinnedImages(tcbInfoJSON []byte) (map[string]string, error) {
	appCompose, err := appComposeOf(tcbInfoJSON)
	if err != nil {
		return nil, err
	}

	var manifest struct {
		DockerComposeFile string `json:"docker_compose_file"`
	}
	if err := json.Unmarshal([]byte(appCompose), &manifest); err != nil {
		return nil, fmt.Errorf("parsing app_compose: %w", err)
	}
	if manifest.DockerComposeFile == "" {
		return nil, fmt.Errorf("app_compose carries no docker_compose_file")
	}

	var compose struct {
		Services map[string]struct {
			Image string `yaml:"image"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(manifest.DockerComposeFile), &compose); err != nil {
		return nil, fmt.Errorf("parsing docker_compose_file: %w", err)
	}

	images := make(map[string]string, len(compose.Services))
	for name, service := range compose.Services {
		if service.Image != "" {
			images[name] = service.Image
		}
	}
	return images, nil
}

// afterSystemReady returns the events a container wrote, which is everything past
// dstack's system-ready marker.
//
// A log with no such marker is refused rather than read from the top. Without the
// boundary there is no way to tell dstack's boot facts from an application's
// events, and an application can write any name it likes.
func afterSystemReady(events []RuntimeEvent) ([]RuntimeEvent, error) {
	for i, event := range events {
		if event.Event == eventSystemReady {
			return events[i+1:], nil
		}
		// Nothing of ours can predate system-ready: the controller is a container
		// and does not run until after it. One here means the log is not what it
		// claims to be.
		if strings.HasPrefix(event.Event, EventNamespace) {
			return nil, fmt.Errorf("%s event %q appears before %s, where only dstack writes", EventNamespace, event.Event, eventSystemReady)
		}
	}
	return nil, fmt.Errorf("no %s event in the log, so dstack's boot events cannot be told apart from the application's", eventSystemReady)
}

// digestOfImageRef takes the digest out of "<repo>@sha256:<64hex>".
//
// A reference without one names an image by tag, and a tag is a registry-side
// pointer: two pulls of it can return different images, so it does not answer what
// is running. Refused rather than passed through, because a caller comparing it
// against a digest would see a mismatch and blame the wrong thing.
func digestOfImageRef(ref string) (string, error) {
	_, digest, pinned := strings.Cut(ref, "@")
	if !pinned {
		return "", fmt.Errorf("image reference %q pins no digest, so which image it names is not decided by the reference", ref)
	}
	if !digestPattern.MatchString(digest) {
		return "", fmt.Errorf("image reference %q carries %q, want \"sha256:\" and 64 lowercase hex characters", ref, digest)
	}
	return digest, nil
}

// appComposeOf pulls the compose manifest text out of a tcb_info document.
//
// The exact bytes, not a re-serialisation: the caller hashes them against the
// compose hash in the signed report, and any reformatting would break that.
func appComposeOf(tcbInfoJSON []byte) (string, error) {
	var tcbInfo struct {
		AppCompose string `json:"app_compose"`
	}
	if err := json.Unmarshal(tcbInfoJSON, &tcbInfo); err != nil {
		return "", fmt.Errorf("parsing tcb_info: %w", err)
	}
	if tcbInfo.AppCompose == "" {
		return "", fmt.Errorf("tcb_info carries no app_compose, so the deployment's compose file is unavailable")
	}
	return tcbInfo.AppCompose, nil
}

// imageRecord reads a zg-image-update payload: an image reference, then both keys derived from
// that image — the response-signing address and the enclave encryption public key.
//
//	ghcr.io/0gfoundation/0g-serving-broker@sha256:<64hex> 0x<40hex> <64hex>
//
// A payload naming no digest is how the writer says it could not establish the truth, and is
// refused — the point of it emitting one at all is that refusing beats believing the record it
// replaced.
//
// A payload with a digest but only one key, or none, is refused for the same reason rather than
// treated as an older format to tolerate. Such a record would be exactly as plausible as a
// correct one and exactly as unverifiable, and it is the shape an attacker would choose: the
// digest a reviewer is looking for, with nothing tying it to whoever holds the keys. Both are
// required together rather than checked as far as they go, because a half-checked record is the
// state a caller cannot distinguish from a checked one. No released controller writes that form.
func imageRecord(payload string) (digest, signer, encPub string, err error) {
	fields := strings.Fields(payload)
	if len(fields) == 0 {
		return "", "", "", fmt.Errorf("%s payload is empty", EventImageUpdate)
	}
	digest, err = digestOfImageRef(fields[0])
	if err != nil {
		return "", "", "", err
	}
	if len(fields) < 3 {
		return "", "", "", fmt.Errorf("%s record %q names an image but not both keys, so nothing ties that image to the key signing responses or the key requests are sealed to", EventImageUpdate, payload)
	}
	if len(fields) > 3 {
		return "", "", "", fmt.Errorf("%s payload %q has %d fields, want an image reference, a signer address and an enc_pub", EventImageUpdate, payload, len(fields))
	}
	signer = strings.ToLower(fields[1])
	if !addressPattern.MatchString(signer) {
		return "", "", "", fmt.Errorf("%s record %q does not carry an address", EventImageUpdate, payload)
	}
	encPub = strings.ToLower(fields[2])
	if !encPubPattern.MatchString(encPub) {
		return "", "", "", fmt.Errorf("%s record %q does not carry a 32-byte enc_pub", EventImageUpdate, payload)
	}
	return digest, signer, encPub, nil
}
