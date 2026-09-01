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

// upstreamIdentityPattern is the same shape inference/config accepts for a provider
// identity (lowercase alphanumeric with optional hyphens), restated here rather than
// imported because common must not depend on inference.
//
// Restated, so it can drift — and drift would be silent and expensive: the identity
// goes into the canonical text UpstreamSetHash covers, which the signing key's
// derivation path will bind, so a writer normalising it one way and a reader another
// would produce two different keys for what everyone believes is one deployment.
// Changing either copy means changing both.
var upstreamIdentityPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

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
		if !upstreamIdentityPattern.MatchString(fields[2]) {
			return fmt.Errorf("%s record %q carries identity %q, which is not lowercase alphanumeric with optional hyphens", EventUpstreamAdd, payload, fields[2])
		}
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
	// Credentials in the URL are refused, not stripped. This record goes into RTMR3
	// and RTMR3 travels in the quote, which is served to anyone who asks — so a
	// userinfo section would publish whatever it holds. Stripping it would be worse
	// than refusing: the secret would already have been written by the time a reader
	// could tell, and the writer would think it had been accepted.
	if u.User != nil {
		return fmt.Errorf("base URL %q carries credentials; the ledger this record enters is public, so record the URL without them", raw)
	}
	// A query or a fragment cannot be part of a base. The forward URL is base+route,
	// so "…/v1?k=v" + "/chat/completions" is not a URL anyone meant, and a query is
	// also where an API key would end up if one were pasted in — see above.
	if u.RawQuery != "" || u.ForceQuery {
		return fmt.Errorf("base URL %q carries a query string; a base is concatenated with a route, so it cannot have one", raw)
	}
	if u.Fragment != "" {
		return fmt.Errorf("base URL %q carries a fragment; a base is concatenated with a route, so it cannot have one", raw)
	}
	// The host must be recorded in one form only. url.Parse lowercases the scheme but
	// not the host, so "HTTP://X:1/v1" would otherwise reach the canonical text as a
	// second spelling of one destination — two set hashes for one set, and therefore
	// two signing keys.
	if u.Host != strings.ToLower(u.Host) {
		return fmt.Errorf("base URL %q has an uppercase host; record it lowercase so one destination has one spelling", raw)
	}
	// A dot segment is another second spelling: "/v1/../v2" and "/v2" address the
	// same endpoint. Refusing beats normalising for the same reason as above — the
	// bytes in the log have to determine the set.
	if strings.Contains(u.Path, "/../") || strings.Contains(u.Path, "/./") ||
		strings.HasSuffix(u.Path, "/..") || strings.HasSuffix(u.Path, "/.") {
		return fmt.Errorf("base URL %q has a dot segment in its path; record the resolved path", raw)
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
