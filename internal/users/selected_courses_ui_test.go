package users

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectedCoursesPanelReadOnlyAndExplicitFetch(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	raw, err := os.ReadFile("admin.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(raw)
	start := strings.Index(html, "function createSelectedCoursesPanel(")
	end := strings.Index(html[start:], "async function loadPlatforms(){")
	if start < 0 || end < 0 {
		t.Fatal("selected course panel missing")
	}
	script := html[start : start+end]
	harness := `const assert=require('node:assert/strict');
class E {constructor(tag){this.tagName=tag;this.children=[];this.listeners={};this.value='';this.disabled=false;this.hidden=false;this.textContent='';}append(...v){this.children.push(...v);}replaceChildren(...v){this.children=[...v];}setAttribute(k,v){this[k]=v;}addEventListener(k,f){this.listeners[k]=f;}change(){this.listeners.change?.();}}
global.document={createElement:t=>new E(t)};let actor='owner',epoch=1,calls=[],queue=[];const platformCurrent=(o,e)=>actor===o&&epoch===e;const cell=(r,v)=>{const x=new E('td');x.textContent=String(v);r.append(x);};async function api(path,method='GET',body){calls.push({path,method,body});if(!queue.length)throw Error('unexpected API');const q=queue.shift();if(q instanceof Error)throw q;return typeof q==='function'?await q():q;}
` + script + `
const course=(id,parent='[2026,1]')=>({id,source:'jwxt',kind:'course',parent_id:parent,content:{title:'课程'+id,fields:{teacher:'教师',credits_raw:'2',nature:'必修',class_id:'class'}}});
(async()=>{
 const year=new E('input'),term=new E('select');year.value='2026';term.value='1';const c={id:'a/b',user_id:'owner',enabled:true};const p=createSelectedCoursesPanel(c,year,term,'owner',1);assert.equal(calls.length,0,'construction must not fetch');
 queue.push({status:'page_committed',academic_year:2026,term:1,page_count:2,has_more:true},{items:[course(1),course(2,'[2025,2]')],next_after:10});await p.fetch();assert.deepEqual(calls[0].body,{resource:'course',academic_year:2026,term:1});assert(!('calendar'in calls[0].body));assert.equal(p.state().records.length,1);assert.equal(p.state().fetchMore,true);assert.equal(p.state().nextAfter,10);
 queue.push({items:[course(1),course(3)],next_after:0});await p.more();assert.equal(p.state().records.length,2,'IDs are de-duplicated');assert.equal(p.state().archiveComplete,true);assert(calls[2].path.includes('after=10'));
 const online=course(20);online.course_delivery={mode:'online',basis:'default_missing_time_and_location',is_default:true,label:'线上课程（默认）'};queue.push({items:[online,course(21)],next_after:0});await p.read();const texts=[];function textWalk(e){texts.push(e.textContent);for(const child of e.children||[])textWalk(child);}textWalk(p.element);assert(texts.includes('线上课程（默认）'));assert(texts.includes('未标记'),'unclassified is not forced offline');
 queue.push({status:'page_committed',academic_year:2026,term:1,page_count:0,has_more:false},{items:[],next_after:0});await p.fetch();assert.equal(p.state().committedCount,2);assert.equal(p.state().fetchMore,false);assert.equal(p.state().records.length,0);assert.equal(p.state().archiveComplete,true);
 queue.push({items:[course(4)],next_after:0});await p.read();const saved=p.state().records[0].id;queue.push(new Error('upstream unavailable'));await p.fetch();assert.equal(p.state().records[0].id,saved,'failed fetch preserves displayed records');
 year.value='2025';term.value='2';year.change();queue.push(new Error('error'));await p.fetch();assert.equal(p.state().records[0].id,4);assert.equal(p.state().resultSemester.academic_year,2026,'failed different semester keeps old label');
 let resolve;queue.push(()=>new Promise(r=>resolve=r));const running=p.fetch();await Promise.resolve();assert(year.disabled&&term.disabled);const n=calls.length;await p.fetch();assert.equal(calls.length,n,'busy prevents duplicate upstream request');actor='other';resolve({status:'page_committed',academic_year:2025,term:2,page_count:5,has_more:false});await running;assert.equal(calls.length,n,'stale owner response does not read records');assert.equal(p.state().records[0].id,4);
 actor='owner';year.value='';assert.doesNotThrow(()=>year.change());const priorCalls=calls.length;await p.fetch();assert.equal(calls.length,priorCalls,'invalid semester cannot trigger upstream');const y2=new E('input'),t2=new E('select');y2.value='2026';t2.value='1';const q=createSelectedCoursesPanel(c,y2,t2,'owner',1);let done;queue.push(()=>new Promise(r=>done=r));const reading=q.read();await Promise.resolve();epoch=2;done({items:[course(99)],next_after:0});await reading;assert.equal(q.state().records.length,0,'stale epoch response ignored');
 console.log('selected course UI behavior verified');
})().catch(e=>{console.error(e);process.exit(1)});`
	path := filepath.Join(t.TempDir(), "check.cjs")
	if err = os.WriteFile(path, []byte(harness), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, path).CombinedOutput()
	if err != nil {
		t.Fatalf("%s\n%v", out, err)
	}
}
