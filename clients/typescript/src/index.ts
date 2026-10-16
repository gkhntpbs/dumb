import type { components, paths } from "./contract.js";
import { limits } from "./contract.js";

export type { components, paths } from "./contract.js";
export { limits } from "./contract.js";
export type SubmitRequest = components["schemas"]["SubmitRequest"];
export type JobStatus = components["schemas"]["JobStatus"];
export type Status = components["schemas"]["Status"];
export type FallbackReason = "unavailable" | "expired" | "failed" | "hedge";
/** Why execute ended without DUMB output; "cancelled" never invokes the fallback. */
export type EndReason = FallbackReason | "cancelled";

export interface ClientConfig {
  baseURL: string;
  token: string;
  fetch?: typeof globalThis.fetch;
}

export interface RequestOptions {
  signal?: AbortSignal;
  requestId?: string;
}

export interface WaitOptions extends RequestOptions {
  waitMs?: number;
}

export interface SubmitOptions extends WaitOptions {
 idempotencyKey?: string;
}

export interface FallbackContext extends RequestOptions {
  reason: FallbackReason;
  job?: JobStatus;
}

export interface ExecuteOptions<T> extends RequestOptions {
 idempotencyKey?: string;
  hedgeMs?: number;
  /**
   * When execute stops waiting for a job that is still queued or leased (hedge,
   * elapsed budget, consumer abort or an error while polling), send one
   * best-effort POST /v1/jobs/{id}/cancel. Off by default: no hidden requests.
   */
  cancelOnAbandon?: boolean;
  fallback?: (request: SubmitRequest, context: FallbackContext) => Promise<T>;
}

export type ExecutionResult<T> =
  | { source: "dumb"; output: string; thinking?: string; done_reason?: string; job: JobStatus }
  | { source: "fallback"; value: T; reason: FallbackReason };

export class HTTPError extends Error {
  constructor(
    public readonly status: number,
    public readonly requestId: string | null,
    public readonly retryAfter: string | null,
    message: string,
  ) {
    super(message);
    this.name = "HTTPError";
  }
}

export class ProtocolError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "ProtocolError";
  }
}

export class JobError extends Error {
  constructor(public readonly reason: EndReason, public readonly job?: JobStatus) {
    super(`DUMB execution ended: ${reason}`);
    this.name = "JobError";
  }
}

