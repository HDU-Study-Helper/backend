// Batch UI regression. Uses a temporary pending connection (no upstream credentials),
// and browser-only fixtures for job states. Never mutates existing group lists.
const assert=require('node:assert/strict');
const {chromium}=require(process.env.PLAYWRIGHT_MODULE||'playwright');
const origin=process.env.DEMO_ORIGIN||'http://127.0.0.1:2610';
(async()=>{
 const browser=await chromium.launch({headless:true,...(process.env.CHROMIUM_PATH?{executablePath:process.env.CHROMIUM_PATH}:{}),args:['--no-sandbox']});
 let admin,userID;const errors=[],checked=[];
 async function call(page,path,method='GET',body){return page.evaluate(async({path,method,body})=>{const csrf=document.cookie.split('; ').find(v=>v.startsWith('academic_csrf='))?.split('=').slice(1).join('=');const r=await fetch('/api/v1'+path,{method,headers:{'Content-Type':'application/json','X-CSRF-Token':decodeURIComponent(csrf||'')},body:body===undefined?undefined:JSON.stringify(body)});if(!r.ok)throw Error('API '+r.status+' '+path);return r.status===204?null:r.json();},{path,method,body});}
 async function login(page,username,password){page.on('pageerror',e=>errors.push(e.message));await page.goto(origin+'/admin');await page.locator('#loginForm input[name=username]').fill(username);await page.locator('#loginForm input[name=password]').fill(password);await page.locator('#loginForm button').click();await page.waitForFunction(()=>document.querySelector('#account')&&!document.querySelector('#account').hidden&&!document.querySelector('#dwsRefresh').disabled);}
 async function ready(page){await page.waitForFunction(()=>!document.querySelector('#dwsGroupReload').disabled);}
 async function save(page){await page.locator('#dwsGroupSave').click();await page.waitForFunction(()=>!document.querySelector('#dwsGroupReload').disabled);}
 try{
 admin=await browser.newPage({viewport:{width:1440,height:960}});await login(admin,process.env.BOOTSTRAP_ADMIN_USERNAME,process.env.BOOTSTRAP_ADMIN_PASSWORD);await ready(admin);
 const before=await call(admin,'/dws/connections');const snapshots={};for(const c of before.items)snapshots[c.id]=await call(admin,'/dws/connections/'+c.id+'/groups');
 if(process.env.DWS_LIVE_DISCOVERY==='1'){
  const response=admin.waitForResponse(r=>r.url().includes('/available-groups'));
  await admin.locator('#dwsGroupDiscover').click();const r=await response;const data=await r.json();
  if(!r.ok())throw Error('Real discovery HTTP '+r.status()+' '+(data.error?.code||''));
  await ready(admin);assert.ok(Array.isArray(data.items));console.log(JSON.stringify({liveDiscovery:true,count:data.items.length,hasMore:data.has_more}));checked.push('real_upstream_group_discovery');
 }

 await admin.locator('#dwsGroupsPanel').screenshot({path:process.env.DWS_SCREENSHOT_DIR?process.env.DWS_SCREENSHOT_DIR+'/dws-groups-desktop.png':'/tmp/dws-groups-desktop.png'});
 await admin.setViewportSize({width:390,height:844});assert.equal(await admin.evaluate(()=>document.querySelector('#dwsGroupConnection').getBoundingClientRect().right<=window.innerWidth),true);await admin.locator('#dwsGroupsPanel').screenshot({path:process.env.DWS_SCREENSHOT_DIR?process.env.DWS_SCREENSHOT_DIR+'/dws-groups-mobile.png':'/tmp/dws-groups-mobile.png'});checked.push('real_panel_desktop_mobile');
 const username='dwstest_'+Date.now(),password=require('node:crypto').randomBytes(24).toString('base64url');
 const user=await call(admin,'/users','POST',{username,password,display_name:'DWS UI temporary regression',role:'user'});userID=user.id;
 const ctx=await browser.newContext();const page=await ctx.newPage();await login(page,username,password);
 const c=await call(page,'/dws/connections','POST',{profile:'fixturecorp:fixtureuser',corp_id:'fixturecorp',upstream_user_id:'fixtureuser',mode:'personal'});const path='/dws/connections/'+c.id;
 // Browser sees a selectable active connection; only discovery is mocked.
 await page.route('**/api/v1/dws/connections',route=>route.fulfill({json:{items:[{...c,enabled:true,state:'active'}]}}));
 let discoveryFail=false;
 await page.route('**/api/v1/dws/connections/*/available-groups*',route=>{
  if(discoveryFail)return route.fulfill({status:502,json:{error:{message:'fixture upstream unavailable'}}});
  const more=new URL(route.request().url()).searchParams.has('cursor');return route.fulfill({json:more?{items:[{id:'fixture-cid-two',name:'第二个群'}],has_more:false}:{items:[{id:'fixture-cid',name:'<img src=x onerror=alert(1)>'}],has_more:true,next_cursor:'page-two'}});
 });
 await page.reload();await ready(page);await page.locator('#dwsGroupDiscover').click();await ready(page);
 assert.equal(await page.locator('#dwsGroupCandidates img').count(),0);
 await page.locator('#dwsGroupCandidates input').first().check();await save(page);
 assert.equal((await call(page,path+'/groups')).items[0].id,'fixture-cid');
 assert.equal(await page.locator('.dws-group-id').isEditable(),false);checked.push('select_without_typing_and_real_save');
 await page.locator('#dwsGroupMore').click();await ready(page);assert.equal(await page.locator('#dwsGroupCandidates input').count(),2);assert.equal(await page.locator('#dwsGroupCandidates input').first().isChecked(),true);
 await page.locator('#dwsGroupCandidates input').last().check();await save(page);assert.equal((await call(page,path+'/groups')).items.length,2);checked.push('pagination_preserves_selection');
 await page.reload();await ready(page);assert.equal(await page.locator('.dws-group-id').count(),2);await page.locator('#dwsGroupDiscover').click();await ready(page);assert.equal(await page.locator('#dwsGroupCandidates input').first().isChecked(),true);assert.equal(await page.locator('.dws-group-id').count(),2);checked.push('reload_and_unlisted_saved_groups_preserved');
 discoveryFail=true;await page.locator('#dwsGroupDiscover').click();await ready(page);assert.match(await page.locator('#dwsGroupDiscoveryStatus').textContent(),/读取群失败/);assert.equal(await page.locator('.dws-group-id').count(),2);discoveryFail=false;checked.push('upstream_failure_preserves_selection');
 await page.locator('#dwsGroupCandidates input').first().uncheck();
 await page.route('**/api/v1/dws/connections/*/groups',async route=>{if(route.request().method()==='PUT')return route.fulfill({status:503,json:{error:{message:'fixture save failure'}}});await route.continue();});
 await save(page);assert.match(await page.locator('#dwsGroupStatus').textContent(),/保存失败/);assert.equal(await page.locator('.dws-group-id').count(),1);await page.unroute('**/api/v1/dws/connections/*/groups');checked.push('failed_save_keeps_selection');
 await page.getByRole('button',{name:'移除',exact:true}).click();await save(page);assert.deepEqual((await call(page,path+'/groups')).items,[]);checked.push('real_remove_save_empty');
 // Only this browser sees an active connection and synthetic group/job responses.
 await page.route('**/api/v1/dws/connections/*/groups',route=>route.fulfill({json:{items:[{id:'browser-only-cid',name:'Browser-only fixture'}]}}));
 let terminal='succeeded';const job={id:'browser-only-job',group_id:'browser-only-cid',state:'queued'};
 await page.route('**/api/v1/dws/connections/*/sync',route=>route.fulfill({status:202,json:job}));await page.route('**/api/v1/dws/connections/*/jobs/*',route=>route.fulfill({json:{...job,state:terminal,...(terminal==='failed'?{error_code:'fixture_upstream_error'}:{})}}));
 await page.reload();await ready(page);await page.getByRole('button',{name:'手动试采',exact:true}).click();await page.waitForFunction(()=>document.querySelector('#dwsGroupJobStatus').textContent.includes('采集成功'));checked.push('fixture_queued_to_success');
 terminal='failed';await page.getByRole('button',{name:'手动试采',exact:true}).click();await page.waitForFunction(()=>document.querySelector('#dwsGroupJobStatus').textContent.includes('fixture_upstream_error'));checked.push('fixture_failure_visible');
 await ctx.close();
 const stable=page=>page.items.map(({updated_at,...c})=>c);assert.deepEqual(stable(await call(admin,'/dws/connections')),stable(before));for(const c of before.items)assert.deepEqual(await call(admin,'/dws/connections/'+c.id+'/groups'),snapshots[c.id]);checked.push('existing_connections_and_groups_unchanged');assert.deepEqual(errors,[]);
 console.log(JSON.stringify({passed:true,checked,pageErrors:errors}));
 }finally{if(userID&&admin)await call(admin,'/users/'+userID,'DELETE');await browser.close();}
})().catch(e=>{console.error(e.message);process.exitCode=1});
