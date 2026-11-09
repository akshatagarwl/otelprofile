"""Offline regression tests: python3 -m unittest discover -s scripts/vs-ebpf."""
import gzip
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

SCRIPT = Path(__file__).with_name("probe-report.py")


class ProbeReportTest(unittest.TestCase):
    def report(self, compressed=False, wrong_trace=False):
        strings = ["", "samples", "count", "cpu", "nanoseconds", "process.executable.name",
                   "service.name", "qotel", "main.spinLCG", "main.burn", "main.queryDB",
                   "off_cpu", "finish_task_switch", "query-db"]
        doc = {
            "dictionary": {
                "stringTable": strings,
                "attributeTable": [],
                "functionTable": [{}, *({"nameStrindex": i} for i in (8, 9, 10, 12))],
                "locationTable": [{}, *({"lines": [{"functionIndex": i}]} for i in range(1, 5))],
                "stackTable": [{}, {"locationIndices": [1, 3]}, {"locationIndices": [1, 2, 3]},
                               {"locationIndices": [4]}],
                "linkTable": [{}, {"traceId": "trace1", "spanId": "span1"}],
            },
            "resourceProfiles": [{
                "resource": {"attributes": [
                    {"keyStrindex": 5, "value": {"stringValueStrindex": 7}},
                    {"keyStrindex": 6, "value": {"stringValueStrindex": 7}},
                ]},
                "scopeProfiles": [{
                    "scope": {"name": "go.opentelemetry.io/ebpf-profiler"},
                    "profiles": [{
                        "sampleType": {"typeStrindex": 1, "unitStrindex": 2},
                        "periodType": {"typeStrindex": 3}, "period": "20000000",
                        "samples": [{"stackIndex": 1, "timestampsUnixNano": ["1", "2"], "linkIndex": 1}],
                    }, {
                        "sampleType": {"typeStrindex": 11, "unitStrindex": 4},
                        "samples": [
                            {"stackIndex": 3, "timestampsUnixNano": ["1", "2"], "values": ["10", "30"]},
                            {"stackIndex": 1, "timestampsUnixNano": ["3"], "values": ["60"]},
                        ],
                    }],
                }, {
                    "scope": {"name": "otelprofile"},
                    "profiles": [{
                        "sampleType": {"typeStrindex": 1, "unitStrindex": 2},
                        "period": "5000000", "periodType": {"typeStrindex": 3},
                        "samples": [{"stackIndex": 2, "values": ["4"], "linkIndex": 1}],
                    }],
                }],
            }],
        }
        traces = {"resourceSpans": [{"scopeSpans": [{"spans": [{
            "traceId": "different-trace" if wrong_trace else "trace1",
            "spanId": "span1", "name": "query-db",
        }]}]}]}
        with tempfile.TemporaryDirectory() as directory:
            out = Path(directory)
            (out / "rusage.txt").write_text('qotel {"cpu_s": 0.1, "maxrss_mb": 20}\n')
            for name, data in (("profiles", doc), ("traces", traces)):
                text = json.dumps(data) + "\n"
                if compressed:
                    with gzip.open(out / (name + ".jsonl.gz"), "wt") as stream:
                        stream.write(text)
                else:
                    (out / (name + ".jsonl")).write_text(text)
            result = subprocess.check_output([sys.executable, str(SCRIPT), str(out), "qotel"], text=True)
            return json.loads(result.splitlines()[0])

    def test_counts_units_and_callers(self):
        r = self.report()
        self.assertEqual(r["ebpf_cpu_s"], 0.04)
        self.assertEqual(r["otel_cpu_s"], 0.02)
        self.assertEqual(r["ebpf_leaf_caller_kept"], "0/2")
        self.assertEqual(r["otel_leaf_caller_kept"], "4/4")
        self.assertEqual(r["offcpu_top"], [["main.spinLCG <- main.queryDB", 60.0],
                                           ["finish_task_switch", 40.0]])
        self.assertEqual(r["obi_main.queryDB"]["span_found"], 2)
        self.assertEqual(r["otel_link_main.queryDB"], {"query-db": 4})
        self.assertEqual(r["maxrss_mb"], 20)

    def test_gzip(self):
        self.assertEqual(self.report(), self.report(compressed=True))

    def test_match_trace_and_span_together(self):
        r = self.report(wrong_trace=True)
        self.assertEqual(r["obi_main.queryDB"]["span_found"], 0)
        self.assertEqual(r["obi_main.queryDB"]["linked_span"], {"<span not in traces>": 2})
        self.assertEqual(r["otel_link_main.queryDB"], {"<span not in traces>": 4})


if __name__ == "__main__":
    unittest.main()
