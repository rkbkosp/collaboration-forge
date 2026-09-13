import { codexRPC, CodexError } from './transport.ts';
import { stopDecision } from '../client/runtime-stop.ts';
import { codexIdentity } from './facade.ts';
export const PROTOCOL='Forge is this workspace\'s work ledger. Before substantial work, inspect and claim the relevant issue with forge issue. Record discoveries as comments/issues/links. Close with truthful typed evidence, or release when handing off. After claiming, work in the original directory or use forge checkout ISSUE --dirty / --ref COMMIT for isolation (one per repository). Use the returned worktree as the explicit cwd for shell/edit/test tools. In isolated directories use "$FORGE_CODEX_CLI" if a shell profile overrides forge. Completed checkouts are archived without deletion. Use forge --help. Runtime authority and renewal are managed by forged; never start a legacy session broker inside Codex.';
export async function handleHook(input:any,env:NodeJS.ProcessEnv=process.env,rpc:typeof codexRPC=codexRPC):Promise<any>{
 if(!input||typeof input!=='object'||Array.isArray(input)||typeof input.session_id!=='string')throw new CodexError('invalid_hook');
 if(env.CODEX_SESSION_ID&&env.CODEX_SESSION_ID!==input.session_id)throw new CodexError('hook_identity_mismatch');
 // v0.154.0 supplies agent_id on child tool/prompt/compact hooks too. Hook
 // commands use an environment snapshot, so do not inherit a parent's thread.
 const identity=codexIdentity({...env,CODEX_SESSION_ID:input.session_id,CODEX_THREAD_ID:input.agent_id??input.session_id});
 const name=input.hook_event_name;
 const events:Record<string,string>={SessionStart:input.source==='compact'?'context':'start',SubagentStart:'start',PostCompact:'context',PreCompact:'touch',PreToolUse:'touch',PostToolUse:'touch',UserPromptSubmit:'prompt',SessionEnd:'session_end',Interrupt:'interrupt',Stop:'stop_check',SubagentStop:'stop_check'};
 if(!Object.hasOwn(events,name))throw new CodexError('unsupported_hook');
 const state=await rpc('event',{identity,event:events[name]},env,name==='SessionEnd'||name==='Interrupt'?650:2500);
 // PostCompact only supports common output fields; its state refresh above
 // remains useful, but it cannot inject additionalContext.
 if(name==='SessionStart'||name==='SubagentStart')return {hookSpecificOutput:{hookEventName:name,additionalContext:PROTOCOL+'\n'+JSON.stringify(state)}};
 if(name==='Stop'||name==='SubagentStop'){
  const policy=env.FORGE_CODEX_STOP_POLICY??'strict';
  if(policy!=='strict'&&policy!=='advisory')throw new CodexError('invalid_stop_policy');
  const result=stopDecision(state,input,policy,'forge codex');
  if(result.systemMessage)await rpc('event',{identity,event:'pause'},env,650);
  return result;
 }
 return {};
}
