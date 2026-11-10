#!/usr/bin/env python3
"""Batch regression for the Agent console. Keys stay in the server's encrypted settings.

Reads only the admin login from AGENT_TEST_USERNAME/PASSWORD or BOOTSTRAP_ADMIN_*
(environment), creates temporary users for mutations, and cleans them up. Re-saves settings once to verify blank-key preservation, without changing provider values.
--live uses configured model quota. --repair-demo-timeout is an explicit repair mode.
"""
import argparse
import copy
import http.cookiejar
import json
import os
from pathlib import Path
import secrets
import time
import urllib.error
import urllib.parse
import urllib.request

class Client:
    def __init__(self, origin):
        self.origin = origin.rstrip('/')
        self.jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.jar))
    def request(self, path, method='GET', body=None, timeout=330, csrf=True):
        headers = {'Origin': self.origin}
        if body is not None: headers['Content-Type'] = 'application/json'
        if csrf:
            for cookie in self.jar:
                if cookie.name == 'academic_csrf': headers['X-CSRF-Token'] = cookie.value
        req = urllib.request.Request(self.origin + path, method=method, headers=headers,
                                     data=None if body is None else json.dumps(body).encode())
        start = time.monotonic()
        try: response = self.opener.open(req, timeout=timeout)
        except urllib.error.HTTPError as error: response = error
        with response:
            raw = response.read()
            try: data = json.loads(raw) if raw else None
            except ValueError: data = {'non_json': True}
            return response.status, data, round(time.monotonic()-start, 3)
    def login(self, username, password):
        status, data, _ = self.request('/api/v1/auth/login','POST',{'username':username,'password':password})
        if status != 200: raise RuntimeError('admin login failed (HTTP %s)' % status)
        return data

