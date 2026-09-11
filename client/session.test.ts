import assert from "node:assert/strict";
import { chmod, link, lstat, mkdtemp, readFile, realpath, rm, symlink, unlink, writeFile } from "node:fs/promises";
import { createConnection, createServer, type Server, type Socket } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test, type TestContext } from "node:test";
import { Controller, ForgeError } from "../pi-extension/controller.ts";
import { toolSchemas } from "../pi-extension/schemas.ts";
import { requestSession, startSession } from "./session.ts";

async function directory(t: TestContext) {
  const dir = await mkdtemp(join(await realpath(tmpdir()), "forge-s-"));
  await chmod(dir, 0o700);
  t.after(() => rm(dir, { recursive: true, force: true }));
  return { dir, path: join(dir, "s") };
}
function mockController() {
  const sanitizer = new Controller({ url: "http://127.0.0.1:7347", workerToken: "worker-private", ttlSeconds: 60 }, { sessionId: "00000000-0000-4000-8000-000000000000" });
  const calls: unknown[][] = [];
  const mock = {
    calls,
    execute: async (name: string, params: unknown): Promise<Record<string, any>> => { calls.push([name, params]); return { name, params, execution_token: "execution-private", echoed: "worker-private execution-private" }; },
    state: () => ({ mode: "idle", pendingClaim: false, pendingClose: false }),
    guardTool: async (name: string): Promise<{ block: true; reason: string } | undefined> => { calls.push(["guard", name]); return { block: true, reason: "No lease worker-private" }; },
    context: async () => "safe context worker-private",
    retryPending: async (): Promise<Record<string, any>> => { calls.push(["retry"]); throw new ForgeError("no_pending", "No pending request"); },
    shutdown: async () => { calls.push(["shutdown"]); },
    sanitize: <T>(value: T) => sanitizer.sanitize(value),
  };
  return { mock, controller: mock as unknown as Controller };
}
async function raw(path: string, payload: string | Buffer) {
  return new Promise<any>((resolve, reject) => {
    const socket = createConnection(path);
    const chunks: Buffer[] = [];
    socket.on("connect", () => socket.end(payload));
    socket.on("data", (chunk: Buffer) => chunks.push(chunk));
    socket.on("error", reject);
    socket.on("end", () => {
      try {
        const text = Buffer.concat(chunks).toString();
        assert.equal(text.split("\n").length, 2, "exactly one response line");
        resolve(JSON.parse(text));
      } catch (error) { reject(error); }
    });
  });
}
async function listen(server: Server, path: string) {
  await new Promise<void>((resolve, reject) => { server.once("error", reject); server.listen(path, resolve); });
}
async function stop(server: Server) {
  await new Promise<void>((resolve) => server.close(() => resolve()));
}
const code = (expected: string) => (error: unknown) => error instanceof ForgeError && error.code === expected;

test("broker owns a 0600 socket, routes all tools, sanitizes output and serves concurrent requests", async (t) => {
  const { path } = await directory(t); const { mock, controller } = mockController();
  const broker = await startSession(path, controller); t.after(broker.close);
  assert.equal((await lstat(path)).mode & 0o7777, 0o600);
  assert.deepEqual(await requestSession(path, { op: "state" }), mock.state());
  const operations = Object.keys(toolSchemas);
  const results = await Promise.all(operations.map((op) => requestSession(path, { op, params: { ref: "#1" } })));
  assert.deepEqual(mock.calls.map(([name]) => name).sort(), [...operations].sort());
  assert.ok(!JSON.stringify(results).includes("private"));
  assert.equal(results[0].execution_token, undefined);
  assert.deepEqual(await requestSession(path, { op: "guard" }), { allowed: false, reason: "No lease [REDACTED]" });
  mock.guardTool = async () => undefined;
  assert.deepEqual(await requestSession(path, { op: "guard" }), { allowed: true });
  assert.equal(await requestSession(path, { op: "context" }), "safe context [REDACTED]");
  mock.state = () => ({ mode: "idle", pendingClaim: false, pendingClose: true });
  mock.retryPending = async () => ({ retried: true });
  assert.deepEqual(await requestSession(path, { op: "retry" }), { retried: true });
  await broker.close(); await broker.done;
  await assert.rejects(lstat(path), { code: "ENOENT" });
  assert.equal(mock.calls.filter(([name]) => name === "shutdown").length, 1);
});

