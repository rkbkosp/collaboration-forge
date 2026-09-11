import {spawnSync} from 'node:child_process';
import {readFile} from 'node:fs/promises';
import {join} from 'node:path';
import {randomUUID} from 'node:crypto';
import {handleHook} from '../../codex/hooks.ts';
process.env.CODEX_SESSION_ID=randomUUID();process.env.CODEX_THREAD_ID=process.env.CODEX_SESSION_ID;
const [issue,source,second]=process.argv.slice(2);
function cli(args:string[],cwd=source){const p=spawnSync('forge',args,{cwd,env:process.env,encoding:'utf8',timeout:60_000});if(p.status!==0)throw new Error('fixture CLI failed: '+args[0]+' '+p.status);return JSON.parse(p.stdout);}
await handleHook({hook_event_name:'SessionStart',session_id:process.env.CODEX_SESSION_ID,source:'startup'});
if(!source){throw new Error('missing fixture source');}
cli(['issue','claim',issue]);
const a=cli(['checkout',issue,'--dirty','--source',source]);
const b=cli(['checkout',issue,'--ref','HEAD','--source',second],a.worktree);
const state=cli(['codex','status'],a.worktree);if(!state.active||state.workspaces.length!==2)throw new Error('workspace authority missing');
if((await readFile(join(a.worktree,'tracked.txt'),'utf8'))!=='dirty\n')throw new Error('dirty snapshot mismatch');
const close={reason:'audit-no-change',message:'Generated real-process checkout fixture verified dirty/commit isolation and archive retention; no product code change is required.',evidence:[{type:'no-change-audit',rationale:'Only temporary Git repositories were used to verify CLI, daemon, Kata lease, and workspace lifecycle.'}]};
const closed=cli(['issue','close',issue,'--data',JSON.stringify(close)],b.worktree);
if(closed.workspace_archive?.state!=='archived')throw new Error('archive missing from close');
const archived=cli(['checkout','status',a.workspace_id],a.worktree);if(archived.state!=='archived')throw new Error('archive status missing');
await handleHook({hook_event_name:'SessionEnd',session_id:process.env.CODEX_SESSION_ID});
process.stdout.write(JSON.stringify({workspaces:[a,b],closed:true,archived:true})+'\n');
