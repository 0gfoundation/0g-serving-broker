#!/usr/bin/env python3
"""Probe an OpenAI-compatible model endpoint and report its real capabilities.

Motivation
----------
When a provider serves a model, the broker's ``service.modelInfo`` block
(contextLength, supportedParameters, architecture, ...) is filled in by hand.
Operators frequently do not know which sampling parameters a given backend
actually honours, whether it exposes reasoning, what its real context window
is, or whether it speaks the Anthropic Messages format in addition to OpenAI.

This script talks directly to the *upstream* endpoint (the ``targetUrl`` a
provider would configure, NOT through the broker) and discovers those facts
empirically, then prints a ready-to-paste ``modelInfo`` YAML snippet.

Why "send a parameter and get 200" is not enough
------------------------------------------------
Many OpenAI-compatible servers (vLLM in particular) are *lenient*: they accept
unknown fields, return 200, and silently ignore them. So a non-error response
does not prove the parameter is honoured. The probe therefore uses two tiers:

  * Behavioural verification -- for parameters whose effect is observable
    (max_tokens, stop, n, logprobs, response_format, tools, reasoning,
    stream, ...) we send a request crafted so a supporting backend produces a
    distinguishable response, and check for that effect. Result: VERIFIED.

  * Rejection probing -- for sampling knobs whose effect can't be asserted
    deterministically (temperature, top_p, top_k, penalties, seed, ...) we can
    only observe whether the server rejects the field (4xx) or tolerates it
    (200). Result: ACCEPTED (tolerated) or REJECTED.

The report distinguishes these honestly. The suggested supportedParameters
list includes VERIFIED and ACCEPTED params (i.e. everything not rejected),
which matches how OpenRouter-style ``supported_parameters`` is populated.

Dependencies
------------
Standard library only (urllib, json, argparse). No pip install required --
this is meant to be a "just run it" maintainer tool.

Usage
-----
    python3 probe_model_capabilities.py \
        --base-url https://my-host/v1 \
        --model meta-llama/Llama-3.1-8B-Instruct \
        --api-key "$UPSTREAM_API_KEY"        # optional

    # emit only the YAML snippet (machine-friendly)
    python3 probe_model_capabilities.py --base-url ... --model ... --quiet --yaml

Environment variables (used when the matching flag is omitted):
    PROBE_BASE_URL, PROBE_MODEL, PROBE_API_KEY
"""

import argparse
import json
import os
import sys
import time
import urllib.error
import urllib.request

# ---------------------------------------------------------------------------
# Low-level HTTP
# ---------------------------------------------------------------------------


class Resp:
    """A normalized HTTP response: status, decoded JSON (or raw text), error."""

    def __init__(self, status, body_text):
        self.status = status
        self.text = body_text
        self.json = None
        try:
            self.json = json.loads(body_text) if body_text else None
        except (ValueError, TypeError):
            self.json = None

    @property
    def ok(self):
        return 200 <= self.status < 300

    def error_message(self):
        """Best-effort extraction of the server's error string."""
        if isinstance(self.json, dict):
            err = self.json.get("error")
            if isinstance(err, dict):
                return str(err.get("message") or err.get("type") or err)
            if err:
                return str(err)
            if "message" in self.json:
                return str(self.json["message"])
            if "detail" in self.json:
                return str(self.json["detail"])
        return (self.text or "")[:400]


def _do_request(url, headers, payload, timeout, stream=False):
    data = json.dumps(payload).encode("utf-8") if payload is not None else None
    method = "POST" if data is not None else "GET"
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            if stream:
                return r.status, r.read().decode("utf-8", "replace")
            return r.status, r.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", "replace")
        return e.code, body
    except (urllib.error.URLError, TimeoutError, OSError) as e:
        return 0, json.dumps({"error": {"message": "transport error: %s" % e}})


