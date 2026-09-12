import test from 'node:test';
import assert from 'node:assert/strict';
import { launchCodex } from './launcher.ts';

test('launcher registers a fresh instance, strips inherited execution, forwards args and ends normally',async()=>{
 const calls:any[]=[];
 const env={PATH:process.env.PATH,FORGE_SOCKET:'/old',FORGE_CODEX_INSTANCE_ID:'old',FORGE_CODEX_TOKEN_FILE:'/old-token'};
 const deps={register:async(x:any)=>{calls.push(['register',x]);}, end:async(x:any)=>{calls.push(['end',x]);},run:async(_bin:string,args:string[],childEnv:any)=>{
  assert.deepEqual(args,['resume','abc']);assert.equal(childEnv.FORGE_SOCKET,undefined);
  assert.notEqual(childEnv.FORGE_CODEX_INSTANCE_ID,'old'); assert.notEqual(childEnv.FORGE_CODEX_TOKEN_FILE,'/old-token');
  calls.push(['run',childEnv.FORGE_CODEX_INSTANCE_ID]);return {code:0,signal:null};
 }};
 assert.equal(await launchCodex(['resume','abc'],env,deps),0);
 assert.equal(calls[0][0],'register');assert.equal(calls.at(-1)[1].normal,true);
 assert.equal(calls[0][1].pid,process.pid);
});
test('crash stops renewal without requesting normal release',async()=>{
 let ended:any;
 assert.equal(await launchCodex([],{}, {register:async()=>{},run:async()=>({code:null,signal:'SIGKILL'}),end:async(x:any)=>{ended=x;}}),1);
 assert.equal(ended.normal,false);
});

test('managed worktrees use the same runtime CLI while ordinary paths keep host project routing',async()=>{
 const {mkdtemp,mkdir,writeFile,rm}=await import('node:fs/promises');const {tmpdir}=await import('node:os');const {join}=await import('node:path');const {spawnSync}=await import('node:child_process');
 const dir=await mkdtemp(join(tmpdir(),'forge-shim-'));const root=join(dir,'managed');const work=join(dir,'volume','opaque-id','worktree');await mkdir(work,{recursive:true});await mkdir(join(root,'records'),{recursive:true});await writeFile(join(root,'records','workspace.json'),JSON.stringify({project_uid:'project',worktree:work}));
 const sub=join(work,'sub');await mkdir(sub);const lookalike=join(root,'trees','issue','tenure','repo','job','worktree');await mkdir(lookalike,{recursive:true});
 const host=join(dir,'forge');await writeFile(host,'#!/bin/sh\necho host-project-routing\n',{mode:0o700});
 try{await launchCodex([], {PATH:dir+':'+process.env.PATH}, {register:async()=>({workspace_root:root,project_uid:'project'}),end:async()=>{},run:async(_b,_a,env)=>{
  const plain=spawnSync('forge',['--help'],{cwd:dir,env,encoding:'utf8'});assert.match(plain.stdout,/host-project-routing/);
  const fake=spawnSync('forge',['--help'],{cwd:lookalike,env,encoding:'utf8'});assert.match(fake.stdout,/host-project-routing/);
  const nested=spawnSync('forge',['--help'],{cwd:sub,env,encoding:'utf8'});assert.equal(nested.status,0);assert.match(nested.stdout,/Forge client/);
  const managed=spawnSync('forge',['--help'],{cwd:work,env,encoding:'utf8'});assert.equal(managed.status,0);assert.match(managed.stdout,/Forge client/);assert.ok(!managed.stdout.includes('host-project-routing'));
  return {code:0,signal:null};
 }});}finally{await rm(dir,{recursive:true,force:true});}
});
