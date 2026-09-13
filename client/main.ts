import { codexTool } from '../codex/facade.ts';
import { claudeTool } from '../claude/facade.ts';
import { parseArgs } from 'node:util';
import { randomUUID } from 'node:crypto';
import { constants } from 'node:fs';
import { open, mkdir, cp, readdir } from 'node:fs/promises';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { setTimeout as delay } from 'node:timers/promises';
import { Controller, ForgeError } from '../pi-extension/controller.ts';
import { forgeErrorEnvelope } from '../pi-extension/errors.ts';
import { loadConfig, validateURL } from '../pi-extension/config.ts';
import { toolSchemas, type ToolName, validateParams } from '../pi-extension/schemas.ts';
import { ClientHTTP } from './http.ts';
import { startSession, requestSession } from './session.ts';

const stringFlags = ['url','worker-token-file','admin-token-file','socket','session-id','ttl','data','data-file','title','body','body-file','status','limit','depth','reason','purpose','message','message-file','evidence','evidence-file','if-match','idempotency-key','after-id','max-pages','owner','priority','confirm','type','to-ref','target','format'];
const boolFlags = ['help','version','all','clear-owner','json'];
const HELP = `Forge client — JSON output; credentials are FILE paths, never token arguments.

forge human <same client arguments> [--format human|json]
  Render deterministic human-readable output; --json selects JSON explicitly.

forge codex [Codex arguments...]  (daemon renewal + lifecycle hooks)
forge codex --help-forge
forge pi [Pi arguments...]       (routed Forge extension)
forge checkout ISSUE (--ref COMMIT | --dirty) [--source REPO]
forge checkout list | status WORKSPACE_ID | archive WORKSPACE_ID
forge health | project
forge issue list [--status open|closed] [--limit N]
forge issue get REF | graph REF [--depth 1..10]
forge issue create --title TEXT [--body TEXT | --body-file FILE]
forge issue comment REF --body TEXT | --body-file FILE
forge issue link REF --type parent|blocks|related --to-ref REF
forge issue timeline REF [--after-id N] [--limit N] [--all] [--max-pages N]
forge issue claim REF [--purpose TEXT]
forge issue renew REF | release REF [--reason TEXT]
forge issue close REF --reason done --message TEXT --evidence-file FILE
forge tool issue_NAME --data JSON | --data-file FILE  (same strict worker schema)

forge session start [--socket /private/dir/name.sock] [--ttl 60..3600]
forge session status | guard | context | retry | stop | wait --socket PATH
  start stays in foreground, owns heartbeat and private retry state.
  claim/renew/release/close require this live session. guard exits 4 if blocked.
  retry replays the original pending acquire/close without new arguments.

forge admin create | list | get REF | comment REF | link REF
forge admin update REF [--owner NAME | --clear-owner] [--priority 0..4|none]
forge admin reopen REF | close REF | force-release REF --reason TEXT
forge admin delete REF --confirm 'DELETE project#short_id'
forge admin label add|remove REF LABEL | unlink REF LINK_ID
forge admin request METHOD /api/v1/PATH [--data-file FILE] [--confirm TEXT]
  admin ALWAYS requires separate --admin-token-file or FORGE_ADMIN_TOKEN_FILE.
  Native project/token/federation administration remains disabled by the server.

forge skill path | install --target /path/to/skills/collab-forge-client

Common: --url (FORGE_URL, default http://127.0.0.1:7347),
--worker-token-file (FORGE_WORKER_TOKEN_FILE), --socket (FORGE_SOCKET).
--data/--data-file supply an object; named fields cannot overwrite it.
Files may be '-' for stdin. Close evidence is a typed JSON array, not prose.
Exit: 0 success, 1 local/HTTP error, 2 usage, 3 ambiguous mutation, 4 guard blocked.
Trusted local use only: same-UID tools can read credentials; this is not a sandbox.
`;
const HUMAN_HELP = `Forge human — structured human-readable output; credentials are FILE paths, never token arguments.

forge human issue list [--status open|closed] [--limit N]
forge human issue get REF | graph REF [--depth 1..10]
forge human issue timeline REF [--after-id N] [--limit N] [--all]
forge human admin ...
forge human health | project

Use --format json or --json for machine-readable output. The same worker,
admin, session, and server authority rules apply as the stock forge command.
`;

