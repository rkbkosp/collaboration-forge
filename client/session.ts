import { chmod, link, lstat, mkdtemp, rmdir, unlink } from "node:fs/promises";
import type { Stats } from "node:fs";
import { createConnection, createServer, type Socket } from "node:net";
import { dirname, isAbsolute, join, normalize } from "node:path";
import { Controller, ForgeError } from "../pi-extension/controller.ts";
import { errorCode, forgeErrorEnvelope } from "../pi-extension/errors.ts";
import { toolSchemas, type ToolName } from "../pi-extension/schemas.ts";

const MAX_REQUEST = 1024 * 1024;
const MAX_RESPONSE = 8 * 1024 * 1024;
const IO_TIMEOUT = 5_000;
const REQUEST_TIMEOUT = 30_000;
const readOps = new Set(["issue_timeline", "project", "health"]);
const controlOps = new Set(["state", "guard", "context", "retry", "shutdown"]);
const executionOps = new Set(["issue_claim", "issue_renew", "issue_release", "issue_close"]);
const mutationOps = new Set([...executionOps, "issue_create", "issue_comment", "issue_link", "retry", "shutdown"]);
type Request = { op: string; params?: unknown };
type Extra = (op: string, params: unknown) => Promise<unknown>;
const record = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === "object" && !Array.isArray(value);
const sameFile = (a: Stats, b: Stats) => a.dev === b.dev && a.ino === b.ino;
const failure = (code: string, message: string, ambiguous = false, status = 0, options: { hint?: string; data?: Record<string, unknown> } = {}) =>
  new ForgeError(code, message, status, ambiguous, options);

function checkPath(path: string) {
  if (typeof path !== "string" || !isAbsolute(path) || path.includes("\0") || normalize(path) !== path || Buffer.byteLength(path) > 100 || dirname(path) === path) {
    throw failure("unsafe_socket_path", "Require an absolute Unix socket path of at most 100 bytes");
  }
}
async function parentStat(path: string): Promise<Stats> {
  checkPath(path);
  try {
    const stat = await lstat(dirname(path));
    if (!process.getuid || !stat.isDirectory() || stat.isSymbolicLink() || stat.uid !== process.getuid() || (stat.mode & 0o7777) !== 0o700) throw new Error();
    return stat;
  } catch { throw failure("unsafe_socket_path", "Socket parent must be an owned 0700 directory, not a symlink"); }
}
async function statOrMissing(path: string) {
  try { return await lstat(path); }
  catch (error) { if ((error as NodeJS.ErrnoException).code === "ENOENT") return undefined; throw error; }
}
function parse(line: Buffer): unknown {
  return JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(line));
}
function checkRequest(value: unknown): asserts value is Request {
  if (!record(value) || typeof value.op !== "string" || !value.op || Object.keys(value).some((key) => key !== "op" && key !== "params") ||
      (controlOps.has(value.op) && Object.hasOwn(value, "params"))) {
    throw failure("invalid_request", "Require one request object with op and optional tool params; control operations take no params");
  }
}
function responseLine(result: unknown, error: unknown, controller: Controller, fallbackAmbiguous = false): string {
  let line: string;
  try {
    const envelope = error instanceof ForgeError
      ? forgeErrorEnvelope(error, (value) => controller.sanitize(value))
      : { error: { code: "session_error", message: "Session operation unavailable", ambiguous: false } };
    line = JSON.stringify(error ? { ok: false, ...envelope } : { ok: true, result: controller.sanitize(result ?? null) }) + "\n";
  } catch { line = JSON.stringify({ ok: false, error: { code: "session_error", message: "Session operation unavailable", ambiguous: false } }) + "\n"; }
  if (Buffer.byteLength(line) > MAX_RESPONSE) {
    return JSON.stringify({ ok: false, error: { code: "response_too_large", message: "Session response exceeds size limit; check state before retrying", ambiguous: error instanceof ForgeError ? error.ambiguous || fallbackAmbiguous : fallbackAmbiguous } }) + "\n";
  }
  return line;
}

/** One foreground process owns this controller and all in-memory retry authority.
 * The filesystem grants access to cooperative current-UID clients, not an OS sandbox. */
