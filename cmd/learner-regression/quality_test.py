#!/usr/bin/env python3
"""Self-contained offline tests; no regression artifacts, database or models."""
import copy
import importlib.util
import json
import pathlib
import tempfile
import unittest

SPEC = importlib.util.spec_from_file_location("learner_quality", pathlib.Path(__file__).with_name("quality.py"))
quality = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(quality)


def query(dataset, fields, filters, offset=0):
    return {"dataset": dataset, "fields": fields, "filters": filters, "limit": 100, "offset": offset}


def eq(field, value):
    return {"field": field, "operator": "eq", "value": value}


def call(identity, name, args):
    return {"id": identity, "function": {"name": name, "arguments": json.dumps(args)}}


def result(data, ok=True):
    return {"ok": ok, "data": data, "error": None if ok else {"code": "INVALID_ARGUMENT"}}


def fixture():
    root = query("academic_offerings", ["id", "connection_id", "source", "course_ref"], [eq("connection_id", "conn"), eq("id", "off")])
    items = query("academic_items", ["id", "connection_id", "offering_id", "title", "observed_at"], [eq("connection_id", "conn"), eq("offering_id", "off")])
    seeds = [{"category": name, "title": "Private title", "content": "Private fact", "source_refs": [{"dataset": "academic_items", "id": "one"}], "memory_type": "TEMP", "is_personal": False, "structured_content": {"as_of": "2026-11-14"}} for name in ("profile_index", *quality.TOPICS)]
    seeds[0]["structured_content"]["coverage"] = {"academic_items_rows": 2}
    definition = {"entity": {"id": "source=chaoxing|connection=conn|course_ref=ref"}, "profile_manifest": {"query_coverage": [root, items]}, "goal": "g", "applicability": {"kind": "course"}, "instructions": ["read"], "input_contract": {"required": ["task"]}, "output_contract": {"status": "done"}}
    args = {"definition": definition, "initial_private_memories": seeds}
    receipt = result({"workflow_id": "workflow", "profile_initialization": {"verified": True, "owner_workflow_id": "workflow", "count": len(seeds), "memory_ids": [str(i) for i in range(len(seeds))]}})
    evidence = quality.Evidence()
    evidence.observe(call("root", "data_query", root), result({"rows": [{"id": "off", "connection_id": "conn", "source": "chaoxing", "course_ref": "ref"}], "has_more": False}))
    evidence.observe(call("items", "data_query", items), result({"rows": [{"id": "one", "title": "private", "observed_at": "2026-11-14T01:00:00+08:00"}, {"id": "two", "title": "private"}], "common_fields": {"connection_id": "conn", "offering_id": "off"}, "has_more": False}))
    return root, items, args, receipt, evidence


