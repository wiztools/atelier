// Runtime smoke test for the Atelier frontend, with no Go backend.
//
//   npm run smoke                          run the session against the working tree
//   npm run smoke -- --baseline <git-ref>  also run it against a clean worktree of
//                                          <git-ref> and diff the two step by step
//   npm run smoke -- --keep                keep the per-step DOM snapshots and the
//                                          report (the path is printed)
//
// It serves the frontend through vite with test/smoke/stubs.js injected ahead of
// the app (fake Wails bindings and runtime over a fixed fake workspace — no
// config read, no keychain, no network), drives headless Chrome over the
// DevTools protocol through SCENARIO, and records each step's DOM, the console
// errors and uncaught exceptions, and the ordered backend binding calls.
//
// A run fails when a step's action can't find its target or anything logs an
// error (404s for favicon.ico and /atelier-artifact/ files are expected without
// a backend). With --baseline it also fails when any step's DOM or binding
// calls differ from the baseline's — the check for a behavior-neutral change. A
// deliberate UI change shows up as a diff to read, not a failure to suppress.
//
// Needs Node 22+ (global WebSocket) and Chrome — any Chromium binary through
// CHROME_PATH. Everything else is already in node_modules. A run takes about a
// minute; --baseline runs the session twice.
import {execFileSync, spawn} from 'node:child_process';
import {existsSync, mkdtempSync, readFileSync, rmSync, symlinkSync, unlinkSync, writeFileSync} from 'node:fs';
import {createServer as createNetServer} from 'node:net';
import {tmpdir} from 'node:os';
import {dirname, join} from 'node:path';
import {fileURLToPath} from 'node:url';
import {createServer as createViteServer} from 'vite';
import react from '@vitejs/plugin-react';

const here = dirname(fileURLToPath(import.meta.url));
const frontendRoot = join(here, '..', '..');
const repoRoot = join(frontendRoot, '..');

const args = process.argv.slice(2);
const keep = args.includes('--keep');
const baselineIndex = args.indexOf('--baseline');
const baselineRef = baselineIndex === -1 ? null : args[baselineIndex + 1];
if (baselineIndex !== -1 && (!baselineRef || baselineRef.startsWith('--'))) {
  usageError('--baseline needs a git ref (a commit, branch, or tag)');
}
if (typeof WebSocket === 'undefined') {
  usageError(`npm run smoke needs Node 22+ for the global WebSocket (this is ${process.version})`);
}
const CHROME = process.env.CHROME_PATH || [
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/Applications/Chromium.app/Contents/MacOS/Chromium',
  '/usr/bin/google-chrome',
  '/usr/bin/chromium',
  '/usr/bin/chromium-browser',
].find((path) => existsSync(path));
if (!CHROME) {
  usageError('no Chrome found; set CHROME_PATH to a Chrome or Chromium binary');
}

function usageError(message) {
  console.error(`smoke: ${message}`);
  process.exit(2);
}

