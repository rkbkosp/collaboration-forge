import test from 'node:test';
import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { execFile, fork } from 'node:child_process';
import { once } from 'node:events';
import { promisify } from 'node:util';
import { resolve, join } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { performance } from 'node:perf_hooks';
import { Controller, ForgeError } from '../extensions/controller.ts';
import { startForge } from './harness.ts';

const exec = promisify(execFile);
async function eventually(check:()=>Promise<boolean>,timeout:number) {
 const end=performance.now()+timeout;
 while(performance.now()<end){if(await check())return;await delay(500);}
 assert.fail('timed out waiting for real server state');
}

test('real workers: collaboration, automatic heartbeat, committed-response loss, stale tenure and human timeline', {timeout:65_000}, async()=>{
 const f=await startForge();
 const session=randomUUID();
 let lost=false, renewals=0;
 const closes:Array<{body:string,headers:Headers}>=[];
 const transport:typeof fetch=async(input,init)=>{
  const url=String(input);
  if(url.endsWith('/issue_renew')) renewals++;
  if(url.endsWith('/issue_close')) closes.push({body:String(init?.body),headers:new Headers(init?.headers)});
  const response=await fetch(input,init);
  if(url.endsWith('/issue_close') && response.ok && !lost){lost=true;await response.arrayBuffer();throw new Error('injected committed response loss');}
  return response;
 };
 const config={url:f.url,workerToken:f.workerToken,ttlSeconds:60};
 const a=new Controller(config,{sessionId:session,fetch:transport});
 const b=new Controller(config,{sessionId:session}); // same persisted Pi session, new runtime
 try {
  const created=await f.admin(`/api/v1/projects/${f.projectID}/issues`,{title:'Deploy staging acceptance',body:'Human plan: prove execution exclusion and preserve evidence.'});
  const uid=created.issue.uid;
  const first=await a.execute('issue_claim',{ref:uid});
  assert.equal('execution_token' in first,false);
  await assert.rejects(b.execute('issue_claim',{ref:uid}),e=>e instanceof ForgeError && e.status===409);
  assert.equal((await b.guardTool('edit'))?.block,true);
  await b.execute('issue_comment',{ref:uid,body:'Independent contributor discovered a follow-up depending on this deployment.'});
  const dependent=await b.execute('issue_create',{title:'Follow-up staging verification',body:'Run after this deployment is complete.'});
  await b.execute('issue_link',{ref:uid,type:'blocks',to_ref:dependent.issue.uid});
  assert.equal(await a.guardTool('bash'),undefined);
  await exec('go',['test','./...'],{maxBuffer:8<<20}); // Real evidence, not a mocked command outcome.
  await eventually(async()=>{
   if(renewals===0)return false;
   const current=await a.execute('issue_get',{ref:uid});
   return Date.parse(current.lease.expires_at)>Date.parse(first.lease.expires_at)+10_000;
  },30_000);
  const body={ref:uid,reason:'done',message:'Implemented the requested behavior and successfully ran the real Go test suite.',evidence:[{type:'test',command:'go test ./...'}]};
  await assert.rejects(a.execute('issue_close',body),e=>e instanceof ForgeError && e.ambiguous);
  const closed=await a.execute('issue_get',{ref:uid});
  assert.equal(closed.issue.status,'closed');assert.equal(closed.lease??null,null);
  assert.equal(a.state().pendingClose,true);
  await f.restart(); // A separate forged process reopens the same DB and signing key.
  await f.admin(`/api/v1/projects/${f.projectID}/issues/${uid}/actions/reopen`,{});
  const replacement=await b.execute('issue_claim',{ref:uid});
  assert.notEqual(replacement.lease.claim_uid,first.lease.claim_uid);
  const replay=await a.execute('issue_close',body);
  assert.equal(replay.reused,true);assert.equal(replay.issue.status,'open');
  assert.equal(closes.length,2);
  assert.equal(closes[0].body===closes[1].body,true);
  for(const header of ['Idempotency-Key','X-Forge-Execution']) assert.equal(closes[0].headers.get(header)===closes[1].headers.get(header),true);
  const oldHeaders=new Headers(closes[0].headers);oldHeaders.set('Idempotency-Key',randomUUID());
  const stale=await fetch(f.url+'/forge/v1/tools/issue_close',{method:'POST',headers:oldHeaders,body:closes[0].body});
  assert.equal(stale.status,409);
  const current=await b.execute('issue_get',{ref:uid});
  assert.equal(current.issue.status,'open');assert.equal(current.lease.claim_uid,replacement.lease.claim_uid);
  const history=await f.admin(`/api/v1/projects/${f.projectID}/events?limit=1000`);
  assert.equal(history.events.filter((e:any)=>e.issue_uid===uid && e.type==='issue.closed').length,1);
  const timeline=await exec(resolve('bin/forged'),['timeline','--url',f.url,'--token-file',join(f.dataDir,'admin-token'),'--limit','2',uid]);
  for(const item of ['Human','Agent/','issue.commented','issue.linked','claim.acquired','issue.closed','claim.released','go test ./...']) assert.ok(timeline.stdout.includes(item),`timeline missing ${item}`);
  assert.equal(timeline.stdout.includes(f.workerToken)||timeline.stdout.includes(f.adminToken),false);
  // Host force-release is independent authority; old runtime fails its next edit preflight.
  await f.admin(`/api/v1/projects/${f.projectID}/issues/${uid}/lease/actions/force_release`,{reason:'Supervisor acceptance check'});
  assert.equal((await b.guardTool('write'))?.block,true);
 } finally {await a.shutdown('test');await b.shutdown('test');await f.close();}
});

