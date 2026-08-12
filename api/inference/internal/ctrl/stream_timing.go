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
// max_gap_ms spans the whole response: the wait for the first line, every
// stretch between lines, and the trailing silence. Any of the three can be the
// one the client's watchdog fires on, so excluding one would make the field
// quietly wrong rather than merely incomplete.
//
// The router measures the same quantities on its own hop (0g-router #723), from
// the same start point and with the same arithmetic, so the two are comparable:
// if the broker already saw the gap it came from upstream, if only the router
// did it was introduced between here and there. The NAMES differ by logger
// convention — this one writes printf text, so `first_line_after_headers_ms`
// here is `firstLineAfterHeadersMs` there, and both byte counters are named for
// being post-decompression. A query has to spell each side's own field name.
//
// What it does NOT measure, so the numbers are not over-read: it samples the
// interval between ReadString RETURNS, and the loop between them sanitizes,
// seals and writes. A client reading slowly backpressures the write, which
// delays the next read, and that wait is attributed to the following gap. So a
// slow client inflates the gap at BOTH hops at once and can look like an
// upstream stall under the rule above; a stream whose gaps are all small is
// definitely healthy, one with a large gap needs more than the subtraction.
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
	from := t.last
	if t.lines == 0 {
		t.first = now
		// The wait for the FIRST line is silence too. Excluding it while
		// finish() includes the trailing silence would leave the head as the one
		// blind spot — and a head stall is one of the commonest shapes a "the
		// stream froze" report takes: an upstream that flushes response headers
		// at once and only then starts thinking. Worse than merely missing it,
		// this hop would report max_gap_ms≈0 while the router reported the full
		// wait, and the cross-hop rule ("broker small, router large ⇒ introduced
		// between them") would blame the router for the upstream's stall.
		from = t.start
	}
	if gap := now.Sub(from); gap > t.maxGap {
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
	// Two field names carry their own caveat, which is why they are spelled the
	// long way:
	//
	//   - first_line_after_headers_ms names where the clock starts. This runs
	//     only once the upstream's response headers are in hand, so the wait for
	//     THOSE — queueing at the vendor, a gateway that withholds headers until
	//     the first token — is not included. "ttft" would have quietly claimed
	//     otherwise; the router's matching field is named the same way.
	//   - decompressed_bytes is counted after decompression, so a gzipped
	//     upstream puts far fewer bytes on the wire than this reports.
	//     Subtracting it from an edge log's compressed response size does not
	//     give zero.
	return fmt.Sprintf(
		"first_line_after_headers_ms=%d max_gap_ms=%d tail_gap_ms=%d frames=%d lines=%d decompressed_bytes=%d",
		firstLineMs, t.maxGap.Milliseconds(), t.tailGap.Milliseconds(), t.frames, t.lines, t.bytes)
}
