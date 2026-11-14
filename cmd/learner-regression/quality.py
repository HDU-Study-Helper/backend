#!/usr/bin/env python3
"""Read-only semantic review of persisted Learner profile seeds.

Import/review_directory never writes or calls a database/model. Only explicit CLI
execution emits private quality.json and sanitized quality.md. Rules identify
review needs; they neither repair history nor establish semantic correctness.
"""
import argparse
import collections
import datetime as dt
import hashlib
import json
import os
import pathlib
import re
from zoneinfo import ZoneInfo

TOPICS = ("identity", "timetable", "teaching_progress", "assignments", "materials", "discussion_participation")
DATASETS = ("academic_offerings", "academic_items", "platform_records", "schedules", "dws_messages", "dws_attachments")
AS_OF_KEYS = {"as_of", "asof", "observed_as_of"}
KNOWN_TIME_KEYS = {"observed_at"}
HOST_PREFIX = "Host-verified owner-scoped database snapshot"


def canonical(value):
    return json.dumps(value, sort_keys=True, ensure_ascii=False, separators=(",", ":"))


def anonymous(value):
    return hashlib.sha256(str(value).encode()).hexdigest()[:12]


def decoded(value, default=None):
    if isinstance(value, str):
        try:
            return json.loads(value)
        except (ValueError, TypeError):
            return default
    return value if value is not None else default


def filters_key(query):
    filters = query.get("filters") or []
    return canonical(sorted(filters, key=canonical))


def query_key(query):
    return (query.get("dataset"), filters_key(query), tuple(sorted(query.get("fields") or [])), query.get("limit") or 20)


def equal_filter(query, field):
    values = [f.get("value") for f in query.get("filters") or [] if f.get("field") == field and f.get("operator") == "eq"]
    return values[0] if len(values) == 1 else None


def row_identity(row):
    for key in ("id", "item_id"):
        if row.get(key) is not None and str(row[key]) != "":
            return str(row[key])
    return None


def walk_keys(value, predicate):
    if isinstance(value, dict):
        for key, child in value.items():
            if predicate(key):
                yield key, child
            yield from walk_keys(child, predicate)
    elif isinstance(value, list):
        for child in value:
            yield from walk_keys(child, predicate)


def parse_instant(value):
    if not isinstance(value, str):
        return None
    try:
        result = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
        return result if result.tzinfo else None
    except ValueError:
        return None


def time_supported(claim, candidates):
    """Accept explicit instants or day precision; never use academic start times."""
    if not isinstance(claim, str):
        return False
    instant = parse_instant(claim)
    if instant:
        return any(instant == candidate for candidate in candidates)
    if re.fullmatch(r"\d{4}-\d{2}-\d{2}", claim):
        return any(claim in {candidate.date().isoformat(), candidate.astimezone(dt.timezone.utc).date().isoformat(), candidate.astimezone(ZoneInfo("Asia/Shanghai")).date().isoformat()} for candidate in candidates)
    return False


def normalize_rows(data, search=False):
    common = data.get("common_fields") or {}
    common = common if isinstance(common, dict) else {}
    for raw in data.get("hits" if search else "rows") or []:
        if search:
            if not isinstance(raw, dict) or not isinstance(raw.get("row"), dict):
                continue
            yield raw.get("dataset"), {**common, **raw["row"]}
        elif isinstance(raw, dict):
            yield None, {**common, **raw}


