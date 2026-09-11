import { randomUUID } from "node:crypto";
import { v7 as uuidv7 } from "uuid";
import { performance } from "node:perf_hooks";
import { loadConfig, validateTTL, validateURL, type ForgeConfig } from "./config.ts";
import { validateParams, type ToolName } from "./schemas.ts";

export interface Clock {
  /** Monotonic milliseconds, never Date.now(). */
  now(): number;
  setTimeout(fn: () => void, ms: number): unknown;
  clearTimeout(handle: unknown): void;
}
export interface ControllerOptions {
  /** Real ctx.sessionManager.getSessionId(), NOT the runtime identity. */
  sessionId: string;
  fetch?: typeof fetch;
  clock?: Clock;
  requestTimeoutMs?: number;
  shutdownTimeoutMs?: number;
}
type ObjectJSON = Record<string, any>;
type Mode = "idle" | "claim-pending" | "live" | "stop-editing" | "stopped";
interface Tenure {
  alias: string;
  issueUID: string;
  claimUID: string;
  holder: string;
  holderInstanceUID: string;
  clientKind: string;
  token: string;
  executionID: string;
  deadline: number;
}
interface PendingClaim { body: ObjectJSON; signature: string }
interface PendingClose { body: ObjectJSON; signature: string; alias: string; token: string; key: string }

const realClock: Clock = {
  now: () => performance.now(),
  setTimeout: (fn, ms) => { const timer = setTimeout(fn, ms); timer.unref(); return timer; },
  clearTimeout: (timer) => clearTimeout(timer as ReturnType<typeof setTimeout>),
};
const editTools = new Set(["edit", "write", "bash", "apply_patch"]);
const secretKey = /^(execution[_-]?token|worker[_-]?token|authorization|x-forge-execution|token)$/i;
const privateKey = /^(execution[_-]?id|attempt[_-]?id)$/i;
const object = (value: unknown): ObjectJSON => value !== null && typeof value === "object" && !Array.isArray(value) ? value as ObjectJSON : {};
const requiredString = (value: unknown): value is string => typeof value === "string" && value.length > 0;
function stable(value: any): string {
  if (Array.isArray(value)) return `[${value.map(stable).join(",")}]`;
  if (value && typeof value === "object") return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${stable(value[key])}`).join(",")}}`;
  return JSON.stringify(value);
}

/** Only sanitized properties are attached to errors; no response/request/cause. */
export class ForgeError extends Error {
  constructor(public readonly code: string, message: string, public readonly status = 0, public readonly ambiguous = false) {
    super(`${code}: ${message}`);
    this.name = "ForgeError";
  }
}

/** A single Pi runtime. No persistence or history API; every constructor creates
 * a fresh instance, never an old execution. Config injection is for host/E2E use only. */
export class Controller {
  readonly #runtimeId = randomUUID();
  readonly #sessionId: string;
  readonly #url: string;
  #workerToken: string;
  readonly #ttl: number;
  readonly #fetch: typeof fetch;
  readonly #clock: Clock;
  readonly #timeout: number;
  readonly #shutdownTimeout: number;
  readonly #abort = new AbortController();
  readonly #secrets = new Set<string>();
  #mode: Mode = "idle";
  #reason = "No execution lease; claim an issue before editing";
  #active?: Tenure;
  #boundRef?: string;
  #issue?: ObjectJSON;
  #leaseView: unknown = null;
  #pendingClaim?: PendingClaim;
  #pendingClose?: PendingClose;
  #timer?: unknown;
  #queue: Promise<unknown> = Promise.resolve();
  #shutdown?: Promise<void>;

  constructor(config: ForgeConfig, options: ControllerOptions) {
    if (!/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(options.sessionId)) throw new Error("A real Pi session UUID is required");
    this.#sessionId = options.sessionId;
    this.#url = validateURL(config.url);
    this.#ttl = validateTTL(config.ttlSeconds);
    if (!config.workerToken || /\s/.test(config.workerToken)) throw new Error("Invalid Forge worker credential");
    this.#workerToken = config.workerToken;
    this.#secrets.add(config.workerToken);
    this.#fetch = options.fetch ?? fetch;
    this.#clock = options.clock ?? realClock;
    this.#timeout = options.requestTimeoutMs ?? 10_000;
    this.#shutdownTimeout = options.shutdownTimeoutMs ?? 1_500;
  }

