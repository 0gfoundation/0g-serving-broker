package attest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// upstreamNamePattern is what a name may be. Deliberately narrow: the name is the
// key a config's model mapping refers to, it is compared byte-for-byte, and it goes
// into the canonical text UpstreamSetHash covers — so anything that could be written
// two ways (case, whitespace, unicode look-alikes) would let one name mean two
// things. Lowercase alphanumeric with dashes and underscores has no such freedom.
var upstreamNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// upstreamSet replays EventUpstreamAdd and EventUpstreamRemove into the set that is
// permitted now, keeping the order names were first added so a caller can print the
// set as the log built it. UpstreamSetHash does not use that order — see there.
type upstreamSet struct {
	byName map[string]Upstream
	order  []string
}

func newUpstreamSet() *upstreamSet {
	return &upstreamSet{byName: map[string]Upstream{}}
}

// add applies one EventUpstreamAdd payload: "<name> <base URL>" or
// "<name> <base URL> <identity>".
//
// Re-adding a name with an identical record is accepted, so a writer that re-emits
// its table at boot (which it must: RTMR3 is cleared, and the assignment that
// survives on disk has to be recorded again) does not have to diff against what it
// already wrote. Re-adding a name with a DIFFERENT record is refused: the config
// maps models onto names, so a name that silently changed meaning mid-boot would
// make that mapping unreadable — the one thing recording the set is for. Moving a
// name to another URL is remove-then-add, which says so in the log.
func (s *upstreamSet) add(payload string) error {
	fields := strings.Fields(payload)
	if len(fields) < 2 || len(fields) > 3 {
		return fmt.Errorf("%s payload %q has %d fields, want a name, a base URL and an optional identity", EventUpstreamAdd, payload, len(fields))
	}
	name := fields[0]
	if !upstreamNamePattern.MatchString(name) {
		return fmt.Errorf("%s record %q names %q, which is not a lowercase alphanumeric name (dashes and underscores allowed, 63 bytes max)", EventUpstreamAdd, payload, name)
	}
	base := fields[1]
	if err := validUpstreamURL(base); err != nil {
		return fmt.Errorf("%s record %q: %w", EventUpstreamAdd, payload, err)
	}
	next := Upstream{Name: name, URL: base}
	if len(fields) == 3 {
		next.Identity = fields[2]
	}
	if prev, ok := s.byName[name]; ok {
		if prev != next {
			return fmt.Errorf("%s record %q redefines %q, which the log already bound to %q: a name that means two things in one boot makes the config's model mapping unreadable, so move it with %s and then %s", EventUpstreamAdd, payload, name, prev.URL, EventUpstreamRemove, EventUpstreamAdd)
		}
		return nil
	}
	s.byName[name] = next
	s.order = append(s.order, name)
	return nil
}

// remove applies one EventUpstreamRemove payload: "<name>".
//
// Removing a name the log never added is refused rather than ignored. Only the
// controller can write RTMR3 (see the package doc), so such a record means either a
// writer whose idea of the set differs from the log's — and then neither side's set
// can be trusted — or a log that is not what it claims to be.
func (s *upstreamSet) remove(payload string) error {
	fields := strings.Fields(payload)
	if len(fields) != 1 {
		return fmt.Errorf("%s payload %q has %d fields, want just a name", EventUpstreamRemove, payload, len(fields))
	}
	name := fields[0]
	if _, ok := s.byName[name]; !ok {
		return fmt.Errorf("%s record %q removes %q, which no %s record added: the log does not describe a set this reader can reconstruct", EventUpstreamRemove, payload, name, EventUpstreamAdd)
	}
	delete(s.byName, name)
	for i, n := range s.order {
		if n == name {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	return nil
}

// list returns the permitted set in the order names were first added. Nil when the
// set is empty, so RunningState.Upstreams distinguishes "no records" from "records
// that cancelled out" only by the event log, not by a zero-length slice.
func (s *upstreamSet) list() []Upstream {
	if len(s.order) == 0 {
		return nil
	}
	out := make([]Upstream, 0, len(s.order))
	for _, n := range s.order {
		out = append(out, s.byName[n])
	}
	return out
}

// validUpstreamURL rejects what cannot be a destination base URL.
//
// It does NOT decide whether the destination is acceptable — whether it must be
// inside the CVM, or must be HTTPS, depends on the provider type, which this
// package does not know. That belongs to the caller. What is checked here is only
// that the record names something a base URL could be, so a set built from it means
// something at all.
func validUpstreamURL(raw string) error {
	if raw != strings.TrimSpace(raw) {
		return fmt.Errorf("base URL %q has surrounding whitespace", raw)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("base URL %q does not parse: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("base URL %q is not http or https", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("base URL %q has no host", raw)
	}
	// A trailing slash is refused rather than trimmed. The forward URL is base+route
	// and the route starts with "/", so a base ending in one produces a double slash;
	// and trimming here would make two records that differ in one byte build the same
	// set, so the canonical text below — and therefore the set's identity — would no
	// longer be a function of the bytes in the log.
	if strings.HasSuffix(u.Path, "/") && u.Path != "/" {
		return fmt.Errorf("base URL %q ends in a slash; record it without one", raw)
	}
	if u.Path == "/" {
		return fmt.Errorf("base URL %q has a bare slash path; record it without one", raw)
	}
	return nil
}

// UpstreamSetHash is the identity of the permitted set: hex SHA-256 over one line
// per upstream, "<name> <URL> <identity>\n", sorted by name.
//
// Sorted rather than in insertion order, and hashing the set rather than the log,
// because it answers "where may plaintext go now" — two deployments permitting the
// same destinations must agree regardless of the order the records arrived in or how
// many times a name was moved. A history-dependent value would make the same set
// look like different sets.
//
// The empty set hashes to the SHA-256 of the empty string rather than to a special
// value: a caller distinguishes "no upstreams recorded" by RunningState.Upstreams
// being nil, and must not read a hash as evidence that any set was recorded.
//
// This is the encoding the signing key's derivation path will bind, so writer and
// reader have to produce it identically. It lives here for the reason the event
// names do: one definition, or the two sides drift.
func (r *RunningState) UpstreamSetHash() string {
	lines := make([]string, 0, len(r.Upstreams))
	for _, u := range r.Upstreams {
		lines = append(lines, u.Name+" "+u.URL+" "+u.Identity+"\n")
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "")))
	return hex.EncodeToString(sum[:])
}