test("optional read facade allows only timeline/project/health, with sanitized output and no admin fallback", async (t) => {
  const { path } = await directory(t); const { controller } = mockController();
  const extras: unknown[][] = [];
  const broker = await startSession(path, controller, async (op, params) => { extras.push([op, params]); return { op, params, worker_token: "worker-private" }; });
  t.after(broker.close);
  for (const op of ["issue_timeline", "project", "health"]) {
    assert.deepEqual(await requestSession(path, { op, params: { ref: "#1" } }), { op, params: { ref: "#1" } });
  }
  assert.equal(extras.length, 3);
  await assert.rejects(requestSession(path, { op: "force_release", params: {} }), code("unknown_operation"));
  assert.equal(extras.length, 3);
  await broker.close();
  const plain = await startSession(path, mockController().controller); t.after(plain.close);
  await assert.rejects(requestSession(path, { op: "health" }), code("unsupported_operation"));
});

test("unsafe paths and permissions are refused without repairing or deleting them", async (t) => {
  const { path, dir } = await directory(t); const { controller, mock } = mockController();
  for (const bad of ["relative.sock", "", `${dir}/null\0sock`, `${dir}/${"x".repeat(101)}`, `${dir}/${"界".repeat(35)}`]) {
    await assert.rejects(startSession(bad, controller), code("unsafe_socket_path"));
    await assert.rejects(requestSession(bad, { op: "state" }), code("unsafe_socket_path"));
  }
  await chmod(dir, 0o755);
  await assert.rejects(startSession(path, controller), code("unsafe_socket_path"));
  await assert.rejects(requestSession(path, { op: "state" }), code("unsafe_socket_path"));
  assert.equal((await lstat(dir)).mode & 0o7777, 0o755);
  await chmod(dir, 0o700);
  const alias = `${dir}-alias`;
  await symlink(dir, alias); t.after(() => unlink(alias));
  await assert.rejects(startSession(join(alias, "s"), controller), code("unsafe_socket_path"));
  await assert.rejects(requestSession(join(alias, "s"), { op: "state" }), code("unsafe_socket_path"));
  assert.deepEqual(mock.calls, []);
});

test("existing file, symlink, active socket and stale socket are never replaced", async (t) => {
  const { path, dir } = await directory(t); const { controller } = mockController();
  await writeFile(path, "keep");
  await assert.rejects(startSession(path, controller), code("socket_occupied"));
  assert.equal(await readFile(path, "utf8"), "keep"); await unlink(path);
  const target = join(dir, "target"); await writeFile(target, "keep"); await symlink(target, path);
  await assert.rejects(startSession(path, controller), code("socket_occupied"));
  assert.ok((await lstat(path)).isSymbolicLink()); await unlink(path);
  const other = createServer(); await listen(other, path); await chmod(path, 0o600);
  await assert.rejects(startSession(path, controller), code("socket_occupied"));
  const stale = join(dir, "stale"); await link(path, stale); await stop(other);
  await assert.rejects(startSession(stale, controller), code("socket_occupied"));
  assert.ok((await lstat(stale)).isSocket());
  await assert.rejects(requestSession(stale, { op: "state" }), code("session_unavailable"));
});

test("wrong-UID parents are refused even with restrictive permissions", async (t) => {
  const { path } = await directory(t);
  const uid = process.getuid!();
  const mocked = t.mock.method(process as NodeJS.Process & { getuid(): number }, "getuid", () => uid + 1);
  try {
    await assert.rejects(startSession(path, mockController().controller), code("unsafe_socket_path"));
    await assert.rejects(requestSession(path, { op: "state" }), code("unsafe_socket_path"));
  } finally { mocked.mock.restore(); }
});

test("request clients refuse socket symlinks and permissive socket modes", async (t) => {
  const { path, dir } = await directory(t); const { controller } = mockController();
  const broker = await startSession(path, controller); t.after(broker.close);
  const alias = join(dir, "alias"); await symlink(path, alias);
  await assert.rejects(requestSession(alias, { op: "state" }), code("unsafe_socket_path"));
  await chmod(path, 0o666);
  await assert.rejects(requestSession(path, { op: "state" }), code("unsafe_socket_path"));
  await chmod(path, 0o600);
});