  static async fromEnv(sessionId: string, env: NodeJS.ProcessEnv = process.env, options: Omit<ControllerOptions, "sessionId"> = {}): Promise<Controller> {
    return new Controller(await loadConfig(env), { ...options, sessionId });
  }
  get runtimeId(): string { return this.#runtimeId; }

  state() {
    this.#checkExpiry();
    return this.sanitize({ mode: this.#mode, reason: this.#reason, issueUID: this.#boundRef,
      claimUID: this.#active?.claimUID, pendingClaim: !!this.#pendingClaim, pendingClose: !!this.#pendingClose });
  }

  /** All outward results are redacted, including nested fields and echoed values. */
  sanitize<T>(value: T): T {
    const collect = (entry: unknown) => {
      if (!entry || typeof entry !== "object") return;
      for (const [key, child] of Object.entries(entry)) {
        if (secretKey.test(key) && typeof child === "string" && child) this.#secrets.add(child);
        else collect(child);
      }
    };
    collect(value);
    const walk = (entry: any): any => {
      if (typeof entry === "string") {
        for (const secret of this.#secrets) entry = entry.replaceAll(secret, "[REDACTED]");
        return entry.replace(/Bearer\s+[^\s"\\]+/gi, "Bearer [REDACTED]");
      }
      if (Array.isArray(entry)) return entry.map(walk);
      if (entry && typeof entry === "object") return Object.fromEntries(Object.entries(entry)
        .filter(([key]) => !secretKey.test(key) && !privateKey.test(key)).map(([key, child]) => [key, walk(child)]));
      return entry;
    };
    return walk(value);
  }

  execute(name: ToolName, params: unknown, signal?: AbortSignal): Promise<ObjectJSON> {
    return this.#exclusive(async () => {
      this.#assertRunning();
      validateParams(name, params);
      // Own the request data before any await; external callers cannot mutate retries.
      const body = JSON.parse(JSON.stringify(params)) as ObjectJSON;
      signal?.throwIfAborted();
      if (name === "issue_claim") return this.#claim(body, signal);
      if (name === "issue_renew") return this.#renew(body, signal);
      if (name === "issue_release") return this.#release(body, signal);
      if (name === "issue_close") return this.#close(body, signal);
      const boundGet = name === "issue_get" && (body.ref === this.#boundRef || body.ref === this.#active?.alias);
      try {
        const reply = await this.#request(name, body, { signal });
        if (boundGet) this.#observe(reply.body, reply.started);
        return this.sanitize(reply.body);
      } catch (error) {
        if (boundGet) this.#stop("Cannot confirm bound issue; stop editing until exact server confirmation");
        throw error;
      }
    });
  }

  /** Cooperative preflight only: cannot sandbox OS access or cancel an already
   * running stock tool. Forge server still fences close transactionally. */
  async guardTool(name: string, signal?: AbortSignal): Promise<{ block: true; reason: string } | undefined> {
    if (!editTools.has(name)) return;
    return this.#exclusive(async () => {
      try {
        this.#assertRunning();
        this.#checkExpiry();
        if (this.#pendingClose) throw new ForgeError("pending_close", "Stop editing; retry the original issue_close first");
        if (!this.#active) throw new ForgeError("lease_required", this.#reason);
        await this.#refresh(signal);
        if (this.#mode !== "live") throw new ForgeError("stop_editing", this.#reason);
      } catch (error) {
        return { block: true, reason: this.#safeMessage(error) };
      }
    });
  }

  /** Safe live context, no credentials or execution nonce. Stale issue fields
   * remain explicitly marked when refresh fails. */
  context(signal?: AbortSignal): Promise<string> {
    return this.#exclusive(async () => {
      let refreshed = false;
      if (this.#boundRef && this.#mode !== "stopped") {
        try { await this.#refresh(signal); refreshed = true; } catch { /* state carries failure */ }
      }
      const state = this.state();
      return JSON.stringify(this.sanitize({ ...state, refreshed, issue: this.#issue ? {
        uid: this.#issue.uid ?? this.#boundRef, revision: this.#issue.revision,
        owner: this.#issue.owner ?? null, status: this.#issue.status,
      } : null, liveLease: refreshed ? this.#leaseView : null })) +
        "\nForge collaboration control (not an OS sandbox): owner is long-term responsibility; live lease is exclusive execution authority. " +
        "Do not edit/write/bash/apply_patch without a confirmed exact lease. Additive issue_create/comment/link and reads remain allowed without a lease. " +
        "issue_close requires truthful typed evidence; never invent tests, commits, or results. If pendingClose is true, retry the ORIGINAL issue_close unchanged even if the lease is now released.";
    });
  }

  /** Invalidate FIRST; bounded release uses the retired runtime's exact token.
   * Unconfirmed acquire/crash cannot be safely released; the server TTL recovers it. */
  shutdown(reason = "quit"): Promise<void> {
    if (this.#shutdown) return this.#shutdown;
    const tenure = this.#active;
    this.#mode = "stopped";
    this.#reason = "Forge runtime stopped; use a fresh runtime, never resume old execution";
    this.#cancelTimer();
    this.#abort.abort();
    this.#active = undefined;
    this.#pendingClaim = undefined;
    this.#pendingClose = undefined;
    this.#shutdown = (async () => {
      try {
        if (tenure) await this.#request("issue_release", { ref: tenure.issueUID, reason: `runtime_${reason}` }, {
          token: tenure.token, detached: true, timeout: this.#shutdownTimeout,
        });
      } catch { /* best effort only; timed lease expires */ }
      finally { this.#workerToken = ""; }
    })();
    return this.#shutdown;
  }

  #exclusive<T>(fn: () => Promise<T>): Promise<T> {
    const next = this.#queue.then(fn, fn);
    this.#queue = next.catch(() => {});
    return next;
  }
  #assertRunning() {
    if (this.#mode === "stopped") throw new ForgeError("runtime_stopped", "Forge runtime stopped");
  }
  #safeMessage(error: unknown): string {
    return error instanceof ForgeError ? this.sanitize(error.message) : "Forge operation unavailable; stop editing and retry after checking connectivity";
  }
  #stop(reason: string, lost = false) {
    if (this.#mode === "stopped") return;
    this.#mode = "stop-editing";
    this.#reason = reason;
    this.#cancelTimer();
    if (lost) this.#active = undefined;
  }
  #checkExpiry() {
    if (this.#active && this.#clock.now() >= this.#active.deadline) {
      this.#stop("Lease expired at conservative server/monotonic deadline; claim a new tenure", true);
    }
  }
  #cancelTimer() {
    if (this.#timer !== undefined) this.#clock.clearTimeout(this.#timer);
    this.#timer = undefined;
  }
  #schedule(delay = Math.min(this.#ttl * 1000 / 3, 30_000)) {
    this.#cancelTimer();
    if (!this.#active || this.#pendingClose || this.#mode === "stopped") return;
    const remaining = this.#active.deadline - this.#clock.now();
    if (remaining <= 0) { this.#checkExpiry(); return; }
    this.#timer = this.#clock.setTimeout(() => {
      this.#timer = undefined;
      void this.#exclusive(async () => {
        if (!this.#active || this.#pendingClose || this.#mode === "stopped") return;
        try { await this.#renew({ ref: this.#active.issueUID }); } catch { /* renew sets safe state and retry */ }
      });
    }, Math.min(delay, remaining));
  }
  #deadline(lease: ObjectJSON, serverNow: unknown, started: number): number {
    const expiry = typeof lease.expires_at === "string" ? Date.parse(lease.expires_at) : NaN;
    const now = typeof serverNow === "string" ? Date.parse(serverNow) : NaN;
    if (!Number.isFinite(expiry) || !Number.isFinite(now)) throw new ForgeError("invalid_lease", "Missing server timestamps; stop editing", 0, true);
    // Charge the entire RTT against the lease. Never trust local wall time.
    return started + Math.min(this.#ttl * 1000, expiry - now) - 1000;
  }
  #sameLease(lease: ObjectJSON, active: Tenure) {
    return !lease.released_at && lease.claim_uid === active.claimUID && lease.issue_uid === active.issueUID &&
      lease.holder === active.holder && lease.holder_instance_uid === active.holderInstanceUID && lease.client_kind === active.clientKind;
  }
  #requireActive(ref: unknown): Tenure {
    this.#checkExpiry();
    if (!this.#active) throw new ForgeError("lease_required", "No current exact execution lease; claim an issue first");
    if (ref !== this.#active.issueUID && ref !== this.#active.alias) throw new ForgeError("different_issue", "Release the currently bound issue before working on a different issue");
    return this.#active;
  }

  async #claim(body: ObjectJSON, signal?: AbortSignal): Promise<ObjectJSON> {
    this.#checkExpiry();
    if (this.#pendingClose) throw new ForgeError("pending_close", "Finish the pending original close retry first");
    if (this.#active) throw new ForgeError("lease_active", "An issue is already bound; renew or release it before a new claim");
    const signature = stable(body);
    if (this.#pendingClaim && signature !== this.#pendingClaim.signature) throw new ForgeError("pending_claim", "Retry the pending original issue_claim unchanged before another issue/attempt");
    this.#pendingClaim ??= { signature, body: { ...body, attempt_id: uuidv7(), ttl_seconds: this.#ttl } };
    const attempt = this.#pendingClaim;
    this.#secrets.add(attempt.body.attempt_id);
    this.#mode = "claim-pending";
    try {
      const reply = await this.#request("issue_claim", attempt.body, { signal });
      if (reply.body.granted === false) {
        this.#pendingClaim = undefined;
        this.#stop("Claim denied; no execution authority");
        return this.sanitize(reply.body);
      }
      const lease = object(reply.body.lease);
      if (reply.body.granted !== true || ![lease.claim_uid, lease.issue_uid, lease.holder, lease.holder_instance_uid, lease.client_kind, reply.body.execution_token, reply.body.execution_id].every(requiredString)) {
        throw new ForgeError("invalid_claim", "Ambiguous claim response; retry original issue_claim", 0, true);
      }
      const deadline = this.#deadline(lease, reply.body.server_now, reply.started);
      this.#active = { alias: body.ref, issueUID: lease.issue_uid, claimUID: lease.claim_uid,
        holder: lease.holder, holderInstanceUID: lease.holder_instance_uid, clientKind: lease.client_kind,
        token: reply.body.execution_token, executionID: reply.body.execution_id, deadline };
      this.#boundRef = lease.issue_uid;
      this.#leaseView = this.sanitize(lease);
      this.#issue = undefined;
      this.#pendingClaim = undefined;
      this.#mode = "live";
      this.#reason = "Exact execution lease confirmed";
      this.#checkExpiry();
      this.#schedule();
      return this.sanitize(reply.body);
    } catch (error) {
      if (error instanceof ForgeError && !error.ambiguous) this.#pendingClaim = undefined;
      this.#stop(this.#pendingClaim ? "Claim outcome ambiguous; retry original issue_claim (same attempt retained)" : "Claim denied; no execution authority");
      throw error;
    }
  }

  #observe(body: ObjectJSON, started: number) {
    this.#issue = object(body.issue);
    this.#leaseView = this.sanitize(body.lease ?? null);
    this.#checkExpiry();
    const active = this.#active;
    if (!active) return;
    const lease = object(body.lease);
    if (!this.#sameLease(lease, active) || this.#issue.uid !== active.issueUID) {
      this.#stop("Exact lease lost/replaced/released; stop editing and do not reuse the old tenure", true);
      return;
    }
    active.deadline = Math.min(active.deadline, this.#deadline(lease, body.lease_hub_now, started));
    this.#checkExpiry();
    if (!this.#active || this.#pendingClose) return;
    this.#mode = "live";
    this.#reason = "Exact execution lease freshly confirmed";
    // Frequent edit preflights must not postpone the already scheduled heartbeat.
    if (this.#timer === undefined) this.#schedule();
  }
  async #refresh(signal?: AbortSignal) {
    if (!this.#boundRef) return;
    try {
      const reply = await this.#request("issue_get", { ref: this.#boundRef }, { signal });
      this.#observe(reply.body, reply.started);
    } catch (error) {
      this.#stop("Cannot confirm current lease; stop editing until exact server confirmation");
      throw error;
    }
  }

  async #renew(body: ObjectJSON, signal?: AbortSignal): Promise<ObjectJSON> {
    if (this.#pendingClose) throw new ForgeError("pending_close", "Retry pending original close before renewing");
    const active = this.#requireActive(body.ref);
    try {
      const reply = await this.#request("issue_renew", { ref: active.issueUID, ttl_seconds: this.#ttl }, { token: active.token, signal });
      if (reply.body.granted !== true || !this.#sameLease(object(reply.body.lease), active)) {
        throw new ForgeError("claim_lost", "Renew did not confirm the exact tenure", 409);
      }
      // A response arriving after the old safe deadline must not resurrect it.
      this.#checkExpiry();
      if (!this.#active) throw new ForgeError("claim_expired", "Renew arrived after safe deadline; start a new tenure", 409);
      active.deadline = this.#deadline(reply.body.lease, reply.body.server_now, reply.started);
      this.#leaseView = this.sanitize(reply.body.lease);
      this.#mode = "live";
      this.#reason = "Exact lease renewed";
      this.#checkExpiry();
      this.#schedule();
      return this.sanitize(reply.body);
    } catch (error) {
      const ambiguous = !(error instanceof ForgeError) || error.ambiguous;
      this.#stop(ambiguous ? "Renew outcome uncertain; stop editing while retrying" : "Renew rejected; old execution cannot edit", !ambiguous);
      if (ambiguous) this.#schedule(1000);
      throw error;
    }
  }

  async #release(body: ObjectJSON, signal?: AbortSignal): Promise<ObjectJSON> {
    if (this.#pendingClose) throw new ForgeError("pending_close", "Finish the pending original close retry before release");
    const active = this.#requireActive(body.ref);
    this.#stop("Release pending; stop editing");
    try {
      const reply = await this.#request("issue_release", { ...body, ref: active.issueUID }, { token: active.token, signal });
      if (reply.body.granted !== true) throw new ForgeError("claim_lost", "Release did not confirm the current lease", 409);
      this.#clearTenure();
      return this.sanitize(reply.body);
    } catch (error) {
      this.#stop("Release unconfirmed; stop editing and retry release", error instanceof ForgeError && !error.ambiguous);
      throw error;
    }
  }

  async #close(body: ObjectJSON, signal?: AbortSignal): Promise<ObjectJSON> {
    // K7 receipt-first: pending retry is deliberately BEFORE all live checks.
    if (this.#pendingClose) {
      const pending = this.#pendingClose;
      const sameRef = body.ref === pending.alias || body.ref === pending.body.ref;
      if (!sameRef || stable({ ...body, ref: pending.body.ref }) !== pending.signature) {
        throw new ForgeError("pending_close", "A different close cannot reuse the pending key; first retry the ORIGINAL issue_close unchanged");
      }
    } else {
      const active = this.#requireActive(body.ref);
      await this.#refresh(signal);
      if (this.#mode !== "live" || this.#active !== active) throw new ForgeError("claim_lost", "Cannot close without current exact lease", 409);
      const snapshot = { ...body, ref: active.issueUID };
      this.#pendingClose = { body: snapshot, signature: stable(snapshot), alias: active.alias, token: active.token, key: randomUUID() };
    }
    const pending = this.#pendingClose;
    this.#stop("Close outcome pending; stop editing and retry original close if needed");
    try {
      const reply = await this.#request("issue_close", pending.body, { token: pending.token, key: pending.key, signal });
      if (!reply.body.issue || typeof reply.body.changed !== "boolean") {
        throw new ForgeError("invalid_close", "Ambiguous close response; retry original close", 0, true);
      }
      this.#pendingClose = undefined;
      this.#issue = object(reply.body.issue);
      this.#clearTenure();
      return this.sanitize(reply.body);
    } catch (error) {
      if (error instanceof ForgeError && !error.ambiguous) {
        this.#pendingClose = undefined;
        if ((error.status === 409 && /claim|lease|execution/.test(error.code)) || error.status === 401 || error.status === 403) this.#stop("Close rejected; stop editing and acquire a new tenure", true);
        else { this.#stop("Close rejected; refresh the exact lease before editing or correcting evidence"); this.#schedule(); }
      }
      throw error;
    }
  }
  #clearTenure() {
    this.#active = undefined;
    this.#pendingClaim = undefined;
    this.#leaseView = null;
    this.#cancelTimer();
    if (this.#mode !== "stopped") { this.#mode = "idle"; this.#reason = "Execution finished/released; claim before editing"; }
  }

  async #request(name: ToolName, body: ObjectJSON, options: { token?: string; key?: string; signal?: AbortSignal; detached?: boolean; timeout?: number } = {}): Promise<{ body: ObjectJSON; started: number }> {
    const started = this.#clock.now();
    const controller = new AbortController();
    const signals = [options.signal, ...(options.detached ? [] : [this.#abort.signal])].filter((s): s is AbortSignal => !!s);
    const abort = () => controller.abort();
    for (const signal of signals) { if (signal.aborted) abort(); else signal.addEventListener("abort", abort, { once: true }); }
    // Real bounded deadline even with an injected/noncooperative fetch transport.
    const timer = setTimeout(abort, options.timeout ?? this.#timeout);
    let rejectAbort: () => void = () => {};
    const aborted = new Promise<never>((_, reject) => {
      rejectAbort = () => reject(new ForgeError("ambiguous_request", "Request outcome ambiguous (network/timeout/cancel); retry the original operation", 0, true));
      controller.signal.addEventListener("abort", rejectAbort, { once: true });
      if (controller.signal.aborted) rejectAbort();
    });
    try {
      const headers: Record<string, string> = { "Content-Type": "application/json", Authorization: `Bearer ${this.#workerToken}`, "X-Forge-Session": this.#sessionId };
      if (options.token) headers["X-Forge-Execution"] = options.token;
      if (options.key) headers["Idempotency-Key"] = options.key;
      const exchange = async () => {
        controller.signal.throwIfAborted();
        const response = await this.#fetch(`${this.#url}/forge/v1/tools/${name}`, {
          method: "POST", headers, body: JSON.stringify(body), redirect: "error", signal: controller.signal,
        });
        let data: ObjectJSON;
        try { data = object(await response.json()); }
        catch { throw new ForgeError("ambiguous_response", "Unreadable response; retry original operation", response.status, true); }
        this.sanitize(data); // Register echoed secrets before constructing any error.
        if (!response.ok) {
          const err = this.sanitize(object(data.error));
          const ambiguous = response.status >= 500 || response.status === 408 || response.status === 429;
          throw new ForgeError(requiredString(err.code) ? err.code : "http_error",
            `${requiredString(err.message) ? err.message : "Forge rejected request"}${err.hint ? ` (${err.hint})` : ""}${ambiguous ? "; outcome ambiguous: retry original operation" : ""}`,
            response.status, ambiguous);
        }
        return data;
      };
      const data = await Promise.race([exchange(), aborted]);
      if (!options.detached) this.#assertRunning();
      return { body: data, started };
    } catch (error) {
      if (error instanceof ForgeError) throw error;
      // Native fetch failures may contain URLs/headers; never expose their cause.
      throw new ForgeError("ambiguous_request", "Request outcome ambiguous (network/timeout/cancel); retry original operation", 0, true);
    } finally {
      clearTimeout(timer);
      controller.signal.removeEventListener("abort", rejectAbort);
      for (const signal of signals) signal.removeEventListener("abort", abort);
    }
  }
}