// The session. Each step runs one in-page expression (it may return a promise)
// and then waits for the app to settle. The expression's value is the step's
// action result: false or undefined means the target wasn't found.
const SCENARIO = [
  ['initial', 'true', 0],
  ['conversation', `__smoke.click('.history-open', 'Storyboard ideas')`, 2000],
  ['mention-open', `__smoke.type('textarea', 'Look at @img')`, 600],
  ['mention-closed', `__smoke.key('textarea', 'Escape')`, 400],
  ['stream-start', `__smoke.type('textarea', 'Hello there') && __smoke.key('textarea', 'Enter')`, 1200],
  ['stream-mid', `(() => {
    const s = window.__atelier.lastStream;
    if (!s) return false;
    const base = {requestID: s.requestID, conversationId: s.conversationId || 'conv_alpha'};
    window.__atelier.emit('chat:chunk', {...base, content: 'Hi! ', done: false});
    window.__atelier.emit('chat:chunk', {...base, content: 'How can I help?', done: false});
    window.__atelier.emit('atelier:harness-run', {requestId: s.requestID, conversationId: base.conversationId, run: {status: 'running', steps: [
      {kind: 'triage', status: 'completed', provider: 'ollama', model: 'qwen3:8b', durationMs: 400},
      {kind: 'tool_call', status: 'running', summary: 'running run_command'},
    ]}});
    window.__atelier.emit('atelier:tool-permission', {id: 'perm_1', requestID: s.requestID, conversationId: base.conversationId, toolName: 'run_command', action: 'execute', summary: 'List the workspace', command: ['ls', '-la'], cwd: '/Users/ana/work'});
    return true;
  })()`, 800],
  ['permission-allow', `__smoke.click('.tool-permission-actions button.primary')`, 800],
  ['stream-done', `(() => {
    const s = window.__atelier.lastStream;
    if (!s) return false;
    return window.__atelier.emit('chat:chunk', {requestID: s.requestID, conversationId: s.conversationId || 'conv_alpha', done: true, model: 'qwen3:8b', provider: 'ollama'}) > 0;
  })()`, 2000],
  ['editor-open', `__smoke.click('button[title="Open in the markdown editor"]')`, 1500],
  ['editor-close', `__smoke.click('button[aria-label="Close editor"]')`, 1000],
  ['search', `__smoke.click('[aria-label="Search chats"]') && new Promise((resolve) => setTimeout(() => resolve(__smoke.type('[aria-label="Search conversation history"]', 'poster')), 200))`, 1200],
  ['settings-providers', `__smoke.click('.settings-button')`, 1500],
  ['settings-models', `__smoke.click('button[aria-selected]', 'Models')`, 1200],
  ['settings-tools', `__smoke.click('button[aria-selected]', 'Tools')`, 1000],
  ['settings-general', `__smoke.click('button[aria-selected]', 'General')`, 1000],
];

// Installed on every document (it survives vite's dependency-optimization
// reload). type() goes through the native value setter so React sees a real
// input event.
const IN_PAGE_HELPERS = `window.__smoke = {
  find(selector, text) {
    return [...document.querySelectorAll(selector)].find((el) => text === undefined || el.textContent.trim() === text || el.textContent.includes(text)) || null;
  },
  click(selector, text) { const el = this.find(selector, text); if (!el) return false; el.click(); return true; },
  type(selector, value) {
    const el = this.find(selector);
    if (!el) return false;
    const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    el.focus();
    Object.getOwnPropertyDescriptor(proto, 'value').set.call(el, value);
    el.setSelectionRange(value.length, value.length);
    el.dispatchEvent(new Event('input', {bubbles: true}));
    return true;
  },
  key(selector, key) {
    const el = this.find(selector);
    if (!el) return false;
    el.dispatchEvent(new KeyboardEvent('keydown', {key, bubbles: true, cancelable: true}));
    return true;
  },
};`;

// Everything the runner creates is registered here and torn down in reverse —
// on success, on failure, and on Ctrl-C — so a run leaves no Chrome, no temp
// directories, and no git worktree behind. A teardown may be async (stopping
// Chrome, closing vite); each finishes before the next starts, so a process is
// gone before the directory it writes to is removed.
const cleanups = [];
async function cleanupAll() {
  while (cleanups.length) {
    try {
      await cleanups.pop()();
    } catch {
      // best effort: one failed teardown must not skip the rest
    }
  }
}
for (const signal of ['SIGINT', 'SIGTERM']) {
  process.on(signal, () => {
    void cleanupAll().finally(() => process.exit(130));
  });
}
function tempDir(prefix) {
  const dir = mkdtempSync(join(tmpdir(), prefix));
  if (!keep || !prefix.startsWith('atelier-smoke-out')) {
    cleanups.push(() => rmSync(dir, {recursive: true, force: true, maxRetries: 5, retryDelay: 200}));
  }
  return dir;
}

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const freePort = () => new Promise((resolve, reject) => {
  const server = createNetServer();
  server.on('error', reject);
  server.listen(0, '127.0.0.1', () => {
    const {port} = server.address();
    server.close(() => resolve(port));
  });
});

// Missing artifact files and the favicon are expected without a backend; any
// other console error or exception fails the run.
const isExpectedNoise = (event) => event.kind === 'log.error'
  && /status of 404/.test(event.text)
  && /\/favicon\.ico|\/atelier-artifact\//.test(event.text);

