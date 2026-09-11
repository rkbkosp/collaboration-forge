import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { mkdir, readFile } from 'node:fs/promises';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';
import { InMemoryCredentialStore } from '@earendil-works/pi-ai';
import {
  createAgentSession, DefaultResourceLoader, ModelRuntime, SessionManager, SettingsManager,
  type AgentSession, type ExtensionError,
} from '@earendil-works/pi-coding-agent';
import { Check } from 'typebox/value';
import { startForge } from './harness.ts';

const extensionPath = fileURLToPath(new URL('../extensions/forge.ts', import.meta.url));
const issueTools = [
  'issue_claim', 'issue_close', 'issue_comment', 'issue_create', 'issue_get',
  'issue_graph', 'issue_link', 'issue_list', 'issue_release', 'issue_renew',
];

/**
 * Real SDK smoke, not a fake ExtensionAPI or fake provider:
 * - Pi's loader imports the production default factory and supplies ExtensionAPI.
 * - bindExtensions/navigateTree/reload run Pi's own lifecycle implementation.
 * - Tools are the actual wrapped AgentTools from session.agent.state.tools.
 * - Hook preflights and quit are explicitly dispatched via the public runner.
 *
 * No prompt/agent loop, synthetic model response, TUI, RPC, or OS signal flow is
 * exercised. Direct AgentTool.execute does not run pi-agent-core's automatic
 * validation, tool_call/tool_result batching, or history persistence. These
 * boundaries are intentional; Forge still validates every execute internally.
 */
