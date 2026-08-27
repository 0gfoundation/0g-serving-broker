package ctrl

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// fakeClock returns a now() that advances by the given deltas, one per call.
//
// Panics rather than repeating base once they run out: a clock that silently
// jumps backwards makes every `gap > maxGap` comparison false, so a test missing
// one delta (forgetting that finish() consumes one, say) would still pass while
// exercising a timeline nobody wrote.
func fakeClock(base time.Time, deltas ...time.Duration) func() time.Time {
	i := 0
	return func() time.Time {
		if i >= len(deltas) {
			panic("fakeClock: ran out of deltas — the test's timeline is shorter than the calls it makes")
		}
		d := deltas[i]
		i++
		return base.Add(d)
	}
}

func TestStreamTimingMeasuresLongestSilence(t *testing.T) {
	base := time.Unix(0, 0)
	// Lines land at +2s, +3s, +170s, +171s, finish at +172s. The 167s stretch is
	// the one a client's idle watchdog would fire on, and the one the log has to
	// name.
	timing := &streamTiming{
		start: base,
		now: fakeClock(base,
			2*time.Second, 3*time.Second, 170*time.Second, 171*time.Second, 172*time.Second),
	}
	lines := []string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n",
		"\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"b\"}}]}\n",
		"\n",
	}
	wantBytes := 0
	for _, line := range lines {
		wantBytes += len(line)
		timing.mark(line)
	}
	timing.finish()

	assert.Equal(t, 2*time.Second, timing.first.Sub(timing.start))
	assert.Equal(t, 167*time.Second, timing.maxGap)
	assert.Equal(t, 2, timing.frames, "only data: lines count as frames")
	assert.Equal(t, 4, timing.lines)
	assert.Equal(t, int64(wantBytes), timing.bytes)

	assert.Equal(t,
		fmt.Sprintf("first_line_after_headers_ms=2000 max_gap_ms=167000 client_max_gap_ms=172000 tail_gap_ms=1000 frames=2 lines=4 writes=0 decompressed_bytes=%d", wantBytes),
		timing.String())
}

// The case the instrument exists for: a few frames, then the upstream hangs.
// Measuring only BETWEEN lines reports the spacing from before the hang and
// clears this hop, which is the exact wrong answer for a stalled stream.
func TestStreamTimingCountsTheTrailingSilence(t *testing.T) {
	base := time.Unix(0, 0)
	timing := &streamTiming{
		start: base,
		now:   fakeClock(base, time.Second, time.Second+300*time.Millisecond, 168*time.Second),
	}
	timing.mark("data: a\n")
	timing.mark("data: b\n")
	assert.Equal(t, 1*time.Second, timing.maxGap,
		"before finish, the widest known stretch is the wait for the first line")

	timing.finish()

	assert.Equal(t, 166700*time.Millisecond, timing.tailGap)
	assert.Equal(t, 166700*time.Millisecond, timing.maxGap,
		"the trailing silence must dominate maxGap, not be invisible to it")
}

// The third place silence can hide, and the one this copy was missing while the
// router had already fixed it: an upstream that flushes response headers at once
// and only then starts thinking puts the whole wait BEFORE the first line. Per-
// line gaps cannot see it, so this hop would report ~0 while the router reported
// the full wait — and the cross-hop rule would read that as "the router
// introduced it".
func TestStreamTimingCountsTheSilenceBeforeTheFirstLine(t *testing.T) {
	base := time.Unix(0, 0)
	timing := &streamTiming{
		start: base,
		now: fakeClock(base,
			150*time.Second, 150050*time.Millisecond, 150100*time.Millisecond, 150150*time.Millisecond),
	}
	timing.mark("data: a\n") // first token, 150s after the headers
	timing.mark("data: b\n") // then a healthy 50ms cadence
	timing.mark("data: c\n")
	timing.finish()

	assert.Equal(t, 150*time.Second, timing.maxGap,
		"the pre-first-line wait must dominate, not be excluded because no line preceded it")
	assert.Contains(t, timing.String(), "first_line_after_headers_ms=150000",
		"and the separate field still says WHICH stretch it was")
}

