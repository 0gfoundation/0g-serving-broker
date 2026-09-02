package attest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
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

// The three states RunningState.UpstreamsState can hold. The zero value is
// UpstreamsUnrecorded on purpose: a RunningState nobody filled in must not read as
// "the set is known and empty".
const (
	UpstreamsUnrecorded = "" // no upstream record appeared
	UpstreamsKnown      = "known"
	UpstreamsUnknown    = "unknown" // the deciding record could not be read
)

// upstreamCountPrefix opens the one header line every EventUpstreamSet payload
// carries: "count=<n>", the number of members the writer means the set to hold.
//
// It cannot be confused with a member line. upstreamNamePattern admits no "=", so no
// member's first field can start with this.
const upstreamCountPrefix = "count="

// parseUpstreamSet reads one EventUpstreamSet payload into the set it names.
//
// The payload is the WHOLE set:
//
//	count=<n>
//	<name> <base URL> [<identity>]
//	…                              ← n of these
//
// It is not a mutation of what an earlier record said, so nothing is replayed and
// nothing accumulates: the last record decides, exactly as it does for the image and
// config records. That is why one record carries the whole set rather than one member
// each — the set comes from one config file, read as a whole, so a change to it is
// already atomic at the source, and an encoding that splits it across records invents a
// partial state the source never has, which RTMR3 then makes permanent because it only
// appends and a half-written batch has no successor that undoes it.
//
// # Why the count is in the payload
//
// Because "one record per set" does NOT by itself make a partial write unreadable, and
// an earlier version of this comment claimed it did. A payload is several lines; lose
// the tail and what remains is a SHORTER READABLE SET, not a broken record. Of the 76
// prefixes of a two-member payload, 40 parse cleanly, and one of them is exactly
// "engine1 http://engine-1:8000/v1\n" — a set of one in-CVM engine, with the external
// vendor silently gone, a state of known and a valid hash. That is the fail-open
// direction the whole record exists to close, and nothing else catches it:
// upstreamChanges reports no change because the shorter record is internally
// consistent, and the hash forks the signing key without saying why.
//
// It does not take an attacker. A writer renders this payload from its config, and the
// grammar gives it no way to spell "one member I could not establish" — so a writer
// that gives up mid-build has a shorter, perfectly readable set to emit.
//
// The count closes both. A writer takes n from its config BEFORE rendering, so a build
// that falls short says so; and a truncated payload loses members without losing the
// count. What makes this work where the deleted zg-upstream-set-complete record did not
// is that it travels in the same payload as the members: a separate record could be the
// one that got lost, which is what forced a reader to treat every unclosed batch as
// unusable.
//
// count=0 is the empty set, and it is the only spelling of it — an empty or
// whitespace-only payload has no header and is refused. The two must not be
// interchangeable: a bound of zero says the config routes nowhere, which is the
// strongest thing a set can say, and it must not be what a writer produces by failing
// to fill a field in.
//
// The order of the returned members is the order the record lists them, so a caller
// can print the set the way it was written. UpstreamSetHash does not use that order —
// see there.
func parseUpstreamSet(payload string) ([]Upstream, error) {
	lines := strings.Split(payload, "\n")
	// The header must be the first line that holds anything. Not "somewhere in the
	// payload": a reader that went looking for it would accept a payload whose real
	// header was truncated away and a member line's tail happened to spell another one.
	var i int
	for ; i < len(lines) && len(strings.Fields(lines[i])) == 0; i++ {
	}
	if i == len(lines) {
		return nil, fmt.Errorf("%s payload %q is empty; even the empty set is written out, as %s0, so that an unwritten payload is not read as a bound of zero", EventUpstreamSet, payload, upstreamCountPrefix)
	}
	header := strings.Fields(lines[i])
	if len(header) != 1 || !strings.HasPrefix(header[0], upstreamCountPrefix) {
		return nil, fmt.Errorf("%s payload starts with %q, want a header %s<n> naming how many members follow", EventUpstreamSet, lines[i], upstreamCountPrefix)
	}
	want, err := strconv.Atoi(strings.TrimPrefix(header[0], upstreamCountPrefix))
	if err != nil || want < 0 {
		return nil, fmt.Errorf("%s payload header %q does not name a member count", EventUpstreamSet, header[0])
	}

	members := make([]Upstream, 0, want)
	seen := make(map[string]struct{}, want)
	for _, line := range lines[i+1:] {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			// A blank line is skipped rather than refused: a writer that joins members with
			// "\n" produces a trailing one, and that is not worth making a set unreadable
			// over. It cannot hide a member, since a member needs two fields — and it cannot
			// hide one from the count either, which is what actually guards the tally.
			continue
		}
		if len(fields) < 2 || len(fields) > 3 {
			return nil, fmt.Errorf("%s payload line %q has %d fields, want a name, a base URL and an optional identity", EventUpstreamSet, line, len(fields))
		}
		name := fields[0]
		if !upstreamNamePattern.MatchString(name) {
			return nil, fmt.Errorf("%s payload line %q names %q, which is not a lowercase alphanumeric name (dashes and underscores allowed, 63 bytes max)", EventUpstreamSet, line, name)
		}
		// A name twice in one set is refused rather than resolved by last-wins. Inside a
		// single record there is no ordering to appeal to — both lines are equally current
		// — so a name meaning two URLs makes the config's model mapping unreadable, which
		// is the one thing recording the set is for. Between records last-wins still
		// applies; that is a different question, and there the log says which came later.
		if _, dup := seen[name]; dup {
			return nil, fmt.Errorf("%s payload names %q twice; within one set a name has one destination", EventUpstreamSet, name)
		}
		seen[name] = struct{}{}
		base := fields[1]
		if err := validUpstreamURL(base); err != nil {
			return nil, fmt.Errorf("%s payload line %q: %w", EventUpstreamSet, line, err)
		}
		member := Upstream{Name: name, URL: base}
		if len(fields) == 3 {
			if !upstreamIdentityPattern.MatchString(fields[2]) {
				return nil, fmt.Errorf("%s payload line %q carries identity %q, which is not lowercase alphanumeric with optional hyphens", EventUpstreamSet, line, fields[2])
			}
			member.Identity = fields[2]
		}
		members = append(members, member)
	}
	// The check the rest of this function exists to make possible. A count that
	// disagrees with what the payload spells means the writer and this reader do not
	// have the same set, and neither of them can say which is right — so no set.
	if len(members) != want {
		return nil, fmt.Errorf("%s payload says %s%d and spells %d member(s), so the set it names is not the set it lists", EventUpstreamSet, upstreamCountPrefix, want, len(members))
	}
	if len(members) == 0 {
		// Distinguished from a set only by UpstreamsState, never by this slice — see the
		// field's doc. Returned as nil rather than an empty slice so the two cannot be
		// told apart by shape and a caller is forced to read the state.
		return nil, nil
	}
	return members, nil
}

