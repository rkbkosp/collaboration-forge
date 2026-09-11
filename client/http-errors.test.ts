import test from 'node:test';
import assert from 'node:assert/strict';
import { ClientHTTP } from './http.ts';

test('plain-text auth rejection is definite and never echoed as an ambiguous mutation',async()=>{
 const client=new ClientHTTP('http://127.0.0.1:7347','private-token','session',async()=>new Response('private-token',{status:403}));
 await assert.rejects(client.request('/api/v1/issues','POST',{}),(error:any)=>error.status===403 && !error.ambiguous && !error.message.includes('private-token'));
});
test('rate-limit and server failures are ambiguous only for mutations',async()=>{
 for(const method of ['POST','PATCH']) {
  const client=new ClientHTTP('http://127.0.0.1:7347','private-token','session',async()=>new Response(JSON.stringify({error:{code:'rate_limited',message:'retry later'}}),{status:429}));
  await assert.rejects(client.request('/api/v1/issues',method,{}),(error:any)=>error.status===429 && error.ambiguous===true);
 }
 const read=new ClientHTTP('http://127.0.0.1:7347','private-token','session',async()=>new Response(JSON.stringify({error:{code:'rate_limited',message:'retry later'}}),{status:429}));
 await assert.rejects(read.request('/api/v1/issues'),(error:any)=>error.status===429 && error.ambiguous===false);
});

test('oversized or interrupted rejection bodies remain definite',async()=>{
 for(const response of [new Response('x'.repeat(8_388_609),{status:403}),new Response(new ReadableStream({start(c){c.error(new Error('untrusted detail'));}}),{status:403})]) {
  const client=new ClientHTTP('http://127.0.0.1:7347','private-token','session',async()=>response);
  await assert.rejects(client.request('/api/v1/issues','POST',{}),(error:any)=>error.status===403 && !error.ambiguous && !error.message.includes('untrusted detail'));
 }
});

test('structured server error fields survive client propagation',async()=>{
 const client=new ClientHTTP('http://127.0.0.1:7347','private-token','session',async()=>new Response(JSON.stringify({status:409,error:{code:'claim_denied',message:'another execution holds this issue',hint:'wait for the lease',data:{cleanup_pending:false}}}),{status:409}));
 await assert.rejects(client.request('/api/v1/issues','POST',{}),(error:any)=>error.code==='claim_denied' && error.status===409 && error.hint==='wait for the lease' && error.data.cleanup_pending===false && error.message.includes('another execution holds this issue'));
});

test('native optimistic-concurrency and retry headers are preserved',async()=>{
 const client=new ClientHTTP('http://127.0.0.1:7347','private-token','session',async(_url,init)=>{
  const headers=new Headers(init?.headers);
  assert.equal(headers.get('If-Match'),'"rev-1"');assert.equal(headers.get('Idempotency-Key'),'logical-close');
  return new Response('{}');
 });
 await client.request('/api/v1/issues','POST',{},undefined,'"rev-1"','logical-close');
});
