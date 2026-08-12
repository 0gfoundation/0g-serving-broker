package ctrl

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// fakeClock returns a now() that advances by the given deltas, one per call.
func fakeClock(base time.Time, deltas ...time.Duration) func() time.Time {
	i := 0
	return func() time.Time {
		d := time.Duration(0)
		if i < len(deltas) {
			d = deltas[i]
		}
		i++
		return base.Add(d)
	}
}

func TestStreamTimingMeasuresLongestSilence(t *testing.T) {
	base := time.Unix(0, 0)
	// Lines land at +2s, +3s, +170s, +171s. The 167s stretch is the one a
	// client's idle watchdog would fire on, and the one the log has to name.
	timing := &streamTiming{
		start: base,
		now:   fakeClock(base, 2*time.Second, 3*time.Second, 170*time.Second, 171*time.Second),
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

	assert.Equal(t, 2*time.Second, timing.first.Sub(timing.start), "ttft is start→first line")
	assert.Equal(t, 167*time.Second, timing.maxGap, "maxGap is the longest stretch between lines")
	assert.Equal(t, 2, timing.frames, "only data: lines count as frames")
	assert.Equal(t, 4, timing.lines)
	assert.Equal(t, int64(wantBytes), timing.bytes)

	assert.Equal(t,
		fmt.Sprintf("ttft_ms=2000 max_gap_ms=167000 frames=2 lines=4 upstream_bytes=%d", wantBytes),
		timing.String())
}

func TestStreamTimingEmptyStreamReportsZeroTTFT(t *testing.T) {
	// A stream that produced nothing must not report the time since the zero
	// Time as its ttft — that would be ~55 years and would poison any alerting
	// built on the field.
	timing := &streamTiming{start: time.Unix(0, 0), now: time.Now}
	assert.Equal(t, "ttft_ms=0 max_gap_ms=0 frames=0 lines=0 upstream_bytes=0", timing.String())
}
