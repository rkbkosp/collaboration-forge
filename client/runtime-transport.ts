import { constants } from 'node:fs';
import { open } from 'node:fs/promises';
import { directFetch } from '../pi-extension/direct-fetch.ts';
import { loadConfig } from '../pi-extension/config.ts';
import { redactErrorValue, type ErrorData } from '../pi-extension/errors.ts';

/** The shared error shape every supervised adapter transport reports. */
export interface RuntimeErrorOptions {
  status?: number;
  message?: string;
  hint?: string;
  data?: ErrorData;
}
export interface RuntimeErrorLike extends Error {
  readonly code: string;
  readonly ambiguous: boolean;
  readonly status: number;
  readonly hint?: string;
  readonly data?: ErrorData;
}
export type RuntimeErrorFactory = (code: string, ambiguous: boolean, options?: RuntimeErrorOptions) => RuntimeErrorLike;

/**
 * One supervised CLI harness's private control transport. This selects a
 * namespace, a capability header, a credential variable and diagnostic codes —
 * never an execution protocol. Every profile dispatches into the same daemon
 * runtime, Worker API, execution proof and strict-close facade.
 */
export interface RuntimeProfile {
  /** Diagnostic label, e.g. `Codex`. */
  name: string;
  /** Private control namespace, e.g. `/forge/v1/codex/`. */
  prefix: string;
  /** Header carrying the instance capability. */
  header: string;
  /** Environment variable naming the 0600 capability file. */
  tokenVariable: string;
  codes: {
    credential: string;
    configuration: string;
    requestFailed: string;
    transportUnknown: string;
  };
  makeError: RuntimeErrorFactory;
}

/**
 * Credential files are opened without following symlinks and must be owned
 * single-token 0600 files. The capability is read only here: it is never a
 * model argument, a model-visible result, or a log line.
 */
export async function instanceToken(file: string | undefined, profile: RuntimeProfile): Promise<string> {
  let handle;
  try {
    handle = await open(String(file ?? ''), constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK);
    const stat = await handle.stat();
    if (!stat.isFile() || stat.uid !== process.getuid?.() || (stat.mode & 0o777) !== 0o600 || stat.size !== 64) throw new Error();
    const token = await handle.readFile('utf8');
    if (!/^[a-f0-9]{64}$/.test(token)) throw new Error();
    return token;
  } catch {
    throw profile.makeError(profile.codes.credential, false);
  } finally { await handle?.close(); }
}

const record = (value: unknown): value is Record<string, any> => value !== null && typeof value === 'object' && !Array.isArray(value);
function code(profile: RuntimeProfile, value: unknown): string {
  return typeof value === 'string' && /^[a-z][a-z0-9_]{0,99}$/.test(value) ? value : profile.codes.requestFailed;
}
const mutatingOperations = new Set(['issue_claim', 'issue_renew', 'issue_release', 'issue_close', 'issue_create', 'issue_comment', 'issue_link', 'checkout', 'retry']);
function mutationRequest(op: string, body: unknown): boolean {
  if (op !== 'tool' && op !== 'command') return op === 'register' || op === 'event';
  if (!record(body)) return false;
  return mutatingOperations.has(String(body.operation));
}

/**
 * One logical control request. Callers own the body, including its stable
 * request identity, so a bounded transport retry after an ambiguous outcome
 * reuses the same logical request instead of creating a second attempt.
 */
export async function runtimeRPC(profile: RuntimeProfile, op: string, body: unknown, env: NodeJS.ProcessEnv = process.env, timeout = 10_000): Promise<any> {
  const mutation = mutationRequest(op, body);
  const uncertain = (status: number) => mutation && (status === 0 || status === 408 || status === 429 || status >= 500);
  const config = await loadConfig(env).catch(() => { throw profile.makeError(profile.codes.configuration, false); });
  const token = await instanceToken(env[profile.tokenVariable], profile);
  let status = 0;
  try {
    const headers: Record<string, string> = {
      Authorization: 'Bearer ' + config.workerToken,
      'Content-Type': 'application/json',
    };
    headers[profile.header] = token;
    const response = await directFetch(config.url + profile.prefix + op, {
      method: 'POST', redirect: 'error', headers, body: JSON.stringify(body), signal: AbortSignal.timeout(timeout),
    });
    status = response.status;
    const reader = response.body?.getReader();
    let size = 0;
    const chunks: Uint8Array[] = [];
    if (reader) for (;;) {
      const part = await reader.read();
      if (part.done) break;
      size += part.value.length;
      if (size > 8 << 20) { await reader.cancel(); throw new Error(); }
      chunks.push(part.value);
    }
    let result: any;
    try { result = JSON.parse(Buffer.concat(chunks).toString('utf8')); }
    catch {
      throw profile.makeError(response.ok ? profile.codes.transportUnknown : profile.codes.requestFailed, uncertain(response.status), {
        status: response.status,
        message: response.ok
          ? profile.name + ' returned an unreadable response'
          : profile.name + ' request failed with HTTP ' + response.status,
      });
    }
    if (!response.ok) {
      const error = record(result?.error) ? result.error : {};
      const secrets = [config.workerToken, token];
      const message = typeof error.message === 'string' && error.message
        ? String(redactErrorValue(error.message, secrets))
        : profile.name + ' request failed with HTTP ' + response.status;
      const hint = typeof error.hint === 'string' && error.hint ? String(redactErrorValue(error.hint, secrets)) : undefined;
      const data = error.data && typeof error.data === 'object' && !Array.isArray(error.data) ? redactErrorValue(error.data, secrets) as ErrorData : undefined;
      throw profile.makeError(code(profile, error.code), uncertain(response.status) || error.ambiguous === true, { status: response.status, message, hint, data });
    }
    return result;
  } catch (error) {
    if (error instanceof Error && 'ambiguous' in error && (error as RuntimeErrorLike).code) throw error;
    throw profile.makeError(profile.codes.transportUnknown, uncertain(status), {
      status,
      message: profile.name + ' request failed or its response was invalid; inspect server state before repeating a mutation',
    });
  }
}
