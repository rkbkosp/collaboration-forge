#!/usr/bin/env node
import { register } from 'tsx/esm/api';
// Load Forge dependencies independently of the target workspace path aliases.
register({tsconfig:false});
if(process.argv[2]==='checkout'){
 const {checkoutCommand}=await import('../codex/checkout.ts');
 process.exitCode=await checkoutCommand(process.argv.slice(3));
}else if(process.argv[2]==='pi'){
 const {piCommand}=await import('../client/pi.ts');
 process.exitCode=await piCommand(process.argv.slice(3));
}else if(process.argv[2]==='codex'){
 const {codexCommand}=await import('../codex/cli.ts');
 process.exitCode=await codexCommand(process.argv.slice(3));
}else{
 const {main}=await import('../client/main.ts');
 process.exitCode=await main(process.argv.slice(2));
}
