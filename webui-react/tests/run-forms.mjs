// No extra test framework: compile the actual form sources with the installed
// TypeScript, then use Node's test runner. Temporary output is always removed.
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { spawnSync } from 'node:child_process';

const require = createRequire(import.meta.url);
const compilerManifest = require.resolve('typescript/package.json');
const compiler = join(dirname(compilerManifest), JSON.parse(readFileSync(compilerManifest, 'utf8')).bin.tsc);
const root = fileURLToPath(new URL('../', import.meta.url));
// Keep output beneath node_modules so imports resolve the exact installed deps.
const buildDir = mkdtempSync(join(root, 'node_modules/.forms-test-'));
function run(args, env = process.env) {
  const result = spawnSync(process.execPath, args, { cwd: root, stdio: 'inherit', env });
  if (result.error) throw result.error;
  if (result.status !== 0) throw new Error(`Form test command failed (${result.status ?? result.signal})`);
}
try {
  writeFileSync(join(buildDir, 'package.json'), '{"type":"module"}\n');
  run([compiler, '--project', 'tests/tsconfig.forms.json', '--outDir', buildDir]);
  run(['--test', 'tests/forms.test.mjs'], { ...process.env, KVMC_FORMS_TEST_BUILD: buildDir });
} finally {
  rmSync(buildDir, { recursive: true, force: true });
}
