import test from "node:test";
import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { DUMBClient, HTTPError, JobError, ProtocolError, limits } from "../dist/index.js";

const request = { model: "fixture", prompt: "Return a short answer", deadline_ms: 1000, num_predict: 17 };
const response = (code, body, headers = {}) => new Response(JSON.stringify(body), { status: code, headers });
const job = (state, extra = {}) => ({ job_id: "job", state, ...extra });
const client = fetch => new DUMBClient({ baseURL: "http://127.0.0.1:9000", token: randomUUID(), fetch });
const stalled = requests => async (url, init) => {
  requests.push({ url, init });
  return new Promise((resolve, reject) => {
    if (init.signal.aborted) return reject(init.signal.reason);
    init.signal.addEventListener("abort", () => reject(init.signal.reason), { once: true });
  });
};

test("successful completion forwards contract fields and never invokes fallback", async () => {
  let fallback = 0;
  let submissions = 0;
  const c = client(async (url, init) => {
    submissions++;
    assert.equal(url.pathname, "/v1/jobs");
    assert.equal(url.searchParams.get("wait"), "1000ms");
    assert.equal(init.method, "POST");
    assert.equal(init.redirect, "error");
    assert.equal(init.headers.get("X-Request-ID"), "unit-request");
    assert.deepEqual(JSON.parse(init.body), request);
    return response(200, job("done", { output: "ok" }));
  });
  const result = await c.execute(request, { requestId: "unit-request", fallback: async () => { fallback++; return "other"; } });
  assert.equal(result.source, "dumb");
  assert.equal(result.output, "ok");
  assert.equal(submissions, 1);
  assert.equal(fallback, 0);
});

test("503 invokes the injected fallback immediately", async () => {
  const started = performance.now();
  const c = client(async () => response(503, { error: "no online worker" }));
  const result = await c.execute({ ...request, deadline_ms: 5000 }, {
    hedgeMs: 4000,
    fallback: async (input, context) => {
      assert.deepEqual(input, { ...request, deadline_ms: 5000 });
      assert.equal(context.reason, "unavailable");
      return { provider: "injected", text: "fallback" };
    },
  });
  assert.equal(result.source, "fallback");
  assert.equal(result.reason, "unavailable");
  assert.equal(result.value.text, "fallback");
  assert.ok(performance.now() - started < 1000);
});

test("queued submission polls the same job without duplicate submissions", async () => {
  const calls = [];
  const c = client(async (url, init) => {
    calls.push({ url, init });
    return calls.length === 1 ? response(202, job("queued")) : response(200, job("done", { output: "later" }));
  });
  assert.equal((await c.execute(request)).output, "later");
  assert.deepEqual(calls.map(call => call.init.method), ["POST", "GET"]);
  assert.equal(calls[1].url.pathname, "/v1/jobs/job");
  assert.ok(parseInt(calls[1].url.searchParams.get("wait")) <= request.deadline_ms);
});

test("low-level submit, wait and availability use exact caller routes", async () => {
  const calls = [];
  const c = client(async (url, init) => {
    calls.push({ url, init });
    if (url.pathname === "/v1/status") return response(200, { workers_online: 1, models: ["fixture"] });
    return response(202, job("leased"));
  });
  await c.submit(request);
  await c.getJob("a/b", { waitMs: 12.9 });
  assert.deepEqual(await c.status(), { workers_online: 1, models: ["fixture"] });
  assert.equal(calls[0].url.search, "?wait=0ms");
  assert.equal(calls[1].url.pathname, "/v1/jobs/a%2Fb");
  assert.equal(calls[1].url.search, "?wait=12ms");
  assert.equal(calls[2].url.search, "");
});

for (const [state, code] of [["expired", 410], ["failed", 200]]) {
  test(`${state} job follows the injected fallback policy`, async () => {
    const c = client(async () => response(code, job(state, { error: "fixture failure" })));
    const result = await c.execute(request, { fallback: async (_, context) => {
      assert.equal(context.job.state, state);
      return context.reason;
    } });
    assert.equal(result.source, "fallback");
    assert.equal(result.value, state);
    await assert.rejects(c.execute(request), error => error instanceof JobError && error.reason === state);
  });
}

for (const [options, deadline, reason] of [[{ hedgeMs: 30 }, 1000, "hedge"], [{}, 30, "expired"]]) {
  test(`${reason} budget aborts the local request before fallback`, async () => {
    const requests = [];
    const c = client(stalled(requests));
    const result = await c.execute({ ...request, deadline_ms: deadline }, { ...options, fallback: async (_, context) => {
      assert.ok(requests[0].init.signal.aborted);
      return context.reason;
    } });
    assert.equal(result.reason, reason);
    assert.equal(requests.length, 1);
    assert.equal(requests[0].init.method, "POST");
  });
}

test("abort propagates during submission and never starts fallback or retries", async () => {
  const requests = [];
  const controller = new AbortController();
  let fallback = 0;
  const pending = client(stalled(requests)).execute(request, { signal: controller.signal, fallback: async () => { fallback++; } });
  controller.abort(new Error("consumer stopped"));
  await assert.rejects(pending, /consumer stopped/);
  assert.ok(requests[0].init.signal.aborted);
  assert.equal(requests.length, 1);
  assert.equal(fallback, 0);
});

