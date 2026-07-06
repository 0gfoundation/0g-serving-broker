#!/usr/bin/env python3
"""LLM benchmark readiness gate.

Parses an aiperf JSON export (``profile_export_aiperf.json``), compares the
measured metrics against configured thresholds, and renders a READY / NOT READY
verdict as Markdown. Written for GitHub Actions: the verdict is appended to
``$GITHUB_STEP_SUMMARY`` and written to ``$GITHUB_OUTPUT``'s companion file for
the PR-comment step.

Report-only: this script ALWAYS exits 0. Readiness is conveyed solely in the
verdict text; a NOT READY result (including a missing/unparseable export) never
fails the job.

Configuration comes from environment variables:
  ENDPOINT_URL, MODEL, CONCURRENCY, REQUEST_COUNT   — run parameters (display)
  MAX_TTFT_MS, MAX_LATENCY_MS, MIN_OUTPUT_TPS, MAX_ERROR_RATE  — thresholds
  AIPERF_ARTIFACT_DIR   — directory searched for the JSON export
  GITHUB_STEP_SUMMARY   — file the verdict table is appended to (optional)
  VERDICT_FILE          — file the comment body is written to (default verdict.md)
"""

import glob
import json
import os
from dataclasses import dataclass, field

COMMENT_MARKER = "<!-- llm-benchmark-verdict -->"
EXPORT_FILENAME = "profile_export_aiperf.json"


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
    """Return the parsed aiperf JSON export found under artifact_dir, or None.

    aiperf writes profile_export_aiperf.json directly in the artifact dir; when a
    run fails entirely it writes no export at all, so None means "no results".
    """
    matches = glob.glob(os.path.join(artifact_dir, "**", EXPORT_FILENAME), recursive=True)
    if not matches:
        return None
    try:
        with open(matches[0]) as f:
            return json.load(f)
    except (OSError, ValueError):
        return None


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


def _error_rate(data):
    """errors / (errors + successful), where errors come from error_summary counts."""
    errors = 0
    for entry in data.get("error_summary") or []:
        try:
            errors += int(entry.get("count", 0))
        except (TypeError, ValueError, AttributeError):
            continue
    successful = _stat(data, "request_count", "avg") or 0.0
    total = errors + successful
    return (errors / total) if total > 0 else 0.0


def config_error_result(message):
    """A NOT READY result for a misconfiguration (e.g. endpoint/model unset)."""
    return EvalResult(ready=False, rows=[], notes=[message])


def evaluate(data, thresholds):
    """Evaluate an aiperf export (or None) against thresholds → EvalResult."""
    if data is None:
        return EvalResult(
            ready=False,
            rows=[],
            notes=["No aiperf JSON export found — the benchmark produced no results "
                   "(the endpoint may be unreachable or every request failed)."],
        )

    rows = []
    notes = []

    # TTFT p99 — only present on streaming runs; absent ⇒ cannot confirm readiness.
    ttft = _stat(data, "time_to_first_token", "p99")
    max_ttft = thresholds["max_ttft_ms"]
    if ttft is None:
        rows.append(Row("TTFT p99 (ms)", "n/a", f"≤ {max_ttft:g}", False))
        notes.append("time_to_first_token missing from export — enable --streaming to measure TTFT.")
    else:
        rows.append(Row("TTFT p99 (ms)", f"{ttft:.1f}", f"≤ {max_ttft:g}", ttft <= max_ttft))

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

    # Error rate (lower is better) — always computable.
    rate = _error_rate(data)
    max_rate = thresholds["max_error_rate"]
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


def _thresholds_from_env():
    return {
        "max_ttft_ms": float(os.environ.get("MAX_TTFT_MS", "2000")),
        "max_latency_ms": float(os.environ.get("MAX_LATENCY_MS", "10000")),
        "min_output_tps": float(os.environ.get("MIN_OUTPUT_TPS", "10")),
        "max_error_rate": float(os.environ.get("MAX_ERROR_RATE", "0")),
    }


def _params_from_env():
    return {
        "endpoint": os.environ.get("ENDPOINT_URL", ""),
        "model": os.environ.get("MODEL", ""),
        "concurrency": os.environ.get("CONCURRENCY", ""),
        "request_count": os.environ.get("REQUEST_COUNT", ""),
    }


def main():
    config_error = os.environ.get("GATE_CONFIG_ERROR", "").strip()
    if config_error:
        result = config_error_result(config_error)
    else:
        artifact_dir = os.environ.get("AIPERF_ARTIFACT_DIR", "./aiperf-out")
        data = load_export(artifact_dir)
        result = evaluate(data, _thresholds_from_env())
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