class Client:
    def __init__(self, base_url, api_key, timeout, extra_headers=None):
        self.base = base_url.rstrip("/")
        self.timeout = timeout
        self.headers = {"Content-Type": "application/json"}
        if api_key:
            self.headers["Authorization"] = "Bearer " + api_key
        if extra_headers:
            self.headers.update(extra_headers)

    def get(self, path):
        s, b = _do_request(self.base + path, self.headers, None, self.timeout)
        return Resp(s, b)

    def chat(self, payload, stream=False):
        url = self.base + "/chat/completions"
        s, b = _do_request(url, self.headers, payload, self.timeout, stream=stream)
        return Resp(s, b)

    def anthropic_messages(self, payload):
        # Anthropic Messages lives at /v1/messages. base already typically
        # ends in /v1; if not, the request will 404 and we report unsupported.
        url = self.base + "/messages"
        h = dict(self.headers)
        h.setdefault("anthropic-version", "2023-06-01")
        s, b = _do_request(url, h, payload, self.timeout)
        return Resp(s, b)


# ---------------------------------------------------------------------------
# Result model
# ---------------------------------------------------------------------------

VERIFIED = "VERIFIED"    # behaviourally confirmed to be honoured
ACCEPTED = "ACCEPTED"    # tolerated (200) but effect not asserted
REJECTED = "REJECTED"    # server returned 4xx rejecting the field
ERROR = "ERROR"          # inconclusive (transport / server error)

STATUS_MARK = {
    VERIFIED: "[+]",
    ACCEPTED: "[~]",
    REJECTED: "[-]",
    ERROR:    "[?]",
}


class ParamResult:
    def __init__(self, name, status, detail=""):
        self.name = name
        self.status = status
        self.detail = detail


# Keywords that indicate the server rejected the *parameter* specifically,
# rather than failing for an unrelated reason (auth, rate limit, etc.).
_REJECT_KEYWORDS = (
    "unknown", "unexpected", "unrecognized", "unrecognised", "not support",
    "unsupported", "extra fields", "extra_forbidden", "additional propert",
    "not permitted", "not allowed", "invalid_request_error", "no longer supported",
    "is not a valid", "unexpected keyword",
)


def _classify_rejection(resp, param):
    """Map an HTTP response to a parameter status for the rejection-probe tier."""
    if resp.ok:
        return ParamResult(param, ACCEPTED, "200 (tolerated; effect not asserted)")
    msg = resp.error_message()
    low = msg.lower()
    if resp.status in (400, 422):
        if any(k in low for k in _REJECT_KEYWORDS) or param.lower() in low:
            return ParamResult(param, REJECTED, "%d: %s" % (resp.status, msg[:160]))
        # A 400 that doesn't mention the param is ambiguous -- the request
        # might be malformed for another reason. Report as ERROR, not REJECTED,
        # so we don't wrongly drop a supported param.
        return ParamResult(param, ERROR, "%d (ambiguous): %s" % (resp.status, msg[:160]))
    return ParamResult(param, ERROR, "%d: %s" % (resp.status, msg[:160]))


# ---------------------------------------------------------------------------
# Probes
# ---------------------------------------------------------------------------


def baseline_payload(model, max_tokens=16):
    return {
        "model": model,
        "messages": [{"role": "user", "content": "Reply with the single word: ok"}],
        "max_tokens": max_tokens,
        "temperature": 0,
    }


def find_reasoning(obj):
    """Return a short description if a chat response carries reasoning, else ''."""
    try:
        choice = obj["choices"][0]
        msg = choice.get("message", {}) or {}
        if msg.get("reasoning_content"):
            return "message.reasoning_content present"
        if msg.get("reasoning"):
            return "message.reasoning present"
        usage = obj.get("usage", {}) or {}
        ctd = usage.get("completion_tokens_details", {}) or {}
        rt = ctd.get("reasoning_tokens")
        if rt:
            return "usage.completion_tokens_details.reasoning_tokens=%s" % rt
    except (KeyError, IndexError, TypeError):
        pass
    return ""


def probe_basic(client, model):
    """Confirm the endpoint works; return (ok, baseline_resp)."""
    r = client.chat(baseline_payload(model))
    return r.ok, r


def probe_models_endpoint(client, model):
    """Fetch /models. Return (context_length, raw_entry_or_None)."""
    r = client.get("/models")
    if not r.ok or not isinstance(r.json, dict):
        return None, None
    data = r.json.get("data") or []
    entry = None
    for m in data:
        if isinstance(m, dict) and m.get("id") == model:
            entry = m
            break
    if entry is None and data:
        # Some servers expose a single model under a different id; fall back to
        # the first entry but flag it by returning the entry for inspection.
        entry = data[0] if isinstance(data[0], dict) else None
    ctx = None
    if isinstance(entry, dict):
        for key in ("context_length", "max_model_len", "max_context_length"):
            v = entry.get(key)
            if isinstance(v, int) and v > 0:
                ctx = v
                break
    return ctx, entry


