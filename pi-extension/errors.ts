export type ErrorData = Record<string, unknown>;

export interface ForgeErrorOptions {
  hint?: string;
  data?: ErrorData;
}

export interface ForgeErrorLike {
  code: string;
  message: string;
  status?: number;
  ambiguous?: boolean;
  hint?: string;
  data?: ErrorData;
}

/** Only sanitized properties are attached to errors; no response/request/cause. */
export class ForgeError extends Error implements ForgeErrorLike {
  readonly status: number;
  readonly ambiguous: boolean;
  readonly hint?: string;
  readonly data?: ErrorData;

  constructor(
    public readonly code: string,
    message: string,
    status = 0,
    ambiguous = false,
    options: ForgeErrorOptions = {},
  ) {
    super(`${code}: ${message}`);
    this.name = "ForgeError";
    this.status = status;
    this.ambiguous = ambiguous;
    this.hint = options.hint;
    this.data = options.data;
  }
}

/**
 * The code is a machine field. Keep it out of the human message so the same
 * error can be rendered as a JSON envelope or as human text without a
 * duplicated `code: code: message` prefix.
 */
export function errorCode(value: unknown, fallback = "remote_error"): string {
  return typeof value === "string" && /^[a-z][a-z0-9_]{0,99}$/.test(value) ? value : fallback;
}

export function errorMessage(error: Pick<ForgeErrorLike, "code" | "message">): string {
  const prefix = `${error.code}: `;
  return error.message.startsWith(prefix) ? error.message.slice(prefix.length) : error.message;
}

const secretKey = /^(?:authorization|bearer|execution[_-]?token|worker[_-]?token|x-forge-execution|token)$/i;
const privateKey = /^(?:execution[_-]?id|attempt[_-]?id)$/i;

/** Redact untrusted error details before they cross a process boundary. */
export function redactErrorValue(value: unknown, secrets: readonly string[] = []): unknown {
  if (typeof value === "string") {
    let out = value;
    for (const secret of secrets) if (secret) out = out.replaceAll(secret, "[REDACTED]");
    return out.replace(/Bearer\s+[^\s"\\]+/gi, "Bearer [REDACTED]");
  }
  if (Array.isArray(value)) return value.map((item) => redactErrorValue(item, secrets));
  if (value && typeof value === "object") {
    return Object.fromEntries(Object.entries(value)
      .filter(([key]) => !secretKey.test(key) && !privateKey.test(key))
      .map(([key, child]) => [key, redactErrorValue(child, secrets)]));
  }
  return value;
}

/**
 * Serialize the shared error shape used by CLI and session transports. HTTP
 * callers use the same `status`/`error` core, while `ambiguous` remains an
 * optional client-side outcome flag inside the error body for compatibility
 * with the existing session protocol.
 */
export function forgeErrorEnvelope(error: ForgeErrorLike, sanitize?: (value: unknown) => unknown): Record<string, unknown> {
  const clean = (value: unknown) => sanitize ? sanitize(value) : redactErrorValue(value);
  const body: Record<string, unknown> = {
    code: errorCode(error.code),
    message: clean(errorMessage(error)),
    ambiguous: error.ambiguous === true,
  };
  if (typeof error.hint === "string" && error.hint) body.hint = clean(error.hint);
  if (error.data && typeof error.data === "object" && !Array.isArray(error.data)) {
    body.data = clean(error.data);
  }
  const envelope: Record<string, unknown> = { error: body };
  if (Number.isInteger(error.status) && (error.status ?? 0) >= 100 && (error.status ?? 0) <= 599) envelope.status = error.status;
  return envelope;
}