def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--base-url',default='http://127.0.0.1:2610')
    parser.add_argument('--inspect-only',action='store_true')
    parser.add_argument('--repair-demo-timeout',action='store_true',help='Raise inherited 30s demo limit to 180s; preserve other fields')
    parser.add_argument('--live',action='store_true',help='Use configured external model; never reads the API key')
    parser.add_argument('--output',default='../.build/agent-regression.json')
    args=parser.parse_args()
    username=os.environ.get('AGENT_TEST_USERNAME') or os.environ.get('BOOTSTRAP_ADMIN_USERNAME')
    password=os.environ.get('AGENT_TEST_PASSWORD') or os.environ.get('BOOTSTRAP_ADMIN_PASSWORD')
    if not username or not password: parser.error('set AGENT_TEST_USERNAME/PASSWORD or BOOTSTRAP_ADMIN_*')
    client=Client(args.base_url);actor=client.login(username,password)
    report={'base_url':args.base_url,'cases':[],'inspection':{},'live':args.live}
    def case(name,path,method='GET',body=None,expected=200,check=None,csrf=True,target=None):
        try:
            status,data,elapsed=(target or client).request(path,method,body,csrf=csrf)
            allowed=expected if isinstance(expected,(list,tuple,set)) else [expected]
            try: valid=check(data) if check and status in allowed else True
            except (KeyError, TypeError, ValueError): valid=False
            ok=status in allowed and valid
            item={'name':name,'method':method,'path':path,'status':status,'seconds':elapsed,'passed':bool(ok)}
            if not ok: item['error']=(data.get('error') if isinstance(data,dict) else None) or 'response validation failed'
        except Exception as error:
            data=None;item={'name':name,'method':method,'path':path,'passed':False,'error':type(error).__name__}
        report['cases'].append(item);print(json.dumps(item,ensure_ascii=False),flush=True)
        return data
    def must(value,message):
        if value is None: raise RuntimeError(message)
        return value
    def write_report():
        report['tool_coverage']=sorted({t['name'] for c in report['cases'] for t in c.get('tools',[])})
        report['expected_unsupported']=['notification_send','web_fetch']
        report['summary']={'passed':sum(c['passed'] for c in report['cases']),'failed':sum(not c['passed'] for c in report['cases'])}
        path=Path(args.output);path.parent.mkdir(parents=True,exist_ok=True);path.write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n')
        print(json.dumps({'report':str(path),'summary':report['summary']},ensure_ascii=False),flush=True)
    settings=must(case('settings.read','/api/v1/agents/settings',check=lambda x:all(not p.get('api_key') for p in x['providers']) and not x['search'].get('api_key')),'settings unavailable')
    tools=must(case('tools.catalog','/api/v1/agents/tools',check=lambda x:len(x['items'])==18),'tools unavailable')['items']
    existing=must(case('agents.existing','/api/v1/agents/instances'),'agents unavailable')['items']
    report['inspection']['providers']=[{k:p[k] for k in ('name','base_url','default_model','timeout_seconds','has_api_key')} for p in settings['providers']]
    report['inspection']['search']={k:settings['search'][k] for k in ('base_url','timeout_seconds','has_api_key')}
    report['inspection']['agents']=[]
    for a in existing:
        status,runs,_=client.request('/api/v1/agents/instances/'+a['agent_id']+'/runs')
        report['inspection']['agents'].append({'agent_id':a['agent_id'],'name':a['name'],'model_config':{k:v for k,v in a['model_config'].items() if k!='parameters'},'limits':a['limits'],'recent_runs':[{'run_id':r['run_id'],'status':r['status'],'duration_ms':r['duration_ms'],'model_calls':r['model_calls'],'tool_calls':r['tool_calls'],'error':r.get('error')} for r in (runs or {}).get('items',[])[:8]]})
    if args.inspect_only:
        write_report();return
    if args.repair_demo_timeout:
        for agent in existing:
            if agent['agent_type']=='demo' and agent['model_config']['provider']!='demo' and agent['limits']['run_timeout']==30_000_000_000:
                update={k:copy.deepcopy(agent[k]) for k in ('name','agent_type','system_prompt','model_config','enabled_tools','permissions','status','limits')}
                update['limits']['run_timeout']=180_000_000_000
                case('repair.inherited_demo_timeout','/api/v1/agents/instances/'+agent['agent_id']+'?owner_id='+agent['owner_user_id'],'PUT',update,check=lambda x:x['limits']['run_timeout']==180_000_000_000)
    temporary=[]
    stamp=secrets.token_hex(4)
    try:
        case('health','/api/v1/health',check=lambda x:x=={'status':'ok'})
        case('page.agent','/admin/agents',check=lambda x:x.get('non_json') is True)
        anon=Client(args.base_url);case('auth.required','/api/v1/agents/settings',expected=401,target=anon)
        case('settings.csrf','/api/v1/agents/settings','PUT',{},expected=403,csrf=False)
        stale=copy.deepcopy(settings);stale['version']=-1
        case('settings.stale','/api/v1/agents/settings','PUT',stale,expected=409)
        case('settings.save.preserve_keys','/api/v1/agents/settings','PUT',settings,check=lambda x:x['version']>settings['version'] and all(p['has_api_key']==settings['providers'][i]['has_api_key'] for i,p in enumerate(x['providers'])))
        for suffix in ('a','b'):
            user=must(case('fixture.user.'+suffix,'/api/v1/users','POST',{'username':'agentr_'+stamp+suffix,'display_name':'Agent 回归测试 '+suffix,'password':secrets.token_urlsafe(24),'role':'user'},expected=201),'temporary user creation failed')
            temporary.append(user)
        owner=temporary[0]['id'];foreign=temporary[1]['id'];query='?owner_id='+owner
        chosen=next((p for p in settings['providers'] if p['has_api_key']),None)
        provider='demo' if settings['demo_enabled'] else (chosen or settings['providers'][0])['name']
        model='local-demo' if provider=='demo' else (chosen or settings['providers'][0])['default_model']
        definition={'name':'批量回归 Agent','agent_type':'regression','system_prompt':'Use the authorized tools to complete the input task. Be concise.','model_config':{'provider':provider,'model':model},'enabled_tools':['memory_list'],'permissions':['memory.read'],'status':'enabled','limits':{'run_timeout':180_000_000_000}}
        a=must(case('agents.create','/api/v1/agents/instances'+query,'POST',definition,expected=201),'agent create failed');path='/api/v1/agents/instances/'+a['agent_id']
        case('agents.list.owner','/api/v1/agents/instances'+query,check=lambda x:any(z['agent_id']==a['agent_id'] for z in x['items']))
        definition['name']='批量回归 Agent 已修改';case('agents.update',path+query,'PUT',definition,check=lambda x:x['name']==definition['name'])
        case('agents.wrong_owner',path+'?owner_id='+foreign,'PUT',definition,expected=404)
        invalid=copy.deepcopy(definition);invalid['enabled_tools']=['nonexistent_tool'];case('agents.unknown_tool',path+query,'PUT',invalid,expected=400)
        memory=must(case('memory.create',path+'/memories'+query,'POST',{'title':'BATCH_MEMORY','content':'{"marker":"BATCH_MEMORY_MARKER","version":1}','category':'regression','priority':10},expected=201),'memory create failed')
        mp=path+'/memories/'+memory['memory_id']+query
        case('memory.list',path+'/memories'+query,check=lambda x:len(x['items'])==1)
        case('memory.search',path+'/memories'+query+'&q=BATCH_MEMORY',check=lambda x:len(x['items'])==1)
        case('memory.search.empty',path+'/memories'+query+'&q=NO_MATCH_98765',check=lambda x:not x['items'])
        case('memory.update',mp,'PUT',{'title':'BATCH_MEMORY_UPDATED','content':'{"marker":"BATCH_MEMORY_MARKER","version":2}','category':'regression','priority':20},check=lambda x:x['priority']==20)
        case('memory.wrong_owner',path+'/memories?owner_id='+foreign,expected=404)
        case('runs.invalid',path+'/runs'+query,'POST',{},expected=400)
        case('runs.owner_spoof',path+'/runs'+query,'POST',{'input':{},'owner_user_id':foreign},expected=400)
        if provider=='demo':
            case('runs.local_tool_loop',path+'/runs'+query,'POST',{'input':{'text':'read memory'}},check=lambda x:x['record']['tool_calls']==1 and 'BATCH_MEMORY_MARKER' in x['result']['output'])
        case('runs.history',path+'/runs'+query,check=lambda x:isinstance(x['items'],list))
        disabled=copy.deepcopy(definition);disabled['status']='disabled';case('agents.disable',path+query,'PUT',disabled)
        case('runs.disabled',path+'/runs'+query,'POST',{'input':{}},expected=403)
        case('agents.enable',path+query,'PUT',definition)
        if args.live and chosen:
            live=copy.deepcopy(definition);live['model_config']={'provider':chosen['name'],'model':chosen['default_model']};live['enabled_tools']=[];live['permissions']=[]
            case('live.agent.configure',path+query,'PUT',live)
            case('live.model.simple',path+'/runs'+query,'POST',{'input':{'task':'Reply with exactly BATCH_MODEL_OK. Do not use tools.'}},check=lambda x:'BATCH_MODEL_OK' in x['result']['output'])
            live['enabled_tools']=['memory_list'];live['permissions']=['memory.read'];case('live.agent.memory_tools',path+query,'PUT',live)
            case('live.model.tool_call',path+'/runs'+query,'POST',{'input':{'task':'You MUST call memory_list exactly once before answering. Then output the marker found in the memory. Keep the final answer under 20 words.'}},check=lambda x:x['record']['tool_calls']>=1 and 'BATCH_MEMORY_MARKER' in x['result']['output'])
            def configure(names):
                live['enabled_tools']=names
                live['permissions']=sorted({p for t in tools if t['name'] in names for p in t['permissions']})
                live['limits'].update({'model_calls':20,'tool_calls':50,'run_timeout':300_000_000_000})
                case('live.configure.'+names[0],path+query,'PUT',live)
            def tool_run(name,names,task,expected_errors=None):
                configure(names)
                def verify(x):
                    trace=x['record'].get('tool_executions',[])
                    for tool in names:
                        calls=[t for t in trace if t['name']==tool]
                        if not calls: return False
                        wanted=(expected_errors or {}).get(tool)
                        if wanted:
                            if not any((t.get('error') or {}).get('code') in wanted for t in calls): return False
                        elif any(t.get('error') for t in calls): return False
                    return True
                data=case(name,path+'/runs'+query,'POST',{'input':{'task':task+' Actually execute every requested tool; do not simulate results. Answer briefly after completing the calls.'}},check=verify)
                if isinstance(data,dict) and 'record' in data:
                    report['cases'][-1]['run_id']=data['result']['run_id']
                    report['cases'][-1]['tools']=data['record'].get('tool_executions',[])
                return data
            tool_run('live.memory.crud',['memory_create','memory_get','memory_search','memory_list','memory_update','memory_delete'],
                     'You must call these SIX tools in order exactly by name: 1 memory_create (title TEMP_TOOL_TEST, content v1); 2 memory_get (memory_id from create); 3 memory_search (query TEMP_TOOL_TEST); 4 memory_list; 5 memory_update (same ID, content v2); 6 memory_delete (same ID). Do not skip memory_get even though create returned its contents. Preserve other memories.')
            tool_run('live.workflow.crud',['workflow_create','workflow_get','workflow_list','workflow_update','workflow_delete'],
                     'Create a workflow named TEMP_WORKFLOW_TEST with definition {"steps":[]}, get it, list workflows, update its name to TEMP_WORKFLOW_UPDATED using returned current version and definition, then delete it.')
            tool_run('live.data.read',['data_list_tables','data_describe_table','data_query'],
                     'List datasets, describe platform_records, query platform_records fields id,title with limit 2. Empty rows are acceptable.')
            tool_run('live.search',['web_search'],'Search public web for DeepSeek official API documentation, count 2. Return source URLs.')
            tool_run('live.fetch.disabled',['web_fetch'],'Call web_fetch once with https://example.com; report its actual error code.',{'web_fetch':['NOT_IMPLEMENTED']})
            tool_run('live.notification.unavailable',['notification_send'],'Call notification_send once: title Regression, body Batch test, category regression; report the actual error code.',{'notification_send':['NOT_IMPLEMENTED']})
            child=copy.deepcopy(live);child['name']='批测子智能体';child['enabled_tools']=['memory_list'];child['permissions']=['memory.read']
            child=must(case('live.dispatch.child','/api/v1/agents/instances'+query,'POST',child,expected=201),'child create failed')
            dispatch=tool_run('live.dispatch',['agent_dispatch'],'Call agent_dispatch once targeting '+child['agent_id']+' with task: call memory_list once, then reply CHILD_BATCH_OK. Include the marker in your final answer.')
            case('live.dispatch.child_history','/api/v1/agents/instances/'+child['agent_id']+'/runs'+query,check=lambda x:any(r.get('parent_run_id')==(dispatch or {}).get('result',{}).get('run_id') and r['status']=='succeeded' and r['tool_calls']>=1 for r in x['items']))
            tiny=copy.deepcopy(live);tiny['enabled_tools']=[];tiny['permissions']=[];tiny['limits']['run_timeout']=1_000_000
            case('live.timeout.configure',path+query,'PUT',tiny)
            failed=case('live.timeout.diagnostics',path+'/runs'+query,'POST',{'input':{'task':'Reply OK'}},expected=504,check=lambda x:x['error']['code']=='TIMEOUT' and bool(x.get('run_id')) and 'deadline' in x['error']['message'])
            case('live.timeout.persisted',path+'/runs'+query,check=lambda x:any(r['run_id']==(failed or {}).get('run_id') and r['status']=='failed' for r in x['items']))
        elif args.live:
            report['live_unavailable']='No configured model key'
            report['cases'].append({'name':'live.configuration','passed':False,'error':'No configured model key'})
        case('memory.delete',mp,'DELETE',expected=204)
        case('memory.delete.missing',mp,'DELETE',expected=404)
    finally:
        for user in reversed(temporary): case('fixture.cleanup','/api/v1/users/'+user['id'],'DELETE',expected=204)
        write_report()
    if report['summary']['failed']: raise SystemExit(1)

if __name__=='__main__': main()
