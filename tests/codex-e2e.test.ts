import test from 'node:test';
import assert from 'node:assert/strict';
import {spawn,type ChildProcess} from 'node:child_process';
import {once} from 'node:events';
import {fileURLToPath} from 'node:url';
import {join} from 'node:path';
import {setTimeout as delay} from 'node:timers/promises';
import {startForge} from './harness.ts';
import {codexTool} from '../codex/facade.ts';
const loader=fileURLToPath(new URL('../node_modules/tsx/dist/loader.mjs',import.meta.url));
const launcher=fileURLToPath(new URL('../scripts/forge.mjs',import.meta.url));
const worker=fileURLToPath(new URL('./codex-fixtures/worker.ts',import.meta.url));
async function fixture(){
 const f=await startForge();const children:ChildProcess[]=[];const workerPIDs:number[]=[];
 const env={...process.env,FORGE_URL:f.url,FORGE_WORKER_TOKEN_FILE:join(f.dataDir,'worker-token'),FORGE_TTL_SECONDS:'60',FORGE_CODEX_BINARY:process.execPath,FORGE_SOCKET:'',FORGE_ADMIN_TOKEN_FILE:''};
 async function issue(){return (await f.admin(`/api/v1/projects/${f.projectID}/issues`,{title:'Codex lifecycle process fixture'})).issue;}
 async function start(ref:string){
  const child=spawn(process.execPath,[launcher,'codex','--import',loader,worker,ref],{env,stdio:['pipe','pipe','pipe']});children.push(child);
  let buffer='';const lines:any[]=[];const listeners:Array<(x:any)=>void>=[];
  child.stdout!.on('data',b=>{buffer+=b;for(;;){const at=buffer.indexOf('\n');if(at<0)break;const raw=buffer.slice(0,at);buffer=buffer.slice(at+1);assert.equal(raw.includes(f.workerToken)||raw.includes(f.adminToken),false);const x=JSON.parse(raw);if(listeners.length)listeners.shift()!(x);else lines.push(x);}});
  const next=()=>{if(lines.length)return Promise.resolve(lines.shift());return new Promise<any>((resolve,reject)=>{const timer=setTimeout(()=>reject(new Error('fixture output timeout')),12000);listeners.push(x=>{clearTimeout(timer);resolve(x);});});};
  const ready=await next();if(ready.pid)workerPIDs.push(ready.pid);return {child,ready,next};
 }
 async function close(){for(const pid of workerPIDs){try{process.kill(pid,'SIGKILL');}catch{}}for(const c of children){if(c.exitCode===null&&c.signalCode===null)c.kill('SIGKILL');}await f.close();}
 return {...f,env,issue,start,cleanup:close};
}
test('real forged: launcher claims/renews without CLI polling, closes, and fresh instance cannot inherit',{timeout:80_000},async(t)=>{
 const f=await fixture();t.after(()=>f.cleanup());try{
  const issue=await f.issue();const a=await f.start(issue.uid);assert.equal(a.ready.ready,true);
  const get=()=>f.admin(`/api/v1/projects/${f.projectID}/issues/${issue.uid}`);
  const first=await get();await delay(23_000);const renewed=await get();assert.ok(Date.parse(renewed.lease.expires_at)>Date.parse(first.lease.expires_at));
  const b=await f.start(issue.uid);assert.equal(b.ready.conflict,true);
  a.child.stdin!.write('close\n');assert.equal((await a.next()).closed,true);
  const current=await get();assert.equal(current.issue.status,'closed');assert.equal(current.lease==null,true);
  const done=once(a.child,'exit');a.child.stdin!.write('exit\n');await done;
 }finally{await f.cleanup();}
});
test('real forged: launcher SIGKILL stops renew; TTL permits fresh instance and daemon restart rejects old capability',{timeout:100_000},async(t)=>{
 const f=await fixture();t.after(()=>f.cleanup());let workerPID:number|undefined;
 try{
  const issue=await f.issue();const a=await f.start(issue.uid);workerPID=a.ready.pid;
  const oldEnv={...f.env,FORGE_CODEX_INSTANCE_ID:a.ready.instance,FORGE_CODEX_TOKEN_FILE:a.ready.tokenFile,CODEX_SESSION_ID:'11111111-1111-4111-8111-111111111111',CODEX_THREAD_ID:'11111111-1111-4111-8111-111111111111'};
  const exited=once(a.child,'exit');a.child.kill('SIGKILL');await exited;await delay(2500);
  await assert.rejects(codexTool('issue_release',{ref:issue.uid},oldEnv));
  const get=()=>f.admin(`/api/v1/projects/${f.projectID}/issues/${issue.uid}`);const stopped=await get();
  await delay(22_000);const later=await get();assert.equal(later.lease.expires_at,stopped.lease.expires_at);
  const wait=Math.max(0,Date.parse(stopped.lease.expires_at)-Date.now()+1200);await delay(wait);
  const b=await f.start(issue.uid);assert.equal(b.ready.ready,true);assert.notEqual(b.ready.instance,a.ready.instance);
  await f.restart();await assert.rejects(codexTool('status',{}, {...oldEnv,FORGE_CODEX_INSTANCE_ID:b.ready.instance,FORGE_CODEX_TOKEN_FILE:b.ready.tokenFile}));
 }finally{if(workerPID)try{process.kill(workerPID,'SIGKILL');}catch{}await f.cleanup();}
});
