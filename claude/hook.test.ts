import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { handleHook, PROTOCOL, contextState, environmentPreamble, hookAgent } from './hook.ts';
import { readBinding, bindingLine, BINDING_TTL_MS, bindingPath } from './binding.ts';
import { claudeIdentity, resolveAgent } from './facade.ts';

const SESSION = '11111111-1111-4111-8111-111111111111';
const env = { FORGE_CLAUDE_INSTANCE_ID: 'i', FORGE_CLAUDE_SESSION_ID: SESSION, FORGE_CLAUDE_AGENT_ID: 'main' };

/** Capture the daemon calls a hook makes, and the binding it writes. */
function recorder(state: any = { active: false }) {
  const calls: Array<[string, any]> = [];
  const bound: string[] = [];
  return {
    calls, bound,
    rpc: (async (op: string, body: any) => { calls.push([op, body]); return state; }) as any,
    bind: async (agent: string) => { bound.push(agent); return true; },
  };
}

test('the main agent is the default actor and a subagent keeps its own agent_id', () => {
  assert.equal(hookAgent({}), 'main');
  assert.equal(hookAgent({ agent_id: '' }), 'main');
  assert.equal(hookAgent({ agent_id: 'child-1' }), 'child-1');
});

test('every documented Claude lifecycle event maps to the shared daemon event', async () => {
  const expected: Record<string, string> = {
    SessionStart: 'start', SubagentStart: 'start', PostCompact: 'context',
    PreCompact: 'touch', PreToolUse: 'touch', PostToolUse: 'touch', PostToolUseFailure: 'touch',
    UserPromptSubmit: 'prompt', SessionEnd: 'session_end',
    Stop: 'stop_check', SubagentStop: 'stop_check',
  };
  for (const [event, daemon] of Object.entries(expected)) {
    const r = recorder();
    await handleHook({ hook_event_name: event, session_id: SESSION }, env, { rpc: r.rpc, bind: r.bind, publish: async () => true });
    assert.equal(r.calls[0][0], 'event', event);
    assert.equal(r.calls[0][1].event, daemon, event);
    assert.equal(r.calls[0][1].identity.session_id, SESSION, event);
  }
});

test('subagent events attribute to the real hook agent_id, not the main agent', async () => {
  for (const event of ['SubagentStart', 'SubagentStop', 'PreToolUse', 'PreCompact']) {
    const r = recorder();
    await handleHook({ hook_event_name: event, session_id: SESSION, agent_id: 'child', agent_type: 'Explore' }, env, { rpc: r.rpc, bind: r.bind });
    assert.equal(r.calls[0][1].identity.agent_id, 'child', event);
  }
});

