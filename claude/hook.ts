import { claudeRPC, ClaudeError } from './transport.ts';
import { validateIdentity } from './facade.ts';
import { stopDecision } from '../client/runtime-stop.ts';
import { writeBinding } from './binding.ts';

/**
 * The model-facing protocol note. It names only the ordinary workflow; private
 * attempt identity, execution proofs, ClaimUID plumbing, close-v2 headers and
 * daemon endpoints stay in forged and never reach a prompt or a transcript.
 */
export const PROTOCOL = 'Forge is this workspace\'s work ledger. Before substantial work, inspect and claim the relevant issue with forge issue. Record discoveries as comments/issues/links. Close with truthful typed evidence, or release when handing off. After claiming, work in the original directory or use forge checkout ISSUE --dirty / --ref COMMIT for isolation (one per repository). Use the returned worktree as the explicit cwd for shell/edit/test tools. In isolated directories use "$FORGE_CLAUDE_CLI" if a shell profile overrides forge. Completed checkouts are archived without deletion. Use forge --help. Runtime authority and renewal are managed by forged; never start a legacy session broker inside Claude.';

/** Only these daemon state fields may reach the model's context. */
const CONTEXT_STATE_FIELDS = ['active', 'pending', 'paused', 'unknown', 'issue', 'workspaces'] as const;

/**
 * Claude reports the main agent and each subagent through one `agent_id` field.
 * A hook that carries none is the main agent. The value is attribution only: the
 * private execution proof forged issues remains the actual credential.
 */
export function hookAgent(input: any): string {
  return typeof input?.agent_id === 'string' && input.agent_id ? input.agent_id : 'main';
}

/**
 * Publish the canonical session identity for ordinary main-agent Bash calls.
 * `SessionStart` is the only event that carries the session id and can run
 * before any Bash tool, so it writes CLAUDE_ENV_FILE once. A subagent's
 * `agent_id` is deliberately NOT written here: several subagents can coexist,
 * so the hook binds it per invocation instead.
 */
export function environmentPreamble(sessionId: string): string {
  return `export FORGE_CLAUDE_SESSION_ID='${sessionId}'\nexport FORGE_CLAUDE_AGENT_ID='main'\n`;
}

const EVENTS: Record<string, string> = {
  SessionStart: 'start',
  SubagentStart: 'start',
  PostCompact: 'context',
  PreCompact: 'touch',
  PreToolUse: 'touch',
  PostToolUse: 'touch',
  PostToolUseFailure: 'touch',
  UserPromptSubmit: 'prompt',
  SessionEnd: 'session_end',
  Interrupt: 'interrupt',
  Stop: 'stop_check',
  SubagentStop: 'stop_check',
};

/** Lifecycle events that must not delay the harness keep a fast local RPC. */
const FAST_EVENTS = new Set(['SessionEnd', 'Interrupt']);
const CONTEXT_EVENTS = new Set(['SessionStart', 'SubagentStart']);
const STOP_EVENTS = new Set(['Stop', 'SubagentStop']);

export interface HookDependencies {
  rpc?: typeof claudeRPC;
  /** Persist the session environment. Returns false when CLAUDE_ENV_FILE is unusable. */
  publish?: (sessionId: string) => Promise<boolean>;
  /** Record the agent bound to the next shell/edit invocation. */
  bind?: (agent: string) => Promise<boolean>;
}

