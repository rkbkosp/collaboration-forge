import test from 'node:test';
import assert from 'node:assert/strict';
import { ClientHTTP } from './http.ts';

test('HTTP client confines paths, bounds responses and redacts credentials', async()=>{
 const token='private-credential-test'; let calls=0;
 const client=new ClientHTTP('http://127.0.0.1:7347',token,'test-session',async(_url,init)=>{
  calls++;assert.equal(new Headers(init?.headers).get('Authorization')==='Bearer '+token,true);
  return new Response(JSON.stringify({nested:{token},note:token,execution_token:'proof-secret'}));
 });
 const result=await client.request('/forge/v1/project');
 assert.equal(JSON.stringify(result).includes(token),false);
 assert.equal(JSON.stringify(result).includes('proof-secret'),false);
 for(const path of ['https://evil.invalid','//evil.invalid','/api/v1/../x','/api/v1/%2e%2e/x']) await assert.rejects(client.request(path));
 assert.equal(calls,1);
});

test('HTTP write errors mark uncertainty without echoing server secrets',async()=>{
 const client=new ClientHTTP('http://127.0.0.1:7347','secret','id',async()=>new Response(JSON.stringify({error:{code:'oops',message:'secret'}}),{status:500}));
 await assert.rejects(client.request('/api/v1/test','POST',{}),(e:any)=>e.ambiguous && !e.message.includes('secret'));
});
