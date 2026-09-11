import test from 'node:test';
import assert from 'node:assert/strict';
import { codexIdentity, codexTool } from './facade.ts';
import { CodexError } from './transport.ts';
const env={FORGE_CODEX_INSTANCE_ID:'i',CODEX_SESSION_ID:'s',CODEX_THREAD_ID:'t'};
test('identity requires all three coordinates and never uses inherited broker socket',()=>{
 assert.deepEqual(codexIdentity({...env,FORGE_SOCKET:'/old'}),{instance_id:'i',session_id:'s',thread_id:'t'});
 assert.throws(()=>codexIdentity({...env,CODEX_THREAD_ID:''}));
});
test('network retries reuse the exact logical command including request identity',async()=>{
 const seen:any[]=[];const original={ref:'abcd'};
 const reply=await codexTool('issue_claim',original,env,async(_op,b)=>{seen.push(structuredClone(b));if(seen.length===1){original.ref='changed';throw new CodexError('codex_transport_unknown',true);}return {granted:true};});
 assert.equal(reply.granted,true);assert.deepEqual(seen[0],seen[1]);assert.equal(seen[0].params.ref,'abcd');assert.ok(seen[0].request_id);
});
