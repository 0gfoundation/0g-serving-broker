package attest

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// engineFieldSep separates an engine record's fields.
//
// A tab rather than a space, because the last field is a container's whole argument
// list and arguments contain spaces. Everything before it — a container name, an image
// reference, a GPU list — cannot contain a tab, and an argument that does is refused
// rather than accommodated: no engine takes one, and allowing it would make the field
// count depend on the arguments.
const engineFieldSep = "\t"

// engineFieldCount is how many fields one engine line carries. Fixed, so a line that
// grew or lost one is refused rather than read as a different engine.
const engineFieldCount = 4

// The three states RunningState.EnginesState can hold, mirroring the upstream set's for
// the same reason: the zero value must not read as "recorded, and there are none".
const (
	EnginesUnrecorded = "" // no engine record appeared
	EnginesKnown      = "known"
	EnginesUnknown    = "unknown" // the deciding record could not be read
)

// Engine is one container the controller created, as the record describes it.
//
// Every field is the record's claim. Unlike a compose service, nothing here is covered
// by compose_hash — the container did not exist when the CVM launched, so the signed
// report body says nothing about it. What makes the claim worth anything is the chain
// EventEngineSet describes: app_compose declares the controller's image and declares that
// only it holds the docker socket, and that controller records before it creates. A
// reader who has not reviewed those two things should treat this as unverified.
type Engine struct {
	// Name is the container name, which is also the host the compose network resolves to
	// it. It is what an upstream URL's host must match for the destination to be a
	// container this deployment declares.
	Name string

	// Image is the reference the container runs, "<repo>@sha256:<64hex>". A digest, not a
	// tag: a tag names what was asked for, not what arrived, and this record is the only
	// account of an image compose_hash does not cover.
	Image string

	// GPUs is the NVIDIA_VISIBLE_DEVICES value, verbatim. Placement rather than
	// behaviour, recorded because an operator reading the ledger needs to know which card
	// a container held.
	GPUs string

	// Args is the container's whole argument list as the record spells it, verbatim.
	//
	// "As the record spells it" and not "as the container received it": nothing here can
	// tell the two apart. A reader holds the writer's claim about the arguments, and the
	// writer is the party being described — the same standing every field on this type
	// has. An earlier version of this comment said "space-joined as the container
	// received it", which described a writer that does not exist yet and asserted a
	// correspondence no reader can check.
	//
	// Recorded in full rather than filtered. The filtered version was the earlier design
	// and it was worse in the direction that matters: it hid the performance knobs, and
	// with them any flag that moves data OFF the container — a request-content log, an
	// external trace endpoint — which is exactly what a reader needs to see. Publishing
	// everything means an argument carrying a credential would be published too, and that
	// is the operator's to avoid; the reader's need to see where data can go wins.
	Args string
}

// parseEngineSet reads one EventEngineSet payload into the engines it names.
//
// The payload is the WHOLE set, and the last record decides — the same shape and the
// same reasoning as parseUpstreamSet, including why the count travels in the payload.
// See there; this does not restate it.
//
//	count=<n>
//	<name>\t<image>\t<gpus>\t<args>
//	…                              ← n of these
//
// The caps and the "size nothing from the payload" rule are shared with that parser
// deliberately: the input is the same untrusted event log, so the bound has to be the
// same one.
func parseEngineSet(payload string) ([]Engine, error) {
	var engines []Engine
	seen := map[string]struct{}{}
	want := -1

	for line := range strings.SplitSeq(payload, "\n") {
		if len(line) > maxUpstreamLine {
			return nil, fmt.Errorf("%s payload has a %d-byte line, over the %d-byte limit; an engine is a name, an image, a GPU list and an argument list", EventEngineSet, len(line), maxUpstreamLine)
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		if want < 0 {
			if !strings.HasPrefix(line, upstreamCountPrefix) || strings.Contains(line, engineFieldSep) {
				return nil, fmt.Errorf("%s payload starts with %q, want a header %s<n> naming how many engines follow", EventEngineSet, line, upstreamCountPrefix)
			}
			n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, upstreamCountPrefix)))
			if err != nil || n < 0 {
				return nil, fmt.Errorf("%s payload header %q does not name an engine count", EventEngineSet, line)
			}
			if n > maxUpstreamMembers {
				return nil, fmt.Errorf("%s payload says %s%d, over the %d-engine limit", EventEngineSet, upstreamCountPrefix, n, maxUpstreamMembers)
			}
			want = n
			continue
		}

		fields := strings.Split(line, engineFieldSep)
		if len(fields) != engineFieldCount {
			return nil, fmt.Errorf("%s payload line %q has %d tab-separated fields, want %d: a name, an image, a GPU list and an argument list", EventEngineSet, line, len(fields), engineFieldCount)
		}
		name := fields[0]
		// The same pattern an upstream member's name takes, and it has to be: this name is
		// a container name, which is the host the compose network resolves, and an upstream
		// URL's host is matched against it. A name the URL grammar cannot spell could never
		// be matched, so accepting one here would record an engine no destination can
		// reach.
		if !upstreamNamePattern.MatchString(name) {
			return nil, fmt.Errorf("%s payload line %q names %q, which is not a container name a URL host could match", EventEngineSet, line, name)
		}
		if _, dup := seen[name]; dup {
			return nil, fmt.Errorf("%s payload names %q twice; within one set a container has one description", EventEngineSet, name)
		}
		seen[name] = struct{}{}

		// A digest, checked rather than trusted. A tag here would make the record name
		// what was asked for instead of what runs, and this record is the only account of
		// an image compose_hash does not cover — so a tag would leave nothing accountable
		// at all.
		image := fields[1]
		repo, digest, pinned := strings.Cut(image, "@")
		if !pinned || repo == "" || !digestPattern.MatchString(digest) {
			return nil, fmt.Errorf("%s payload line %q carries image %q, which does not pin a digest as <repo>@sha256:<64hex>", EventEngineSet, line, image)
		}

		engines = append(engines, Engine{Name: name, Image: image, GPUs: fields[2], Args: fields[3]})
		if len(engines) > want {
			return nil, engineTallyError(want, len(engines))
		}
	}

	if want < 0 {
		return nil, fmt.Errorf("%s payload is %d bytes of nothing but whitespace; even the empty set is written out, as %s0", EventEngineSet, len(payload), upstreamCountPrefix)
	}
	if len(engines) != want {
		return nil, engineTallyError(want, len(engines))
	}
	if len(engines) == 0 {
		return nil, nil
	}
	return engines, nil
}

