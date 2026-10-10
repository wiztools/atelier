const {mkdtempSync, rmSync, writeFileSync} = require('fs');
const {tmpdir} = require('os');
const {join} = require('path');
const {spawnSync} = require('child_process');

const outDir = mkdtempSync(join(tmpdir(), 'atelier-editor-launch-'));

try {
  writeFileSync(join(outDir, 'package.json'), '{"type":"commonjs"}\n');
  const tsc = require.resolve('typescript/bin/tsc');
  const compile = spawnSync(process.execPath, [
    tsc,
    '--target', 'ES2020',
    '--module', 'CommonJS',
    '--moduleResolution', 'Node',
    '--strict',
    '--skipLibCheck',
    '--outDir', outDir,
    '--rootDir', '.',
    'src/editorLaunch.ts',
    'test/editorLaunch.assert.ts',
  ], {stdio: 'inherit'});
  if (compile.status !== 0) {
    throw new Error(`editor launch compile failed with exit ${compile.status || 1}`);
  }
  const run = spawnSync(process.execPath, [join(outDir, 'test/editorLaunch.assert.js')], {stdio: 'inherit'});
  if (run.status !== 0) {
    throw new Error(`editor launch assertions failed with exit ${run.status || 1}`);
  }
} finally {
  rmSync(outDir, {recursive: true, force: true});
}