test("reject malformed, extra-field, multiline and oversized requests without dispatch", async (t) => {
  const { path } = await directory(t); const { controller, mock } = mockController();
  const broker = await startSession(path, controller); t.after(broker.close);
  for (const payload of ["{\n", "null\n", "[]\n", "{}\n", '{"op":12}\n', '{"op":"state","token":"secret"}\n', '{"op":"state"}\n{"op":"shutdown"}\n', '{"op":"retry","params":{}}\n', '{"op":"guard","params":{"tool":"read"}}\n', Buffer.from([0xff, 10])]) {
    const response = await raw(path, payload);
    assert.equal(response.ok, false);
    assert.equal(response.error.code, "invalid_request");
    assert.ok(!JSON.stringify(response).includes("secret"));
  }
  assert.equal((await raw(path, "x".repeat(1024 * 1024 + 1) + "\n")).error.code, "request_too_large");
  await assert.rejects(requestSession(path, { op: "issue_comment", params: { body: "x".repeat(1024 * 1024) } }), code("request_too_large"));
  assert.equal((await raw(path, '{"op":"__proto__"}\n')).error.code, "unknown_operation");
  assert.deepEqual(mock.calls, []);
  assert.deepEqual(await requestSession(path, { op: "state" }), mock.state());
});

test("request errors preserve only sanitized ForgeError fields; native failures are generic", async (t) => {
  const { path } = await directory(t); const { controller, mock } = mockController();
  const broker = await startSession(path, controller); t.after(broker.close);
  mock.execute = async () => { throw new ForgeError("uncertain", "worker-private Bearer invisible", 503, true, { hint: "retry safely", data: { cleanup_pending: false, execution_token: "hidden" } }); };
  await assert.rejects(requestSession(path, { op: "issue_claim", params: { ref: "#1" } }), (error: unknown) => {
    assert.ok(error instanceof ForgeError); assert.equal(error.code, "uncertain"); assert.equal(error.status, 503); assert.equal(error.hint, "retry safely");
    assert.deepEqual(error.data, { cleanup_pending: false }); assert.equal(error.ambiguous, true);
    assert.ok(!error.message.includes("private")); assert.ok(!error.message.includes("invisible"));
    assert.equal((error as any).cause, undefined); return true;
  });
  mock.execute = async () => { throw new Error("secret native URL and header"); };
  const response = await raw(path, '{"op":"issue_list","params":{}}\n');
  assert.equal(response.ok, false); assert.equal(response.error.code, "session_error");
  assert.ok(!JSON.stringify(response).includes("secret native"));
  for (const thrown of [undefined, null, false, 0]) {
    mock.execute = async () => { throw thrown; };
    await assert.rejects(requestSession(path, { op: "issue_list", params: {} }), code("session_error"));
  }
  mock.context = async () => "x".repeat(8 * 1024 * 1024);
  await assert.rejects(requestSession(path, { op: "context" }), code("response_too_large"));
});

test("client rejects unavailable, malformed, incomplete and oversized broker replies safely", async (t) => {
  const { path } = await directory(t);
  await assert.rejects(requestSession(path, { op: "state" }), code("session_unavailable"));
  for (const payload of ["secret native garbage\n", '{"ok":true}\n', '{"ok":false,"error":{"code":12,"message":"secret"}}\n', '{"ok":true,"result":1}', '{"ok":true,"result":1}\n{}\n', "x".repeat(8 * 1024 * 1024 + 1)]) {
    const server = createServer((socket) => { socket.on("error", () => {}); socket.once("data", () => socket.end(payload)); });
    await listen(server, path); await chmod(path, 0o600);
    try {
      await assert.rejects(requestSession(path, { op: "state" }), (error: unknown) => error instanceof ForgeError && !error.message.includes("secret") && ["invalid_response", "response_too_large"].includes(error.code));
    } finally { await stop(server); }
  }
  const server = createServer((socket) => { socket.on("error", () => {}); socket.once("data", () => socket.end('{"ok":false,"error":{"code":"remote_error","message":"Bearer private-token","hint":"Bearer private-token","data":{"execution_token":"private-token","safe":true}}}\n')); });
  await listen(server, path); await chmod(path, 0o600);
  try {
    await assert.rejects(requestSession(path, { op: "state" }), (error: unknown) => error instanceof ForgeError && !error.message.includes("private-token") && !String(error.hint).includes("private-token") && !JSON.stringify(error.data).includes("private-token"));
  } finally { await stop(server); }
});

