import test from 'node:test';
import assert from 'node:assert/strict';
import { spawn, execFile, type ChildProcess } from 'node:child_process';
import { promisify } from 'node:util';
import { once } from 'node:events';
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { startForge } from './harness.ts';
import { claudeTool } from '../claude/facade.ts';
import { launchClaude } from '../claude/launcher.ts';
import { claudeRPC } from '../claude/transport.ts';
import { handleHook, environmentPreamble } from '../claude/hook.ts';

const launcher = fileURLToPath(new URL('../scripts/forge.mjs', import.meta.url));
const { fakeClaude } = await import('./claude-fixtures/bin.mts');

test('real forged: concurrent delayed Bash calls and Stop keep exact actor tenures', { timeout: 30_000 }, async () => {
  const f = await startForge();
  const exec = promisify(execFile);
  const quote = (value: string) => "'" + value.replaceAll("'", "'\\''") + "'";
  const command = (...args: string[]) => [process.execPath, launcher, ...args].map(quote).join(' ');
  try {
    const main = (await f.admin(`/api/v1/projects/${f.projectID}/issues`, { title: 'Main tenure' })).issue;
    const child = (await f.admin(`/api/v1/projects/${f.projectID}/issues`, { title: 'Child tenure' })).issue;
    await launchClaude([], { ...process.env, FORGE_URL: f.url, FORGE_WORKER_TOKEN_FILE: join(f.dataDir, 'worker-token') }, {
      register: (b, e) => claudeRPC('register', b, e),
      end: (b, e) => claudeRPC('end', b, e),
      run: async (_bin, _args, env) => {
        const session = 'binding-regression';
        const bash = async (agent: string, script: string) => {
          const output = await handleHook({ hook_event_name: 'PreToolUse', session_id: session, agent_id: agent, tool_name: 'Bash', tool_input: { command: script } }, env);
          assert.equal(output.hookSpecificOutput?.permissionDecision, undefined);
          const updated = output.hookSpecificOutput?.updatedInput?.command;
          assert.equal(typeof updated, 'string');
          return exec('/bin/bash', ['-c', environmentPreamble(session) + updated], { env });
        };
        await bash('main', command('issue', 'claim', main.uid));
        await bash('child', command('issue', 'claim', child.uid));
        const close = JSON.stringify({ reason: 'audit-no-change', message: 'Generated regression fixture verifies actor isolation without requesting product changes.', evidence: [{ type: 'no-change-audit', rationale: 'Ephemeral local adapter test, no product change requested.' }] });
        // One child shell issues a command after the old TTL. Other agents run
        // meanwhile. It must still be unable to close main's live tenure.
        await Promise.all([
          assert.rejects(bash('child', `sleep 6\n${command('issue', 'close', main.uid, '--data', close)}`), (error: any) => {
            assert.equal(JSON.parse(error.stderr).error.code, 'lease_required');
            return true;
          }),
          bash('main', command('issue', 'comment', main.uid, '--body', 'main-concurrent')),
          bash('sibling', command('issue', 'comment', main.uid, '--body', 'sibling-concurrent')),
        ]);
        const current = await f.admin(`/api/v1/projects/${f.projectID}/issues/${main.uid}`);
        assert.equal(current.issue.status, 'open');
        assert.match(current.lease.purpose, /thread main/);
        const mainStop = await handleHook({ hook_event_name: 'Stop', session_id: session }, env);
        assert.equal(mainStop.decision, 'block');
        assert.ok(mainStop.reason.includes(main.uid));
        await handleHook({ hook_event_name: 'SubagentStop', session_id: session, agent_id: 'child', stop_hook_active: true }, env);
        assert.equal(JSON.parse((await bash('main', command('claude', 'status'))).stdout).paused, false);
        assert.equal(JSON.parse((await bash('child', command('claude', 'status'))).stdout).paused, true);
        await bash('child', command('issue', 'close', child.uid, '--data', close));
        await bash('main', command('issue', 'close', main.uid, '--data', close));
        for (const issue of [main, child]) {
          const after = await f.admin(`/api/v1/projects/${f.projectID}/issues/${issue.uid}`);
          assert.equal(after.issue.status, 'closed');
          assert.equal(after.lease == null, true);
        }
        return { code: 0, signal: null };
      },
    });
  } finally { await f.close(); }
});

/**
 * Real forged, real supervised launcher, real plugin hooks and the real CLI.
 * Only the Claude Code process itself is replaced by a fixture, so this is the
 * adapter's own contract under test rather than the model's behavior.
 */
