import test from 'node:test';
import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { join } from 'node:path';
const exec=promisify(execFile);
async function cli(args:string[]){
 try{const r=await exec(process.execPath,['scripts/forge.mjs',...args],{env:{...process.env,FORGE_SOCKET:'',FORGE_ADMIN_TOKEN_FILE:''}});return {...r,code:0};}
 catch(e:any){return {stdout:String(e.stdout),stderr:String(e.stderr),code:e.code};}
}
test('standalone help/version and failures have stable exit codes',async()=>{
 assert.match((await cli(['--help'])).stdout,/Forge client/);
 assert.match((await cli(['--version'])).stdout,/0\.1\.0/);
 assert.equal((await cli(['issue','claim','abc4'])).code,2);
 assert.equal((await cli(['admin','reopen','abc4'])).code,2);
 assert.equal((await cli(['issue','create','--unknown-secret','do-not-echo'])).code,2);
 assert.equal((await cli(['issue','create','--unknown-secret','do-not-echo'])).stderr.includes('do-not-echo'),false);
});
test('human mode reuses stock arguments and has explicit JSON escape hatch',async()=>{
 assert.match((await cli(['human','--help'])).stdout,/structured human-readable output/);
 const human=await cli(['human','skill','path']);
 assert.equal(human.code,0);assert.match(human.stdout,/Forge skill path\n\s+path:/);assert.equal(human.stdout.trimStart().startsWith('{'),false);
 const json=await cli(['human','--format','json','skill','path']);
 assert.equal(json.code,0);const parsed=JSON.parse(json.stdout);assert.equal(typeof parsed.path,'string');assert.match(parsed.path,/skills[\\/]collab-forge-client[\\/]$/);
});
test('local schema failures are usage errors with a stable envelope',async()=>{
 const result=await cli(['issue','list','--status','bogus']);
 assert.equal(result.code,2);
 const error=JSON.parse(result.stderr);
 assert.equal(error.error.code,'usage');assert.equal(error.error.message,'Invalid Forge tool parameters; use only the published schema');
 assert.equal(error.error.ambiguous,false);assert.equal(error.error.message.includes('usage:'),false);
});
test('skill installation is self-contained and never overwrites a target',async()=>{
 const dir=await mkdtemp('/tmp/forge-skill-');const target=join(dir,'collab-forge-client');
 try{
  assert.equal((await cli(['skill','install','--target',target])).code,0);
  assert.match(await readFile(join(target,'SKILL.md'),'utf8'),/name: collab-forge-client/);
  assert.ok((await readFile(join(target,'references','cli.md'),'utf8')).includes('session retry'));
  assert.equal((await cli(['skill','install','--target',target])).code,1);
 }finally{await rm(dir,{recursive:true,force:true});}
});