// engineTallyError is the refusal for a count that disagrees with the lines, shared by
// the in-loop and post-loop checks so the two cannot describe it differently. Numbers
// only, so it needs no bounding.
func engineTallyError(want, got int) error {
	return fmt.Errorf("%s payload says %s%d and describes %d engine(s), so the set it names is not the set it lists", EventEngineSet, upstreamCountPrefix, want, got)
}

// engineChanges reports how the engine set moved from prev to next, one line per name
// the two do not describe the same way, sorted by name:
//
//	"<name>: created as <image> on GPU <gpus>"
//	"<name>: <old image> on GPU <old> -> <new image> on GPU <new>"
//	"<name>: <old image> on GPU <old> -> removed"
//
// Nil when the two agree, which is the ordinary case — a writer re-emitting its unchanged
// table at boot produces nothing here.
//
// It exists for the reason upstreamChanges does, and the argument is that doc's argument:
// Engines describes only the FINAL state, and both directions of change are fail-open. A
// CVM that ran one image, served requests, and then re-recorded the same container name
// with a different image leaves a final state showing only the second — and the plaintext
// went to the first. Withdrawing a container is the same shape: the set that remains reads
// as though it was always the whole set.
//
// Telling a caller to walk Events instead is not an answer, for the same reason it was not
// one there: the values a caller actually consumes do not do that.
//
// The argument list is compared but not printed. It is the largest field by far and a
// change to a performance knob is not what a reader is looking for here; a change to it
// still produces a line, because the image and GPU shown will be the current pair and the
// line's existence is the signal. A reader who needs the arguments has Engines.
func engineChanges(prev, next []Engine) []string {
	before := make(map[string]Engine, len(prev))
	for _, e := range prev {
		before[e.Name] = e
	}
	var lines []string
	for _, e := range next {
		switch old, existed := before[e.Name]; {
		case !existed:
			lines = append(lines, fmt.Sprintf("%s: created as %s", e.Name, describeEngine(e)))
		case old != e:
			lines = append(lines, fmt.Sprintf("%s: %s -> %s", e.Name, describeEngine(old), describeEngine(e)))
		}
		delete(before, e.Name)
	}
	for _, e := range before {
		lines = append(lines, fmt.Sprintf("%s: %s -> removed", e.Name, describeEngine(e)))
	}
	// Sorted because the removals come out of a map, so without this the same pair of
	// snapshots would report a different order on every run — and a caller diffing two
	// verifications would see changes that did not happen.
	sort.Strings(lines)
	return lines
}

// describeEngine renders one engine for a change line: the image it runs and the GPUs it
// holds. "GPU" is spelled out rather than left blank for an empty value, because losing a
// GPU assignment is a change a reader needs to see and an empty string beside an arrow
// reads like a formatting slip.
func describeEngine(e Engine) string {
	gpus := e.GPUs
	if gpus == "" {
		gpus = "(none)"
	}
	return e.Image + " on GPU " + gpus
}

