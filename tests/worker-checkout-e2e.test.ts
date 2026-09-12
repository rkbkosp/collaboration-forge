import test from 'node:test';
import assert from 'node:assert/strict';
import {spawn,spawnSync} from 'node:child_process';
import {mkdtemp,mkdir,writeFile,readFile,realpath,rm} from 'node:fs/promises';
import {join} from 'node:path';
import {fileURLToPath} from 'node:url';
import {randomUUID} from 'node:crypto';
import {Controller} from '../pi-extension/controller.ts';
import {registerForge} from '../pi-extension/forge.ts';
import {startForge} from './harness.ts';
const script=fileURLToPath(new URL('../scripts/forge.mjs',import.meta.url));

test('Fornax-style multi-repository project: CLI and Pi share one claim across three retained worktrees',{timeout:90_000},async()=>{
 const f=await startForge();const dir=await realpath(await mkdtemp('/tmp/forge-worker-checkout-'));
 let broker:ReturnType<typeof spawn>|undefined;let brokerExit:Promise<unknown>|undefined;let piController:Controller|undefined;
 const env={...process.env,FORGE_URL:f.url,FORGE_WORKER_TOKEN_FILE:join(f.dataDir,'worker-token')};
 for(const k of Object.keys(env))if(k.startsWith('FORGE_CODEX_')||k==='FORGE_SOCKET')delete env[k as keyof typeof env];
 async function cli(args:string[],cwd?:string){
  return new Promise<any>((resolve,reject)=>{
   const child=spawn(process.execPath,[script,...args],{env,cwd,stdio:['ignore','pipe','pipe']});let out='';let err='';
   child.stdout.on('data',b=>out+=b);child.stderr.on('data',b=>err+=b);
   child.once('error',reject);child.once('close',code=>{
    assert.ok(!out.includes(f.workerToken)&&!err.includes(f.workerToken));
    if(code!==0){reject(new Error(`CLI failed ${code}: ${err}`));return}try{resolve(JSON.parse(out))}catch(e){reject(e)}
   });
  });
 }
 try{
  const sources=['fornax','fornax-agent-runtime','fornax-console'].map(name=>join(dir,name));
  for(const source of sources){
   await mkdir(source);
   const git=(args:string[])=>{const p=spawnSync('git',args,{cwd:source,encoding:'utf8'});assert.equal(p.status,0,p.stderr)};
   git(['init','-q']);git(['config','user.name','Fixture']);git(['config','user.email','fixture@example.invalid']);await writeFile(join(source,'a'),'base\n');git(['add','.']);git(['commit','-qm','base']);await writeFile(join(source,'a'),'dirty\n');
  }
  const resolver=fileURLToPath(new URL('../scripts/forge-project-root.mjs',import.meta.url));
  for(const source of sources){const routed=spawnSync(process.execPath,[resolver,...sources],{cwd:source,encoding:'utf8'});assert.equal(routed.status,0);assert.equal(routed.stdout.trim(),source)}
  const socket=join(dir,'s.sock');broker=spawn(process.execPath,[script,'session','start','--socket',socket],{env,stdio:['ignore','ignore','ignore']});brokerExit=new Promise(r=>broker!.once('close',r));
  await cli(['session','wait','--socket',socket]);
  for(const kind of ['cli','pi']){
   const issue=(await f.admin(`/api/v1/projects/${f.projectID}/issues`,{title:kind+' checkout fixture'})).issue;
   const handlers=new Map<string,Function>();const tools=new Map<string,any>();
   if(kind==='pi'){
    const pi={on:(n:string,h:Function)=>handlers.set(n,h),registerTool:(t:any)=>tools.set(t.name,t),registerCommand:()=>{},sendMessage:()=>{}};
    registerForge(pi as any,async id=>{piController=new Controller({url:f.url,workerToken:f.workerToken,ttlSeconds:60},{sessionId:id});return piController});
    await handlers.get('session_start')!({}, {sessionManager:{getSessionId:()=>randomUUID()}});
   }
   const piTool=async(name:string,params:any)=>JSON.parse((await tools.get(name).execute('fixture',params)).content[0].text);
   const claim=kind==='cli'?await cli(['issue','claim',issue.uid,'--socket',socket]):await piTool('issue_claim',{ref:issue.uid});
   const workspaces:any[]=[];
   for(const [index,source] of sources.entries()){
    const dirty=index!==1;
    let work=kind==='cli'?await cli(['checkout',issue.uid,...(dirty?['--dirty']:['--ref','HEAD']),'--source',source,'--socket',socket],source):await piTool('checkout',{ref:issue.uid,source,...(dirty?{dirty:true}:{commit:'HEAD'})});
    for(let n=0;n<500&&work.state==='preparing';n++){await new Promise(r=>setTimeout(r,20));work=await piTool('checkout_status',{workspace_id:work.workspace_id})}
    assert.equal(work.state,'ready');assert.equal(await readFile(join(work.worktree,'a'),'utf8'),dirty?'dirty\n':'base\n');
    const live=await f.admin(`/api/v1/projects/${f.projectID}/issues/${issue.uid}`);assert.equal(live.lease.claim_uid,claim.lease.claim_uid);
    assert.equal(work.issue_uid,issue.uid);assert.equal(work.project_uid,(await f.admin('/forge/v1/project')).project.uid);
    // Explicit installed CLI path + the same socket preserve project routing
    // even from snapshot repos that no longer share source Git common dirs.
    if(kind==='cli'){const current=await cli(['issue','get',issue.uid,'--socket',socket],work.worktree);assert.equal(current.lease.claim_uid,claim.lease.claim_uid)}
    workspaces.push(work);
   }
   assert.equal(new Set(workspaces.map(w=>w.repository_id)).size,3);
   assert.equal(new Set(workspaces.map(w=>w.worktree)).size,3);
   assert.equal(new Set(workspaces.map(w=>w.tenure)).size,1);
   const listed=await cli(['checkout','list',issue.uid],workspaces[0].worktree);assert.equal(listed.workspaces.length,3);
   const close={ref:issue.uid,reason:'audit-no-change',message:'Verified generated checkout fixture and exact lease preservation with retained archive. No fixture product change.',evidence:[{type:'no-change-audit',rationale:'Generated integration fixture exercises checkout lifecycle.'}]};
   const closed=kind==='cli'?await cli(['issue','close',issue.uid,'--data',JSON.stringify({...close,ref:undefined}),'--socket',socket]):await piTool('issue_close',close);
   assert.equal(closed.workspace_archive.state,'archived');assert.equal(closed.workspace_archive.workspaces.length,3);
   for(const work of workspaces){
    const archived=await cli(['checkout','status',work.workspace_id],work.worktree);assert.equal(archived.state,'archived');
    assert.equal(await readFile(join(work.worktree,'a'),'utf8'),work.source_kind==='dirty'?'dirty\n':'base\n');
   }
   if(kind==='pi')await handlers.get('session_shutdown')!({reason:'stop'});
  }
  await cli(['session','stop','--socket',socket]);await brokerExit;
  for(const source of sources)assert.equal(await readFile(join(source,'a'),'utf8'),'dirty\n');
 }finally{await piController?.shutdown();if(broker&&broker.exitCode===null)broker.kill('SIGTERM');await brokerExit;await f.close();await rm(dir,{recursive:true,force:true})}
});