export async function startSession(socketPath: string, controller: Controller, extra?: Extra): Promise<{ close(): Promise<void>; done: Promise<void> }> {
  const parent = await parentStat(socketPath);
  try {
    if (await statOrMissing(socketPath)) throw failure("socket_occupied", "Socket path already exists; stale sockets must be removed explicitly");
  } catch (error) {
    if (error instanceof ForgeError) throw error;
    throw failure("unsafe_socket_path", "Cannot safely inspect socket path");
  }

  // libuv unconditionally unlinks its bind path on close (even a replacement).
  // Bind privately, then publish with link(): atomic no-overwrite, same inode.
  // Only our explicit identity-checked unlink ever touches the public pathname.
  let staging: string | undefined;
  let stagingStat: Stats | undefined;
  let published: Stats | undefined;
  let closing: Promise<void> | undefined;
  // A historical confirmation, NOT execution authority or a current state view.
  // Keep only a bounded, detached, sanitized JSON snapshot; never persist it.
  let confirmed: string | undefined;
  let executionQueue: Promise<unknown> = Promise.resolve();
  const remember = (result: unknown) => {
    if (closing) return;
    try {
      const safe = controller.sanitize(result);
      if (!record(safe) || safe.granted === false) return;
      const json = JSON.stringify({ ...safe, client_replayed: true, current_state_not_refreshed: true });
      if (Buffer.byteLength(`{"ok":true,"result":${json}}\n`) <= MAX_RESPONSE) confirmed = json;
    } catch { /* Unserializable/oversized confirmations are never retained. */ }
  };
  // Serialize cache invalidation, Controller execution, and confirmation capture
  // together. Socket reads/writes and non-execution operations never hold this queue.
  const execute = (request: Request, signal: AbortSignal) => {
    const next = executionQueue.then(async () => {
      if (closing) throw failure("runtime_stopped", "Session runtime stopped");
      signal.throwIfAborted();
      if (request.op === "retry") {
        const state = controller.state();
        if (!state.pendingClaim && !state.pendingClose) {
          if (confirmed) return JSON.parse(confirmed);
          throw failure("no_pending", "No pending request or retained confirmation to retry");
        }
        confirmed = undefined;
        const result = await controller.retryPending(signal);
        remember(result);
        return result;
      }
      confirmed = undefined; // Even a definite new-operation failure retires the old confirmation.
      const result = await controller.execute(request.op as ToolName, request.params ?? {}, signal);
      if (request.op === "issue_claim" || request.op === "issue_close") remember(result);
      return result;
    });
    executionQueue = next.catch(() => {});
    return next;
  };
  const clients = new Set<Socket>();
  const server = createServer({ allowHalfOpen: true }, (socket) => {
    if (closing) { socket.destroy(); return; }
    clients.add(socket);
    const abort = new AbortController();
    let received = false;
    let replied = false;
    let chunks: Buffer[] = [];
    let size = 0;
    let timer: ReturnType<typeof setTimeout>;
    const deadline = (ms: number, fn: () => void) => { clearTimeout(timer); timer = setTimeout(fn, ms); };
    const reply = (result: unknown, error?: ForgeError, shutdown = false, fallbackAmbiguous = false) => {
      if (replied) return;
      replied = true; received = true;
      chunks = [];
      socket.pause();
      clearTimeout(timer);
      const finished = () => {
        clearTimeout(timer);
        socket.destroy();
        if (shutdown) void close().catch(() => {});
      };
      if (socket.destroyed) { finished(); return; }
      deadline(IO_TIMEOUT, finished);
      socket.end(responseLine(result, error, controller, fallbackAmbiguous), finished);
    };
    deadline(IO_TIMEOUT, () => reply(undefined, failure("session_timeout", "Session request read timed out")));
    socket.on("error", () => { socket.destroy(); });
    socket.on("close", () => { clearTimeout(timer); abort.abort(); clients.delete(socket); });
    socket.on("end", () => { if (!received) reply(undefined, failure("invalid_request", "Require a newline-terminated JSON request")); });
    socket.on("data", (chunk: Buffer) => {
      if (received) return;
      size += chunk.length;
      if (size > MAX_REQUEST) { reply(undefined, failure("request_too_large", "Session request exceeds 1 MiB")); return; }
      const newline = chunk.indexOf(10);
      chunks.push(chunk);
      if (newline < 0) return;
      let request: Request;
      try {
        if (newline !== chunk.length - 1) throw new Error();
        const value = parse(Buffer.concat(chunks, size).subarray(0, -1));
        checkRequest(value); request = value;
      } catch { reply(undefined, failure("invalid_request", "Require exactly one valid JSON request line; control operations take no params")); return; }
      received = true; chunks = []; socket.pause();
      deadline(REQUEST_TIMEOUT, () => {
        abort.abort();
        reply(undefined, failure("session_timeout", "Session operation timed out; check state before retrying", mutationOps.has(request.op)));
      });
      const dispatch = async () => {
        if (executionOps.has(request.op) || request.op === "retry") return execute(request, abort.signal);
        if (Object.hasOwn(toolSchemas, request.op)) return controller.execute(request.op as ToolName, request.params ?? {}, abort.signal);
        if (readOps.has(request.op)) {
          if (!extra) throw failure("unsupported_operation", "This session does not support the requested read operation");
          return extra(request.op, request.params);
        }
        switch (request.op) {
          case "state": return controller.state();
          case "guard": {
            const guard = await controller.guardTool("bash", abort.signal);
            return guard ? { allowed: false, reason: guard.reason } : { allowed: true };
          }
          case "context": return controller.context(abort.signal);
          case "shutdown": return { stopped: true };
          default: throw failure("unknown_operation", "Unknown session operation");
        }
      };
      const uncertain = mutationOps.has(request.op);
      void dispatch().then((result) => reply(result, undefined, request.op === "shutdown", uncertain),
        (error) => reply(undefined, error instanceof ForgeError ? error : failure("session_error", "Session operation unavailable"), false, uncertain));
    });
  });
  // Native transport errors are never printed (they may include local paths).
  server.on("error", () => {});
  let resolveDone!: () => void;
  let rejectDone!: (error: unknown) => void;
  const done = new Promise<void>((resolve, reject) => { resolveDone = resolve; rejectDone = reject; });
  void done.catch(() => {});
  const cleanup = async () => {
    if (published) {
      const current = await statOrMissing(socketPath);
      if (current?.isSocket() && sameFile(current, published)) await unlink(socketPath);
    }
    if (staging && stagingStat) {
      const current = await statOrMissing(staging);
      if (current?.isDirectory() && sameFile(current, stagingStat)) await rmdir(staging);
    }
  };
  const stopServer = () => new Promise<void>((resolve) => {
    server.close(() => resolve());
    for (const socket of clients) socket.destroy();
  });
  function close(): Promise<void> {
    if (closing) return closing;
    confirmed = undefined;
    closing = (async () => {
      let failed = false;
      const stopped = stopServer();
      try { await controller.shutdown("session"); } catch { failed = true; }
      await stopped;
      try { await cleanup(); } catch { failed = true; }
      if (failed) {
        const error = failure("session_error", "Session cleanup unavailable");
        rejectDone(error); throw error;
      }
      resolveDone();
    })();
    return closing;
  }
  try {
    staging = await mkdtemp(join(dirname(socketPath), ".f-"));
    await chmod(staging, 0o700);
    stagingStat = await lstat(staging);
    const bindPath = join(staging, "s");
    if (Buffer.byteLength(bindPath) > 100) throw failure("unsafe_socket_path", "Socket directory is too long for safe private binding; choose a shorter directory");
    checkPath(bindPath);
    await new Promise<void>((resolve, reject) => {
      server.once("error", reject);
      server.listen(bindPath, () => { server.removeListener("error", reject); resolve(); });
    });
    await chmod(bindPath, 0o600);
    const bound = await lstat(bindPath);
    if (!sameFile(parent, await parentStat(socketPath))) throw failure("unsafe_socket_path", "Socket parent changed during startup");
    await link(bindPath, socketPath);
    published = bound;
    server.on("error", () => { void close().catch(() => {}); });
    return { close, done };
  } catch (error) {
    await stopServer();
    try { await cleanup(); } catch { /* Never recursively delete unexpected paths. */ }
    if (error instanceof ForgeError) throw error;
    if ((error as NodeJS.ErrnoException).code === "EEXIST" || (error as NodeJS.ErrnoException).code === "EADDRINUSE") {
      throw failure("socket_occupied", "Socket path already exists; stale sockets must be removed explicitly");
    }
    throw failure("session_unavailable", "Cannot start session at the requested safe socket path");
  }
}

