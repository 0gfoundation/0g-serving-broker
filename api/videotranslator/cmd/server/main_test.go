package server

import (
	"testing"

	"github.com/0glabs/0g-serving-broker/videotranslator/internal/dashscope"
)

// TestWriteTimeout_ExceedsContentFetchTimeoutWithMargin guards the exact
// coupling a self-review caught: the inbound WriteTimeout is a hard deadline
// measured from when the request arrived, spanning GetVideoContent's entire
// handling (GetTask, then FetchContent, then copying the response back) —
// not just the content download itself. If it were only equal to
// dashscope.ContentFetchTimeout, a download using close to its own budget
// would leave ~no headroom for the rest of the request, truncating an
// otherwise-successful download.
func TestWriteTimeout_ExceedsContentFetchTimeoutWithMargin(t *testing.T) {
	if writeTimeout <= dashscope.ContentFetchTimeout {
		t.Fatalf("writeTimeout (%v) must be strictly greater than dashscope.ContentFetchTimeout (%v), not equal or less",
			writeTimeout, dashscope.ContentFetchTimeout)
	}
	if margin := writeTimeout - dashscope.ContentFetchTimeout; margin < writeTimeoutMargin {
		t.Errorf("writeTimeout - ContentFetchTimeout = %v, want at least %v of headroom", margin, writeTimeoutMargin)
	}
}
