#!/usr/bin/env python3
"""Read the exact frozen round through the existing authenticated backend API."""
import collections,datetime,hashlib,json,os,sys,time
from pathlib import Path
sys.path.insert(0,str(Path(__file__).parent))
from agent_regression import Client
root=Path('../.build/backend-flash-full-20261115').resolve()
manifest=json.loads((root/'replay/committed.private.json').read_text())
rid=manifest['round_id'];ids=manifest['batch_ids'];origin='http://127.0.0.1:2611'
client=Client(origin);client.login(os.environ['BOOTSTRAP_ADMIN_USERNAME'],os.environ['BOOTSTRAP_ADMIN_PASSWORD'])
def get(path):
 status,data,_=client.request('/api/v1'+path)
 if status!=200:raise RuntimeError('report endpoint HTTP '+str(status))
 return data
def pages(kind):
 after='';rows=[];seen=set()
 while True:
  page=get('/control/'+kind+'?round_id='+rid+'&limit=500'+('&after='+after if after else ''));incoming=page['items']
  for row in incoming:
   key=row['item_id' if kind=='items' else 'artifact_id']
   if key in seen:raise RuntimeError('duplicate pagination identity')
   seen.add(key);rows.append(row)
  nxt=page.get('next_after','')
  if not incoming or not nxt:break
  if nxt==after:raise RuntimeError('pagination did not advance')
  after=nxt
 return rows
def write_private(name,value):
 p=root/name;raw=json.dumps(value,ensure_ascii=False,indent=2)+'\n';tmp=root/(name+'.tmp')
 with open(tmp,'w',encoding='utf8')as f:os.chmod(tmp,0o600);f.write(raw);f.flush();os.fsync(f.fileno())
 os.replace(tmp,p)
def sample():
 detail=[get('/control/batches/'+bid)for bid in ids];batches=[x['batch']for x in detail];counts=dict(collections.Counter(x['status']for x in batches));terminal=not any(x['status']in ['pending','queued','running']for x in batches)
 items=pages('items');artifacts=pages('artifacts');kind_counts=collections.defaultdict(collections.Counter)
 for item in items:
  expected='item_'+hashlib.sha256((item['batch_id']+'/'+item.get('change_id','')).encode()).hexdigest()[:32]
  kind='processing_group' if item['resource']=='course_change_group' else 'source_item' if item['item_id']==expected else 'derived_item'
  kind_counts[kind][item['status']]+=1
 if sum(kind_counts['source_item'].values())!=manifest['freeze_count']:raise RuntimeError('frozen source audit coverage differs from manifest')
 runs={r['run_id']:r for d in detail for r in d['runs']};errors=collections.Counter(r.get('error_code','')for r in runs.values()if r['state']in ['failed','cancelled','interrupted'])
 usage=collections.defaultdict(lambda:{'completed_requests':0,'input_tokens':0,'output_tokens':0,'provider_errors':0})
 for directory in [root/'execution',root/'execution-repaired']:
  log=directory/'requests.jsonl'
  if not log.exists():continue
  for line in log.read_text().splitlines():
   try:event=json.loads(line)
   except json.JSONDecodeError:continue
   run=event['run_id'];n=event['call'];request=directory/(run+'-'+str(n).zfill(4)+'-request.private.json')
   if not request.exists():continue
   model=json.loads(request.read_text()).get('Config',{}).get('model','unknown');entry=usage[model];entry['completed_requests']+=1;entry['input_tokens']+=event.get('usage',{}).get('prompt_tokens',0);entry['output_tokens']+=event.get('usage',{}).get('completion_tokens',0);entry['provider_errors']+=bool(event.get('error'))
 report={'round_id':rid,'at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'terminal':terminal,'frozen_observations':manifest['freeze_count'],'observations_in_batches':sum(b['change_count']for b in batches),'batches':counts,'item_kinds':{k:dict(v)for k,v in kind_counts.items()},'artifact_types':dict(collections.Counter(x['type']for x in artifacts)),'artifact_count':len(artifacts),'actual_runs':len(runs),'run_error_codes':dict(errors),'model_usage':dict(usage),'backend_url':origin+'/admin/tasks','scope':'Only the 25 IDs from the immutable frozen replay manifest; no model calls or business replay by this reporter.'}
 write_private('final-report.private.json' if terminal else 'latest-report.private.json',report)
 if terminal:
  write_private('final-batches.private.json',batches)
  text='# 正式全量执行结果\n\n轮次：'+rid+'\n\n后台：'+origin+'/admin/tasks\n\n已核验全部'+str(manifest['freeze_count'])+'条原始观察审计，不将处理组和拆分事项重复计数。\n\n批次状态：'+json.dumps(counts,ensure_ascii=False)+'\n\n产物：'+str(len(artifacts))+'，类型：'+json.dumps(report['artifact_types'],ensure_ascii=False)+'\n\n模型用量（Flash与Luna混合，不作纯模型对比）：\n\n```json\n'+json.dumps(report['model_usage'],ensure_ascii=False,indent=2)+'\n```\n\n全部批次已终态，不代表全部业务成功。partial/failed/NEEDS_USER_INPUT及旧预算、余额、模型服务失败如实保留，不清理历史或重放可能已有写入的事项。\n'
  p=root/'result.md';p.write_text(text);p.chmod(0o600)
 print('Full report:',counts,'artifacts='+str(len(artifacts)),'terminal='+str(terminal),flush=True)
 return terminal
if '--watch' in sys.argv:
 while True:
  if sample():break
  time.sleep(30)
else:sample()
