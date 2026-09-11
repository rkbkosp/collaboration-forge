import test from 'node:test';
import assert from 'node:assert/strict';
import {parseCheckout,runCheckout} from './checkout.ts';
test('checkout requires an explicit snapshot source kind and never supplies execution authority',()=>{
 assert.throws(()=>parseCheckout(['abc']));assert.throws(()=>parseCheckout(['abc','--dirty','--ref','HEAD']));
 assert.deepEqual(parseCheckout(['abc','--recover','old','--dirty']).params,{ref:'abc',recover:'old',dirty:true});
 const p=parseCheckout(['abc','--source','/repo','--ref','HEAD']);assert.deepEqual(p.params,{ref:'abc',source:'/repo',commit:'HEAD',dirty:false});
 assert.throws(()=>parseCheckout(['abc','--dirty','--dest','/arbitrary']));
});
test('checkout polls a single logical job without a second acquire or checkout',async()=>{
 const calls:string[]=[];let polls=0;
 const result=await runCheckout(['abc','--dirty'],{},async(op)=>{calls.push(op);return op==='checkout'?{workspace_id:'job',state:'preparing'}:{workspace_id:'job',state:++polls===1?'preparing':'ready',worktree:'/isolated/worktree'};},async()=>{});
 assert.equal(result.state,'ready');assert.deepEqual(calls,['checkout','checkout_status','checkout_status']);
});