/** One JSONL exchange; never creates or restores a runtime or reads credentials. */
export async function requestSession(socketPath: string, request: Request): Promise<any> {
  await parentStat(socketPath);
  try {
    const stat = await statOrMissing(socketPath);
    if (!stat) throw failure("session_unavailable", "Session socket is unavailable; start a fresh foreground session");
    if (!stat.isSocket() || stat.isSymbolicLink() || stat.uid !== process.getuid!() || (stat.mode & 0o7777) !== 0o600) {
      throw failure("unsafe_socket_path", "Require an owned 0600 Unix socket, not a symlink");
    }
  } catch (error) {
    if (error instanceof ForgeError) throw error;
    throw failure("session_unavailable", "Cannot inspect session socket");
  }
  let line: string;
  try { checkRequest(request); line = JSON.stringify(request) + "\n"; }
  catch { throw failure("invalid_request", "Require a serializable session request; control operations take no params"); }
  if (Buffer.byteLength(line) > MAX_REQUEST) throw failure("request_too_large", "Session request exceeds 1 MiB");
  const mutation = mutationOps.has(request.op); // Freeze alongside the serialized request.
  return new Promise((resolve, reject) => {
    const socket = createConnection(socketPath);
    let sent = false;
    const uncertain = () => sent && mutation;
    let settled = false;
    let size = 0;
    let chunks: Buffer[] = [];
    let writeTimer: ReturnType<typeof setTimeout> | undefined;
    const finish = (error?: ForgeError, result?: unknown) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer); clearTimeout(writeTimer); socket.destroy(); chunks = [];
      if (error) reject(error); else resolve(result);
    };
    const timer = setTimeout(() => finish(failure("session_timeout", "Session exchange timed out; check state before retrying", uncertain())), REQUEST_TIMEOUT);
    socket.on("connect", () => {
      sent = true;
      writeTimer = setTimeout(() => finish(failure("session_timeout", "Session request write timed out; check state before retrying", uncertain())), IO_TIMEOUT);
      socket.write(line, () => clearTimeout(writeTimer));
    });
    socket.on("error", () => finish(failure("session_unavailable", "Session connection unavailable; check state before retrying", uncertain())));
    socket.on("end", () => finish(failure("invalid_response", "Session ended without a complete response; check state before retrying", uncertain())));
    socket.on("close", () => { if (!settled) finish(failure("session_unavailable", "Session connection closed; check state before retrying", uncertain())); });
    socket.on("data", (chunk: Buffer) => {
      size += chunk.length;
      if (size > MAX_RESPONSE) { finish(failure("response_too_large", "Session response exceeds 8 MiB; check state before retrying", uncertain())); return; }
      chunks.push(chunk);
      const newline = chunk.indexOf(10);
      if (newline < 0) return;
      try {
        if (newline !== chunk.length - 1) throw new Error();
        const response = parse(Buffer.concat(chunks, size).subarray(0, -1));
        if (!record(response)) throw new Error();
        if (response.ok === true && Object.hasOwn(response, "result") && Object.keys(response).every((key) => key === "ok" || key === "result")) {
          finish(undefined, response.result); return;
        }
        const error = response.error;
        const responseStatus = response.status;
        const responseCode = record(error) ? errorCode(error.code, "") : "";
        if (response.ok !== false || Object.keys(response).some((key) => !["ok", "error", "status"].includes(key)) || !record(error) ||
            !responseCode || typeof error.message !== "string" ||
            (responseStatus !== undefined && (typeof responseStatus !== "number" || !Number.isInteger(responseStatus) || responseStatus < 100 || responseStatus > 599)) ||
            (error.ambiguous !== undefined && typeof error.ambiguous !== "boolean") ||
            (error.hint !== undefined && typeof error.hint !== "string") ||
            (error.data !== undefined && (!record(error.data) || Array.isArray(error.data))) ||
            Object.keys(error).some((key) => !["code", "message", "ambiguous", "hint", "data"].includes(key))) throw new Error();
        finish(failure(responseCode, error.message, error.ambiguous === true, typeof responseStatus === "number" ? responseStatus : 0,
          { ...(typeof error.hint === "string" ? { hint: error.hint } : {}), ...(record(error.data) ? { data: error.data } : {}) }));
      } catch { finish(failure("invalid_response", "Invalid session response; check state before retrying", uncertain())); }
    });
  });
}
