import { directFetch } from '../extensions/direct-fetch.ts';
import { constants } from 'node:fs';
import { open } from 'node:fs/promises';
import { loadConfig } from '../extensions/config.ts';

export class CodexError extends Error {
 constructor(public code:string, public ambiguous=false){super(code);}
}
export async function instanceToken(file:string):Promise<string>{
 let f;
 try{
  f=await open(file,constants.O_RDONLY|constants.O_NOFOLLOW|constants.O_NONBLOCK);
  const s=await f.stat();if(!s.isFile()||s.uid!==process.getuid?.()||(s.mode&0o777)!==0o600||s.size!==64)throw new Error();
  const token=await f.readFile('utf8');if(!/^[a-f0-9]{64}$/.test(token))throw new Error();return token;
 }catch{throw new CodexError('codex_instance_credential');}finally{await f?.close();}
}
export async function codexRPC(op:string,body:unknown,env:NodeJS.ProcessEnv=process.env,timeout=10_000):Promise<any>{
 const config=await loadConfig(env).catch(()=>{throw new CodexError('codex_configuration');});
 const token=await instanceToken(env.FORGE_CODEX_TOKEN_FILE??'');
 try{
  const response=await directFetch(config.url+'/forge/v1/codex/'+op,{method:'POST',redirect:'error',headers:{Authorization:'Bearer '+config.workerToken,'X-Forge-Codex-Token':token,'Content-Type':'application/json'},body:JSON.stringify(body),signal:AbortSignal.timeout(timeout)});
  const reader=response.body?.getReader();let size=0;const chunks:Uint8Array[]=[];
  if(reader)for(;;){const x=await reader.read();if(x.done)break;size+=x.value.length;if(size>8<<20){await reader.cancel();throw new Error();}chunks.push(x.value);}
  const result=JSON.parse(Buffer.concat(chunks).toString('utf8'));
  if(!response.ok)throw new CodexError(/^[a-z_]+$/.test(result.error?.code)?result.error.code:'codex_request_failed',response.status>=500||result.error?.ambiguous===true);
  return result;
 }catch(e){if(e instanceof CodexError)throw e;throw new CodexError('codex_transport_unknown',true);}
}
