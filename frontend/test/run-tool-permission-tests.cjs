const {mkdtempSync, rmSync, writeFileSync} = require('fs');
const {tmpdir} = require('os');
const {join} = require('path');
const {spawnSync} = require('child_process');

const outDir = mkdtempSync(join(tmpdir(), 'atelier-tool-permission-'));

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
    'src/toolPermission.ts',
    'test/toolPermission.assert.ts',
  ], {stdio: 'inherit'});
  if (compile.status !== 0) {
    throw new Error(`tool permission compile failed with exit ${compile.status || 1}`);
  }

  require(join(outDir, 'test/toolPermission.assert.js'));
  console.log('tool permission tests passed');
} finally {
  rmSync(outDir, {recursive: true, force: true});
}
