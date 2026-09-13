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
 * Resolve the acting agent, most specific first.
 *
 * The binding wins. This matters even though `SessionStart` publishes `main` into
 * CLAUDE_ENV_FILE, and therefore into the environment of every Bash call: Claude
 * runs that preamble for a subagent's shell too, and the subagent's `PreToolUse`
 * writes its own binding first. If the published value outranked the binding,
 * every subagent would be attributed to the main agent and its own acquire would
 * target the main agent's tenure.
 *
 * With no binding (an ordinary main-agent call between shell invocations, or a
 * stale one), the published value applies, defaulting to `main`.
 *
 * This is cooperative attribution, not authority: forged still enforces the
 * worker credential, the instance capability and the exact live ClaimUID.
 */
export function resolveAgent(env: NodeJS.ProcessEnv = process.env, read = readBinding): string | undefined {
  // The binding describes the shell about to run, so it is the most specific
  // signal and is consulted first.
  let bound: string | undefined;
  try { bound = read(env); } catch { bound = undefined; }
  if (valid(bound)) return bound;
  const explicit = env.FORGE_CLAUDE_AGENT_ID;
  if (valid(explicit)) return explicit;
  return undefined;
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
