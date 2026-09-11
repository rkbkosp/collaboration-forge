import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp,readFile,rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {codexCommand} from './cli.ts';
test('hook installer creates a standalone bounded bundle and refuses overwrite',async()=>{
 const dir=await mkdtemp(join(tmpdir(),'forge-codex-install-'));
 try{
  assert.equal(await codexCommand(['install','--target',dir]),0);
  const config=JSON.parse(await readFile(join(dir,'hooks.json'),'utf8'));
  assert.ok(config.hooks.SubagentStart);assert.ok(config.hooks.Stop);assert.equal(config.hooks.SessionEnd[0].hooks[0].timeout,3);
  assert.equal(await codexCommand(['install','--target',dir]),1);
 }finally{await rm(dir,{recursive:true,force:true});}
});
