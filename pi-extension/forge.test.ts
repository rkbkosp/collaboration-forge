import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { mkdtemp, writeFile, chmod, rm, symlink } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { test } from "node:test";
import type { ExtensionAPI, ExtensionContext, ToolDefinition } from "@earendil-works/pi-coding-agent";
import { Controller, registerForge } from "./forge.ts";
import { loadConfig, validateURL } from "./config.ts";
import { toolSchemas, validateParams } from "./schemas.ts";

test("env config allows only loopback literal origins and reads owned 0600 token file", async () => {
  const dir = await mkdtemp(join(tmpdir(), "forge-config-"));
  try {
    const path = join(dir, "token");
    await writeFile(path, "not-for-model\n", { mode: 0o600 });
    assert.deepEqual(await loadConfig({ FORGE_WORKER_TOKEN_FILE: path }), { url: "http://127.0.0.1:7347", ttlSeconds: 300, workerToken: "not-for-model" });
    for (const url of ["http://localhost:7779", "http://example.com", "http://192.168.1.1", "http://127.1", "http://2130706433", "http://0177.0.0.1", "http://127.0.0.1/path", "http://secret@127.0.0.1", "http://127.0.0.1?token=x", "http://127.0.0.1#x", "file:///token", "http://[::ffff:127.0.0.1]"]) assert.throws(() => validateURL(url));
    assert.equal(validateURL("http://[::1]:7779"), "http://[::1]:7779");
    assert.equal(validateURL("http://127.1.2.3:7779/"), "http://127.1.2.3:7779");
    for (const ttl of ["", "59", "3601", "3e2", "300.1", " 300"]) await assert.rejects(loadConfig({ FORGE_WORKER_TOKEN_FILE: path, FORGE_TTL_SECONDS: ttl }));
    await chmod(path, 0o644);
    await assert.rejects(loadConfig({ FORGE_WORKER_TOKEN_FILE: path }), (e: Error) => !e.message.includes("not-for-model"));
    await chmod(path, 0o600);
    await symlink(path, join(dir, "link"));
    await assert.rejects(loadConfig({ FORGE_WORKER_TOKEN_FILE: join(dir, "link") }));
    await assert.rejects(loadConfig({}));
  } finally { await rm(dir, { recursive: true, force: true }); }
});

test("all schemas reject authority/protocol input and typed evidence rejects extra fields", () => {
  assert.equal(Object.keys(toolSchemas).length, 14);
  for (const schema of Object.values(toolSchemas)) {
    assert.equal((schema as { additionalProperties?: unknown }).additionalProperties, false);
    assert.ok(!/attempt_id|execution_token|claim_uid|ClaimUID|client_kind|actor|force|metadata|replace|retry_protocol/.test(JSON.stringify(schema)));
  }
  for (const key of ["attempt_id", "execution_token", "claim_uid", "ClaimUID", "client_kind", "actor", "force", "metadata", "replace", "retry_protocol", "ttl_seconds"]) {
    assert.throws(() => validateParams("issue_claim", { ref: "#1", [key]: "hidden" }));
  }
  assert.throws(() => validateParams("issue_close", { ref: "#1", reason: "done", message: "done", evidence: ["npm test"] }));
  assert.throws(() => validateParams("issue_close", { ref: "#1", reason: "done", message: "done", evidence: [{ type: "test", sha: "1234567" }] }));
  assert.throws(() => validateParams("issue_link", { ref: "#1", type: "blocks", to_ref: "#2", replace: true }));
});

function fakePi() {
  const handlers = new Map<string, (event: any, ctx: ExtensionContext) => any>();
  const tools: ToolDefinition[] = [];
  const commands = new Map<string, any>();
  const messages: unknown[] = [];
  const api = {
    on(name: string, handler: any) { handlers.set(name, handler); },
    registerTool(tool: ToolDefinition) { tools.push(tool); },
    registerCommand(name: string, command: any) { commands.set(name, command); },
    sendMessage(message: unknown) { messages.push(message); },
    appendEntry() { throw new Error("Credentials must not be persisted"); },
  } as unknown as ExtensionAPI;
  const context = (sessionId: string) => ({ sessionManager: new Proxy({}, {
    get(_target, key) {
      if (key === "getSessionId") return () => sessionId;
      throw new Error("Must not read/restore execution from history");
    },
  }), hasUI: false } as ExtensionContext);
  return { handlers, tools, commands, messages, api, context };
}

test("adapter uses Pi 0.85.1 start/shutdown lifecycle; tree no-op; no history restore or factory resources", async () => {
  const pi = fakePi();
  const created: Controller[] = [];
  const headers: Headers[] = [];
  const installed = registerForge(pi.api, async (sessionId) => {
    const c = new Controller({ url: "http://127.0.0.1:7779", ttlSeconds: 60, workerToken: "worker-secret" }, { sessionId, fetch: async (_url, init) => {
      headers.push(new Headers(init?.headers));
      return new Response(JSON.stringify({ issues: [], token: "never-output", note: "worker-secret" }));
    } });
    created.push(c); return c;
  });
  assert.equal(created.length, 0);
  assert.equal(pi.tools.length, 14);
  assert.equal(pi.handlers.has("session_switch"), false);
  const sessionId = randomUUID();
  for (const reason of ["startup", "reload", "new", "resume", "fork"]) {
    if (created.length) await pi.handlers.get("session_shutdown")!({ reason }, pi.context(sessionId));
    await pi.handlers.get("session_start")!({ reason }, pi.context(sessionId));
    assert.equal(installed.controller()?.state().mode, "idle");
    const current = installed.controller();
    await pi.handlers.get("session_tree")!({}, pi.context(sessionId));
    assert.equal(installed.controller(), current);
    const tool = pi.tools.find((t) => t.name === "issue_list")!;
    const result = await tool.execute("call", {}, undefined, undefined, pi.context(sessionId));
    assert.ok(!JSON.stringify(result).includes("worker-secret"));
    assert.ok(!JSON.stringify(result).includes("never-output"));
    assert.equal(headers.at(-1)?.get("X-Forge-Session"), sessionId);
  }
  assert.equal(new Set(created.map((c) => c.runtimeId)).size, 5);
  const injected = await pi.handlers.get("before_agent_start")!({ systemPrompt: "Base" }, pi.context(sessionId));
  assert.match(injected.systemPrompt, /owner is long-term/);
  assert.match(injected.systemPrompt, /never invent tests/);
  assert.equal((await pi.handlers.get("tool_call")!({ toolName: "edit" }, pi.context(sessionId))).block, true);
  assert.equal(await pi.handlers.get("tool_call")!({ toolName: "read" }, pi.context(sessionId)), undefined);
  await pi.commands.get("forge-stop").handler("", pi.context(sessionId));
  assert.equal(created.at(-1)?.state().mode, "stopped");
  await pi.handlers.get("session_start")!({ reason: "reload" }, pi.context(sessionId));
  await pi.commands.get("forge-logout").handler("", pi.context(sessionId));
  assert.equal(created.at(-1)?.state().mode, "stopped");
  assert.ok(!JSON.stringify(pi.messages).includes("worker-secret"));
  assert.ok(!JSON.stringify(pi.messages).includes("never-output"));
});
