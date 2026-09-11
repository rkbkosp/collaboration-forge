#!/usr/bin/env node
// Host-side routing only. This never reads credentials or changes process cwd.
// Arguments are the host's registered repository roots, not worker overrides.
import {realpathSync} from 'node:fs';
import {spawnSync} from 'node:child_process';

// Resolve the actual cwd, ignoring inherited Git overrides (including config
// injection). Otherwise a foreign repository could impersonate a binding.
const env=Object.fromEntries(Object.entries(process.env).filter(([key])=>!key.startsWith('GIT_')));
function commonDirectory(cwd){
 try{
  const result=spawnSync('git',['-C',cwd,'rev-parse','--path-format=absolute','--git-common-dir'],
   {env,encoding:'utf8',timeout:5000,maxBuffer:64*1024});
  if(result.status!==0)return;
  return realpathSync(result.stdout.trimEnd());
 }catch{return;}
}
const common=commonDirectory(process.cwd());
let selected;
if(common){
 for(const candidate of process.argv.slice(2)){
  if(commonDirectory(candidate)===common){
   try{selected=realpathSync(candidate);}catch{}
   if(selected)break;
  }
 }
}
if(selected)process.stdout.write(selected+'\n');
else process.exitCode=2;
