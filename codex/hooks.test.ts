import test from 'node:test';
import assert from 'node:assert/strict';
import { handleHook } from './hooks.ts';
const env={FORGE_CODEX_INSTANCE_ID:'i',CODEX_SESSION_ID:'s',CODEX_THREAD_ID:'root'};
test('subagent attribution uses agent_id, while compaction only refreshes context',async()=>{
 const calls:any[]=[];const rpc=async(op:string,b:any)=>{calls.push([op,b]);return {active:false};};
 const start=await handleHook({hook_event_name:'SubagentStart',session_id:'s',agent_id:'child'},env,rpc);
 assert.equal(calls[0][1].identity.thread_id,'child');assert.equal(calls[0][1].identity.session_id,'s');
 assert.equal(start.hookSpecificOutput.hookEventName,'SubagentStart');
 await handleHook({hook_event_name:'SessionStart',session_id:'s',source:'compact'},env,rpc);
 assert.equal(calls[1][1].event,'context');
});
test('SubagentStop is a stop guard, not shutdown; SessionEnd is session-scoped',async()=>{
 const calls:any[]=[];const rpc=async(op:string,b:any)=>{calls.push([op,b]);return {active:false};};
 await handleHook({hook_event_name:'SubagentStop',session_id:'s',agent_id:'child'},env,rpc);
 assert.equal(calls[0][1].event,'stop_check');
 await handleHook({hook_event_name:'SessionEnd',session_id:'s'},env,rpc);
 assert.equal(calls[1][1].event,'session_end');
});
test('invalid input does not perform lifecycle mutation',async()=>{
 let called=false;await assert.rejects(handleHook({hook_event_name:'SessionEnd',session_id:'wrong'},env,async()=>{called=true;}));assert.equal(called,false);
});
