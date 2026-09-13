import test from 'node:test';
import assert from 'node:assert/strict';
import { stopDecision } from './runtime-stop.ts';

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

test('each harness is told to use its own commands, never another harness\'s',()=>{
 // Codex wording is unchanged by the shared policy.
 const codex = stopDecision({ active: true, pending: true }, {}, 'strict', 'forge codex');
 assert.equal(codex.decision, 'block');
 assert.match(codex.reason!, /forge codex retry/);
 assert.equal(codex.reason!.includes('forge claude'), false);

 const claude = stopDecision({ active: true, pending: true }, {}, 'strict', 'forge claude');
 assert.equal(claude.decision, 'block');
 assert.match(claude.reason!, /forge claude retry/);
 assert.equal(claude.reason!.includes('forge codex'), false, 'a Claude session must not be told to run Codex commands');

 // The held-work wording and the pause hint follow the same rule.
 const held = stopDecision({ active: true, issue: 'abc' }, {}, 'strict', 'forge claude');
 assert.match(held.reason!, /forge claude pause/);
 assert.equal(held.reason!.includes('forge codex'), false);
 const advisory = stopDecision({ active: true, issue: 'abc' }, {}, 'advisory', 'forge claude');
 assert.match(advisory.systemMessage!, /forge claude pause/);
 assert.equal(advisory.systemMessage!.includes('forge codex'), false);
});
