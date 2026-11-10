#!/usr/bin/env python3
"""Batch API regression. Temporarily adjusts the global timer, triggers one real
refresh round, restores settings, and deletes a temporary authorization-test user.
Credentials come from the environment; keys and passwords are never reported.
"""
import json
import os
from pathlib import Path
import secrets
import time
from agent_regression import Client

def main():
    origin=os.environ.get('SCHEDULER_TEST_URL','http://127.0.0.1:2610')
    client=Client(origin)
    client.login(os.environ.get('AGENT_TEST_USERNAME') or os.environ['BOOTSTRAP_ADMIN_USERNAME'],os.environ.get('AGENT_TEST_PASSWORD') or os.environ['BOOTSTRAP_ADMIN_PASSWORD'])
    report={'cases':[]};fixture=None;original=None
    def case(name,path,method='GET',body=None,expected=200,check=None,who=None,csrf=True):
        status,data,seconds=(who or client).request(path,method,body,csrf=csrf)
        ok=status==expected and (check(data) if check and status==expected else True)
        item={'name':name,'status':status,'seconds':seconds,'passed':ok}
        report['cases'].append(item);print(json.dumps(item),flush=True)
        if not ok:raise RuntimeError(name+' failed')
        return data
    def await_idle():
        deadline=time.monotonic()+180
        while time.monotonic()<deadline:
            _,data,_=client.request('/api/v1/tasks')
            state=data['items'][0]
            if not state['running'] and not state['pending']:return state
            time.sleep(1)
        raise RuntimeError('scan still running after 180 seconds')
    try:
        case('page','/admin/tasks',check=lambda x:x.get('non_json') is True)
        case('anonymous.denied','/api/v1/tasks',expected=401,who=Client(origin))
        data=case('tasks.shared','/api/v1/tasks',check=lambda x:len(x['items'])==1 and x['items'][0]['id']=='information-refresh')
        original=data['items'][0];path='/api/v1/tasks/'+original['id']
        case('csrf.required',path,'PUT',{'enabled':True,'interval_seconds':600},expected=403,csrf=False)
        case('interval.invalid',path,'PUT',{'enabled':True,'interval_seconds':1},expected=400)
        case('task.missing','/api/v1/tasks/missing/trigger','POST',expected=404)
        password=secrets.token_urlsafe(24)
        fixture=case('fixture.create','/api/v1/users','POST',{'username':'scheduler_test_'+secrets.token_hex(5),'display_name':'Scheduler test','password':password,'role':'user'},201)
        ordinary=Client(origin);ordinary.login(fixture['username'],password)
        case('ordinary.list.denied','/api/v1/tasks',who=ordinary,expected=403)
        case('ordinary.trigger.denied',path+'/trigger','POST',who=ordinary,expected=403)
        case('ordinary.update.denied',path,'PUT',{'enabled':False,'interval_seconds':600},who=ordinary,expected=403)
        state=case('pause.update',path,'PUT',{'enabled':False,'interval_seconds':720},check=lambda x:not x['enabled'] and x['interval_seconds']==720 and x.get('next_run') is None)
        case('preview.shared_interval','/api/v1/preview/overview',check=lambda x:x['poll_interval_seconds']==720 and x['dws_poll_interval_seconds']==720)
        if state['running']:case('running.duplicate.denied',path+'/trigger','POST',expected=409)
        await_idle()
        case('manual.paused.accepted',path+'/trigger','POST',expected=202,check=lambda x:x['accepted'])
        state=await_idle()
        report['manual_round']={'runs':state['runs'],'last_error':state.get('last_error'),'last_finished':state.get('last_finished'),'results':state.get('results',[])}
        case('stats.round','/api/v1/tasks',check=lambda x:len(x['items'][0]['results'])==4 and len(x['items'][0]['history'])>=1 and all(isinstance(r['created'],int) and isinstance(r['updated'],int) and isinstance(r['unchanged'],int) for r in x['items'][0]['results']))
        case('paused.no_reschedule','/api/v1/tasks',check=lambda x:not x['items'][0]['enabled'] and x['items'][0].get('next_run') is None)
        case('resume.update',path,'PUT',{'enabled':True,'interval_seconds':600},check=lambda x:x['enabled'] and x.get('next_run') is not None)
    finally:
        if original:case('settings.restore','/api/v1/tasks/'+original['id'],'PUT',{'enabled':original['enabled'],'interval_seconds':original['interval_seconds']})
        if fixture:case('fixture.cleanup','/api/v1/users/'+fixture['id'],'DELETE',expected=204)
        report['summary']={'passed':sum(c['passed'] for c in report['cases']),'failed':sum(not c['passed'] for c in report['cases'])}
        target=Path(__file__).resolve().parents[2]/'.build/scheduler-regression.json'
        target.write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n')
        print(json.dumps(report['summary']),flush=True)

if __name__=='__main__':main()
