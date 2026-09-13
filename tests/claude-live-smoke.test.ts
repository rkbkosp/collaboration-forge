import test from 'node:test';
import assert from 'node:assert/strict';
import { spawn, type ChildProcess } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { setTimeout as delay } from 'node:timers/promises';
import { startForge } from './harness.ts';

const forge = fileURLToPath(new URL('../scripts/forge.mjs', import.meta.url));
const node = process.execPath;
const quote = (value: string) => "'" + value.replaceAll("'", "'\\''") + "'";

/**
 * Opt-in checks against the real Claude Code binary. Ordinary CI must not depend
 * on a live model, so these only run with FORGE_REAL_CLAUDE=1. The adapter's own
 * contract is covered without a model by tests/claude-e2e.test.ts.
 */
const live = process.env.FORGE_REAL_CLAUDE === '1';
const LIVE_TIMEOUT = 300_000;

interface LiveFixture {
  url: string;
  dataDir: string;
  workerToken: string;
  adminToken: string;
  projectID: number;
  admin: (path: string) => Promise<any>;
  close: () => Promise<void>;
  env: NodeJS.ProcessEnv;
  issue: () => Promise<any>;
}

async function liveFixture(): Promise<LiveFixture> {
  const f = await startForge();
  const issue = async () => (await f.admin(`/api/v1/projects/${f.projectID}/issues`, { title: 'Real Claude live fixture' })).issue;
  return {
    ...f,
    issue,
    env: {
      ...process.env,
      FORGE_URL: f.url,
      FORGE_WORKER_TOKEN_FILE: f.dataDir + '/worker-token',
      FORGE_CLAUDE_BINARY: 'claude',
      FORGE_SOCKET: '',
      FORGE_ADMIN_TOKEN_FILE: '',
    },
  };
}

/** Run one bounded print-mode Claude turn through the real launcher. */
function runLive(f: LiveFixture, allowedTools: string, prompt: string): Promise<{ code: number | null; output: string; errors: string; child: ChildProcess }> {
  // --allowedTools is variadic, so it must use the `=` form or it would consume
  // the positional prompt. The launcher injects --plugin-dir itself.
  const child = spawn(node, [forge, 'claude', '-p', `--allowedTools=${allowedTools}`, prompt], { env: f.env, stdio: ['ignore', 'pipe', 'pipe'] });
  let output = '';
  let errors = '';
  child.stdout!.on('data', (chunk) => { output += chunk; });
  child.stderr!.on('data', (chunk) => { errors += chunk; });
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => { child.kill('SIGTERM'); reject(new Error('real Claude timeout')); }, LIVE_TIMEOUT - 20_000);
    child.once('error', () => { clearTimeout(timer); reject(new Error('real Claude launch failure')); });
    child.once('exit', (code) => { clearTimeout(timer); resolve({ code, output, errors, child }); });
  });
}

function assertNoCredential(f: LiveFixture, ...streams: string[]): void {
  for (const stream of streams) {
    assert.equal(stream.includes(f.workerToken) || stream.includes(f.adminToken), false, 'credential disclosure');
  }
}

