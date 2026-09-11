import test from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { execFile, spawn } from 'node:child_process';
import { promisify } from 'node:util';
import { once } from 'node:events';
import { join } from 'node:path';
import { startForge } from './harness.ts';
const exec=promisify(execFile);

test('CLI retries committed HTTP response loss across daemon restart without affecting replacement tenure', {timeout:60_000}, async()=>{
 const f=await startForge();let dropped=false;
 const closes:Array<{body:string;proof:string|undefined;key:string|undefined}>=[];
 const proxy=createServer(async(req,res)=>{
  try {
   const chunks:Buffer[]=[];for await(const part of req)chunks.push(Buffer.from(part));const body=Buffer.concat(chunks);
   const response=await fetch(f.url+req.url,{method:req.method,headers:req.headers as Record<string,string>,body:body.length?body:undefined,redirect:'error'});
   const result=Buffer.from(await response.arrayBuffer());
   if(req.url?.endsWith('/issue_close')){
    closes.push({body:body.toString(),proof:req.headers['x-forge-execution'] as string,key:req.headers['idempotency-key'] as string});
    if(response.ok&&!dropped){dropped=true;res.destroy();return;}
   }
   res.writeHead(response.status,{'Content-Type':'application/json'});res.end(result);
  }catch{res.destroy();}
 });
 proxy.listen(0,'127.0.0.1');await once(proxy,'listening');
 const address=proxy.address();assert.ok(address&&typeof address==='object');
 const env={...process.env,FORGE_URL:`http://127.0.0.1:${address.port}`,FORGE_WORKER_TOKEN_FILE:join(f.dataDir,'worker-token'),FORGE_ADMIN_TOKEN_FILE:'',FORGE_SOCKET:''};
 const children:Array<ReturnType<typeof spawn>>=[];
 async function cli(args:string[],socket:string,expected=0){
  let stdout='',stderr='',code=0;
  try{({stdout,stderr}=await exec(process.execPath,['scripts/forge.mjs',...args],{env:{...env,FORGE_SOCKET:socket},timeout:25_000}));}
  catch(e:any){stdout=e.stdout;stderr=e.stderr;code=e.code;}
  assert.equal((stdout+stderr).includes(f.workerToken),false);
  assert.equal(code,expected,stderr);return JSON.parse(stdout||stderr);
 }
 async function start(name:string){const socket=join(f.dataDir,name+'.sock');children.push(spawn(process.execPath,['scripts/forge.mjs','session','start','--socket',socket],{env,stdio:'ignore'}));await cli(['session','wait'],socket);return socket;}
 try {
  const a=await start('retry-a');const b=await start('retry-b');
  const issue=(await cli(['issue','create','--title','CLI retry fixture'],a)).issue;
  const ref=issue.uid;
  const first=await cli(['issue','claim',ref],a);
  const body={reason:'audit-no-change',message:'This generated fixture only verifies CLI receipt recovery and needs no additional product implementation.',evidence:[{type:'no-change-audit',rationale:'This is a generated integration fixture for close retry behavior, not a real product implementation request.'}]};
  await cli(['issue','close',ref,'--data',JSON.stringify(body)],a,3);
  assert.equal((await cli(['session','status'],a)).pendingClose,true);
  assert.equal((await cli(['issue','get',ref],a)).issue.status,'closed');
  await f.restart();
  await f.admin(`/api/v1/projects/${f.projectID}/issues/${ref}/actions/reopen`,{});
  const next=await cli(['issue','claim',ref],b);
  assert.notEqual(next.lease.claim_uid,first.lease.claim_uid);
  const recovered=await cli(['session','retry'],a);
  assert.equal(recovered.reused,true);assert.equal(recovered.issue.status,'open');
  assert.equal(closes.length,2);
  assert.equal(closes[0].body===closes[1].body,true);
  assert.equal(closes[0].proof===closes[1].proof,true);
  assert.equal(closes[0].key===closes[1].key,true);
  assert.equal((await cli(['issue','get',ref],b)).lease.claim_uid,next.lease.claim_uid);
  const timeline=await cli(['issue','timeline',ref,'--all','--limit','1'],a);
  assert.equal(timeline.events.filter((event:any)=>event.type==='issue.closed').length,1);
  await cli(['session','stop'],a);await cli(['session','stop'],b);
 } finally {
  await Promise.all(children.map(async child=>{if(child.exitCode!==null||child.signalCode!==null)return;const done=once(child,'close');child.kill('SIGTERM');const timer=setTimeout(()=>child.kill('SIGKILL'),5000);try{await done;}finally{clearTimeout(timer);}}));
  proxy.closeAllConnections();await new Promise<void>(resolve=>proxy.close(()=>resolve()));await f.close();
 }
});
