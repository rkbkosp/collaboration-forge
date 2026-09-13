/**
 * A fake Claude child, launched by the real `forge claude` supervisor.
 *
 * It drives the same code paths a Claude Code session would: it runs the real
 * plugin hooks through handleHook, applies the environment preamble Claude runs
 * before each Bash command, and then issues ordinary `forge` CLI commands. The
 * launcher injects `--plugin-dir` ahead of these arguments, which a script
 * receives as plain argv.
 */
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { mkdtemp, readFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createInterface } from 'node:readline';
import { handleHook } from '../../claude/hook.ts';

const exec = promisify(execFile);
const forge = fileURLToPath(new URL('../../scripts/forge.mjs', import.meta.url));
const SESSION_ID = '11111111-1111-4111-8111-111111111111';
const emit = (value: any) => process.stdout.write(JSON.stringify(value) + '\n');

/**
 * Consume the arguments the supervisor supplied, the way Claude Code itself
 * would: `--plugin-dir` takes a value, and the remaining positional is the
 * fixture's issue reference. This also asserts the injection actually happened.
 */
function issueRef(argv: string[]): string {
  const positional: string[] = [];
  let injected = false;
  for (let index = 0; index < argv.length; index++) {
    const arg = argv[index];
    if (arg === '--plugin-dir') { injected = true; index++; continue; }
    if (arg.startsWith('--plugin-dir=')) { injected = true; continue; }
    positional.push(arg);
  }
  if (!injected) throw new Error('expected the supervisor to inject the bundled plugin directory');
  if (positional.length !== 1) throw new Error('expected exactly one issue reference');
  return positional[0];
}
const ref = issueRef(process.argv.slice(2));

async function forgeCLI(args: string[], env: NodeJS.ProcessEnv) {
  const { stdout } = await exec(process.execPath, [forge, ...args], { env, timeout: 25_000 });
  return JSON.parse(stdout);
}

/** Claude Code runs the CLAUDE_ENV_FILE contents as a preamble before each Bash call. */
async function applyEnvPreamble() {
  const file = process.env.CLAUDE_ENV_FILE;
  if (!file) return;
  let text = '';
  try { text = await readFile(file, 'utf8'); } catch { return; }
  for (const line of text.split('\n')) {
    const match = /^export ([A-Z0-9_]+)='(.*)'$/.exec(line.trim());
    if (match) process.env[match[1]] = match[2];
  }
}

// The real SessionStart hook, exactly as the plugin ships it.
process.env.CLAUDE_ENV_FILE = join(process.env.FORGE_CLAUDE_ENV_DIR ?? await mkdtemp(join(tmpdir(), 'forge-claude-env-')), 'env.sh');
await handleHook({ hook_event_name: 'SessionStart', session_id: SESSION_ID, source: 'startup' });
await applyEnvPreamble();

try {
  await forgeCLI(['issue', 'claim', ref], process.env);
  emit({
    ready: true, instance: process.env.FORGE_CLAUDE_INSTANCE_ID,
    tokenFile: process.env.FORGE_CLAUDE_TOKEN_FILE, pid: process.pid,
    session: process.env.FORGE_CLAUDE_SESSION_ID, agent: process.env.FORGE_CLAUDE_AGENT_ID,
  });
} catch (error) {
  emit({ conflict: true, code: (error as any)?.code });
  process.exit(2);
}

const lines = createInterface({ input: process.stdin });
for await (const line of lines) {
  if (line === 'exit') {
    await handleHook({ hook_event_name: 'SessionEnd', session_id: SESSION_ID, reason: 'prompt_input_exit' });
    break;
  }
  if (line === 'close') {
    const result = await forgeCLI(['issue', 'close', ref, '--data', JSON.stringify({
      reason: 'audit-no-change',
      message: 'This generated process lifecycle fixture verifies the Claude adapter end to end without changing product code.',
      evidence: [{ type: 'no-change-audit', rationale: 'Ephemeral automated fixture with no product changes required.' }],
    })], process.env);
    emit({ closed: result.issue.status === 'closed' });
  }
  if (line.startsWith('subagent ')) {
    // A subagent's Bash call: its own PreToolUse binds it, and its shell carries
    // no published agent, exactly as Claude provides it.
    const action = line.split(' ')[1];
    const agent = line.split(' ')[2];
    await handleHook({ hook_event_name: 'PreToolUse', session_id: SESSION_ID, agent_id: agent, tool_name: 'Bash' });
    const subEnv: NodeJS.ProcessEnv = { ...process.env };
    delete subEnv.FORGE_CLAUDE_AGENT_ID;
    if (action === 'comment') {
      // Collaboration needs no lease, so this must succeed.
      try {
        await forgeCLI(['issue', 'comment', ref, '--body', 'subagent attribution probe'], subEnv);
        emit({ subagent_comment: true });
      } catch (error) { emit({ subagent_error: (error as any)?.code }); }
    }
    if (action === 'claim') {
      // The main agent holds this issue, and the subagent is a distinct
      // execution identity, so its acquire must be denied.
      const denied = await forgeCLI(['issue', 'claim', ref], subEnv).then(() => false, () => true);
      emit({ subagent_claim_denied: denied });
    }
    if (action === 'close') {
      // No lease for this identity: a strict close must be refused outright.
      const refused = await forgeCLI(['issue', 'close', ref, '--data', JSON.stringify({
        reason: 'audit-no-change', message: 'Subagent must not close work held by the main agent.',
        evidence: [{ type: 'no-change-audit', rationale: 'Negative fixture.' }],
      })], subEnv).then(() => false, () => true);
      emit({ subagent_close_refused: refused });
    }
  }
  if (line === 'crash') process.kill(process.pid, 'SIGKILL');
}

lines.close();
process.stdin.destroy();
