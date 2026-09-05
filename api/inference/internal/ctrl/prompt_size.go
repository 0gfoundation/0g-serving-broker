package ctrl

import (
	"encoding/json"
)

// promptTextBytes estimates the bytes of a chat request that actually become
// prompt text, for the fit test in injectableOutputCap.
//
// The whole body is not that number, and for one shape it is not even close.
// An image arrives as a base64 data URL inside the JSON: half a megabyte of
// characters standing for on the order of a thousand tokens, because vision
// models charge by patch, not by byte. Measuring the envelope reads that as
// ~170k tokens and concludes the context window is nearly full — for a request
// using about one percent of it. The cap is then skipped on exactly the
// requests with the most room to generate into.
//
// So non-text content parts are charged a flat allowance rather than their
// length, and anything outside the prompt-bearing fields is not charged at all.
// Everything here errs high on purpose (see bytesPerToken): the fit test must
// not conclude "it fits" when it does not.
//
// A body this cannot parse falls back to its own length, which is the old
// behaviour and the conservative direction.
func promptTextBytes(body []byte) int64 {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return int64(len(body))
	}

	total := textPartBytes(fields["system"])

	var messages []json.RawMessage
	if err := json.Unmarshal(fields["messages"], &messages); err != nil {
		// No readable messages: fall back to the envelope rather than to zero,
		// which would claim every request fits.
		return int64(len(body))
	}
	for _, m := range messages {
		var msg map[string]json.RawMessage
		if err := json.Unmarshal(m, &msg); err != nil {
			total += int64(len(m))
			continue
		}
		total += textPartBytes(msg["content"])
		total += perMessageBytes
	}
	// Tool definitions are templated into the prompt verbatim.
	total += int64(len(fields["tools"]))
	return total
}

// perMessageBytes is the chat-template scaffolding around each message: role
// markers and separators, a handful of tokens expressed in bytes.
const perMessageBytes = 16

// nonTextPartBytes is what one non-text content part is charged, in the same
// byte currency as everything else here. Roughly 1500 tokens at three bytes per
// token — the high end of what a vision model spends on a single image, so the
// fit test stays conservative without tracking the byte length of a payload
// whose length says nothing about its cost.
const nonTextPartBytes = 4500

// textPartBytes measures a field that is either a string or a content-part
// array, charging non-text parts a flat allowance instead of their length.
func textPartBytes(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return int64(len(s))
	}
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return int64(len(raw))
	}
	var total int64
	for _, p := range parts {
		var text string
		if err := json.Unmarshal(p["text"], &text); err == nil {
			total += int64(len(text))
			continue
		}
		total += nonTextPartBytes
	}
	return total
}
