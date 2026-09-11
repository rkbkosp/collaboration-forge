import { constants } from "node:fs";
import { open } from "node:fs/promises";
import { isIP } from "node:net";

export interface ForgeConfig {
  url: string;
  workerToken: string;
  ttlSeconds: number;
}

/** No DNS resolution, credentials, base paths or redirects. Reject URL parser's
 * shorthand/octal/integer IPv4 coercions by validating the literal authority. */
export function validateURL(input: string): string {
  const match = /^(https?):\/\/(\[[^\]]+\]|[^/:?#]+)(?::([0-9]+))?\/?$/.exec(input);
  if (!match) throw new Error("FORGE_URL must be an http(s) loopback literal origin");
  const host = match[2].replace(/^\[|\]$/g, "");
  const loopback = (isIP(host) === 4 && host.split(".")[0] === "127") ||
    (isIP(host) === 6 && new URL(`http://[${host}]`).hostname === "[::1]");
  if (!loopback) throw new Error("FORGE_URL must use a loopback IP literal (not a hostname)");
  try {
    const url = new URL(input);
    if (url.port === "0") throw new Error();
    return url.origin;
  } catch { throw new Error("Invalid FORGE_URL origin or port"); }
}

export function validateTTL(value: number): number {
  if (!Number.isInteger(value) || value < 60 || value > 3600) {
    throw new Error("FORGE_TTL_SECONDS must be an integer in 60..3600");
  }
  return value;
}

/** Credentials are read only here, never via Pi session history or model input. */
export async function loadConfig(env: NodeJS.ProcessEnv = process.env): Promise<ForgeConfig> {
  const url = validateURL(env.FORGE_URL ?? "http://127.0.0.1:7347");
  const ttl = env.FORGE_TTL_SECONDS ?? "300";
  if (!/^[0-9]+$/.test(ttl)) throw new Error("FORGE_TTL_SECONDS must be an integer in 60..3600");
  const ttlSeconds = validateTTL(Number(ttl));
  if (!env.FORGE_WORKER_TOKEN_FILE) throw new Error("FORGE_WORKER_TOKEN_FILE is required (0600 file)");
  let file;
  try {
    file = await open(env.FORGE_WORKER_TOKEN_FILE, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK);
    const stat = await file.stat();
    if (!stat.isFile() || (stat.mode & 0o7777) !== 0o600 || stat.size > 16_384 ||
        (process.getuid && stat.uid !== process.getuid())) throw new Error();
    const workerToken = (await file.readFile("utf8")).trim();
    if (!workerToken || /\s/.test(workerToken)) throw new Error();
    return { url, workerToken, ttlSeconds };
  } catch {
    // Do not expose path, file contents, or an OS error containing credentials.
    throw new Error("Cannot read Forge worker token: require an owned, regular 0600 file without symlinks and a nonempty single token");
  } finally { await file?.close(); }
}
