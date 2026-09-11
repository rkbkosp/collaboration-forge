import {execFile} from 'node:child_process';
import {promisify} from 'node:util';
import {fileURLToPath} from 'node:url';
import {createInterface} from 'node:readline';
const exec=promisify(execFile);
const forge=fileURLToPath(new URL('../../scripts/forge.mjs',import.meta.url));
async function forgeCLI(args:string[]){const {stdout}=await exec(process.execPath,[forge,...args],{env:process.env,timeout:15000});return JSON.parse(stdout);}
import {handleHook} from '../../codex/hooks.ts';
process.env.CODEX_SESSION_ID='11111111-1111-4111-8111-111111111111';
process.env.CODEX_THREAD_ID=process.env.CODEX_SESSION_ID;
const ref=process.argv[2];
const emit=(x:any)=>process.stdout.write(JSON.stringify(x)+'\n');
await handleHook({hook_event_name:'SessionStart',session_id:process.env.CODEX_SESSION_ID,source:'startup'});
try{
 await forgeCLI(['issue','claim',ref]);
 emit({ready:true,instance:process.env.FORGE_CODEX_INSTANCE_ID,tokenFile:process.env.FORGE_CODEX_TOKEN_FILE,pid:process.pid});
}catch(e){emit({conflict:true});process.exit(2);}
const lines=createInterface({input:process.stdin});
for await(const line of lines){
 if(line==='exit'){await handleHook({hook_event_name:'SessionEnd',session_id:process.env.CODEX_SESSION_ID});break;}
 if(line==='close'){
  const result=await forgeCLI(['issue','close',ref,'--data',JSON.stringify({reason:'audit-no-change',message:'This generated process lifecycle fixture verifies Codex integration without changing product code.',evidence:[{type:'no-change-audit',rationale:'Ephemeral automated fixture with no product changes required.'}]})]);emit({closed:result.issue.status==='closed'});
 }
 if(line==='crash')process.kill(process.pid,'SIGKILL');
}

lines.close();process.stdin.destroy();
