#!/usr/bin/env node
import { register } from 'tsx/esm/api';
register();
const {handleHook}=await import('../codex/hooks.ts');
try {
 let input='';for await(const b of process.stdin){input+=b;if(Buffer.byteLength(input)>1048576)throw new Error();}
 process.stdout.write(JSON.stringify(await handleHook(JSON.parse(input)))+'\n');
} catch {
 // Never echo payloads, paths, credentials, transcript text or native exceptions.
 process.stdout.write(JSON.stringify({systemMessage:'Forge lifecycle unavailable. Use forge codex status before continuing claimed work; restart through forge codex if the runtime ended.'})+'\n');
}
