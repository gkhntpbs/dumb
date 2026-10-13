import { readFile, writeFile } from 'node:fs/promises';
import { parse } from 'yaml';

const spec = parse(await readFile(new URL('../../../docs/protocol/openapi.yaml', import.meta.url), 'utf8'));
const paths = {};
for (const [path, operations] of Object.entries(spec.paths)) {
  for (const operation of Object.values(operations)) {
    if (!operation.operationId?.startsWith('admin')) continue;
    const key = operation.operationId.slice(5);
    paths[key[0].toLowerCase() + key.slice(1)] = path;
  }
}
const output = '// Generated from docs/protocol/openapi.yaml; do not edit.\nexport const adminPaths = Object.freeze(' + JSON.stringify(paths, null, 2) + ');\n';
const destination = new URL('../../../internal/server/adminui/assets/admin-contract.js', import.meta.url);
if (process.argv.includes('--check')) {
  if (await readFile(destination, 'utf8') !== output) throw new Error('Admin contract differs; run npm run generate');
} else {
  await writeFile(destination, output);
}
