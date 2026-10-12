import test from "node:test";
import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { DUMBClient, HTTPError } from "../dist/index.js";

const baseURL = process.env.QA_DUMB_URL;
const token = process.env.QA_CALLER_TOKEN;
const workerToken = process.env.QA_WORKER_TOKEN;
if (!baseURL || !token || !workerToken) throw new Error("Integration needs the Go QA fixture and QA_ credentials");
const client = new DUMBClient({ baseURL, token });
const input = { model: "fixture", prompt: "Return a short answer", deadline_ms: 1000, num_predict: 17 };
const headers = { Authorization: `Bearer ${workerToken}`, "X-DUMB-Worker-Version": "0.1.0", "Content-Type": "application/json" };
async function worker(path, method = "POST", body) {
  const init = { method, headers };
  if (body !== undefined) init.body = JSON.stringify(body);
  return fetch(`${baseURL}${path}`, init);
}
async function finish(assignment, output) {
  assert.equal((await worker(`/v1/jobs/${assignment.job_id}/ack`, "POST", { lease_id: assignment.lease_id })).status, 204);
  assert.equal((await worker(`/v1/jobs/${assignment.job_id}/result`, "POST", { lease_id: assignment.lease_id, output })).status, 204);
}

test("actual Go server: availability, immediate 503, submit/wait, execution, expiry and errors", async () => {
  assert.equal((await client.status()).workers_online, 0);
  const absent = await client.execute(input, { fallback: async (_, context) => context.reason });
  assert.equal(absent.value, "unavailable");
  assert.equal((await worker("/v1/workers/hello", "POST", { worker_version: "0.1.0", protocol_version: 1, models: ["fixture"], capacity: 1, features: ["ack"] })).status, 200);
  assert.equal((await client.status()).workers_online, 1);
  const submitted = await client.submit(input);
  assert.equal(submitted.state, "queued");
  const assignment = await (await worker("/v1/jobs/next?wait=500ms", "GET")).json();
  assert.equal(assignment.job_id, submitted.job_id);
  assert.equal(assignment.num_predict, 17);
  const pending = client.getJob(submitted.job_id, { waitMs: 500 });
  await finish(assignment, "first answer");
  assert.equal((await pending).output, "first answer");
  let fallback = 0;
  const execution = client.execute(input, { fallback: async () => { fallback++; return "other"; } });
  const second = await (await worker("/v1/jobs/next?wait=500ms", "GET")).json();
  await finish(second, "second answer");
  assert.equal((await execution).output, "second answer");
  assert.equal(fallback, 0);
  const expiring = await client.submit({ ...input, deadline_ms: 40 });
  assert.equal((await client.getJob(expiring.job_id, { waitMs: 100 })).state, "expired");
  await assert.rejects(client.getJob("missing"), error => error instanceof HTTPError && error.status === 404);
  const unauthorized = new DUMBClient({ baseURL, token: randomUUID() });
  await assert.rejects(unauthorized.status(), error => error instanceof HTTPError && error.status === 401);
  const hedged = await client.execute(input, { hedgeMs: 30, fallback: async (_, context) => context.reason });
  assert.equal(hedged.reason, "hedge");
  const stillLive = await (await worker("/v1/jobs/next?wait=100ms", "GET")).json();
  assert.equal(stillLive.prompt, input.prompt);
  await finish(stillLive, "result after caller hedge");
  assert.equal((await worker("/v1/workers/goodbye")).status, 204);
  assert.equal((await client.status()).workers_online, 0);
});
