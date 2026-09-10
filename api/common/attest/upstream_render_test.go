package attest

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
)

// The property the whole design rests on: whatever RenderUpstreamSet accepts, the
// reader reads back as the same set. RenderUpstreamSet enforces this structurally by
// parsing its own output, so this test is not what makes it true — it is what catches
// the enforcement being removed, which a later "simplification" of the round-trip
// would do silently.
func TestRenderRoundTripsThroughTheReader(t *testing.T) {
	for _, tc := range []struct {
		name    string
		members []Upstream
	}{
		{"the empty set", nil},
		{"one in-CVM engine", []Upstream{
			{Name: "engine1", URL: "http://engine-1:8000/v1"},
		}},
		{"one vendor with an identity", []Upstream{
			{Name: "openrouter", URL: "https://openrouter.ai/api/v1", Identity: "openrouter"},
		}},
		{"a mix, given out of order", []Upstream{
			{Name: "tencent", URL: "https://tokenhub.tencentcloudmaas.com/v1", Identity: "tencent"},
			{Name: "engine1", URL: "http://engine-1:8000/v1"},
			{Name: "openrouter", URL: "https://openrouter.ai/api/v1", Identity: "openrouter"},
		}},
		{"a member with no path, which config really holds", []Upstream{
			{Name: "litellm", URL: "http://litellm:4000"},
		}},
		{"names using every character the pattern allows", []Upstream{
			{Name: "a", URL: "http://h1:1/v1"},
			{Name: "a-b_c9", URL: "http://h2:1/v1"},
			{Name: "z9", URL: "http://h3:1/v1"},
		}},
		// Name order and URL order DISAGREE here, on purpose. Every other case above
		// happens to have them coincide — "http://" sorts before "https://", so an in-CVM
		// engine named early also has the earliest URL — which made the sort assertion
		// below pass whether the sort keyed on the name or on the URL. A mutation swapping
		// the key survived until this case existed.
		{"a set where sorting by name and by URL differ", []Upstream{
			{Name: "aaa", URL: "https://zzz.example/v1"},
			{Name: "zzz", URL: "https://aaa.example/v1"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := RenderUpstreamSet(tc.members)
			if err != nil {
				t.Fatalf("RenderUpstreamSet(%+v) = %v", tc.members, err)
			}
			back, err := parseUpstreamSet(payload)
			if err != nil {
				t.Fatalf("the reader refused what the writer produced:\npayload %q\nerr %v", payload, err)
			}
			if len(back) != len(tc.members) {
				t.Fatalf("round trip gave %d members, want %d\npayload %q", len(back), len(tc.members), payload)
			}
			// Sorted by name on the way out, so compare against the same order rather than
			// the order the case listed.
			want := map[string]Upstream{}
			for _, u := range tc.members {
				want[u.Name] = u
			}
			for _, got := range back {
				w, ok := want[got.Name]
				if !ok {
					t.Errorf("round trip produced an upstream nobody asked for: %+v", got)
					continue
				}
				if got.Name != w.Name || got.URL != w.URL || got.Identity != w.Identity {
					t.Errorf("round trip changed %q: %q %q -> %q %q", w.Name, w.URL, w.Identity, got.URL, got.Identity)
				}
			}
			for i := 1; i < len(back); i++ {
				if back[i-1].Name >= back[i].Name {
					t.Errorf("members came back unsorted at %d: %q then %q", i, back[i-1].Name, back[i].Name)
				}
			}
		})
	}
}

// ValidUpstreamName must answer for exactly the strings a record's name field accepts,
// because a writer deriving a name asks it BEFORE it has a set to render — so a
// disagreement here surfaces as a set that reads as unknown, not as a local refusal.
func TestValidUpstreamNameAgreesWithTheRecord(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"engine1", true},
		{"a", true},
		{"0gm-sglang", true},
		{"phala-inference-guard", true},
		{"qwavity-sia-vllm", true},
		{"api", true},
		{"vllm", true},
		{"a_b-c9", true},
		{strings.Repeat("a", 63), true},
		{"", false},
		{strings.Repeat("a", 64), false},
		{"openrouter.ai", false}, // a dotted FQDN, which is why an identity is required there
		{"Engine1", false},
		{"-engine", false},
		{"_engine", false},
		{"engine 1", false},
		{"engine=1", false},
		{"еngine1", false}, // Cyrillic first letter
	} {
		t.Run(fmt.Sprintf("%q", tc.name), func(t *testing.T) {
			if got := ValidUpstreamName(tc.name); got != tc.want {
				t.Fatalf("ValidUpstreamName(%q) = %v, want %v", tc.name, got, tc.want)
			}
			// And the record itself must agree, which is the property that matters: a name
			// this accepts has to survive a render, and one it rejects has to be refused.
			_, err := RenderUpstreamSet([]Upstream{{Name: tc.name, URL: "http://h:1/v1"}})
			if tc.want && err != nil {
				t.Errorf("ValidUpstreamName accepted %q but the record refused it: %v", tc.name, err)
			}
			if !tc.want && err == nil {
				t.Errorf("ValidUpstreamName rejected %q but the record accepted it", tc.name)
			}
		})
	}
}

