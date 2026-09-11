import { Agent } from 'undici';

// A private direct dispatcher, never Node's environment-proxy/global dispatcher.
// Forge callers validate loopback origins before using this transport. Do not
// forward credentials through HTTP_PROXY/HTTPS_PROXY/NODE_USE_ENV_PROXY.
const dispatcher = new Agent({ connect: { timeout: 10_000 } });

export function directFetch(input: Parameters<typeof fetch>[0], init: RequestInit = {}, transport: typeof fetch = fetch): ReturnType<typeof fetch> {
  return transport(input, { ...init, redirect: 'error', dispatcher } as RequestInit);
}