test('Pi 0.85.1 SDK loads Forge and connects real runtime lifecycle/tools to Kata', { timeout: 60_000 }, async (t) => {
  const forge = await startForge();
  const env = {
    FORGE_URL: forge.url,
    FORGE_WORKER_TOKEN_FILE: join(forge.dataDir, 'worker-token'),
    FORGE_TTL_SECONDS: '60',
    PI_OFFLINE: '1',
  };
  const previousEnv = new Map(Object.keys(env).map(key => [key, process.env[key]]));
  Object.assign(process.env, env);

  // Observe and forward actual HTTP without inventing responses or credentials.
  // Unexpected fetches fail before leaving loopback, including provider/catalog calls.
  const realFetch = globalThis.fetch;
  const requests: { path: string; method: string; sessionID: string | null }[] = [];
  let outsideFetches = 0;
  globalThis.fetch = async (input, init) => {
    const url = new URL(input instanceof Request ? input.url : String(input));
    if (url.origin !== forge.url) {
      outsideFetches++;
      throw new Error('Pi smoke prohibits non-fixture network requests');
    }
    const headers = new Headers(init?.headers ?? (input instanceof Request ? input.headers : undefined));
    requests.push({ path: url.pathname, method: init?.method ?? 'GET', sessionID: headers.get('X-Forge-Session') });
    return realFetch(input, init);
  };

  let session: AgentSession | undefined;
  const errors: ExtensionError[] = [];
  const starts: { reason: string; sessionID: string }[] = [];
  const shutdowns: string[] = [];
  const trees: { oldLeafId: string | null; newLeafId: string | null }[] = [];
  let factoryRuns = 0;
  try {
    const cwd = join(forge.dataDir, 'pi-cwd');
    const agentDir = join(forge.dataDir, 'pi-agent');
    await mkdir(cwd);
    await mkdir(agentDir);
    const settingsManager = SettingsManager.inMemory({ compaction: { enabled: false }, retry: { enabled: false } });
    const loader = new DefaultResourceLoader({
      cwd, agentDir, settingsManager,
      noExtensions: true, // Disable discovery; explicit CLI-equivalent path still loads.
      noSkills: true, noPromptTemplates: true, noThemes: true, noContextFiles: true,
      additionalExtensionPaths: [extensionPath],
      extensionFactories: [{
        name: 'forge-smoke-observer',
        factory(pi) {
          factoryRuns++;
          pi.on('session_start', (event, ctx) => { starts.push({ reason: event.reason, sessionID: ctx.sessionManager.getSessionId() }); });
          pi.on('session_shutdown', event => { shutdowns.push(event.reason); });
          pi.on('session_tree', event => { trees.push({ oldLeafId: event.oldLeafId, newLeafId: event.newLeafId }); });
        },
      }],
    });
    await loader.reload();
    assert.deepEqual(loader.getExtensions().errors, []);
    assert.equal(loader.getExtensions().extensions.filter(extension => extension.path === extensionPath).length, 1);

    const modelRuntime = await ModelRuntime.create({
      credentials: new InMemoryCredentialStore(), modelsPath: null,
      refreshOnCreate: false, allowModelNetwork: false,
    });
    // Inert built-in catalog metadata avoids probing inherited provider auth.
    // It is never prompted; no stream function or provider is mocked/injected.
    const model = modelRuntime.getModel('anthropic', 'claude-sonnet-4-5');
    assert.ok(model, 'Pi 0.85.1 includes this static model descriptor');
    const sessionManager = SessionManager.inMemory(cwd);
    ({ session } = await createAgentSession({
      cwd, agentDir, resourceLoader: loader, settingsManager, sessionManager,
      modelRuntime, model, thinkingLevel: 'off', noTools: 'builtin',
    }));
    const current = session;
    const sdkPackage = JSON.parse(await readFile(new URL('../package.json', import.meta.resolve('@earendil-works/pi-coding-agent')), 'utf8'));
    assert.equal(sdkPackage.version, '0.85.1');

    const tool = (name: string) => {
      const found = current.agent.state.tools.find(candidate => candidate.name === name);
      assert.ok(found, `registered active Pi tool ${name}`);
      return found;
    };
    const execute = async (name: string, params: Record<string, unknown>) => {
      const result = await tool(name).execute(randomUUID(), params, AbortSignal.timeout(10_000));
      const serialized = JSON.stringify(result);
      assert.equal(serialized.includes(forge.workerToken), false, 'worker credential must not enter tool output');
      assert.doesNotMatch(serialized, /"(?:execution_token|execution_id|attempt_id)"/);
      const content = result.content.filter(block => block.type === 'text').map(block => block.text).join('\n');
      return { result, body: JSON.parse(content) as Record<string, any> };
    };
    const preflight = () => current.extensionRunner.emitToolCall({
      type: 'tool_call', toolName: 'edit', toolCallId: randomUUID(),
      input: { path: 'not-executed.txt', edits: [{ oldText: 'old', newText: 'new' }] },
    });
    const snapshot = (ref: string) => forge.admin(`/api/v1/projects/${forge.projectID}/issues/${ref}`);

    assert.deepEqual(current.getActiveToolNames().sort(), issueTools);
    assert.deepEqual(current.getAllTools().filter(item => item.name.startsWith('issue_')).map(item => item.name).sort(), issueTools);
    for (const name of issueTools) {
      assert.equal(current.getAllTools().find(item => item.name === name)?.sourceInfo.path, extensionPath);
    }
    assert.deepEqual([...starts], []);
    await assert.rejects(() => execute('issue_list', {}), /forge_unavailable/);
    await current.bindExtensions({ mode: 'print', onError: error => { errors.push(error); } });
    assert.deepEqual([...starts], [{ reason: 'startup', sessionID: sessionManager.getSessionId() }]);
    assert.match(sessionManager.getSessionId(), /^[0-9a-f]{8}-[0-9a-f]{4}-[47][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
    assert.equal((await preflight())?.block, true, 'real runner refuses edits without a claim');
    await t.test('Pi publishes a graph depth maximum of 10', () => {
      assert.equal(Check(tool('issue_graph').parameters, { ref: 'abc4', depth: 10 }), true);
      assert.equal(Check(tool('issue_graph').parameters, { ref: 'abc4', depth: 11 }), false);
    });
    await t.test('Pi publishes open/closed list status enums', () => {
      assert.equal(Check(tool('issue_list').parameters, { status: 'open' }), true);
      assert.equal(Check(tool('issue_list').parameters, { status: 'closed' }), true);
      assert.equal(Check(tool('issue_list').parameters, { status: 'deleted' }), false);
    });

    const { body: created } = await execute('issue_create', { title: 'Real Pi SDK smoke', body: 'No model inference is involved in this test.' });
    const ref = created.issue.short_id as string;
    const uid = created.issue.uid as string;
    assert.match(ref, /^[0-9a-hjkmnp-tv-z]{4,26}$/);
    assert.equal((await snapshot(ref)).issue.uid, uid, 'tool creation reached real Kata storage');

    // A small fixture exercises the remaining registered write tools, without
    // repeating controller fault-injection, concurrent claims, or heartbeat tests.
    const { body: peer } = await execute('issue_create', { title: 'Pi SDK tool wiring fixture' });
    const peerUID = peer.issue.uid as string;
    const note = 'Recorded through the SDK-registered issue_comment tool.';
    await execute('issue_comment', { ref, body: note });
    await execute('issue_link', { ref, type: 'related', to_ref: peerUID });
    const contributed = await snapshot(ref);
    assert.ok(contributed.comments.some((comment: any) => comment.body === note));
    assert.ok(contributed.links.some((link: any) => link.type === 'related' && link.to.uid === peerUID));
    assert.equal((await execute('issue_claim', { ref: peerUID })).body.granted, true);
    assert.equal((await execute('issue_release', { ref: peerUID })).body.granted, true);
    assert.equal((await snapshot(peerUID)).lease ?? null, null);
    assert.equal((await execute('issue_claim', { ref: peerUID })).body.granted, true);
    const { body: closed } = await execute('issue_close', {
      ref: peerUID, reason: 'audit-no-change', message: 'This generated fixture needs no product change; it only checks real Pi SDK tool wiring.',
      evidence: [{ type: 'no-change-audit', rationale: 'This generated test issue requires no product change; it exists only to exercise real SDK tool dispatch.' }],
    });
    assert.equal(closed.changed, true);
    assert.equal((await snapshot(peerUID)).issue.status, 'closed');

    await t.test('registered graph schema and execution agree with the server depth bound', async () => {
      assert.equal(Check(tool('issue_graph').parameters, { ref, depth: 10 }), true);
      assert.equal(Check(tool('issue_graph').parameters, { ref, depth: 11 }), false);
      const { body: graph } = await execute('issue_graph', { ref, depth: 10 });
      assert.equal(graph.source_uid, uid);
      const before = requests.length;
      await assert.rejects(() => execute('issue_graph', { ref, depth: 11 }), /Invalid Forge tool parameters/);
      assert.equal(requests.length, before, 'invalid depth fails locally, before HTTP');
    });
    await t.test('registered list status is open/closed and both work over real HTTP', async () => {
      for (const status of ['open', 'closed']) {
        assert.equal(Check(tool('issue_list').parameters, { status }), true);
        const { body } = await execute('issue_list', { status });
        assert.ok(Array.isArray(body.issues));
        assert.ok(body.issues.every((issue: any) => issue.status === status));
        assert.ok(body.issues.some((issue: any) => issue.uid === (status === 'open' ? uid : peerUID)));
      }
      assert.equal(Check(tool('issue_list').parameters, { status: 'deleted' }), false);
      const before = requests.length;
      await assert.rejects(() => execute('issue_list', { status: 'deleted' }), /Invalid Forge tool parameters/);
      assert.equal(requests.length, before, 'invalid status fails locally, before HTTP');
    });
    await assert.rejects(() => execute('issue_get', { ref: `e2e#${ref}` }), /validation/, 'real facade rejects qualified refs');
    assert.equal((await execute('issue_get', { ref })).body.issue.uid, uid);

    const { body: claimed } = await execute('issue_claim', { ref, purpose: 'bounded SDK lifecycle smoke' });
    assert.equal(claimed.granted, true);
    const firstClaim = claimed.lease.claim_uid;
    assert.equal((await snapshot(uid)).lease.claim_uid, firstClaim);
    assert.equal(await preflight(), undefined, 'actual runner confirms the exact live lease');
    const prompt = await current.extensionRunner.emitBeforeAgentStart(
      'Smoke preflight only; never sent to a model', undefined, current.systemPrompt,
      current.extensionRunner.createCommandContext().getSystemPromptOptions(),
    );
    assert.ok(prompt?.systemPrompt?.includes(uid));
    assert.ok(prompt?.systemPrompt?.includes(firstClaim));
    assert.equal(prompt?.systemPrompt?.includes(forge.workerToken), false);

    // Preserve the actual sanitized result as host-supplied history, not a fake
    // assistant turn. Reload must not reconstruct execution even from this entry.
    const checkpoint = sessionManager.appendCustomEntry('smoke-checkpoint', { purpose: 'tree target' });
    sessionManager.appendMessage({
      role: 'toolResult', toolCallId: 'host-recorded-claim', toolName: 'issue_claim',
      content: [{ type: 'text', text: JSON.stringify(claimed) }], details: { claimUID: firstClaim },
      isError: false, timestamp: Date.now(),
    });
    const oldLeaf = sessionManager.getLeafId();
    const originalRunner = current.extensionRunner;
    assert.equal((await current.navigateTree(checkpoint, { summarize: false })).cancelled, false);
    assert.deepEqual(trees, [{ oldLeafId: oldLeaf, newLeafId: checkpoint }]);
    assert.equal(current.extensionRunner, originalRunner);
    assert.deepEqual([...shutdowns], []);
    assert.equal((await snapshot(uid)).lease.claim_uid, firstClaim, 'tree did not release');
    assert.equal((await execute('issue_renew', { ref: uid })).body.lease.claim_uid, firstClaim);
    // Return to the real claim result so it is on the active branch at reload.
    assert.ok(oldLeaf);
    await current.navigateTree(oldLeaf, { summarize: false });

    await current.reload();
    assert.notEqual(current.extensionRunner, originalRunner);
    assert.equal(factoryRuns, 2);
    assert.deepEqual([...shutdowns], ['reload']);
    assert.deepEqual(starts.map(start => start.reason), ['startup', 'reload']);
    assert.ok(starts.every(start => start.sessionID === sessionManager.getSessionId()));
    assert.equal((await snapshot(uid)).lease ?? null, null, 'reload shutdown released the real Kata lease');
    assert.equal((await preflight())?.block, true);
    await assert.rejects(() => execute('issue_renew', { ref: uid }), /lease_required/);
    assert.ok(sessionManager.getEntries().some(entry => entry.type === 'message' && entry.message.role === 'toolResult'), 'old sanitized history still exists');

    const { body: reclaimed } = await execute('issue_claim', { ref });
    assert.equal(reclaimed.granted, true);
    assert.notEqual(reclaimed.lease.claim_uid, firstClaim, 'same Pi session must acquire a new tenure');
    assert.notEqual(reclaimed.lease.holder, claimed.lease.holder, 'new factory must not recover the old execution identity');
    await current.extensionRunner.emit({ type: 'session_shutdown', reason: 'quit' });
    assert.equal((await snapshot(uid)).lease ?? null, null, 'public shutdown event releases the new lease');
    assert.deepEqual([...shutdowns], ['reload', 'quit']);
    await assert.rejects(() => execute('issue_list', {}), /forge_unavailable/);
    assert.equal((await preflight())?.block, true);
    assert.deepEqual(errors, []);
    assert.equal(outsideFetches, 0);
    const workerCalls = requests.filter(request => request.path.startsWith('/forge/v1/tools/'));
    assert.deepEqual([...new Set(workerCalls.map(request => request.path.split('/').at(-1)))].sort(), issueTools);
    assert.ok(workerCalls.every(request => request.method === 'POST' && request.sessionID === sessionManager.getSessionId()));
    assert.equal(current.agent.state.isStreaming, false);
    t.diagnostic('Real SDK loader/session/tree/reload and registered tools -> real forged/Kata verified; hooks/quit explicitly dispatched, no LLM/TUI/RPC or OS-signal interaction claimed.');
  } finally {
    try {
      // dispose() only disconnects the SDK; the host must emit shutdown first.
      await session?.extensionRunner.emit({ type: 'session_shutdown', reason: 'quit' });
    } finally {
      session?.dispose();
      globalThis.fetch = realFetch;
      for (const [key, value] of previousEnv) {
        if (value === undefined) delete process.env[key];
        else process.env[key] = value;
      }
      await forge.close();
    }
  }
});
