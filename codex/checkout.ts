import {parseArgs} from 'node:util';
import {resolve} from 'node:path';
import {setTimeout as delay} from 'node:timers/promises';
import {codexTool} from './facade.ts';
import {CodexError} from './transport.ts';
export const CHECKOUT_HELP=`forge checkout ISSUE (--ref COMMIT | --dirty) [--source REPO] [--no-wait]
forge checkout ISSUE --recover WORKSPACE_ID (--dirty | --ref COMMIT)
forge checkout list [ISSUE]
forge checkout status WORKSPACE_ID
forge checkout archive WORKSPACE_ID

Requires the current Codex thread to hold ISSUE. No second claim or renewer.
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
export async function runCheckout(args:string[],env:NodeJS.ProcessEnv=process.env,rpc:typeof codexTool=codexTool,sleep:(ms:number)=>Promise<unknown>=delay){
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
 catch(e){const code=e instanceof CodexError?e.code:'checkout_failed';process.stderr.write(JSON.stringify({error:{code,ambiguous:e instanceof CodexError&&e.ambiguous,hint:code==='checkout_usage'?'Use forge checkout --help': 'Inspect forge checkout list and forge codex status before retrying; keep existing workspace files.'}})+'\n');return code==='checkout_usage'?2:e instanceof CodexError&&e.ambiguous?3:1;}
}