test("shutdown replies first, is idempotent, and idle/slow clients cannot hold close open", async (t) => {
  const { path } = await directory(t); const { controller, mock } = mockController();
  const broker = await startSession(path, controller); t.after(broker.close);
  const slow = createConnection(path); slow.on("error", () => {});
  await new Promise<void>((resolve) => slow.once("connect", resolve));
  slow.write('{"op":');
  assert.deepEqual(await requestSession(path, { op: "state" }), mock.state());
  const started = performance.now();
  assert.deepEqual(await requestSession(path, { op: "shutdown" }), { stopped: true });
  await broker.done; await broker.close();
  assert.ok(performance.now() - started < 1500);
  assert.equal(mock.calls.filter(([name]) => name === "shutdown").length, 1);
  slow.destroy();
  await assert.rejects(requestSession(path, { op: "state" }), code("session_unavailable"));
});

test("a client not reading a large response cannot block other requests or close", async (t) => {
  const { path } = await directory(t); const { controller, mock } = mockController();
  let dispatched!: () => void;
  const handling = new Promise<void>((resolve) => { dispatched = resolve; });
  mock.context = async () => { dispatched(); return "x".repeat(7 * 1024 * 1024); };
  const broker = await startSession(path, controller); t.after(broker.close);
  const slow = createConnection(path); slow.on("error", () => {}); t.after(() => slow.destroy());
  await new Promise<void>((resolve) => slow.once("connect", resolve));
  slow.pause(); slow.write('{"op":"context"}\n'); await handling;
  assert.deepEqual(await requestSession(path, { op: "state" }), mock.state());
  const started = performance.now(); await broker.close(); await broker.done;
  assert.ok(performance.now() - started < 1500);
});

test("shutdown failures are sanitized and still remove the owned socket", async (t) => {
  const { path } = await directory(t); const { controller, mock } = mockController();
  const broker = await startSession(path, controller);
  mock.shutdown = async () => { throw new Error("sensitive cleanup detail"); };
  const done = assert.rejects(broker.done, code("session_error"));
  await assert.rejects(broker.close(), (error: unknown) => error instanceof ForgeError && !error.message.includes("sensitive"));
  await done;
  await assert.rejects(lstat(path), { code: "ENOENT" });
});

test("cleanup does not remove a replacement file or replacement broker socket", async (t) => {
  const { path } = await directory(t); const { controller } = mockController();
  const first = await startSession(path, controller); t.after(first.close);
  await unlink(path); await writeFile(path, "replacement");
  await first.close(); assert.equal(await readFile(path, "utf8"), "replacement"); await unlink(path);
  const second = await startSession(path, mockController().controller); t.after(second.close);
  await unlink(path);
  const third = await startSession(path, mockController().controller); t.after(third.close);
  await second.close();
  assert.equal((await requestSession(path, { op: "state" })).mode, "idle");
});

test("concurrent starts have one winner and failed start cannot remove its socket", async (t) => {
  const { path } = await directory(t);
  const results = await Promise.allSettled([startSession(path, mockController().controller), startSession(path, mockController().controller)]);
  assert.equal(results.filter((r) => r.status === "fulfilled").length, 1);
  const success = results.find((r) => r.status === "fulfilled")! as PromiseFulfilledResult<Awaited<ReturnType<typeof startSession>>>;
  t.after(success.value.close);
  assert.equal((await requestSession(path, { op: "state" })).mode, "idle");
});

test("lost IPC claim/close confirmations replay sanitized snapshots without executing them again", async (t) => {
  for (const op of ["issue_claim", "issue_close"]) await t.test(op, async (t) => {
    const { path } = await directory(t); const { controller, mock } = mockController();
    const receipt = { granted: true, changed: true, issue: { uid: "confirmed", status: "closed" }, execution_token: "execution-private", attempt_id: "private-attempt", echoed: "worker-private execution-private" };
    let executed = 0;
    const broker = await startSession(path, controller); t.after(broker.close);
    const socket = createConnection(path); socket.on("error", () => {});
    const lost = new Promise<void>((resolve) => socket.once("close", resolve));
    mock.execute = async () => { executed++; socket.destroy(); return receipt; };
    await new Promise<void>((resolve) => socket.once("connect", resolve));
    socket.write(JSON.stringify({ op, params: { ref: "#1" } }) + "\n");
    await lost;
    await requestSession(path, { op: "state" }); // HTTP/Controller is already confirmed, with no pending proof.
    receipt.issue.uid = "mutated-outside-broker";
    const replay = await requestSession(path, { op: "retry" });
    assert.equal(replay.issue.uid, "confirmed");
    assert.equal(replay.client_replayed, true);
    assert.equal(replay.current_state_not_refreshed, true);
    assert.ok(!JSON.stringify(replay).includes("private"));
    assert.equal(executed, 1);
    assert.deepEqual(await requestSession(path, { op: "retry" }), replay);
    assert.equal(executed, 1);
    assert.equal(mock.calls.filter(([name]) => name === "retry").length, 0);
    await broker.close();
    const fresh = await startSession(path, mockController().controller); t.after(fresh.close);
    await assert.rejects(requestSession(path, { op: "retry" }), code("no_pending"));
  });
});

