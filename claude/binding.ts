import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';

/**
 * `PreToolUse` binds the current agent to the shell or file mutation about to
 * run. The value is produced by the hook, never trusted from the model.
 *
 * Claude offers no per-invocation environment mechanism to a plugin. The Bash
 * tool's input carries no `env` field, so `updatedInput` cannot convey one, and
 * setting `permissionDecision` would take over Claude's own permission flow —
 * which this adapter must not do, because it is not a permission boundary.
 *
 * The available mechanism is therefore a binding that this runtime's CLI reads
 * when the environment does not already name an agent. It is last-writer-wins:
 * two agents issuing Bash in the same instant can mis-attribute one call. That
 * is acceptable because this value is cooperative attribution only. Authority
 * stays with forged, which enforces the worker credential, the instance
 * capability and the exact live ClaimUID carried by the private execution
 * proof — none of which this value can influence.
 */
export function bindingPath(env: NodeJS.ProcessEnv = process.env): string | undefined {
  const tokenFile = env.FORGE_CLAUDE_TOKEN_FILE;
  if (!tokenFile) return undefined;
  return join(dirname(tokenFile), 'agent-binding');
}

/** A binding older than this is treated as absent, so a dead agent cannot label a later call. */
export const BINDING_TTL_MS = 60_000;

export function bindingLine(agent: string, at = Date.now()): string {
  return JSON.stringify({ agent, at }) + '\n';
}

/** Read the binding the CLI should apply, or undefined when it is absent or stale. */
export function readBinding(env: NodeJS.ProcessEnv = process.env, now = Date.now()): string | undefined {
  const path = bindingPath(env);
  if (!path) return undefined;
  try {
    const parsed = JSON.parse(readFileSync(path, 'utf8'));
    if (!parsed || typeof parsed.agent !== 'string' || !parsed.agent) return undefined;
    if (typeof parsed.at !== 'number' || now - parsed.at > BINDING_TTL_MS || parsed.at > now + 1_000) return undefined;
    return parsed.agent;
  } catch { return undefined; }
}

export async function writeBinding(agent: string, env: NodeJS.ProcessEnv = process.env): Promise<boolean> {
  const path = bindingPath(env);
  if (!path) return false;
  const { writeFile } = await import('node:fs/promises');
  await writeFile(path, bindingLine(agent), { mode: 0o600 });
  return true;
}