class Evidence:
    def __init__(self):
        self.seen_ids = collections.defaultdict(set)
        # IDs exposed by safe academic projections establish source lineage,
        # not proof that raw record fields or bodies were directly fetched.
        self.linked_platform_record_ids = set()
        self.queries = {}
        self.data_times = set()
        self.host_times = set()

    def add_time(self, value, host=False):
        instant = parse_instant(value)
        if instant:
            (self.host_times if host else self.data_times).add(instant)

    def observe(self, query, result):
        name = query["function"]["name"]
        args = decoded(query["function"].get("arguments"), {})
        data = result.get("data") or {}
        if result.get("ok") is not True or result.get("error") or not isinstance(args, dict) or not isinstance(data, dict):
            return
        if name not in ("data_query", "data_search"):
            return
        rows = list(normalize_rows(data, search=name == "data_search"))
        for dataset, row in rows:
            dataset = dataset or args.get("dataset")
            identity = row_identity(row)
            if dataset in DATASETS and identity is not None:
                self.seen_ids[dataset].add(identity)
            if dataset == "academic_offerings":
                linked = row.get("record_id")
                if isinstance(linked, (str, int)) and not isinstance(linked, bool) and str(linked):
                    self.linked_platform_record_ids.add(str(linked))
            elif dataset == "academic_items":
                linked = decoded(row.get("record_ids"), [])
                if isinstance(linked, list):
                    self.linked_platform_record_ids.update(str(value) for value in linked if isinstance(value, (str, int)) and not isinstance(value, bool) and str(value))
            observed = row.get("observed_at")
            if observed is not None:
                self.add_time(observed)
        if name != "data_query":
            return
        key = query_key(args)
        state = self.queries.setdefault(key, {"query": args, "expected": 0, "complete": False, "rows": {}, "anonymous_rows": 0})
        if state["complete"] or (args.get("offset") or 0) != state["expected"]:
            return
        for _, row in rows:
            identity = row_identity(row)
            if identity is None:
                state["anonymous_rows"] += 1
            else:
                state["rows"][identity] = {**state["rows"].get(identity, {}), **row}
        if data.get("has_more") is False:
            state["complete"] = True
        elif data.get("has_more") is True:
            next_offset = data.get("next_offset")
            if isinstance(next_offset, int) and next_offset > state["expected"]:
                state["expected"] = next_offset

    def complete_queries(self):
        return [state for state in self.queries.values() if state["complete"]]


def entity_root(args, evidence):
    entity_id = ((args.get("definition") or {}).get("entity") or {}).get("id")
    for state in evidence.complete_queries():
        if state["query"].get("dataset") != "academic_offerings":
            continue
        for row in state["rows"].values():
            source, conn, ref = (row.get(key) for key in ("source", "connection_id", "course_ref"))
            if source and conn and ref and entity_id == f"source={source}|connection={conn}|course_ref={ref}":
                return row
    return None


def root_item_count(root, evidence):
    if not root:
        return None
    ids = set()
    complete = False
    unknown_identity = False
    for state in evidence.complete_queries():
        query = state["query"]
        if query.get("dataset") != "academic_items":
            continue
        connection = equal_filter(query, "connection_id")
        if connection is not None and connection != root.get("connection_id"):
            continue
        scoped = equal_filter(query, "offering_id") == root.get("id") or (connection == root.get("connection_id") and equal_filter(query, "course_ref") == root.get("course_ref"))
        if not scoped:
            continue
        # A kind/status subset does not establish the total root-course row count.
        if any(f.get("field") not in {"connection_id", "source", "offering_id", "course_ref"} or f.get("operator") != "eq" for f in query.get("filters") or []):
            continue
        complete = True
        unknown_identity |= bool(state["anonymous_rows"])
        ids.update(state["rows"])
    return len(ids) if complete and not unknown_identity else None


def count_claims(structured):
    """Recognize explicit read-row claims, excluding marked source totals."""
    if not isinstance(structured, dict):
        return
    for key, value in structured.items():
        if any(word in key.lower() for word in ("source_total", "source_count", "observed_count", "available_count")):
            continue
        if any(key == name + suffix for name in DATASETS for suffix in ("_rows", "_rows_read", "_read_count")):
            if isinstance(value, int) and not isinstance(value, bool) and value >= 0:
                dataset = next(name for name in DATASETS if key.startswith(name + "_"))
                yield dataset, value
        elif isinstance(value, dict):
            yield from count_claims(value)


