import { randomUUID } from 'node:crypto';
import { claudeRPC, ClaudeError } from './transport.ts';

/**
 * Claude-native attribution: the launcher's instance, the hook's canonical
 * session id, and the hook's agent id (`main` for the main agent). This tuple
 * routes a request to one daemon runtime; it is not an execution credential.
 * The private execution proof never leaves forged.
 */
export type Identity = { instance_id: string; session_id: string; agent_id: string };

const valid = (value: unknown): value is string =>
  typeof value === 'string' && value.length > 0 && value.length <= 128 && !/[\s\0]/.test(value);

/** Validate an identity whose source has already been selected by the caller. */
export function validateIdentity(identity: Identity): Identity {
  if (!valid(identity.instance_id) || !valid(identity.session_id) || !valid(identity.agent_id)) throw new ClaudeError('claude_identity_required', false, { message: 'Claude harness identity is unavailable; launch with forge claude' });
  return identity;
}

/** Bash calls must carry the identity injected into that individual command. */
export function claudeIdentity(env: NodeJS.ProcessEnv = process.env): Identity {
  return validateIdentity({
    instance_id: env.FORGE_CLAUDE_INSTANCE_ID ?? '',
    session_id: env.FORGE_CLAUDE_SESSION_ID ?? '',
    agent_id: env.FORGE_CLAUDE_AGENT_ID ?? '',
  });
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
