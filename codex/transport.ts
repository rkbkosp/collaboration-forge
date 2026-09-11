import { directFetch } from '../pi-extension/direct-fetch.ts';
import { constants } from 'node:fs';
import { open } from 'node:fs/promises';
import { loadConfig } from '../pi-extension/config.ts';
import { redactErrorValue, type ErrorData } from '../pi-extension/errors.ts';

export interface CodexErrorOptions {
 status?: number;
 message?: string;
 hint?: string;
 data?: ErrorData;
}

export class CodexError extends Error {
 readonly status: number;
 readonly hint?: string;
 readonly data?: ErrorData;
 constructor(public code: string, public ambiguous = false, options: CodexErrorOptions = {}) {
  super(options.message ?? 'Codex operation failed');
  this.name = 'CodexError';
  this.status = options.status ?? 0;
  this.hint = options.hint;
  this.data = options.data;
 }
}
export async function instanceToken(file:string):Promise<string>{
 let f;
 try{
  f=await open(file,constants.O_RDONLY|constants.O_NOFOLLOW|constants.O_NONBLOCK);
  const s=await f.stat();if(!s.isFile()||s.uid!==process.getuid?.()||(s.mode&0o777)!==0o600||s.size!==64)throw new Error();
  const token=await f.readFile('utf8');if(!/^[a-f0-9]{64}$/.test(token))throw new Error();return token;
 }catch{throw new CodexError('codex_instance_credential');}finally{await f?.close();}
}
function code(value: unknown): string {
 return typeof value === 'string' && /^[a-z][a-z0-9_]{0,99}$/.test(value) ? value : 'codex_request_failed';
}
function object(value: unknown): Record<string, any> {
 return value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, any> : {};
}
const mutationOperations=new Set(['issue_claim','issue_renew','issue_release','issue_close','issue_create','issue_comment','issue_link','checkout','retry']);
function mutationRequest(op:string,body:unknown): boolean {
 if(op==='tool'||op==='command')return mutationOperations.has(String(object(body).operation??''));
 return op==='register'||op==='event';
}
export async function codexRPC(op:string,body:unknown,env:NodeJS.ProcessEnv=process.env,timeout=10_000):Promise<any>{
  const mutation=mutationRequest(op,body);
  const uncertain=(status:number)=>mutation && (status===0 || status===408 || status===429 || status>=500);
 const config=await loadConfig(env).catch(()=>{throw new CodexError('codex_configuration');});
 const token=await instanceToken(env.FORGE_CODEX_TOKEN_FILE??'');
 let status = 0;
 try{
  const response=await directFetch(config.url+'/forge/v1/codex/'+op,{method:'POST',redirect:'error',headers:{Authorization:'Bearer '+config.workerToken,'X-Forge-Codex-Token':token,'Content-Type':'application/json'},body:JSON.stringify(body),signal:AbortSignal.timeout(timeout)});
  status = response.status;
  const reader=response.body?.getReader();let size=0;const chunks:Uint8Array[]=[];
  if(reader)for(;;){const x=await reader.read();if(x.done)break;size+=x.value.length;if(size>8<<20){await reader.cancel();throw new Error();}chunks.push(x.value);}
  let result: any;
  try { result=JSON.parse(Buffer.concat(chunks).toString('utf8')); }
  catch {
   throw new CodexError(response.ok ? 'codex_transport_unknown' : 'codex_request_failed', uncertain(response.status),
    { status: response.status, message: response.ok ? 'Codex returned an unreadable response' : `Codex request failed with HTTP ${response.status}` });
  }
  if(!response.ok){
   const error=object(result.error);
   const secrets=[config.workerToken,token];
   const message=typeof error.message==='string'&&error.message ? String(redactErrorValue(error.message,secrets)) : `Codex request failed with HTTP ${response.status}`;
   const hint=typeof error.hint==='string'&&error.hint ? String(redactErrorValue(error.hint,secrets)) : undefined;
   const data=error.data&&typeof error.data==='object'&&!Array.isArray(error.data) ? redactErrorValue(error.data,secrets) as ErrorData : undefined;
   throw new CodexError(code(error.code), uncertain(response.status) || error.ambiguous === true,
    { status: response.status, message, hint, data });
  }
  return result;
 }catch(e){
  if(e instanceof CodexError)throw e;
  throw new CodexError('codex_transport_unknown', uncertain(status),
   { status, message: 'Codex request failed or response was invalid; inspect server state before repeating a mutation' });
 }
}