// upstreamChanges reports how the set moved from prev to next, one line per name that
// the two do not bind the same way, sorted by name:
//
//	"<name>: added as <URL> (<identity>)"
//	"<name>: <old URL> (<old identity>) -> <new URL> (<new identity>)"
//	"<name>: <old URL> (<old identity>) -> withdrawn"
//
// Nil when the two sets agree, which is the ordinary case: a writer re-emitting its
// unchanged table at boot — and it must re-emit, since RTMR3 is cleared while the
// config survives on disk — produces nothing here.
//
// It exists because Upstreams and UpstreamSetHash, the two values a caller consumes,
// describe only the final state, and both directions of change are fail-open.
// Rewriting a name from an external vendor to something that looks like an in-CVM
// container makes a deployment appear to have kept plaintext inside. Withdrawing that
// vendor leaves a set of nothing but in-CVM containers, which reads the same way,
// while the log itself holds the evidence that plaintext could have left minutes
// earlier. Telling a caller to walk the raw events instead is no answer when neither
// value they use does that.
func upstreamChanges(prev, next []Upstream) []string {
	before := make(map[string]Upstream, len(prev))
	for _, u := range prev {
		before[u.Name] = u
	}
	var lines []string
	for _, u := range next {
		switch old, existed := before[u.Name]; {
		case !existed:
			lines = append(lines, fmt.Sprintf("%s: added as %s", u.Name, describeUpstream(u)))
		case old != u:
			lines = append(lines, fmt.Sprintf("%s: %s -> %s", u.Name, describeUpstream(old), describeUpstream(u)))
		}
		delete(before, u.Name)
	}
	for _, u := range before {
		lines = append(lines, fmt.Sprintf("%s: %s -> withdrawn", u.Name, describeUpstream(u)))
	}
	// Sorted because the withdrawal lines come out of a map, so without this the same
	// pair of snapshots would produce a different order on every run.
	sort.Strings(lines)
	return lines
}