def behavioural_probes(client, model):
    """Run the behaviourally-verifiable probes. Returns list[ParamResult] plus
    a dict of derived facts (reasoning, streaming, tools, json, ...)."""
    results = []
    facts = {}

    # --- max_tokens: ask for a long answer but cap at 1 token -> truncation.
    p = baseline_payload(model, max_tokens=1)
    p["messages"] = [{"role": "user", "content": "Count slowly from one to fifty."}]
    p.pop("temperature", None)
    r = client.chat(p)
    if r.ok and isinstance(r.json, dict):
        try:
            ch = r.json["choices"][0]
            fin = ch.get("finish_reason")
            content = (ch.get("message", {}) or {}).get("content") or ""
            if fin == "length" or len(content.split()) <= 3:
                results.append(ParamResult("max_tokens", VERIFIED,
                                           "output truncated (finish_reason=%s)" % fin))
            else:
                results.append(ParamResult("max_tokens", ACCEPTED,
                                           "200 but truncation not observed"))
        except (KeyError, IndexError, TypeError):
            results.append(ParamResult("max_tokens", ACCEPTED, "200; unparsable choice"))
    else:
        results.append(_classify_rejection(r, "max_tokens"))

    # --- max_completion_tokens (newer OpenAI name).
    p = baseline_payload(model)
    p.pop("max_tokens", None)
    p["max_completion_tokens"] = 1
    p["messages"] = [{"role": "user", "content": "Count slowly from one to fifty."}]
    p.pop("temperature", None)
    r = client.chat(p)
    if r.ok and isinstance(r.json, dict):
        try:
            ch = r.json["choices"][0]
            content = (ch.get("message", {}) or {}).get("content") or ""
            if ch.get("finish_reason") == "length" or len(content.split()) <= 3:
                results.append(ParamResult("max_completion_tokens", VERIFIED, "output truncated"))
            else:
                results.append(ParamResult("max_completion_tokens", ACCEPTED, "200; effect unclear"))
        except (KeyError, IndexError, TypeError):
            results.append(ParamResult("max_completion_tokens", ACCEPTED, "200"))
    else:
        results.append(_classify_rejection(r, "max_completion_tokens"))

    # --- stop: stop right after the first word.
    p = baseline_payload(model, max_tokens=32)
    p["messages"] = [{"role": "user", "content": "Say: alpha beta gamma"}]
    p["stop"] = ["beta"]
    p.pop("temperature", None)
    r = client.chat(p)
    if r.ok and isinstance(r.json, dict):
        try:
            content = (r.json["choices"][0].get("message", {}) or {}).get("content") or ""
            if "beta" not in content.lower():
                results.append(ParamResult("stop", VERIFIED, "stop sequence honoured"))
            else:
                results.append(ParamResult("stop", ACCEPTED, "200; stop not observed"))
        except (KeyError, IndexError, TypeError):
            results.append(ParamResult("stop", ACCEPTED, "200"))
    else:
        results.append(_classify_rejection(r, "stop"))

    # --- n: request 2 completions.
    p = baseline_payload(model, max_tokens=4)
    p["n"] = 2
    r = client.chat(p)
    if r.ok and isinstance(r.json, dict):
        n = len(r.json.get("choices") or [])
        if n >= 2:
            results.append(ParamResult("n", VERIFIED, "returned %d choices" % n))
        else:
            results.append(ParamResult("n", ACCEPTED, "200 but only %d choice" % n))
    else:
        results.append(_classify_rejection(r, "n"))

    # --- logprobs / top_logprobs.
    p = baseline_payload(model, max_tokens=4)
    p["logprobs"] = True
    p["top_logprobs"] = 2
    r = client.chat(p)
    if r.ok and isinstance(r.json, dict):
        try:
            lp = r.json["choices"][0].get("logprobs")
            if lp:
                results.append(ParamResult("logprobs", VERIFIED, "logprobs returned"))
                results.append(ParamResult("top_logprobs", VERIFIED, "top_logprobs returned"))
            else:
                results.append(ParamResult("logprobs", ACCEPTED, "200; no logprobs in response"))
        except (KeyError, IndexError, TypeError):
            results.append(ParamResult("logprobs", ACCEPTED, "200"))
    else:
        results.append(_classify_rejection(r, "logprobs"))

    # --- response_format: JSON object mode.
    p = baseline_payload(model, max_tokens=64)
    p["messages"] = [{"role": "user",
                      "content": 'Return a JSON object with a key "city" set to "Paris".'}]
    p["response_format"] = {"type": "json_object"}
    p.pop("temperature", None)
    r = client.chat(p)
    if r.ok and isinstance(r.json, dict):
        try:
            content = (r.json["choices"][0].get("message", {}) or {}).get("content") or ""
            json.loads(content)
            results.append(ParamResult("response_format", VERIFIED, "valid JSON returned"))
            facts["json_mode"] = True
        except (KeyError, IndexError, TypeError, ValueError):
            results.append(ParamResult("response_format", ACCEPTED, "200; content not valid JSON"))
    else:
        results.append(_classify_rejection(r, "response_format"))

    # --- tools / tool_choice (function calling).
    p = baseline_payload(model, max_tokens=64)
    p["messages"] = [{"role": "user", "content": "What is the weather in Paris? Use the tool."}]
    p["tools"] = [{
        "type": "function",
        "function": {
            "name": "get_weather",
            "description": "Get the current weather for a city",
            "parameters": {
                "type": "object",
                "properties": {"city": {"type": "string"}},
                "required": ["city"],
            },
        },
    }]
    p["tool_choice"] = "auto"
    p.pop("temperature", None)
    r = client.chat(p)
    if r.ok and isinstance(r.json, dict):
        try:
            msg = r.json["choices"][0].get("message", {}) or {}
            if msg.get("tool_calls"):
                results.append(ParamResult("tools", VERIFIED, "tool_calls emitted"))
                results.append(ParamResult("tool_choice", VERIFIED, "honoured"))
                facts["tools"] = True
            else:
                results.append(ParamResult("tools", ACCEPTED,
                                           "200 but no tool_calls (model may still support tools)"))
        except (KeyError, IndexError, TypeError):
            results.append(ParamResult("tools", ACCEPTED, "200"))
    else:
        results.append(_classify_rejection(r, "tools"))

    # --- reasoning: OpenAI-style reasoning_effort.
    reasoning_status = None
    p = baseline_payload(model, max_tokens=256)
    p["messages"] = [{"role": "user",
                      "content": "If a train travels 60 km in 1.5 hours, what is its average speed? Think step by step."}]
    p["reasoning_effort"] = "low"
    p.pop("temperature", None)
    r = client.chat(p)
    if r.ok and isinstance(r.json, dict):
        desc = find_reasoning(r.json)
        if desc:
            results.append(ParamResult("reasoning", VERIFIED, "reasoning_effort -> " + desc))
            facts["reasoning"] = desc
            reasoning_status = VERIFIED
        else:
            results.append(ParamResult("reasoning", ACCEPTED,
                                       "reasoning_effort accepted (200) but no reasoning field in response"))
            reasoning_status = ACCEPTED
    else:
        results.append(_classify_rejection(r, "reasoning"))
        reasoning_status = REJECTED

    # If reasoning_effort was rejected, try the OpenRouter/Anthropic-style
    # `reasoning` object before concluding the model can't reason.
    if reasoning_status == REJECTED:
        p = baseline_payload(model, max_tokens=256)
        p["messages"] = [{"role": "user",
                          "content": "If a train travels 60 km in 1.5 hours, what is its average speed? Think step by step."}]
        p["reasoning"] = {"effort": "low"}
        p.pop("temperature", None)
        r = client.chat(p)
        if r.ok and isinstance(r.json, dict):
            desc = find_reasoning(r.json)
            if desc:
                # Replace the earlier REJECTED entry.
                results = [x for x in results if x.name != "reasoning"]
                results.append(ParamResult("reasoning", VERIFIED, "reasoning{} -> " + desc))
                facts["reasoning"] = desc

    # --- streaming.
    p = baseline_payload(model, max_tokens=8)
    p["stream"] = True
    r = client.chat(p, stream=True)
    if r.ok and ("data:" in r.text or "chat.completion.chunk" in r.text):
        results.append(ParamResult("stream", VERIFIED, "SSE chunks received"))
        facts["streaming"] = True
    elif r.ok:
        results.append(ParamResult("stream", ACCEPTED, "200 but no SSE detected"))
    else:
        results.append(_classify_rejection(r, "stream"))

    return results, facts


