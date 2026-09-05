package proxy

import (
	"bytes"
	"encoding/json"
)

// estimateRequest reads a chatbot request body once and returns both numbers
// the KV budget needs: how many of its bytes actually become prompt, and how
// many output tokens the caller declared it wants.
//
// The raw body length is not that number, and the difference is exploitable.
// JSON whitespace, fields the upstream ignores, and escape expansion all
// inflate the envelope without reaching the model, so four megabytes of legal
// indentation would read as a million tokens while costing the engine nothing.
// Since an over-budget request is admitted alone rather than rejected, one such
// body would take the entire chatbot surface with it for as long as it runs —
// and it is cheap to send, because the caller is billed on what the upstream
// actually saw.
//
// So this walks the fields that carry prompt text and counts only those.
// Whitespace inside them does not count either: everything measured here is
// measured compacted, because that is what reaches the model.
//
// The estimate errs high rather than low wherever a shape is unfamiliar. An
// unrecognised content block is charged its whole compacted JSON, and a
// messages array that will not parse is charged its full compacted length,
// because "we could not read it, so it is free" is an invitation to send
// exactly that.
//
// Returns a zero estimate only for a body that is not a JSON object at all.
// Such a body is rejected by the engine before it holds any KV, so charging it
// for context would be charging for work that never happens.
func estimateRequest(reqBody []byte) requestEstimate {
	var body map[string]json.RawMessage
	if err := json.Unmarshal(reqBody, &body); err != nil || body == nil {
		return requestEstimate{}
	}

	est := requestEstimate{OutputTokens: requestedOutputTokens(body)}
	var total int64
	total += textBytes(body["system"]) // Anthropic, and OpenAI's newer top-level form
	total += textBytes(body["prompt"]) // legacy completions

	// Tool definitions and a response schema are handed to the model verbatim,
	// so their whole JSON is prompt — compacted, since the engine re-serializes
	// them and the whitespace never arrives.
	total += compactLen(body["tools"])
	total += compactLen(body["functions"]) // pre-tools spelling, still accepted
	total += compactLen(body["response_format"])

	raw, ok := body["messages"]
	if !ok {
		est.PromptBytes = total
		return est
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(raw, &messages); err != nil {
		// Unreadable shape. Charge the whole thing rather than nothing: the
		// upstream may well accept what this parser did not, and a body that is
		// free to send is the one an attacker sends.
		est.PromptBytes = total + compactLen(raw)
		return est
	}
	for _, m := range messages {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(m, &fields); err != nil {
			total += compactLen(m)
			continue
		}
		total += textBytes(fields["content"])
		total += textBytes(fields["role"])
		total += textBytes(fields["name"])
		total += compactLen(fields["tool_calls"])
		total += compactLen(fields["function_call"])
		// Every message costs a few tokens of chat-template scaffolding
		// regardless of content; without this a thousand empty messages would
		// weigh nothing.
		total += perMessageOverheadBytes
	}
	est.PromptBytes = total
	return est
}

// requestEstimate is what one parse of the body yields: the bytes that become
// prompt, and how many output tokens the request has asked to generate.
type requestEstimate struct {
	PromptBytes int64
	// OutputTokens is the caller's own declared ceiling on generation, 0 when it
	// declared none. Decode KV grows with every token produced, so a request that
	// says it wants 30k of them costs far more than the flat reserve assumes.
	OutputTokens int64
}

// requestedOutputTokens reads the caller's declared generation ceiling.
//
// It takes the LARGEST of the spellings present, because they are alternatives
// and the engine will honour whichever it recognises: OpenAI's max_tokens and
// max_completion_tokens, and Anthropic's thinking.budget_tokens, which declares
// the thinking allowance on top of the answer. Multiplied by n / best_of, since
// those generate that many independent sequences and each holds its own KV.
func requestedOutputTokens(body map[string]json.RawMessage) int64 {
	var want int64
	for _, key := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		if v := numberField(body[key]); v > want {
			want = v
		}
	}
	if thinking := objectField(body["thinking"]); thinking != nil {
		want += numberField(thinking["budget_tokens"])
	}
	if want <= 0 {
		return 0
	}

	sequences := int64(1)
	for _, key := range []string{"n", "best_of"} {
		if v := numberField(body[key]); v > sequences {
			sequences = v
		}
	}
	// Bounded so a silly n cannot overflow or swamp the budget on its own; the
	// weight is clamped to the context window afterwards regardless.
	if sequences > 8 {
		sequences = 8
	}
	return want * sequences
}

// numberField reads a JSON number, returning 0 for anything else — a missing
// field, a null, a string. A declaration this cannot read is no declaration.
func numberField(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil || f <= 0 {
		return 0
	}
	return int64(f)
}

// objectField reads a nested object, returning nil for anything else.
func objectField(raw json.RawMessage) map[string]json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

// perMessageOverheadBytes approximates the role markers and separators a chat
// template wraps each message in — a handful of tokens, expressed in the same
// byte currency as everything else here.
const perMessageOverheadBytes = 16

// textBytes returns the prompt bytes in a field that may be a plain string or a
// content-block array.
//
// A block carrying a "text" string is counted by that string. Every other block
// is counted by its whole compacted JSON, which is the point: Anthropic's
// prompt body lives in blocks this code does not enumerate — tool_result,
// thinking, tool_use, document — and an agentic conversation is mostly
// tool_result. Charging those zero would have exempted the exact traffic this
// gate exists for, and made "hide the prompt in a tool_result" a one-line
// bypass. Over-counting an unfamiliar block by its JSON overhead is the safe
// direction.
func textBytes(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return int64(len(s))
	}

	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return compactLen(raw)
	}

	var total int64
	for _, p := range parts {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(p, &fields); err != nil {
			total += compactLen(p)
			continue
		}
		if text, ok := fields["text"]; ok {
			var s string
			if err := json.Unmarshal(text, &s); err == nil {
				total += int64(len(s))
				continue
			}
		}
		total += compactLen(p)
	}
	return total
}

// compactLen is the length of raw with insignificant whitespace removed.
//
// Measuring raw length would reopen the padding hole one level down: the engine
// re-serializes tool definitions and content blocks before templating them, so
// whitespace inside those is as free to it as whitespace between top-level
// fields. Falls back to the raw length if the value will not compact, which
// only happens for input that is not valid JSON.
func compactLen(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return int64(len(raw))
	}
	return int64(buf.Len())
}
