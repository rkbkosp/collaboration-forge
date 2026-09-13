import test from 'node:test';
import assert from 'node:assert/strict';
import { launchClaude, claudeArguments, claudeEnvironment, CLAUDE_PLUGIN_DIR } from './launcher.ts';
import { handleHook } from './hook.ts';

test('launcher preserves the configured Stop policy through to the child hook', async () => {
  for (const policy of ['strict', 'advisory', undefined]) {
    await launchClaude([], { FORGE_CLAUDE_STOP_POLICY: policy }, {
      register: async () => ({}), end: async () => {},
      run: async (_bin, _args, childEnv) => {
        assert.equal(childEnv.FORGE_CLAUDE_STOP_POLICY, policy);
        const output = await handleHook({ hook_event_name: 'Stop', session_id: 'policy-test' }, childEnv, { rpc: async () => ({ active: true, issue: 'owned' }) });
        assert.equal(output.decision, policy === 'advisory' ? undefined : 'block');
        return { code: 0, signal: null };
      },
    });
  }
});

test('the bundled plugin is injected once and the user\'s own plugin arguments survive', () => {
  assert.deepEqual(claudeArguments(['--resume', 'abc']), ['--plugin-dir', CLAUDE_PLUGIN_DIR, '--resume', 'abc']);
  // An explicitly supplied copy is never added twice, in either spelling.
  for (const args of [
    ['--plugin-dir', CLAUDE_PLUGIN_DIR, '--resume'],
    ['--plugin-dir=' + CLAUDE_PLUGIN_DIR],
  ]) {
    assert.deepEqual(claudeArguments(args), args);
  }
  const other = ['--plugin-dir', '/somewhere/else', '--plugin-dir=' + CLAUDE_PLUGIN_DIR];
  assert.deepEqual(claudeArguments(other), other);
  const foreign = ['--plugin-dir', '/other/plugin'];
  assert.deepEqual(claudeArguments(foreign), ['--plugin-dir', CLAUDE_PLUGIN_DIR, '--plugin-dir', '/other/plugin']);
});

test('inherited authority and runtime state never reach the child', () => {
  const child = claudeEnvironment({
    PATH: '/bin',
    FORGE_SOCKET: '/old.sock',
    FORGE_EXECUTION_SOCKET: '/old-exec.sock',
    FORGE_ADMIN_TOKEN_FILE: '/admin-token',
    FORGE_CODEX_INSTANCE_ID: 'old-codex',
    FORGE_CODEX_TOKEN_FILE: '/old-codex-token',
    FORGE_CLAUDE_INSTANCE_ID: 'stale',
    FORGE_CLAUDE_TOKEN_FILE: '/stale-claude-token',
    FORGE_CLAUDE_SESSION_ID: 'stale-session',
    CODEX_SESSION_ID: 's',
    CODEX_THREAD_ID: 't',
    FORGE_URL: 'http://127.0.0.1:7347',
    FORGE_WORKER_TOKEN_FILE: '/worker-token',
  });
  for (const key of ['FORGE_SOCKET', 'FORGE_EXECUTION_SOCKET', 'FORGE_ADMIN_TOKEN_FILE',
    'FORGE_CODEX_INSTANCE_ID', 'FORGE_CODEX_TOKEN_FILE', 'FORGE_CLAUDE_INSTANCE_ID',
    'FORGE_CLAUDE_TOKEN_FILE', 'FORGE_CLAUDE_SESSION_ID', 'CODEX_SESSION_ID', 'CODEX_THREAD_ID']) {
    assert.equal(child[key], undefined, key + ' survived');
  }
  // The host's endpoint and worker credential are inherited, not replaced.
  assert.equal(child.FORGE_URL, 'http://127.0.0.1:7347');
  assert.equal(child.FORGE_WORKER_TOKEN_FILE, '/worker-token');
  assert.equal(child.PATH?.startsWith('/bin'), true);
});

test('launcher registers a fresh instance, strips inherited execution and ends normally', async () => {
  const calls: any[] = [];
  const env = { PATH: process.env.PATH, FORGE_SOCKET: '/old', FORGE_CLAUDE_INSTANCE_ID: 'old', FORGE_CLAUDE_TOKEN_FILE: '/old-token' };
  const deps = {
    register: async (body: any) => { calls.push(['register', body]); return {}; },
    end: async (body: any) => { calls.push(['end', body]); },
    run: async (bin: string, args: string[], childEnv: NodeJS.ProcessEnv) => {
      assert.equal(bin, 'claude');
      assert.deepEqual(args.slice(0, 3), ['--plugin-dir', CLAUDE_PLUGIN_DIR, 'resume']);
      assert.equal(childEnv.FORGE_SOCKET, undefined);
      assert.notEqual(childEnv.FORGE_CLAUDE_INSTANCE_ID, 'old');
      assert.notEqual(childEnv.FORGE_CLAUDE_TOKEN_FILE, '/old-token');
      assert.equal(calls[0][1].instance_id, childEnv.FORGE_CLAUDE_INSTANCE_ID);
      calls.push(['run', childEnv.FORGE_CLAUDE_INSTANCE_ID]);
      return { code: 0, signal: null };
    },
  };
  assert.equal(await launchClaude(['resume'], env, deps), 0);
  assert.equal(calls[0][0], 'register');
  assert.equal(calls[0][1].pid, process.pid);
  assert.equal(calls.at(-1)[0], 'end');
  assert.equal(calls.at(-1)[1].normal, true);
  assert.equal(calls.at(-1)[1].instance_id, calls[0][1].instance_id);
});

test('FORGE_CLAUDE_BINARY selects the child binary', async () => {
  let launched: string | undefined;
  await launchClaude([], { FORGE_CLAUDE_BINARY: '/opt/claude' }, {
    register: async () => ({}),
    end: async () => {},
    run: async (bin: string) => { launched = bin; return { code: 0, signal: null }; },
  });
  assert.equal(launched, '/opt/claude');
});

test('an abnormal child end retires the instance without requesting a normal release', async () => {
  let ended: any;
  assert.equal(await launchClaude([], {}, {
    register: async () => ({}),
    run: async () => ({ code: null, signal: 'SIGKILL' }),
    end: async (body: any) => { ended = body; },
  }), 1);
  assert.equal(ended.normal, false);
});

test('a failed registration never guesses an end and still removes the capability file', async () => {
  // Registering is itself ambiguous on transport failure: the instance may or
  // may not exist. Sending `end` would assert a fact we do not have, so the
  // launcher deliberately leaves recovery to its own process death and Kata TTL,
  // matching the Codex launcher. No child is launched.
  const { dirname } = await import('node:path');
  const { existsSync } = await import('node:fs');
  let capabilityDir: string | undefined;
  let ran = false;
  let ended = false;
  await assert.rejects(launchClaude([], {}, {
    register: async (_body: any, env: NodeJS.ProcessEnv) => { capabilityDir = dirname(String(env.FORGE_CLAUDE_TOKEN_FILE)); throw new Error('boom'); },
    run: async () => { ran = true; return { code: 0, signal: null }; },
    end: async () => { ended = true; },
  }));
  assert.equal(ran, false, 'a child was launched without a registered instance');
  assert.equal(ended, false, 'an end was sent for an unconfirmed registration');
  assert.ok(capabilityDir, 'no capability file was created');
  assert.equal(existsSync(capabilityDir!), false, 'capability directory survived');
});
