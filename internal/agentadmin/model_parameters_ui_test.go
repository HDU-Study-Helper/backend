package agentadmin

import (
	"os/exec"
	"testing"
)

// Execute the actual inline page JavaScript with a minimal DOM. No server,
// browser installation, model request or authenticated API is involved.
func TestAgentModelParametersUIOffline(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is required for the offline page JavaScript contract test")
	}
	cmd := exec.Command(node, "-e", modelParametersUITest, "agents.html")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("offline model-parameter UI test: %v\n%s", err, out)
	}
}

const modelParametersUITest = `
const fs=require('node:fs'),vm=require('node:vm'),assert=require('node:assert/strict');
const html=fs.readFileSync(process.argv[1],'utf8');
const script=html.match(/<script>([\s\S]*?)<\/script>/)[1];
new vm.Script(script); // Compile the complete event wiring too.
for(const id of ['reasoningEffort','reasoningHint','modelParameters'])assert(html.includes('id="'+id+'"'));
const elements=new Map();
function element(id){if(!elements.has(id)){const e={id,value:'',checked:false,textContent:'',disabled:false,required:false,hidden:false,dataset:{},options:[],children:[],replaceChildren(){this.children=[];},append(...values){this.children.push(...values);if(this.id==='reasoningEffort')for(const x of values){this.options.push(x);x.remove=()=>{this.options=this.options.filter(v=>v!==x);};}},querySelectorAll(){return[];}};elements.set(id,e);}return elements.get(id);}
const document={getElementById:element,querySelectorAll:()=>[],createElement:()=>({value:'',dataset:{}})};
const context=vm.createContext({document,console,JSON,Number,Set,Error,encodeURIComponent,decodeURIComponent,fetch(){throw Error('offline test must not make API calls');}});
// Leave bootstrap/event subscriptions unexecuted; the actual named functions are tested.
vm.runInContext(script.slice(0,script.indexOf("document.querySelectorAll('[data-tab]')")),context);
function run(source){return vm.runInContext(source,context);}
function json(source){return JSON.parse(JSON.stringify(run(source)));}
run("state.settings={providers:[{name:'luna',default_model:'gpt-6-luna',default_reasoning_effort:'high'},{name:'other',default_model:'other-model'}]};$('agentProvider').value='luna';$('agentModelMode').value='default';");
run("setModelParameterFields({parameters:{top_p:0.8,seed:0,reasoning_effort:null}})");
assert.equal(run("$('reasoningEffort').value"),'off');assert.equal(run("$('modelParameters').value.includes('reasoning_effort')"),false);
assert.deepEqual(json('modelParametersPayload()'),{top_p:0.8,seed:0,reasoning_effort:null});
for(const effort of ['low','high','max']){run('setModelParameterFields({parameters:{reasoning_effort:'+JSON.stringify(effort)+'}})');assert.equal(run("$('reasoningEffort').value"),effort);assert.deepEqual(json('modelParametersPayload()'),{reasoning_effort:effort});}
run('setModelParameterFields({parameters:{}})');assert.equal(run("$('reasoningEffort').value"),'inherit');assert.deepEqual(json('modelParametersPayload()'),{});assert.match(run("$('reasoningHint').textContent"),/继承模型服务设置：high/);
run("$('agentProvider').value='other';syncModelMode()");assert.match(run("$('reasoningHint').textContent"),/供应商默认/);assert.deepEqual(json('modelParametersPayload()'),{});
run("$('agentModelMode').value='custom';$('agentModel').value='gpt-6-luna';syncModelMode()");assert.match(run("$('reasoningHint').textContent"),/供应商默认/);
run("$('agentProvider').value='luna';$('agentModel').value='other-model';syncModelMode()");assert.match(run("$('reasoningHint').textContent"),/继承模型服务设置：high/);
function rejected(text,pattern){element('modelParameters').value=text;assert.throws(()=>run('modelParametersPayload()'),pattern);assert.equal(element('modelParameters').value,text);}
rejected('{bad',/JSON 无效/);rejected('[]',/JSON 对象/);rejected('null',/JSON 对象/);rejected('12',/JSON 对象/);rejected('{"reasoning_effort":"low"}',/思考强度/);rejected('{"unrecognized":42}',/不支持.*unrecognized/);
rejected('{"top_p":2}',/top_p/);rejected('{"seed":0.5}',/整数/);rejected('{"max_tokens":0}',/正整数/);rejected('{"presence_penalty":-3}',/presence_penalty/);rejected('{"frequency_penalty":"1"}',/有限数字/);
run("setModelParameterFields({parameters:{reasoning_effort:'legacy-value',seed:42}})");assert.equal(run("$('reasoningEffort').value"),'unsupported');assert.throws(()=>run('modelParametersPayload()'),/现有思考强度值不受支持/);assert.equal(run("JSON.parse($('modelParameters').value).seed"),42);
// Re-loading known values removes the old unsupported option and does not alter the saved object.
run("renderProviderOptions=value=>{$('agentProvider').value=value||'luna';};renderTools=()=>{};renderAgentList=()=>{};syncMemoryAgents=()=>{};");
run("const savedFixture={agent_id:'agent',name:'A',agent_type:'worker',status:'enabled',system_prompt:'prompt',enabled_tools:[],model_config:{provider:'luna',mode:'default',model:'',parameters:{top_p:0.7,seed:8,reasoning_effort:null}},limits:{context_bytes:1048576,memory_bytes:16384,tool_result_bytes:131072,max_concurrent:4,model_calls:9,tool_calls:20,run_timeout:100000000000,tool_timeout:20000000000,dispatch_depth:5}};editAgent(savedFixture);");
assert.equal(run("$('reasoningEffort').value"),'off');assert.equal(run("$('reasoningEffort').options.filter(o=>o.dataset.unsupported).length"),0);
const before=json('savedFixture');const payload=json('agentPayload()');assert.deepEqual(payload.model_config.parameters,{top_p:0.7,seed:8,reasoning_effort:null});assert.equal(payload.limits.context_bytes,1048576);assert.equal(payload.limits.tool_result_bytes,131072);assert.equal(payload.limits.max_concurrent,4);assert.equal(payload.limits.dispatch_depth,5);
run("const resultPayload=agentPayload();resultPayload.model_config.parameters.seed=999;resultPayload.limits.context_bytes=1;");assert.deepEqual(json('savedFixture'),before);
// Changing provider updates hints, not the user's editable parameter values.
run("$('agentProvider').value='other';syncModelMode()");assert.deepEqual(json('agentPayload().model_config.parameters'),{top_p:0.7,seed:8,reasoning_effort:null});assert.equal(run("$('reasoningEffort').value"),'off');
run("setModelParameterFields({parameters:{max_tokens:4096,presence_penalty:-2,frequency_penalty:2}});$('temperature').value='0';$('maxTokens').value='1024';");const p=json('agentPayload()');assert.equal(p.model_config.temperature,0);assert.equal(p.model_config.max_tokens,1024);assert.equal(p.model_config.parameters.max_tokens,4096);
console.log('inline page JS compiled; reasoning null/inherit/levels, JSON validation, provider changes, saved-parameter immutability and limits preservation passed');
`
