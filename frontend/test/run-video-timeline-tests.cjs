const {mkdtempSync, rmSync, writeFileSync} = require('fs');
const {tmpdir} = require('os');
const {join} = require('path');
const {spawnSync} = require('child_process');
const outDir = mkdtempSync(join(tmpdir(), 'atelier-video-timeline-'));
try {
  writeFileSync(join(outDir, 'package.json'), '{"type":"commonjs"}\n');
  const compile = spawnSync(process.execPath, [require.resolve('typescript/bin/tsc'), '--target', 'ES2020',
    '--module', 'CommonJS', '--moduleResolution', 'Node', '--strict', '--skipLibCheck', '--outDir', outDir,
    '--rootDir', '.', 'src/editor/videoReframe.ts', 'src/editor/videoTimelineModel.ts', 'src/editor/videoEditState.ts', 'test/videoTimeline.assert.ts'], {stdio: 'inherit'});
  if (compile.status !== 0) throw new Error(`Timeline compilation failed: ${compile.status}`);
  const run = spawnSync(process.execPath, [join(outDir, 'test/videoTimeline.assert.js')], {stdio: 'inherit'});
  if (run.status !== 0) throw new Error(`Timeline assertions failed: ${run.status}`);
} finally {rmSync(outDir, {recursive: true, force: true});}
