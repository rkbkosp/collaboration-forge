import { randomUUID } from 'node:crypto';
import { codexRPC, CodexError } from './transport.ts';
export type Identity={instance_id:string;session_id:string;thread_id:string};
const valid=(s:unknown):s is string=>typeof s==='string'&&s.length>0&&s.length<=128&&!/[\s\0]/.test(s);
export function codexIdentity(env:NodeJS.ProcessEnv=process.env):Identity{
 const {FORGE_CODEX_INSTANCE_ID:instance_id,CODEX_SESSION_ID:session_id,CODEX_THREAD_ID:thread_id}=env;
 if(!valid(instance_id)||!valid(session_id)||!valid(thread_id))throw new CodexError('codex_identity_required');
 return {instance_id,session_id,thread_id};
}
export async function codexTool(operation:string,params:unknown={},env:NodeJS.ProcessEnv=process.env,rpc:typeof codexRPC=codexRPC):Promise<any>{
 const body={identity:codexIdentity(env),operation,params:structuredClone(params),request_id:randomUUID()};
 for(let n=0;;n++){
  try{return await rpc('tool',body,env);}
  catch(e){if(!(e instanceof CodexError)||!e.ambiguous||n>=2)throw e;}
 }
}
