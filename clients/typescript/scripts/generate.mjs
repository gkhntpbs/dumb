import { readFile, writeFile } from "node:fs/promises";
import openapiTS, { astToString } from "openapi-typescript";
import ts from "typescript";
import { parse } from "yaml";

const specURL = new URL("../../../docs/protocol/openapi.yaml", import.meta.url);
const source = await readFile(specURL, "utf8");
const spec = parse(source);
const ast = await openapiTS(spec);
const parsed = ts.createSourceFile("contract.ts", astToString(ast), ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
const printer = ts.createPrinter({ removeComments: true, newLine: ts.NewLineKind.LineFeed });
const submit = spec.components.schemas.SubmitRequest.properties;
const output = `${printer.printFile(parsed)}\nexport const limits = ${JSON.stringify({ maxDeadlineMs: submit.deadline_ms.maximum, maxNumPredict: submit.num_predict.maximum, defaultNumPredict: submit.num_predict.default })} as const;\n`;
const destination = new URL("../src/contract.ts", import.meta.url);
if (process.argv.includes("--check")) {
  if (await readFile(destination, "utf8") !== output) {
    throw new Error("Generated contract differs from docs/protocol/openapi.yaml; run npm run generate");
  }
} else {
  await writeFile(destination, output);
}
