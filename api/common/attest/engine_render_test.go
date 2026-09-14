package attest

import (
	"runtime"
	"strings"
	"testing"
)

const (
	renderImage  = "lmsysorg/sglang@sha256:fc458e79e0b1c2d3e4f50617283940a1b2c3d4e5f60718293a4b5c6d7e8f9012"
	renderImage2 = "vllm/vllm-openai@sha256:0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
)

// The property RenderEngineSet exists to guarantee: whatever it accepts, the reader
// reads back as the same set. It enforces that structurally by parsing its own output,
// so this test is not what makes it true — it is what catches the enforcement being
// removed, which a later "simplification" of the round trip would do silently.
func TestRenderEngineSetRoundTripsThroughTheReader(t *testing.T) {
	for _, tc := range []struct {
		name    string
		engines []Engine
	}{
		{"the empty set, which is still written out", nil},
		{"one engine", []Engine{
			{Name: "glm53", Image: renderImage, GPUs: "all", Args: "--model-path zai-org/GLM-5.3 --tp 8"},
		}},
		// The field that makes the grammar tab-separated rather than space-separated: an
		// argument list is full of spaces, so a space grammar would make the field count
		// depend on the arguments.
		{"an argument list with many spaces", []Engine{
			{Name: "a", Image: renderImage, GPUs: "0", Args: "--a 1 --b 2 --c 3 --d 4 --e 5"},
		}},
		{"no arguments at all, which an entrypoint-configured image has", []Engine{
			{Name: "a", Image: renderImage, GPUs: "0", Args: ""},
		}},
		{"an empty GPU list, which a container with no cards has", []Engine{
			{Name: "a", Image: renderImage, GPUs: "", Args: "--x"},
		}},
		{"names using every character the pattern allows", []Engine{
			{Name: "a", Image: renderImage, GPUs: "0", Args: "--x"},
			{Name: "a-b_c9", Image: renderImage, GPUs: "1", Args: "--x"},
			{Name: "z9", Image: renderImage, GPUs: "2", Args: "--x"},
		}},
		// Name order and GPU order DISAGREE here, on purpose: a sort keyed on anything
		// but the name would still produce a payload the reader accepts, and every case
		// above would pass. This one would not.
		{"given out of order, with the name order disagreeing with every other field", []Engine{
			{Name: "zulu", Image: renderImage, GPUs: "0", Args: "--a"},
			{Name: "alpha", Image: renderImage2, GPUs: "7", Args: "--z"},
			{Name: "mike", Image: renderImage, GPUs: "3", Args: "--m"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := RenderEngineSet(tc.engines)
			if err != nil {
				t.Fatalf("RenderEngineSet() = %v, want nil", err)
			}
			back, err := parseEngineSet(payload)
			if err != nil {
				t.Fatalf("parseEngineSet(%q) = %v, want nil", payload, err)
			}
			if len(back) != len(tc.engines) {
				t.Fatalf("read back %d engines from %q, want %d", len(back), payload, len(tc.engines))
			}
			// Compared against the SORTED input rather than the input as given, because
			// the sort is part of the contract: the payload is a function of the set, not
			// of the order a caller assembled it in.
			want := sortedByName(tc.engines)
			for i := range want {
				if back[i] != want[i] {
					t.Errorf("engine %d read back as %+v, want %+v", i, back[i], want[i])
				}
			}
		})
	}
}

func TestRenderEngineSetSortsByName(t *testing.T) {
	payload, err := RenderEngineSet([]Engine{
		{Name: "zulu", Image: renderImage, GPUs: "0", Args: "--a"},
		{Name: "alpha", Image: renderImage, GPUs: "1", Args: "--b"},
	})
	if err != nil {
		t.Fatalf("RenderEngineSet() = %v", err)
	}
	// Exact bytes, because this is the one property a round trip cannot check: the
	// reader accepts either order, so only the literal payload shows the sort happened.
	want := "count=2\nalpha\t" + renderImage + "\t1\t--b\nzulu\t" + renderImage + "\t0\t--a"
	if payload != want {
		t.Errorf("payload =\n%q\nwant\n%q", payload, want)
	}
}

func TestRenderEngineSetDoesNotReorderTheCallersSlice(t *testing.T) {
	engines := []Engine{
		{Name: "zulu", Image: renderImage, GPUs: "0", Args: "--a"},
		{Name: "alpha", Image: renderImage, GPUs: "1", Args: "--b"},
	}
	if _, err := RenderEngineSet(engines); err != nil {
		t.Fatalf("RenderEngineSet() = %v", err)
	}
	if engines[0].Name != "zulu" {
		t.Errorf("the caller's slice was reordered to %v; the controller reads it after rendering", engines)
	}
}

func TestRenderEngineSetRefusals(t *testing.T) {
	long := strings.Repeat("x", maxUpstreamLine)

	for _, tc := range []struct {
		name    string
		engines []Engine
		want    string
	}{{
		// The refusal the whole record rests on: a tag names what was asked for, not
		// what arrived, and this record is the only account of an image compose_hash
		// does not cover.
		name:    "an image pinned by tag",
		engines: []Engine{{Name: "a", Image: "lmsysorg/sglang:v0.5.18", GPUs: "0"}},
		want:    "does not pin a digest",
	}, {
		name:    "an image with no digest",
		engines: []Engine{{Name: "a", Image: "lmsysorg/sglang", GPUs: "0"}},
		want:    "does not pin a digest",
	}, {
		name:    "a name no URL host could match",
		engines: []Engine{{Name: "A", Image: renderImage, GPUs: "0"}},
		want:    "not a container name a URL host could match",
	}, {
		name:    "no name at all",
		engines: []Engine{{Name: "", Image: renderImage, GPUs: "0"}},
		want:    "not a container name a URL host could match",
	}, {
		name: "the same name twice",
		engines: []Engine{
			{Name: "a", Image: renderImage, GPUs: "0"},
			{Name: "a", Image: renderImage2, GPUs: "1"},
		},
		want: "twice",
	}, {
		// Each of the four fields, because a check on one of them is not a check on the
		// others and the separator is what decides the field boundaries.
		name:    "a tab in the name",
		engines: []Engine{{Name: "a\tb", Image: renderImage, GPUs: "0"}},
		want:    "tab or newline in its name",
	}, {
		name:    "a tab in the GPU list",
		engines: []Engine{{Name: "a", Image: renderImage, GPUs: "0\t1"}},
		want:    "tab or newline in its GPU list",
	}, {
		name:    "a tab in the argument list",
		engines: []Engine{{Name: "a", Image: renderImage, GPUs: "0", Args: "--x\ty"}},
		want:    "tab or newline in its argument list",
	}, {
		name:    "a newline in the argument list, which would split the record's line",
		engines: []Engine{{Name: "a", Image: renderImage, GPUs: "0", Args: "--x\ncount=9"}},
		want:    "tab or newline in its argument list",
	}, {
		name:    "a carriage return in the argument list",
		engines: []Engine{{Name: "a", Image: renderImage, GPUs: "0", Args: "--x\r"}},
		want:    "tab or newline in its argument list",
	}, {
		name:    "an argument list over the line cap",
		engines: []Engine{{Name: "a", Image: renderImage, GPUs: "0", Args: long}},
		want:    "-byte limit",
	}, {
		name:    "more engines than the grammar holds",
		engines: tooManyEngines(),
		want:    "holds at most",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := RenderEngineSet(tc.engines)
			if err == nil {
				t.Fatalf("RenderEngineSet() = %q, want a refusal", payload)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("RenderEngineSet() = %v, want it to mention %q", err, tc.want)
			}
			if payload != "" {
				t.Errorf("RenderEngineSet() returned %q alongside its refusal; a caller that ignored the error would record it", payload)
			}
		})
	}
}