test("pending Controller retry takes priority and replaces confirmation cache; definite failure clears it", async (t) => {
  const { path } = await directory(t); const { controller, mock } = mockController();
  mock.execute = async () => ({ issue: { uid: "old" }, changed: true });
  const broker = await startSession(path, controller); t.after(broker.close);
  await requestSession(path, { op: "issue_close", params: {} });
  let pending = true; let retries = 0;
  mock.state = () => ({ mode: "idle", pendingClaim: false, pendingClose: pending });
  mock.retryPending = async () => { retries++; pending = false; return { issue: { uid: "pending" }, changed: true, execution_token: "private-proof" }; };
  const confirmed = await requestSession(path, { op: "retry" });
  assert.equal(confirmed.issue.uid, "pending");
  assert.equal(confirmed.client_replayed, undefined);
  assert.equal((await requestSession(path, { op: "retry" })).issue.uid, "pending");
  assert.equal(retries, 1);
  pending = true;
  mock.retryPending = async () => { pending = false; throw new ForgeError("claim_lost", "Definite failure", 409); };
  await assert.rejects(requestSession(path, { op: "retry" }), code("claim_lost"));
  await assert.rejects(requestSession(path, { op: "retry" }), code("no_pending"));
});

test("read/comment preserve confirmations; every new execution starts by invalidating the old result", async (t) => {
  const { path } = await directory(t); const { controller, mock } = mockController();
  mock.execute = async () => ({ issue: { uid: "confirmed" }, changed: true });
  const broker = await startSession(path, controller, async () => ({ read: true })); t.after(broker.close);
  await requestSession(path, { op: "issue_close", params: {} });
  for (const op of ["state", "guard", "context", "issue_get", "issue_comment", "issue_link", "issue_create", "issue_timeline", "project", "health"]) {
    await requestSession(path, { op });
    assert.equal((await requestSession(path, { op: "retry" })).issue.uid, "confirmed");
  }
  for (const op of ["issue_claim", "issue_renew", "issue_release", "issue_close"]) {
    mock.execute = async () => ({ issue: { uid: "confirmed" }, changed: true });
    await requestSession(path, { op: "issue_close", params: {} });
    mock.execute = async () => { throw new ForgeError("definite_failure", "No mutation", 409); };
    await assert.rejects(requestSession(path, { op, params: {} }), code("definite_failure"));
    await assert.rejects(requestSession(path, { op: "retry" }), code("no_pending"));
  }
  for (const op of ["issue_renew", "issue_release"]) {
    mock.execute = async () => ({ granted: true });
    await requestSession(path, { op: "issue_claim", params: {} });
    await requestSession(path, { op, params: {} });
    await assert.rejects(requestSession(path, { op: "retry" }), code("no_pending"));
  }
  mock.execute = async () => ({ granted: false });
  await requestSession(path, { op: "issue_claim", params: {} });
  await assert.rejects(requestSession(path, { op: "retry" }), code("no_pending"));
});

test("confirmation replay waits for an earlier execution and never returns the old tenure's cache", async (t) => {
  for (const denied of [false, true]) await t.test(String(denied), async (t) => {
    const { path } = await directory(t); const { controller, mock } = mockController();
    mock.execute = async () => ({ issue: { uid: "old" } });
    const broker = await startSession(path, controller); t.after(broker.close);
    await requestSession(path, { op: "issue_close", params: {} });
    let release!: () => void; let entered!: () => void;
    const gate = new Promise<void>((resolve) => { release = resolve; });
    const started = new Promise<void>((resolve) => { entered = resolve; });
    mock.execute = async () => { entered(); await gate; if (denied) throw new ForgeError("denied", "New attempt denied", 409); return { granted: true, issue: { uid: "new" } }; };
    const next = requestSession(path, { op: "issue_claim", params: {} });
    const result = denied ? assert.rejects(next, code("denied")) : next;
    void result.catch(() => {}); t.after(() => release());
    await started;
    let settled = false;
    const retry = requestSession(path, { op: "retry" }).then((value) => { settled = true; return value; }, (error) => { settled = true; throw error; });
    const replay = denied ? assert.rejects(retry, code("no_pending")) : retry;
    void replay.catch(() => {});
    await new Promise<void>((resolve) => setImmediate(resolve));
    assert.equal(settled, false, "retry cannot bypass the in-flight execution");
    release(); await result;
    const value = await replay;
    if (!denied) { assert.equal(value.issue.uid, "new"); assert.equal(value.client_replayed, true); }
  });
});