# Sampling knobs we can only rejection-probe (effect not deterministically
# assertable). Each maps to a representative value.
REJECTION_PROBE_PARAMS = [
    ("temperature", 0.7),
    ("top_p", 0.9),
    ("top_k", 40),
    ("min_p", 0.05),
    ("top_a", 0.0),
    ("frequency_penalty", 0.5),
    ("presence_penalty", 0.5),
    ("repetition_penalty", 1.1),
    ("seed", 42),
    ("logit_bias", {"123": -100}),
]


def rejection_probes(client, model):
    results = []
    for name, value in REJECTION_PROBE_PARAMS:
        p = baseline_payload(model, max_tokens=4)
        p.pop("temperature", None)  # avoid clashing with the probed temperature
        p[name] = value
        r = client.chat(p)
        results.append(_classify_rejection(r, name))
        time.sleep(0.05)
    return results


def probe_anthropic(client, model):
    """Best-effort check for /v1/messages (Anthropic Messages) support."""
    payload = {
        "model": model,
        "max_tokens": 8,
        "messages": [{"role": "user", "content": "Reply with: ok"}],
    }
    r = client.anthropic_messages(payload)
    if r.ok and isinstance(r.json, dict) and r.json.get("type") == "message":
        return True, "200 with Anthropic message shape"
    if r.ok:
        return True, "200 (shape unverified)"
    return False, "%d: %s" % (r.status, r.error_message()[:120])


