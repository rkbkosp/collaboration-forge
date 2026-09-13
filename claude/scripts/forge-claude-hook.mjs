#!/usr/bin/env node
import { register } from 'tsx/esm/api';
// Load Forge dependencies independently of the target workspace path aliases.
register({tsconfig:false});
const {handleHook}=await import('../hook.ts');
try {
 let input='';for await(const b of process.stdin){input+=b;if(Buffer.byteLength(input)>1048576)throw new Error();}
 const payload=JSON.parse(input);
 process.stdout.write(JSON.stringify(await handleHook(payload)) + '\n');
} catch {
 // Never echo payloads, paths, credentials, transcript text or native exceptions.
 // Lease correctness never depends on a hook running: forged stops renewal when
 // the instance dies and Kata's timed lease recovers on its own.
 process.stdout.write(JSON.stringify({systemMessage:'Forge lifecycle unavailable. Use forge claude status before continuing claimed work; restart through forge claude if the runtime ended.'})+'\n');
}
