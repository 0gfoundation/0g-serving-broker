package attest

import (
	"fmt"
	"net/url"
	"runtime"
	"strings"
	"testing"
)

const testEngineImage = "lmsysorg/sglang@sha256:1111111111111111111111111111111111111111111111111111111111111111"

func engineLine(name, image, gpus, args string) string {
	return strings.Join([]string{name, image, gpus, args}, engineFieldSep)
}

func engineSet(lines ...string) string {
	return fmt.Sprintf("%s%d\n%s", upstreamCountPrefix, len(lines), strings.Join(lines, "\n"))
}

func TestParseEngineSet(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		want    []Engine
		wantErr string
	}{
		{
			name:    "the empty set",
			payload: "count=0",
			want:    nil,
		},
		{
			name:    "one engine",
			payload: engineSet(engineLine("dsv4flash", testEngineImage, "7", "--model-path deepseek-ai/DeepSeek-V4-Flash --tp 1")),
			want: []Engine{{
				Name: "dsv4flash", Image: testEngineImage, GPUs: "7",
				Args: "--model-path deepseek-ai/DeepSeek-V4-Flash --tp 1",
			}},
		},
		{
			// Arguments carry spaces, which is why the fields are tab-separated. A
			// space-separated grammar would make the field count depend on the arguments.
			name:    "arguments full of spaces do not change the field count",
			payload: engineSet(engineLine("e", testEngineImage, "0,1,2,3", "--a 1 --b 2 --c 'three words' --d")),
			want: []Engine{{
				Name: "e", Image: testEngineImage, GPUs: "0,1,2,3",
				Args: "--a 1 --b 2 --c 'three words' --d",
			}},
		},
		{
			name:    "an empty argument list",
			payload: engineSet(engineLine("e", testEngineImage, "0", "")),
			want:    []Engine{{Name: "e", Image: testEngineImage, GPUs: "0", Args: ""}},
		},
		{
			name: "two engines",
			payload: engineSet(
				engineLine("a", testEngineImage, "0", "--x"),
				engineLine("b", testEngineImage, "1", "--y"),
			),
			want: []Engine{
				{Name: "a", Image: testEngineImage, GPUs: "0", Args: "--x"},
				{Name: "b", Image: testEngineImage, GPUs: "1", Args: "--y"},
			},
		},

		// --- refusals ---
		{
			name:    "no header",
			payload: engineLine("a", testEngineImage, "0", "--x"),
			wantErr: "want a header",
		},
		{
			name:    "an empty payload",
			payload: "",
			wantErr: "nothing but whitespace",
		},
		{
			name:    "whitespace only",
			payload: "  \n\n \n",
			wantErr: "nothing but whitespace",
		},
		{
			name:    "the count is short",
			payload: "count=1\n" + engineLine("a", testEngineImage, "0", "--x") + "\n" + engineLine("b", testEngineImage, "1", "--y"),
			wantErr: "not the set it lists",
		},
		{
			name:    "the count is long",
			payload: "count=2\n" + engineLine("a", testEngineImage, "0", "--x"),
			wantErr: "not the set it lists",
		},
		{
			name:    "a negative count",
			payload: "count=-1",
			wantErr: "does not name an engine count",
		},
		{
			name:    "a count over the limit",
			payload: fmt.Sprintf("count=%d", maxUpstreamMembers+1),
			wantErr: "over the",
		},
		{
			name:    "three fields",
			payload: "count=1\n" + strings.Join([]string{"a", testEngineImage, "0"}, engineFieldSep),
			wantErr: "want 4",
		},
		{
			name:    "five fields",
			payload: "count=1\n" + strings.Join([]string{"a", testEngineImage, "0", "--x", "extra"}, engineFieldSep),
			wantErr: "want 4",
		},
		{
			// THE one that matters most. Without a digest the record names what was asked
			// for rather than what runs, and this record is the only account of an image
			// compose_hash does not cover — so a tag leaves nothing accountable.
			name:    "an image with a tag rather than a digest",
			payload: engineSet(engineLine("a", "lmsysorg/sglang:v0.5.18", "0", "--x")),
			wantErr: "does not pin a digest",
		},
		{
			name:    "an image with no repo",
			payload: engineSet(engineLine("a", "@sha256:"+strings.Repeat("1", 64), "0", "--x")),
			wantErr: "does not pin a digest",
		},
		{
			name:    "an image with a truncated digest",
			payload: engineSet(engineLine("a", "lmsysorg/sglang@sha256:abc", "0", "--x")),
			wantErr: "does not pin a digest",
		},
		{
			name:    "an image with an uppercase digest",
			payload: engineSet(engineLine("a", "lmsysorg/sglang@sha256:"+strings.Repeat("A", 64), "0", "--x")),
			wantErr: "does not pin a digest",
		},
		{
			// A name a URL host cannot spell could never be matched, so recording it would
			// describe an engine no destination can reach.
			name:    "a name with an uppercase letter",
			payload: engineSet(engineLine("Engine", testEngineImage, "0", "--x")),
			wantErr: "not a container name",
		},
		{
			name:    "a name with a dot",
			payload: engineSet(engineLine("engine.one", testEngineImage, "0", "--x")),
			wantErr: "not a container name",
		},
		{
			name:    "an empty name",
			payload: engineSet(engineLine("", testEngineImage, "0", "--x")),
			wantErr: "not a container name",
		},
		{
			name: "one name twice",
			payload: engineSet(
				engineLine("a", testEngineImage, "0", "--x"),
				engineLine("a", testEngineImage, "1", "--y"),
			),
			wantErr: "twice",
		},
		{
			name:    "a line over the cap",
			payload: engineSet(engineLine("a", testEngineImage, "0", strings.Repeat("x", maxUpstreamLine))),
			wantErr: "over the",
		},
		{
			// The header is the first line that holds anything, and it may not be a member
			// line that happens to start with the prefix. Without the tab test a writer
			// could name a container "count=1" and have its line read as the header —
			// swallowing one engine and leaving the tally to refuse the whole record for
			// the wrong reason.
			name:    "a member line masquerading as the header",
			payload: engineLine("count=1", testEngineImage, "0", "--x"),
			wantErr: "want a header",
		},
		{
			// A second header later is just a member line, and fails the field count.
			name:    "a second header line",
			payload: "count=1\n" + engineLine("a", testEngineImage, "0", "--x") + "\ncount=1",
			wantErr: "want 4",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseEngineSet(tc.payload)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("parseEngineSet(%q) = %+v, want a refusal", tc.payload, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error %q does not mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseEngineSet(%q) = %v", tc.payload, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d engines, want %d: %+v", len(got), len(tc.want), got)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("engine %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// What parseEngineSet allocates must be bounded by what it VALIDATED, not by what it
// received — the rule parseUpstreamSet settles, applied to the same untrusted input.
//
// Asserted as a budget rather than as the absence of a particular mistake, which is what
// the upstream parser's history earned: five forms of one bug, each fix growing the next.
// It is also what makes the in-loop tally load-bearing rather than decorative — without
// it a payload declaring count=0 and describing 200,000 engines builds every one of them
// before the post-loop tally refuses the record.
func TestParseEngineSetAllocationDoesNotScaleWithThePayload(t *testing.T) {
	const budget = 8 << 20

	// Distinct names, because a repeated one is refused at engine two and would measure
	// nothing — the fixture mistake that made the upstream version of this test useless.
	var b strings.Builder
	b.WriteString("count=0\n")
	for i := 0; b.Len() < 4<<20; i++ {
		b.WriteString(engineLine(fmt.Sprintf("n%d", i), testEngineImage, "0", "--x"))
		b.WriteString("\n")
	}
	payload := b.String()

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	got, err := parseEngineSet(payload)
	runtime.ReadMemStats(&after)

	if err == nil {
		t.Fatalf("count=0 with %d bytes of engines parsed to %+v, want a refusal", len(payload), got)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > budget {
		t.Errorf("refusing the record allocated %d bytes over a %d-byte budget: the cost is tracking the payload rather than what was validated", alloc, budget)
	}
}

// The empty set and an unreadable record must both come back as a nil slice, so nothing
// downstream can tell them apart by shape — EnginesState is the only thing that
// separates them, which is the point of it being the source of truth.
func TestParseEngineSetDistinguishesEmptyFromRefusedOnlyByError(t *testing.T) {
	empty, err := parseEngineSet("count=0")
	if err != nil || empty != nil {
		t.Fatalf("the empty set = %+v, %v; want nil, nil", empty, err)
	}
	refused, err := parseEngineSet("garbage")
	if err == nil || refused != nil {
		t.Fatalf("a refused record = %+v, %v; want nil and an error", refused, err)
	}
}

// A recorded engine must NEVER be reported the way a compose service is.
//
// This is the seam the whole ImageSource field exists for. A compose service is bound to
// the quote by app_compose hashing to the compose hash in the signed report body; a
// recorded engine is the CVM's own claim. A caller shown the same shape for both would
// treat the weaker as the stronger, which is the one thing this must not allow.
func TestARecordedEngineIsNeverReportedAsAComposeService(t *testing.T) {
	engines := []Engine{{Name: "dsv4flash", Image: testEngineImage, GPUs: "7", Args: "--tp 1"}}

	got := classifyUpstreams(
		[]Upstream{{Name: "m", URL: "http://dsv4flash:8000/v1"}},
		map[string]string{"other": "x@sha256:" + strings.Repeat("2", 64)},
		engines,
	)
	if got[0].ComposeService != "dsv4flash" {
		t.Errorf("ComposeService = %q, want the recorded container name", got[0].ComposeService)
	}
	if got[0].PinnedImage != testEngineImage {
		t.Errorf("PinnedImage = %q, want the recorded image", got[0].PinnedImage)
	}
	if got[0].ImageSource != ImageSourceRecord {
		t.Errorf("ImageSource = %q, want %q: a caller must be able to tell this from a compose service", got[0].ImageSource, ImageSourceRecord)
	}
}

// Compose wins on a name both sources claim, and the ImageSource says so.
//
// The other order would let a CVM that recorded an engine shadowing a compose service
// describe that service itself — replacing a hardware-bound answer with its own claim.
func TestComposeWinsOverARecordedEngine(t *testing.T) {
	const composeImage = "ghcr.io/example/real@sha256:" + "3333333333333333333333333333333333333333333333333333333333333333"

	got := classifyUpstreams(
		[]Upstream{{Name: "m", URL: "http://engine:8000/v1"}},
		map[string]string{"engine": composeImage},
		[]Engine{{Name: "engine", Image: testEngineImage, GPUs: "0", Args: ""}},
	)
	if got[0].PinnedImage != composeImage {
		t.Errorf("PinnedImage = %q, want the COMPOSE image %q: the record must not override it", got[0].PinnedImage, composeImage)
	}
	if got[0].ImageSource != ImageSourceCompose {
		t.Errorf("ImageSource = %q, want %q", got[0].ImageSource, ImageSourceCompose)
	}
}

// A compose service still reports ImageSourceCompose when engines are present, and an
// unmatched destination reports neither — so the field is never left over from a
// previous member.
func TestImageSourceIsSetExactlyWhenSomethingMatched(t *testing.T) {
	got := classifyUpstreams(
		[]Upstream{
			{Name: "a", URL: "http://composed:8000/v1"},
			{Name: "b", URL: "http://recorded:8000/v1"},
			{Name: "c", URL: "https://vendor.example/v1"},
		},
		map[string]string{"composed": "x@sha256:" + strings.Repeat("2", 64)},
		[]Engine{{Name: "recorded", Image: testEngineImage, GPUs: "0", Args: ""}},
	)
	for i, want := range []string{ImageSourceCompose, ImageSourceRecord, ""} {
		if got[i].ImageSource != want {
			t.Errorf("member %d ImageSource = %q, want %q", i, got[i].ImageSource, want)
		}
		if (got[i].ImageSource == "") != (got[i].PinnedImage == "") {
			t.Errorf("member %d has ImageSource=%q but PinnedImage=%q: one without the other", i, got[i].ImageSource, got[i].PinnedImage)
		}
	}
}

// The adversarial question, again, for the record-backed path: can a URL the writer
// chooses match a recorded engine name while addressing something else?
//
// The record is written by the same party that writes the upstream set, so both halves
// of the match are its own claim — but the two must still agree with each other. A
// destination classified as a recorded engine has to have a host that IS that engine's
// name, or the lookup matched something the host is not.
func TestNoExternalDestinationPassesForARecordedEngine(t *testing.T) {
	engines := []Engine{{Name: "api", Image: testEngineImage, GPUs: "0", Args: ""}}

	for _, raw := range []string{
		"http://api:9999/v1",
		"http://api.evil.example:80/v1",
		"http://api.:9999/v1",
		"http://API:9999/v1",
		"http://api%2eevil%2eexample/v1",
		"http://evil.example@api/v1",
		"http://api․evil.example/v1",
		"http://аpi:9999/v1",
		"http://xn--api-4va:9999/v1",
	} {
		t.Run(raw, func(t *testing.T) {
			if err := validUpstreamURL(raw); err != nil {
				return // refused before classification; it never becomes a member
			}
			got := classifyUpstreams([]Upstream{{Name: "m", URL: raw}}, nil, engines)
			if got[0].ComposeService == "" {
				return
			}
			parsed, perr := url.Parse(raw)
			if perr != nil {
				t.Fatalf("validUpstreamURL accepted a URL that does not parse: %v", perr)
			}
			if parsed.Hostname() != got[0].ComposeService {
				t.Errorf("classified as recorded engine %q but the host is %q", got[0].ComposeService, parsed.Hostname())
			}
		})
	}
}

// Two engines whose names collide once lowercased classify NEITHER, for the reason two
// compose services do: picking one would make up which container sees the plaintext.
//
// Unreachable through the resolver, because parseEngineSet refuses an uppercase name and
// therefore a duplicate-once-lowercased pair. Asserted on engineLookup directly, so the
// property holds for any caller rather than only for the one the parser feeds.
func TestAmbiguousEngineNamesClassifyNothing(t *testing.T) {
	got := classifyUpstreams(
		[]Upstream{{Name: "m", URL: "http://api:9999/v1"}},
		nil,
		[]Engine{
			{Name: "api", Image: testEngineImage, GPUs: "0", Args: ""},
			{Name: "API", Image: testEngineImage, GPUs: "1", Args: ""},
		},
	)
	if got[0].ComposeService != "" || got[0].ImageSource != "" {
		t.Errorf("an ambiguous engine name classified as %q/%q", got[0].ComposeService, got[0].ImageSource)
	}
}
