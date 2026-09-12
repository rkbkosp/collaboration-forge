import test from 'node:test';
import assert from 'node:assert/strict';
import {spawnSync} from 'node:child_process';
import {mkdtemp,symlink,rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {fileURLToPath} from 'node:url';

test('published package loads checkout CLI and Pi extension without source-only modules',async()=>{
 const root=fileURLToPath(new URL('../',import.meta.url));const dir=await mkdtemp(join(tmpdir(),'forge-package-'));
 const run=(bin:string,args:string[],cwd:string)=>spawnSync(bin,args,{cwd,encoding:'utf8',timeout:30_000});
 try{
  const packed=run('npm',['pack','--ignore-scripts','--json','--pack-destination',dir],root);assert.equal(packed.status,0);
  const filename=JSON.parse(packed.stdout)[0].filename;
  assert.equal(run('tar',['-xzf',join(dir,filename),'-C',dir],dir).status,0);
  const pkg=join(dir,'package');await symlink(join(root,'node_modules'),join(pkg,'node_modules'),'dir');
  for(const args of [['scripts/forge.mjs','--help'],['scripts/forge.mjs','checkout','--help'],['--import','tsx','--input-type=module','-e',"await import('./pi-extension/forge.ts')"]]){
   const result=run(process.execPath,args,pkg);assert.equal(result.status,0,result.stderr);
  }
 }finally{await rm(dir,{recursive:true,force:true})}
});