test('real Claude Code claims and closes through the Forge launcher', { skip: !live, timeout: LIVE_TIMEOUT }, async () => {
  const f = await liveFixture();
  try {
    const issue = await f.issue();
    const close = JSON.stringify({
      reason: 'audit-no-change',
      message: 'This generated smoke fixture verifies real Claude Code and Forge lifecycle; no product changes are required.',
      evidence: [{ type: 'no-change-audit', rationale: 'This is an ephemeral automated integration fixture, not a product change request.' }],
    });
    const command = `${quote(node)} ${quote(forge)} issue claim ${issue.uid} && ${quote(node)} ${quote(forge)} issue close ${issue.uid} --data ${quote(close)}`;
    const prompt = 'This is a bounded integration smoke test. Do not modify files, inspect credentials, spawn subagents, or perform other work. Use a shell tool to execute exactly this command, then reply SMOKE_OK if it succeeds; otherwise report the error code and stop. ' + command;

    const { code, output, errors } = await runLive(f, 'Bash', prompt);
    assertNoCredential(f, output, errors);
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

test('real Claude records Claude attribution without exposing private values', { skip: !live, timeout: LIVE_TIMEOUT }, async () => {
  const f = await liveFixture();
  let child: ChildProcess | undefined;
  try {
    const issue = await f.issue();
    const prompt = 'Do not modify files, inspect credentials, spawn subagents, release, close or comment. Use a shell tool to run exactly this one command: '
      + `${quote(node)} ${quote(forge)} issue claim ${issue.uid} --purpose live-attribution-probe`
      + '. Then stop and reply PROBE_DONE without taking any other Forge action.';

    // Sample the lease while the session is alive: a normal exit releases it, so
    // the purpose is only observable during the run.
    let purpose: string | null = null;
    const running = runLive(f, 'Bash', prompt);
    running.then((r) => { child = r.child; }).catch(() => {});
    for (let attempt = 0; attempt < 90 && purpose === null; attempt++) {
      await delay(1000);
      const current = await f.admin(`/api/v1/projects/${f.projectID}/issues/${issue.uid}`).catch(() => null);
      if (current?.lease?.purpose) purpose = current.lease.purpose;
    }
    const { code, output, errors } = await running;
    assertNoCredential(f, output, errors);
    assert.equal(code, 0, 'real Claude must complete successfully');

    assert.ok(purpose, 'no live lease purpose was observed');
    // The harness name plus session/thread/instance is the observable attribution.
    assert.match(purpose!, /^Claude\//);
    assert.match(purpose!, /\[Claude session .+, thread main, instance .+\]/);
    // A model-visible or event-recorded purpose never carries execution plumbing.
    assert.equal(/execution:|attempt|token|claim_uid/i.test(purpose!), false, 'private value in attribution');

    // The strict Stop hook must have intervened rather than silently allowing a
    // stop with work held. The wording is the model's own summary of it, so match
    // the invariant rather than one phrasing.
    assert.match(output + errors, /stop[- ]hook|forge claude pause/i, 'strict Stop did not intervene on held work');
    // And it must never have pointed this Claude session at another harness.
    assert.equal(/forge codex (retry|pause)/.test(output + errors), false, 'Claude session was told to run Codex commands');
  } finally {
    if (child && child.exitCode === null && child.signalCode === null) child.kill('SIGKILL');
    await f.close();
  }
});

test('real Claude subagent is isolated from the main agent tenure', { skip: !live, timeout: LIVE_TIMEOUT }, async () => {
  const f = await liveFixture();
  try {
    const issue = await f.issue();
    // The main agent claims and comments first, so the subagent's comment has a
    // same-session baseline to differ from.
    const prompt = [
      'Do exactly this and nothing else. Use the Task tool to spawn ONE subagent with Bash access.',
      `First, claim the issue and record your own comment with Bash calls: ${quote(node)} ${quote(forge)} issue claim ${issue.uid} && ${quote(node)} ${quote(forge)} issue comment ${issue.uid} --body main-probe`,
      'Then give the subagent this task, verbatim:',
      `"Run exactly these two Bash commands and report both raw JSON outputs verbatim, nothing else:`,
      `(1) ${quote(node)} ${quote(forge)} issue comment ${issue.uid} --body subagent-probe`,
      `(2) ${quote(node)} ${quote(forge)} issue claim ${issue.uid}"`,
      'After the subagent returns, reply with its raw outputs inside <SUBAGENT_OUTPUT> tags. Do not release or close anything.',
    ].join(' ');

    // Sample the tenure while the session is alive: a normal exit releases it, so
    // a post-run read cannot tell us which execution held it.
    const observed = new Set<string>();
    const running = runLive(f, 'Bash,Task', prompt);
    for (let attempt = 0; attempt < 60; attempt++) {
      const current = await f.admin(`/api/v1/projects/${f.projectID}/issues/${issue.uid}`).catch(() => null);
      if (current?.lease?.purpose) observed.add(current.lease.purpose);
      if (current?.comments?.some((c: any) => c.body === 'subagent-probe')) break;
      await delay(500);
    }
    const { code, output, errors } = await running;
    assertNoCredential(f, output, errors);
    if (code !== 0) process.stderr.write(JSON.stringify({ real_claude_exit: code, tail: (output + errors).slice(-600) }) + '\n');
    assert.equal(code, 0, 'real Claude must complete successfully');

    // Verify the daemon's own record, not the model's summary of it.
    const after = await f.admin(`/api/v1/projects/${f.projectID}/issues/${issue.uid}`);
    const mainComment = after.comments.find((c: any) => c.body === 'main-probe');
    const subComment = after.comments.find((c: any) => c.body === 'subagent-probe');
    assert.ok(subComment, 'the subagent comment was not recorded');
    // Both share one Claude session, so they share one session actor prefix.
    assert.match(subComment.author, /^Claude\//);
    assert.match(subComment.author, /^Claude\/[^ ]+$/, 'subagent author is not a Claude session actor');
    // But the subagent is a distinct execution identity, not the main agent.
    assert.notEqual(subComment.author, mainComment?.author ?? '', 'the subagent contributed as the main agent');
    // And no execution the subagent started ever held the tenure: every lease
    // observed during the run belonged to the main agent.
    assert.ok(observed.size > 0, 'no live tenure was observed');
    for (const purpose of observed) {
      assert.match(purpose, /thread main/, `a non-main execution held the tenure: ${purpose}`);
    }
  } finally { await f.close(); }
});