// A stream that produced nothing is the most alarming outcome, so it must not
// render as the healthiest-looking one.
func TestStreamTimingEmptyStreamIsNotReportedAsHealthy(t *testing.T) {
	base := time.Unix(0, 0)
	timing := &streamTiming{start: base, now: fakeClock(base, 90*time.Second)}
	timing.finish()

	rendered := timing.String()
	assert.NotContains(t, rendered, "first_line_after_headers_ms",
		"omitted, not 0 and not -1: either is a number that drags avg/p50/min toward 'fastest ever' for a stream that produced nothing")
	assert.Equal(t,
		"max_gap_ms=90000 client_max_gap_ms=90000 tail_gap_ms=90000 frames=0 lines=0 writes=0 decompressed_bytes=0",
		rendered,
		"with no lines at all, the whole stream is the silence — on both the upstream and the client side")
}

// ReadString hands back the data it read alongside the error, so a final line
// with no trailing newline arrives on the error path. rawBody (via TeeReader)
// captures it, so the counters must too or the two disagree.
func TestStreamTimingCountsAFinalLineWithoutNewline(t *testing.T) {
	base := time.Unix(0, 0)
	timing := &streamTiming{start: base, now: fakeClock(base, time.Second, 2*time.Second)}
	timing.mark("data: {\"choices\":[]}") // no trailing \n, as delivered with io.EOF
	timing.finish()

	assert.Equal(t, 1, timing.lines)
	assert.Equal(t, 1, timing.frames)
	assert.Equal(t, int64(20), timing.bytes)
}

// The error path calls mark unconditionally, and a clean EOF carries an empty
// string — which must not be recorded as a line arriving.
func TestStreamTimingIgnoresEmptyMarks(t *testing.T) {
	base := time.Unix(0, 0)
	timing := &streamTiming{start: base, now: fakeClock(base, time.Second)}
	timing.mark("")

	assert.Equal(t, 0, timing.lines)
	assert.True(t, timing.first.IsZero(), "an empty read must not count as the first line")
}

// The line is parsed by splitting on spaces, so no rendered value may contain
// one. The first version of this log interpolated a whole model.Request, which
// broke that AND wrote user addresses, signatures and fees into an INFO log.
func TestStreamTimingRendersOnlyBareNumbers(t *testing.T) {
	base := time.Unix(0, 0)
	timing := &streamTiming{start: base, now: fakeClock(base, time.Second, 2*time.Second)}
	timing.mark("data: x\n")
	timing.finish()

	rendered := timing.String()
	fields := strings.Fields(rendered)
	assert.Len(t, fields, 8, "space-splitting must yield exactly the eight fields: %q", rendered)
	for _, pair := range fields {
		parts := strings.SplitN(pair, "=", 2)
		assert.Len(t, parts, 2, "every field must be key=value: %q", pair)
		_, err := strconv.ParseInt(parts[1], 10, 64)
		assert.NoError(t, err, "every value must be a bare number: %q", pair)
	}
}

// The finding this pair of fields exists for. sanitizeStreamLine drops every SSE
// comment, so an upstream that keeps itself alive with `: OPENROUTER PROCESSING`
// advances max_gap_ms here while the client receives nothing at all. Reported
// alone, that reads as a healthy broker against a stalling router — the exact
// misattribution this instrument was added to prevent.
func TestStreamTimingSeparatesDroppedCommentsFromClientSilence(t *testing.T) {
	base := time.Unix(0, 0)
	// A 140s think, kept alive upstream by a comment every 15s. One real frame
	// lands at the end and is the only thing written to the client.
	deltas := []time.Duration{}
	for i := 1; i <= 9; i++ {
		deltas = append(deltas, time.Duration(i*15)*time.Second) // comments
	}
	deltas = append(deltas,
		140*time.Second, // the real data: line arrives
		140*time.Second, // markWrite for it
		141*time.Second, // finish
	)
	timing := &streamTiming{start: base, now: fakeClock(base, deltas...)}

	for i := 0; i < 9; i++ {
		timing.mark(": OPENROUTER PROCESSING\n") // read, then dropped by sanitize
	}
	timing.mark("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n")
	timing.markWrite()
	timing.finish()

	assert.Equal(t, 15*time.Second, timing.maxGap,
		"upstream really did send something every 15s")
	assert.Equal(t, 140*time.Second, timing.maxClientGap,
		"but the client got nothing for 140s, and only this number says so")
	assert.Equal(t, 1, timing.writes)
	assert.Contains(t, timing.String(), "client_max_gap_ms=140000")
}
