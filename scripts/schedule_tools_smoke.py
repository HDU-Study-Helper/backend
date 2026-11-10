#!/usr/bin/env python3
"""Verify live schedule tool configuration; --live also runs a read-only Filter task."""
import argparse
import json
import os
from pathlib import Path
from agent_regression import Client

p = argparse.ArgumentParser(description=__doc__)
p.add_argument('--origin', default='http://127.0.0.1:2610')
p.add_argument('--live', action='store_true')
a = p.parse_args()
c = Client(a.origin)
c.login(os.environ['BOOTSTRAP_ADMIN_USERNAME'], os.environ['BOOTSTRAP_ADMIN_PASSWORD'])
status, catalog, _ = c.request('/api/v1/agents/tools')
assert status == 200
tools = {t['name']: t for t in catalog['items']}
for name, permission in {'schedule_query': 'schedule.read', 'schedule_get': 'schedule.read',
                         'schedule_create': 'schedule.write', 'schedule_update': 'schedule.write',
                         'schedule_delete': 'schedule.delete'}.items():
    assert tools[name]['permissions'] == [permission]
status, agents, _ = c.request('/api/v1/agents/instances')
assert status == 200
agent = next(v for v in agents['items'] if v['agent_type'] == 'filter')
assert {'schedule_query', 'schedule_get'} <= set(agent['enabled_tools'])
assert not {'schedule_create', 'schedule_update', 'schedule_delete'} & set(agent['enabled_tools'])
assert 'schedule.read' in agent['permissions']
assert not {'schedule.write', 'schedule.delete'} & set(agent['permissions'])
report = {'passed': True, 'catalog_size': len(tools), 'filter_agent_id': agent['agent_id'],
          'verified': ['five_registered_schedule_tools', 'filter_readonly_permissions'],
          'live_model_test': False}
if a.live:
    status, response, elapsed = c.request('/api/v1/agents/instances/' + agent['agent_id'] + '/runs', 'POST', {
        'input': {'event_id': 'schedule-tool-readonly-smoke', 'input_type': 'USER_REQUEST',
                  'occurred_at': '2026-11-05T10:00:00+08:00',
                  'content': '这是只读路由测试。请先查询2026年11月5日00:00至11月6日00:00（Asia/Shanghai）的日程，结合当天课程判断“今天下午那节课取消”应路由到哪个现有Workflow。不要修改任何数据或调用下游；不明确则提出澄清问题。'}})
    assert status == 200, 'Filter run failed HTTP ' + str(status)
    record = response['record']
    executions = record.get('tool_executions', [])
    assert any(t['name'] == 'schedule_query' and not t.get('error') for t in executions), 'No successful schedule_query call'
    output = json.loads(response['result']['output'])
    assert isinstance(output.get('items'), list) and output['items'], 'Filter did not return item results'
    statuses = {item['status'] for item in output['items']}
    assert statuses <= {'COMPLETED', 'NEEDS_USER_INPUT', 'PARTIAL', 'FAILED', 'IGNORED'}
    report.update(live_model_test=True, run_id=record['run_id'], seconds=elapsed,
                  tool_executions=executions, output_statuses=sorted(statuses))
root = Path(__file__).resolve().parents[2]
(root / '.build').mkdir(exist_ok=True)
(root / '.build/schedule-tools-smoke.json').write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
print(json.dumps(report, ensure_ascii=False))