test('SessionStart injects the protocol and publishes the canonical session', async () => {
  const r = recorder();
  const published: string[] = [];
  const output = await handleHook({ hook_event_name: 'SessionStart', session_id: SESSION, source: 'startup' }, env, { rpc: r.rpc, bind: r.bind, publish: async (s: string) => { published.push(s); return true; } });
  assert.deepEqual(published, [SESSION]);
  assert.equal(output.hookSpecificOutput.hookEventName, 'SessionStart');
  assert.match(output.hookSpecificOutput.additionalContext, /Forge is this workspace's work ledger/);
  assert.deepEqual(environmentPreamble(SESSION), `export FORGE_CLAUDE_SESSION_ID='${SESSION}'\nexport FORGE_CLAUDE_AGENT_ID='main'\n`);
});

test('a subagent session start never publishes a global agent identity', async () => {
  const r = recorder();
  let published = false;
  const output = await handleHook({ hook_event_name: 'SubagentStart', session_id: SESSION, agent_id: 'child' }, env, { rpc: r.rpc, bind: r.bind, publish: async () => { published = true; return true; } });
  assert.equal(published, false, 'a subagent overwrote the global agent identity');
  assert.equal(output.hookSpecificOutput.hookEventName, 'SubagentStart');
});

test('compaction refreshes context without creating, restoring or releasing execution', async () => {
  for (const [event, expected] of [['PreCompact', 'touch'], ['PostCompact', 'context']] as const) {
    const r = recorder({ active: true, pending: true });
    const output = await handleHook({ hook_event_name: event, session_id: SESSION, trigger: 'auto' }, env, { rpc: r.rpc, bind: r.bind });
    assert.equal(r.calls.length, 1, event);
    assert.equal(r.calls[0][1].event, expected);
    // PostCompact cannot inject context; PreCompact never injects either.
    assert.deepEqual(output, {});
  }
});

test('PreToolUse binds the agent for Bash and file mutation, and never decides permissions', async () => {
  for (const tool of ['Bash', 'Edit', 'Write', 'NotebookEdit']) {
    const r = recorder();
    const output = await handleHook({ hook_event_name: 'PreToolUse', session_id: SESSION, agent_id: 'child', tool_name: tool }, env, { rpc: r.rpc, bind: r.bind });
    assert.deepEqual(r.bound, ['child'], tool);
    // The adapter is not a permission boundary: no decision is ever returned.
    assert.deepEqual(output, {}, tool);
  }
  const read = recorder();
  await handleHook({ hook_event_name: 'PreToolUse', session_id: SESSION, agent_id: 'child', tool_name: 'Read' }, env, { rpc: read.rpc, bind: read.bind });
  assert.deepEqual(read.bound, [], 'a read bound an agent unnecessarily');
});

test('tool observations are liveness only and never carry context', async () => {
  for (const event of ['PostToolUse', 'PostToolUseFailure']) {
    const r = recorder();
    assert.deepEqual(await handleHook({ hook_event_name: event, session_id: SESSION, agent_id: 'child', tool_name: 'Bash' }, env, { rpc: r.rpc, bind: r.bind }), {});
    // Liveness is still attributed to the right agent, but no binding is written.
    assert.equal(r.calls[0][1].identity.agent_id, 'child', event);
    assert.deepEqual(r.bound, [], event);
  }
});

test('strict Stop blocks unresolved work once and pauses renewal; advisory only warns', async () => {
  const busy = { active: true, pending: false, issue: 'abc' };
  const strict = recorder(busy);
  const blocked = await handleHook({ hook_event_name: 'Stop', session_id: SESSION, stop_hook_active: false }, env, { rpc: strict.rpc, bind: strict.bind });
  assert.equal(blocked.decision, 'block');
  assert.ok(blocked.reason);
  // Blocking continues the turn, so renewal must keep running: exactly one call.
  assert.equal(strict.calls.length, 1);
  assert.equal(strict.calls[0][1].event, 'stop_check');

  const advisory = recorder(busy);
  const warned = await handleHook({ hook_event_name: 'Stop', session_id: SESSION, stop_hook_active: false }, { ...env, FORGE_CLAUDE_STOP_POLICY: 'advisory' }, { rpc: advisory.rpc, bind: advisory.bind });
  assert.equal(warned.decision, undefined);
  assert.ok(warned.systemMessage);
  // A warned stop does let the agent stop, so renewal is paused.
  assert.equal(advisory.calls.length, 2);
  assert.equal(advisory.calls[1][1].event, 'pause');

  // An already-continued turn never blocks again.
  const again = recorder(busy);
  assert.equal((await handleHook({ hook_event_name: 'Stop', session_id: SESSION, stop_hook_active: true }, env, { rpc: again.rpc, bind: again.bind })).decision, undefined);

  // A subagent stop is scoped to that subagent, not treated as session shutdown.
  const sub = recorder(busy);
  await handleHook({ hook_event_name: 'SubagentStop', session_id: SESSION, agent_id: 'child', stop_hook_active: false }, env, { rpc: sub.rpc, bind: sub.bind });
  assert.equal(sub.calls[0][1].identity.agent_id, 'child');
});

test('an idle Stop is a no-op and SessionEnd is retired immediately', async () => {
  const idle = recorder({ active: false, pending: false });
  assert.deepEqual(await handleHook({ hook_event_name: 'Stop', session_id: SESSION }, env, { rpc: idle.rpc, bind: idle.bind }), {});
  const end = recorder();
  await handleHook({ hook_event_name: 'SessionEnd', session_id: SESSION, reason: 'logout' }, env, { rpc: end.rpc, bind: end.bind });
  assert.equal(end.calls[0][1].event, 'session_end');
});

test('invalid input and identity mismatch never reach the daemon', async () => {
  const r = recorder();
  const deps = { rpc: r.rpc, bind: r.bind };
  await assert.rejects(handleHook(null, env, deps));
  await assert.rejects(handleHook({ hook_event_name: 'Stop' }, env, deps));
  await assert.rejects(handleHook({ hook_event_name: 'NotARealEvent', session_id: SESSION }, env, deps));
  await assert.rejects(handleHook({ hook_event_name: 'Stop', session_id: '22222222-2222-4222-8222-222222222222' }, env, deps));
  await assert.rejects(handleHook({ hook_event_name: 'Stop', session_id: SESSION }, { ...env, FORGE_CLAUDE_STOP_POLICY: 'bogus' }, deps));
  assert.deepEqual(r.calls, []);
});

test('injected context carries workflow state and never execution plumbing', () => {
  // The daemon reply is redacted, and this allowlist is a second boundary.
  const state = {
    active: true, pending: false, paused: false, unknown: false, issue: '01K00000000000000000000002',
    workspaces: [], execution_token: 'private-proof', execution_id: 'private-execution', attempt_id: 'private-attempt',
  };
  const rendered = contextState(state);
  assert.equal(rendered.includes('private-'), false);
  assert.equal(rendered.includes('execution_token'), false);
  assert.deepEqual(Object.keys(JSON.parse(rendered)).sort(), ['active', 'issue', 'paused', 'pending', 'unknown', 'workspaces']);
  assert.equal(contextState(null), '{}');
  assert.equal(contextState('nope'), '{}');
  assert.equal(PROTOCOL.includes('execution_token'), false);
});

test('the CLI resolves the acting agent from the environment, then the binding', async () => {
  const dir = await mkdtemp(join(tmpdir(), 'forge-binding-'));
  const tokenFile = join(dir, 'instance-token');
  const binding = join(dir, 'agent-binding');
  const bindingEnv = { ...env, FORGE_CLAUDE_TOKEN_FILE: tokenFile };
  try {
    assert.equal(bindingPath(bindingEnv), binding);
    assert.equal(resolveAgent(bindingEnv, () => undefined), 'main', 'no binding must fall back to the published agent');

    // A subagent's shell sees no published agent, so its own binding decides.
    const subEnv = { FORGE_CLAUDE_INSTANCE_ID: 'i', FORGE_CLAUDE_SESSION_ID: SESSION, FORGE_CLAUDE_TOKEN_FILE: tokenFile };
    assert.equal(resolveAgent(subEnv, () => 'child'), 'child');
    assert.equal(claudeIdentity({ ...subEnv, FORGE_CLAUDE_AGENT_ID: 'main' }).agent_id, 'main', 'the published environment must win');

    // A real file round-trips, and a stale or malformed binding is ignored.
    await writeFile(binding, bindingLine('child'), { mode: 0o600 });
    assert.equal(readBinding(bindingEnv), 'child');
    await writeFile(binding, JSON.stringify({ agent: 'child', at: Date.now() - BINDING_TTL_MS - 1 }));
    assert.equal(readBinding(bindingEnv), undefined, 'a stale binding must not label a later call');
    await writeFile(binding, JSON.stringify({ agent: 'child', at: Date.now() + 60_000 }));
    assert.equal(readBinding(bindingEnv), undefined, 'a future-dated binding must be refused');
    await writeFile(binding, 'not json');
    assert.equal(readBinding(bindingEnv), undefined);
    assert.equal(readBinding({}), undefined, 'no token file means no binding');
    assert.equal((await readFile(binding, 'utf8')), 'not json');
  } finally { await rm(dir, { recursive: true, force: true }); }
});
