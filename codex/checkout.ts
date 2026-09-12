import {parseArgs} from 'node:util';
import {resolve} from 'node:path';
import {setTimeout as delay} from 'node:timers/promises';
import {codexTool} from './facade.ts';
import {CodexError} from './transport.ts';
import {forgeErrorEnvelope} from '../pi-extension/errors.ts';
import {ForgeError} from '../pi-extension/errors.ts';
import {requestSession} from '../client/session.ts';
import {Controller} from '../pi-extension/controller.ts';
import {randomUUID} from 'node:crypto';
import type {ToolName} from '../pi-extension/schemas.ts';
export const CHECKOUT_HELP=`forge checkout ISSUE (--ref COMMIT | --dirty) [--source REPO] [--no-wait]
forge checkout ISSUE --recover WORKSPACE_ID (--dirty | --ref COMMIT)
forge checkout list [ISSUE]
forge checkout status WORKSPACE_ID
forge checkout archive WORKSPACE_ID

Requires a current Codex claim or CLI session claim (--socket PATH / FORGE_SOCKET).
Pi uses its checkout tool with the current Controller; never start a second broker.
No second claim or renewer. Source defaults to the current directory.
Use explicit tool working directories from the returned worktree path.
Close archives metadata and retains files. archive retries require a closed issue.
`;
export function parseCheckout(args:string[]){
 let parsed;try{parsed=parseArgs({args,allowPositionals:true,strict:true,options:{ref:{type:'string'},dirty:{type:'boolean'},source:{type:'string'},recover:{type:'string'},'no-wait':{type:'boolean'}}});}catch{throw new CodexError('checkout_usage');}
 const {positionals:p,values:v}=parsed;
 if(['list','status','archive'].includes(p[0])){
  if(Object.keys(v).length||p.length>2||(p[0]!=='list'&&p.length!==2))throw new CodexError('checkout_usage');
  return {op:'checkout_'+p[0],params:p[0]==='list'?(p[1]?{ref:p[1]}:{}):{workspace_id:p[1]},wait:false};
 }
 if(v.recover===''||v.source==='')throw new CodexError('checkout_usage');
 if(p.length!==1||Boolean(v.dirty)===Boolean(v.ref)||(v.source!==undefined&&v.recover!==undefined))throw new CodexError('checkout_usage');
 return {op:'checkout',params:{ref:p[0],...(v.recover?{recover:v.recover}:{source:resolve(v.source??'.')}),dirty:!!v.dirty,...(v.ref?{commit:v.ref}:{})},wait:!v['no-wait']};
}
export async function checkoutRPC(op:string,params:unknown,env:NodeJS.ProcessEnv=process.env):Promise<any>{
 if(env.FORGE_CODEX_INSTANCE_ID){
  if(env.FORGE_SOCKET)throw new CodexError('runtime_conflict');
  return codexTool(op,params,env);
 }
 if(!env.FORGE_SOCKET){
  if(op==='checkout')throw new CodexError('checkout_runtime_required',false,{message:'Checkout requires a claimed runtime',hint:'Use a live CLI session with --socket PATH, launch forge codex, or use the Pi checkout tool.'});
  const controller=await Controller.fromEnv(randomUUID(),env);
  try{return await controller.execute(op as ToolName,params)}finally{await controller.shutdown()}
 }
 return requestSession(env.FORGE_SOCKET,{op,params});
}
export async function runCheckout(args:string[],env:NodeJS.ProcessEnv=process.env,rpc:typeof codexTool=checkoutRPC,sleep:(ms:number)=>Promise<unknown>=delay){
 const routed=[...args];let socket:string|undefined;
 for(let i=0;i<routed.length;i++){
  const arg=routed[i];if(arg!=='--socket'&&!arg.startsWith('--socket='))continue;
  if(socket!==undefined)throw new CodexError('checkout_usage');
  socket=arg==='--socket'?routed[i+1]:arg.slice(9);
  if(!socket||socket.startsWith('--'))throw new CodexError('checkout_usage');
  routed.splice(i,arg==='--socket'?2:1);i--;
 }
 if(socket!==undefined){if(env.FORGE_SOCKET&&env.FORGE_SOCKET!==socket)throw new CodexError('runtime_conflict');env={...env,FORGE_SOCKET:socket};}
 args=routed;
 const command=parseCheckout(args);let result=await rpc(command.op,command.params,env);
 if(command.wait){
  const until=Date.now()+310_000;
  while(result.state==='preparing'&&Date.now()<until){await sleep(300);result=await rpc('checkout_status',{workspace_id:result.workspace_id},env);}
  if(result.state==='preparing')return {...result,wait_timed_out:true};
 }
 return result;
}
export async function checkoutCommand(args:string[]):Promise<number>{
 if(args.length===1&&args[0]==='--help'){process.stdout.write(CHECKOUT_HELP);return 0;}
 try{const r=await runCheckout(args);process.stdout.write(JSON.stringify(r)+'\n');return r.wait_timed_out?3:['failed','orphaned','archive_pending'].includes(r.state)?1:0;}
 catch(e){
  const error=e instanceof CodexError||e instanceof ForgeError?e:new CodexError('checkout_failed');
  const hint=error.hint??(error.code==='checkout_usage'?'Use forge checkout --help':'Inspect checkout status/list. For an ambiguous create use session retry with the same socket, codex retry, or the identical Pi checkout call; keep existing files.');
  process.stderr.write(JSON.stringify(forgeErrorEnvelope({code:error.code,message:error.message,status:error.status,ambiguous:error.ambiguous,hint,data:error.data}))+'\n');
  return error.code==='checkout_usage'?2:error.ambiguous?3:1;
 }
}
