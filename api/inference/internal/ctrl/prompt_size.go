package ctrl

import (
	"encoding/json"
)

// promptTextBytes estimates the bytes of a chat request that stand for prompt
// tokens, for the fit test in injectableOutputCap.
//
// It starts from the whole body and subtracts only what is provably cheaper
// than its length: a non-text content part. That direction is the whole design.
// The obvious alternative — walk the fields that carry prompt and add them up —
// was tried and was wrong, because the list is not closeable. Tool-call
// arguments, a message name, and every Anthropic block whose text lives under a
// key other than "text" (tool_result, thinking, tool_use) all reach the model,
// and every one of them missing from such a list makes the estimate read LOW.
// Reading low is the dangerous direction here: the fit test then says the
// advertised cap still fits when it does not, and the upstream answers 400 on a
// request that worked before the flag was turned on.
//
// Subtracting cannot have that bug. An unrecognised field keeps its full byte
// length, which over-reads at worst, and over-reading only forwards a request
// without a cap.
//
// The one thing worth subtracting is an attachment. An image arrives as a
// base64 data URL, and a vision model charges it by patch: half a megabyte of
// characters for a few thousand tokens. Left at its length it would read as a
// nearly full context window on a request using a fraction of one.
func promptTextBytes(body []byte) int64 {
	total := int64(len(body))

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return total
	}
	total -= attachmentExcess(fields["system"])

	var messages []json.RawMessage
	if err := json.Unmarshal(fields["messages"], &messages); err != nil {
		return total
	}
	for _, m := range messages {
		var msg map[string]json.RawMessage
		if err := json.Unmarshal(m, &msg); err != nil {
			continue
		}
		total -= attachmentExcess(msg["content"])
	}
	if total < 0 {
		total = 0
	}
	return total
}

// nonTextPartBytes is what one non-text content part is charged instead of its
// length, in the same byte currency as the rest of the estimate.
//
// 12288 bytes is 4096 tokens at three bytes per token, which is what one image
// costs on the vision model this network actually serves: sglang's default
// max_pixels of 12845056, over 28x28 patches, four patches to a token. Anthropic
// (~1600) and GPT-4o high detail (~1105) sit well below that, so this is an
// upper bound across the engines in play rather than a typical figure —
// deliberately, because it is SUBTRACTED from an otherwise conservative total
// and a too-small allowance is what would make the estimate read low.
const nonTextPartBytes = 12288

// mediaPartTypes are the content-block types whose bytes are an encoded
// attachment rather than text — the only ones worth less than their length.
//
// An allowlist, not "anything without a text field". That test looked
// equivalent and was not: Anthropic keeps prompt text under other keys, so a
// tool_result or a thinking block would have been charged the flat allowance
// instead of the ninety kilobytes of prompt it carries, and the estimate would
// read LOW — the direction that injects a cap the window cannot hold. An
// unrecognised block type keeps its full length, which is only ever the safe
// error.
//
// "document" is deliberately absent: a PDF's text does become tokens roughly in
// proportion to its size.
var mediaPartTypes = map[string]struct{}{
	"image_url":   {},
	"image":       {},
	"input_audio": {},
	"audio":       {},
	"video":       {},
	"video_url":   {},
	"input_image": {},
}

// attachmentExcess is how much of a content field is attachment padding: for
// each media part, the amount by which its raw length exceeds the flat
// allowance. Zero for a plain string, for a parse it cannot read, for a block
// type it does not recognise, and for parts already smaller than the allowance.
func attachmentExcess(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return 0
	}

	var excess int64
	for _, p := range parts {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(p, &fields); err != nil {
			continue
		}
		var partType string
		if err := json.Unmarshal(fields["type"], &partType); err != nil {
			continue
		}
		if _, isMedia := mediaPartTypes[partType]; !isMedia {
			continue
		}
		if over := int64(len(p)) - nonTextPartBytes; over > 0 {
			excess += over
		}
	}
	return excess
}