def reference_access(ref, evidence):
    """Classify exact record references without treating lineage as direct access."""
    if isinstance(ref, str):
        match = re.fullmatch(r"(" + "|".join(DATASETS) + r"):([^:\s=]+)", ref)
        dataset, identity = (match[1], match[2]) if match else (None, None)
    elif isinstance(ref, dict) and ref.get("type") != "query_ref":
        dataset = ref.get("dataset")
        identity = ref.get("id") or ref.get("record_id") or ref.get("item_id")
    else:
        return None
    if dataset not in DATASETS or identity is None:
        return None
    identity = str(identity)
    if identity in evidence.seen_ids[dataset]:
        return "seen_directly"
    if dataset == "platform_records" and identity in evidence.linked_platform_record_ids:
        return "linked_by_verified_projection"
    return None


def check_reference(ref, declared, evidence):
    """Return a review code or None; lineage validates a link, never an unread body."""
    if reference_access(ref, evidence):
        return None
    if isinstance(ref, str):
        match = re.fullmatch(r"(" + "|".join(DATASETS) + r"):([^:\s=]+)", ref)
        if match and match[2] in evidence.seen_ids[match[1]]:
            return None
        return "unstructured_or_unseen_reference"
    if not isinstance(ref, dict) or not ref:
        return "invalid_reference"
    dataset = ref.get("dataset")
    if ref.get("type") == "query_ref":
        if dataset not in DATASETS or not isinstance(ref.get("fields"), list) or not ref["fields"] or (ref.get("offset") or 0) != 0:
            return "query_ref_invalid"
        fields = set(ref["fields"])
        # Subset projections are allowed, but the declared filters and page size
        # must be unchanged; a narrower proof cannot establish a broader scope.
        matches = [q for q in declared if isinstance(q, dict) and q.get("dataset") == dataset and filters_key(q) == filters_key(ref) and (q.get("limit") or 20) == (ref.get("limit") or 20) and fields <= set(q.get("fields") or [])]
        if not matches:
            return "query_ref_not_in_query_coverage"
        actual = [s for s in evidence.complete_queries() if s["query"].get("dataset") == dataset and filters_key(s["query"]) == filters_key(ref) and fields <= set(s["query"].get("fields") or [])]
        return None if actual else "query_ref_fields_or_scope_not_read"
    identity = ref.get("id") or ref.get("record_id") or ref.get("item_id")
    if dataset in DATASETS and identity is not None:
        return None if str(identity) in evidence.seen_ids[dataset] else "record_reference_not_seen"
    return "reference_without_verifiable_dataset_id"


def persistence_receipt(args, result):
    if result.get("ok") is not True or result.get("error"):
        return False
    data = result.get("data") or {}
    receipt = data.get("profile_initialization") or {}
    seeds = args.get("initial_private_memories") or []
    ids = receipt.get("memory_ids") or []
    return bool(seeds and receipt.get("verified") is True and data.get("workflow_id") and receipt.get("owner_workflow_id") == data["workflow_id"] and receipt.get("count") == len(seeds) == len(ids) and len(set(ids)) == len(ids))


