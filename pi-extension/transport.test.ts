import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { createServer, type Server } from "node:http";
import { once } from "node:events";
import { test } from "node:test";
import { Controller, ForgeError, type Clock } from "./controller.ts";

const config = { url: "http://127.0.0.1:7779", ttlSeconds: 60, workerToken: "static-do-not-disclose" };
function claimResponse(init?: RequestInit, overrides = {}) {
  const body = JSON.parse(String(init?.body));
  const now = Date.parse("2035-01-01T00:00:00Z");
  return {
    granted: true, execution_token: "execution-private", execution_id: "opaque-derived-subject",
    server_now: new Date(now).toISOString(),
    lease: { claim_uid: "C1", issue_uid: "I1", holder: "opaque-derived-subject", holder_instance_uid: "instance", client_kind: "pi", expires_at: new Date(now + (body.ttl_seconds ?? 60) * 1000).toISOString() },
    ...overrides,
  };
}
const json = (body: unknown) => new Response(JSON.stringify(body));
async function listen(server: Server) {
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const address = server.address();
  assert.ok(address && typeof address !== "string");
  return `http://127.0.0.1:${address.port}`;
}
async function closeServer(server: Server) {
  server.closeAllConnections();
  await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()));
}

test("real fetch refuses redirects instead of forwarding worker credentials", async () => {
  let targetCalls = 0;
  const target = createServer((_req, res) => { targetCalls++; res.end("{}"); });
  const targetURL = await listen(target);
  const source = createServer((req, res) => {
    assert.equal(req.method, "POST");
    assert.equal(req.headers.authorization, `Bearer ${config.workerToken}`);
    res.writeHead(307, { Location: `${targetURL}/credential-trap` }); res.end();
  });
  const sourceURL = await listen(source);
  const controller = new Controller({ ...config, url: sourceURL }, { sessionId: randomUUID() });
  try {
    await assert.rejects(controller.execute("issue_list", {}), (error: unknown) => error instanceof ForgeError && !error.ambiguous && error.code === "transport_error");
    assert.equal(targetCalls, 0);
  } finally { await controller.shutdown(); await closeServer(source); await closeServer(target); }
});

test("deadline charges entire RTT using server time, independent of wall clock", async () => {
  let monotonic = 0;
  const clock: Clock = { now: () => monotonic, setTimeout: () => 1, clearTimeout: () => {} };
  const controller = new Controller(config, { sessionId: randomUUID(), clock, fetch: async (_url, init) => {
    const response = claimResponse(init);
    monotonic += 30_000;
    return json(response);
  } });
  await controller.execute("issue_claim", { ref: "#1" });
  assert.equal(controller.state().mode, "live");
  monotonic = 59_001;
  assert.equal(controller.state().mode, "stop-editing");
  assert.equal((await controller.guardTool("edit"))?.block, true);
  await controller.shutdown();
});

test("delayed renew after conservative deadline cannot resurrect execution", async () => {
  let monotonic = 0;
  const clock: Clock = { now: () => monotonic, setTimeout: () => 1, clearTimeout: () => {} };
  let claim: ReturnType<typeof claimResponse>;
  const controller = new Controller(config, { sessionId: randomUUID(), clock, fetch: async (url, init) => {
    if (String(url).endsWith("issue_claim")) { claim = claimResponse(init); return json(claim); }
    monotonic = 60_000;
    return json({ granted: true, lease: { ...claim.lease, expires_at: "2035-01-01T00:02:00Z" }, server_now: "2035-01-01T00:00:30Z" });
  } });
  await controller.execute("issue_claim", { ref: "#1" });
  monotonic = 30_000;
  await assert.rejects(controller.execute("issue_renew", { ref: "I1" }), /expired/);
  assert.equal((await controller.guardTool("write"))?.block, true);
  await controller.shutdown();
});

test("shutdown release is bounded even if transport ignores AbortSignal", async () => {
  const requests: string[] = [];
  const controller = new Controller(config, { sessionId: randomUUID(), shutdownTimeoutMs: 20, fetch: async (url, init) => {
    requests.push(String(url));
    if (String(url).endsWith("issue_claim")) return json(claimResponse(init));
    return new Promise<Response>(() => {});
  } });
  await controller.execute("issue_claim", { ref: "#1" });
  const began = performance.now();
  await controller.shutdown("reload");
  assert.ok(performance.now() - began < 1000);
  assert.equal(controller.state().mode, "stopped");
  assert.equal(requests.length, 2);
});

test("shutdown during an unconfirmed acquire prevents a late response from restoring execution", async () => {
  let finish: () => void = () => {};
  let sent: () => void = () => {};
  const started = new Promise<void>((resolve) => { sent = resolve; });
  const controller = new Controller(config, { sessionId: randomUUID(), fetch: async (_url, init) => {
    const response = claimResponse(init);
    const delayed = new Promise<Response>((resolve) => { finish = () => resolve(json(response)); });
    sent(); return delayed;
  } });
  const attempt = controller.execute("issue_claim", { ref: "#1" });
  const rejected = assert.rejects(attempt);
  await started;
  await controller.shutdown("fork");
  finish();
  await rejected;
  assert.equal(controller.state().mode, "stopped");
  assert.equal(controller.state().pendingClaim, false);
  assert.equal((await controller.guardTool("edit"))?.block, true);
});

test("serialized concurrent claims cannot bind two issues", async () => {
  const bodies: any[] = [];
  const controller = new Controller(config, { sessionId: randomUUID(), fetch: async (url, init) => {
    bodies.push(JSON.parse(String(init?.body)));
    return json(String(url).endsWith("issue_claim") ? claimResponse(init) : { granted: true });
  } });
  const results = await Promise.allSettled([
    controller.execute("issue_claim", { ref: "#1" }),
    controller.execute("issue_claim", { ref: "#2" }),
  ]);
  assert.equal(results[0].status, "fulfilled");
  assert.equal(results[1].status, "rejected");
  assert.equal(bodies.length, 1);
  await controller.shutdown();
});
