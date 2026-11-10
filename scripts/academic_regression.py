#!/usr/bin/env python3
"""Authenticated read-only structure acceptance; never starts source or models."""
import json,os,urllib.parse
from pathlib import Path
from agent_regression import Client
origin='http://127.0.0.1:2610';c=Client(origin)
actor=c.login(os.environ['BOOTSTRAP_ADMIN_USERNAME'],os.environ['BOOTSTRAP_ADMIN_PASSWORD'])
checks=[]
def case(name,path,expected=200,predicate=None,client=c):
 status,data,_=client.request(path)
 ok=status==expected and (predicate(data) if predicate and status==expected else True)
 checks.append({'name':name,'status':status,'passed':bool(ok)})
 return data
case('health','/api/v1/health')
tree=case('tree.all','/api/v1/academic/tree',predicate=lambda d:len(d['offerings'])==29 and len(d['items'])>2000 and 'directory' in d)
case('source.mooc','/api/v1/academic/tree?source=mooc',predicate=lambda d:len(d['offerings'])==6 and all(x['source']=='mooc' for x in d['items']))
case('source.cx','/api/v1/academic/tree?source=chaoxing',predicate=lambda d:len(d['offerings'])==23 and all(x['source']=='chaoxing' for x in d['items']))
case('query.discussion','/api/v1/academic/items?source=chaoxing&kind=discussion&limit=100',predicate=lambda d:len(d['items'])==44 and all(x['published'].get('value') for x in d['items']))
case('query.exams','/api/v1/academic/items?source=mooc&kind=exam&limit=100',predicate=lambda d:len(d['items'])==1)
case('query.deadline','/api/v1/academic/items?source=mooc&kind=assignment&deadline_after=2026-01-01T00%3A00%3A00Z&limit=100',predicate=lambda d:bool(d['items']) and all(x['deadline'].get('value') for x in d['items']))
case('query.partialdate.reject','/api/v1/academic/items?deadline_after=11-08',400)
case('query.owner.reject','/api/v1/academic/tree?user_id=other',400)
case('query.limit.reject','/api/v1/academic/items?limit=10000',400)
case('query.connection.scope','/api/v1/academic/tree?source=mooc&connection_id=missing',404)
case('anonymous.denied','/api/v1/academic/tree',401,client=Client(origin))
mooc=case('query.course_details','/api/v1/academic/offerings?source=mooc&limit=100',predicate=lambda d:len(d['items'])==6 and all('details' in o for o in d['items']))
case('query.discussion_body','/api/v1/academic/items?source=mooc&kind=discussion&limit=100',predicate=lambda d:bool(d['items']) and all(x['metadata'].get('body_status')=='read' for x in d['items']))
case('query.notices','/api/v1/academic/items?source=mooc&kind=notice&limit=100',predicate=lambda d:len(d['items'])==14 and all(x.get('text') for x in d['items']))
root=Path(__file__).resolve().parents[2]
report={'checks':checks,'passed':sum(x['passed'] for x in checks),'failed':sum(not x['passed'] for x in checks),'offerings':len(tree.get('offerings',[])),'items':len(tree.get('items',[])),'models_started':False}
(root/'.build/academic-http-regression.json').write_text(json.dumps(report,ensure_ascii=False,indent=2))
print(json.dumps(report,ensure_ascii=False,indent=2))
raise SystemExit(bool(report['failed']))