# ---------------------------------------------------------------------------
# Reporting
# ---------------------------------------------------------------------------


def supported_param_list(all_results):
    """Params to advertise: everything VERIFIED or ACCEPTED, deduped, ordered."""
    seen = {}
    for r in all_results:
        if r.status in (VERIFIED, ACCEPTED):
            # Prefer VERIFIED over ACCEPTED if a param appears twice.
            if r.name not in seen or r.status == VERIFIED:
                seen[r.name] = r.status
    return list(seen.keys())


def yaml_snippet(model, context_length, supported, facts, anthropic_ok):
    lines = []
    lines.append("  modelInfo:")
    lines.append('    name: "%s"' % model)
    lines.append('    description: "TODO: human-readable description"')
    if context_length:
        lines.append("    contextLength: %d" % context_length)
    else:
        lines.append("    # contextLength: <unknown — set manually>")
    lines.append("    architecture:")
    lines.append('      modality: "text->text"')
    lines.append('      inputModalities: ["text"]')
    lines.append('      outputModalities: ["text"]')
    lines.append("    supportedParameters:")
    for p in supported:
        lines.append("      - %s" % p)
    formats = ["openai"]
    if anthropic_ok:
        formats.append("anthropic")
    lines.append("    supportedFormats: [%s]" % ", ".join('"%s"' % f for f in formats))
    if facts.get("reasoning"):
        lines.append("    # reasoning: detected (%s)" % facts["reasoning"])
    return "\n".join(lines)


def print_report(model, base_url, context_length, models_entry,
                 behav, reject, facts, anthropic_ok, anthropic_detail):
    out = sys.stdout

    def w(s=""):
        out.write(s + "\n")

    w("=" * 70)
    w("Model capability probe")
    w("=" * 70)
    w("endpoint : %s" % base_url)
    w("model    : %s" % model)
    w("context  : %s" % (str(context_length) if context_length else "unknown (set manually)"))
    if facts.get("reasoning"):
        w("reasoning: YES (%s)" % facts["reasoning"])
    else:
        w("reasoning: not detected")
    w("anthropic /v1/messages: %s (%s)" % ("YES" if anthropic_ok else "no", anthropic_detail))
    w("streaming: %s" % ("yes" if facts.get("streaming") else "not detected"))
    w("tools    : %s" % ("yes" if facts.get("tools") else "not confirmed"))
    w("json mode: %s" % ("yes" if facts.get("json_mode") else "not confirmed"))
    w("")
    w("Parameter probe results")
    w("-" * 70)
    w("  legend: [+] verified  [~] accepted/tolerated  [-] rejected  [?] inconclusive")
    w("")
    w("  behavioural (effect observed):")
    for r in behav:
        w("    %s %-22s %s" % (STATUS_MARK[r.status], r.name, r.detail))
    w("")
    w("  sampling knobs (rejection probe only):")
    for r in reject:
        w("    %s %-22s %s" % (STATUS_MARK[r.status], r.name, r.detail))
    w("")
    w("Suggested modelInfo (paste into provider config, review TODOs):")
    w("-" * 70)


