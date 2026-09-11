#!/usr/bin/env node
import { register } from 'tsx/esm/api';
register();
if(process.argv[2]==='codex'){
 const {codexCommand}=await import('../codex/cli.ts');
 process.exitCode=await codexCommand(process.argv.slice(3));
}else{
 const {main}=await import('../client/main.ts');
 process.exitCode=await main(process.argv.slice(2));
}
