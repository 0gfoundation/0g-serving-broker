#!/usr/bin/env python3
"""LLM benchmark readiness gate.

Parses an aiperf JSON export (``profile_export_aiperf.json``), compares the
measured metrics against configured thresholds, and renders a READY / NOT READY
verdict as Markdown. Written for GitHub Actions: the verdict is appended to
``$GITHUB_STEP_SUMMARY`` and written to ``$VERDICT_FILE`` (default
``verdict.md``) for the PR-comment step.

Report-only: this script ALWAYS exits 0. Readiness is conveyed solely in the
verdict text; a NOT READY result (including a missing/unparseable export or a
misconfigured threshold) never fails the job.

Configuration comes from environment variables:
  ENDPOINT_URL, MODEL, CONCURRENCY, REQUEST_COUNT   — run parameters (display)
  STREAMING             — "true"/"false"; when false, absent TTFT is informational
  MAX_TTFT_MS, MAX_LATENCY_MS, MIN_OUTPUT_TPS, MAX_ERROR_RATE  — thresholds
  AIPERF_ARTIFACT_DIR   — directory searched for the JSON export
  GATE_CONFIG_ERROR     — non-empty ⇒ render a "not configured" verdict and stop
  GITHUB_STEP_SUMMARY   — file the verdict table is appended to (optional)
  VERDICT_FILE          — file the comment body is written to (default verdict.md)
"""

import glob
import json
import os
from dataclasses import dataclass, field

COMMENT_MARKER = "<!-- llm-benchmark-verdict -->"
EXPORT_FILENAME = "profile_export_aiperf.json"

# Threshold env vars with defaults. Kept in one table so parse errors can name
# the exact variable. An empty string (GitHub sets empty, not unset, when an
# expression is empty) falls back to the default.
THRESHOLD_SPEC = {
    "max_ttft_ms": ("MAX_TTFT_MS", "2000"),
    "max_latency_ms": ("MAX_LATENCY_MS", "10000"),
    "min_output_tps": ("MIN_OUTPUT_TPS", "10"),
    "max_error_rate": ("MAX_ERROR_RATE", "0"),
}


@dataclass
class Row:
    metric: str
    measured: str
    threshold: str
    passed: bool


@dataclass
class EvalResult:
    ready: bool
    rows: list = field(default_factory=list)
    notes: list = field(default_factory=list)


def load_export(artifact_dir):
    """Locate and parse the aiperf JSON export under artifact_dir.

    Returns (data, note):
      (dict, None)  — export found and parsed
      (None, None)  — no export file (aiperf writes none when every request fails)
      (None, str)   — file present but unusable; note says why (never silently
                      conflated with the missing-file case)
    """
    matches = glob.glob(os.path.join(artifact_dir, "**", EXPORT_FILENAME), recursive=True)
    if not matches:
        return None, None
    path = matches[0]
    try:
        with open(path) as f:
            data = json.load(f)
    except (OSError, ValueError) as e:
        return None, f"export file present but unparseable ({path}): {e}"
    if not isinstance(data, dict):
        return None, (f"export JSON has unexpected structure ({path}): "
                      f"expected an object, got {type(data).__name__}")
    return data, None


def parse_thresholds(env):
    """Parse threshold env vars → (thresholds, None) or (None, error_note).

    Never raises: a non-numeric value yields an error note naming the variable
    so main() can render a NOT READY verdict instead of crashing.
    """
    out = {}
    for key, (var, default) in THRESHOLD_SPEC.items():
        raw = (env.get(var) or "").strip() or default
        try:
            out[key] = float(raw)
        except ValueError:
            return None, (f"threshold {var} has invalid value {raw!r} — "
                          f"expected a number.")
    return out, None


def _stat(data, metric, stat):
    """Return data[metric][stat] as a float, or None if absent/non-numeric."""
    m = data.get(metric)
    if not isinstance(m, dict):
        return None
    v = m.get(stat)
    try:
        return float(v)
    except (TypeError, ValueError):
        return None


def config_error_result(message):
    """A NOT READY result for a misconfiguration (e.g. endpoint/model unset)."""
    return EvalResult(ready=False, rows=[], notes=[message])


