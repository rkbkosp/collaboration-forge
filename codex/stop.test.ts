import test from 'node:test';
import assert from 'node:assert/strict';
import { stopDecision } from './stop.ts';
test('strict blocks held/pending work once, never loops or blocks a user pause',()=>{
 assert.equal(stopDecision({active:true},{},'strict').decision,'block');
 assert.equal(stopDecision({pending:true},{},'strict').decision,'block');
 assert.equal(stopDecision({active:true},{stop_hook_active:true},'strict').decision,undefined);
 assert.equal(stopDecision({active:true},{last_assistant_message:'我需要用户确认以后再执行。'},'strict').decision,undefined);
 assert.equal(stopDecision({active:true,paused:true},{},'strict').decision,undefined);
 assert.deepEqual(stopDecision({active:false,pending:false},{},'strict'),{});
});
test('advisory reports unresolved work without creating a continuation',()=>{
 const result=stopDecision({active:true,issue:'abc'},{},'advisory');assert.equal(result.decision,undefined);assert.ok(result.systemMessage);
});
