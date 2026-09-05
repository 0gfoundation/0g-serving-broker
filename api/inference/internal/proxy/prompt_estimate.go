package proxy

import (
	"encoding/json"
)

// promptBytes estimates how many bytes of a chatbot request body actually
// become prompt tokens.
//
// The raw body length is not that number, and the difference is exploitable.
// JSON whitespace, fields the upstream ignores, and escape expansion all
// inflate the envelope without reaching the model: four megabytes of legal
// indentation reads as a million tokens while costing the engine nothing. Since
// an over-budget request is admitted alone rather than rejected, one such body
// takes the entire chatbot surface with it for as long as it runs — and it is
// cheap to send, because the caller is billed on what the upstream actually
// saw.
//
// So this walks the fields that carry prompt text — system, each message's
// content, and the tool definitions, which are prompt too — and counts only
// those. Anything else in the envelope is free, which is correct: it is free
// for the engine as well.
//
// Returns 0 when the body is not a JSON object. A body the engine cannot parse
// will be rejected before it holds any KV, so charging it for context would be
// charging for work that never happens.
func promptBytes(reqBody []byte) int64 {
	var body map[string]json.RawMessage
	if err := json.Unmarshal(reqBody, &body); err != nil || body == nil {
		return 0
	}

	var total int64
	total += textBytes(body["system"]) // Anthropic, and OpenAI's newer top-level form
	total += textBytes(body["prompt"]) // legacy completions

	// Tool definitions are sent to the model verbatim, so their whole JSON is
	// prompt. Same for any response-format schema.
	total += int64(len(body["tools"]))
	total += int64(len(body["response_format"]))

	var messages []map[string]json.RawMessage
	if raw, ok := body["messages"]; ok && json.Unmarshal(raw, &messages) == nil {
		for _, m := range messages {
			total += textBytes(m["content"])
			total += textBytes(m["role"])
			// Assistant turns carry tool calls that are replayed to the model.
			total += int64(len(m["tool_calls"]))
			// Every message costs a few tokens of chat-template scaffolding
			// regardless of content; without this a thousand empty messages would
			// weigh nothing.
			total += perMessageOverheadBytes
		}
	}
	return total
}

// perMessageOverheadBytes approximates the role markers and separators a chat
// template wraps each message in — a handful of tokens, expressed in the same
// byte currency as everything else here.
const perMessageOverheadBytes = 16

// textBytes returns the prompt bytes in a field that may be a plain string or
// the OpenAI content-part array. Anything else contributes its encoded length,
// which is the honest answer for a shape this does not recognise.
func textBytes(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return int64(len(s))
	}

	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parts); err == nil {
		var total int64
		for _, p := range parts {
			var text string
			if err := json.Unmarshal(p["text"], &text); err == nil {
				total += int64(len(text))
				continue
			}
			// A part with no text field — an image_url, say. Models that accept
			// those are excluded from the budget entirely (see tokenBudgetWeight),
			// so this is a text-only model being sent a shape it will reject; count
			// the encoded length and let the upstream answer for it.
			total += int64(len(p["text"]))
		}
		return total
	}

	return int64(len(raw))
}