function object(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function jobStatus(value: unknown): JobStatus {
  if (!object(value) || typeof value.job_id !== "string" || !["queued", "leased", "done", "failed", "expired", "cancelled"].includes(String(value.state)) || (value.output !== undefined && typeof value.output !== "string") || (value.error !== undefined && typeof value.error !== "string") || (value.thinking !== undefined && typeof value.thinking !== "string") || (value.done_reason !== undefined && typeof value.done_reason !== "string")) {
    throw new ProtocolError("Invalid job status response");
  }
  return value as unknown as JobStatus;
}

function waitQuery(waitMs: number = 0): string {
  if (!Number.isFinite(waitMs) || waitMs < 0) throw new RangeError("waitMs must be finite and non-negative");
  return `wait=${Math.floor(waitMs)}ms`;
}

function validateRequest(request: SubmitRequest): void {
  if (request.think !== undefined && typeof request.think !== "boolean") throw new TypeError("think must be a boolean");
  if (!Number.isInteger(request.deadline_ms) || request.deadline_ms < 1 || request.deadline_ms > limits.maxDeadlineMs) throw new RangeError("deadline_ms is out of range");
  if (request.num_predict !== undefined && (!Number.isInteger(request.num_predict) || request.num_predict < 0 || request.num_predict > limits.maxNumPredict)) throw new RangeError("num_predict is out of range");
}


async function abortable<T>(task: () => Promise<T>, signal?: AbortSignal): Promise<T> {
  if (signal === undefined) return task();
  signal.throwIfAborted();
  let onAbort: () => void = () => {};
  const aborted = new Promise<never>((_, reject) => {
    onAbort = () => reject(signal.reason);
    signal.addEventListener("abort", onAbort, { once: true });
  });
  try {
    return await Promise.race([task(), aborted]);
  } finally {
    signal.removeEventListener("abort", onAbort);
  }
}

export class DUMBClient {
  readonly #base: URL;
  readonly #token: string;
  readonly #fetch: typeof globalThis.fetch;

  constructor(config: ClientConfig) {
    this.#base = new URL(config.baseURL.endsWith("/") ? config.baseURL : `${config.baseURL}/`);
    const loopback = this.#base.hostname === "localhost" || this.#base.hostname === "[::1]" || /^127\.\d+\.\d+\.\d+$/.test(this.#base.hostname);
    if (this.#base.protocol !== "https:" && !(this.#base.protocol === "http:" && loopback)) throw new TypeError("baseURL must use HTTPS, except on loopback");
    if (this.#base.username || this.#base.password || this.#base.search || this.#base.hash) throw new TypeError("baseURL must not contain credentials, query or fragment");
    if (!config.token.trim()) throw new TypeError("token is required");
    this.#token = config.token;
    this.#fetch = config.fetch ?? globalThis.fetch.bind(globalThis);
  }

  async #request(method: "GET" | "POST", path: keyof paths | string, query: string, body: SubmitRequest | undefined, options: RequestOptions & { idempotencyKey?: string }, accepted: number[]): Promise<unknown> {
    options.signal?.throwIfAborted();
    const url = new URL(path.replace(/^\//, ""), this.#base);
    url.search = query;
    const headers = new Headers({ Authorization: `Bearer ${this.#token}` });
    if (options.requestId !== undefined) headers.set("X-Request-ID", options.requestId);
    if (options.idempotencyKey !== undefined) {
      if (!new RegExp(limits.idempotencyKeyPattern).test(options.idempotencyKey)) throw new RangeError("idempotencyKey must be 1 to 64 letters, digits, dots, underscores, colons or hyphens");
      if (method === "POST") headers.set("Idempotency-Key", options.idempotencyKey);
    }
    const init: RequestInit = { method, headers, redirect: "error" };
    if (options.signal !== undefined) init.signal = options.signal;
    if (body !== undefined) {
      headers.set("Content-Type", "application/json");
      init.body = JSON.stringify(body);
    }
    const response = await this.#fetch(url, init);
    if (!accepted.includes(response.status)) {
      let message = `DUMB HTTP ${response.status}`;
      try {
        const error: unknown = await response.json();
        if (object(error) && typeof error.error === "string") message = error.error;
      } catch {
        options.signal?.throwIfAborted();
      }
      throw new HTTPError(response.status, response.headers.get("X-Request-ID"), response.headers.get("Retry-After"), message);
    }
    try {
      return await response.json();
    } catch {
      options.signal?.throwIfAborted();
      throw new ProtocolError("DUMB returned invalid JSON");
    }
  }

  async submit(request: SubmitRequest, options: SubmitOptions = {}): Promise<JobStatus> {
    validateRequest(request);
    return jobStatus(await this.#request("POST", "/v1/jobs", waitQuery(options.waitMs), request, options, [200, 202, 410]));
  }

  async getJob(id: string, options: WaitOptions = {}): Promise<JobStatus> {
    if (!id) throw new TypeError("job id is required");
    return jobStatus(await this.#request("GET", `/v1/jobs/${encodeURIComponent(id)}`, waitQuery(options.waitMs), undefined, options, [200, 202, 410]));
  }

  /**
   * Cancel the caller's own queued or leased job with one POST (no retry).
   * Returns the cancelled status (200, also on repeat), or the unchanged
   * terminal status when the job already finished or expired (409).
   */
  async cancel(id: string, options: RequestOptions = {}): Promise<JobStatus> {
    if (!id) throw new TypeError("job id is required");
    return jobStatus(await this.#request("POST", `/v1/jobs/${encodeURIComponent(id)}/cancel`, "", undefined, options, [200, 409]));
  }

  async status(options: RequestOptions = {}): Promise<Status> {
    const value = await this.#request("GET", "/v1/status", "", undefined, options, [200]);
    if (!object(value) || !Number.isInteger(value.workers_online) || Number(value.workers_online) < 0 || !Array.isArray(value.models) || value.models.some(model => typeof model !== "string")) throw new ProtocolError("Invalid availability response");
    return value as unknown as Status;
  }

  async execute<T = never>(request: SubmitRequest, options: ExecuteOptions<T> = {}): Promise<ExecutionResult<T>> {
    validateRequest(request);
    if (options.hedgeMs !== undefined && (!Number.isFinite(options.hedgeMs) || options.hedgeMs <= 0)) throw new RangeError("hedgeMs must be finite and positive");
    options.signal?.throwIfAborted();
    const budget = Math.min(request.deadline_ms, options.hedgeMs ?? request.deadline_ms);
    const reason: FallbackReason = options.hedgeMs !== undefined && options.hedgeMs < request.deadline_ms ? "hedge" : "expired";
    const controller = new AbortController();
    const onAbort = () => controller.abort(options.signal?.reason);
    options.signal?.addEventListener("abort", onAbort, { once: true });
    const started = performance.now();
    const timer = setTimeout(() => controller.abort(new JobError(reason)), budget);
    let job: JobStatus | undefined;
    let fallbackReason: FallbackReason;
    let inFlight: Promise<JobStatus> | undefined;
    const cancelActive = (status: JobStatus) => {
      if (status.state !== "queued" && status.state !== "leased") return;
      const cancelOptions: RequestOptions = {};
      if (options.requestId !== undefined) cancelOptions.requestId = options.requestId;
      this.cancel(status.job_id, cancelOptions).catch(() => {});
    };
    const abandon = () => {
      if (!options.cancelOnAbandon) return;
      if (job !== undefined) cancelActive(job);
      // The budget ended while the submission was in flight: cancel the job as
      // soon as its id arrives. A consumer abort also aborts that request.
      else inFlight?.then(cancelActive, () => {});
    };
    try {
      const pending: WaitOptions = { signal: controller.signal };
      if (options.requestId !== undefined) pending.requestId = options.requestId;
      const submission: SubmitOptions = { ...pending, waitMs: budget };
      if (options.idempotencyKey !== undefined) submission.idempotencyKey = options.idempotencyKey;
      if (options.cancelOnAbandon) {
        // Keep the submission alive past the local budget so its job id is
        // learned and can be cancelled; only the consumer signal aborts it.
        const detached: SubmitOptions = { ...submission };
        if (options.signal !== undefined) detached.signal = options.signal;
        else delete detached.signal;
        const submitted = this.submit(request, detached);
        inFlight = submitted;
        submitted.catch(() => {});
        job = await abortable(() => submitted, controller.signal);
      } else {
        job = await this.submit(request, submission);
      }
      while (job.state === "queued" || job.state === "leased") {
        const remaining = budget - (performance.now() - started);
        if (remaining <= 0) throw new JobError(reason, job);
        job = await this.getJob(job.job_id, { ...pending, waitMs: remaining });
      }
      if (job.state === "done") return { source: "dumb", output: job.output ?? "", ...(job.thinking !== undefined ? { thinking: job.thinking } : {}), ...(job.done_reason !== undefined ? { done_reason: job.done_reason } : {}), job };
      if (job.state === "cancelled") throw new JobError("cancelled", job);
      fallbackReason = job.state === "expired" ? "expired" : "failed";
    } catch (error) {
      abandon();
      if (error instanceof JobError && error.reason === "cancelled") throw error;
      options.signal?.throwIfAborted();
      if (controller.signal.aborted && controller.signal.reason instanceof JobError) fallbackReason = controller.signal.reason.reason as FallbackReason;
      else if (error instanceof JobError) fallbackReason = error.reason as FallbackReason;
      else if (error instanceof HTTPError && error.status === 503) fallbackReason = "unavailable";
      else throw error;
    } finally {
      clearTimeout(timer);
      options.signal?.removeEventListener("abort", onAbort);
    }
    if (options.fallback === undefined) throw new JobError(fallbackReason, job);
    options.signal?.throwIfAborted();
    const context: FallbackContext = { reason: fallbackReason };
    if (options.signal !== undefined) context.signal = options.signal;
    if (options.requestId !== undefined) context.requestId = options.requestId;
    if (job !== undefined) context.job = job;
    const fallback = options.fallback;
    return { source: "fallback", value: await abortable(() => fallback(request, context), options.signal), reason: fallbackReason };
  }
}