// engineLookup keys engines by the host that resolves to them, which is the container
// name lowercased — DNS is case-insensitive and validUpstreamURL refuses an uppercase
// host, so a recorded URL always arrives lowercase.
//
// A name two engines share once lowercased is dropped rather than resolved by last-wins,
// for the reason composeServiceLookup drops one: the output is a statement about which
// container sees the plaintext, and picking between two candidates would make it up.
//
// Unreachable through the resolver, and stated rather than left to look load-bearing:
// parseEngineSet refuses a name outside upstreamNamePattern, which admits no uppercase,
// so two entries cannot differ by case alone and a duplicate is already refused there.
// Kept because this function is exported to no one but is called with whatever a future
// caller holds, and because the parser could relax — the compose side has the same guard
// for a map that CAN genuinely collide, and the two should not answer differently.
func engineLookup(engines []Engine) map[string]Engine {
	lookup := make(map[string]Engine, len(engines))
	ambiguous := make(map[string]bool)
	for _, e := range engines {
		host := strings.ToLower(e.Name)
		if _, dup := lookup[host]; dup {
			ambiguous[host] = true
			continue
		}
		lookup[host] = e
	}
	for host := range ambiguous {
		delete(lookup, host)
	}
	return lookup
}

// RenderEngineSet builds an EventEngineSet payload from the engines it is given, and
// refuses rather than producing one the reader would reject.
//
// Validation is by round trip through parseEngineSet, exactly as RenderUpstreamSet does
// it and for the same reason: the reader IS the specification, so a writer with its own
// copy of the rules is a writer that can drift from them. Every refusal here — an
// unpinned image, an unmatchable name, a duplicate, a line over the cap — is the reader's
// refusal, quoted back at the caller before it reaches the ledger.
//
// Sorted by name, so the payload is a function of the SET rather than of the order docker
// happened to list the containers in. Without it, two identical machines would write
// different records, and a restart that reordered a listing would look like a change.
//
// The caller's slice is not reordered.
func RenderEngineSet(engines []Engine) (string, error) {
	// Checked before anything is built, for the reason RenderUpstreamSet states at
	// length: this is the one place a caller's slice length sizes the work, and leaving
	// the cap to the parse below means paying for the whole payload to earn a refusal
	// about its first line.
	if len(engines) > maxUpstreamMembers {
		return "", fmt.Errorf("cannot record %d engines: the %s grammar holds at most %d", len(engines), EventEngineSet, maxUpstreamMembers)
	}

	sorted := make([]Engine, len(engines))
	copy(sorted, engines)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	var b strings.Builder
	fmt.Fprintf(&b, "%s%d", upstreamCountPrefix, len(sorted))
	for _, e := range sorted {
		// A tab in any field would move the field boundaries, so the line would read as a
		// different engine or as the wrong number of them; a newline would split it in two.
		// Refused here rather than at the parse, because the parse can only report the shape
		// it ended up with — "5 fields, want 4" — and not which field carried it.
		for _, f := range []struct{ what, value string }{
			{"name", e.Name}, {"image", e.Image}, {"GPU list", e.GPUs}, {"argument list", e.Args},
		} {
			if strings.ContainsAny(f.value, "\t\n\r") {
				return "", fmt.Errorf("engine %q has a tab or newline in its %s, which would move the field boundaries of its record", e.Name, f.what)
			}
		}
		// Before the line is built, not after: the cap has to bound the allocation, and an
		// argument list is the field a caller can make arbitrarily long.
		line := len(e.Name) + len(e.Image) + len(e.GPUs) + len(e.Args) + (engineFieldCount-1)*len(engineFieldSep)
		if line > maxUpstreamLine {
			return "", fmt.Errorf("engine %q renders a %d-byte line, over the %d-byte limit", e.Name, line, maxUpstreamLine)
		}
		b.WriteString("\n")
		b.WriteString(strings.Join([]string{e.Name, e.Image, e.GPUs, e.Args}, engineFieldSep))
	}
	payload := b.String()

	back, err := parseEngineSet(payload)
	if err != nil {
		return "", fmt.Errorf("this engine set cannot be recorded: %w", err)
	}
	// Unreachable while the header is written from len(sorted) — the parse already refuses
	// a payload whose members disagree with its count. Kept for the reason
	// RenderUpstreamSet keeps its twin: it is the difference between "the count matched"
	// and "the set that came back is the set that went in", and it becomes load-bearing
	// the moment the header stops being derived from this slice.
	if len(back) != len(sorted) {
		return "", fmt.Errorf("rendering %d engines produced a record of %d: the encoding here and the one in parseEngineSet disagree", len(sorted), len(back))
	}
	for i := range sorted {
		if back[i] != sorted[i] {
			return "", fmt.Errorf("engine %q rendered as %+v and read back as %+v: the encoding here and the one in parseEngineSet disagree", sorted[i].Name, sorted[i], back[i])
		}
	}
	return payload, nil
}