// Message ids embed a fresh timestamp per request, so they differ between any
// two runs; they are normalized before comparing.
const normalize = (html) => html
  .replace(/msg-assistant-chat-\d+-\w+/g, 'msg-assistant-chat-ID')
  .replace(/msg-user-\d+/g, 'msg-user-ID');

async function runSession(label, root, outDir) {
  const port = await freePort();
  const server = await createViteServer({
    root,
    configFile: false,
    logLevel: 'error',
    cacheDir: tempDir('atelier-smoke-vite-'),
    plugins: [react(), {
      name: 'atelier-smoke-stubs',
      transformIndexHtml: () => [{tag: 'script', children: readFileSync(join(here, 'stubs.js'), 'utf8'), injectTo: 'head-prepend'}],
    }],
    // fs.strict off: a baseline worktree's node_modules is a symlink outside its root.
    server: {port, strictPort: true, host: '127.0.0.1', fs: {strict: false}},
  });
  await server.listen();
  cleanups.push(() => void server.close());

  const chromePort = await freePort();
  const chrome = spawn(CHROME, [
    '--headless=new', `--remote-debugging-port=${chromePort}`, `--user-data-dir=${tempDir('atelier-smoke-chrome-')}`,
    '--no-first-run', '--no-default-browser-check', '--disable-extensions', '--window-size=1440,900',
    ...(process.platform === 'linux' ? ['--no-sandbox'] : []),
    'about:blank',
  ], {stdio: 'ignore'});
  // chrome.kill() sends SIGTERM and returns immediately; Chrome keeps writing
  // its profile while it shuts down, so its teardown waits for the process to
  // exit (SIGKILL after 5s) before the profile's own cleanup runs.
  cleanups.push(() => new Promise((resolve) => {
    if (chrome.exitCode !== null || chrome.signalCode !== null) {
      resolve();
      return;
    }
    const force = setTimeout(() => chrome.kill('SIGKILL'), 5000);
    chrome.once('exit', () => {
      clearTimeout(force);
      resolve();
    });
    chrome.kill();
  }));

  let target = null;
  for (let attempt = 0; attempt < 75 && !target; attempt++) {
    await sleep(200);
    try {
      const pages = await (await fetch(`http://127.0.0.1:${chromePort}/json/list`)).json();
      target = pages.find((page) => page.type === 'page') ?? null;
    } catch {
      // Chrome is still starting
    }
  }
  if (!target) {
    throw new Error(`${label}: Chrome did not open a DevTools endpoint`);
  }

  const ws = new WebSocket(target.webSocketDebuggerUrl);
  await new Promise((resolve, reject) => {
    ws.addEventListener('open', resolve, {once: true});
    ws.addEventListener('error', () => reject(new Error(`${label}: DevTools socket failed`)), {once: true});
  });
  cleanups.push(() => ws.close());
  let nextID = 0;
  const pending = new Map();
  const events = [];
  let step = 'load';
  ws.addEventListener('message', (message) => {
    const data = JSON.parse(message.data);
    if (data.id !== undefined) {
      pending.get(data.id)?.(data);
      pending.delete(data.id);
      return;
    }
    if (data.method === 'Runtime.exceptionThrown') {
      const details = data.params.exceptionDetails;
      events.push({step, kind: 'exception', text: details.exception?.description || details.text});
    } else if (data.method === 'Runtime.consoleAPICalled' && ['error', 'assert'].includes(data.params.type)) {
      events.push({step, kind: `console.${data.params.type}`, text: data.params.args.map((arg) => arg.value ?? arg.description ?? '').join(' ')});
    } else if (data.method === 'Log.entryAdded' && data.params.entry.level === 'error') {
      events.push({step, kind: 'log.error', text: `${data.params.entry.text} ${data.params.entry.url || ''}`.trim()});
    }
  });
  const send = (method, params = {}) => new Promise((resolve) => {
    const id = ++nextID;
    pending.set(id, resolve);
    ws.send(JSON.stringify({id, method, params}));
  });
  const evaluate = async (expression) => {
    const reply = await send('Runtime.evaluate', {expression, awaitPromise: true, returnByValue: true});
    if (reply.result?.exceptionDetails) {
      events.push({step, kind: 'exception', text: reply.result.exceptionDetails.exception?.description || 'evaluate failed'});
      return undefined;
    }
    return reply.result?.result?.value;
  };
  const appMounted = async () => {
    for (let attempt = 0; attempt < 100; attempt++) {
      if (await evaluate(`!!document.querySelector('.settings-button')`)) {
        return true;
      }
      await sleep(200);
    }
    return false;
  };

  await send('Runtime.enable');
  await send('Log.enable');
  await send('Page.enable');
  // A cold vite cache optimizes dependencies on first load and then reloads
  // the page; warm it up once, then load again for the measured session.
  await send('Page.navigate', {url: `http://127.0.0.1:${port}/`});
  await appMounted();
  await sleep(3000);
  events.length = 0;
  await send('Page.addScriptToEvaluateOnNewDocument', {source: IN_PAGE_HELPERS});
  await send('Page.navigate', {url: `http://127.0.0.1:${port}/`});
  if (!(await appMounted())) {
    events.push({step, kind: 'harness', text: 'the app never mounted'});
  }
  await sleep(2500);

  const report = {steps: {}, events};
  for (const [name, expression, settleMs] of SCENARIO) {
    step = name;
    const result = await evaluate(expression);
    await sleep(settleMs);
    const html = await evaluate(`document.getElementById('root')?.innerHTML ?? ''`);
    writeFileSync(join(outDir, `${name}.html`), `${String(html).replace(/></g, '>\n<')}\n`);
    report.steps[name] = {actionOk: Boolean(result), calls: (await evaluate('window.__atelier.calls.splice(0)')) ?? []};
  }
  writeFileSync(join(outDir, 'report.json'), `${JSON.stringify(report, null, 2)}\n`);
  return report;
}

