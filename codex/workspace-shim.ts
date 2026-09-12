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
 env.FORGE_CODEX_CLI=join(dir,'forge');
 env.PATH=dir+delimiter+(env.PATH??'');env.FORGE_CODEX_WORKSPACE_ROOT=root;
 if(fallback)env.FORGE_CODEX_BASE_CLI=fallback;
}