def main(argv):
    ap = argparse.ArgumentParser(
        description="Probe an OpenAI-compatible endpoint for real model capabilities.")
    ap.add_argument("--base-url", default=os.getenv("PROBE_BASE_URL"),
                    help="OpenAI-compatible base URL, e.g. https://host/v1 (env PROBE_BASE_URL)")
    ap.add_argument("--model", default=os.getenv("PROBE_MODEL"),
                    help="Model id to probe (env PROBE_MODEL)")
    ap.add_argument("--api-key", default=os.getenv("PROBE_API_KEY"),
                    help="Bearer token for the upstream, if required (env PROBE_API_KEY)")
    ap.add_argument("--header", action="append", default=[],
                    metavar="K:V", help="Extra header (repeatable), e.g. --header 'X-Foo:bar'")
    ap.add_argument("--timeout", type=float, default=60.0, help="Per-request timeout seconds")
    ap.add_argument("--no-anthropic", action="store_true",
                    help="Skip the Anthropic /v1/messages format probe")
    ap.add_argument("--yaml", action="store_true",
                    help="Print only the modelInfo YAML snippet to stdout")
    ap.add_argument("--json", dest="as_json", action="store_true",
                    help="Print the full result as JSON to stdout")
    ap.add_argument("--quiet", action="store_true",
                    help="Suppress the human-readable report (use with --yaml/--json)")
    args = ap.parse_args(argv)

    if not args.base_url or not args.model:
        ap.error("--base-url and --model are required (or set PROBE_BASE_URL / PROBE_MODEL)")

    extra = {}
    for h in args.header:
        if ":" not in h:
            ap.error("--header must be K:V, got %r" % h)
        k, v = h.split(":", 1)
        extra[k.strip()] = v.strip()

    client = Client(args.base_url, args.api_key, args.timeout, extra)

    ok, baseline = probe_basic(client, args.model)
    if not ok:
        sys.stderr.write(
            "FATAL: baseline chat request failed (%d): %s\n"
            "The endpoint/model/auth must work before capabilities can be probed.\n"
            % (baseline.status, baseline.error_message()))
        return 2

    context_length, models_entry = probe_models_endpoint(client, args.model)
    behav, facts = behavioural_probes(client, args.model)
    reject = rejection_probes(client, args.model)

    anthropic_ok, anthropic_detail = (False, "skipped")
    if not args.no_anthropic:
        anthropic_ok, anthropic_detail = probe_anthropic(client, args.model)

    all_results = behav + reject
    supported = supported_param_list(all_results)
    snippet = yaml_snippet(args.model, context_length, supported, facts, anthropic_ok)

    if args.as_json:
        payload = {
            "endpoint": args.base_url,
            "model": args.model,
            "context_length": context_length,
            "reasoning": facts.get("reasoning", ""),
            "streaming": bool(facts.get("streaming")),
            "tools": bool(facts.get("tools")),
            "json_mode": bool(facts.get("json_mode")),
            "anthropic_messages": anthropic_ok,
            "supported_parameters": supported,
            "parameters": [
                {"name": r.name, "status": r.status, "detail": r.detail}
                for r in all_results
            ],
        }
        print(json.dumps(payload, indent=2))
        return 0

    if args.yaml and args.quiet:
        print(snippet)
        return 0

    if not args.quiet:
        print_report(args.model, args.base_url, context_length, models_entry,
                     behav, reject, facts, anthropic_ok, anthropic_detail)
    print(snippet)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
