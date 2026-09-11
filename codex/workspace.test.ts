import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp,writeFile,rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {fileURLToPath} from 'node:url';
import {spawnSync} from 'node:child_process';

test('CLI and hook entrypoints ignore the working repository tsconfig aliases',async()=>{
 const dir=await mkdtemp(join(tmpdir(),'forge-foreign-tsconfig-'));
 try{
  await writeFile(join(dir,'tsconfig.json'),JSON.stringify({compilerOptions:{baseUrl:'.',paths:{'@earendil-works/pi-ai':['./poison.ts'],'undici':['./poison.ts'],'typebox':['./poison.ts']}}}));
  await writeFile(join(dir,'poison.ts'),"throw new Error('foreign_tsconfig_loaded'); export {};\n");
  for(const [entry,args] of [['forge.mjs',['--help']],['forge-codex-hook.mjs',[]]] as const){
   const result=spawnSync(process.execPath,[fileURLToPath(new URL('../scripts/'+entry,import.meta.url)),...args],{cwd:dir,encoding:'utf8',input:'{}',timeout:15_000});
   assert.equal(result.status,0,entry+' must load its own dependencies: '+result.stderr);
   assert.ok(!result.stderr.includes('poison'));
  }
 }finally{await rm(dir,{recursive:true,force:true});}
});
