"""Tests for aiperf_gate.py — the LLM benchmark readiness gate.

Run: python3 -m unittest discover -s .github/scripts -p 'test_*.py'
Pure stdlib. Fixtures are real aiperf 0.10.0 exports captured against the
OpenAI-compatible mock in api/e2e-lora-serving/mock_sllm.py:
  - testdata/success_export.json: 6/6 requests succeeded
  - testdata/partial_failure_export.json: 9 requests, 3 injected HTTP 500s
    (pins the semantic that request_count.avg counts ONLY successful requests)
"""

import copy
import json
import os
import tempfile
import unittest

import aiperf_gate as gate

TESTDATA = os.path.join(os.path.dirname(__file__), "testdata")
FIXTURE = os.path.join(TESTDATA, "success_export.json")
PARTIAL_FIXTURE = os.path.join(TESTDATA, "partial_failure_export.json")

# Lenient thresholds: everything should pass against the fixture.
LENIENT = {
    "max_ttft_ms": 100000.0,
    "max_latency_ms": 100000.0,
    "min_output_tps": 0.0,
    "max_error_rate": 1.0,
}


def load_fixture(path=FIXTURE):
    with open(path) as f:
        return json.load(f)


def with_ttft(data, p99):
    """The mock is non-streaming so the fixture lacks TTFT; add a streaming-shaped block."""
    d = copy.deepcopy(data)
    d["time_to_first_token"] = {"unit": "ms", "avg": p99 * 0.8, "p99": p99, "min": 1.0, "max": p99}
    return d


class LoadExportTest(unittest.TestCase):
    def test_missing_export_returns_none_without_note(self):
        with tempfile.TemporaryDirectory() as d:
            data, note = gate.load_export(d)
            self.assertIsNone(data)
            self.assertIsNone(note)

    def test_finds_export_in_dir(self):
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "profile_export_aiperf.json")
            with open(path, "w") as f:
                json.dump({"request_count": {"avg": 1.0}}, f)
            data, note = gate.load_export(d)
            self.assertEqual(data, {"request_count": {"avg": 1.0}})
            self.assertIsNone(note)

    def test_corrupt_export_returns_unparseable_note(self):
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "profile_export_aiperf.json")
            with open(path, "w") as f:
                f.write('{"truncated": ')
            data, note = gate.load_export(d)
            self.assertIsNone(data)
            self.assertIn("unparseable", note)

    def test_non_dict_export_returns_note(self):
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "profile_export_aiperf.json")
            with open(path, "w") as f:
                json.dump([1, 2, 3], f)
            data, note = gate.load_export(d)
            self.assertIsNone(data)
            self.assertIn("unexpected structure", note)


class ParseThresholdsTest(unittest.TestCase):
    def test_defaults_when_unset_or_empty(self):
        # GitHub Actions sets env vars to "" (not unset) when the expression is empty.
        thr, err = gate.parse_thresholds({"MAX_TTFT_MS": "", "MIN_OUTPUT_TPS": ""})
        self.assertIsNone(err)
        self.assertEqual(thr["max_ttft_ms"], 2000.0)
        self.assertEqual(thr["min_output_tps"], 10.0)

    def test_valid_values_parsed(self):
        thr, err = gate.parse_thresholds({"MAX_TTFT_MS": "3500", "MAX_ERROR_RATE": "0.05"})
        self.assertIsNone(err)
        self.assertEqual(thr["max_ttft_ms"], 3500.0)
        self.assertEqual(thr["max_error_rate"], 0.05)

    def test_invalid_value_returns_error_not_crash(self):
        for bad in ("2,000", "2000ms", "abc"):
            thr, err = gate.parse_thresholds({"MAX_TTFT_MS": bad})
            self.assertIsNone(thr)
            self.assertIn("MAX_TTFT_MS", err)
            self.assertIn(bad, err)


