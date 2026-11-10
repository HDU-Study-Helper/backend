#!/usr/bin/env python3
"""Read-only local run-control smoke batch; never starts Trigger or Filter."""
import json, os, sys
from pathlib import Path
from agent_regression import Client
origin=os.environ.get('CONTROL_TEST_ORIGIN','http://127.0.0.1:2610')
c=Client(origin); c.login(os.environ['BOOTSTRAP_ADMIN_USERNAME'],os.environ['BOOTSTRAP_ADMIN_PASSWORD'])
checks=[]
def case(name,path,method='GET',body=None,expected=200,csrf=True):
 status,data,elapsed=c.request(path,method,body,csrf=csrf)
 checks.append({'name':name,'status':status,'expected':expected,'passed':status==expected,'seconds':elapsed})
 return data
case('health','/api/v1/health')
control=case('control.overview','/api/v1/control')
case('timer.catalog','/api/v1/tasks')
case('start.reject_empty','/api/v1/control/start','POST',{},400)
case('start.reject_unknown_fields','/api/v1/control/start','POST',{'batch_id':'missing','unexpected':True},400)
case('start.reject_csrf','/api/v1/control/start','POST',{'batch_id':'missing'},403,False)
case('start.empty_selection','/api/v1/control/start','POST',{'batch_id':'missing'})
case('retry.unknown','/api/v1/control/batches/missing/retry','POST',None,409)
case('cancel.unknown','/api/v1/control/batches/missing/cancel','POST',None,404)
case('batch.unknown','/api/v1/control/batches/missing',expected=404)
case('run.unknown','/api/v1/control/runs/missing',expected=404)
for batch in control.get('batches',[])[:4]:case('batch.detail.'+batch['source'],'/api/v1/control/batches/'+batch['id'])
for run in control.get('runs',[])[:4]:case('run.detail','/api/v1/control/runs/'+run['run_id'])
anon=Client(origin);status,_,_=anon.request('/api/v1/control');checks.append({'name':'anonymous.denied','status':status,'passed':status==401})
assert control['mode']=='manual'
report={'cases':checks,'summary':{'passed':sum(v['passed'] for v in checks),'failed':sum(not v['passed'] for v in checks)},'mode':control['mode'],'rounds':len(control['rounds']),'batch_counts':control['counts'],'model_runs_started_by_test':0}
root=Path(__file__).resolve().parents[2];(root/'.build/run-control-regression.json').write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n');print(json.dumps(report,ensure_ascii=False,indent=2));sys.exit(bool(report['summary']['failed']))