function usage(message: string): never { throw new ForgeError('usage',message); }
function integer(value: unknown, min: number, max: number): number {
  if(typeof value !== 'string' || !/^\d+$/.test(value)) usage('Expected an integer');
  const n=Number(value);if(!Number.isSafeInteger(n)||n<min||n>max)usage(`Integer must be in ${min}..${max}`);return n;
}
async function textFile(path: string): Promise<string> {
  if(path==='-') {
    const chunks:Buffer[]=[];let length=0;
    for await(const chunk of process.stdin){const bytes=Buffer.from(chunk);length+=bytes.length;if(length>1_048_576)usage('Input exceeds 1 MiB');chunks.push(bytes);}
    return Buffer.concat(chunks).toString('utf8');
  }
  let file;
  try {
    file=await open(path,constants.O_RDONLY|constants.O_NONBLOCK);
    const stat=await file.stat();if(!stat.isFile()||stat.size>1_048_576)usage('Input must be a regular file up to 1 MiB');
    const buffer=Buffer.alloc(1_048_577);let length=0;
    while(length<buffer.length){const part=await file.read(buffer,length,buffer.length-length,null);if(!part.bytesRead)break;length+=part.bytesRead;}
    if(length>1_048_576)usage('Input exceeds 1 MiB');return buffer.subarray(0,length).toString('utf8');
  } catch(error){if(error instanceof ForgeError)throw error;return usage('Cannot read input file');}
  finally{await file?.close();}
}
function json(text:string): any {try{return JSON.parse(text);}catch{usage('Invalid JSON input');}}

