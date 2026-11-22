#!/usr/bin/env python3
"""Configure the authorized backend; never print provider credentials."""
import json,os,sys,hashlib,urllib.request,urllib.error
from pathlib import Path
sys.path.insert(0,str(Path(__file__).parent))
from agent_regression import Client
origin='http://127.0.0.1:2611'
c=Client(origin);actor=c.login(os.environ['BOOTSTRAP_ADMIN_USERNAME'],os.environ['BOOTSTRAP_ADMIN_PASSWORD'])
def call(path,method='GET',body=None):
 if body is None:
  status,data,_=c.request('/api/v1'+path)
 else:
  headers={'Origin':origin,'Content-Type':'application/json'}
  for cookie in c.jar:
   if cookie.name=='academic_csrf':headers['X-CSRF-Token']=cookie.value
  req=urllib.request.Request(origin+'/api/v1'+path,data=json.dumps(body,ensure_ascii=False).encode(),headers=headers,method=method)
  try:r=c.opener.open(req,timeout=60)
  except urllib.error.HTTPError as e:r=e
  with r:status=r.status;data=json.loads(r.read())
 if status!=200:raise RuntimeError('backend request failed '+path+' HTTP '+str(status)+' '+str(data.get('error',{}).get('message','')))
 return data
out=Path('../.build/backend-flash-full-20261115');out.mkdir(mode=0o700,exist_ok=True)
def private(name,value):
 p=out/name;p.write_text(json.dumps(value,ensure_ascii=False,indent=2));p.chmod(0o600)
settings=call('/agents/settings');agents=call('/agents/instances')['items'];
if not (out/'agents.before.private.json').exists():private('agents.before.private.json',agents)
if not (out/'settings.before.redacted.json').exists():private('settings.before.redacted.json',settings)
roles={}
for a in agents:
 if a['owner_user_id']==actor['id'] and a['status']=='enabled' and a['agent_type'] in ['trigger','filter','learner','worker']:
  if a['agent_type'] in roles:raise RuntimeError('ambiguous role')
  roles[a['agent_type']]=a
if len(roles)!=4:raise RuntimeError('missing role')
provider={'name':'deepseek','base_url':'https://api.deepseek.com/v1','default_model':'deepseek-flash','api_key':os.environ['BACKEND_DEEPSEEK_API_KEY'],'timeout_seconds':240,'max_concurrent':4,'retries':1}
settings['providers']=[v for v in settings['providers'] if v['name']!='deepseek']+[provider]
saved=call('/agents/settings','PUT',settings)
report={'provider':next({k:v.get(k)for k in ['name','base_url','default_model','has_api_key','timeout_seconds','max_concurrent','retries']}for v in saved['providers'] if v['name']=='deepseek'),'agents':[]}
root=Path('../agent')
for role,a in roles.items():
 d=json.loads((root/(role+'-agent')/'definition.template.json').read_text());prompt=(root/(role+'-agent')/'system-prompt.md').read_text();assert d['system_prompt']==prompt
 for key,target in [('WORKER_AGENT_ID','worker'),('LEARNER_AGENT_ID','learner')]:prompt=prompt.replace('{{'+key+'}}',roles[target]['agent_id'])
 d['system_prompt']=prompt;d['model_config']={'provider':'deepseek','mode':'default','model':''}
 result=call('/agents/instances/'+a['agent_id'],'PUT',d)
 if result['owner_user_id']!=actor['id'] or result['system_prompt']!=prompt or result['model_config']['provider']!='deepseek':raise RuntimeError('agent readback mismatch')
 report['agents'].append({'role':role,'id':result['agent_id'],'prompt_sha256':hashlib.sha256(prompt.encode()).hexdigest(),'tools':len(result['enabled_tools']),'model_config':result['model_config']})
private('configuration-applied.json',report)
print(json.dumps(report,ensure_ascii=False,indent=2))