test('SIGKILL of execution process: no shutdown cleanup, real TTL expires, another runtime takes a new tenure', {timeout:90_000}, async()=>{
 const f=await startForge();
 const session=randomUUID();
 const created=await f.admin(`/api/v1/projects/${f.projectID}/issues`,{title:'Crash and actual timed expiry',body:'Do not assume graceful release after SIGKILL.'});
 const uid=created.issue.uid;
 const b=new Controller({url:f.url,workerToken:f.workerToken,ttlSeconds:60},{sessionId:session});
 const child=fork(resolve('tests/crash-worker.ts'),[],{execArgv:['--import','tsx'],stdio:['ignore','ignore','ignore','ipc'],env:{...process.env,FORGE_URL:f.url,FORGE_WORKER_TOKEN_FILE:join(f.dataDir,'worker-token'),FORGE_TTL_SECONDS:'60',TEST_PI_SESSION:session,TEST_ISSUE_UID:uid}});
 try {
  const [message]=await once(child,'message',{signal:AbortSignal.timeout(15_000)});
  assert.equal(message.ok,true);
  const oldUID=message.claim.lease.claim_uid;
  const exited=once(child,'exit');child.kill('SIGKILL');await exited;
  await assert.rejects(b.execute('issue_claim',{ref:uid}),e=>e instanceof ForgeError && e.status===409);
  let acquired:any;
  await eventually(async()=>{
   try { acquired=await b.execute('issue_claim',{ref:uid});return true; }
   catch(error){if(error instanceof ForgeError && error.status===409)return false;throw error;}
  },75_000);
  assert.notEqual(acquired.lease.claim_uid,oldUID);
  assert.equal(await b.guardTool('edit'),undefined);
  const events=await f.admin(`/api/v1/projects/${f.projectID}/events?limit=1000`);
  assert.ok(events.events.some((event:any)=>event.issue_uid===uid && event.type==='claim.expired' && JSON.stringify(event.payload).includes(oldUID)));
 } finally {if(child.exitCode===null && child.signalCode===null)child.kill('SIGKILL');await b.shutdown('test');await f.close();}
});
