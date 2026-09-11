import { mkdtemp, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { truncateHead, type ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { Controller, ForgeError } from "./controller.ts";
import { toolSchemas, type ToolName } from "./schemas.ts";

export { Controller, ForgeError } from "./controller.ts";
export type { Clock, ControllerOptions } from "./controller.ts";
export { loadConfig } from "./config.ts";
export type { ForgeConfig } from "./config.ts";

const descriptions: Record<ToolName, string> = {
  issue_list: "List Forge issues, optionally filtered by status.",
  issue_get: "Read an issue, comments, links and current live lease.",
  issue_graph: "Read an issue dependency/parent graph.",
  issue_create: "Create an issue as additive collaboration; no execution lease required.",
  issue_comment: "Add a finding/comment, including to another holder's issue; no execution lease required.",
  issue_link: "Add a parent/blocks/related link; no execution lease required. Never replaces existing links.",
  issue_claim: "Acquire one timed execution lease for this runtime. On ambiguous failure retry the identical call; do not change issue/purpose. Attempts and heartbeat are plugin-managed.",
  issue_renew: "Renew this runtime's current exact execution tenure (also automatic). Ref may be its original claim alias or canonical UID.",
  issue_release: "Release this runtime's current lease. On ambiguous failure stop editing and retry.",
  issue_close: "Close with a live exact lease and truthful typed evidence. Retry an ambiguous close with the ORIGINAL complete arguments even if get now shows released/closed. Never change arguments until the pending retry resolves. The plugin supplies credential and idempotency key.",
};

/** Exported registration adapter for deterministic hook tests; production always
 * loads credentials/config from env during session_start, never in the factory. */
export function registerForge(pi: ExtensionAPI, create: (sessionId: string) => Promise<Controller> = (id) => Controller.fromEnv(id)) {
  let controller: Controller | undefined;
  let unavailable = "Forge has not started; editing requires a confirmed issue lease";
  const requireController = () => {
    if (!controller) throw new ForgeError("forge_unavailable", unavailable);
    return controller;
  };

  pi.on("session_start", async (_event, ctx) => {
    // Real 0.85.1 new/resume/fork/reload gets a fresh extension instance.
    // Also defensively retire any previous instance if a host reuses the factory.
    const previous = controller;
    controller = undefined;
    await previous?.shutdown("restart");
    try { controller = await create(ctx.sessionManager.getSessionId()); }
    catch { unavailable = "Forge configuration unavailable: check FORGE_URL, FORGE_WORKER_TOKEN_FILE (owned 0600 file), and FORGE_TTL_SECONDS; editing is blocked"; }
  });
  pi.on("session_shutdown", async (event) => {
    const previous = controller;
    controller = undefined;
    unavailable = "Forge runtime stopped; new runtime must claim a new execution";
    await previous?.shutdown(event.reason);
  });
  pi.on("session_tree", () => { /* Same runtime: deliberately do nothing. */ });

  pi.on("tool_call", async (event, ctx) => {
    if (!["edit", "write", "bash", "apply_patch"].includes(event.toolName)) return;
    if (!controller) return { block: true, reason: unavailable };
    return controller.guardTool(event.toolName, ctx.signal);
  });
  pi.on("before_agent_start", async (event, ctx) => {
    const context = controller ? await controller.context(ctx.signal) :
      `${unavailable}. Forge is collaboration control, not an OS sandbox. Reads and additive issue contributions are permitted; do not fabricate tests or close evidence.`;
    // Per-turn system context, not appendEntry or execution restoration.
    return { systemPrompt: `${event.systemPrompt}\n\n${context}` };
  });

  for (const name of Object.keys(toolSchemas) as ToolName[]) {
    pi.registerTool({
      name, label: name, description: `${descriptions[name]} Output is limited to 2000 lines/50 KiB; larger redacted results are saved to a 0600 temporary file.`,
      promptSnippet: descriptions[name],
      parameters: toolSchemas[name],
      executionMode: name === "issue_claim" || name === "issue_renew" || name === "issue_release" || name === "issue_close" ? "sequential" : "parallel",
      async execute(_id, params, signal) {
        const current = requireController();
        const result = await current.execute(name, params, signal);
        const output = JSON.stringify(current.sanitize(result), null, 2);
        const truncated = truncateHead(output, { maxLines: 2000, maxBytes: 50 * 1024 });
        let text = truncated.content;
        let fullOutputPath: string | undefined;
        if (truncated.truncated) {
          const dir = await mkdtemp(join(tmpdir(), "forge-output-"));
          fullOutputPath = join(dir, "result.json");
          await writeFile(fullOutputPath, output, { mode: 0o600 });
          text += `\n[Truncated to 2000 lines/50 KiB. Full redacted output: ${fullOutputPath}]`;
        }
        return { content: [{ type: "text", text }], details: { state: current.state(), truncated: truncated.truncated, fullOutputPath } };
      },
    });
  }
  for (const name of ["forge-stop", "forge-logout"]) {
    pi.registerCommand(name, {
      description: "Stop Forge heartbeat, invalidate this runtime and best-effort release its lease. Reload to initialize a fresh runtime.",
      handler: async () => {
        const previous = controller;
        controller = undefined;
        unavailable = "Forge stopped/logged out; reload before claiming a new execution";
        await previous?.shutdown(name === "forge-stop" ? "stop" : "logout");
        pi.sendMessage({ customType: "forge-status", content: "Forge stopped. Release was best effort; a remaining timed lease expires by TTL. Reload initializes a fresh execution runtime.", display: true });
      },
    });
  }
  return { controller: () => controller };
}

export default function forge(pi: ExtensionAPI): void { registerForge(pi); }
