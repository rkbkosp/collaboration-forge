import { instanceToken as readInstanceToken, runtimeRPC, type RuntimeProfile } from '../client/runtime-transport.ts';
import { ForgeError } from '../pi-extension/errors.ts';

/**
 * Claude uses the shared Forge error shape, so hook envelopes, CLI exit codes
 * and human-readable rendering behave exactly as they do elsewhere.
 */
export class ClaudeError extends ForgeError {
  constructor(code: string, ambiguous = false, options: { status?: number; message?: string; hint?: string; data?: Record<string, unknown> } = {}) {
    super(code, options.message ?? 'Claude operation failed', options.status ?? 0, ambiguous, { hint: options.hint, data: options.data });
    this.name = 'ClaudeError';
  }
}

/**
 * Claude keeps its own control namespace, capability header, credential
 * variable and diagnostic codes. Everything behind them — the Worker API, the
 * execution proof, the lease authority — is the one shared implementation.
 */
export const claudeProfile: RuntimeProfile = {
  name: 'Claude',
  prefix: '/forge/v1/claude/',
  header: 'X-Forge-Claude-Token',
  tokenVariable: 'FORGE_CLAUDE_TOKEN_FILE',
  codes: {
    credential: 'claude_instance_credential',
    configuration: 'claude_configuration',
    requestFailed: 'claude_request_failed',
    transportUnknown: 'claude_transport_unknown',
  },
  makeError: (code, ambiguous, options = {}) => new ClaudeError(code, ambiguous, options),
};

export async function instanceToken(file: string | undefined): Promise<string> {
  return readInstanceToken(file, claudeProfile);
}

/** One logical control request. Callers supply the body, including its stable request identity. */
export async function claudeRPC(op: string, body: unknown, env: NodeJS.ProcessEnv = process.env, timeout = 10_000): Promise<any> {
  return runtimeRPC(claudeProfile, op, body, env, timeout);
}
