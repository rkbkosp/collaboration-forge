import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { test } from "node:test";
import { Controller, ForgeError, type Clock } from "./controller.ts";

class FakeClock implements Clock {
  time = 0;
  next = 0;
  timers = new Map<number, { fn: () => void; at: number }>();
  now = () => this.time;
  setTimeout = (fn: () => void, ms: number) => {
    const id = ++this.next;
    this.timers.set(id, { fn, at: this.time + ms });
    return id;
  };
  clearTimeout = (id: unknown) => { this.timers.delete(id as number); };
  async advance(ms: number) {
    this.time += ms;
    for (const [id, timer] of [...this.timers]) {
      if (timer.at <= this.time) { this.timers.delete(id); timer.fn(); }
    }
    await new Promise<void>((resolve) => setImmediate(resolve));
  }
}

const workerToken = "private-worker-secret";
const epoch = Date.parse("2040-01-01T00:00:00Z");
type Call = { name: string; body: Record<string, any>; headers: Headers; redirect: RequestRedirect | undefined };
function harness() {
  const clock = new FakeClock();
  const calls: Call[] = [];
  let lease: Record<string, any> | null = null;
  let tenure = 0;
  let claimFault = false;
  let closeFault = false;
  let renewFault: "network" | "lost" | undefined;
  let getFault = false;
  let releaseFault = false;
  let intercept: ((call: Call) => Response | undefined) | undefined;
  const receipts = new Map<string, unknown>();
  const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
  const denied = () => json({ status: 409, error: { code: "claim_lost", message: "No exact lease" } }, 409);
  const now = () => new Date(epoch + clock.time).toISOString();
  const fetcher: typeof fetch = async (url, init) => {
    const call = { name: String(url).split("/").at(-1)!, body: JSON.parse(String(init?.body)), headers: new Headers(init?.headers), redirect: init?.redirect };
    calls.push(call);
    const intercepted = intercept?.(call);
    if (intercepted) return intercepted;
    assert.equal(call.headers.get("Authorization"), `Bearer ${workerToken}`);
    assert.equal(call.redirect, "error");
    const session = call.headers.get("X-Forge-Session")!;
    const body = call.body;
    if (call.name === "issue_claim") {
      if (lease && lease.expires_at <= now()) lease = null;
      const holder = `${session}:${body.attempt_id}`;
      if (lease && lease.holder !== holder) return denied();
      lease ??= { claim_uid: `claim-${++tenure}`, issue_uid: "issue-uid", holder, holder_instance_uid: body.attempt_id, client_kind: "pi", expires_at: new Date(epoch + clock.time + body.ttl_seconds * 1000).toISOString() };
      if (claimFault) { claimFault = false; throw new Error("response lost"); }
      return json({ granted: true, lease, execution_token: `signed-secret-${lease.claim_uid}`, execution_id: body.attempt_id, server_now: now() });
    }
    if (call.name === "issue_get") {
      if (getFault) throw new Error("unknown network");
      return json({ issue: { uid: "issue-uid", revision: 3, owner: "human", title: "Work" }, comments: [], links: [], lease, lease_hub_now: now() });
    }
    if (call.name === "issue_renew") {
      if (renewFault === "network") { renewFault = undefined; throw new Error("lost renew"); }
      if (renewFault === "lost" || !lease) return denied();
      assert.equal(body.ref, "issue-uid");
      assert.equal(call.headers.get("X-Forge-Execution"), `signed-secret-${lease.claim_uid}`);
      lease.expires_at = new Date(epoch + clock.time + body.ttl_seconds * 1000).toISOString();
      return json({ granted: true, lease, server_now: now() });
    }
    if (call.name === "issue_release") {
      if (releaseFault) throw new Error("release lost");
      assert.equal(body.ref, "issue-uid");
      lease = null;
      return json({ granted: true, holder: {} });
    }
    if (call.name === "issue_close") {
      assert.equal(body.ref, "issue-uid");
      assert.equal(call.headers.get("If-Lease-Match"), null);
      const key = call.headers.get("Idempotency-Key")!;
      if (receipts.has(key)) return json(receipts.get(key));
      if (!lease) return denied();
      const receipt = { issue: { uid: "issue-uid", status: "closed" }, event: { type: "issue.closed" }, changed: true, reused: false };
      receipts.set(key, receipt);
      lease = null;
      if (closeFault) { closeFault = false; throw new Error("close committed but response lost"); }
      return json(receipt);
    }
    return json({ ok: true });
  };
  const controller = (sessionId = randomUUID(), ttlSeconds = 60) => new Controller({ url: "http://127.0.0.1:7779", workerToken, ttlSeconds }, { sessionId, fetch: fetcher, clock });
  return { controller, calls, clock, intercept(fn: typeof intercept) { intercept = fn; }, get lease() { return lease; }, set lease(value) { lease = value; }, faultClaim() { claimFault = true; }, faultClose() { closeFault = true; }, faultRenew(value: typeof renewFault) { renewFault = value; }, faultGet(value: boolean) { getFault = value; }, faultRelease() { releaseFault = true; } };
}

