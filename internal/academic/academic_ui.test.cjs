const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const path=require('node:path');
const html=fs.readFileSync(path.join(__dirname,'academic.html'),'utf8');
const script=html.match(/<script>([\s\S]*?)<\/script>/)[1];
function element(tag='div'){
 return {tag,value:'',textContent:'',children:[],disabled:false,append(...nodes){this.children.push(...nodes)},replaceChildren(...nodes){this.children=nodes},setAttribute(){},addEventListener(name,fn){(this.listeners||=Object.create(null))[name]=fn},querySelector(){return element('button')},get firstChild(){return this.children[0]}};
}
function harness(fetch){
 const elements=new Map();
 const document={cookie:'academic_csrf=token',getElementById(id){if(!elements.has(id))elements.set(id,element());return elements.get(id)},createElement:element};
 class FormData {constructor(form){this.entries=form.entries||[]}*[Symbol.iterator](){yield* this.entries}}
 const context=vm.createContext({document,fetch,URLSearchParams,FormData,Intl,console});
 new vm.Script(script.replace(';load();loadPolicy();',';')).runInContext(context);
 return {run:code=>vm.runInContext(code,context),elements,context};
}
const response=(body,status=200)=>({ok:status<400,status,json:async()=>body});
test('catalog record nodes remain clickable and render unit descendants with kind counts',()=>{
 const h=harness();
 h.run(`snapshot={items:[{kind:'chapter',title:'章节'},{kind:'lesson',title:'课时'},{kind:'assignment',title:'测验'}]};draw([{label:'章节',item:0,count:3,kind_counts:{chapter:1,lesson:1,assignment:1},children:[{label:'课时',item:1,count:2,children:[{label:'测验',item:2,count:1}]}]}],$('tree'))`);
 const chapter=h.elements.get('tree').children[0];
 assert.equal(chapter.tag,'details');
 assert.match(chapter.children[0].textContent,/作业与任务 1/);
 assert.equal(chapter.children[1].tag,'details');
 assert.match(chapter.children[1].children[1].textContent,/测验 \[作业与任务\]/);
 assert.equal(h.run(`labels.discussion_reply_user`),'讨论参与与回复记录');
 assert.match(h.run(`offeringLabel({name:'同名',source:'mooc',native_class_id:'class',native_term_id:'owner',source_terms:[{termId:'reference'}],connection_id:'conn'})`),/教学班 class.*开课期次 owner.*reference.*连接 conn/);
});
test('large participant lists stay collapsed and render only once when expanded',()=>{
 const h=harness();
 h.run(`snapshot={items:[{kind:'discussion_reply_user',title:'参与人'}]};draw([{label:'参与记录',count:1,kind_counts:{discussion_reply_user:1},children:[{label:'参与人',item:0,count:1}]}],$('tree'))`);
 const folder=h.elements.get('tree').children[0];
 assert.equal(folder.open,false);assert.equal(folder.children.length,1);
 folder.open=true;folder.listeners.toggle();assert.equal(folder.children.length,2);
 folder.listeners.toggle();assert.equal(folder.children.length,2);
});
test('inline UI script parses and preserves hidden and filtered selections',()=>{
 const h=harness();
 h.run(`snapshot={offerings:[{offering_id:'new',name:'同名课程',connection_id:'active',source:'mooc',teacher:'张老师'}]};applyPolicy({version:4,excluded:[{offering_id:'old',name:'同名课程',connection_id:'disabled',source:'mooc'}]});renderPolicy()`);
 assert.equal(h.elements.get('policy-list').children.length,2);
 h.run(`$('policy-filter').value='张老师';renderPolicy()`);
 assert.equal(h.elements.get('policy-list').children.length,1);
 const checkbox=h.elements.get('policy-list').children[0].children[0];checkbox.checked=true;checkbox.onchange();
 assert.equal(h.run(`policySelected.has('old')&&policySelected.has('new')`),true);
 h.run(`snapshot={offerings:[]};renderPolicy()`);
 assert.equal(h.run(`policySelected.size`),2);
 h.run(`$('policy-filter').value='';renderPolicy()`);
 const old=h.elements.get('policy-list').children[0].children[0];
 old.checked=false;old.onchange();
 assert.equal(h.run(`policySelected.has('old')`),false);
});
test('policy read failure retains drafts and disables save',async()=>{
 const h=harness(async()=>response({error:{message:'读取失败'}},500));
 h.run(`applyPolicy({version:3,excluded:[{offering_id:'old'}]});policySelected.add('draft');policyDirty=true`);
 await h.run('loadPolicy()');
 assert.equal(h.run(`policySelected.has('old')&&policySelected.has('draft')`),true);
 assert.equal(h.elements.get('policy-save').disabled,true);
 assert.equal(h.elements.get('policy-clear').disabled,true);
});
test('late policy responses cannot replace the newer version',async()=>{
 const pending=[];const h=harness(()=>new Promise(resolve=>pending.push(resolve)));
 const first=h.run('loadPolicy()');const second=h.run('loadPolicy()');
 pending[1](response({version:2,excluded:[{offering_id:'new'}]}));await second;
 pending[0](response({version:1,excluded:[{offering_id:'old'}]}));await first;
 assert.equal(h.run('policyVersion'),2);
 assert.equal(h.run(`policySelected.has('new')&&!policySelected.has('old')`),true);
});
test('scope requests ignore stale responses and preserve policy drafts',async()=>{
 const pending=[];const h=harness(()=>new Promise(resolve=>pending.push(resolve)));
 h.run(`applyPolicy({version:1,excluded:[{offering_id:'hidden'}]});policySelected.add('draft');policyDirty=true`);
 const first=h.run('load()');const second=h.run('load()');
 pending[1](response({offerings:[{offering_id:'new',name:'新目录'}],items:[],directory:[],coverage:{}}));await second;
 pending[0](response({offerings:[{offering_id:'stale',name:'过时目录'}],items:[],directory:[],coverage:{}}));await first;
 assert.equal(h.run('snapshot.offerings[0].offering_id'),'new');
 assert.equal(h.run(`policySelected.has('hidden')&&policySelected.has('draft')`),true);
});
test('save sends complete set with CSRF and conflict preserves draft until explicit reload',async()=>{
 let sent;const h=harness(async(url,options)=>{sent={url,options};return response({error:{message:'版本冲突'}},409)});
 h.run(`applyPolicy({version:9,excluded:[{offering_id:'hidden'}]});policySelected.add('visible');policyChanged()`);
 await h.run('savePolicy()');
 assert.equal(sent.options.method,'PUT');assert.equal(sent.options.headers['X-CSRF-Token'],'token');
 assert.deepEqual(JSON.parse(sent.options.body),{version:9,offering_ids:['hidden','visible']});
 assert.equal(h.run(`policySelected.has('hidden')&&policySelected.has('visible')`),true);
 assert.equal(h.elements.get('policy-save').disabled,true);
 assert.match(h.elements.get('policy-message').textContent,/重新读取/);
});
