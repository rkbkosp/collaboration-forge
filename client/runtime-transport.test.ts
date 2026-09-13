import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { runtimeRPC, type RuntimeProfile } from './runtime-transport.ts';
import { codexProfile } from '../codex/transport.ts';
import { claudeProfile } from '../claude/transport.ts';

/** A readable worker credential, so configuration succeeds and the capability read is reached. */
async function workerEnv(): Promise<{ dir: string; env: NodeJS.ProcessEnv }> {
  const dir = await mkdtemp(join(tmpdir(), 'forge-runtime-transport-'));
  const file = join(dir, 'worker-token');
  await writeFile(file, 'w'.repeat(48), { mode: 0o600 });
  return { dir, env: { FORGE_URL: 'http://127.0.0.1:7347', FORGE_WORKER_TOKEN_FILE: file } };
}

class TestError extends Error {
  readonly status: number;
  constructor(public code: string, public ambiguous = false, public options: { status?: number } = {}) {
    super(code);
    this.status = options.status ?? 0;
  }
}
const profile = (over: Partial<RuntimeProfile> = {}): RuntimeProfile => ({
  name: 'Test', prefix: '/forge/v1/test/', header: 'X-Forge-Test-Token', tokenVariable: 'FORGE_TEST_TOKEN_FILE',
  codes: { credential: 'test_credential', configuration: 'test_configuration', requestFailed: 'test_request_failed', transportUnknown: 'test_transport_unknown' },
  makeError: (code, ambiguous, options = {}) => new TestError(code, ambiguous, options),
  ...over,
});

test('both adapters declare distinct namespaces, headers, credentials and codes', () => {
  assert.equal(codexProfile.prefix, '/forge/v1/codex/');
  assert.equal(codexProfile.header, 'X-Forge-Codex-Token');
  assert.equal(codexProfile.tokenVariable, 'FORGE_CODEX_TOKEN_FILE');
  assert.equal(claudeProfile.prefix, '/forge/v1/claude/');
  assert.equal(claudeProfile.header, 'X-Forge-Claude-Token');
  assert.equal(claudeProfile.tokenVariable, 'FORGE_CLAUDE_TOKEN_FILE');
  assert.notEqual(codexProfile.prefix, claudeProfile.prefix);
  assert.notEqual(codexProfile.header, claudeProfile.header);
});

test('a missing capability file is a credential error naming only the harness', async () => {
  const { dir, env } = await workerEnv();
  try {
    for (const p of [codexProfile, claudeProfile]) {
      await assert.rejects(runtimeRPC(p, 'register', {}, env),
        (error: TestError) => error.code === p.codes.credential);
    }
  } finally { await rm(dir, { recursive: true, force: true }); }
});

test('unreadable worker configuration never echoes a supplied URL secret', async () => {
  for (const p of [codexProfile, claudeProfile]) {
    await assert.rejects(
      runtimeRPC(p, 'register', {}, { FORGE_URL: 'https://secret-user:secret-password@example.com', [p.tokenVariable]: '/nonexistent' }),
      (error: Error) => !error.message.includes('secret'));
  }
});

test('every credential failure is specific and never leaks a path or token', async () => {
  const { dir, env } = await workerEnv();
  const capability = join(dir, 'capability');
  try {
    for (const p of [codexProfile, claudeProfile]) {
      const missing = await runtimeRPC(p, 'register', {}, env).then(() => undefined, (e: TestError) => e);
      assert.equal(missing?.code, p.codes.credential);
      assert.equal(missing?.message.includes(capability), false);
      // A wrong mode or size is refused the same way as a missing file.
      await writeFile(capability, 'f'.repeat(64), { mode: 0o644 });
      const wrongMode = await runtimeRPC(p, 'register', {}, { ...env, [p.tokenVariable]: capability }).then(() => undefined, (e: TestError) => e);
      assert.equal(wrongMode?.code, p.codes.credential);
    }
  } finally { await rm(dir, { recursive: true, force: true }); }
});

test('observation requests are never classified as ambiguous mutations', async () => {
  const { dir, env } = await workerEnv();
  const capability = join(dir, 'capability');
  await writeFile(capability, 'a'.repeat(64), { mode: 0o600 });
  const withToken = { ...env, FORGE_TEST_TOKEN_FILE: capability };
  try {
    // An unreachable loopback port is a transport failure, not a server rejection.
    const read = await runtimeRPC(profile(), 'tool', { operation: 'status' }, withToken).then(() => undefined, (e: TestError) => e);
    assert.equal(read?.ambiguous, false);
    // The same failure on a mutation must stay ambiguous so the caller can retry
    // the identical logical request instead of inventing a new attempt.
    const write = await runtimeRPC(profile(), 'tool', { operation: 'issue_claim' }, withToken).then(() => undefined, (e: TestError) => e);
    assert.equal(write?.ambiguous, true);
  } finally { await rm(dir, { recursive: true, force: true }); }
});