class QualityTests(unittest.TestCase):
    def test_persistence_and_semantics_are_distinct(self):
        _, _, args, receipt, evidence = fixture()
        self.assertTrue(quality.persistence_receipt(args, receipt))
        args["definition"] = {"entity": args["definition"]["entity"], "profile_manifest": args["definition"]["profile_manifest"]}
        review = quality.review_create("run", "create", args, receipt, evidence)
        self.assertTrue(review["persistence_verified"])
        self.assertEqual(review["review_codes"]["workflow_execution_contract_missing"], 5)
        self.assertEqual(review["semantic_review"], "review_required")

    def test_counts_common_fields_and_source_totals(self):
        _, _, args, receipt, evidence = fixture()
        args["initial_private_memories"][0]["structured_content"]["coverage"]["academic_items_rows"] = 9
        args["initial_private_memories"][0]["structured_content"]["source_totals"] = {"academic_items_rows": 40}
        review = quality.review_create("r", "c", args, receipt, evidence)
        self.assertEqual(review["actual_complete_root_academic_item_unique_ids"], 2)
        self.assertEqual(len(review["row_count_claims"]), 1)
        self.assertEqual(review["row_count_claims"][0]["status"], "mismatch")
        self.assertEqual(review["as_of"]["supported_by_returned_observed_at"], 7)

    def test_dates_do_not_use_semester_start(self):
        _, _, args, receipt, evidence = fixture()
        evidence.data_times.clear()
        evidence.observe(call("calendar", "data_query", query("schedules", ["id", "start_time"], [eq("title", "course")])), result({"rows": [{"id": "date", "start_time": "2026-09-01T08:00:00+08:00"}], "has_more": False}))
        evidence.add_time("2026-11-14T01:00:00+08:00", host=True)
        args["initial_private_memories"][0]["structured_content"]["as_of"] = "2026-09-01"
        review = quality.review_create("r", "c", args, receipt, evidence)
        self.assertEqual(review["as_of"]["unsupported_or_unparseable"], 1)
        self.assertEqual(review["as_of"]["supported_by_host_execution_time"], 6)

    def test_query_refs_require_same_filters_and_read_fields(self):
        _, items, args, _, evidence = fixture()
        declared = args["definition"]["profile_manifest"]["query_coverage"]
        ref = {**items, "type": "query_ref", "fields": ["id", "title"]}
        self.assertIsNone(quality.check_reference(ref, declared, evidence))
        wrong = copy.deepcopy(ref)
        wrong["fields"].append("deadline_time")
        self.assertEqual(quality.check_reference(wrong, declared, evidence), "query_ref_not_in_query_coverage")
        declared_with_unread = copy.deepcopy(declared)
        declared_with_unread[1]["fields"].append("deadline_time")
        self.assertEqual(quality.check_reference(wrong, declared_with_unread, evidence), "query_ref_fields_or_scope_not_read")
        wrong = {**ref, "filters": [eq("kind", "assignment")]}
        self.assertEqual(quality.check_reference(wrong, declared, evidence), "query_ref_not_in_query_coverage")
        self.assertEqual(quality.check_reference("academic_items:offering=off", declared, evidence), "unstructured_or_unseen_reference")
        self.assertEqual(quality.check_reference({"dataset": "academic_items", "id": "absent"}, declared, evidence), "record_reference_not_seen")

    def test_search_can_support_specific_ids_not_unread_query_fields(self):
        evidence = quality.Evidence()
        evidence.observe(call("search", "data_search", {"keywords": ["course"]}), result({"hits": [{"dataset": "academic_items", "row": {"id": "seen", "title": "private"}}], "has_more": False}))
        self.assertIsNone(quality.check_reference({"dataset": "academic_items", "id": "seen"}, [], evidence))
        self.assertEqual(quality.check_reference({"type": "query_ref", "dataset": "academic_items", "fields": ["id"], "filters": [eq("id", "seen")], "limit": 100, "offset": 0}, [], evidence), "query_ref_not_in_query_coverage")

    def test_incomplete_and_subset_queries_do_not_prove_total(self):
        root, items, args, receipt, evidence = fixture()
        evidence.queries.pop(quality.query_key(items))
        subset = copy.deepcopy(items)
        subset["filters"].append(eq("kind", "material"))
        evidence.observe(call("subset", "data_query", subset), result({"rows": [{"id": "one"}], "has_more": False}))
        self.assertIsNone(quality.root_item_count(quality.entity_root(args, evidence), evidence))
        evidence.observe(call("start", "data_query", items), result({"rows": [{"id": "one"}], "has_more": True, "next_offset": 1}))
        skipped = {**items, "offset": 2}
        evidence.observe(call("skipped", "data_query", skipped), result({"rows": [], "has_more": False}))
        self.assertIsNone(quality.root_item_count(quality.entity_root(args, evidence), evidence))
        evidence.observe(call("last", "data_query", {**items, "offset": 1}), result({"rows": [{"id": "two"}], "has_more": False}))
        self.assertEqual(quality.root_item_count(quality.entity_root(args, evidence), evidence), 2)

    def test_directory_dedup_excludes_failed_creates_and_import_does_not_write(self):
        root, items, args, receipt, _ = fixture()
        calls = [call("root", "data_query", root), call("items", "data_query", items), call("bad", "workflow_create", args), call("create", "workflow_create", args)]
        outputs = [result({"rows": [{"id": "off", "connection_id": "conn", "source": "chaoxing", "course_ref": "ref"}], "has_more": False}), result({"rows": [{"id": "one"}, {"id": "two"}], "common_fields": {"connection_id": "conn", "offering_id": "off"}, "has_more": False}), result(None, False), receipt]
        messages = []
        for c, out in zip(calls, outputs):
            messages += [{"role": "assistant", "tool_calls": [c]}, {"role": "tool", "tool_call_id": c["id"], "content": json.dumps(out)}]
        with tempfile.TemporaryDirectory() as tmp:
            directory = pathlib.Path(tmp)
            for n in (1, 2):
                (directory / f"run-{n:03d}-request.private.json").write_text(json.dumps({"Messages": messages}))
            (directory / "requests.jsonl").write_text(json.dumps({"run_id": "run", "call": 0, "at": "2026-11-14T01:00:00+08:00"}) + "\n")
            before = set(directory.iterdir())
            review = quality.review_directory(directory)
            self.assertEqual(review["summary"]["successful_persisted_profiles_reviewed"], 1)
            self.assertEqual(review["summary"]["rejected_creates_excluded"], 1)
            self.assertEqual(review["profiles"][0]["actual_complete_root_academic_item_unique_ids"], 2)
            self.assertEqual(set(directory.iterdir()), before)
            md = quality.markdown(review)
            self.assertNotIn("Private title", md)
            self.assertNotIn("2026-11-14", md)
            self.assertNotIn("workflow", md)

    def test_projection_lineage_is_verifiable_without_claiming_raw_body_access(self):
        _, _, args, receipt, evidence = fixture()
        root = query("academic_offerings", ["id", "record_id"], [eq("id", "off")])
        evidence.observe(call("lineage-root", "data_query", root), result({"rows": [{"id": "off", "record_id": "123"}], "has_more": False}))
        items = query("academic_items", ["id", "record_ids"], [eq("id", "one")])
        evidence.observe(call("lineage-item", "data_query", items), result({"rows": [{"id": "one", "record_ids": '["456", "789"]'}], "has_more": False}))
        ref = {"dataset": "platform_records", "id": "123"}
        self.assertIsNone(quality.check_reference(ref, [], evidence))
        self.assertEqual(quality.reference_access(ref, evidence), "linked_by_verified_projection")
        self.assertIsNone(quality.check_reference("platform_records:456", [], evidence))
        self.assertNotIn("123", evidence.seen_ids["platform_records"])
        self.assertNotIn("456", evidence.seen_ids["platform_records"])
        args["initial_private_memories"][0]["source_refs"] = [ref]
        review = quality.review_create("r", "c", args, receipt, evidence)
        self.assertEqual(review["references"]["linked_by_verified_projection"], 1)
        self.assertNotIn("record_reference_not_seen", review["review_codes"])
        evidence.observe(call("raw", "data_query", query("platform_records", ["id"], [eq("id", "123")])), result({"rows": [{"id": "123"}], "has_more": False}))
        self.assertEqual(quality.reference_access(ref, evidence), "seen_directly")

    def test_lineage_does_not_accept_malformed_center_item_reference(self):
        evidence = quality.Evidence()
        actual = "a" * 64
        typo = "a" * 65
        evidence.observe(call("item", "data_query", query("academic_items", ["id", "record_ids"], [eq("id", actual)])), result({"rows": [{"id": actual, "record_ids": ["123"]}], "has_more": False}))
        self.assertEqual(quality.reference_access({"dataset": "academic_items", "id": actual}, evidence), "seen_directly")
        self.assertIsNone(quality.reference_access({"dataset": "academic_items", "id": typo}, evidence))
        self.assertEqual(quality.check_reference({"dataset": "academic_items", "id": typo}, [], evidence), "record_reference_not_seen")
        evidence.observe(call("failed-lineage", "data_query", query("academic_offerings", ["id", "record_id"], [eq("id", "off")])), result({"rows": [{"id": "off", "record_id": "999"}]}, ok=False))
        self.assertEqual(quality.check_reference({"dataset": "platform_records", "id": "999"}, [], evidence), "record_reference_not_seen")

    def test_memory_defaults_personal_and_missing_refs(self):
        _, _, args, receipt, evidence = fixture()
        first = args["initial_private_memories"][0]
        first.pop("memory_type")
        first.pop("is_personal")
        first["source_refs"] = []
        review = quality.review_create("r", "c", args, receipt, evidence)
        self.assertEqual(review["memory_type"]["omitted_defaults_BASE"], 1)
        self.assertEqual(review["is_personal"]["omitted"], 1)
        self.assertEqual(review["review_codes"]["missing_source_refs"], 1)
        receipt["data"]["profile_initialization"]["verified"] = False
        self.assertFalse(quality.persistence_receipt(args, receipt))


if __name__ == "__main__":
    unittest.main()
