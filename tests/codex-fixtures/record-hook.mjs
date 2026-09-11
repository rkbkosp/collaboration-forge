import {register} from 'tsx/esm/api';
import {appendFile} from 'node:fs/promises';
register();
const {handleHook}=await import('../../codex/hooks.ts');
let raw='';for await(const chunk of process.stdin){raw+=chunk;if(Buffer.byteLength(raw)>1048576)throw new Error('fixture input limit');}
const input=JSON.parse(raw);
try{
 const result=await handleHook(input);
 await appendFile(process.argv[2],JSON.stringify({event:input.hook_event_name,ok:true})+'\n');
 process.stdout.write(JSON.stringify(result)+'\n');
}catch{
 await appendFile(process.argv[2],JSON.stringify({event:input.hook_event_name,ok:false})+'\n');
 process.stdout.write('{}\n');
}
