import { ClaudeError } from './transport.ts';
import type { Identity } from './facade.ts';

const quote = (value: string) => "'" + value.replaceAll("'", "'\\''") + "'";

/**
 * Bind the whole Bash command, including pipelines and later CLI calls, to its
 * hook identity. No shared file, TTL or global agent environment is involved.
 * PreToolUse.updatedInput is evaluated by Claude's normal permission checks;
 * returning it without permissionDecision never auto-approves the command.
 */
export function bindBashInput(input: unknown, identity: Identity): Record<string, unknown> {
  if (!input || typeof input !== 'object' || Array.isArray(input) ||
      typeof (input as any).command !== 'string') throw new ClaudeError('invalid_hook');
  const original = input as Record<string, unknown>;
  return {
    ...original,
    command: `export FORGE_CLAUDE_SESSION_ID=${quote(identity.session_id)}\nexport FORGE_CLAUDE_AGENT_ID=${quote(identity.agent_id)}\n${original.command}`,
  };
}
