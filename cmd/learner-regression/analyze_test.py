import importlib.util
import json
import pathlib
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("regression_analyze", pathlib.Path(__file__).with_name("analyze.py"))
analyze = importlib.util.module_from_spec(spec)
spec.loader.exec_module(analyze)


class AnalyzeTests(unittest.TestCase):
    def test_cumulative_history_and_new_fields_are_not_duplicate_records(self):
        with tempfile.TemporaryDirectory() as root:
            directory = pathlib.Path(root)
            (directory / "manifest.json").write_text(json.dumps({"mode": "completed", "courses": [{}]}))
            executions = [{"name": "data_query"}, {"name": "data_query"}, {"name": "data_query"}]
            result = {"offering_id": "course", "run_id": "run", "verified": True, "record": {"tool_calls": 3, "model_calls": 3, "tool_executions": executions}}
            (directory / "results.private.json").write_text(json.dumps([result]))
            messages = []
            for index, row in enumerate([{"id": "1", "title": "Course"}, {"id": "1", "status": "open"}, {"id": "1", "status": "open"}], 1):
                call = {"id": f"c{index}", "function": {"name": "data_query", "arguments": json.dumps({"dataset": "academic_items", "fields": list(row), "filters": [{"field": "offering_id", "operator": "eq", "value": "course"}], "offset": 0, "limit": 100})}}
                messages.extend([{"role": "assistant", "tool_calls": [call]}, {"role": "tool", "tool_call_id": call["id"], "content": json.dumps({"ok": True, "data": {"rows": [row], "has_more": False}})}])
                (directory / f"run-{index:03d}-request.private.json").write_text(json.dumps({"Messages": messages}))
            report = analyze.summarize(directory)
            case = report["cases"][0]
            self.assertEqual(case["query_row_instances_with_ids"], 3)
            self.assertEqual(case["unique_records_by_dataset"], {"academic_items": 1})
            self.assertEqual(case["calls_with_new_records"], 1)
            self.assertEqual(case["calls_with_only_new_field_values"], 1)
            self.assertEqual(case["calls_with_no_new_field_values"], 1)
            self.assertEqual(case["connection_only_queries"], 0)
            self.assertEqual(case["trace_missing_terminal_results"], 0)

    def test_retained_cancelled_result_is_distinct_from_current_attempt(self):
        with tempfile.TemporaryDirectory() as root:
            directory = pathlib.Path(root)
            (directory / "manifest.json").write_text(json.dumps({"mode": "executing", "courses": [{}, {}]}))
            results = [{"offering_id": "one", "run_id": "old", "verified": False, "error": {"code": "CANCELLED"}}, {"offering_id": "two", "run_id": "new", "verified": True}]
            (directory / "results.private.json").write_text(json.dumps(results))
            attempt = directory / "attempt-20261114"
            attempt.mkdir()
            (attempt / "results.private.json").write_text(json.dumps([results[1]]))
            summary = analyze.summarize(directory)["summary"]
            self.assertEqual(summary["retained_cancelled_results"], 1)
            self.assertEqual(summary["current_attempt_finished"], 1)
            self.assertEqual(summary["current_attempt_verified"], 1)
            self.assertEqual(summary["verified"], 1)


if __name__ == "__main__":
    unittest.main()
