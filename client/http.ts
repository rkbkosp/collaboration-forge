import { randomUUID } from 'node:crypto';
import { Controller, ForgeError } from '../pi-extension/controller.ts';
import { errorCode } from '../pi-extension/errors.ts';
import { validateURL } from '../pi-extension/config.ts';
import { directFetch } from '../pi-extension/direct-fetch.ts';

/** No credentials in outward objects/errors; no raw arbitrary-origin request. */
export class ClientHTTP {
  readonly #url: string;
  readonly #token: string;
  readonly #session: string;
  readonly #fetch: typeof fetch;
  readonly #redactor: Controller;
  constructor(url: string, token: string, session: string, transport: typeof fetch = fetch) {
    this.#url = validateURL(url);
    this.#token = token;
    this.#session = session;
    this.#fetch = (input, init) => directFetch(input, init, transport);
    this.#redactor = new Controller({url,workerToken:token || 'public-read',ttlSeconds:300},{sessionId:randomUUID()});
  }
  async request(path: string, method = 'GET', body?: unknown, confirmation?: string, ifMatch?: string, idempotencyKey?: string): Promise<any> {
    if (!/^\/(?:api\/v1\/|forge\/v1\/|health$)/.test(path) || /[\\\r\n#]/.test(path) || /(?:^|\/)\.{1,2}(?:\/|$)/.test(path) || /%2e|%2f|%5c/i.test(path)) {
      throw new ForgeError('invalid_path','Use an absolute local API path without traversal');
    }
    if (!['GET','POST','PATCH','DELETE','PUT'].includes(method)) throw new ForgeError('invalid_method','Unsupported HTTP method');
    const mutation = method !== 'GET' && path !== '/forge/v1/tools/issue_timeline';
    let receivedStatus=0;
    const ambiguous=()=>mutation && (receivedStatus===0 || receivedStatus===408 || receivedStatus===429 || receivedStatus>=500);
    try {
      const headers:Record<string,string>={'X-Forge-Session':this.#session,'Content-Type':'application/json'};
      if(this.#token) headers.Authorization='Bearer '+this.#token;
      if (confirmation !== undefined) headers['X-Kata-Confirm']=confirmation;
      if (ifMatch !== undefined) headers['If-Match']=ifMatch;
      if (idempotencyKey !== undefined) headers['Idempotency-Key']=idempotencyKey;
      const text=body === undefined ? undefined : JSON.stringify(body);
      if (text && Buffer.byteLength(text)>1_048_576) throw new ForgeError('too_large','Request exceeds 1 MiB');
      const response=await this.#fetch(this.#url+path,{method,headers,body:text,redirect:'error',signal:AbortSignal.timeout(15_000)});
      receivedStatus=response.status;
      const reader=response.body?.getReader();
      const chunks:Uint8Array[]=[];let size=0;
      if(reader) for(;;) {
        const part=await reader.read();if(part.done)break;
        size+=part.value.byteLength;
        if(size>8_388_608){await reader.cancel();throw new ForgeError('response_limit','Response exceeds 8 MiB',response.status,ambiguous());}
        chunks.push(part.value);
      }
      const raw=Buffer.concat(chunks).toString('utf8');
      let decoded: unknown;
      try { decoded=raw ? JSON.parse(raw) : {}; }
      catch { if(response.ok)throw new ForgeError('invalid_response',mutation ? 'Server returned non-JSON success; inspect state before retrying' : 'Server returned non-JSON success',response.status,mutation); }
      const result:any=this.#redactor.sanitize(decoded && typeof decoded==='object' ? decoded : {});
      if(!response.ok) {
        const body = result.error && typeof result.error === 'object' && !Array.isArray(result.error) ? result.error : {};
        const details = {
          ...(typeof body.hint === 'string' && body.hint ? { hint: body.hint } : {}),
          ...(body.data && typeof body.data === 'object' && !Array.isArray(body.data) ? { data: body.data } : {}),
        };
        throw new ForgeError(errorCode(body.code, 'http_error'), String(body.message ?? `HTTP ${response.status}`), response.status,
          body.ambiguous === true || ambiguous(), details);
      }
      return result;
    } catch(error) {
      if(error instanceof ForgeError)throw error;
      throw new ForgeError('transport_error','Request failed or response was invalid; inspect server state before repeating a mutation',receivedStatus,ambiguous());
    }
  }
}
