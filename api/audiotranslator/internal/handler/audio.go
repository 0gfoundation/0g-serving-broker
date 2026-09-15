// Package handler serves the OpenAI Audio Speech API in front of Seed Audio.
package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/audiotranslator/internal/seedaudio"
	"github.com/0glabs/0g-serving-broker/audiotranslator/internal/translate"
	"github.com/0glabs/0g-serving-broker/common/log"
)

// DurationHeader is the contract with the broker: the billable output duration,
// in seconds, on a successful response.
//
// It exists because the response BODY is the audio — an OpenAI SDK writes it
// straight to a file — so there is nowhere in it for a usage block. Must match
// ctrl.AudioDurationHeader on the broker side; a rename on one side alone makes
// every request fall back to the reserved ceiling, silently over-billing every
// caller.
const DurationHeader = "X-0G-Audio-Duration-Seconds"

// maxRequestBytes bounds an inbound body. Generous enough for three 10MB
// reference clips supplied as base64 data: URIs (~40MB) plus the prompt.
const maxRequestBytes = 48 << 20

// AudioHandler serves POST /v1/audio/speech.
type AudioHandler struct {
	client *seedaudio.Client
	logger log.Logger
	// credentialHeaders names the inbound headers forwarded to the vendor as
	// credentials. BytePlus Voice authenticates with a header SET, not a bearer
	// token, so this is a list rather than a single value.
	credentialHeaders []string
}

func NewAudioHandler(client *seedaudio.Client, logger log.Logger) *AudioHandler {
	return &AudioHandler{
		client: client,
		logger: logger,
		credentialHeaders: []string{
			seedaudio.HeaderAPIKey,
			seedaudio.HeaderRequestID,
		},
	}
}

// Speech handles POST /v1/audio/speech: OpenAI's request shape in, the audio
// bytes out, with the billable duration in a header.
func (h *AudioHandler) Speech(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxRequestBytes)

	var req translate.SpeechRequest
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "invalid request body: " + err.Error()}})
		return
	}
	// Refused BEFORE the vendor call. A local named failure costs nothing; a
	// vendor 400 arrives only after the broker has routed the request and taken
	// a balance reserve against it.
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": err.Error()}})
		return
	}

	resp, err := h.client.Create(c.Request.Context(), h.credentials(c), translate.ToCreateRequest(req))
	if err != nil {
		h.writeVendorError(c, err)
		return
	}

	audio, err := translate.DecodeAudio(*resp)
	if err != nil {
		h.logger.Errorf("seedaudio: %v", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": "upstream returned unusable audio"}})
		return
	}

	// Set before the body: once the first byte is written the headers are
	// committed, and the duration is the only channel the broker has for billing.
	if secs, ok := translate.BillableSeconds(*resp); ok {
		c.Writer.Header().Set(DurationHeader, strconv.FormatFloat(secs, 'f', -1, 64))
	} else {
		// The broker falls back to the reserved ceiling, which OVER-bills. Logged
		// loudly here because this side is where the cause is visible.
		h.logger.Errorf("seedaudio: response carried no usable duration; the broker will bill the reserved ceiling for this request")
	}
	c.Writer.Header().Set("Content-Type", translate.ContentTypeFor(req.ResponseFormat))
	c.Writer.Header().Set("Content-Length", strconv.Itoa(len(audio)))

	c.Status(http.StatusOK)
	if _, err := c.Writer.Write(audio); err != nil {
		h.logger.Warnf("seedaudio: write audio to client: %v", err)
	}
}

// credentials collects the vendor credential headers the broker attached.
func (h *AudioHandler) credentials(c *gin.Context) map[string]string {
	out := make(map[string]string, len(h.credentialHeaders))
	for _, k := range h.credentialHeaders {
		if v := c.GetHeader(k); v != "" {
			out[k] = v
		}
	}
	return out
}

// writeVendorError maps a vendor failure onto a status the broker treats
// correctly. Anything non-2xx returns BEFORE the broker's billing dispatch, so a
// failed generation is never charged.
func (h *AudioHandler) writeVendorError(c *gin.Context, err error) {
	h.logger.Errorf("seedaudio create failed: %v", err)
	status := http.StatusBadGateway
	msg := "audio generation failed"
	if apiErr, ok := err.(*seedaudio.APIError); ok {
		// A 4xx from the vendor is the caller's request being wrong, and passing
		// it through lets the client see that rather than a generic upstream
		// failure. A 5xx stays 502: the vendor's outage is not the client's error.
		if apiErr.StatusCode >= 400 && apiErr.StatusCode < 500 {
			status = apiErr.StatusCode
		}
		if apiErr.Message != "" {
			msg = apiErr.Message
		}
	}
	c.JSON(status, gin.H{"error": gin.H{"message": msg}})
}
