#!/usr/bin/env python3
"""Observe the running scheduler and batch-check Chaoxing recovery without extra refresh requests.

Uses the existing administrator environment variables; never exports upstream
cookies. Run after starting the updated local demo. Verification failures remain
failures; a separate discussion parsing failure is reported, not hidden.
"""
import argparse
import json
import os
from pathlib import Path
import time
from agent_regression import Client


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--base-url', default='http://127.0.0.1:2610')
    parser.add_argument('--wait-seconds', type=int, default=240)
    parser.add_argument('--require-all-resources', action='store_true', help='Also require all seven Chaoxing resource attempts to succeed')
    args = parser.parse_args()
    client = Client(args.base_url)
    client.login(os.environ.get('AGENT_TEST_USERNAME') or os.environ['BOOTSTRAP_ADMIN_USERNAME'],
                 os.environ.get('AGENT_TEST_PASSWORD') or os.environ['BOOTSTRAP_ADMIN_PASSWORD'])
    report = {'cases': []}

    def check(name, valid):
        report['cases'].append({'name': name, 'passed': bool(valid)})

    before = {}
    for source in ('platforms', 'dws'):
        status, data, _ = client.request('/api/v1/' + source + '/connections')
        check(source + '.list', status == 200 and len(data['items']) == (3 if source == 'platforms' else 1))
        before[source] = {c['id'] for c in data['items']}
    deadline = time.monotonic() + args.wait_seconds
    while True:
        status, data, _ = client.request('/api/v1/tasks')
        if status != 200:
            raise RuntimeError('task state unavailable')
        task = data['items'][0]
        if task['runs'] > 0 and not task['running'] and not task['pending']:
            break
        if time.monotonic() >= deadline:
            raise RuntimeError('scheduler round did not finish')
        time.sleep(2)
    report['round'] = {k: task.get(k) for k in ('last_finished', 'last_error', 'results')}
    cx = next(r for r in task['results'] if r['source'] == 'chaoxing')
    check('chaoxing.connected', cx['connections'] >= 1)
    check('chaoxing.all_resources_attempted', cx['attempts'] == 7)
    check('chaoxing.no_verification_block', cx.get('last_error') != 'verification_required')
    if args.require_all_resources:
        check('chaoxing.all_resources_succeeded', cx['failed'] == 0 and cx['succeeded'] == 7)
    status, overview, _ = client.request('/api/v1/preview/overview')
    check('preview.available', status == 200)
    # Response shapes are kept in the report only as safe resource status codes.
    items = overview.get('items', overview.get('connections', []))
    resources = [r for c in items if c.get('source') == 'chaoxing' and c.get('enabled') and c.get('state') == 'active' for r in c.get('resources', [])]
    check('chaoxing.no_resource_verification', bool(resources) and all(r.get('last_error_code') != 'verification_required' for r in resources))
    for kind in ('assignment', 'progress'):
        selected = next((r for r in resources if r['kind'] == kind), {})
        check('chaoxing.' + kind + '.recovered', selected.get('last_success_at') and not selected.get('last_error_code'))
    report['chaoxing_resources'] = [{k: r.get(k) for k in ('kind', 'last_error_code', 'last_success_at')} for r in resources]
    for source in ('platforms', 'dws'):
        status, data, _ = client.request('/api/v1/' + source + '/connections')
        check(source + '.connections_preserved', status == 200 and before[source] == {c['id'] for c in data['items']})
    report['summary'] = {'passed': sum(c['passed'] for c in report['cases']),
                         'failed': sum(not c['passed'] for c in report['cases'])}
    target = Path(__file__).resolve().parents[2] / '.build/chaoxing-recovery-regression.json'
    target.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
    print(json.dumps(report, ensure_ascii=False), flush=True)
    if report['summary']['failed']:
        raise SystemExit(1)


if __name__ == '__main__':
    main()
