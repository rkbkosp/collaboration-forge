import { randomBytes, randomUUID } from 'node:crypto';
import { mkdtemp, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawn } from 'node:child_process';
import { workspaceShim, claudeShim } from '../codex/workspace-shim.ts';
import { claudeRPC } from './transport.ts';

type Exit = { code: number | null; signal: NodeJS.Signals | null };
type Dependencies = {
  register: (body: any, env: NodeJS.ProcessEnv) => Promise<unknown>;
  end: (body: any, env: NodeJS.ProcessEnv) => Promise<unknown>;
  run: (bin: string, args: string[], env: NodeJS.ProcessEnv) => Promise<Exit>;
};

export const CLAUDE_PLUGIN_DIR = fileURLToPath(new URL('./', import.meta.url));

async function run(bin: string, args: string[], env: NodeJS.ProcessEnv): Promise<Exit> {
  const child = spawn(bin, args, { env, stdio: 'inherit' });
  const forward = (signal: NodeJS.Signals) => child.kill(signal);
  const interrupt = () => forward('SIGINT');
  const terminate = () => forward('SIGTERM');
  process.on('SIGINT', interrupt);
  process.on('SIGTERM', terminate);
  try {
    return await new Promise((resolve, reject) => {
      child.once('error', () => reject(new Error('claude_launch_failed')));
      child.once('exit', (code, signal) => resolve({ code, signal }));
    });
  } finally {
    process.removeListener('SIGINT', interrupt);
    process.removeListener('SIGTERM', terminate);
  }
}

/** Inject the bundled plugin unless the caller already supplied this exact directory. */
export function claudeArguments(args: string[], plugin = CLAUDE_PLUGIN_DIR): string[] {
  const supplied = args.some((arg, index) =>
    arg === `--plugin-dir=${plugin}` ||
    (arg === '--plugin-dir' && args[index + 1] === plugin));
  // Other --plugin-dir arguments are the user's and stay untouched.
  return supplied ? [...args] : ['--plugin-dir', plugin, ...args];
}

/**
 * The launcher removes inherited authority and runtime state that would conflict
 * with the fresh instance it owns, and passes no supervisor credential. The
 * host-selected FORGE_URL and worker-token path remain inherited.
 */
export function claudeEnvironment(env: NodeJS.ProcessEnv): NodeJS.ProcessEnv {
  const childEnv = { ...env };
  for (const key of Object.keys(childEnv)) {
    if (key.startsWith('FORGE_CODEX_') || key.startsWith('FORGE_CLAUDE_')) delete childEnv[key];
  }
  for (const key of [
    'FORGE_SOCKET', 'FORGE_EXECUTION_SOCKET', 'FORGE_ADMIN_TOKEN_FILE',
    'FORGE_ISSUE', 'FORGE_CLI', 'CODEX_SESSION_ID', 'CODEX_THREAD_ID',
  ]) delete childEnv[key];
  return childEnv;
}

/**
 * The supervisor owns no execution or renewal loop. Its process-birth identity
 * fences the instance; forged owns renewal and the lease. A normal child exit
 * reports cleanup, and a crash only retires the instance so Kata's TTL recovers
 * the lease instead of force-releasing it.
 */
export async function launchClaude(args: string[], env: NodeJS.ProcessEnv = process.env, deps: Dependencies = {
  register: (b, e) => claudeRPC('register', b, e),
  end: (b, e) => claudeRPC('end', b, e, 750),
  run,
}): Promise<number> {
  const dir = await mkdtemp(join(tmpdir(), 'forge-claude-'));
  const instance = randomUUID();
  const file = join(dir, 'instance-token');
  const childEnv: NodeJS.ProcessEnv = {
    ...claudeEnvironment(env),
    FORGE_CLAUDE_INSTANCE_ID: instance,
    FORGE_CLAUDE_TOKEN_FILE: file,
  };
  let registered = false;
  try {
    await writeFile(file, randomBytes(32).toString('hex'), { mode: 0o600, flag: 'wx' });
    const registration = await deps.register({
      instance_id: instance, pid: process.pid,
      ...(env.FORGE_TTL_SECONDS ? { ttl_seconds: Number(env.FORGE_TTL_SECONDS) } : {}),
    }, childEnv);
    registered = true;
    await workspaceShim(dir, registration, childEnv, claudeShim);
    const result = await deps.run(env.FORGE_CLAUDE_BINARY ?? 'claude', claudeArguments(args), childEnv);
    await deps.end({ instance_id: instance, normal: result.code === 0 && result.signal === null }, childEnv).catch(() => {});
    registered = false;
    return result.code ?? 1;
  } finally {
    if (registered) await deps.end({ instance_id: instance, normal: false }, childEnv).catch(() => {});
    await rm(dir, { recursive: true, force: true });
  }
}
