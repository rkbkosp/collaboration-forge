import test from 'node:test';
import assert from 'node:assert/strict';
import {spawn,spawnSync} from 'node:child_process';
import {mkdtemp,mkdir,writeFile,readFile,readdir,realpath,symlink,rm} from 'node:fs/promises';
import {join} from 'node:path';
import {homedir} from 'node:os';
import {fileURLToPath} from 'node:url';
import {hookConfiguration} from '../codex/bundle.ts';
import {startForge} from './harness.ts';
const forge=fileURLToPath(new URL('../scripts/forge.mjs',import.meta.url));
const quote=(s:string)=>"'"+s.replaceAll("'","'\\''")+"'";

test('real Codex model uses a claimed checkout then closes and archives without deployment',{skip:process.env.FORGE_REAL_CODEX!=='1',timeout:180_000},async()=>{
 const f=await startForge();const dir=await realpath(await mkdtemp('/tmp/forge-checkout-live-'));
 try{
  const source=join(dir,'source');await mkdir(source);const git=(args:string[])=>{const r=spawnSync('git',args,{cwd:source,encoding:'utf8'});assert.equal(r.status,0);};git(['init','-q']);git(['config','user.email','fixture@example.invalid']);git(['config','user.name','Fixture']);await writeFile(join(source,'tracked.txt'),'base\n');git(['add','.']);git(['commit','-qm','base']);await writeFile(join(source,'tracked.txt'),'dirty\n');
  const home=join(dir,'codex-home');await mkdir(home);await symlink(join(process.env.CODEX_HOME??join(homedir(),'.codex'),'auth.json'),join(home,'auth.json'));
  await writeFile(join(home,'config.toml'),`[features]\nhooks=true\n[projects.${JSON.stringify(source)}]\ntrust_level="trusted"\n`);
  await mkdir(join(source,'.codex'));
  const bundle=hookConfiguration(fileURLToPath(new URL('./codex-fixtures/record-hook.mjs',import.meta.url)));
  for(const groups of Object.values(bundle.hooks))for(const group of groups)for(const hook of group.hooks)hook.command+=' '+quote(join(dir,'hooks.jsonl'));
  await writeFile(join(source,'.codex/hooks.json'),JSON.stringify(bundle));
  const host=join(dir,'host-forge');await writeFile(host,`#!/bin/sh\nexec ${quote(process.execPath)} ${quote(forge)} "$@"\n`,{mode:0o700});
  const issue=(await f.admin(`/api/v1/projects/${f.projectID}/issues`,{title:'Real model checkout acceptance fixture'})).issue;
  const close={reason:'audit-no-change',message:'Generated smoke fixture verified source isolation and retained archive behavior. No product modification is required.',evidence:[{type:'no-change-audit',rationale:'Read-only inspection of a generated snapshot fixture validates this setup; no product code change was requested.'}]};
  const prompt=`This is a bounded disposable integration fixture. Do not modify files, read credentials, delegate, or do other work. Use the shell CLI "$FORGE_CODEX_CLI" for every forge command. First claim issue ${issue.uid}; then run checkout ${issue.uid} --dirty --source ${quote(source)}. Read the returned worktree absolute path. Run pwd and cat tracked.txt with the shell tool's working directory set to that returned worktree; it must contain dirty. From that same worktree run "$FORGE_CODEX_CLI" issue close ${issue.uid} --data ${quote(JSON.stringify(close))}. Confirm the close reports workspace_archive.state archived, then reply CHECKOUT_LIVE_OK. If a step fails, report only error categories and stop.`;
  const env={...process.env,CODEX_HOME:home,FORGE_URL:f.url,FORGE_WORKER_TOKEN_FILE:join(f.dataDir,'worker-token'),FORGE_CODEX_BASE_CLI:host,FORGE_CODEX_BINARY:'codex'};
  const child=spawn(process.execPath,[forge,'codex','exec','--ephemeral','--dangerously-bypass-hook-trust','-s','danger-full-access','-C',source,'--json',prompt],{env,stdio:['ignore','pipe','pipe']});let output='';let errors='';child.stdout.on('data',b=>output+=b);child.stderr.on('data',b=>errors+=b);
  const code=await new Promise<number|null>((resolve,reject)=>{const timer=setTimeout(()=>{child.kill('SIGTERM');reject(new Error('live checkout timeout'));},160_000);child.once('error',()=>{clearTimeout(timer);reject(new Error('live launch failed'));});child.once('close',code=>{clearTimeout(timer);resolve(code);});});
  assert.ok(!output.includes(f.workerToken)&&!errors.includes(f.workerToken)&&!output.includes(f.adminToken));assert.equal(code,0);assert.ok(output.includes('CHECKOUT_LIVE_OK'));
  const project=(await f.admin('/forge/v1/project')).project;const records=join(f.dataDir,'workspaces',project.uid,'records');const names=(await readdir(records)).filter(x=>x.endsWith('.json'));assert.equal(names.length,1);
  const record=JSON.parse(await readFile(join(records,names[0]),'utf8'));assert.equal(record.state,'archived');assert.ok(output.includes(record.worktree));
  const cwdObserved=output.split('\n').some(line=>{try{const event=JSON.parse(line);return typeof event.item?.aggregated_output==='string'&&event.item.aggregated_output.split('\n').includes(record.worktree);}catch{return false;}});assert.ok(cwdObserved,'a real shell pwd must report the managed worktree');assert.equal(await readFile(join(record.worktree,'tracked.txt'),'utf8'),'dirty\n');assert.equal(await readFile(join(source,'tracked.txt'),'utf8'),'dirty\n');
  const shown=await f.admin(`/api/v1/projects/${f.projectID}/issues/${issue.uid}`);assert.equal(shown.issue.status,'closed');assert.ok(shown.lease==null);
  const hooks=(await readFile(join(dir,'hooks.jsonl'),'utf8')).trim().split('\n').map(x=>JSON.parse(x));assert.ok(hooks.every(x=>x.ok));for(const event of ['SessionStart','PreToolUse','PostToolUse','Stop','SessionEnd'])assert.ok(hooks.some(x=>x.event===event),event);
  process.stdout.write(JSON.stringify({real_model_checkout:true,archived_without_deletion:true,hook_events:[...new Set(hooks.map(x=>x.event))]})+'\n');
 }finally{await f.close();await rm(dir,{recursive:true,force:true});}
});