async function fixture() {
  const f = await startForge();
  const binary = await fakeClaude(join(f.dataDir, 'fake-claude'));
  const children: ChildProcess[] = [];
  const workerPIDs: number[] = [];
  const env = {
    ...process.env,
    FORGE_URL: f.url,
    FORGE_WORKER_TOKEN_FILE: join(f.dataDir, 'worker-token'),
    FORGE_TTL_SECONDS: '60',
    FORGE_CLAUDE_BINARY: binary,
    FORGE_SOCKET: '',
    FORGE_ADMIN_TOKEN_FILE: '',
  };
  const issue = async (title: string) =>
    (await f.admin(`/api/v1/projects/${f.projectID}/issues`, { title })).issue;

  async function start(ref: string) {
    // The supervisor execs the configured binary, so the fake Claude is the
    // child and receives the injected --plugin-dir plus these arguments.
    const child = spawn(process.execPath, [launcher, 'claude', ref], { env, stdio: ['pipe', 'pipe', 'pipe'] });
    children.push(child);
    let buffer = '';
    const lines: any[] = [];
    const listeners: Array<(value: any) => void> = [];
    let stderr = '';
    child.stderr!.on('data', (chunk) => { stderr += chunk; });
    child.stdout!.on('data', (chunk) => {
      buffer += chunk;
      for (;;) {
        const at = buffer.indexOf('\n');
        if (at < 0) break;
        const raw = buffer.slice(0, at);
        buffer = buffer.slice(at + 1);
        // No credential may ever reach the model-visible stream.
        assert.equal(raw.includes(f.workerToken) || raw.includes(f.adminToken), false);
        const value = JSON.parse(raw);
        if (listeners.length) listeners.shift()!(value); else lines.push(value);
      }
    });
    const next = (): Promise<any> => {
      if (lines.length) return Promise.resolve(lines.shift());
      return new Promise((resolve, reject) => {
        const timer = setTimeout(() => reject(new Error('fixture output timeout: ' + stderr.slice(-500))), 20_000);
        listeners.push((value) => { clearTimeout(timer); resolve(value); });
      });
    };
    const ready = await next();
    if (ready.pid) workerPIDs.push(ready.pid);
    return { child, ready, next, stderr: () => stderr };
  }

  async function cleanup() {
    for (const pid of workerPIDs) { try { process.kill(pid, 'SIGKILL'); } catch {} }
    for (const child of children) { if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL'); }
    await f.close();
  }
  return { ...f, env, issue, start, cleanup, binary };
}

test('real forged: claim, subagent attribution, close and normal exit', { timeout: 120_000 }, async (t) => {
  const f = await fixture();
  t.after(() => f.cleanup());
  try {
    const issue = await f.issue('Claude adapter process fixture');
    const a = await f.start(issue.uid);
    assert.equal(a.ready.ready, true, JSON.stringify(a.ready));
    // SessionStart published the canonical hook session id for Bash calls.
    assert.equal(a.ready.session, '11111111-1111-4111-8111-111111111111');
    assert.equal(a.ready.agent, 'main');

    // A subagent is a distinct execution identity sharing the Claude session:
    // it may contribute collaboratively, but it cannot take or close the work
    // the main agent holds.
    a.child.stdin!.write('subagent comment child-1\n');
    assert.deepEqual(await a.next(), { subagent_comment: true });
    a.child.stdin!.write('subagent claim child-1\n');
    assert.deepEqual(await a.next(), { subagent_claim_denied: true });
    a.child.stdin!.write('subagent close child-1\n');
    assert.deepEqual(await a.next(), { subagent_close_refused: true });

    // A second live instance cannot inherit this tenure.
    const b = await f.start(issue.uid);
    assert.equal(b.ready.conflict, true);

    a.child.stdin!.write('main close\n');
    assert.deepEqual(await a.next(), { closed: true });
    const closed = await f.admin(`/api/v1/projects/${f.projectID}/issues/${issue.uid}`);
    assert.equal(closed.issue.status, 'closed');
    assert.equal(closed.lease == null, true, 'close did not release the lease');

    const done = once(a.child, 'exit');
    a.child.stdin!.write('exit\n');
    await done;
  } finally { await f.cleanup(); }
});

test('real forged: the daemon renews a Claude tenure without CLI polling', { timeout: 120_000 }, async (t) => {
  const f = await fixture();
  t.after(() => f.cleanup());
  try {
    const issue = await f.issue('Claude renewal fixture');
    const a = await f.start(issue.uid);
    assert.equal(a.ready.ready, true);
    const get = () => f.admin(`/api/v1/projects/${f.projectID}/issues/${issue.uid}`);
    const first = await get();
    await delay(23_000);
    const renewed = await get();
    assert.ok(Date.parse(renewed.lease.expires_at) > Date.parse(first.lease.expires_at), 'daemon did not renew');
    const done = once(a.child, 'exit');
    a.child.stdin!.write('exit\n');
    await done;
  } finally { await f.cleanup(); }
});

test('real forged: a Claude crash stops renewal and TTL lets a fresh instance claim', { timeout: 150_000 }, async (t) => {
  const f = await fixture();
  t.after(() => f.cleanup());
  let workerPID: number | undefined;
  try {
    const issue = await f.issue('Claude crash fixture');
    const a = await f.start(issue.uid);
    workerPID = a.ready.pid;
    const oldEnv = {
      ...f.env,
      FORGE_CLAUDE_INSTANCE_ID: a.ready.instance,
      FORGE_CLAUDE_TOKEN_FILE: a.ready.tokenFile,
      FORGE_CLAUDE_SESSION_ID: a.ready.session,
      FORGE_CLAUDE_AGENT_ID: 'main',
    };
    const exited = once(a.child, 'exit');
    a.child.kill('SIGKILL');
    await exited;
    await delay(2500);

    // The retired instance capability is refused, and no force-release happened.
    await assert.rejects(claudeTool('issue_release', { ref: issue.uid }, oldEnv));
    const get = () => f.admin(`/api/v1/projects/${f.projectID}/issues/${issue.uid}`);
    const stopped = await get();
    await delay(22_000);
    const later = await get();
    assert.equal(later.lease.expires_at, stopped.lease.expires_at, 'a crash renewed or released the lease');

    await delay(Math.max(0, Date.parse(stopped.lease.expires_at) - Date.now() + 1200));
    const b = await f.start(issue.uid);
    assert.equal(b.ready.ready, true);
    assert.notEqual(b.ready.instance, a.ready.instance);

    // A daemon restart invalidates every previously registered capability.
    await f.restart();
    await assert.rejects(claudeTool('status', {}, { ...oldEnv, FORGE_CLAUDE_INSTANCE_ID: b.ready.instance, FORGE_CLAUDE_TOKEN_FILE: b.ready.tokenFile }));
  } finally {
    if (workerPID) { try { process.kill(workerPID, 'SIGKILL'); } catch {} }
    await f.cleanup();
  }
});
