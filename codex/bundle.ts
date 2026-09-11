import { fileURLToPath } from 'node:url';
const quote=(s:string)=>"'"+s.replaceAll("'","'\\''")+"'";
export function hookConfiguration(script=fileURLToPath(new URL('../scripts/forge-codex-hook.mjs',import.meta.url)),node=process.execPath){
 const command=quote(node)+' '+quote(script);
 return {description:'Forge Codex lifecycle (CLI 0.154.0 contract)',hooks:Object.fromEntries(['SessionStart','SubagentStart','PreToolUse','PostToolUse','UserPromptSubmit','PreCompact','PostCompact','Stop','SubagentStop','Interrupt','SessionEnd'].map(event=>[event,[{hooks:[{type:'command',command,timeout:['SessionEnd','Interrupt'].includes(event)?3:10,...(['PreToolUse','PostToolUse'].includes(event)?{async:true}:{})}]}]]))};
}
