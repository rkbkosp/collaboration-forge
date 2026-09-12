import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { ForgeError } from '../pi-extension/controller.ts';
import { forgeErrorEnvelope } from '../pi-extension/errors.ts';

type Exit = { code: number | null; signal: NodeJS.Signals | null };
export type PiRunner = (binary: string, args: string[], env: NodeJS.ProcessEnv) => Promise<Exit>;
export interface PiLaunchDependencies {
  run?: PiRunner;
  binary?: string;
  extension?: string;
}

async function run(binary: string, args: string[], env: NodeJS.ProcessEnv): Promise<Exit> {
  const child = spawn(binary, args, { env, stdio: 'inherit' });
  const forward = (signal: NodeJS.Signals) => { if (child.pid) child.kill(signal); };
  const interrupt = () => forward('SIGINT');
  const terminate = () => forward('SIGTERM');
  process.on('SIGINT', interrupt);
  process.on('SIGTERM', terminate);
  try {
    return await new Promise((resolve, reject) => {
      child.once('error', () => reject(new Error('pi_launch_failed')));
      child.once('exit', (code, signal) => resolve({ code, signal }));
    });
  } finally {
    process.removeListener('SIGINT', interrupt);
    process.removeListener('SIGTERM', terminate);
  }
}

function hasExtension(args: string[], extension: string): boolean {
  return args.some((arg, index) =>
    arg === `--extension=${extension}` || arg === `-e=${extension}` ||
    ((arg === '--extension' || arg === '-e') && args[index + 1] === extension));
}

export function piArguments(args: string[], extension = fileURLToPath(new URL('../pi-extension/forge.ts', import.meta.url))): string[] {
  return hasExtension(args, extension) ? [...args] : ['--extension', extension, ...args];
}

/** Pi owns its own runtime. Never pass a CLI broker, supervisor credential, or
 * Codex/legacy checkout execution identity into the child process. The host
 * wrapper's selected FORGE_URL and worker-token path remain inherited. */
export function piEnvironment(env: NodeJS.ProcessEnv): NodeJS.ProcessEnv {
  const childEnv = { ...env };
  for (const key of Object.keys(childEnv)) if (key.startsWith('FORGE_CODEX_')) delete childEnv[key];
  for (const key of [
    'FORGE_SOCKET', 'FORGE_EXECUTION_SOCKET', 'FORGE_ADMIN_TOKEN_FILE',
    'FORGE_ISSUE', 'FORGE_CLI',
  ]) delete childEnv[key];
  return childEnv;
}

export async function launchPi(args: string[], env: NodeJS.ProcessEnv = process.env, dependencies: PiLaunchDependencies = {}): Promise<number> {
  const extension = dependencies.extension ?? fileURLToPath(new URL('../pi-extension/forge.ts', import.meta.url));
  const result = await (dependencies.run ?? run)(dependencies.binary ?? 'pi', piArguments(args, extension), piEnvironment(env));
  return result.code ?? 1;
}

export async function piCommand(args: string[], env: NodeJS.ProcessEnv = process.env): Promise<number> {
  try {
    return await launchPi(args, env);
  } catch {
    const error = new ForgeError('pi_launch_failed', 'Unable to launch Pi with the Forge extension');
    process.stderr.write(JSON.stringify(forgeErrorEnvelope(error)) + '\n');
    return 1;
  }
}