export async function handleHook(input: any, env: NodeJS.ProcessEnv = process.env, dependencies: HookDependencies = {}): Promise<any> {
  if (!input || typeof input !== 'object' || Array.isArray(input) || typeof input.session_id !== 'string' || !input.session_id) {
    throw new ClaudeError('invalid_hook');
  }
  // An inherited session must never be silently replaced by a different one.
  if (env.FORGE_CLAUDE_SESSION_ID && env.FORGE_CLAUDE_SESSION_ID !== input.session_id) {
    throw new ClaudeError('hook_identity_mismatch');
  }
  const name = input.hook_event_name;
  const event = EVENTS[name];
  if (!event) throw new ClaudeError('unsupported_hook');

  // Lifecycle payloads are canonical. Shell attribution must never redirect a
  // hook, including the subsequent pause after an advisory Stop.
  const identity = validateIdentity({
    instance_id: env.FORGE_CLAUDE_INSTANCE_ID ?? '',
    session_id: input.session_id,
    agent_id: hookAgent(input),
  });
  // Validate configuration before touching the daemon, so a misconfigured policy
  // is a pure local decision rather than a lifecycle observation followed by a
  // failure.
  const policy = name === 'Stop' || name === 'SubagentStop' ? env.FORGE_CLAUDE_STOP_POLICY ?? 'strict' : undefined;
  if (policy !== undefined && policy !== 'strict' && policy !== 'advisory') throw new ClaudeError('invalid_stop_policy');

  const rpc = dependencies.rpc ?? claudeRPC;
  // SessionEnd must be cheap: a session that is already ending must not wait on
  // network retries. A missed event is recovered by process liveness plus TTL.
  const state = await rpc('event', { identity, event }, env, FAST_EVENTS.has(name) ? 650 : 2500);

  if (CONTEXT_EVENTS.has(name)) {
    // Bind the canonical session for later Bash calls before injecting context.
    // Compact/reload refreshes context only: it never creates or restores an
    // execution, and the daemon keeps the current tenure and pending request.
    if (name === 'SessionStart' && hookAgent(input) === 'main') {
      const publish = dependencies.publish ?? publishSessionEnvironment;
      await publish(input.session_id).catch(() => false);
    }
    return { hookSpecificOutput: { hookEventName: name, additionalContext: PROTOCOL + '\n' + contextState(state) } };
  }
  if (name === 'PreToolUse') {
    // Bind the current agent to the shell/edit about to run. This hook never
    // returns a permission decision: Claude's own permission flow is untouched.
    if (input.tool_name === 'Bash' || mutatesFiles(input.tool_name)) {
      const bind = dependencies.bind ?? writeBinding;
      await bind(hookAgent(input)).catch(() => false);
    }
    return {};
  }
  if (STOP_EVENTS.has(name)) {
    // Scoped by session + agent: a subagent holding work is not the main agent
    // stopping, and one subagent ending is not the whole session ending.
    const result = stopDecision(state, input, policy as 'strict' | 'advisory', 'forge claude');
    // A pending request must keep its private retry identity: Stop never clears it.
    if (result.systemMessage) await rpc('event', { identity, event: 'pause' }, env, 650).catch(() => {});
    return result;
  }
  // Compaction and tool activity are liveness observations. PostCompact accepts
  // only common output fields, so it returns the empty object; no observation may
  // create, restore, release or re-key an execution.
  return {};
}

/**
 * Reduce the daemon's state reply to the fields a model may see. The reply is
 * already redacted server-side, and this allowlist is a second boundary so no
 * execution plumbing can reach a prompt even if that reply gains a field.
 */
export function contextState(state: any): string {
  try {
    if (!state || typeof state !== 'object' || Array.isArray(state)) return '{}';
    const safe: Record<string, unknown> = {};
    for (const key of CONTEXT_STATE_FIELDS) if (key in state) safe[key] = state[key];
    return JSON.stringify(safe);
  } catch { return '{}'; }
}

async function publishSessionEnvironment(sessionId: string): Promise<boolean> {
  const file = process.env.CLAUDE_ENV_FILE;
  if (!file) return false;
  const { appendFile } = await import('node:fs/promises');
  await appendFile(file, environmentPreamble(sessionId));
  return true;
}

/** Edit/Write are the direct filesystem mutators Claude exposes. */
export function mutatesFiles(toolName: unknown): boolean {
  return toolName === 'Edit' || toolName === 'Write' || toolName === 'NotebookEdit';
}