def review_create(run, call_id, args, result, evidence):
    seeds = args.get("initial_private_memories") or []
    manifest = (args.get("definition") or {}).get("profile_manifest") or {}
    declared = manifest.get("query_coverage") or []
    categories = collections.Counter()
    types = collections.Counter()
    personal = collections.Counter()
    references = collections.Counter()
    dates = collections.Counter()
    claims = []
    issues = collections.Counter()
    definition = args.get("definition") or {}
    missing_contract = [key for key in ("goal", "applicability", "instructions", "input_contract", "output_contract") if not definition.get(key)]
    if missing_contract:
        issues["workflow_execution_contract_missing"] += len(missing_contract)
    root_count = root_item_count(entity_root(args, evidence), evidence)
    for seed in seeds:
        category = seed.get("category")
        category = category if category in TOPICS or category == "profile_index" else "other_or_missing"
        categories[category] += 1
        types[seed.get("memory_type") if seed.get("memory_type") in ("TEMP", "BASE") else "omitted_defaults_BASE"] += 1
        personal["true" if seed.get("is_personal") is True else "false" if seed.get("is_personal") is False else "omitted"] += 1
        refs = seed.get("source_refs")
        if not isinstance(refs, list) or not refs:
            issues["missing_source_refs"] += 1
        else:
            for ref in refs:
                code = check_reference(ref, declared, evidence)
                references[code or reference_access(ref, evidence) or "seen_or_completed"] += 1
                if code:
                    issues[code] += 1
        structured = seed.get("structured_content") or {}
        # Reference payloads are provenance, not the seed's observation date.
        as_of = list(walk_keys({key: value for key, value in seed.items() if key != "source_refs"}, lambda key: key.lower() in AS_OF_KEYS))
        if not as_of and (seed.get("memory_type") == "TEMP" or category in ("teaching_progress", "assignments", "discussion_participation")):
            issues["dynamic_topic_without_as_of_key"] += 1
        for _, value in as_of:
            dates["as_of_keys"] += 1
            if time_supported(value, evidence.data_times):
                dates["supported_by_returned_observed_at"] += 1
            elif time_supported(value, evidence.host_times):
                dates["supported_by_host_execution_time"] += 1
            else:
                dates["unsupported_or_unparseable"] += 1
                issues["as_of_requires_review"] += 1
        for dataset, value in count_claims(structured):
            actual = root_count if dataset == "academic_items" else None
            status = "matches_complete_root_read" if actual is not None and value == actual else "mismatch" if actual is not None else "no_complete_root_count_proof"
            claims.append({"category": category, "dataset": dataset, "claimed_rows": value, "actual_complete_root_unique_ids": actual, "status": status})
            if status != "matches_complete_root_read":
                issues["row_count_" + status] += 1
    for topic in TOPICS:
        if not categories[topic]:
            issues["missing_topic_category"] += 1
    if not categories["profile_index"]:
        issues["missing_profile_index"] += 1
    return {
        "case_hash": anonymous(run + ":" + call_id),
        "persistence_verified": True,
        "persistence_evidence": "successful_tool_result_with_verified_atomic_memory_receipt",
        "semantic_review": "review_required" if issues else "no_rule_findings_not_semantic_verification",
        "seed_count": len(seeds), "missing_execution_contract_fields": missing_contract,
        "categories": dict(categories), "memory_type": dict(types),
        "is_personal": dict(personal), "as_of": dict(dates),
        "references": dict(references), "row_count_claims": claims,
        "actual_complete_root_academic_item_unique_ids": root_count,
        "review_codes": dict(issues),
    }


def load_events(paths):
    """Cumulative requests repeat messages: retain each real tool_call_id once."""
    calls, results, order, host_messages = {}, {}, [], []
    for fallback_index, path in enumerate(sorted(paths), 1):
        match = re.search(r"-(\d+)-request\.private\.json$", path.name)
        index = int(match[1]) if match else fallback_index
        request = json.loads(path.read_text())
        for message in request.get("Messages") or request.get("messages") or []:
            if message.get("role") == "user" and isinstance(message.get("content"), str) and message["content"].startswith(HOST_PREFIX):
                if message["content"] not in host_messages:
                    host_messages.append(message["content"])
            for call in message.get("tool_calls") or []:
                calls.setdefault(call["id"], call)
            if message.get("role") == "tool" and message.get("tool_call_id") not in results:
                call_id = message.get("tool_call_id")
                value = decoded(message.get("content"), {})
                if isinstance(value, dict):
                    results[call_id] = value
                    order.append((call_id, index))
    return calls, results, order, host_messages


