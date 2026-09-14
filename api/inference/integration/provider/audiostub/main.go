// Command audiostub is a throwaway stand-in for the Seed Audio adaptor, so the
// broker's audio-generation path can be exercised end to end before that adaptor
// exists and without BytePlus credentials.
//
// It answers POST /v1/audio/speech exactly as the real adaptor must:
//
//	Content-Type: audio/mpeg
//	X-0G-Audio-Duration-Seconds: <seconds>
//	<audio bytes>
//
// It is NOT a vendor emulator. It never calls BytePlus and its "audio" is filler
// bytes. What it reproduces faithfully is the one contract the broker depends on —
// bytes in the body, the billable duration in a header — which is the thing worth
// testing before the real adaptor lands.
//
// Usage:
//
//	go run ./inference/integration/provider/audiostub            # listens on :8090
//	PORT=9000 DURATION=12.5 go run ./...                         # override
//
// Query overrides, so one process covers every case the broker must handle:
//
//	?duration=47.2   the reported duration
//	?duration=       omit the header entirely  -> exercises the reserve fallback
//	?bytes=100000    size of the returned body
//	?status=500      return an error instead   -> broker must not bill
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8090"
	}
	defaultDuration := os.Getenv("DURATION")
	if defaultDuration == "" {
		defaultDuration = "47.2"
	}

	http.HandleFunc("/v1/audio/speech", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		if s := q.Get("status"); s != "" {
			code, err := strconv.Atoi(s)
			if err != nil {
				code = http.StatusInternalServerError
			}
			// A non-200 must reach the broker BEFORE its billing dispatch, so
			// nothing is charged. The body shape mimics a vendor error envelope.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			fmt.Fprintf(w, `{"code":%d,"message":"audiostub forced status"}`, code)
			return
		}

		// An explicitly EMPTY duration omits the header, which is how a broken
		// adaptor looks to the broker: it should fall back to the reserved ceiling
		// and increment broker_audio_billing_fallback_total{source="reserve"}.
		duration := defaultDuration
		if q.Has("duration") {
			duration = q.Get("duration")
		}

		size := 4096
		if b := q.Get("bytes"); b != "" {
			if n, err := strconv.Atoi(b); err == nil && n >= 0 {
				size = n
			}
		}

		w.Header().Set("Content-Type", "audio/mpeg")
		if duration != "" {
			w.Header().Set("X-0G-Audio-Duration-Seconds", duration)
		}
		w.WriteHeader(http.StatusOK)

		// Filler shaped like an MP3 only at the head, so `file` and most players
		// identify it and a truncated read is obvious.
		body := make([]byte, size)
		copy(body, []byte("ID3\x04\x00\x00\x00\x00\x00\x00"))
		if _, err := w.Write(body); err != nil {
			log.Printf("audiostub: write body: %v", err)
		}
		log.Printf("audiostub: served %d bytes, duration=%q", size, duration)
	})

	addr := ":" + port
	log.Printf("audiostub listening on %s (default duration %s)", addr, defaultDuration)
	srv := &http.Server{Addr: addr, Handler: http.DefaultServeMux}
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("audiostub: %v", err)
	}
}
