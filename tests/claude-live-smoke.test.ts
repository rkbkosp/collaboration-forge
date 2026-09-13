import test from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { startForge } from './harness.ts';

const forge = fileURLToPath(new URL('../scripts/forge.mjs', import.meta.url));

/**
 * Opt-in end-to-end check against the real Claude Code binary. Ordinary CI must
 * not depend on a live model, so this only runs with FORGE_REAL_CLAUDE=1.
 */
test('real Claude Code claims and closes through the Forge launcher', { skip: process.env.FORGE_REAL_CLAUDE !== '1', timeout: 300_000 }, async () => {
  const f = await startForge();
  try {
    const issue = (await f.admin(`/api/v1/projects/${f.projectID}/issues`, { title: 'Real Claude lifecycle smoke fixture' })).issue;
    const close = JSON.stringify({
      reason: 'audit-no-change',
      message: 'This generated smoke fixture verifies real Claude Code and Forge lifecycle; no product changes are required.',
      evidence: [{ type: 'no-change-audit', rationale: 'This is an ephemeral automated integration fixture, not a product change request.' }],
    });
    const quote = (value: string) => "'" + value.replaceAll("'", "'\\''") + "'";
    const command = `${quote(process.execPath)} ${quote(forge)} issue claim ${issue.uid} && ${quote(process.execPath)} ${quote(forge)} issue close ${issue.uid} --data ${quote(close)}`;
    const prompt = 'This is a bounded integration smoke test. Do not modify files, inspect credentials, spawn subagents, or perform other work. Use a shell tool to execute exactly this command, then reply SMOKE_OK if it succeeds; otherwise report the error code and stop. ' + command;
    const env = {
      ...process.env,
      FORGE_URL: f.url,
      FORGE_WORKER_TOKEN_FILE: f.dataDir + '/worker-token',
      FORGE_CLAUDE_BINARY: 'claude',
      FORGE_SOCKET: '',
      FORGE_ADMIN_TOKEN_FILE: '',
    };
    // -p prints a single turn; the plugin is injected by the launcher itself.
    const child = spawn(process.execPath, [forge, 'claude', '-p', '--allowedTools', 'Bash', prompt], { env, stdio: ['ignore', 'pipe', 'pipe'] });
    let output = '';
    let errors = '';
    child.stdout.on('data', (chunk) => { output += chunk; });
    child.stderr.on('data', (chunk) => { errors += chunk; });
    const code = await new Promise<number | null>((resolve, reject) => {
      const timer = setTimeout(() => { child.kill('SIGTERM'); reject(new Error('real Claude timeout')); }, 280_000);
      child.once('error', () => { clearTimeout(timer); reject(new Error('real Claude launch failure')); });
      child.once('exit', (value) => { clearTimeout(timer); resolve(value); });
    });

    // No credential may appear in either stream.
    assert.equal(output.includes(f.workerToken) || output.includes(f.adminToken) || errors.includes(f.workerToken) || errors.includes(f.adminToken), false, 'credential disclosure');
    if (code !== 0) process.stderr.write(JSON.stringify({ real_claude_exit: code, auth_error: /auth|unauthorized|login/i.test(errors), plugin_error: /plugin/i.test(errors) }) + '\n');
    assert.equal(code, 0, 'real Claude must complete successfully');

    const current = await f.admin(`/api/v1/projects/${f.projectID}/issues/${issue.uid}`);
    if (current.issue.status !== 'closed') {
      process.stderr.write(JSON.stringify({ forge_error_codes: [...output.matchAll(/claude_[a-z_]+/g)].map((match) => match[0]).slice(0, 10), saw_smoke_ok: output.includes('SMOKE_OK') }) + '\n');
    }
    assert.equal(current.issue.status, 'closed');
    assert.equal(current.lease == null, true);
    assert.ok(output.includes('SMOKE_OK'));
  } finally { await f.close(); }
});
