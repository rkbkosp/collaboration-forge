/** Materialize the fake-Claude launcher script for the process fixtures. */
import { chmod, writeFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
const worker = fileURLToPath(new URL('./worker.ts', import.meta.url));
const loader = fileURLToPath(new URL('../../node_modules/tsx/dist/loader.mjs', import.meta.url));
export async function fakeClaude(path: string): Promise<string> {
  // A shebang script: the supervisor execs this path directly, so the injected
  // --plugin-dir arguments arrive as ordinary argv instead of node options.
  await writeFile(path, `#!/bin/sh\nexec ${JSON.stringify(process.execPath)} --import ${JSON.stringify(loader)} ${JSON.stringify(worker)} "$@"\n`, { mode: 0o700 });
  await chmod(path, 0o700);
  return path;
}
