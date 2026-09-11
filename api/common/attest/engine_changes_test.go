package attest

import (
	"strings"
	"testing"
)

const otherEngineImage = "lmsysorg/sglang@sha256:2222222222222222222222222222222222222222222222222222222222222222"

func TestEngineChanges(t *testing.T) {
	a := Engine{Name: "a", Image: testEngineImage, GPUs: "0", Args: "--x"}
	b := Engine{Name: "b", Image: testEngineImage, GPUs: "1", Args: "--y"}

	for _, tc := range []struct {
		name string
		prev []Engine
		next []Engine
		want []string
	}{
		{
			// The ordinary case: a writer re-emitting its unchanged table at every boot
			// must produce nothing, or the log would be noise a reader learns to ignore.
			name: "an unchanged set reports nothing",
			prev: []Engine{a, b},
			next: []Engine{a, b},
			want: nil,
		},
		{
			name: "order alone is not a change",
			prev: []Engine{a, b},
			next: []Engine{b, a},
			want: nil,
		},
		{
			name: "a created engine",
			prev: []Engine{a},
			next: []Engine{a, b},
			want: []string{"b: created as " + testEngineImage + " on GPU 1"},
		},
		{
			// THE case this exists for. The final state shows only the second image; the
			// plaintext went to the first.
			name: "an engine's image replaced under the same name",
			prev: []Engine{a},
			next: []Engine{{Name: "a", Image: otherEngineImage, GPUs: "0", Args: "--x"}},
			want: []string{"a: " + testEngineImage + " on GPU 0 -> " + otherEngineImage + " on GPU 0"},
		},
		{
			name: "an engine moved to another GPU",
			prev: []Engine{a},
			next: []Engine{{Name: "a", Image: testEngineImage, GPUs: "3", Args: "--x"}},
			want: []string{"a: " + testEngineImage + " on GPU 0 -> " + testEngineImage + " on GPU 3"},
		},
		{
			// Arguments are compared but not printed — the line's existence is the signal,
			// and a reader who needs them has Engines.
			name: "an argument change still reports a line",
			prev: []Engine{a},
			next: []Engine{{Name: "a", Image: testEngineImage, GPUs: "0", Args: "--x --more"}},
			want: []string{"a: " + testEngineImage + " on GPU 0 -> " + testEngineImage + " on GPU 0"},
		},
		{
			// The other fail-open direction: what remains reads as though it was always
			// the whole set.
			name: "a removed engine",
			prev: []Engine{a, b},
			next: []Engine{a},
			want: []string{"b: " + testEngineImage + " on GPU 1 -> removed"},
		},
		{
			name: "everything removed",
			prev: []Engine{a, b},
			next: nil,
			want: []string{
				"a: " + testEngineImage + " on GPU 0 -> removed",
				"b: " + testEngineImage + " on GPU 1 -> removed",
			},
		},
		{
			// This function reports a creation; the RESOLVER does not call it for the first
			// record of a boot, which is what makes an unchanged first record silent. The
			// two facts live in different places and the tests for them do too — see
			// TestResolveReportsNoEngineChangeForARepeatedRecord.
			name: "a set built from no baseline reports every engine as created",
			prev: nil,
			next: []Engine{a},
			want: []string{"a: created as " + testEngineImage + " on GPU 0"},
		},
		{
			name: "an empty GPU list is spelled out",
			prev: []Engine{{Name: "a", Image: testEngineImage, GPUs: "", Args: ""}},
			next: []Engine{{Name: "a", Image: otherEngineImage, GPUs: "", Args: ""}},
			want: []string{"a: " + testEngineImage + " on GPU (none) -> " + otherEngineImage + " on GPU (none)"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := engineChanges(tc.prev, tc.next)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d lines, want %d:\n got %q\nwant %q", len(got), len(tc.want), got, tc.want)
			}
			for i := range tc.want {
				if !strings.Contains(got[i], tc.want[i]) {
					t.Errorf("line %d = %q, want it to contain %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// The removal lines come out of a map, so without the sort the same pair of snapshots
// would report a different order on every run — and a caller diffing two verifications,
// or logging the lines, would see changes that did not happen.
func TestEngineChangesAreOrderedDeterministically(t *testing.T) {
	prev := []Engine{
		{Name: "z", Image: testEngineImage, GPUs: "0"},
		{Name: "a", Image: testEngineImage, GPUs: "1"},
		{Name: "m", Image: testEngineImage, GPUs: "2"},
	}
	first := engineChanges(prev, nil)
	for range 40 {
		again := engineChanges(prev, nil)
		if len(again) != len(first) {
			t.Fatalf("line count varied: %d vs %d", len(again), len(first))
		}
		for i := range first {
			if again[i] != first[i] {
				t.Fatalf("order varied at %d: %q vs %q", i, again[i], first[i])
			}
		}
	}
	for i := 1; i < len(first); i++ {
		if first[i-1] >= first[i] {
			t.Errorf("lines are not sorted at %d: %q then %q", i, first[i-1], first[i])
		}
	}
}

// Through the resolver: a swapped image must appear in EngineChanges even though the
// final set shows only the new one.
func TestResolveReportsAnEngineImageSwap(t *testing.T) {
	compose := pinnedCompose(t)
	events := append(bootEvents(),
		engineSetEvent(engineLine("a", testEngineImage, "0", "--x")),
		engineSetEvent(engineLine("a", otherEngineImage, "0", "--x")),
	)

	state, err := resolve(t, compose, events)
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v", err)
	}
	if len(state.Engines) != 1 || state.Engines[0].Image != otherEngineImage {
		t.Fatalf("Engines = %+v, want the last record's image", state.Engines)
	}
	if len(state.EngineChanges) != 1 {
		t.Fatalf("EngineChanges = %q, want one line naming the swap", state.EngineChanges)
	}
	if !strings.Contains(state.EngineChanges[0], testEngineImage) {
		t.Errorf("EngineChanges = %q, want it to name the image that was REPLACED — the final state already shows the other", state.EngineChanges)
	}
}

// A writer re-emitting its unchanged table at every boot must leave the log empty.
func TestResolveReportsNoEngineChangeForARepeatedRecord(t *testing.T) {
	compose := pinnedCompose(t)
	rec := engineSetEvent(engineLine("a", testEngineImage, "0", "--x"))
	state, err := resolve(t, compose, append(bootEvents(), rec, rec, rec))
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v", err)
	}
	if len(state.EngineChanges) != 0 {
		t.Errorf("EngineChanges = %q, want empty for a repeated record", state.EngineChanges)
	}
}

// A swap that STRADDLES an unreadable record must still be reported.
//
// The baseline is the last set that read, not state.Engines, which the unreadable record
// cleared. Otherwise writing garbage and then a rewritten set would suppress the change
// across the garbage — and the garbage is written by whoever writes the records, so that
// would be a way to hide exactly what this log exists to show.
func TestResolveReportsAnEngineSwapStraddlingAnUnreadableRecord(t *testing.T) {
	compose := pinnedCompose(t)
	events := append(bootEvents(),
		engineSetEvent(engineLine("a", testEngineImage, "0", "--x")),
		RuntimeEvent{Event: EventEngineSet, Payload: []byte("garbage")},
		engineSetEvent(engineLine("a", otherEngineImage, "0", "--x")),
	)

	state, err := resolve(t, compose, events)
	if err != nil {
		t.Fatalf("ResolveRunningState() = %v", err)
	}
	var swap, superseded bool
	for _, l := range state.EngineChanges {
		if strings.Contains(l, "could not be read") {
			superseded = true
		}
		if strings.Contains(l, testEngineImage) && strings.Contains(l, otherEngineImage) {
			swap = true
		}
	}
	if !superseded {
		t.Errorf("EngineChanges = %q, want the superseded unreadable record noted", state.EngineChanges)
	}
	if !swap {
		t.Errorf("EngineChanges = %q, want the swap reported across the unreadable record", state.EngineChanges)
	}
}
