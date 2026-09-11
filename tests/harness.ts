import { spawn } from 'node:child_process';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';

export async function startForge() {
  const dataDir = await mkdtemp(join(tmpdir(), 'forge-e2e-'));
  let child: ReturnType<typeof spawn>;
  let exited: Promise<void>;
  let closed = false;

  async function launch(address: string) {
    child = spawn(resolve('bin/forged'), ['serve', '--data-dir', dataDir, '--project', 'e2e', '--listen', address], { stdio: ['ignore', 'ignore', 'pipe'] });
    exited = new Promise<void>(r => child.once('close', () => r()));
    return new Promise<string>((ready, reject) => {
      const timer = setTimeout(() => reject(new Error('forged startup timeout')), 15_000);
      let text = '';
      child.stderr!.on('data', chunk => {
        text = (text + String(chunk)).slice(-16_384);
        const match = text.match(/forged listening on (127\.0\.0\.1:\d+)/);
        if (match) { clearTimeout(timer); ready('http://' + match[1]); }
      });
      child.once('error', err => { clearTimeout(timer); reject(err); });
      child.once('exit', () => { clearTimeout(timer); reject(new Error('forged exited before readiness')); });
    });
  }
  async function stop() {
    if (child.exitCode === null && child.signalCode === null) child.kill('SIGTERM');
    const timer = setTimeout(() => child.kill('SIGKILL'), 12_000);
    try { await exited; } finally { clearTimeout(timer); }
  }
  async function close() {
    if (closed) return;
    closed = true;
    try { await stop(); } finally { await rm(dataDir, { recursive: true, force: true }); }
  }
  try {
    const url = await launch('127.0.0.1:0');
    const adminToken = (await readFile(join(dataDir, 'admin-token'), 'utf8')).trim();
    const workerToken = (await readFile(join(dataDir, 'worker-token'), 'utf8')).trim();
    async function admin(path: string, body?: unknown, method = body === undefined ? 'GET' : 'POST') {
      const response = await fetch(url + path, {
        method, redirect: 'error',
        headers: { Authorization: 'Bearer ' + adminToken, 'Content-Type': 'application/json' },
        body: body === undefined ? undefined : JSON.stringify(body),
        signal: AbortSignal.timeout(10_000),
      });
      if (!response.ok) throw new Error(`supervisor HTTP ${response.status}`);
      return response.json() as Promise<any>;
    }
    async function restart() {
      await stop();
      await launch(new URL(url).host); // Same endpoint and durable signing key/receipts.
    }
    const info = await admin('/forge/v1/project');
    return { url, dataDir, adminToken, workerToken, projectID: info.project.id as number, admin, restart, close };
  } catch (error) { await close(); throw error; }
}
