import {access,writeFile} from 'node:fs/promises';
import {constants} from 'node:fs';
import {delimiter,join,isAbsolute} from 'node:path';
import {fileURLToPath} from 'node:url';
const quote=(s:string)=>"'"+s.replaceAll("'","'\\''")+"'";
/** A server-provided project artifact root is outside normal repository routing.
 * Keep that runtime's CLI in managed worktrees; preserve host routing elsewhere.
 * This is convenience only: forged still checks worker/instance/exact lease. */
export async function workspaceShim(dir:string,registration:any,env:NodeJS.ProcessEnv){
 const root=registration?.workspace_root;
 if(typeof root!=='string'||!isAbsolute(root))return;
 const entry=fileURLToPath(new URL('../scripts/forge.mjs',import.meta.url));
 let fallback=env.FORGE_CODEX_BASE_CLI;
 if(!fallback){for(const part of (env.PATH??'').split(delimiter)){if(!isAbsolute(part))continue;const candidate=join(part,'forge');try{await access(candidate,constants.X_OK);fallback=candidate;break;}catch{}}}
 const direct=`exec ${quote(process.execPath)} ${quote(entry)} "$@"`;
 const script=`#!/bin/sh\nforge_cwd=$(pwd -P) || exit 2\ncase "$forge_cwd" in\n  ${quote(root)}/trees/*/worktree|${quote(root)}/trees/*/worktree/*) ${direct} ;;\n  *) ${fallback?`exec ${quote(fallback)} "$@"`:direct} ;;\nesac\n`;
 await writeFile(join(dir,'forge'),script,{mode:0o700,flag:'wx'});
 env.FORGE_CODEX_CLI=join(dir,'forge');
 env.PATH=dir+delimiter+(env.PATH??'');env.FORGE_CODEX_WORKSPACE_ROOT=root;
 if(fallback)env.FORGE_CODEX_BASE_CLI=fallback;
}
