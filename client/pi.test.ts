import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { delimiter } from 'node:path';
import { fileURLToPath } from 'node:url';
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import test from 'node:test';
import { launchPi } from './pi.ts';

const exec = promisify(execFile);
const extension = fileURLToPath(new URL('../pi-extension/forge.ts', import.meta.url));

test('Pi launcher injects the Forge extension and strips legacy credentials/runtime state', async () => {
  let binary = '';
  let args: string[] = [];
  let childEnv: NodeJS.ProcessEnv = {};
  const env = {
    FORGE_URL: 'http://127.0.0.1:7348',
    FORGE_WORKER_TOKEN_FILE: '/private/worker-token',
    FORGE_TTL_SECONDS: '300',
    FORGE_PROJECT_ROOT: '/Volumes/SuperDisk/collab-forge',
    FORGE_SOCKET: '/tmp/old.sock',
    FORGE_EXECUTION_SOCKET: '/tmp/legacy.sock',
    FORGE_ADMIN_TOKEN_FILE: '/private/admin-token',
    FORGE_CODEX_INSTANCE_ID: 'old-instance',
    FORGE_CODEX_TOKEN_FILE: '/private/instance-token',
  };
  const result = await launchPi(['--model', 'fixture', 'prompt'], env, {
    run: async (nextBinary, nextArgs, nextEnv) => {
      binary = nextBinary;
      args = nextArgs;
      childEnv = nextEnv;
      return { code: 0, signal: null };
    },
  });
  assert.equal(result, 0);
  assert.equal(binary, 'pi');
  assert.deepEqual(args, ['--extension', extension, '--model', 'fixture', 'prompt']);
  assert.equal(childEnv.FORGE_URL, env.FORGE_URL);
  assert.equal(childEnv.FORGE_WORKER_TOKEN_FILE, env.FORGE_WORKER_TOKEN_FILE);
  assert.equal(childEnv.FORGE_PROJECT_ROOT, env.FORGE_PROJECT_ROOT);
  for (const key of ['FORGE_SOCKET', 'FORGE_EXECUTION_SOCKET', 'FORGE_ADMIN_TOKEN_FILE', 'FORGE_CODEX_INSTANCE_ID', 'FORGE_CODEX_TOKEN_FILE']) {
    assert.equal(childEnv[key], undefined, `${key} must not enter Pi`);
  }
});

test('Pi launcher does not duplicate its extension and returns child failures', async () => {
  let seen: string[] = [];
  assert.equal(await launchPi(['--extension', extension], {}, {
    run: async (_binary, args) => { seen = args; return { code: 0, signal: null }; },
  }), 0);
  assert.deepEqual(seen, ['--extension', extension]);
  assert.equal(await launchPi([], {}, { run: async () => ({ code: 7, signal: null }) }), 7);
  assert.equal(await launchPi([], {}, { run: async () => ({ code: null, signal: 'SIGTERM' }) }), 1);
});

test('forge pi is an end-to-end CLI entrypoint with routed environment inheritance', async () => {
  const dir = await mkdtemp(`${tmpdir()}/forge-pi-launch-`);
  try {
    const record = `${dir}/args-and-env`;
    const fakePi = `${dir}/pi`;
    await writeFile(fakePi, '#!/bin/sh\nprintf \'%s\\n\' "$@" > "$PI_TEST_RECORD"\nprintf \'ENV_URL=%s\\n\' "${FORGE_URL-UNSET}" >> "$PI_TEST_RECORD"\nprintf \'ENV_WORKER=%s\\n\' "${FORGE_WORKER_TOKEN_FILE-UNSET}" >> "$PI_TEST_RECORD"\nprintf \'ENV_SOCKET=%s\\n\' "${FORGE_SOCKET-UNSET}" >> "$PI_TEST_RECORD"\nprintf \'ENV_ADMIN=%s\\n\' "${FORGE_ADMIN_TOKEN_FILE-UNSET}" >> "$PI_TEST_RECORD"\nprintf \'ENV_CODEX=%s\\n\' "${FORGE_CODEX_INSTANCE_ID-UNSET}" >> "$PI_TEST_RECORD"\n', { mode: 0o700 });
    await exec(process.execPath, ['scripts/forge.mjs', 'pi', '--no-session', 'hello'], {
      cwd: fileURLToPath(new URL('..', import.meta.url)),
      env: {
        ...process.env,
        PATH: `${dir}${delimiter}${process.env.PATH ?? ''}`,
        PI_TEST_RECORD: record,
        FORGE_URL: 'http://127.0.0.1:7348',
        FORGE_WORKER_TOKEN_FILE: '/private/worker-token',
        FORGE_SOCKET: '/tmp/old.sock',
        FORGE_ADMIN_TOKEN_FILE: '/private/admin-token',
        FORGE_CODEX_INSTANCE_ID: 'old-instance',
      },
    });
    const lines = (await readFile(record, 'utf8')).trim().split('\n');
    assert.deepEqual(lines.slice(0, 4), ['--extension', extension, '--no-session', 'hello']);
    assert.ok(lines.includes('ENV_URL=http://127.0.0.1:7348'));
    assert.ok(lines.includes('ENV_WORKER=/private/worker-token'));
    assert.ok(lines.includes('ENV_SOCKET=UNSET'));
    assert.ok(lines.includes('ENV_ADMIN=UNSET'));
    assert.ok(lines.includes('ENV_CODEX=UNSET'));
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});