test("confirmed result cache is bounded and does not retain an oversized result", async (t) => {
  const { path } = await directory(t); const { controller, mock } = mockController();
  const broker = await startSession(path, controller); t.after(broker.close);
  mock.execute = async () => ({ issue: { uid: "old" } });
  await requestSession(path, { op: "issue_close", params: {} });
  mock.execute = async () => ({ issue: { body: "x".repeat(8 * 1024 * 1024) } });
  await assert.rejects(requestSession(path, { op: "issue_close", params: {} }), code("response_too_large"));
  await assert.rejects(requestSession(path, { op: "retry" }), code("no_pending"));
});

test("sent mutation transport/parse failures are ambiguous, reads and definite errors are not; never resend", async (t) => {
  const { path } = await directory(t);
  await assert.rejects(requestSession(path, { op: "issue_close" }), (error: unknown) => error instanceof ForgeError && !error.ambiguous);
  const mutations = ["issue_claim", "issue_renew", "issue_release", "issue_close", "issue_create", "issue_comment", "issue_link", "retry", "shutdown"];
  const cases = [...mutations.map((op) => ({ op, payload: "invalid\n", ambiguous: true })),
    ...[undefined, '{"ok":true}', '{"ok":true}\n', '{"ok":false,"error":null}\n', "x".repeat(8 * 1024 * 1024 + 1)].map((payload) => ({ op: "issue_close", payload, ambiguous: true })),
    { op: "issue_get", payload: "invalid\n", ambiguous: false },
    { op: "issue_close", payload: '{"ok":false,"error":{"code":"denied","message":"No mutation"}}\n', ambiguous: false }];
  for (const { op, payload, ambiguous } of cases) {
    let calls = 0;
    const server = createServer((socket) => {
      socket.on("error", () => {});
      socket.once("data", () => { calls++; if (payload === undefined) socket.destroy(); else socket.end(payload); });
    });
    await listen(server, path); await chmod(path, 0o600);
    try {
      await assert.rejects(requestSession(path, { op }), (error: unknown) => error instanceof ForgeError && error.ambiguous === ambiguous);
      assert.equal(calls, 1);
    } finally { await stop(server); }
  }
});

test("IPC ambiguity classification uses the sent operation, not a later-mutated caller object", async (t) => {
  const { path } = await directory(t);
  const request = { op: "issue_close" };
  const server = createServer((socket) => {
    socket.on("error", () => {});
    socket.once("data", () => { request.op = "issue_get"; socket.end("invalid\n"); });
  });
  await listen(server, path); await chmod(path, 0o600); t.after(() => stop(server));
  await assert.rejects(requestSession(path, request), (error: unknown) => error instanceof ForgeError && error.ambiguous);
});

test("read deadlines close incomplete requests; response deadlines safely abort clients", async (t) => {
  const { path } = await directory(t); const { controller } = mockController();
  const broker = await startSession(path, controller); t.after(broker.close);
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const socket = createConnection(path); socket.on("error", () => {});
  await new Promise<void>((resolve) => socket.once("connect", resolve));
  const ended = new Promise<void>((resolve) => { socket.resume(); socket.once("close", resolve); });
  t.mock.timers.tick(30_001); await ended;
  await broker.close();
  let accepted!: (socket: Socket) => void;
  const connected = new Promise<Socket>((resolve) => { accepted = resolve; });
  const server = createServer((client) => { client.on("error", () => {}); client.once("data", () => accepted(client)); });
  await listen(server, path); await chmod(path, 0o600); t.after(() => stop(server));
  const pending = requestSession(path, { op: "issue_close", params: {} });
  const rejected = assert.rejects(pending, (error: unknown) => error instanceof ForgeError && error.code === "session_timeout" && error.ambiguous);
  const peer = await connected;
  t.mock.timers.tick(30_001); await rejected; peer.destroy(); await stop(server);
});
