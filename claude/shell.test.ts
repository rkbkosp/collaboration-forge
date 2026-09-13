import test from 'node:test';
import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { mkdtemp, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { handleHook, environmentPreamble } from './hook.ts';
import { claudeIdentity } from './facade.ts';

const exec = promisify(execFile);
const quote = (s: string) => "'" + s.replaceAll("'", "'\\''") + "'";
const env = { ...process.env, FORGE_CLAUDE_INSTANCE_ID: 'shell-test', FORGE_CLAUDE_SESSION_ID: 'session', FORGE_CLAUDE_AGENT_ID: 'main' };
const probe = `${quote(process.execPath)} --import tsx --input-type=module -e ${quote(`import { claudeIdentity } from ${JSON.stringify(new URL('./facade.ts', import.meta.url).href)}; console.log(JSON.stringify(claudeIdentity()));`)}`;

async function bashInput(command: string, agent: string, shellEnv = env) {
  const input = { command, timeout: 20_000, description: 'identity regression', run_in_background: false };
  const output = await handleHook({ hook_event_name: 'PreToolUse', session_id: 'session', agent_id: agent, tool_name: 'Bash', tool_input: input }, shellEnv, { rpc: async () => ({ active: false }) });
  assert.ok(output.hookSpecificOutput?.updatedInput, 'Bash identity must be bound in its own input');
  assert.equal(output.hookSpecificOutput.permissionDecision, undefined);
  assert.equal(output.hookSpecificOutput.updatedInput.timeout, input.timeout);
  assert.equal(output.hookSpecificOutput.updatedInput.description, input.description);
  assert.equal(output.hookSpecificOutput.updatedInput.run_in_background, false);
  assert.equal(input.command, command, 'hook must not mutate its input');
  return output.hookSpecificOutput.updatedInput.command as string;
}

test('concurrent delayed shells keep their own identity beyond the former binding TTL', { timeout: 20_000 }, async () => {
  const dir = await mkdtemp(join(tmpdir(), 'forge-shell-test-'));
  try {
    const shellEnv = { ...env, FORGE_CLAUDE_TOKEN_FILE: join(dir, 'instance-token') };
    // A leftover file from an older plugin must not influence any new call.
    await writeFile(join(dir, 'agent-binding'), JSON.stringify({ agent: 'wrong', at: Date.now() }));
    const child = await bashInput(`${probe}\nsleep 6\n${probe}`, 'child', shellEnv);
    const main = await bashInput(probe, 'main', shellEnv);
    const sibling = await bashInput(probe, 'sibling', shellEnv);
    const results = await Promise.all([child, main, sibling].map(command => exec('/bin/bash', ['-c', environmentPreamble('session') + command], { env: shellEnv })));
    assert.deepEqual(results.map(r => r.stdout.trim().split('\n').map(line => JSON.parse(line).agent_id)), [['child', 'child'], ['main'], ['sibling']]);
  } finally { await rm(dir, { recursive: true, force: true }); }
});

test('missing shell attribution fails closed, including inherited shell state', async () => {
  assert.throws(() => claudeIdentity({ FORGE_CLAUDE_INSTANCE_ID: 'i', FORGE_CLAUDE_SESSION_ID: 's' }), /identity/i);
  const result = await exec('/bin/bash', ['-c', environmentPreamble('session') + 'test -z "${FORGE_CLAUDE_AGENT_ID:-}"'], { env });
  assert.equal(result.stderr, '');
});

test('Bash identity quoting preserves metacharacters, command text and exit status', async () => {
  const actor = "child'$(false);x";
  const command = await bashInput(`${probe}\nexit 7`, actor);
  await assert.rejects(exec('/bin/bash', ['-c', command], { env }), (error: any) => {
    assert.equal(error.code, 7);
    assert.equal(JSON.parse(error.stdout).agent_id, actor);
    return true;
  });
});