class EvaluateTest(unittest.TestCase):
    def test_all_pass_is_ready(self):
        data = with_ttft(load_fixture(), 50.0)
        result = gate.evaluate(data, LENIENT)
        self.assertTrue(result.ready)
        self.assertTrue(all(r.passed for r in result.rows))

    def test_latency_over_threshold_not_ready(self):
        # fixture request_latency p99 ~= 5.11 ms
        data = with_ttft(load_fixture(), 50.0)
        thr = dict(LENIENT, max_latency_ms=1.0)
        result = gate.evaluate(data, thr)
        self.assertFalse(result.ready)
        latency_row = next(r for r in result.rows if "latency" in r.metric.lower())
        self.assertFalse(latency_row.passed)

    def test_throughput_below_min_not_ready(self):
        data = with_ttft(load_fixture(), 50.0)
        thr = dict(LENIENT, min_output_tps=1e12)
        result = gate.evaluate(data, thr)
        self.assertFalse(result.ready)

    def test_ttft_over_threshold_not_ready(self):
        data = with_ttft(load_fixture(), 5000.0)
        thr = dict(LENIENT, max_ttft_ms=2000.0)
        result = gate.evaluate(data, thr)
        self.assertFalse(result.ready)

    def test_missing_ttft_streaming_is_not_ready_with_note(self):
        data = load_fixture()  # no time_to_first_token
        result = gate.evaluate(data, LENIENT, streaming=True)
        self.assertFalse(result.ready)
        ttft_row = next(r for r in result.rows if "ttft" in r.metric.lower())
        self.assertFalse(ttft_row.passed)
        self.assertTrue(any("streaming" in n.lower() for n in result.notes))

    def test_missing_ttft_non_streaming_is_informational(self):
        # streaming=false is a supported config; absent TTFT must not force NOT READY.
        data = load_fixture()
        result = gate.evaluate(data, LENIENT, streaming=False)
        self.assertTrue(result.ready)
        ttft_row = next(r for r in result.rows if "ttft" in r.metric.lower())
        self.assertTrue(ttft_row.passed)
        self.assertIn("non-streaming", ttft_row.measured)

    def test_present_ttft_evaluated_even_if_non_streaming(self):
        data = with_ttft(load_fixture(), 5000.0)
        thr = dict(LENIENT, max_ttft_ms=2000.0)
        result = gate.evaluate(data, thr, streaming=False)
        self.assertFalse(result.ready)

    def test_error_rate_from_real_partial_failure_export(self):
        # Real aiperf 0.10.0 export: 9 requests, 3 failed → request_count.avg=6,
        # error_summary count=3 → rate 3/9 ≈ 0.33.
        data = with_ttft(load_fixture(PARTIAL_FIXTURE), 50.0)
        thr = dict(LENIENT, max_error_rate=0.1)
        result = gate.evaluate(data, thr)
        self.assertFalse(result.ready)
        err_row = next(r for r in result.rows if "error" in r.metric.lower())
        self.assertIn("0.33", err_row.measured)

    def test_error_rate_fails_closed_when_fields_missing(self):
        # Schema drift must never produce a silent 0.00 ✅ (false READY).
        data = with_ttft(load_fixture(), 50.0)
        del data["error_summary"]
        del data["request_count"]
        result = gate.evaluate(data, LENIENT)
        self.assertFalse(result.ready)
        err_row = next(r for r in result.rows if "error" in r.metric.lower())
        self.assertFalse(err_row.passed)
        self.assertEqual(err_row.measured, "n/a")

    def test_no_errors_gives_zero_error_rate(self):
        data = with_ttft(load_fixture(), 50.0)
        result = gate.evaluate(data, dict(LENIENT, max_error_rate=0.0))
        err_row = next(r for r in result.rows if "error" in r.metric.lower())
        self.assertTrue(err_row.passed)

    def test_missing_export_is_not_ready(self):
        result = gate.evaluate(None, LENIENT)
        self.assertFalse(result.ready)
        self.assertTrue(result.notes)

    def test_data_note_overrides_default_missing_message(self):
        result = gate.evaluate(None, LENIENT, data_note="export file present but unparseable: boom")
        self.assertFalse(result.ready)
        self.assertTrue(any("unparseable" in n for n in result.notes))
        self.assertFalse(any("unreachable" in n for n in result.notes))


class ConfigErrorTest(unittest.TestCase):
    def test_config_error_result_is_not_ready_with_message(self):
        result = gate.config_error_result("endpoint_url and model must be provided.")
        self.assertFalse(result.ready)
        self.assertEqual(result.rows, [])
        self.assertTrue(any("endpoint_url" in n for n in result.notes))


class RenderTest(unittest.TestCase):
    def test_render_has_marker_and_verdict(self):
        data = with_ttft(load_fixture(), 50.0)
        result = gate.evaluate(data, LENIENT)
        md = gate.render_markdown(result, {"endpoint": "http://x", "model": "m",
                                           "concurrency": "2", "request_count": "6"})
        self.assertIn(gate.COMMENT_MARKER, md)
        self.assertIn("READY", md)
        self.assertIn("http://x", md)

    def test_render_not_ready_verdict(self):
        result = gate.evaluate(None, LENIENT)
        md = gate.render_markdown(result, {"endpoint": "", "model": "",
                                           "concurrency": "", "request_count": ""})
        self.assertIn("NOT READY", md)


if __name__ == "__main__":
    unittest.main()
