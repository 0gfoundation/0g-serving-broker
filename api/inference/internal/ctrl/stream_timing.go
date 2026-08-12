package ctrl

import (
	"fmt"
	"strings"
	"time"
)

// streamTiming measures HOW an upstream SSE stream arrived, not merely that it
// did: time to first byte, the longest stretch with nothing on the wire, and how
// many frames the output was split across.
//
// maxGap is the field this exists for. A client's idle watchdog fires on the
// longest silent stretch, so that single number decides whether a stream is
// perceived as working — and a successful stream currently leaves exactly one
// line in this log ("Whitelist user request"), with no timing at all. Answering
// a "the stream just stopped" report therefore meant dividing a total response
// size by billed completion tokens and guessing the frame spacing that would
// produce it, which those numbers cannot actually settle: a low byte count is
// equally consistent with few large frames and with tokens billed but never
// emitted, and those have different owners.
//
// The router logs the same four values on its own hop. Having both turns
// attribution into a subtraction rather than an argument: if the broker already
// saw the gap it came from upstream, if only the router did it was introduced
// between here and there.
//
// Cost is two time.Now() calls and a prefix test per line, on a path that
// already sanitizes and JSON-parses each line.
type streamTiming struct {
	start  time.Time
	first  time.Time
	last   time.Time
	maxGap time.Duration
	frames int
	lines  int
	bytes  int64

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

// String renders the measurement for the printf-style logger this package uses.
// Key=value so the fields stay greppable in the text-formatted broker log.
func (t *streamTiming) String() string {
	// A stream that produced nothing has no first line, so ttft would otherwise
	// be reported as the time since the zero Time.
	var ttft time.Duration
	if !t.first.IsZero() {
		ttft = t.first.Sub(t.start)
	}
	return fmt.Sprintf("ttft_ms=%d max_gap_ms=%d frames=%d lines=%d upstream_bytes=%d",
		ttft.Milliseconds(), t.maxGap.Milliseconds(), t.frames, t.lines, t.bytes)
}
