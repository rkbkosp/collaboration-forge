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
 * when the environment does not explicitly name an agent. It is last-writer-wins
 * and short-lived, so it describes the shell that is about to run rather than
 * whichever agent started the session. That is acceptable because this value is
 * cooperative attribution only. Authority stays with forged, which enforces the
 * worker credential, the instance capability and the exact live ClaimUID carried
 * by the private execution proof — none of which this value can influence.
 */
export function bindingPath(env: NodeJS.ProcessEnv = process.env): string | undefined {
  const tokenFile = env.FORGE_CLAUDE_TOKEN_FILE;
  if (!tokenFile) return undefined;
  return join(dirname(tokenFile), 'agent-binding');
}

/**
 * How long a binding describes the shell it was written for. It is written by the
 * `PreToolUse` immediately preceding a shell or file mutation, so this only needs
 * to cover that gap. It is deliberately much shorter than a lease TTL: a stale
 * binding must not keep labeling later calls after an agent has stopped.
 */
export const BINDING_TTL_MS = 5_000;

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
    // A missing or implausible timestamp is refused rather than assumed fresh.
    if (typeof parsed.at !== 'number' || !Number.isFinite(parsed.at)) return undefined;
    if (parsed.at > now + 1_000) return undefined;
    if (now - parsed.at > BINDING_TTL_MS) return undefined;
    return parsed.agent;
  } catch { return undefined; }
}

/**
 * Write the binding atomically. A reader must never observe a partial line and
 * fall back to the wrong agent; the rename makes the new value appear at once.
 */
export async function writeBinding(agent: string, env: NodeJS.ProcessEnv = process.env): Promise<boolean> {
  const path = bindingPath(env);
  if (!path) return false;
  const { writeFile, rename } = await import('node:fs/promises');
  const temporary = `${path}.${process.pid}`;
  await writeFile(temporary, bindingLine(agent), { mode: 0o600 });
  await rename(temporary, path);
  return true;
}
