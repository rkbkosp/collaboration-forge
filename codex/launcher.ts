import { randomBytes, randomUUID } from 'node:crypto';
import { mkdtemp, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawn } from 'node:child_process';
import { workspaceShim } from './workspace-shim.ts';
import { codexRPC } from './transport.ts';

type Exit={code:number|null;signal:NodeJS.Signals|null};
type Dependencies={register:(body:any,env:NodeJS.ProcessEnv)=>Promise<unknown>;end:(body:any,env:NodeJS.ProcessEnv)=>Promise<unknown>;run:(bin:string,args:string[],env:NodeJS.ProcessEnv)=>Promise<Exit>};
async function run(bin:string,args:string[],env:NodeJS.ProcessEnv):Promise<Exit>{
 const child=spawn(bin,args,{env,stdio:'inherit'});
 const forward=(signal:NodeJS.Signals)=>child.kill(signal);
 const interrupt=()=>forward('SIGINT');const terminate=()=>forward('SIGTERM');
 process.on('SIGINT',interrupt);process.on('SIGTERM',terminate);
 try{return await new Promise((resolve,reject)=>{child.once('error',()=>reject(new Error('codex_launch_failed')));child.once('exit',(code,signal)=>resolve({code,signal}));});}
 finally{process.removeListener('SIGINT',interrupt);process.removeListener('SIGTERM',terminate);}
}
/** The supervisor owns no execution or renewal loop. Its process-birth identity
 * fences the instance; normal child exit reports cleanup, crashes only retire it. */
export async function launchCodex(args:string[],env:NodeJS.ProcessEnv=process.env,deps:Dependencies={register:(b,e)=>codexRPC('register',b,e),end:(b,e)=>codexRPC('end',b,e,750),run}):Promise<number>{
 const dir=await mkdtemp(join(tmpdir(),'forge-codex-'));
 const instance=randomUUID();const file=join(dir,'instance-token');
 const childEnv:NodeJS.ProcessEnv={...env,FORGE_CODEX_INSTANCE_ID:instance,FORGE_CODEX_TOKEN_FILE:file};
 delete childEnv.FORGE_SOCKET;delete childEnv.FORGE_ADMIN_TOKEN_FILE;
 delete childEnv.CODEX_SESSION_ID;delete childEnv.CODEX_THREAD_ID;
 let registered=false;
 try{
  await writeFile(file,randomBytes(32).toString('hex'),{mode:0o600,flag:'wx'});
  const registration=await deps.register({instance_id:instance,pid:process.pid,...(env.FORGE_TTL_SECONDS?{ttl_seconds:Number(env.FORGE_TTL_SECONDS)}:{})},childEnv);registered=true;
  await workspaceShim(dir,registration,childEnv);
  const result=await deps.run(env.FORGE_CODEX_BINARY??'codex',args,childEnv);
  await deps.end({instance_id:instance,normal:result.code===0&&result.signal===null},childEnv).catch(()=>{});
  registered=false;return result.code??1;
 }finally{
  if(registered)await deps.end({instance_id:instance,normal:false},childEnv).catch(()=>{});
  await rm(dir,{recursive:true,force:true});
 }
}
