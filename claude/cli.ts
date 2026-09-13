import { launchClaude, CLAUDE_PLUGIN_DIR } from './launcher.ts';
import { claudeIdentity, claudeTool } from './facade.ts';
import { claudeRPC, ClaudeError } from './transport.ts';
import { forgeErrorEnvelope } from '../pi-extension/errors.ts';

const HELP = `forge claude [Claude arguments...]     launch a fresh supervised instance
forge claude status | retry | pause | unpause
forge claude plugin-dir                 print the bundled plugin directory
forge claude --help-forge               show this help

Set FORGE_URL and FORGE_WORKER_TOKEN_FILE before launching.
FORGE_CLAUDE_STOP_POLICY=strict (default) or advisory.
The bundled Forge plugin is injected with --plugin-dir automatically; other
--plugin-dir arguments are passed through untouched.
Use standard forge issue commands inside Claude; no background session broker.
`;

export async function claudeCommand(args: string[], env: NodeJS.ProcessEnv = process.env): Promise<number> {
  try {
    const action = args[0];
    const output = (value: any) => process.stdout.write(JSON.stringify(value) + '\n');
    if (action === '--help-forge') { process.stdout.write(HELP); return 0; }
    if (action === 'plugin-dir') {
      if (args.length !== 1) throw new ClaudeError('usage');
      output({ plugin_dir: CLAUDE_PLUGIN_DIR });
      return 0;
    }
    if (['status', 'retry', 'pause', 'unpause'].includes(action)) {
      if (args.length !== 1) throw new ClaudeError('usage');
      const result = action === 'pause' || action === 'unpause'
        ? await claudeRPC('event', { identity: claudeIdentity(env), event: action === 'pause' ? 'pause' : 'prompt' }, env)
        : await claudeTool(action, {}, env);
      output(result);
      return 0;
    }
    return await launchClaude(args, env);
  } catch (error) {
    const failure = error instanceof ClaudeError ? error : new ClaudeError('claude_command_failed');
    const hint = failure.hint ?? (failure.code === 'claude_configuration'
      ? 'Set FORGE_URL to the loopback service and FORGE_WORKER_TOKEN_FILE to an owned 0600 worker credential file; check FORGE_TTL_SECONDS (60..3600).'
      : undefined);
    process.stderr.write(JSON.stringify(forgeErrorEnvelope({ code: failure.code, message: failure.message, status: failure.status, ambiguous: failure.ambiguous, hint, data: failure.data })) + '\n');
    return failure.ambiguous ? 3 : failure.code === 'usage' ? 2 : 1;
  }
}
