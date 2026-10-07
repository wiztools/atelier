const {mkdtempSync, rmSync, writeFileSync} = require('fs');
const {tmpdir} = require('os');
const {join} = require('path');
const {spawnSync} = require('child_process');

const outDir = mkdtempSync(join(tmpdir(), 'atelier-crop-'));

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
    'src/editor/cropGeometry.ts',
    'test/cropGeometry.assert.ts',
  ], {stdio: 'inherit'});
  if (compile.status !== 0) {
    throw new Error(`crop geometry compile failed with exit ${compile.status || 1}`);
  }
  const run = spawnSync(process.execPath, [join(outDir, 'test/cropGeometry.assert.js')], {stdio: 'inherit'});
  if (run.status !== 0) {
    throw new Error(`crop geometry assertions failed with exit ${run.status || 1}`);
  }
} finally {
  rmSync(outDir, {recursive: true, force: true});
}