const closeBody = { ref: "#1", reason: "done", message: "Implemented and verified behavior", evidence: [{ type: "test", command: "npm test" }] };

test("ambiguous acquire reuses attempt; cross-ref blocked; release/new runtime/new tenure use new identities", async () => {
  const h = harness(); const a = h.controller();
  h.faultClaim();
  await assert.rejects(a.execute("issue_claim", { ref: "#1" }), /ambiguous/i);
  await assert.rejects(a.execute("issue_claim", { ref: "#2" }), /pending/i);
  const result = await a.execute("issue_claim", { ref: "#1" });
  const claims = h.calls.filter((c) => c.name === "issue_claim");
  assert.equal(claims.length, 2);
  assert.equal(claims[0].body.attempt_id, claims[1].body.attempt_id);
  assert.ok(!JSON.stringify(result).includes("signed-secret"));
  await a.execute("issue_release", { ref: "#1" });
  await a.execute("issue_claim", { ref: "#1" });
  assert.notEqual(h.calls.at(-1)!.body.attempt_id, claims[0].body.attempt_id);
  await a.shutdown("new");
  const b = h.controller();
  assert.notEqual(a.runtimeId, b.runtimeId);
  assert.equal(b.state().mode, "idle");
  await b.execute("issue_claim", { ref: "#1" });
  assert.notEqual(h.calls.at(-1)!.body.attempt_id, claims[0].body.attempt_id);
  await b.shutdown();
});

test("retryPending replays the exact original acquire signature and private attempt only in this runtime", async () => {
  const h = harness(); const a = h.controller();
  await assert.rejects(a.retryPending(), /no_pending/);
  const body = { ref: "#1", purpose: "original purpose" };
  h.faultClaim();
  await assert.rejects(a.execute("issue_claim", body), /ambiguous/i);
  body.ref = "#2"; body.purpose = "changed";
  await a.retryPending();
  const claims = h.calls.filter((c) => c.name === "issue_claim");
  assert.equal(claims.length, 2);
  assert.deepEqual(claims[0].body, claims[1].body);
  assert.equal(claims[1].body.ref, "#1");
  assert.equal(claims[1].body.purpose, "original purpose");
  await assert.rejects(a.retryPending(), /no_pending/);
  const fresh = h.controller();
  await assert.rejects(fresh.retryPending(), /no_pending/);
  await a.shutdown(); await fresh.shutdown();
});

test("retryPending close preserves canonical snapshot, key, proof and evidence after lease expiry", async () => {
  const h = harness(); const a = h.controller();
  await a.execute("issue_claim", { ref: "#1" });
  const body = structuredClone(closeBody);
  h.faultClose(); await assert.rejects(a.execute("issue_close", body), /ambiguous/i);
  body.ref = "#2"; body.message = "changed"; body.evidence[0].command = "changed";
  await h.clock.advance(60_000); await a.context();
  const aborted = new AbortController(); aborted.abort();
  await assert.rejects(a.retryPending(aborted.signal));
  assert.equal(a.state().pendingClose, true);
  const result = await a.retryPending();
  const closes = h.calls.filter((c) => c.name === "issue_close");
  assert.equal(closes.length, 2);
  assert.deepEqual(closes[0].body, closes[1].body);
  assert.equal(closes[1].body.ref, "issue-uid");
  assert.equal(closes[0].headers.get("Idempotency-Key"), closes[1].headers.get("Idempotency-Key"));
  assert.equal(closes[0].headers.get("X-Forge-Execution"), closes[1].headers.get("X-Forge-Execution"));
  assert.ok(!JSON.stringify(result).includes("signed-secret"));
  await assert.rejects(a.retryPending(), /no_pending/);
  await a.shutdown();
});

