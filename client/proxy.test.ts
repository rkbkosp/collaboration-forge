import test from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { once } from 'node:events';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
const exec=promisify(execFile);

for(const implementation of ['http','controller']) test(`${implementation} never sends credentials via Node environment proxy`,async()=>{
 let proxyHits=0;
 const destination=createServer((_req,res)=>res.end(JSON.stringify({source:'direct',issues:[]})));
 const proxy=createServer((_req,res)=>{proxyHits++;res.end(JSON.stringify({source:'proxy',issues:[]}));});
 const tunnels=new Set<import('node:stream').Duplex>();
 proxy.on('connect',(_req,socket)=>{
  proxyHits++;tunnels.add(socket);socket.on('error',()=>{});socket.on('close',()=>tunnels.delete(socket));
  socket.write('HTTP/1.1 200 Connection Established\r\n\r\n');
  socket.once('data',()=>{const body=JSON.stringify({source:'proxy',issues:[]});socket.end(`HTTP/1.1 200 OK\r\nContent-Length: ${Buffer.byteLength(body)}\r\nConnection: close\r\n\r\n${body}`);});
 });
 destination.listen(0,'127.0.0.1');proxy.listen(0,'127.0.0.1');await Promise.all([once(destination,'listening'),once(proxy,'listening')]);
 const a=destination.address(),b=proxy.address();assert.ok(a&&typeof a==='object'&&b&&typeof b==='object');
 const url=`http://127.0.0.1:${a.port}`,proxyURL=`http://127.0.0.1:${b.port}`;
 const setup=implementation==='http'
  ? `import {ClientHTTP} from './client/http.ts'; const result=await new ClientHTTP('${url}','test-only-fixture','session').request('/forge/v1/project');`
  : `import {Controller} from './pi-extension/controller.ts';import {randomUUID} from 'node:crypto';const c=new Controller({url:'${url}',workerToken:'test-only-fixture',ttlSeconds:60},{sessionId:randomUUID()});const result=await c.execute('issue_list',{});await c.shutdown();`;
 try {
  const {stdout}=await exec(process.execPath,['--import','tsx','--input-type=module','-e',setup+'console.log(JSON.stringify(result));'],{timeout:15_000,env:{...process.env,NODE_USE_ENV_PROXY:'1',HTTP_PROXY:proxyURL,http_proxy:proxyURL,HTTPS_PROXY:proxyURL,https_proxy:proxyURL,NO_PROXY:'',no_proxy:''}});
  assert.equal(proxyHits,0,'Configured proxy must not see a Forge request');
  assert.equal(JSON.parse(stdout).source,'direct');
 } finally {for(const tunnel of tunnels)tunnel.destroy();destination.closeAllConnections();proxy.closeAllConnections();await Promise.all([new Promise<void>(r=>destination.close(()=>r())),new Promise<void>(r=>proxy.close(()=>r()))]);}
});
