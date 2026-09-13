import {access,writeFile} from 'node:fs/promises';
import {constants} from 'node:fs';
import {delimiter,join,isAbsolute} from 'node:path';
import {fileURLToPath} from 'node:url';
const quote=(s:string)=>"'"+s.replaceAll("'","'\\''")+"'";

/**
 * A supervised adapter's runtime CLI. The launcher prepends a shim to PATH so
 * that inside a managed worktree `forge` resolves to this runtime's CLI, and
 * elsewhere it keeps the host's project routing. Adapters differ only in the
 * variable prefix they publish (`FORGE_CODEX_`, `FORGE_CLAUDE_`).
 *
 * This is convenience only: forged still checks worker credential, instance
 * capability and exact lease on every request.
 */
export interface WorkspaceShimNaming {
  /** Prefix for the published variables, e.g. `FORGE_CODEX_`. */
  prefix: string;
}

export const codexShim: WorkspaceShimNaming = { prefix: 'FORGE_CODEX_' };
export const claudeShim: WorkspaceShimNaming = { prefix: 'FORGE_CLAUDE_' };

export async function workspaceShim(dir:string,registration:any,env:NodeJS.ProcessEnv,naming:WorkspaceShimNaming=codexShim){
 const cliVariable=naming.prefix+'CLI', rootVariable=naming.prefix+'WORKSPACE_ROOT', baseVariable=naming.prefix+'BASE_CLI';
 const root=registration?.workspace_root;
 if(typeof root!=='string'||!isAbsolute(root))return;
 const entry=fileURLToPath(new URL('../scripts/forge.mjs',import.meta.url));
 let fallback=env[baseVariable];
 if(!fallback){for(const part of (env.PATH??'').split(delimiter)){if(!isAbsolute(part))continue;const candidate=join(part,'forge');try{await access(candidate,constants.X_OK);fallback=candidate;break;}catch{}}}
 const router=join(dir,'forge-route.mjs');
 // Location is record data, never inferred from Issue/tenure directory names.
 await writeFile(router,`
import {readdirSync,readFileSync,realpathSync,lstatSync} from 'node:fs';
import {join,isAbsolute,sep} from 'node:path';
import {spawnSync} from 'node:child_process';
const root=${JSON.stringify(root)}, project=${JSON.stringify(registration.project_uid)};
const cwd=realpathSync(process.cwd());
let managed=false;
try { for(const name of readdirSync(join(root,'records'))) {
 if(!name.endsWith('.json'))continue;
 try {
  const file=join(root,'records',name), stat=lstatSync(file);
  if(!stat.isFile()||stat.size>65536)continue;
  const r=JSON.parse(readFileSync(file,'utf8'));
  if(r.project_uid!==project||typeof r.worktree!=='string'||!isAbsolute(r.worktree))continue;
  const path=realpathSync(r.worktree);
  if(cwd===path||cwd.startsWith(path+sep)){managed=true;break;}
 } catch {}
}} catch {}
const fallback=${JSON.stringify(fallback??null)};
const child=managed||!fallback
 ? spawnSync(${JSON.stringify(process.execPath)},[${JSON.stringify(entry)},...process.argv.slice(2)],{stdio:'inherit'})
 : spawnSync(fallback,process.argv.slice(2),{stdio:'inherit'});
if(child.signal)process.kill(process.pid,child.signal);
else process.exitCode=child.status??1;
`,{mode:0o600,flag:'wx'});
 const script=`#!/bin/sh\nexec ${quote(process.execPath)} ${quote(router)} "$@"\n`;
 await writeFile(join(dir,'forge'),script,{mode:0o700,flag:'wx'});
 env[cliVariable]=join(dir,'forge');
 env.PATH=dir+delimiter+(env.PATH??'');env[rootVariable]=root;
 if(fallback)env[baseVariable]=fallback;
}