test("queued duplicate retries cannot apply a retired close snapshot to a new tenure", async () => {
  const h = harness(); const a = h.controller();
  await a.execute("issue_claim", { ref: "#1" });
  h.faultClose(); await assert.rejects(a.execute("issue_close", closeBody), /ambiguous/i);
  const first = a.retryPending();
  const nextTenure = a.execute("issue_claim", { ref: "#1" });
  const duplicate = assert.rejects(a.retryPending(), /no_pending/);
  await first; await nextTenure; await duplicate;
  assert.equal(a.state().mode, "live");
  assert.equal(h.calls.filter((c) => c.name === "issue_close").length, 2);
  await a.shutdown();
});

test("A holder/B conflict; nonholder can comment/link; every editing gate rechecks", async () => {
  const h = harness(); const a = h.controller(); const b = h.controller();
  await a.execute("issue_claim", { ref: "#1" });
  await assert.rejects(b.execute("issue_claim", { ref: "#1" }), /claim_lost/);
  assert.equal((await b.guardTool("write"))?.block, true);
  assert.equal(await b.guardTool("read"), undefined);
  await b.execute("issue_comment", { ref: "#1", body: "Additional finding" });
  await b.execute("issue_link", { ref: "#1", type: "blocks", to_ref: "#2" });
  for (const name of ["edit", "write", "bash", "apply_patch"]) {
    const before = h.calls.length;
    assert.equal(await a.guardTool(name), undefined);
    assert.equal(h.calls.length, before + 1);
    assert.equal(h.calls.at(-1)!.name, "issue_get");
  }
  await a.shutdown(); await b.shutdown();
});

test("heartbeat at TTL/3, lost response stops edits then retries; exact ClaimUID replacement stops editing", async () => {
  const h = harness(); const a = h.controller();
  await a.execute("issue_claim", { ref: "#1" });
  await h.clock.advance(20_000);
  assert.equal(h.calls.at(-1)!.name, "issue_renew");
  h.faultRenew("network");
  await h.clock.advance(20_000);
  assert.equal(a.state().mode, "stop-editing");
  await h.clock.advance(1_000);
  assert.equal(a.state().mode, "live");
  h.lease = { ...h.lease!, claim_uid: "replacement-same-holder" };
  assert.equal((await a.guardTool("edit"))?.block, true);
  assert.equal(a.state().mode, "stop-editing");
  await a.shutdown();
});

test("expiry, network uncertainty, force-release and renew rejection block edits", async (t) => {
  for (const failure of ["expiry", "network", "force", "renew"] as const) await t.test(failure, async () => {
    const h = harness(); const a = h.controller();
    await a.execute("issue_claim", { ref: "#1" });
    if (failure === "expiry") h.clock.time += 60_000;
    if (failure === "network") h.faultGet(true);
    if (failure === "force") h.lease = null;
    if (failure === "renew") { h.faultRenew("lost"); await assert.rejects(a.execute("issue_renew", { ref: "#1" })); }
    assert.equal((await a.guardTool("bash"))?.block, true);
    assert.equal(a.state().mode, "stop-editing");
    await a.shutdown();
  });
});

test("close lost-response preserves complete snapshot/key/token after get shows released, then clears", async () => {
  const h = harness(); const a = h.controller();
  await a.execute("issue_claim", { ref: "#1" });
  h.faultClose();
  await assert.rejects(a.execute("issue_close", closeBody), /ambiguous/i);
  assert.equal(a.state().pendingClose, true);
  await a.context();
  assert.equal(h.lease, null);
  await assert.rejects(a.execute("issue_close", { ...closeBody, message: "Different body" }), /pending/i);
  const result = await a.execute("issue_close", structuredClone(closeBody));
  const closes = h.calls.filter((c) => c.name === "issue_close");
  assert.equal(closes.length, 2);
  assert.deepEqual(closes[0].body, closes[1].body);
  assert.equal(closes[0].headers.get("Idempotency-Key"), closes[1].headers.get("Idempotency-Key"));
  assert.equal(closes[0].headers.get("X-Forge-Execution"), closes[1].headers.get("X-Forge-Execution"));
  assert.ok(!JSON.stringify([result, a.state(), await a.context(), a]).includes("signed-secret"));
  assert.ok(!JSON.stringify([result, a.state(), await a.context(), a]).includes(workerToken));
  assert.equal(a.state().pendingClose, false);
  assert.equal(a.state().mode, "idle");
  assert.equal(h.clock.timers.size, 0);
  await a.shutdown();
});

test("long TTL heartbeat is capped at 30 seconds", async () => {
  const h = harness(); const a = h.controller(randomUUID(), 300);
  await a.execute("issue_claim", { ref: "#1" });
  await h.clock.advance(30_000);
  assert.equal(h.calls.at(-1)?.name, "issue_renew");
  assert.equal(h.calls.at(-1)?.body.ttl_seconds, 300);
  await a.shutdown();
});

