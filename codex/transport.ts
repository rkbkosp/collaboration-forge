import { instanceToken as readInstanceToken, runtimeRPC, type RuntimeErrorOptions, type RuntimeProfile } from '../client/runtime-transport.ts';
import type { ErrorData } from '../pi-extension/errors.ts';

export interface CodexErrorOptions {
  status?: number;
  message?: string;
  hint?: string;
  data?: ErrorData;
}

export class CodexError extends Error {
  readonly status: number;
  readonly hint?: string;
  readonly data?: ErrorData;
  constructor(public code: string, public ambiguous = false, options: CodexErrorOptions = {}) {
    super(options.message ?? 'Codex operation failed');
    this.name = 'CodexError';
    this.status = options.status ?? 0;
    this.hint = options.hint;
    this.data = options.data;
  }
}

/** Codex keeps its established namespace, header, credential variable and codes. */
export const codexProfile: RuntimeProfile = {
  name: 'Codex',
  prefix: '/forge/v1/codex/',
  header: 'X-Forge-Codex-Token',
  tokenVariable: 'FORGE_CODEX_TOKEN_FILE',
  codes: {
    credential: 'codex_instance_credential',
    configuration: 'codex_configuration',
    requestFailed: 'codex_request_failed',
    transportUnknown: 'codex_transport_unknown',
  },
  makeError: (code, ambiguous, options: RuntimeErrorOptions = {}) => new CodexError(code, ambiguous, options),
};

export async function instanceToken(file: string): Promise<string> {
  return readInstanceToken(file, codexProfile);
}

export async function codexRPC(op: string, body: unknown, env: NodeJS.ProcessEnv = process.env, timeout = 10_000): Promise<any> {
  return runtimeRPC(codexProfile, op, body, env, timeout);
}