// The member cap has to be checked BEFORE the payload is built, not left to the parse:
// rendering is the one place a caller's own slice length sizes the work, and a cap
// enforced afterwards means paying for the whole payload to earn a refusal about it.
//
// A budget rather than an assertion about one particular mistake, which is the rule the
// upstream renderer earned across five successive forms of one bug.
func TestRenderEngineSetRefusesAnOversizedSetCheaply(t *testing.T) {
	engines := make([]Engine, maxUpstreamMembers*4)
	for i := range engines {
		engines[i] = Engine{Name: "a", Image: renderImage, GPUs: "0", Args: strings.Repeat("x", 1024)}
	}

	const budget = 1 << 20 // against a set whose payload would be ~4 MiB

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	if _, err := RenderEngineSet(engines); err == nil {
		t.Fatal("this set rendered, so the budget below is measuring the wrong path")
	}
	runtime.ReadMemStats(&after)

	if used := after.TotalAlloc - before.TotalAlloc; used > budget {
		t.Errorf("refusing %d engines allocated %d bytes, over the %d-byte budget: the cap is being enforced after the payload is built rather than before", len(engines), used, budget)
	}
}

func tooManyEngines() []Engine {
	out := make([]Engine, maxUpstreamMembers+1)
	for i := range out {
		out[i] = Engine{Name: "a", Image: renderImage, GPUs: "0"}
	}
	return out
}

func sortedByName(engines []Engine) []Engine {
	out := make([]Engine, len(engines))
	copy(out, engines)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Name < out[j-1].Name; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// The per-line cap has to be checked before the LINE is built, for the same reason the
// member cap is checked before the loop — and this is the half the upstream renderer got
// wrong first, at a measured 5.37 GB to produce a refusal about one line.
//
// A budget rather than an assertion about the message, because the parse refuses an
// oversized line too: the refusal reads identically whichever check produced it, so only
// what it COST distinguishes them. That is the same reasoning the upstream renderer's
// budget carries.
func TestRenderEngineSetRefusesAnOversizedLineCheaply(t *testing.T) {
	// One member, so the member cap cannot be what refuses this. An argument list of
	// 64 MiB is a config field holding a pasted file, not an attack.
	engines := []Engine{{Name: "a", Image: renderImage, GPUs: "0", Args: strings.Repeat("x", 64<<20)}}

	const budget = 1 << 20

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	if _, err := RenderEngineSet(engines); err == nil {
		t.Fatal("this set rendered, so the budget below is measuring the wrong path")
	}
	runtime.ReadMemStats(&after)

	if used := after.TotalAlloc - before.TotalAlloc; used > budget {
		t.Errorf("refusing a %d-byte argument list allocated %d bytes, over the %d-byte budget: the line cap is being enforced after the line is built rather than before",
			len(engines[0].Args), used, budget)
	}
}