test("frequent preflights do not postpone automatic renew", async () => {
  const h = harness(); const a = h.controller();
  await a.execute("issue_claim", { ref: "#1" });
  for (let i = 0; i < 4; i++) { await h.clock.advance(5_000); await a.guardTool("edit"); }
  assert.equal(h.calls.filter((c) => c.name === "issue_renew").length, 1);
  await a.shutdown();
});

test("500 acquire/close retry retain full request identity; server errors cannot leak tokens", async () => {
  const h = harness(); const a = h.controller();
  h.intercept((call) => call.name === "issue_claim" ? new Response(JSON.stringify({ error: { code: "server_error", message: `${workerToken} response uncertain` } }), { status: 500 }) : undefined);
  await assert.rejects(a.execute("issue_claim", { ref: "#1", purpose: "test" }), (err: unknown) => err instanceof ForgeError && err.ambiguous && !err.message.includes(workerToken));
  h.intercept(undefined);
  await a.execute("issue_claim", { ref: "#1", purpose: "test" });
  const claims = h.calls.filter((c) => c.name === "issue_claim");
  assert.deepEqual(claims[0].body, claims[1].body);
  h.intercept((call) => call.name === "issue_close" ? new Response(JSON.stringify({ error: { code: "server_error", message: `${workerToken} signed-secret-claim-1`, data: { execution_token: "signed-secret-claim-1" } } }), { status: 503 }) : undefined);
  const body = structuredClone(closeBody);
  await assert.rejects(a.execute("issue_close", body), (err: Error) => !err.message.includes(workerToken) && !err.message.includes("signed-secret"));
  body.evidence[0].command = "MUTATED";
  await assert.rejects(a.execute("issue_close", body), /pending/);
  h.intercept(undefined);
  await a.execute("issue_close", closeBody);
  const closes = h.calls.filter((c) => c.name === "issue_close");
  assert.deepEqual(closes[0].body, closes[1].body);
  assert.equal(closes[0].headers.get("Idempotency-Key"), closes[1].headers.get("Idempotency-Key"));
  await a.shutdown();
});

test("close definite lost rejects and forgets retry authority; malformed success preserves retry", async () => {
  const h = harness(); const a = h.controller();
  await a.execute("issue_claim", { ref: "#1" });
  h.intercept((c) => c.name === "issue_close" ? new Response("{}") : undefined);
  await assert.rejects(a.execute("issue_close", closeBody), /Ambiguous/);
  assert.equal(a.state().pendingClose, true);
  h.intercept((c) => c.name === "issue_close" ? new Response(JSON.stringify({ error: { code: "claim_lost", message: "stale execution" } }), { status: 409 }) : undefined);
  await assert.rejects(a.execute("issue_close", closeBody), /claim_lost/);
  assert.equal(a.state().pendingClose, false);
  assert.equal((await a.guardTool("write"))?.block, true);
  await a.shutdown();
});

test("two fresh runtimes sharing the same real Pi session use distinct attempts and cannot inherit live tenure", async () => {
  const h = harness(); const sessionId = randomUUID();
  const a = h.controller(sessionId); const b = h.controller(sessionId);
  await a.execute("issue_claim", { ref: "#1" });
  await assert.rejects(b.execute("issue_claim", { ref: "#1" }), /claim_lost/);
  const claims = h.calls.filter((c) => c.name === "issue_claim");
  assert.equal(claims[0].headers.get("X-Forge-Session"), sessionId);
  assert.equal(claims[1].headers.get("X-Forge-Session"), sessionId);
  assert.notEqual(claims[0].body.attempt_id, claims[1].body.attempt_id);
  assert.equal(claims[0].body.attempt_id[14], "7");
  assert.equal((await b.guardTool("edit"))?.block, true);
  await a.shutdown(); await b.shutdown();
});

test("shutdown invalidates immediately, best effort failed release leaves TTL lease; stopped runtime cannot resume", async () => {
  const h = harness(); const a = h.controller();
  await a.execute("issue_claim", { ref: "#1" });
  h.faultRelease();
  const closing = a.shutdown("logout");
  assert.equal(a.state().mode, "stopped");
  await closing;
  assert.ok(h.lease);
  assert.equal(h.clock.timers.size, 0);
  await assert.rejects(a.execute("issue_claim", { ref: "#1" }), /stopped/i);
  await a.shutdown();
  assert.equal(h.calls.filter((c) => c.name === "issue_release").length, 1);
});
