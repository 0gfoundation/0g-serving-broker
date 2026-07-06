"""Tests for aiperf_gate.py — the LLM benchmark readiness gate.

Run: python3 -m unittest discover -s .github/scripts -p 'test_*.py'
Pure stdlib; a real aiperf export is used as the fixture (testdata/success_export.json).
"""

import copy
import json
import os
import tempfile
import unittest

import aiperf_gate as gate

FIXTURE = os.path.join(os.path.dirname(__file__), "testdata", "success_export.json")

# Lenient thresholds: everything should pass against the fixture.
LENIENT = {
    "max_ttft_ms": 100000.0,
    "max_latency_ms": 100000.0,
    "min_output_tps": 0.0,
    "max_error_rate": 1.0,
}


def load_fixture():
    with open(FIXTURE) as f:
        return json.load(f)


def with_ttft(data, p99):
    """The mock is non-streaming so the fixture lacks TTFT; add a streaming-shaped block."""
    d = copy.deepcopy(data)
    d["time_to_first_token"] = {"unit": "ms", "avg": p99 * 0.8, "p99": p99, "min": 1.0, "max": p99}
    return d


class LoadExportTest(unittest.TestCase):
    def test_missing_export_returns_none(self):
        with tempfile.TemporaryDirectory() as d:
            self.assertIsNone(gate.load_export(d))

    def test_finds_export_in_dir(self):
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "profile_export_aiperf.json")
            with open(path, "w") as f:
                json.dump({"request_count": {"avg": 1.0}}, f)
            self.assertEqual(gate.load_export(d), {"request_count": {"avg": 1.0}})


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

    def test_missing_ttft_is_not_ready_with_note(self):
        data = load_fixture()  # no time_to_first_token (non-streaming)
        result = gate.evaluate(data, LENIENT)
        self.assertFalse(result.ready)
        ttft_row = next(r for r in result.rows if "ttft" in r.metric.lower())
        self.assertFalse(ttft_row.passed)
        self.assertTrue(any("streaming" in n.lower() for n in result.notes))

    def test_error_rate_computed_from_error_summary(self):
        data = with_ttft(load_fixture(), 50.0)
        # 6 successful (request_count.avg) + 2 errors => rate = 2/8 = 0.25
        data["error_summary"] = [
            {"error_details": {"code": 500, "message": "boom"}, "count": 2},
        ]
        thr = dict(LENIENT, max_error_rate=0.1)
        result = gate.evaluate(data, thr)
        self.assertFalse(result.ready)
        err_row = next(r for r in result.rows if "error" in r.metric.lower())
        self.assertIn("0.25", err_row.measured)

    def test_no_errors_gives_zero_error_rate(self):
        data = with_ttft(load_fixture(), 50.0)
        result = gate.evaluate(data, dict(LENIENT, max_error_rate=0.0))
        err_row = next(r for r in result.rows if "error" in r.metric.lower())
        self.assertTrue(err_row.passed)

    def test_missing_export_is_not_ready(self):
        result = gate.evaluate(None, LENIENT)
        self.assertFalse(result.ready)
        self.assertTrue(result.notes)


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
