import { mkdir,writeFile,copyFile } from 'node:fs/promises';
import { constants } from 'node:fs';
import { resolve,join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { launchCodex } from './launcher.ts';
import { codexIdentity,codexTool } from './facade.ts';
import { codexRPC,CodexError } from './transport.ts';
import { hookConfiguration } from './bundle.ts';
const HELP=`forge codex [Codex arguments...]        launch a fresh supervised instance
forge codex status | retry | pause | unpause
forge codex hooks                       print hook configuration
forge codex install --target DIR        create DIR/hooks.json (no overwrite)
forge codex skill --target DIR          install Forge Codex skill (no overwrite)
forge codex --help-forge                 show this help

Set FORGE_URL and FORGE_WORKER_TOKEN_FILE before launching.
FORGE_CODEX_STOP_POLICY=strict (default) or advisory.
Install hooks in .codex, review/trust them in Codex, then launch with forge codex.
Use standard forge issue commands inside Codex; no background session broker.
`;
export async function codexCommand(args:string[],env:NodeJS.ProcessEnv=process.env):Promise<number>{
 try{
  const action=args[0];const output=(value:any)=>process.stdout.write(JSON.stringify(value)+'\n');
  if(action==='--help-forge'){process.stdout.write(HELP);return 0;}
  if(action==='hooks'){if(args.length!==1)throw new CodexError('usage');output(hookConfiguration());return 0;}
  if(action==='install'||action==='skill'){
   if(args.length!==3||args[1]!=='--target'||!args[2])throw new CodexError('usage');
   const dir=resolve(args[2]);await mkdir(dir,{recursive:true});
   if(action==='install')await writeFile(join(dir,'hooks.json'),JSON.stringify(hookConfiguration(),null,2)+'\n',{flag:'wx',mode:0o600});
   else await copyFile(fileURLToPath(new URL('./skills/forge-codex/SKILL.md',import.meta.url)),join(dir,'SKILL.md'),constants.COPYFILE_EXCL);
   output({installed:dir,requires_hook_trust:action==='install'});return 0;
  }
  if(['status','retry','pause','unpause'].includes(action)){
   if(args.length!==1)throw new CodexError('usage');
   const result=action==='pause'||action==='unpause'?await codexRPC('event',{identity:codexIdentity(env),event:action==='pause'?'pause':'prompt'},env):await codexTool(action,{},env);
   output(result);return 0;
  }
  return await launchCodex(args,env);
 }catch(e){const code=e instanceof CodexError?e.code:'codex_command_failed';const ambiguous=e instanceof CodexError&&e.ambiguous;process.stderr.write(JSON.stringify({error:{code,ambiguous}})+'\n');return ambiguous?3:code==='usage'?2:1;}
}
