import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { cp, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";
import path from "node:path";

const root = fileURLToPath(new URL("..", import.meta.url));
const consumer = await mkdtemp(path.join(tmpdir(), "dumb-caller-package-"));
function run(binary, args, cwd) {
  const result = spawnSync(binary, args, { cwd, encoding: "utf8", timeout: 60000 });
  if (result.status !== 0) throw new Error(`${path.basename(binary)} failed: ${result.stderr || result.stdout || result.error}`);
  return result.stdout;
}
try {
  const standalone = path.join(consumer, "standalone");
  await cp(root, standalone, { recursive: true, filter: source => !source.includes(`${path.sep}node_modules`) && !source.includes(`${path.sep}dist`) });
  run("npm", ["ci", "--offline", "--ignore-scripts", "--no-audit", "--no-fund"], standalone);
  run("npm", ["run", "build"], standalone);
  run("npm", ["run", "test:unit"], standalone);
  // npm 11 prints an array, npm 12 an object keyed by package name.
  const packOutput = JSON.parse(run("npm", ["pack", "--json", "--pack-destination", consumer], root));
  const packed = Array.isArray(packOutput) ? packOutput[0] : Object.values(packOutput)[0];
  const allowed = ["package.json", "README.md", "LICENSE"];
  assert.ok(packed.files.every(file => file.path.startsWith("dist/") || allowed.includes(file.path)));
  const metadata = JSON.parse(await readFile(path.join(root, "package.json"), "utf8"));
  assert.equal(Object.keys(metadata.dependencies ?? {}).length, 0);
  await writeFile(path.join(consumer, "package.json"), JSON.stringify({ private: true, type: "module" }));
  run("npm", ["install", "--offline", "--ignore-scripts", "--no-audit", "--no-fund", path.join(consumer, packed.filename)], consumer);
  await cp(path.join(root, "examples"), path.join(consumer, "examples"), { recursive: true });
  await writeFile(path.join(consumer, "tsconfig.json"), JSON.stringify({ compilerOptions: { target: "ES2022", module: "NodeNext", moduleResolution: "NodeNext", lib: ["ES2022", "DOM"], strict: true, outDir: "compiled" }, include: ["examples/*.ts"] }));
  run(process.execPath, [path.join(root, "node_modules/typescript/bin/tsc"), "-p", path.join(consumer, "tsconfig.json")], consumer);
  await writeFile(path.join(consumer, "verify.mjs"), `
import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { runBatch } from "./compiled/node-batch.js";
import { createHandler } from "./compiled/fetch-handler.js";
const config = { baseURL: "https://dumb.example.invalid", token: randomUUID(), fetch: async () => Response.json({job_id:"consumer",state:"done",output:"packed answer"}) };
const input = {model:"fixture",prompt:"A short answer",deadline_ms:3000};
assert.deepEqual(await runBatch(config,[input]),["packed answer"]);
const handler = createHandler({...config,fetch:async()=>Response.json({error:"none"},{status:503})},async()=>"injected fallback");
const result = await handler(new Request("https://consumer.example.invalid",{method:"POST",body:JSON.stringify(input)}));
assert.deepEqual(await result.json(),{source:"fallback",output:"injected fallback"});
`);
  run(process.execPath, ["verify.mjs"], consumer);
  const runtime = await readFile(path.join(consumer, "node_modules/@dumb/caller/dist/index.js"), "utf8");
  assert.ok(!/node:|\bprocess\.|\bBuffer\b/.test(runtime));
  console.log("Standalone source builds and tests; packed package has zero runtime dependencies; independent Node and fetch-runtime consumers compile and run");
} finally {
  await rm(consumer, { recursive: true, force: true });
}
