import test from 'node:test';
import assert from 'node:assert/strict';
import {spawnSync} from 'node:child_process';
import {mkdtemp,mkdir,symlink,rm,realpath} from 'node:fs/promises';
import {join} from 'node:path';
import {fileURLToPath} from 'node:url';
const resolver=fileURLToPath(new URL('../scripts/forge-project-root.mjs',import.meta.url));

test('host routing recognizes registered Git worktrees without changing cwd or trusting Git environment overrides',async()=>{
 const dir=await realpath(await mkdtemp('/tmp/forge-project-route-'));
 const a=join(dir,'registered a'),b=join(dir,'registered b'),work=join(dir,'agent tree'),foreign=join(dir,'foreign');
 const git=(cwd:string,args:string[])=>{const r=spawnSync('git',args,{cwd,encoding:'utf8'});assert.equal(r.status,0,r.stderr);};
 try{
  for(const root of [a,b,foreign]){await mkdir(root);git(root,['init','-q']);git(root,['-c','user.name=Fixture','-c','user.email=fixture@example.invalid','commit','--allow-empty','-qm','base']);}
  git(a,['worktree','add','--detach',work,'HEAD']);await mkdir(join(work,'sub'));
  await symlink(work,join(dir,'alias'));
  const route=(cwd:string,roots=[a,b],env=process.env)=>spawnSync(process.execPath,[resolver,...roots],{cwd,env,encoding:'utf8'});
  for(const cwd of [a,work,join(work,'sub'),join(dir,'alias')]){const r=route(cwd);assert.equal(r.status,0,r.stderr);assert.equal(r.stdout.trim(),a);}
  assert.equal(route(b).stdout.trim(),b);
  for(const cwd of [foreign,dir]){const r=route(cwd);assert.equal(r.status,2);assert.equal(r.stdout,'');}
  // A different repository nested under a registered root must not inherit it.
  const nested=join(a,'nested');await mkdir(nested);git(nested,['init','-q']);assert.equal(route(nested).status,2);
  assert.equal(route(foreign,[a,b],{...process.env,GIT_DIR:join(a,'.git'),GIT_WORK_TREE:a}).status,2);
  assert.equal(route(work,[join(dir,'missing'),a]).stdout.trim(),a);
 }finally{await rm(dir,{recursive:true,force:true});}
});
