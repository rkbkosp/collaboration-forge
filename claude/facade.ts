import { randomUUID } from 'node:crypto';
import { claudeRPC, ClaudeError } from './transport.ts';
import { readBinding } from './binding.ts';

/**
 * Claude-native attribution: the launcher's instance, the hook's canonical
 * session id, and the hook's agent id (`main` for the main agent). This tuple
 * routes a request to one daemon runtime; it is not an execution credential.
 * The private execution proof never leaves forged.
 */
export type Identity = { instance_id: string; session_id: string; agent_id: string };

const valid = (value: unknown): value is string =>
  typeof value === 'string' && value.length > 0 && value.length <= 128 && !/[\s\0]/.test(value);

/**
 * Resolve the acting agent. The environment wins: `SessionStart` publishes
 * `main` globally for ordinary main-agent calls. When it is absent — which is
 * how a subagent's Bash call arrives, since a subagent must never be published
 * globally — the binding recorded by that agent's own `PreToolUse` is used.
 */
export function resolveAgent(env: NodeJS.ProcessEnv = process.env, read = readBinding): string | undefined {
  const published = env.FORGE_CLAUDE_AGENT_ID;
  if (valid(published)) return published;
  try { return read(env); } catch { return undefined; }
}

/** `agent_id` defaults to `main`, which is what a main-agent hook reports. */
export function claudeIdentity(env: NodeJS.ProcessEnv = process.env): Identity {
  const instance_id = env.FORGE_CLAUDE_INSTANCE_ID;
  const session_id = env.FORGE_CLAUDE_SESSION_ID;
  const agent_id = resolveAgent(env) ?? 'main';
  if (!valid(instance_id) || !valid(session_id) || !valid(agent_id)) throw new ClaudeError('claude_identity_required', false, { message: 'Claude harness identity is unavailable; launch with forge claude' });
  return { instance_id, session_id, agent_id };
}

/**
 * One logical tool call. The request identity is minted once, before the first
 * attempt, so a bounded transport retry after an ambiguous outcome replays the
 * same logical request: forged resolves the original receipt instead of
 * starting a second execution attempt.
 */
export async function claudeTool(operation: string, params: unknown = {}, env: NodeJS.ProcessEnv = process.env, rpc: typeof claudeRPC = claudeRPC): Promise<any> {
  const body = { identity: claudeIdentity(env), operation, params: structuredClone(params), request_id: randomUUID() };
  for (let attempt = 0; ; attempt++) {
    try { return await rpc('tool', body, env); }
    catch (error) {
      if (!(error instanceof ClaudeError) || !error.ambiguous || attempt >= 2) throw error;
    }
  }
}
