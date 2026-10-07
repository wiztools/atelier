const {existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync} = require('fs');
const {tmpdir} = require('os');
const {join, resolve} = require('path');
const {spawnSync} = require('child_process');

const fixturePath = resolve(process.argv[2] || 'test/fixtures/videoReframe.json');
if (!existsSync(fixturePath)) {
  throw new Error(`video reframe fixture not found: ${fixturePath}`);
}

const outDir = mkdtempSync(join(tmpdir(), 'atelier-video-reframe-'));

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
    'src/editor/videoReframe.ts',
    'test/videoReframe.assert.ts',
  ], {stdio: 'inherit'});
  if (compile.status !== 0) {
    throw new Error(`video reframe compile failed with exit ${compile.status || 1}`);
  }

  const fixture = JSON.parse(readFileSync(fixturePath, 'utf8'));
  const assertions = require(join(outDir, 'test/videoReframe.assert.js'));
  assertions.runVideoReframeAssertions(fixture);
} finally {
  rmSync(outDir, {recursive: true, force: true});
}
