import test from 'node:test';
import assert from 'node:assert/strict';
import { execFile, spawn } from 'node:child_process';
import { promisify } from 'node:util';
import { once } from 'node:events';
import { join } from 'node:path';
import { startForge } from './harness.ts';
const exec=promisify(execFile);

test('independent executable covers worker sessions, timeline and explicit Human management', {timeout:90_000}, async()=>{
 const f=await startForge();
 const env={...process.env,FORGE_URL:f.url,FORGE_WORKER_TOKEN_FILE:join(f.dataDir,'worker-token'),FORGE_ADMIN_TOKEN_FILE:'',FORGE_SOCKET:''};
 const processes:Array<ReturnType<typeof spawn>>=[];
 async function cli(args:string[],socket='',admin=false,expected=0){
  let stdout='',stderr='',code=0;
  const command=['scripts/forge.mjs',...args,...(admin?['--admin-token-file',join(f.dataDir,'admin-token')]:[])];
  try{({stdout,stderr}=await exec(process.execPath,command,{env:{...env,FORGE_SOCKET:socket},maxBuffer:16<<20,timeout:25_000}));}
  catch(error:any){stdout=error.stdout;stderr=error.stderr;code=error.code;}
  assert.equal((stdout+stderr).includes(f.adminToken)||(stdout+stderr).includes(f.workerToken),false);
  assert.equal(code,expected,stderr);
  return JSON.parse(stdout||stderr);
 }
 async function broker(name:string){
  const socket=join(f.dataDir,name+'.sock');
  const child=spawn(process.execPath,['scripts/forge.mjs','session','start','--socket',socket,'--ttl','60'],{env,stdio:['ignore','ignore','ignore']});
  processes.push(child);await cli(['session','wait'],socket);return socket;
 }
 try {
  assert.equal((await cli(['health'])).status,'ok');
  assert.equal((await cli(['project'])).project.id,f.projectID);
  const issue=(await cli(['admin','create','--title','Standalone client acceptance','--body','Generated fixture for client command coverage'], '',true)).issue;
  const ref=issue.uid;
  await cli(['admin','update',ref,'--owner','alice','--priority','1'],'',true);
  assert.equal((await cli(['issue','get',ref])).issue.owner,'alice');
  await cli(['admin','label','add',ref,'urgent'],'',true);
  await cli(['admin','label','remove',ref,'urgent'],'',true);
  const a=await broker('a');const b=await broker('b');
  const socketHealth=await exec(process.execPath,['scripts/forge.mjs','health'],{env:{...env,FORGE_URL:'http://127.0.0.1:1',FORGE_WORKER_TOKEN_FILE:'',FORGE_SOCKET:a}});
  assert.equal(JSON.parse(socketHealth.stdout).status,'ok');
  await cli(['issue','get',ref,'--url','http://127.0.0.1:1'],a,false,2);
  await cli(['session','guard'],a,false,4);
  const first=await cli(['issue','claim',ref,'--purpose','real CLI acceptance'],a);
  assert.equal('execution_token' in first,false);
  await cli(['issue','claim',ref],b,false,1);
  await cli(['issue','comment',ref,'--body','Independent nonholder contribution'],b);
  const followup=(await cli(['issue','create','--title','Follow-up from B'],b)).issue;
  const link=await cli(['issue','link',ref,'--type','blocks','--to-ref',followup.uid],b);
  await cli(['admin','unlink',ref,String(link.link.id)],'',true);
  await cli(['admin','link',ref,'--type','related','--to-ref',followup.uid],'',true);
  await cli(['issue','graph',ref,'--depth','2'],a);
  await cli(['issue','list','--status','open'],a);
  await cli(['session','context'],a);
  assert.equal((await cli(['session','guard'],a)).allowed,true);
  await cli(['issue','renew',ref],a);
  await cli(['admin','force-release',ref,'--reason','Authorized acceptance check'],'',true);
  await cli(['session','guard'],a,false,4);
  const second=await cli(['issue','claim',ref],a);
  assert.notEqual(second.lease.claim_uid,first.lease.claim_uid);
  await cli(['issue','release',ref,'--reason','Explicit release coverage'],a);
  await cli(['issue','claim',ref],a);
  const close={reason:'audit-no-change',message:'This generated fixture exercises the standalone client; no additional product modification is required.',evidence:[{type:'no-change-audit',rationale:'The issue is an ephemeral automated test fixture for the standalone client, not a requested product change.'}]};
  assert.equal((await cli(['issue','close',ref,'--data',JSON.stringify(close)],a)).issue.status,'closed');
  const recovered=await cli(['session','retry'],a);
  assert.equal(recovered.client_replayed,true);assert.equal(recovered.current_state_not_refreshed,true);
  const page=await cli(['issue','timeline',ref,'--limit','1','--all'],b);
  assert.equal(page.truncated,false);assert.ok(page.events.length>5);
  assert.equal(page.events.filter((event:any)=>event.type==='issue.closed').length,1);
  const partial=await cli(['issue','timeline',ref,'--limit','1'],b);
  assert.equal(partial.truncated,true);
  await cli(['admin','reopen',ref],'',true);
  assert.equal((await cli(['session','retry'],a)).current_state_not_refreshed,true);
  assert.equal((await cli(['issue','get',ref],a)).issue.status,'open');
  await cli(['admin','close',ref,'--data',JSON.stringify(close)],'',true);
  await cli(['admin','update',ref,'--clear-owner','--priority','none'],'',true);
  await cli(['admin','comment',ref,'--body','Human verified the result'],'',true);
  await cli(['admin','request','GET',`/api/v1/projects/${f.projectID}`],'',true);
  await cli(['admin','request','POST',`/api/v1/projects/${f.projectID}/issues/${ref}/actions/reopen`,'--data','{}'],'',true);
  assert.equal((await cli(['issue','get',ref],a)).issue.status,'open');
  await cli(['admin','delete',followup.uid,'--confirm',`DELETE e2e#${followup.short_id}`],'',true);
  await cli(['admin','reopen',ref,'--admin-token-file',join(f.dataDir,'worker-token')],'',false,1);
  await cli(['tool','issue_force_release','--data',JSON.stringify({ref})],b,false,1);
  await cli(['session','stop'],a);await cli(['session','stop'],b);
 } finally {
  await Promise.all(processes.map(async child=>{
   if(child.exitCode!==null||child.signalCode!==null)return;
   const done=once(child,'close');child.kill('SIGTERM');const timer=setTimeout(()=>child.kill('SIGKILL'),5000);
   try{await done;}finally{clearTimeout(timer);}
  }));await f.close();
 }
});