const displaySecret = /^(?:authorization|bearer|execution[_-]?token|worker[_-]?token|x-forge-execution|token|execution[_-]?id|attempt[_-]?id)$/i;
const displayRecord = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === 'object' && !Array.isArray(value);
function displayScalar(value: unknown): string {
  if (value === null || value === undefined) return '—';
  if (typeof value === 'string') return value.replace(/Bearer\s+[^\s"\\]+/gi, 'Bearer [REDACTED]').replaceAll('\n', '↵');
  if (typeof value === 'boolean' || typeof value === 'number') return String(value);
  return JSON.stringify(value);
}
function displayEntries(value: Record<string, unknown>): [string, unknown][] {
  return Object.entries(value).filter(([key]) => !displaySecret.test(key)).sort(([a], [b]) => a.localeCompare(b));
}
function renderTable(values: unknown[]): string[] {
  const rows = values.filter(displayRecord).map((value) => displayEntries(value));
  if (!rows.length || rows.some((row) => row.some(([, child]) => child !== null && typeof child === 'object'))) return [];
  const keys = [...new Set(rows.flatMap((row) => row.map(([key]) => key)))];
  if (!keys.length) return [];
  const cells = rows.map((row) => {
    const map = new Map(row);
    return keys.map((key) => displayScalar(map.get(key)));
  });
  const widths = keys.map((key, index) => Math.min(48, Math.max(key.length, ...cells.map((row) => row[index].length))));
  const fit = (value: string, width: number) => value.length > width ? `${value.slice(0, Math.max(0, width - 1))}…` : value.padEnd(width);
  return [
    `  ${keys.map((key, index) => fit(key, widths[index])).join('  ')}`,
    `  ${widths.map((width) => '-'.repeat(width)).join('  ')}`,
    ...cells.map((row) => `  ${row.map((cell, index) => fit(cell, widths[index])).join('  ')}`),
  ];
}
function renderStructured(value: unknown, indent = '  '): string[] {
  if (Array.isArray(value)) {
    const table = renderTable(value);
    if (table.length) return table;
    return value.flatMap((child, index) => [`${indent}[${index}]`, ...renderStructured(child, `${indent}  `)]);
  }
  if (displayRecord(value)) {
    return displayEntries(value).flatMap(([key, child]) => {
      if (child !== null && typeof child === 'object') return [`${indent}${key}:`, ...renderStructured(child, `${indent}  `)];
      return [`${indent}${key}: ${displayScalar(child)}`];
    });
  }
  return [`${indent}${displayScalar(value)}`];
}
function renderHuman(value: unknown, words: string[]): string {
  const title = words.length ? `Forge ${words.join(' ')}` : 'Forge';
  return [title, ...renderStructured(value), ''].join('\n');
}

export async function main(argv: string[]): Promise<number> {
  let controller: Controller | undefined;
  try {
    let parsed;
    try {
      parsed=parseArgs({args:argv,allowPositionals:true,options:Object.fromEntries([...stringFlags.map(k=>[k,{type:'string' as const}]),...boolFlags.map(k=>[k,{type:'boolean' as const}])])});
    } catch {usage('Invalid arguments; run forge --help');}
    const f=parsed.values as Record<string,string|boolean|undefined>;
    const rawWords=parsed.positionals;
    const humanMode=rawWords[0]==='human';
    const words=humanMode?rawWords.slice(1):rawWords;
    const format=f.format===undefined?(f.json===true?'json':undefined):String(f.format);
    if(format!==undefined && !['human','json'].includes(format))usage('--format must be human or json');
    if(f.json===true && f.format!==undefined && format!=='json')usage('Choose --json or --format json');
    if(!humanMode && (f.json===true || f.format!==undefined))usage('--json/--format is only supported with forge human');
    if(Object.entries(f).filter(([key,value])=>key.endsWith('-file') && !key.endsWith('token-file') && value==='-').length>1)usage('Only one input field may consume stdin');
    if(f.version){process.stdout.write('collab-forge client 0.1.0\n');return 0;}
    if(f.help || !words.length){process.stdout.write(humanMode?HUMAN_HELP:HELP);return 0;}
    const common=['url','worker-token-file','socket','session-id','ttl','format','json'];
    function flags(...allowed:string[]){if(Object.keys(f).some(key=>![...common,...allowed].includes(key)))usage('Option not supported by this command');}
    function count(n:number){if(words.length!==n)usage('Incorrect positional arguments; run forge --help');}
    async function fields(allowed:string[]): Promise<Record<string,any>> {
      flags('data','data-file',...allowed);
      if(f.data!==undefined && f['data-file']!==undefined)usage('Choose --data or --data-file');
      const value=f.data!==undefined ? json(String(f.data)) : f['data-file']!==undefined ? json(await textFile(String(f['data-file']))) : {};
      if(!value || typeof value!=='object'||Array.isArray(value))usage('Data must be a JSON object');
      for(const name of allowed) {
        if(name.endsWith('-file') || f[name]===undefined)continue;
        const key=name.replaceAll('-','_');
        if(key in value)usage('Named fields cannot overwrite --data fields');
        value[key]=f[name];
      }
      for(const name of allowed.filter(k=>k.endsWith('-file'))) if(f[name]!==undefined) {
        const key=name.slice(0,-5).replaceAll('-','_');
        if(key in value)usage('Choose an inline field or its file');
        value[key]=await textFile(String(f[name]));
      }
      if(typeof value.evidence==='string')value.evidence=json(value.evidence);
      for(const key of ['limit','depth','after_id','max_pages']) if(key in value && typeof value[key]==='string' && f[key.replaceAll('_','-')]!==undefined)value[key]=integer(value[key],key==='after_id'?0:1,key==='depth'?10:key==='max_pages'?1000:key==='after_id'?Number.MAX_SAFE_INTEGER:1000);
      return value;
    }
    function ref(body:Record<string,any>,index=2){if('ref' in body && body.ref!==words[index])usage('Conflicting ref');body.ref=words[index];return body;}
    const env={...process.env};
    if(f.url!==undefined)env.FORGE_URL=String(f.url);
    if(f['worker-token-file']!==undefined)env.FORGE_WORKER_TOKEN_FILE=String(f['worker-token-file']);
    if(f.ttl!==undefined)env.FORGE_TTL_SECONDS=String(f.ttl);
    if(f.socket==='')usage('--socket must be a nonempty absolute path');
    const socket=String(f.socket ?? env.FORGE_SOCKET ?? '');
    if(socket && !['admin','skill'].includes(words[0]) && !(words[0]==='session' && words[1]==='start') && ['url','worker-token-file','ttl','session-id'].some(key=>f[key]!==undefined))usage('Existing session configuration is immutable; set connection options on session start');
    if(words[0]==='admin' && f.socket!==undefined)usage('Admin uses its own URL and credential, not a worker session socket');
    const sessionId=String(f['session-id'] ?? randomUUID());
    let http:ClientHTTP|undefined;
    async function worker() {
      if(!controller){
        try {
          const config=await loadConfig(env);
          const nextController=new Controller(config,{sessionId});
          const nextHTTP=new ClientHTTP(config.url,config.workerToken,sessionId);
          controller=nextController;
          http=nextHTTP;
        } catch (error) {
          if(error instanceof ForgeError) throw error;
          throw new ForgeError('configuration','Forge client configuration is invalid; check FORGE_URL, FORGE_WORKER_TOKEN_FILE, and FORGE_TTL_SECONDS');
        }
      }
      return controller;
    }
    async function extra(op:string,params:unknown):Promise<any> {
      await worker();
      if(op==='project')return http!.request('/forge/v1/project');
      if(op==='health')return http!.request('/health');
      if(op==='issue_timeline')return http!.request('/forge/v1/tools/issue_timeline','POST',params);
      usage('Unsupported read operation');
    }
    async function call(op:string,params:unknown={}):Promise<any> {
      if(!Object.hasOwn(toolSchemas,op) && !['issue_timeline','project'].includes(op))usage('Unknown worker operation');
      // A supervised harness owns its instance capability and stores execution
      // state in forged, so its ordinary commands route through the daemon
      // facade instead of any inherited legacy socket.
      const harness = env.FORGE_CODEX_INSTANCE_ID ? 'Codex' : env.FORGE_CLAUDE_INSTANCE_ID ? 'Claude' : undefined;
      if(harness && (op.startsWith('issue_')||op.startsWith('checkout'))){
        if(f.socket!==undefined||f['session-id']!==undefined)usage(harness+' identity is harness-owned; do not override socket/session');
        const tool = env.FORGE_CODEX_INSTANCE_ID ? codexTool : claudeTool;
        try{return await tool(op,params,env);}catch(e){if(e instanceof ForgeError)throw e;throw new ForgeError(String((e as any)?.code??'harness_error'),String((e as any)?.message??'Harness operation failed'),(e as any)?.status??0,(e as any)?.ambiguous===true,{hint:(e as any)?.hint,data:(e as any)?.data});}
      }
      if(socket)return requestSession(socket,{op,params});
      if(['checkout','issue_claim','issue_renew','issue_release','issue_close'].includes(op))usage('Execution requires a live session: forge session start, then --socket PATH');
      if(op==='issue_timeline'||op==='project')return extra(op,params);
      return (await worker()).execute(op as ToolName,params);
    }
    const output=(result:unknown)=>process.stdout.write(humanMode && format!=='json' ? renderHuman(result,words) : JSON.stringify(result)+'\n');
    if(words[0]==='skill') {
      const source=fileURLToPath(new URL('../skills/collab-forge-client/',import.meta.url));
      if(words[1]==='path'){count(2);flags();output({path:source});return 0;}
      if(words[1]!=='install')usage('Use skill path or skill install');
      count(2);flags('target');if(!f.target)usage('--target is required');
      const target=resolve(String(f.target));
      await mkdir(dirname(target),{recursive:true});
      // mkdir is exclusive: existing installations/symlinks are never overwritten.
      try { await mkdir(target); }
      catch(error) { if((error as NodeJS.ErrnoException).code==='EEXIST')throw new ForgeError('skill_exists','Skill target already exists; inspect or back it up instead of overwriting');throw error; }
      for(const name of await readdir(source)) await cp(join(source,name),join(target,name),{recursive:true,errorOnExist:true,force:false});
      output({installed:target});return 0;
    }
    if(words[0]==='session') {
      if(env.FORGE_CODEX_INSTANCE_ID)usage('Codex uses daemon execution state; use forge codex status/retry/pause');
      if(env.FORGE_CLAUDE_INSTANCE_ID)usage('Claude uses daemon execution state; use forge claude status/retry/pause');
      count(2);flags();
      const action=words[1];
      if(action==='start') {
        const path=socket || join('/tmp',`forge-client-${process.getuid?.() ?? 'user'}`,randomUUID()+'.sock');
        await mkdir(dirname(path),{recursive:true,mode:0o700});
        const active=await worker();
        const server=await startSession(path,active,extra);
        const stop=()=>{void server.close();};
        process.once('SIGINT',stop);process.once('SIGTERM',stop);
        output({socket:path,sessionID:sessionId,protocol:1});
        try{await server.done;}finally{process.removeListener('SIGINT',stop);process.removeListener('SIGTERM',stop);await server.close();}
        return 0;
      }
      if(!socket)usage('--socket or FORGE_SOCKET is required');
      const operations:Record<string,string>={status:'state',guard:'guard',context:'context',retry:'retry',stop:'shutdown'};
      if(action==='wait') {
        const end=Date.now()+15_000;
        do {
          try { output(await requestSession(socket,{op:'state'})); return 0; }
          catch (error) {
            if (!(error instanceof ForgeError) || !['session_unavailable','session_timeout'].includes(error.code)) throw error;
            await delay(100);
          }
        } while(Date.now()<end);
        throw new ForgeError('unavailable','Session did not become ready within 15 seconds');
      }
      if(!operations[action])usage('Unknown session action');
      const result=await requestSession(socket,{op:operations[action]});output(result);
      return action==='guard' && !result.allowed ? 4 : 0;
    }
    if(words[0]==='health'){
      count(1);flags();
      if(socket){output(await requestSession(socket,{op:'health'}));return 0;}
      let url:string;
      try { url=validateURL(env.FORGE_URL??'http://127.0.0.1:7347'); }
      catch { throw new ForgeError('configuration','FORGE_URL must be an HTTP(S) loopback IP literal origin'); }
      output(await new ClientHTTP(url,'',sessionId).request('/health'));return 0;
    }
    if(words[0]==='project'){count(1);flags();output(await call('project'));return 0;}
    if(words[0]==='admin') {
      // Do not accept a worker credential as an implicit supervisor fallback.
      const tokenFile=f['admin-token-file'] ?? env.FORGE_ADMIN_TOKEN_FILE;
      if(!tokenFile)usage('Admin commands require --admin-token-file or FORGE_ADMIN_TOKEN_FILE');
      delete f['admin-token-file'];
      let config;
      try {config=await loadConfig({...env,FORGE_WORKER_TOKEN_FILE:String(tokenFile)});}catch{throw new ForgeError('credential','Cannot read supervisor credential: require an owned regular 0600 file');}
      const admin=new ClientHTTP(config.url,config.workerToken,sessionId);
      const action=words[1];let result;
      if(action==='request') {
        count(4);const body=await fields(['confirm','if-match','idempotency-key']);const confirmation=body.confirm,match=body.if_match,key=body.idempotency_key;delete body.confirm;delete body.if_match;delete body.idempotency_key;
        if(!words[3].startsWith('/api/v1/'))usage('Admin request requires /api/v1/ path');
        const hasBody=f.data!==undefined || f['data-file']!==undefined || Object.keys(body).length>0;
        if(words[2]==='GET' && hasBody)usage('GET requests use query parameters, not a JSON body');
        result=await admin.request(words[3],words[2],hasBody?body:undefined,confirmation,match,key);
      } else {
        const project=await admin.request('/forge/v1/project');
        const p=`/api/v1/projects/${project.project.id}`;
        const path=p+'/issues/'+encodeURIComponent(words[2]??'');
        if(action==='create'){count(2);result=await admin.request(p+'/issues','POST',await fields(['title','body','body-file']));}
        else if(action==='list'){count(2);const b=await fields(['status','limit']);result=await admin.request(p+'/issues?'+new URLSearchParams(b));}
        else if(action==='get'){count(3);flags();result=await admin.request(path);}
        else if(action==='comment'){count(3);result=await admin.request(path+'/comments','POST',await fields(['body','body-file']));}
        else if(action==='link'){count(3);result=await admin.request(path+'/links','POST',await fields(['type','to-ref']));}
        else if(action==='update') {
          count(3);const b=await fields(['title','body','body-file','owner','clear-owner','priority']);
          if(b.clear_owner){if('owner' in b)usage('Choose owner or clear-owner');b.owner='';}delete b.clear_owner;
          if('priority' in b){if('set_priority'in b||'clear_priority'in b)usage('Conflicting priority');if(b.priority==='none')b.clear_priority=true;else b.set_priority=integer(String(b.priority),0,4);delete b.priority;}
          if(!Object.keys(b).length)usage('Update requires changed fields');
          result=await admin.request(path,'PATCH',b);
        } else if(['reopen','delete','force-release','close'].includes(action)) {
          count(3);const allowed=action==='delete'?['confirm']:action==='force-release'?['reason']:action==='close'?['reason','message','message-file','evidence','evidence-file','if-match','idempotency-key']:[];
          const b=await fields(allowed);const confirmation=b.confirm,match=b.if_match,key=b.idempotency_key;delete b.confirm;delete b.if_match;delete b.idempotency_key;
          if(action==='delete' && typeof confirmation!=='string')usage('Delete requires explicit --confirm');
          b.actor='Human';
          if(action==='close' && key && !b.retry_protocol)b.retry_protocol='close-v1';
          result=await admin.request(path+(action==='force-release'?'/lease/actions/force_release':'/actions/'+action),'POST',b,confirmation,match,key);
        } else if(action==='label') {
          count(5);flags();if(!['add','remove'].includes(words[2]))usage('Use label add or remove');
          const labelPath=p+'/issues/'+encodeURIComponent(words[3])+'/labels';
          result=words[2]==='add'?await admin.request(labelPath,'POST',{label:words[4]}):await admin.request(labelPath+'/'+encodeURIComponent(words[4])+'?actor=Human','DELETE');
        } else if(action==='unlink'){count(4);flags();integer(words[3],1,Number.MAX_SAFE_INTEGER);result=await admin.request(path+'/links/'+words[3]+'?actor=Human','DELETE');}
        else usage('Unknown admin command');
      }
      output(result);return 0;
    }
    if(words[0]!=='issue' && words[0]!=='tool')usage('Unknown command; run forge --help');
    const op=words[0]==='tool'?words[1]:'issue_'+words[1];
    let body:Record<string,any>;
    if(words[0]==='tool'){count(2);body=await fields([]);}
    else {
      const allowed:Record<string,string[]>={issue_list:['status','limit'],issue_create:['title','body','body-file'],issue_get:[],issue_graph:['depth'],issue_comment:['body','body-file'],issue_link:['type','to-ref'],issue_claim:['purpose'],issue_renew:[],issue_release:['reason'],issue_close:['reason','message','message-file','evidence','evidence-file','if-match'],issue_timeline:['after-id','limit','all','max-pages']};
      if(!allowed[op])usage('Unknown issue command');
      count(['issue_list','issue_create'].includes(op)?2:3);
      body=await fields(allowed[op]);
      if(!['issue_list','issue_create'].includes(op))ref(body);
    }
    if(Object.hasOwn(toolSchemas,op)) validateParams(op as ToolName,body);
    if(op==='issue_timeline') {
      if('all' in body && typeof body.all!=='boolean')usage('all must be boolean');
      const all=body.all===true;const max=body.max_pages??100;delete body.all;delete body.max_pages;
      if(!Number.isInteger(max)||max<1||max>1000)usage('max-pages must be 1..1000');
      let result:any;const events:unknown[]=[];let full=false;
      for(let i=0;i<(all?max:1);i++) {
        result=await call(op,body);events.push(...(result.events??[]));
        if(Buffer.byteLength(JSON.stringify(events))>8_388_608)throw new ForgeError('response_limit','Timeline exceeds 8 MiB; request individual pages without --all');
        full=result.scanned_event_count===(body.limit??100) && !result.reset_required;
        if(result.reset_required || !all || !full)break;
        if(typeof result.next_after_id!=='number'||result.next_after_id<=(body.after_id??0))throw new ForgeError('invalid_cursor','Timeline did not advance');
        body.after_id=result.next_after_id;
      }
      output({...result,events,truncated:full});return 0;
    }
    output(await call(op,body));return 0;
  } catch(error) {
    const e=error instanceof ForgeError?error:new ForgeError('client_error','Client operation failed; check configuration, input and session availability');
    process.stderr.write(JSON.stringify(forgeErrorEnvelope(e))+'\n');
    return e.ambiguous?3:e.code==='usage'?2:1;
  } finally {await controller?.shutdown('cli_exit');}
}
