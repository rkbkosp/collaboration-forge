import test from 'node:test';
import assert from 'node:assert/strict';
import {spawn,spawnSync} from 'node:child_process';
import {mkdtemp,mkdir,writeFile,readFile,rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {fileURLToPath} from 'node:url';
import {startForge} from './harness.ts';
const script=fileURLToPath(new URL('../scripts/forge.mjs',import.meta.url));
const fixture=fileURLToPath(new URL('./codex-fixtures/checkout-worker.ts',import.meta.url));
const quote=(s:string)=>"'"+s.replaceAll("'","'\\''")+"'";

test('real Codex facade checkout reuses one claim across repositories and retains archived trees',{timeout:90_000},async()=>{
 const f=await startForge();const dir=await mkdtemp(join(tmpdir(),'forge-checkout-process-'));
 try{
  async function repo(name:string){const d=join(dir,name);await mkdir(d);const git=(args:string[])=>{const p=spawnSync('git',args,{cwd:d,encoding:'utf8'});assert.equal(p.status,0,p.stderr);};git(['init','-q']);git(['config','user.email','fixture@example.invalid']);git(['config','user.name','Fixture']);await writeFile(join(d,'tracked.txt'),'base\n');git(['add','.']);git(['commit','-qm','base']);return d;}
  const a=await repo('first');const b=await repo('second');await writeFile(join(a,'tracked.txt'),'dirty\n');await writeFile(join(a,'new.txt'),'untracked\n');
  const resolver=fileURLToPath(new URL('../scripts/forge-project-root.mjs',import.meta.url));
  const host=join(dir,'host-forge');await writeFile(host,`#!/bin/sh\n${quote(process.execPath)} ${quote(resolver)} ${quote(a)} ${quote(b)} >/dev/null || exit 2\nexec ${quote(process.execPath)} ${quote(script)} "$@"\n`,{mode:0o700});
  const issue=(await f.admin(`/api/v1/projects/${f.projectID}/issues`,{title:'Multi-repository checkout fixture'})).issue;
  const env={...process.env,FORGE_URL:f.url,FORGE_WORKER_TOKEN_FILE:join(f.dataDir,'worker-token'),FORGE_CODEX_BINARY:process.execPath,FORGE_CODEX_BASE_CLI:host};
  const child=spawn(process.execPath,[script,'codex','--import','tsx',fixture,issue.uid,a,b],{env,stdio:['ignore','pipe','pipe']});let output='';let errors='';child.stdout.on('data',b=>output+=b);child.stderr.on('data',b=>errors+=b);
  const code=await new Promise<number|null>((resolve,reject)=>{const timer=setTimeout(()=>{child.kill('SIGTERM');reject(new Error('checkout process timeout'));},75_000);child.once('error',()=>{clearTimeout(timer);reject(new Error('checkout worker launch failed'));});child.once('close',code=>{clearTimeout(timer);resolve(code);});});
  assert.ok(!output.includes(f.workerToken)&&!errors.includes(f.workerToken));assert.equal(code,0,'real checkout process must succeed');
  const result=JSON.parse(output.trim());assert.ok(result.closed&&result.archived);assert.equal(result.workspaces.length,2);assert.equal(result.workspaces[0].tenure,result.workspaces[1].tenure);
  assert.notEqual(result.workspaces[0].repository_id,result.workspaces[1].repository_id);
  for(const r of result.workspaces){assert.equal(r.project_uid,(await f.admin('/forge/v1/project')).project.uid);assert.equal(await readFile(join(r.worktree,'tracked.txt'),'utf8'),r.source_kind==='dirty'?'dirty\n':'base\n');}
  const shown=await f.admin(`/api/v1/projects/${f.projectID}/issues/${issue.uid}`);assert.equal(shown.issue.status,'closed');assert.ok(shown.lease==null);
  await f.restart();
  for(const r of result.workspaces){const stored=JSON.parse(await readFile(join(f.dataDir,'workspaces',r.project_uid,'records',r.workspace_id+'.json'),'utf8'));assert.equal(stored.state,'archived');assert.ok(!JSON.stringify(stored).includes('execution_token'));}
  assert.equal(await readFile(join(a,'tracked.txt'),'utf8'),'dirty\n');
 }finally{await f.close();await rm(dir,{recursive:true,force:true});}
});
