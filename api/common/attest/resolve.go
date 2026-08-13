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
	// EventImageUpdate carries "<repo>@sha256:<64hex>", the reference an upgrade
	// runs on.
	EventImageUpdate = "zg-image-update"
	// EventConfigUpdate carries hex(sha256(config file content)).
	EventConfigUpdate = "zg-config-update"

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
	// ConfigSHA256 is the hex SHA-256 of the config file content, from the last
	// recorded config change. Empty means none was recorded, so the file is still
	// whatever the deployment started with.
	ConfigSHA256 string
	// Events is the full runtime event sequence whose replay matched the quote.
	Events []RuntimeEvent
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
	for _, event := range ledger {
		if !strings.HasPrefix(event.Event, EventNamespace) {
			continue
		}
		switch event.Event {
		case EventImageUpdate:
			digest, err := digestOfImageRef(string(event.Payload))
			state.BrokerDigest, state.DigestSource, imageErr = digest, DigestSourceEvent, err
		case EventConfigUpdate:
			sum := string(event.Payload)
			if configErr = nil; !hexSHA256Pattern.MatchString(sum) {
				configErr = fmt.Errorf("%s payload %q is not a hex sha256", EventConfigUpdate, sum)
			}
			state.ConfigSHA256 = sum
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

	// Not the compose fallback: an image record exists, it just did not resolve, and
	// falling back would answer with the digest the deployment booted on while the
	// ledger says it was changed to something the writer could not name.
	if state.DigestSource == DigestSourceEvent {
		return state, nil
	}

	if state.BrokerDigest == "" {
		// No upgrade recorded, so the broker is still on the image the compose
		// pinned — and that file is now a trusted input, because it hashed to the
		// compose hash in the signed report body.
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