def review_directory(directory):
    directory = pathlib.Path(directory)
    grouped = collections.defaultdict(list)
    for path in directory.glob("*-request.private.json"):
        grouped[path.name.split("-")[0]].append(path)
    times = collections.defaultdict(list)
    event_path = directory / "requests.jsonl"
    if event_path.exists():
        for line in event_path.read_text().splitlines():
            event = json.loads(line)
            times[event.get("run_id")].append(event)
    profiles = []
    rejected = successful_without_receipt = 0
    for run, paths in sorted(grouped.items()):
        calls, results, order, hosts = load_events(paths)
        evidence = Evidence()
        for message in hosts:
            start = message.find("\n")
            host = decoded(message[start + 1:], {}) if start >= 0 else {}
            for _, value in walk_keys(host, lambda key: key in ("checked_at", "execution_at", "snapshot_at")):
                evidence.add_time(value, host=True)
        for call_id, index in order:
            call = calls.get(call_id)
            if not call:
                continue
            result = results[call_id]
            if call.get("function", {}).get("name") != "workflow_create":
                evidence.observe(call, result)
                continue
            args = decoded(call["function"].get("arguments"), {})
            if not persistence_receipt(args, result):
                if result.get("ok") is True and not result.get("error"):
                    successful_without_receipt += 1
                else:
                    rejected += 1
                continue
            # Event 'at' is captured by the host when a model call completes.
            # Use only events no later than the request first carrying this result.
            for event in times[run]:
                if isinstance(event.get("call"), int) and event["call"] < index:
                    evidence.add_time(event.get("at"), host=True)
            profiles.append(review_create(run, call_id, args, result, evidence))
    return {"summary": {
        "successful_persisted_profiles_reviewed": len(profiles),
        "semantic_review_required": sum(p["semantic_review"] == "review_required" for p in profiles),
        "rejected_creates_excluded": rejected,
        "successful_without_verified_receipt_excluded": successful_without_receipt,
        "scope": "Only observed successful atomic creation receipts; rules flag review needs, not semantic truth. No historical writes or repairs.",
        "time_policy": "Only returned observed_at or exact host execution timestamps support as_of; academic start/end/deadline are not observation times.",
    }, "profiles": profiles}


def markdown(report):
    summary = report["summary"]
    lines = ["# Learner 已持久化画像质量审查", "", f"实际成功持久化画像：{summary['successful_persisted_profiles_reviewed']}；需语义复核：{summary['semantic_review_required']}。", "", "持久化核验与语义审查分别记录。工具回执成功不代表事实正确；规则未发现问题也不等于语义验收通过。", "", "|匿名画像|持久化回执|种子数|缺省 BASE|待审查日期|需审查项数|", "|---|---|---:|---:|---:|---:|"]
    for p in report["profiles"]:
        lines.append(f"|{p['case_hash']}|已核验|{p['seed_count']}|{p['memory_type'].get('omitted_defaults_BASE', 0)}|{p['as_of'].get('unsupported_or_unparseable', 0)}|{sum(p['review_codes'].values())}|")
    codes = collections.Counter()
    for p in report["profiles"]:
        codes.update(p["review_codes"])
    lines += ["", "审查类型（仅匿名计数）：", ""]
    lines += [f"- `{code}`：{count}" for code, count in sorted(codes.items())]
    lines += ["", "总行数只与已连续完成的根课程查询去重 ID 比较；来源摘要总量不当作实际读取数量。关键词摘要不能证明未读取字段。日期只接受 observed_at 或确切宿主执行时间，不把学期开始日作为当前观察日。报告不包含课程名、正文、原始 ID 或原始日期，不自动改写历史。", ""]
    return "\n".join(lines)


def write_private(path, text):
    # Explicit main only. Refuse symlink targets and force permissions before data.
    flags = os.O_WRONLY | os.O_CREAT | os.O_TRUNC | getattr(os, "O_NOFOLLOW", 0)
    descriptor = os.open(path, flags, 0o600)
    try:
        os.fchmod(descriptor, 0o600)
        with os.fdopen(descriptor, "w", encoding="utf-8") as stream:
            descriptor = None
            stream.write(text)
    finally:
        if descriptor is not None:
            os.close(descriptor)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=pathlib.Path)
    args = parser.parse_args(argv)
    directory = args.directory.resolve(strict=True)
    report = review_directory(directory)
    write_private(directory / "quality.json", json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    write_private(directory / "quality.md", markdown(report))
    print(json.dumps(report["summary"], ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
