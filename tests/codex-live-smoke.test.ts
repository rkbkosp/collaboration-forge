import test from 'node:test';
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {mkdtemp,mkdir,writeFile,readFile,rm,realpath,symlink} from 'node:fs/promises';
import {join} from 'node:path';
import {homedir} from 'node:os';
import {fileURLToPath} from 'node:url';
import {startForge} from './harness.ts';
import {hookConfiguration} from '../codex/bundle.ts';
const forge=fileURLToPath(new URL('../scripts/forge.mjs',import.meta.url));

test('real Codex CLI model/tool/Stop flow uses the Forge launcher and authenticated CLI facade',{skip:process.env.FORGE_REAL_CODEX!=='1',timeout:180_000},async()=>{
 const f=await startForge();const dir=await realpath(await mkdtemp('/tmp/forge-real-codex-'));
 try{
  await mkdir(join(dir,'.codex'));
  const codexHome=join(dir,'codex-home');await mkdir(codexHome);
  await symlink(join(process.env.CODEX_HOME??join(homedir(),'.codex'),'auth.json'),join(codexHome,'auth.json'));
  await writeFile(join(codexHome,'config.toml'),`[features]\nhooks=true\n[projects.${JSON.stringify(dir)}]\ntrust_level="trusted"\n`);
  const bundle=hookConfiguration(fileURLToPath(new URL('./codex-fixtures/record-hook.mjs',import.meta.url)));
  for(const groups of Object.values(bundle.hooks))for(const group of groups)for(const hook of group.hooks)hook.command+=' '+JSON.stringify(join(dir,'hook-events.jsonl'));
  await writeFile(join(dir,'.codex','hooks.json'),JSON.stringify(bundle));
  const issue=(await f.admin(`/api/v1/projects/${f.projectID}/issues`,{title:'Real Codex lifecycle smoke fixture'})).issue;
  const close=JSON.stringify({reason:'audit-no-change',message:'This generated smoke fixture verifies real Codex CLI and Forge lifecycle; no product changes are required.',evidence:[{type:'no-change-audit',rationale:'This is an ephemeral automated integration fixture, not a product change request.'}]});
  const quote=(s:string)=>"'"+s.replaceAll("'","'\\''")+"'";
  const command=`${quote(process.execPath)} ${quote(forge)} issue claim ${issue.uid} && ${quote(process.execPath)} ${quote(forge)} issue close ${issue.uid} --data ${quote(close)}`;
  const prompt='This is a bounded integration smoke test in an empty temporary workspace. Do not modify files, inspect credentials, spawn subagents, or perform other work. Use a shell tool to execute exactly this command, then reply SMOKE_OK if it succeeds; otherwise report the error code and stop. '+command;
  const env={...process.env,CODEX_HOME:codexHome,FORGE_URL:f.url,FORGE_WORKER_TOKEN_FILE:join(f.dataDir,'worker-token'),FORGE_CODEX_BINARY:'codex',FORGE_SOCKET:'',FORGE_ADMIN_TOKEN_FILE:''};
  const child=spawn(process.execPath,[forge,'codex','exec','--ephemeral','--enable','hooks','-c',`projects.${JSON.stringify(dir)}.trust_level="trusted"`,'--skip-git-repo-check','--dangerously-bypass-hook-trust','-s','danger-full-access','-C',dir,'--json',prompt],{env,stdio:['ignore','pipe','pipe']});
  let output='';let errors='';child.stdout.on('data',b=>{output+=b;});child.stderr.on('data',b=>{errors+=b;});
  const code=await new Promise<number|null>((resolve,reject)=>{const timer=setTimeout(()=>{child.kill('SIGTERM');reject(new Error('real Codex timeout'));},160_000);child.once('error',()=>{clearTimeout(timer);reject(new Error('real Codex launch failure'));});child.once('exit',code=>{clearTimeout(timer);resolve(code);});});
  assert.equal(output.includes(f.workerToken)||output.includes(f.adminToken)||errors.includes(f.workerToken)||errors.includes(f.adminToken),false,'credential disclosure');
  // Only emit bounded categories, never raw backend errors or credential-bearing environment.
  if(code!==0)process.stderr.write(JSON.stringify({real_codex_exit:code,auth_error:/auth|unauthorized|login/i.test(errors),config_error:/config|unknown|unrecognized/i.test(errors)})+'\n');
  assert.equal(code,0,'real Codex must complete successfully');
  const current=await f.admin(`/api/v1/projects/${f.projectID}/issues/${issue.uid}`);
  if(current.issue.status!=='closed')process.stderr.write(JSON.stringify({forge_error_codes:[...output.matchAll(/codex_[a-z_]+/g)].map(x=>x[0]).slice(0,10),saw_smoke_ok:output.includes('SMOKE_OK')})+'\n');
  assert.equal(current.issue.status,'closed');assert.equal(current.lease==null,true);
  assert.ok(output.includes('SMOKE_OK'));
  const hooks=(await readFile(join(dir,'hook-events.jsonl'),'utf8')).trim().split('\n').map(x=>JSON.parse(x));
  assert.ok(hooks.every(x=>x.ok),'all actual Codex hooks must reach forged');
  for(const event of ['SessionStart','PreToolUse','PostToolUse','Stop','SessionEnd'])assert.ok(hooks.some(x=>x.event===event),event+' must fire');
  process.stdout.write(JSON.stringify({verified_real_hooks:[...new Set(hooks.map(x=>x.event))]})+'\n');
 }finally{await f.close();await rm(dir,{recursive:true,force:true});}
});
