const {mkdtempSync, rmSync, writeFileSync} = require('fs');
const {tmpdir} = require('os');
const {join} = require('path');
const {spawnSync} = require('child_process');
const outDir = mkdtempSync(join(tmpdir(), 'atelier-fountain-model-'));
try {
  writeFileSync(join(outDir, 'package.json'), '{"type":"commonjs"}\n');
  const compile = spawnSync(process.execPath, [require.resolve('typescript/bin/tsc'), '--target', 'ES2020',
    '--module', 'CommonJS', '--moduleResolution', 'Node', '--strict', '--skipLibCheck', '--outDir', outDir,
    '--rootDir', '.', 'src/editor/fountainModel.ts', 'src/editor/markdownModel.ts', 'test/fountainModel.assert.ts'], {stdio: 'inherit'});
  if (compile.status !== 0) throw new Error(`Fountain model compilation failed: ${compile.status}`);
  const run = spawnSync(process.execPath, [join(outDir, 'test/fountainModel.assert.js')], {stdio: 'inherit'});
  if (run.status !== 0) throw new Error(`Fountain model assertions failed: ${run.status}`);
} finally {rmSync(outDir, {recursive: true, force: true});}
