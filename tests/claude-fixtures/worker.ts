/**
 * A fake Claude child, launched by the real `forge claude` supervisor.
 *
 * It mimics how Claude Code actually runs a Bash tool call: fire the plugin's
 * `PreToolUse` hook for the acting agent, apply the `CLAUDE_ENV_FILE` preamble,
 * then run the command. Modelling that order matters — the binding that
 * `PreToolUse` writes is what attributes the shell, and Claude rewrites it before
 * every call, so a fixture that skips it would test a state the harness never
 * reaches.
 *
 * The launcher injects `--plugin-dir` ahead of these arguments, which a script
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

/** One Bash tool call as `agent` (omitted for the main agent). */
async function bash(args: string[], agent?: string) {
  await handleHook({ hook_event_name: 'PreToolUse', session_id: SESSION_ID, ...(agent ? { agent_id: agent } : {}), tool_name: 'Bash' });
  await applyEnvPreamble();
  const { stdout } = await exec(process.execPath, [forge, ...args], { env: process.env, timeout: 25_000 });
  return JSON.parse(stdout);
}

// The real SessionStart hook, exactly as the plugin ships it.
process.env.CLAUDE_ENV_FILE = join(process.env.FORGE_CLAUDE_ENV_DIR ?? await mkdtemp(join(tmpdir(), 'forge-claude-env-')), 'env.sh');
await handleHook({ hook_event_name: 'SessionStart', session_id: SESSION_ID, source: 'startup' });

try {
  await bash(['issue', 'claim', ref]);
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
  if (line.startsWith('main ')) {
    const action = line.slice('main '.length);
    if (action === 'close') {
      const result = await bash(['issue', 'close', ref, '--data', JSON.stringify({
        reason: 'audit-no-change',
        message: 'This generated process lifecycle fixture verifies the Claude adapter end to end without changing product code.',
        evidence: [{ type: 'no-change-audit', rationale: 'Ephemeral automated fixture with no product changes required.' }],
      })]);
      emit({ closed: result.issue.status === 'closed' });
    }
  }
  if (line.startsWith('subagent ')) {
    const [action, agent] = line.slice('subagent '.length).split(' ');
    if (action === 'comment') {
      // Collaboration needs no lease, so this must succeed.
      try { await bash(['issue', 'comment', ref, '--body', 'subagent attribution probe'], agent); emit({ subagent_comment: true }); }
      catch (error) { emit({ subagent_error: (error as any)?.code }); }
    }
    if (action === 'claim') {
      // The main agent holds this issue, and the subagent is a distinct execution
      // identity, so its acquire must be denied.
      const denied = await bash(['issue', 'claim', ref], agent).then(() => false, () => true);
      emit({ subagent_claim_denied: denied });
    }
    if (action === 'close') {
      // No lease for this identity: a strict close must be refused outright.
      const refused = await bash(['issue', 'close', ref, '--data', JSON.stringify({
        reason: 'audit-no-change', message: 'Subagent must not close work held by the main agent.',
        evidence: [{ type: 'no-change-audit', rationale: 'Negative fixture.' }],
      })], agent).then(() => false, () => true);
      emit({ subagent_close_refused: refused });
    }
  }
  if (line === 'crash') process.kill(process.pid, 'SIGKILL');
}

lines.close();
process.stdin.destroy();
