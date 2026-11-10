#!/usr/bin/env python3
"""Read-only scheduler acceptance. Does not refresh, activate or execute Agents."""
import json,os
from pathlib import Path
from agent_regression import Client
c=Client('http://127.0.0.1:2610');c.login(os.environ['BOOTSTRAP_ADMIN_USERNAME'],os.environ['BOOTSTRAP_ADMIN_PASSWORD'])
status,data,_=c.request('/api/v1/control');assert status==200
status,agents,_=c.request('/api/v1/agents/instances');assert status==200
trigger=next(a for a in agents['items'] if a.get('agent_type')=='trigger')
assert trigger['limits']['context_bytes']>=1048576
assert 'scope.kind=course' in trigger['system_prompt']
checks=[]
for b in data['batches'][:4]:
 status,d,_=c.request('/api/v1/control/batches/'+b['id']);assert status==200
 checks.append({'batch_id':b['id'],'source':b['source'],'scope':d['batch'].get('scope'),'status':d['batch']['status']})
report={'health':c.request('/api/v1/health')[0],'manual_mode':data['mode']=='manual','historical_batches':checks,'trigger_context_bytes':trigger['limits']['context_bytes'],'trigger_course_instructions':True,'models_started':False,'refresh_started':False}
Path('../.build/course-batch-http-check.json').write_text(json.dumps(report,ensure_ascii=False,indent=2))
print(json.dumps({k:v for k,v in report.items() if k!='historical_batches'},ensure_ascii=False))
