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