// looksNumeric says whether a host is written as digits and dots only — the shape of
// an IPv4 literal, whatever it parses to.
func looksNumeric(host string) bool {
	for i := 0; i < len(host); i++ {
		if (host[i] < '0' || host[i] > '9') && host[i] != '.' {
			return false
		}
	}
	return host != ""
}

// describeUpstream renders one binding for a change line: the URL, and the identity
// when there is one. "(no identity)" is spelled out rather than left blank, because
// losing an identity is the change a reader most needs to see and an empty string
// beside an arrow reads like a formatting slip.
func describeUpstream(u Upstream) string {
	if u.Identity == "" {
		return u.URL + " (no identity)"
	}
	return u.URL + " (" + u.Identity + ")"
}

// validUpstreamURL rejects what cannot be a destination base URL.
//
// It does NOT decide whether the destination is acceptable — whether it must be
// inside the CVM, or must be HTTPS, depends on the provider type, which this
// package does not know. That belongs to the caller. What is checked here is only
// that the record names something a base URL could be, so a set built from it means
// something at all.
//
// A note for whoever writes the writer: these rules are STRICTER than what
// inference/config accepts for a targetUrl today. Config never trims a trailing
// slash from the service-level targetUrl, and it accepts a bare "/" path, a default
// port and an uppercase host. A writer that emits configured URLs verbatim will
// therefore produce a record this refuses, and the whole set goes unknown for a live,
// valid deployment. Since readers must ship before writers, the writer cannot fix
// that by relaxing this — it has to normalise before it emits, to exactly these
// rules.
// The caller splits each payload line with strings.Fields, so raw never arrives with
// surrounding whitespace and this does not check for it.
func validUpstreamURL(raw string) error {
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
	// Checked on the raw bytes, not on u.Fragment: a bare "#" parses to an empty
	// Fragment, and there is no ForceQuery-equivalent flag to notice it. Without this
	// ".../v1" and ".../v1#" are one destination with two hashes.
	if strings.ContainsRune(raw, '#') {
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
	// The rest are alternate spellings of one destination, and each is refused rather
	// than normalised.
	//
	// The property being protected is not that the set is a function of the log's raw
	// bytes — the payload is split with strings.Fields, so a tab or a doubled space
	// builds the same set, and it does not need to be. What must hold is the other
	// direction: **one destination must have one spelling**, so that two deployments
	// permitting the same destinations reach the same hash. Normalising here would
	// satisfy that too, but refusing says so in the error rather than silently
	// accepting a record whose stored URL differs from what was written.
	// The scheme is checked on the RAW bytes, not on u.Scheme. url.Parse lowercases
	// the scheme, but the record stores raw verbatim and the canonical text is built
	// from it — so "HtTp://x/v1" would otherwise pass every check below and reach the
	// hash as a second spelling of one destination.
	if !strings.HasPrefix(raw, u.Scheme+"://") {
		return fmt.Errorf("base URL %q does not spell its scheme in lowercase; record it as %q", raw, u.Scheme)
	}
	// The port is compared numerically, and an equivalent spelling of one is refused
	// for the same reason: ":0080", ":01" and a bare ":" all denote what ":80" or no
	// port denotes, and each would hash differently.
	if port := u.Port(); port != "" {
		if strings.HasPrefix(port, "0") {
			return fmt.Errorf("base URL %q has a leading zero in its port; record it without one", raw)
		}
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("base URL %q does not have a valid port", raw)
		}
		if (u.Scheme == "http" && n == 80) || (u.Scheme == "https" && n == 443) {
			return fmt.Errorf("base URL %q states the scheme's default port; record it without one", raw)
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return fmt.Errorf("base URL %q has an empty port; record it without the colon", raw)
	}
	if strings.HasSuffix(u.Hostname(), ".") {
		return fmt.Errorf("base URL %q has a trailing dot in its host; record it without one", raw)
	}
	// An IP literal has to be in the form net.IP.String() produces. Otherwise the
	// same address has several accepted spellings — "[::1]", "[::0001]",
	// "[0:0:0:0:0:0:0:1]", and "010.0.0.1" for "10.0.0.1", which is the very
	// leading-zero form refused for ports above — and each is a different set hash for
	// one destination. A hostname is left alone: DNS names have no canonical form this
	// package could impose.
	if host := u.Hostname(); host != "" {
		ip := net.ParseIP(host)
		switch {
		case ip != nil && ip.String() != host:
			return fmt.Errorf("base URL %q spells the address %q non-canonically; record it as %q", raw, host, ip.String())
		case ip == nil && looksNumeric(host):
			// Digits and dots only, but not an address Go will parse — "010.0.0.1", say.
			// ParseIP refuses a leading zero because its meaning is ambiguous (some
			// resolvers read it as octal), and that ambiguity is exactly what must not
			// reach the set: it is another spelling of a destination the canonical form
			// already names.
			return fmt.Errorf("base URL %q has a host of digits and dots that is not a valid address; record the canonical form", raw)
		}
	}
	if u.EscapedPath() != u.Path {
		return fmt.Errorf("base URL %q percent-encodes its path; record the decoded path", raw)
	}
	if strings.Contains(u.Path, "//") {
		return fmt.Errorf("base URL %q has an empty path segment; record it without one", raw)
	}
	// A dot segment is another second spelling: "/v1/../v2" and "/v2" address the
	// same endpoint. Refusing beats normalising for the same reason as above — one
	// destination, one spelling.
	if strings.Contains(u.Path, "/../") || strings.Contains(u.Path, "/./") ||
		strings.HasSuffix(u.Path, "/..") || strings.HasSuffix(u.Path, "/.") {
		return fmt.Errorf("base URL %q has a dot segment in its path; record the resolved path", raw)
	}
	// A trailing slash is refused rather than trimmed, for both reasons: the forward
	// URL is base+route and the route starts with "/", so a base ending in one produces
	// a double slash; and it is one more second spelling of a single destination.
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
// Sorted rather than in the order the record listed them, and hashing the set rather
// than the log, because it answers "where may plaintext go now" — two deployments
// permitting the same destinations must agree regardless of the order their records
// listed them, or how many times the set was rewritten before. A history-dependent
// value would make the same set look like different sets.
//
// It errors when there is no set to hash — no record appeared, or the deciding one
// could not be read. Returning a value in those cases is the failure mode that
// matters most: this hash is destined for the signing key's derivation path, so a
// deployment that bounds nothing and one bounded to nothing must not derive the same
// key, and neither must one whose record is unreadable.
//
// The prefix line is version-tagged so a later change to the encoding produces
// different hashes rather than silently colliding with this one.
//
// This is the encoding writer and reader have to produce identically. It lives here
// for the reason the event names do: one definition, or the two sides drift.
func (r *RunningState) UpstreamSetHash() (string, error) {
	switch r.UpstreamsState {
	case UpstreamsUnknown:
		return "", fmt.Errorf("the upstream set is unknown, so it has no hash: %s", r.UpstreamsErr)
	case UpstreamsUnrecorded:
		return "", fmt.Errorf("no %s record appeared, so no set was recorded and there is nothing to hash", EventUpstreamSet)
	case UpstreamsKnown:
	default:
		return "", fmt.Errorf("unrecognised UpstreamsState %q", r.UpstreamsState)
	}
	// The encoding is space-delimited, so it is injective only while no field contains
	// a space: {Name:"a", URL:"b c"} and {Name:"a b", URL:"c"} would both render
	// "a b c". parseUpstreamSet cannot produce such a member — strings.Fields split the
	// line, and every field was pattern-checked — but this type is documented as
	// transported, so a RunningState arriving by json.Unmarshal can hold anything.
	// Refuse rather than hash, because a hash that does not identify the set it claims
	// to is worse than no hash at all.
	lines := make([]string, 0, len(r.Upstreams))
	seen := make(map[string]struct{}, len(r.Upstreams))
	for _, u := range r.Upstreams {
		if strings.ContainsAny(u.Name+u.URL+u.Identity, " \t\n") {
			return "", fmt.Errorf("upstream %q has whitespace in a field, so the set cannot be encoded unambiguously", u.Name)
		}
		// Refused for the same reason parseUpstreamSet refuses it, and reachable the same
		// way the whitespace above is: a name bound to two destinations at once is not a
		// set, so hashing it would produce an identity for something that has none.
		if _, dup := seen[u.Name]; dup {
			return "", fmt.Errorf("upstream %q appears twice, so this is not a set and has no identity to hash", u.Name)
		}
		seen[u.Name] = struct{}{}
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
