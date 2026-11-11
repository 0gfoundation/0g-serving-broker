package config

import (
	"os"
	"syscall"
	"time"
)

// WatchOverlay restarts the process when the overlay file changes, so a new overlay takes
// effect in every process that reads the config — the broker and the event service —
// without either of them holding the docker socket.
//
// It does not reload in place: the config is read once at start and copied into many
// places, and a reload that reached some of them would leave the process running two
// configs at once. Instead it sends the process SIGTERM, which both processes already
// handle with a graceful shutdown, and the container's restart policy
// (`restart: unless-stopped`) starts it again on the new overlay. In-flight requests get
// the broker's shutdown window (30s); a stream longer than that is cut.
//
// A no-op when no overlay file is configured.
func WatchOverlay(interval time.Duration, logf func(format string, args ...interface{})) {
	if os.Getenv(OverlayFileEnvVar) == "" {
		return
	}
	start, err := OverlayDigest()
	if err != nil {
		logf("[CONFIG-OVERLAY] not watching the overlay: %v", err)
		return
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for range t.C {
			now, err := OverlayDigest()
			if err != nil {
				logf("[CONFIG-OVERLAY] reading the overlay: %v", err)
				continue
			}
			if now != start {
				logf("[CONFIG-OVERLAY] overlay changed (%s -> %s); restarting to apply it", short(start), short(now))
				_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
				return
			}
		}
	}()
}

func short(d string) string {
	if d == "" {
		return "none"
	}
	if len(d) > 12 {
		return d[:12]
	}
	return d
}
