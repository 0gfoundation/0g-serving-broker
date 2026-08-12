package ctrl

import (
	"fmt"
	"strings"
	"time"
)

// streamTiming measures HOW an upstream SSE stream arrived, not merely that it
// did: how long the first line took, the longest stretch with nothing on the
// wire, and how many frames the output was split across.
//
// max_gap_ms is the field this exists for. A client's idle watchdog fires on the
// longest silent stretch, so that single number decides whether a stream is
// perceived as working — and a successful stream currently leaves exactly one
// line in this log ("Whitelist user request"), with no timing at all. Answering
// a "the stream just stopped" report therefore meant dividing a total response
// size by billed completion tokens and guessing the frame spacing that would
// produce it, which those numbers cannot actually settle: a low byte count is
// equally consistent with few large frames and with tokens billed but never
// emitted, and those have different owners.
//
// The router logs the same fields on its own hop (0g-router #723), under the
// same names and with the same start point, so the two can be compared directly:
// if the broker already saw the gap it came from upstream, if only the router
// did it was introduced between here and there.
//
// Cost is two time.Now() calls and a prefix test per line, on a path that
// already sanitizes and JSON-parses each line.
type streamTiming struct {
	start  time.Time
	first  time.Time
	last   time.Time
	maxGap time.Duration
	// tailGap is the stretch after the LAST line, filled in by finish. Without
	// it the measurement is blind in exactly the case it was built for: an
	// upstream that delivers a few frames and then hangs produces a small
	// maxGap (the spacing from before it hung), so the hop comparison would
	// clear this hop of a stream that stalled for minutes.
	tailGap time.Duration
	frames  int
	lines   int
	bytes   int64

	// now exists so the gap arithmetic can be tested against exact durations
	// instead of against the scheduler.
	now func() time.Time
}

func newStreamTiming() *streamTiming {
	return &streamTiming{start: time.Now(), now: time.Now}
}

// mark records one line read from upstream. Gaps are measured between LINES
// rather than between `data:` frames because bytes are what a watchdog sees: a
// keepalive comment or a frame's trailing blank line breaks the silence just as
// a payload does, and counting only payloads would report a gap the client never
// experienced. frames counts `data:` lines separately, since it answers the
// different question of how coarsely the output was chunked.
func (t *streamTiming) mark(line string) {
	if line == "" {
		return
	}
	now := t.now()
	if t.lines == 0 {
		t.first = now
	} else if gap := now.Sub(t.last); gap > t.maxGap {
		t.maxGap = gap
	}
	t.last = now
	t.lines++
	t.bytes += int64(len(line))
	if strings.HasPrefix(line, "data:") {
		t.frames++
	}
}

// finish closes the measurement once the stream is over, folding the trailing
// silence into maxGap. Call it before logging.
//
// For a stream that produced nothing at all, the whole duration is the silence —
// the most alarming case, which would otherwise print as a flat zero.
func (t *streamTiming) finish() {
	from := t.last
	if t.lines == 0 {
		from = t.start
	}
	t.tailGap = t.now().Sub(from)
	if t.tailGap > t.maxGap {
		t.maxGap = t.tailGap
	}
}

// String renders the measurement for the printf-style logger this package uses.
// Every value is a bare number so the whole line stays splittable on spaces and
// greppable as key=value.
func (t *streamTiming) String() string {
	// -1, not 0, when the upstream never produced a line. Zero is the healthiest
	// possible value for this field, so reporting it for "nothing ever arrived"
	// would disguise the worst outcome as the best one, and any threshold built
	// on the field would skip straight over it.
	firstLineMs := int64(-1)
	if !t.first.IsZero() {
		firstLineMs = t.first.Sub(t.start).Milliseconds()
	}
	// Named for where the clock starts: this runs only after the upstream's
	// response headers are in hand, so the wait for those headers — queueing at
	// the vendor, a gateway that withholds them until the first token — is NOT
	// included. Calling it "ttft" would have quietly claimed otherwise, and the
	// router's matching field carries the same name for the same reason.
	return fmt.Sprintf(
		"first_line_after_headers_ms=%d max_gap_ms=%d tail_gap_ms=%d frames=%d lines=%d upstream_bytes=%d",
		firstLineMs, t.maxGap.Milliseconds(), t.tailGap.Milliseconds(), t.frames, t.lines, t.bytes)
}
