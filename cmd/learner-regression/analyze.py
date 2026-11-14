#!/usr/bin/env python3
"""Summarize retained Learner traces without printing private course contents.

Usage: python3 analyze.py /absolute/private/regression/directory
Reads existing artifacts only. Emits sanitized audit.json/audit.md in that directory.
A repeated record may add new fields; novelty is measured per dataset/id/field/value.
"""
import collections
import hashlib
import json
import pathlib
import sys


def canonical(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"))


def summarize(directory):
    manifest = json.loads((directory / "manifest.json").read_text())
    result_path = directory / "results.private.json"
    results = json.loads(result_path.read_text()) if result_path.exists() else []
    traces = collections.defaultdict(list)
    event_path = directory / "requests.jsonl"
    if event_path.exists():
        for line in event_path.read_text().splitlines():
            event = json.loads(line)
            traces[event["run_id"]].append(event)
    reports = []
    for result in results:
        run = result["run_id"]
        files = sorted(directory.glob(f"{run}-*-request.private.json"))
        calls, outputs = {}, {}
        peak_bytes = 0
        for path in files:
            request = json.loads(path.read_text())
            peak_bytes = max(peak_bytes, path.stat().st_size)
            for message in request["Messages"]:
                for call in message.get("tool_calls") or []:
                    calls.setdefault(call["id"], call)
                if message["role"] == "tool":
                    outputs.setdefault(message["tool_call_id"], message["content"])
        # The last model response can issue tools absent from a subsequent request.
        # Persisted tool audit is authoritative for total executions and failures.
        record = result.get("record") or {}
        executions = record.get("tool_executions") or []
        counts = collections.Counter(e["name"] for e in executions)
        errors = collections.Counter(e.get("error", {}).get("code") for e in executions if e.get("error"))
        if not executions:
            counts = collections.Counter(c["function"]["name"] for c in calls.values())
            errors = collections.Counter()
            for content in outputs.values():
                error = json.loads(content).get("error")
                if error:
                    errors[error["code"]] += 1
        datasets = collections.Counter()
        scope_only = 0
        new_records = new_fields = no_new_values = rows_count = result_bytes = 0
        ids, cells = collections.defaultdict(set), collections.defaultdict(set)
        shape_counts = collections.Counter()
        for cid, call in calls.items():
            if call["function"]["name"] != "data_query":
                continue
            query = json.loads(call["function"]["arguments"])
            datasets[query["dataset"]] += 1
            filters = query.get("filters") or []
            if not any(f["field"] not in ("connection_id", "source") for f in filters):
                scope_only += 1
            shape = dict(query, offset=0)
            shape_counts[canonical(shape)] += 1
            if cid not in outputs:
                continue
            text = outputs[cid]
            result_bytes += len(text.encode())
            value = json.loads(text)
            if value.get("error"):
                continue
            data = value.get("data") or {}
            common = data.get("common_fields") or {}
            ni = nf = 0
            for raw_row in data.get("rows") or []:
                row = {**common, **raw_row}
                rid = row.get("id") or row.get("item_id")
                if rid is None:
                    # Do not infer record identity from titles or array positions.
                    continue
                ds = query["dataset"]
                rid = str(rid)
                rows_count += 1
                ni += rid not in ids[ds]
                ids[ds].add(rid)
                for key, val in row.items():
                    cell = (rid, key, canonical(val))
                    nf += cell not in cells[ds]
                    cells[ds].add(cell)
            if ni:
                new_records += 1
            elif nf:
                new_fields += 1
            else:
                no_new_values += 1
        reports.append({
            "case_hash": hashlib.sha256(result["offering_id"].encode()).hexdigest()[:12],
            "run_id": run, "verified": result["verified"],
            "validation_error": result.get("validation_error", ""),
            "run_error_code": (result.get("error") or {}).get("code"),
            "attempt_id": result.get("attempt_id"),
            "tool_count_source": "persisted_audit" if executions else "request_history_partial",
            "model_calls": record.get("model_calls", len(files)),
            "tool_calls": record.get("tool_calls", sum(counts.values())),
            "tool_counts": dict(counts), "tool_error_codes": dict(errors),
            "query_datasets": dict(datasets), "connection_only_queries": scope_only,
            "query_row_instances_with_ids": rows_count,
            "unique_records_by_dataset": {k: len(v) for k, v in ids.items()},
            "calls_with_new_records": new_records,
            "calls_with_only_new_field_values": new_fields,
            "calls_with_no_new_field_values": no_new_values,
            "query_content_bytes": result_bytes,
            "peak_request_snapshot_bytes": peak_bytes,
            "usage": record.get("usage", {}), "duration_ms": record.get("duration_ms"),
            "trace_missing_terminal_results": max(0, counts.get("data_query", 0) - sum(c["function"]["name"] == "data_query" and cid in outputs for cid, c in calls.items())),
        })
    attempt_dirs = sorted(p for p in directory.glob("attempt-*") if p.is_dir())
    current_attempt = []
    if attempt_dirs and (attempt_dirs[-1] / "results.private.json").exists():
        current_attempt = json.loads((attempt_dirs[-1] / "results.private.json").read_text())
    aggregate = {
        "mode": manifest["mode"], "frozen_cases": len(manifest["courses"]),
        "finished": len(results), "verified": sum(r["verified"] for r in results),
        "failed": sum(not r["verified"] for r in results),
        "current_attempt_finished": len(current_attempt),
        "current_attempt_verified": sum(r["verified"] for r in current_attempt),
        "retained_cancelled_results": sum((r.get("error") or {}).get("code") == "CANCELLED" for r in results),
        "source_activity_records_at_snapshot": manifest.get("source_activity_records"),
        "attempts_in_latest_results": sorted({r["attempt_id"] for r in reports if r.get("attempt_id")}),
        "all_retained_attempt_model_requests": sum(len(events) for events in traces.values()),
        "all_retained_attempt_reported_tokens": sum((e.get("usage") or {}).get("total_tokens", 0) for events in traces.values() for e in events),
        "all_retained_attempt_request_bytes_sum": sum(e.get("request_bytes", 0) for events in traces.values() for e in events),
        "model_calls": sum(r["model_calls"] for r in reports),
        "tool_calls": sum(r["tool_calls"] for r in reports),
        "connection_only_queries": sum(r["connection_only_queries"] for r in reports),
        "peak_request_snapshot_bytes": max((r["peak_request_snapshot_bytes"] for r in reports), default=0),
        "verification_semantics": "verified means persisted Workflow identity/version plus sourced PRI index/topic checks; it does not certify six-dimension factual correctness. Use quality.py and evidence review separately.",
        "scope": "All frozen structured course offerings; Learner initialization only, not Trigger/Filter/Worker full-chain acceptance.",
        "limits": "Provider usage unverified. Snapshot bytes differ from Runtime ContextBytes and HTTP payload. No-new-values does not prove an unnecessary verification. Unassociated records retained but not counted as course cases.",
    }
    return {"summary": aggregate, "cases": reports}


def main():
    directory = pathlib.Path(sys.argv[1]).resolve(strict=True)
    report = summarize(directory)
    path = directory / "audit.json"
    path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    path.chmod(0o600)
    s = report["summary"]
    lines = ["# Learner 全课程分层检索回归", "", f"冻结 {s['frozen_cases']} 课；累计保留结果 {s['finished']}，持久化核验通过 {s['verified']}，未通过 {s['failed']}（含中断结果 {s['retained_cancelled_results']}）。本次续跑已结束 {s['current_attempt_finished']}，其中持久化核验通过 {s['current_attempt_verified']}。", "", "仅验证 Learner 首次初始化；不等于 Trigger/Filter/Worker 全链验收。核验通过只表示身份、版本和有来源的私有画像已持久化，不代表六维事实语义全部准确，需另看质量审计。源库只读，所有创建发生在保留的隔离 schema。", "", "|课程匿名标识|核验|模型调用|查询|搜索|仅连接查询|峰值请求 KiB|", "|---|---|---:|---:|---:|---:|---:|"]
    for row in report["cases"]:
        lines.append(f"|{row['case_hash']}|{'通过' if row['verified'] else '失败'}|{row['model_calls']}|{row['tool_counts'].get('data_query',0)}|{row['tool_counts'].get('data_search',0)}|{row['connection_only_queries']}|{row['peak_request_snapshot_bytes']/1024:.1f}|")
    lines += ["", "字节为请求快照大小，不能直接当 Runtime 上下文或 token 数。字段信息增量按数据集、记录 ID、字段和值判断；未投影 ID 的记录不猜身份。原始业务正文与凭据不进入本报告。", ""]
    md = directory / "audit.md"
    md.write_text("\n".join(lines))
    md.chmod(0o600)
    print(json.dumps(s, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