def evaluate(data, thresholds, streaming=True, data_note=None):
    """Evaluate an aiperf export (or None) against thresholds → EvalResult.

    streaming=False marks an absent TTFT as informational rather than failing:
    non-streaming endpoints are a supported configuration and must not be
    permanently NOT READY. Every other missing metric fails closed.
    """
    if data is None:
        return EvalResult(
            ready=False,
            rows=[],
            notes=[data_note or
                   "No aiperf JSON export found — the benchmark produced no results "
                   "(the endpoint may be unreachable or every request failed)."],
        )

    rows = []
    notes = []

    # TTFT p99 — only present on streaming runs. If measured, always evaluate;
    # if absent, fail closed only when streaming was requested.
    ttft = _stat(data, "time_to_first_token", "p99")
    max_ttft = thresholds["max_ttft_ms"]
    if ttft is not None:
        rows.append(Row("TTFT p99 (ms)", f"{ttft:.1f}", f"≤ {max_ttft:g}", ttft <= max_ttft))
    elif streaming:
        rows.append(Row("TTFT p99 (ms)", "n/a", f"≤ {max_ttft:g}", False))
        notes.append("time_to_first_token missing from export despite streaming mode — "
                     "the endpoint may not stream; TTFT cannot be verified.")
    else:
        rows.append(Row("TTFT p99 (ms)", "n/a (non-streaming)", "—", True))

    # Request latency p99 (lower is better).
    latency = _stat(data, "request_latency", "p99")
    max_latency = thresholds["max_latency_ms"]
    if latency is None:
        rows.append(Row("Request latency p99 (ms)", "n/a", f"≤ {max_latency:g}", False))
        notes.append("request_latency missing from export.")
    else:
        rows.append(Row("Request latency p99 (ms)", f"{latency:.1f}",
                        f"≤ {max_latency:g}", latency <= max_latency))

    # Output token throughput avg (higher is better).
    tps = _stat(data, "output_token_throughput", "avg")
    min_tps = thresholds["min_output_tps"]
    if tps is None:
        rows.append(Row("Output token throughput (tok/s)", "n/a", f"≥ {min_tps:g}", False))
        notes.append("output_token_throughput missing from export.")
    else:
        rows.append(Row("Output token throughput (tok/s)", f"{tps:.1f}",
                        f"≥ {min_tps:g}", tps >= min_tps))

    # Error rate (lower is better). Fails CLOSED when the fields are absent —
    # schema drift must never render a silent "0.00 ✅" (false READY).
    # aiperf 0.10.0 semantics (pinned by testdata/partial_failure_export.json):
    # request_count.avg counts only successful requests; errors live in
    # error_summary as [{error_details, count}].
    max_rate = thresholds["max_error_rate"]
    err_summary = data.get("error_summary")
    successful = _stat(data, "request_count", "avg")
    if not isinstance(err_summary, list) or successful is None:
        rows.append(Row("Error rate", "n/a", f"≤ {max_rate:g}", False))
        notes.append("error_summary/request_count missing from export — "
                     "cannot compute error rate.")
    else:
        errors = 0
        for entry in err_summary:
            try:
                errors += int(entry.get("count", 0))
            except (TypeError, ValueError, AttributeError):
                errors += 1  # malformed entry still counts as at least one error
                notes.append("error_summary contains a malformed entry — "
                             "counted as one error.")
        total = errors + successful
        rate = (errors / total) if total > 0 else 0.0
        rows.append(Row("Error rate", f"{rate:.2f}", f"≤ {max_rate:g}", rate <= max_rate))

    ready = all(r.passed for r in rows)
    return EvalResult(ready=ready, rows=rows, notes=notes)


def render_markdown(result, params):
    """Render the verdict block (marker + heading + params + table + notes)."""
    verdict = "✅ READY" if result.ready else "❌ NOT READY"
    lines = [
        COMMENT_MARKER,
        f"## LLM Service Benchmark — {verdict}",
        "",
        f"**Endpoint:** {params.get('endpoint', '')}  "
        f"**Model:** {params.get('model', '')}  "
        f"**Concurrency:** {params.get('concurrency', '')}  "
        f"**Requests:** {params.get('request_count', '')}",
        "",
    ]
    if result.rows:
        lines.append("| Metric | Measured | Threshold | Result |")
        lines.append("|---|---|---|---|")
        for r in result.rows:
            mark = "✅" if r.passed else "❌"
            lines.append(f"| {r.metric} | {r.measured} | {r.threshold} | {mark} |")
        lines.append("")
    for note in result.notes:
        lines.append(f"> ⚠️ {note}")
    return "\n".join(lines).rstrip() + "\n"


def _params_from_env():
    return {
        "endpoint": os.environ.get("ENDPOINT_URL", ""),
        "model": os.environ.get("MODEL", ""),
        "concurrency": os.environ.get("CONCURRENCY", ""),
        "request_count": os.environ.get("REQUEST_COUNT", ""),
    }


def main():
    config_error = os.environ.get("GATE_CONFIG_ERROR", "").strip()
    thresholds, threshold_error = parse_thresholds(os.environ)
    if config_error:
        result = config_error_result(config_error)
    elif threshold_error:
        result = config_error_result(threshold_error)
    else:
        streaming = os.environ.get("STREAMING", "true").strip().lower() == "true"
        artifact_dir = os.environ.get("AIPERF_ARTIFACT_DIR", "./aiperf-out")
        data, data_note = load_export(artifact_dir)
        result = evaluate(data, thresholds, streaming=streaming, data_note=data_note)
    md = render_markdown(result, _params_from_env())

    # Console (visible in the Actions log).
    print(md)

    # Step Summary.
    summary = os.environ.get("GITHUB_STEP_SUMMARY")
    if summary:
        with open(summary, "a") as f:
            f.write(md)

    # Comment body for the PR-comment step.
    verdict_file = os.environ.get("VERDICT_FILE", "verdict.md")
    with open(verdict_file, "w") as f:
        f.write(md)

    # Report-only: never fail the job.
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