// Line diff over two snapshots (LCS; a snapshot is a few hundred lines).
function lineDiff(a, b) {
  const n = a.length;
  const m = b.length;
  if (n * m > 25_000_000) {
    return [`(snapshots too large to diff: ${n} vs ${m} lines)`];
  }
  const lcs = Array.from({length: n + 1}, () => new Uint32Array(m + 1));
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      lcs[i][j] = a[i] === b[j] ? lcs[i + 1][j + 1] + 1 : Math.max(lcs[i + 1][j], lcs[i][j + 1]);
    }
  }
  const out = [];
  let i = 0;
  let j = 0;
  while (i < n || j < m) {
    if (i < n && j < m && a[i] === b[j]) {
      i += 1;
      j += 1;
    } else if (j < m && (i === n || lcs[i][j + 1] >= lcs[i + 1][j])) {
      out.push(`+ ${b[j++]}`);
    } else {
      out.push(`- ${a[i++]}`);
    }
  }
  return out;
}

// Problems with one run on its own: failed actions and unexpected errors.
function runProblems(label, report) {
  const problems = [];
  for (const [name] of SCENARIO) {
    if (!report.steps[name]?.actionOk) {
      problems.push(`${label} ${name}: the step's action found nothing to act on`);
    }
  }
  for (const event of report.events.filter((e) => !isExpectedNoise(e))) {
    problems.push(`${label} ${event.step}: ${event.kind}: ${String(event.text).split('\n')[0].slice(0, 240)}`);
  }
  return problems;
}

// Steps whose binding calls may compare as a multiset instead of a sequence,
// with the reason. Order stays meaningful everywhere else: most call orders
// ARE behavior (a save before the fetch that reads it). An entry must name the
// independent reads whose relative order cannot change anything.
const CALL_ORDER_FLEX = {
  // On a turn finishing, three refreshes fire: the conversation list (a call
  // from the chunk listener), the libraries tree, and the assets panel (effect
  // callbacks, so their order is hook-call order). The libraries tree and the
  // assets listings are independent read-only fetches; useAssets' effects
  // register after useLibraries' since its library scope derives from
  // useLibraries' libraryIDForProject, so the two listings may swap relative
  // to main. Which responds first changes nothing either renders from.
  'stream-done': 'independent read-only refreshes (assets panel vs libraries tree)',
};