// The exact bytes, pinned.
//
// The round-trip cannot pin them: strings.Fields collapses any run of whitespace, so a
// tab between the fields, or a trailing space where an identity would go, reads back as
// the same set and passes every property above — two mutations survived on exactly that.
//
// The bytes still matter. They are what extends RTMR3, so the same set spelled two ways
// produces two digests and two records; and once a second implementation exists — a
// router or an SDK rendering this set to compare against a CVM's — it has to produce
// these bytes and not merely an equivalent set.
//
// One space between fields, one newline between lines, no trailing newline, and nothing
// at all where an absent identity would be.
func TestRenderedBytesAreExactly(t *testing.T) {
	payload, err := RenderUpstreamSet([]Upstream{
		{Name: "vendor", URL: "https://v.example/v1", Identity: "openrouter"},
		{Name: "engine1", URL: "http://engine-1:8000/v1"},
	})
	if err != nil {
		t.Fatalf("RenderUpstreamSet() = %v", err)
	}
	want := "count=2\n" +
		"engine1 http://engine-1:8000/v1\n" +
		"vendor https://v.example/v1 openrouter"
	if payload != want {
		t.Errorf("payload bytes differ\n got %q\nwant %q", payload, want)
	}
}

// The empty set has exactly one spelling, and it is not an empty payload — that
// distinction is the whole reason UpstreamsUnrecorded and an empty UpstreamsKnown are
// different states, so the writer must not be able to blur it.
func TestRenderTheEmptySetAsTheCountAndNothingElse(t *testing.T) {
	payload, err := RenderUpstreamSet(nil)
	if err != nil {
		t.Fatalf("RenderUpstreamSet(nil) = %v", err)
	}
	if payload != upstreamCountPrefix+"0" {
		t.Fatalf("the empty set rendered as %q, want %q", payload, upstreamCountPrefix+"0")
	}
	set, err := parseUpstreamSet(payload)
	if err != nil {
		t.Fatalf("the reader refused the empty set: %v", err)
	}
	if set != nil {
		t.Errorf("the empty set read back as %+v, want nil", set)
	}
}