test("pre-aborted signal sends no request", async () => {
  let calls = 0;
  const c = client(async () => { calls++; return response(200, job("done")); });
  await assert.rejects(c.execute(request, { signal: AbortSignal.abort() }));
  assert.equal(calls, 0);
});

test("fallback receives the consumer signal rather than the expired hedge signal", async () => {
  const controller = new AbortController();
  const c = client(async () => response(503, { error: "none" }));
  const pending = c.execute(request, { signal: controller.signal, fallback: async (_, context) => {
    assert.equal(context.signal, controller.signal);
    return new Promise((resolve, reject) => context.signal.addEventListener("abort", () => reject(context.signal.reason), { once: true }));
  } });
  await new Promise(resolve => setImmediate(resolve));
  controller.abort(new Error("fallback stopped"));
  await assert.rejects(pending, /fallback stopped/);
});

for (const status of [400, 401, 404, 429, 500]) {
  test(`HTTP ${status} is exposed without retries or implicit fallback`, async () => {
    let calls = 0;
    let fallback = 0;
    const c = client(async () => { calls++; return response(status, { error: "fixture error" }, { "Retry-After": "2", "X-Request-ID": "http-error" }); });
    await assert.rejects(c.execute(request, { fallback: async () => { fallback++; } }), error => {
      assert.ok(error instanceof HTTPError);
      assert.equal(error.status, status);
      assert.equal(error.retryAfter, "2");
      assert.equal(error.requestId, "http-error");
      return true;
    });
    assert.equal(calls, 1);
    assert.equal(fallback, 0);
  });
}

test("network failure is not retried because submission may already exist", async () => {
  let calls = 0;
  const c = client(async () => { calls++; throw new TypeError("connection lost"); });
  await assert.rejects(c.execute(request), /connection lost/);
  assert.equal(calls, 1);
});

test("malformed JSON and invalid response shapes fail clearly", async () => {
  await assert.rejects(client(async () => new Response("{" )).submit(request), ProtocolError);
  await assert.rejects(client(async () => response(200, { job_id: "job", state: "unknown" })).submit(request), ProtocolError);
  await assert.rejects(client(async () => response(200, { workers_online: "one", models: [] })).status(), ProtocolError);
});

test("contract-derived limits reject invalid budget/cap before sending", async () => {
  let calls = 0;
  const c = client(async () => { calls++; return response(200, job("done")); });
  for (const deadline_ms of [0, -1, 0.5, limits.maxDeadlineMs + 1]) await assert.rejects(c.submit({ ...request, deadline_ms }), RangeError);
  for (const num_predict of [-2, -1, 0.5, limits.maxNumPredict + 1]) await assert.rejects(c.submit({ ...request, num_predict }), RangeError);
  await assert.rejects(c.getJob("job", { waitMs: NaN }), RangeError);
  await assert.rejects(c.execute(request, { hedgeMs: 0 }), RangeError);
  assert.equal(calls, 0);
});

test("remote plaintext and credential-bearing URLs are refused", () => {
  for (const baseURL of ["http://example.invalid", "https://user:password@example.invalid", "https://example.invalid?token=hidden", "https://example.invalid#fragment"]) {
    assert.throws(() => new DUMBClient({ baseURL, token: randomUUID() }), TypeError);
  }
});

test("consumer abort rejects even when an injected fallback ignores the signal", async () => {
  const controller = new AbortController();
  let release;
  const c = client(async () => response(503, {error:"none"}));
  const pending = c.execute(request, {signal:controller.signal,fallback:()=>new Promise(resolve=>{release=resolve;})});
  await new Promise(resolve=>setImmediate(resolve));
  controller.abort(new Error("consumer ended"));
  await assert.rejects(pending,/consumer ended/);
  release("late result");
});

test("global queue saturation invokes consumer fallback once without retry or job polling", async () => {
  const calls = [];
  let fallbacks = 0;
  const c = client(async (url, init) => {
    calls.push({ url, init });
    return response(503, { error: "queue capacity exhausted" }, { "Retry-After": "1" });
  });
  const result = await c.execute(request, { fallback: async (input, context) => {
    fallbacks++;
    assert.deepEqual(input, request);
    assert.equal(context.reason, "unavailable");
    assert.equal(context.job, undefined);
    return "consumer result";
  } });
  assert.equal(result.source, "fallback");
  assert.equal(result.value, "consumer result");
  assert.equal(fallbacks, 1);
  assert.equal(calls.length, 1);
  assert.equal(calls[0].init.method, "POST");
  assert.equal(calls[0].url.pathname, "/v1/jobs");
});

test("caller queue saturation exposes retry advice without implicit retry or fallback", async () => {
  let calls = 0;
  let fallbacks = 0;
  const c = client(async () => {
    calls++;
    return response(429, { error: "caller queue capacity exhausted" }, { "Retry-After": "1" });
  });
  await assert.rejects(c.execute(request, { fallback: async () => { fallbacks++; } }), error => {
    assert.ok(error instanceof HTTPError);
    assert.equal(error.status, 429);
    assert.equal(error.retryAfter, "1");
    return true;
  });
  assert.equal(calls, 1);
  assert.equal(fallbacks, 0);
});
