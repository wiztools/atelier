const {mkdtempSync, rmSync, writeFileSync} = require('fs');
const {tmpdir} = require('os');
const {join} = require('path');
const {spawnSync} = require('child_process');
const outDir = mkdtempSync(join(tmpdir(), 'atelier-video-trim-'));
try {
  writeFileSync(join(outDir, 'package.json'), '{"type":"commonjs"}\n');
  const compile = spawnSync(process.execPath, [require.resolve('typescript/bin/tsc'), '--target', 'ES2020',
    '--module', 'CommonJS', '--moduleResolution', 'Node', '--strict', '--skipLibCheck', '--outDir', outDir,
    '--rootDir', '.', 'src/editor/videoReframe.ts', 'src/editor/videoTrimModel.ts', 'test/videoTrim.assert.ts'], {stdio: 'inherit'});
  if (compile.status !== 0) throw new Error(`Trim compilation failed: ${compile.status}`);
  const run = spawnSync(process.execPath, [join(outDir, 'test/videoTrim.assert.js')], {stdio: 'inherit'});
  if (run.status !== 0) throw new Error(`Trim assertions failed: ${run.status}`);
} finally {rmSync(outDir, {recursive: true, force: true});}
