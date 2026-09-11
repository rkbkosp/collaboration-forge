import test from 'node:test';
import assert from 'node:assert/strict';
import {codexRPC,CodexError} from './transport.ts';

test('missing worker configuration returns a specific safe configuration error',async()=>{
 await assert.rejects(codexRPC('register',{},{}),e=>e instanceof CodexError&&e.code==='codex_configuration'&&!e.ambiguous);
});
test('invalid configuration never echoes supplied URL secrets',async()=>{
 await assert.rejects(codexRPC('register',{}, {FORGE_URL:'https://secret-user:secret-password@example.com'}),e=>e instanceof CodexError&&e.code==='codex_configuration'&&!e.message.includes('secret'));
});