// Every refusal here is the READER's refusal, surfaced before EmitEvent. If any of
// these started to render, a CVM would extend RTMR3 with a record that cannot be read
// and cannot be withdrawn for the rest of the boot.
func TestRenderRefusesWhatTheReaderWouldRefuse(t *testing.T) {
	for _, tc := range []struct {
		name    string
		members []Upstream
		wantErr string
	}{
		{"a name spelled twice", []Upstream{
			{Name: "vendor", URL: "https://a.example/v1"},
			{Name: "vendor", URL: "https://b.example/v1"},
		}, "twice"},
		{"an uppercase name", []Upstream{
			{Name: "Vendor", URL: "https://a.example/v1"},
		}, "lowercase"},
		{"an empty name", []Upstream{
			{Name: "", URL: "https://a.example/v1"},
		}, ""},
		{"a name with a space", []Upstream{
			{Name: "two words", URL: "https://a.example/v1"},
		}, ""},
		{"a URL carrying credentials", []Upstream{
			{Name: "vendor", URL: "https://user:pw@a.example/v1"},
		}, "credentials"},
		{"a URL carrying an API key in the query", []Upstream{
			{Name: "vendor", URL: "https://a.example/v1?key=sk-live"},
		}, "query"},
		{"an uppercase host", []Upstream{
			{Name: "vendor", URL: "https://A.example/v1"},
		}, "uppercase host"},
		{"the scheme's default port", []Upstream{
			{Name: "vendor", URL: "https://a.example:443/v1"},
		}, "default port"},
		{"a trailing slash", []Upstream{
			{Name: "vendor", URL: "https://a.example/v1/"},
		}, "slash"},
		{"a dot segment", []Upstream{
			{Name: "vendor", URL: "https://a.example/v1/../v2"},
		}, "dot segment"},
		{"a non-canonical address", []Upstream{
			{Name: "vendor", URL: "https://010.0.0.1:8000/v1"},
		}, ""},
		{"a non-ASCII host", []Upstream{
			{Name: "vendor", URL: "https://еngine-1:8000/v1"},
		}, ""},
		{"an identity the pattern does not admit", []Upstream{
			{Name: "vendor", URL: "https://a.example/v1", Identity: "Open_Router"},
		}, "identity"},
		{"an empty URL", []Upstream{
			{Name: "vendor", URL: ""},
		}, ""},
		{"whitespace inside a field, which would break the framing", []Upstream{
			{Name: "vendor", URL: "https://a.example/v1 extra"},
		}, ""},
		{"a line over the line cap", []Upstream{
			{Name: "vendor", URL: "https://a.example/" + strings.Repeat("p", maxUpstreamLine)},
		}, ""},
		{"more members than a set may hold", func() []Upstream {
			out := make([]Upstream, 0, maxUpstreamMembers+1)
			for i := 0; i <= maxUpstreamMembers; i++ {
				out = append(out, Upstream{Name: fmt.Sprintf("n%d", i), URL: fmt.Sprintf("http://h%d:1/v1", i)})
			}
			return out
		}(), "at most"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := RenderUpstreamSet(tc.members)
			if err == nil {
				t.Fatalf("rendered %q, want a refusal", payload)
			}
			if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

// What RenderUpstreamSet allocates must be bounded by what it has VALIDATED, not by
// what it was handed — the same rule parseUpstreamSet settles, applied to its writer.
//
// Asserted as a budget rather than as the absence of a particular mistake, because the
// mistake this caught is the shape that keeps coming back: round-tripping bounds what
// the writer ACCEPTS but not what it costs to produce the thing being refused. Building
// the payload first and letting the parse refuse its first line allocated 5.37 GB on
// the case below, which on a CVM is the controller OOMing at boot.
//
// The input is a config file rather than an RTMR3 payload, so this is an operator's
// mistake and not an attacker's — a targetUrl field holding a pasted file. It still
// takes the deployment down, and a caller of an exported function is entitled to a
// refusal that costs less than the thing it refuses.
func TestRenderAllocationDoesNotScaleWithTheInput(t *testing.T) {
	// A megabyte per member across a full set: 1 GiB of input, all of it refusable on the
	// first line. The budget is generous on purpose — it is not measuring a figure, it is
	// asserting that the cost does not track the input, and the mistake missed it by four
	// orders of magnitude.
	const budget = 8 << 20
	pad := strings.Repeat("p", 1<<20)
	members := make([]Upstream, 0, maxUpstreamMembers)
	for i := 0; i < maxUpstreamMembers; i++ {
		members = append(members, Upstream{
			Name: fmt.Sprintf("n%d", i),
			URL:  fmt.Sprintf("http://h%d:1/%s", i, pad),
		})
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	payload, err := RenderUpstreamSet(members)
	runtime.ReadMemStats(&after)

	if err == nil {
		t.Fatalf("rendered a %d-byte payload from 1 MiB member lines, want a refusal", len(payload))
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > budget {
		t.Errorf("refusing the set allocated %d bytes, over the %d-byte budget: the cost is tracking the input rather than what was validated", alloc, budget)
	}
}

// The line cap's boundary, and WHICH guard enforces it.
//
// The budget test above passes whether the arithmetic here is exact or a couple of bytes
// out — being off by the separators over-builds by 4 KB, not by a gigabyte — so two
// mutations that miscounted the line survived it. What separates them is who refuses: a
// line this check gets right is refused here, naming the member, while one it
// under-counts slips through and is refused by the parse, naming a payload the operator
// never wrote.
//
// So the assertions are on the error text, and a line at exactly the cap has to render.
func TestRenderLineCapBoundaryIsEnforcedHere(t *testing.T) {
	// name(1) + " "(1) + url = maxUpstreamLine exactly.
	atCap := Upstream{Name: "n", URL: "http://h/" + strings.Repeat("p", maxUpstreamLine-2-len("http://h/"))}
	if got := len(atCap.Name) + 1 + len(atCap.URL); got != maxUpstreamLine {
		t.Fatalf("the fixture renders %d bytes, want exactly %d", got, maxUpstreamLine)
	}
	if _, err := RenderUpstreamSet([]Upstream{atCap}); err != nil {
		t.Errorf("a line of exactly %d bytes was refused: %v", maxUpstreamLine, err)
	}

	// One byte over, in each of the three fields that make up the line, so a miscount of
	// any one of them is caught.
	for _, tc := range []struct {
		name string
		u    Upstream
	}{
		{"one byte over in the URL", Upstream{Name: "n", URL: atCap.URL + "p"}},
		{"one byte over in the name", Upstream{Name: "nn", URL: atCap.URL}},
		{"one byte over in the identity", Upstream{
			Name: "n",
			URL:  atCap.URL[:len(atCap.URL)-2],
			// name(1) + " "(1) + url(cap-4) + " "(1) + identity(2) = cap+1
			Identity: "ab",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := RenderUpstreamSet([]Upstream{tc.u})
			if err == nil {
				t.Fatal("a line over the cap rendered")
			}
			// "upstream %q renders a …-byte line" is this check; "payload has a …-byte line"
			// is the parse. Reaching the parse means the line was built first.
			if !strings.Contains(err.Error(), "renders a") {
				t.Errorf("refused by the parse rather than before the build, so the line was built: %v", err)
			}
		})
	}
}

// A set at the cap must still render, or the cap is off by one and the refusal above
// is testing the wrong boundary.
func TestRenderAcceptsExactlyTheCap(t *testing.T) {
	members := make([]Upstream, 0, maxUpstreamMembers)
	for i := 0; i < maxUpstreamMembers; i++ {
		members = append(members, Upstream{Name: fmt.Sprintf("n%d", i), URL: fmt.Sprintf("http://h%d:1/v1", i)})
	}
	payload, err := RenderUpstreamSet(members)
	if err != nil {
		t.Fatalf("a set of exactly %d was refused: %v", maxUpstreamMembers, err)
	}
	back, err := parseUpstreamSet(payload)
	if err != nil {
		t.Fatalf("the reader refused a set of exactly %d: %v", maxUpstreamMembers, err)
	}
	if len(back) != maxUpstreamMembers {
		t.Errorf("read back %d members, want %d", len(back), maxUpstreamMembers)
	}
}

// The caller's slice is not reordered, because a caller that renders and then reports
// its own list would otherwise see it silently rearranged.
func TestRenderLeavesTheCallersSliceAlone(t *testing.T) {
	members := []Upstream{
		{Name: "zzz", URL: "http://h1:1/v1"},
		{Name: "aaa", URL: "http://h2:1/v1"},
	}
	if _, err := RenderUpstreamSet(members); err != nil {
		t.Fatalf("RenderUpstreamSet() = %v", err)
	}
	if members[0].Name != "zzz" || members[1].Name != "aaa" {
		t.Errorf("the caller's slice was reordered: %+v", members)
	}
}

// A record the writer produces must hash to what the reader hashes it to — the hash is
// destined for the signing key's derivation path, so a writer and a reader disagreeing
// about it would derive two keys for one deployment.
func TestRenderedSetHashesToTheSameValueTheReaderComputes(t *testing.T) {
	members := []Upstream{
		{Name: "openrouter", URL: "https://openrouter.ai/api/v1", Identity: "openrouter"},
		{Name: "engine1", URL: "http://engine-1:8000/v1"},
	}
	payload, err := RenderUpstreamSet(members)
	if err != nil {
		t.Fatalf("RenderUpstreamSet() = %v", err)
	}
	parsed, err := parseUpstreamSet(payload)
	if err != nil {
		t.Fatalf("parseUpstreamSet() = %v", err)
	}
	fromRecord, err := (&RunningState{Upstreams: parsed, UpstreamsState: UpstreamsKnown}).UpstreamSetHash()
	if err != nil {
		t.Fatalf("hash of the parsed set: %v", err)
	}
	fromWriter, err := (&RunningState{Upstreams: members, UpstreamsState: UpstreamsKnown}).UpstreamSetHash()
	if err != nil {
		t.Fatalf("hash of the writer's own list: %v", err)
	}
	if fromRecord != fromWriter {
		t.Errorf("the writer's set hashes to %s but the record reads back as %s", fromWriter, fromRecord)
	}
}

// Re-emitting an unchanged set must produce the SAME bytes, whatever order the caller
// assembled it in. RTMR3 is cleared at every boot so a writer re-emits its table every
// time, and a payload that varied would extend the register with a different digest
// and read as a change that did not happen.
func TestRenderIsAFunctionOfTheSetNotOfTheOrder(t *testing.T) {
	a := []Upstream{
		{Name: "engine1", URL: "http://engine-1:8000/v1"},
		{Name: "vendor", URL: "https://v.example/v1", Identity: "openrouter"},
	}
	b := []Upstream{a[1], a[0]}
	pa, err := RenderUpstreamSet(a)
	if err != nil {
		t.Fatalf("RenderUpstreamSet(a) = %v", err)
	}
	pb, err := RenderUpstreamSet(b)
	if err != nil {
		t.Fatalf("RenderUpstreamSet(b) = %v", err)
	}
	if pa != pb {
		t.Errorf("the same set rendered two ways:\n%q\n%q", pa, pb)
	}
}

// Every distinct service.targetUrl / modelPricing[].targetUrl in the 32 mainnet
// deployments as of 2026-09-09.
//
// This is the evidence that a writer needs no normalising step: config's grammar is
// looser than validUpstreamURL's, but nothing deployed has used that headroom, so
// validating and refusing covers the real fleet while a normaliser would be
// speculative code on the one path where a bug records the wrong destination silently.
//
// It is also the regression that fires the other way. If someone tightens
// validUpstreamURL and one of these stops passing, a live deployment's set would start
// reading as unknown — this test says so here rather than in production.
func TestEveryRealDeploymentURLPassesVerbatim(t *testing.T) {
	real := []string{
		"http://0g-minimax-video-translator:8090",
		"http://0g-seedance-video-translator:8090",
		"http://0gm-sglang:8000/v1",
		"http://api:9999/v1",
		"http://litellm:4000",
		"http://phala-inference-guard:8000/v1",
		"http://qwavity-sia-vllm:8000/v1",
		"http://vllm:8000/v1",
		"https://0g.athenaai.ac/v1",
		"https://api.dgrid.ai/v1",
		"https://api.minimax.io/v1",
		"https://api.moonshot.ai/v1",
		"https://api.red-pill.ai/v1",
		"https://open.bigmodel.cn/api/paas/v4",
		"https://openrouter.ai/api/v1",
		"https://tokenhub.tencentcloudmaas.com/v1",
		"https://ws-6782hxwe9gchykwn.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1",
		"https://ws-ck3ccdg8ka3n5uvy.cn-beijing.maas.aliyuncs.com/compatible-mode/v1",
	}
	// Rendered as one set, not one at a time, so this also covers the whole fleet's URLs
	// coexisting under the line and member caps.
	members := make([]Upstream, 0, len(real))
	for i, u := range real {
		if err := validUpstreamURL(u); err != nil {
			t.Errorf("a deployed targetUrl is refused as written:\n  %s\n  %v", u, err)
		}
		members = append(members, Upstream{Name: fmt.Sprintf("u%d", i), URL: u})
	}
	if _, err := RenderUpstreamSet(members); err != nil {
		t.Errorf("the fleet's URLs cannot be recorded as one set: %v", err)
	}
}
