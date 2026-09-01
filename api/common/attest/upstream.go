package attest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
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

// apply dispatches one record by event name, so the caller does not repeat the
// mapping from name to method.
func (s *upstreamSet) apply(event, payload string) error {
	switch event {
	case EventUpstreamAdd:
		return s.add(payload)
	case EventUpstreamRemove:
		return s.remove(payload)
	default:
		return fmt.Errorf("upstreamSet cannot apply %q", event)
	}
}

// add applies one EventUpstreamAdd payload: "<name> <base URL>" or
// "<name> <base URL> <identity>".
//
// Re-adding a name is a MOVE: the last record wins, and both records stay in the log
// so the move is visible. An earlier version refused a differing re-add, on the
// grounds that a name meaning two things in one boot makes the config's model mapping
// unreadable. That was stricter than the rest of the ledger for no gain and it put a
// trap on the design's normal path: a writer re-emits its table at boot (it must —
// RTMR3 is cleared and the assignment survives on disk), so an operator adding an
// identity to an already-recorded name would produce a differing re-add and, with no
// corrective record possible on an append-only log, leave the set unknown until the
// next reboot.
//
// Last-wins matches how the image and config records already behave, and a reader
// still learns of the move: the superseded record is right there above it.
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
	if _, ok := s.byName[name]; ok {
		// Already present: overwrite in place and leave its position alone, so a boot
		// re-emit does not reshuffle the reported order and a move keeps the slot the
		// name has always had.
		s.byName[name] = next
		return nil
	}
	s.byName[name] = next
	s.order = append(s.order, name)
	return nil
}

// remove applies one EventUpstreamRemove payload: "<name>".
//
// Removing a name that is not in the set is a no-op, not an error. The set semantics
// are the same either way — it is absent afterwards — and refusing would put the same
// trap on the writer that a strict add did: a writer reconciling its table against a
// freshly cleared RTMR3 emits removes for entries it has dropped, finds no matching
// add, and would leave the set unknown for the boot.
//
// The name is still validated, because an unparseable one means the record does not
// describe an operation on this set at all.
func (s *upstreamSet) remove(payload string) error {
	fields := strings.Fields(payload)
	if len(fields) != 1 {
		return fmt.Errorf("%s payload %q has %d fields, want just a name", EventUpstreamRemove, payload, len(fields))
	}
	name := fields[0]
	if !upstreamNamePattern.MatchString(name) {
		return fmt.Errorf("%s record %q names %q, which is not a lowercase alphanumeric name (dashes and underscores allowed, 63 bytes max)", EventUpstreamRemove, payload, name)
	}
	if _, ok := s.byName[name]; !ok {
		return nil
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

// list returns the permitted set in the order names entered it: a name re-added
// while present keeps its position, and one removed and added again takes a new one
// at the end.
//
// Nil for an empty set. That does NOT mean "no records" — RunningState.UpstreamsRecorded
// is what separates a log that never mentioned upstreams from one that withdrew them
// all, because a slice cannot express both.
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
	// The host must be ASCII. upstreamNamePattern is narrow because unicode
	// look-alikes let one name mean two things, and the host is where that matters
	// more than the name: it decides where the plaintext actually goes. A Cyrillic
	// "е" in "еngine-1" is indistinguishable, to a person or a rendered set, from the
	// in-CVM container it impersonates.
	for i := 0; i < len(u.Host); i++ {
		if u.Host[i] >= utf8.RuneSelf {
			return fmt.Errorf("base URL %q has a non-ASCII host; record it in ASCII so a look-alike cannot pass for another destination", raw)
		}
	}
	// The rest are alternate spellings of one destination. Each is refused rather
	// than normalised, because the set has to be a function of the recorded bytes:
	// normalising here would mean two different records build the same set, and then
	// the bytes in the log no longer determine the hash the derivation path binds.
	if port := u.Port(); (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		return fmt.Errorf("base URL %q states the scheme's default port; record it without one", raw)
	}
	if strings.HasSuffix(u.Hostname(), ".") {
		return fmt.Errorf("base URL %q has a trailing dot in its host; record it without one", raw)
	}
	if u.EscapedPath() != u.Path {
		return fmt.Errorf("base URL %q percent-encodes its path; record the decoded path", raw)
	}
	if strings.Contains(u.Path, "//") {
		return fmt.Errorf("base URL %q has an empty path segment; record it without one", raw)
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
// It errors when there is no set to hash — no records appeared, or they could not be
// replayed. Returning a value in those cases is the failure mode that matters most:
// this hash is destined for the signing key's derivation path, so a deployment that
// bounds nothing and one bounded to nothing must not derive the same key, and neither
// must one whose log is unreadable.
//
// The prefix line is version-tagged so a later change to the encoding produces
// different hashes rather than silently colliding with this one.
//
// This is the encoding writer and reader have to produce identically. It lives here
// for the reason the event names do: one definition, or the two sides drift.
func (r *RunningState) UpstreamSetHash() (string, error) {
	if r.UpstreamsErr != nil {
		return "", fmt.Errorf("the upstream set is unknown, so it has no hash: %w", r.UpstreamsErr)
	}
	if !r.UpstreamsRecorded {
		return "", fmt.Errorf("no %s or %s record appeared, so no set was recorded and there is nothing to hash", EventUpstreamAdd, EventUpstreamRemove)
	}
	lines := make([]string, 0, len(r.Upstreams))
	for _, u := range r.Upstreams {
		lines = append(lines, u.Name+" "+u.URL+" "+u.Identity+"\n")
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(upstreamSetHashPrefix + strings.Join(lines, "")))
	return hex.EncodeToString(sum[:]), nil
}

// upstreamSetHashPrefix domain-separates this encoding from any other SHA-256 in the
// protocol and versions it, so changing the line format below cannot produce a hash a
// reader of the old format would accept.
const upstreamSetHashPrefix = "zg-upstream-set-v1\n"
