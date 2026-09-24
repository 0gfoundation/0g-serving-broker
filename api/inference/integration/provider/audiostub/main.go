// Command audiostub is a throwaway stand-in for the Seed Audio adaptor, so the
// broker's audio-generation path can be exercised end to end before that adaptor
// exists and without BytePlus credentials.
//
// It answers POST /audio/speech (the path the broker calls) and /v1/audio/speech
// exactly as the real adaptor must:
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
//	?format=wav      override the Content-Type (else read from response_format)
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// BodyMarker is written at offset 0 of every response body. Exported so a test
// can assert the bytes it received start with it — see the write site.
const BodyMarker = "0G-AUDIOSTUB\x00"

// contentTypeFor maps a response_format onto the media type the real adaptor
// would return. Unknown formats fall back to application/octet-stream rather than
// guessing: a stub silently claiming audio/mpeg for a format it does not know
// would mask exactly the mismatch a test is looking for.
func contentTypeFor(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "mp3":
		return "audio/mpeg"
	case "wav":
		return "audio/wav"
	case "pcm":
		return "audio/L16"
	case "opus", "ogg_opus":
		return "audio/ogg"
	default:
		return "application/octet-stream"
	}
}

// requestedFormat reads response_format out of a JSON body, tolerating anything
// unparseable — this is a stub, and a malformed body is the caller's business.
func requestedFormat(r *http.Request) string {
	var body struct {
		ResponseFormat string `json:"response_format"`
	}
	// 48MB, the real adaptor's own body limit: a request carrying base64 data:
	// references is routinely over 1MB, and truncating it here made the JSON
	// unparseable, so the stub answered audio/mpeg for a wav request — a false
	// result on exactly the requests this feature adds.
	raw, err := io.ReadAll(io.LimitReader(r.Body, 48<<20))
	if err != nil {
		return ""
	}
	_ = json.Unmarshal(raw, &body)
	return body.ResponseFormat
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8090"
	}
	defaultDuration := os.Getenv("DURATION")
	if defaultDuration == "" {
		defaultDuration = "47.2"
	}

	// Registered on both paths, like the real adaptor (handler.SpeechRoutes): the
	// broker strips any leading /v1 and calls targetUrl + "/audio/speech", so the
	// unprefixed path is the one broker traffic actually hits.
	speech := func(w http.ResponseWriter, r *http.Request) {
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
		// adaptor looks to the broker: it should fall back to the vendor's ceiling
		// and increment broker_audio_billing_fallback_total{source="ceiling"}. A
		// duration above the ceiling (say ?duration=500) is clamped to it and counted
		// as source="usage_over_ceiling".
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

		// Echo the format the caller asked for, because the real adaptor does and
		// the broker copies this header straight through to the client. Getting it
		// wrong here would hide a broker bug rather than expose one.
		format := q.Get("format")
		if format == "" {
			format = requestedFormat(r)
		}
		w.Header().Set("Content-Type", contentTypeFor(format))
		if duration != "" {
			w.Header().Set("X-0G-Audio-Duration-Seconds", duration)
		}
		w.WriteHeader(http.StatusOK)

		// A fixed marker at offset 0, then filler.
		//
		// It is deliberately NOT a valid header for any audio format. The earlier
		// version wrote an ID3v2 tag, which implied an MP3-ness that was not real
		// (no MPEG frames follow it) and had to be special-cased per format for no
		// gain. The marker's only job is to be RECOGNISABLE at a known offset, so a
		// caller can assert the first bytes arrived unchanged and prove the body was
		// passed through rather than wrapped, re-encoded, or injected into.
		//
		// That check is the point: injecting x_0g_trace into an audio body is the
		// hazard the router must avoid, and it fails silently — 200 status,
		// plausible length, a file that will not play. A format-shaped header would
		// test nothing extra, since this stub never produces decodable audio anyway.
		//
		// It does NOT help spot truncation: filler looks alike at any length, so
		// compare the byte count for that.
		body := make([]byte, size)
		copy(body, []byte(BodyMarker))
		if _, err := w.Write(body); err != nil {
			log.Printf("audiostub: write body: %v", err)
		}
		log.Printf("audiostub: served %d bytes, duration=%q", size, duration)
	}
	http.HandleFunc("/audio/speech", speech)
	http.HandleFunc("/v1/audio/speech", speech)

	addr := ":" + port
	log.Printf("audiostub listening on %s (default duration %s)", addr, defaultDuration)
	srv := &http.Server{Addr: addr, Handler: http.DefaultServeMux}
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("audiostub: %v", err)
	}
}