function compareRuns(baseDir, base, headDir, head) {
  const problems = [];
  let identical = 0;
  for (const [name] of SCENARIO) {
    const baseCalls = base.steps[name].calls;
    const headCalls = head.steps[name].calls;
    const callsSame = JSON.stringify(baseCalls) === JSON.stringify(headCalls)
      || (name in CALL_ORDER_FLEX && JSON.stringify([...baseCalls].sort()) === JSON.stringify([...headCalls].sort()));
    if (!callsSame) {
      problems.push(`${name}: backend calls differ\n    baseline: ${baseCalls.join(', ') || '(none)'}\n    head:     ${headCalls.join(', ') || '(none)'}`);
    }
    const diff = lineDiff(
      normalize(readFileSync(join(baseDir, `${name}.html`), 'utf8')).split('\n'),
      normalize(readFileSync(join(headDir, `${name}.html`), 'utf8')).split('\n'),
    );
    if (diff.length) {
      const shown = diff.slice(0, 16).map((line) => `    ${line.slice(0, 220)}`);
      if (diff.length > 16) {
        shown.push(`    … ${diff.length - 16} more changed lines`);
      }
      problems.push(`${name}: DOM differs (- baseline, + head)\n${shown.join('\n')}`);
    }
    if (callsSame && !diff.length) {
      identical += 1;
    }
  }
  return {identical, problems};
}

async function main() {
  const headDir = tempDir('atelier-smoke-out-head-');
  console.log('smoke: running the session against the working tree');
  const head = await runSession('head', frontendRoot, headDir);
  const problems = runProblems('head', head);
  console.log(`smoke: head — ${SCENARIO.length} steps, actions ${SCENARIO.map(([name]) => head.steps[name].actionOk ? '+' : '-').join('')}`);

  if (baselineRef) {
    const worktree = join(tempDir('atelier-smoke-worktree-'), 'atelier');
    execFileSync('git', ['worktree', 'add', '--detach', worktree, baselineRef], {cwd: repoRoot, stdio: 'pipe'});
    cleanups.push(() => execFileSync('git', ['worktree', 'remove', '--force', worktree], {cwd: repoRoot, stdio: 'pipe'}));
    // The worktree borrows this checkout's node_modules through a symlink. It
    // is unlinked before the worktree is removed (cleanups run in reverse), so
    // the removal can never reach through it into the real install.
    const modulesLink = join(worktree, 'frontend', 'node_modules');
    symlinkSync(join(frontendRoot, 'node_modules'), modulesLink, 'dir');
    cleanups.push(() => unlinkSync(modulesLink));
    const baseDir = tempDir('atelier-smoke-out-baseline-');
    console.log(`smoke: running the session against ${baselineRef}`);
    const base = await runSession('baseline', join(worktree, 'frontend'), baseDir);
    console.log(`smoke: baseline — ${SCENARIO.length} steps, actions ${SCENARIO.map(([name]) => base.steps[name].actionOk ? '+' : '-').join('')}`);
    problems.push(...runProblems('baseline', base));
    const comparison = compareRuns(baseDir, base, headDir, head);
    problems.push(...comparison.problems);
    if (!problems.length) {
      console.log(`smoke: PASS — all ${SCENARIO.length} steps match ${baselineRef} (DOM and backend calls), no errors`);
    }
    if (keep) {
      console.log(`smoke: kept ${headDir} and ${baseDir}`);
    }
  } else {
    if (!problems.length) {
      console.log(`smoke: PASS — ${SCENARIO.length} steps, no errors`);
    }
    if (keep) {
      console.log(`smoke: kept ${headDir}`);
    }
  }

  if (problems.length) {
    console.error(`smoke: FAIL — ${problems.length} problem(s):`);
    for (const problem of problems) {
      console.error(`  ${problem}`);
    }
    process.exitCode = 1;
  }
}

try {
  await main();
} catch (error) {
  console.error(`smoke: ${error instanceof Error ? error.stack : error}`);
  process.exitCode = 2;
} finally {
  await cleanupAll();
}
// vite keeps file watchers and sockets open past server.close(); exit
// explicitly so a finished run never hangs.
process.exit(process.exitCode ?? 0);
