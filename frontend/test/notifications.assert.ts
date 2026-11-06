import {
  MAX_TOASTS,
  TOAST_INFO_MS,
  TOAST_SUCCESS_MS,
  createNotificationStore,
  createNotifiers,
  shortenHomePath,
  splitSavedPath,
} from '../src/notifications';

function assert(value: boolean, message: string): void {
  if (!value) {
    throw new Error(message);
  }
}

// A manual clock: the store's timers land in a queue that advance() fires in
// order, so auto-dismiss timing is deterministic.
function createTestClock() {
  const pending: {at: number; run: () => void; handle: number}[] = [];
  let now = 0;
  let nextHandle = 1;
  function clearTimer(handle: number): void {
    const index = pending.findIndex((entry) => entry.handle === handle);
    if (index >= 0) {
      pending.splice(index, 1);
    }
  }
  return {
    setTimer(ms: number, run: () => void): number {
      const handle = nextHandle;
      nextHandle += 1;
      pending.push({at: now + ms, run, handle});
      return handle;
    },
    clearTimer,
    advance(ms: number): void {
      now += ms;
      for (;;) {
        const due = pending.find((entry) => entry.at <= now);
        if (!due) {
          return;
        }
        clearTimer(due.handle);
        due.run();
      }
    },
    pending(): number {
      return pending.length;
    },
  };
}

function setup() {
  const clock = createTestClock();
  const store = createNotificationStore(clock);
  return {clock, store, ...createNotifiers(store)};
}

// Auto-dismiss delays per kind.
{
  const {clock, store, notifySuccess, notifyInfo} = setup();
  notifySuccess('Saved clip');
  notifyInfo('Heads up');
  assert(TOAST_SUCCESS_MS === 6000 && TOAST_INFO_MS === 6000, 'success and info share the 6s delay');
  clock.advance(TOAST_SUCCESS_MS - 1);
  assert(store.getSnapshot().length === 2, 'nothing dismisses before the delay');
  clock.advance(1);
  assert(store.getSnapshot().length === 0, 'success and info dismiss at the delay');
}

{
  const {clock, store, notifyError} = setup();
  notifyError('Could not rename library', 'no such library');
  clock.advance(10 * 60 * 1000);
  assert(store.getSnapshot().length === 1, 'errors never auto-dismiss');
  store.dismiss(store.getSnapshot()[0].id);
  assert(store.getSnapshot().length === 0, 'errors dismiss by hand');
}

// Progress waits for its job, then settles in place under the same key.
{
  const {clock, store, notifySuccess} = setup();
  store.notify({kind: 'progress', title: 'Exporting library', key: 'archive', progress: {done: 1, total: 4}});
  store.notify({kind: 'progress', title: 'Exporting library', key: 'archive', progress: {done: 2, total: 4}});
  clock.advance(10 * 60 * 1000);
  assert(store.getSnapshot().length === 1, 'progress has no timer');
  assert(store.getSnapshot()[0].count === 1, 'progress ticks are not repeats');
  assert(store.getSnapshot()[0].progress?.done === 2, 'progress ticks update in place');
  notifySuccess('Exported library', {key: 'archive'});
  assert(store.getSnapshot().length === 1, 'completion settles the same toast');
  assert(store.getSnapshot()[0].progress === undefined, 'the settled toast drops its progress bar');
  clock.advance(TOAST_SUCCESS_MS);
  assert(store.getSnapshot().length === 0, 'the settled toast then auto-dismisses');
}

// Key updates: repeats count, changed content resets, activity re-fronts.
{
  const {store, notifyError} = setup();
  notifyError('Could not save settings', 'disk full', {key: 'config-save'});
  notifyError('Could not save settings', 'disk full', {key: 'config-save'});
  assert(store.getSnapshot().length === 1, 'one toast per key');
  assert(store.getSnapshot()[0].count === 2, 'an identical repeat bumps the count');
  notifyError('Could not save settings', 'permission denied', {key: 'config-save'});
  assert(store.getSnapshot()[0].message === 'permission denied', 'the content updates');
  assert(store.getSnapshot()[0].count === 1, 'changed content resets the count');
  store.notify({kind: 'info', title: 'other'});
  notifyError('Could not save settings', 'permission denied', {key: 'config-save'});
  assert(store.getSnapshot()[0].key === 'config-save', 'a re-raised toast moves to the front');
  store.dismissKey('config-save');
  assert(store.getSnapshot().length === 1 && store.getSnapshot()[0].title === 'other', 'dismissKey removes only that toast');
}

// The cap.
{
  const {store, notifySuccess} = setup();
  for (let index = 1; index <= MAX_TOASTS + 1; index += 1) {
    notifySuccess(`done ${index}`);
  }
  const titles = store.getSnapshot().map((toast) => toast.title);
  assert(titles.length === MAX_TOASTS, 'the stack caps');
  assert(!titles.includes('done 1'), 'the oldest drops first');
  assert(titles[0] === `done ${MAX_TOASTS + 1}`, 'the newest is first');
}

{
  const {store, notifyError, notifySuccess} = setup();
  for (let index = 1; index < MAX_TOASTS; index += 1) {
    notifyError(`err ${index}`, 'x');
  }
  store.notify({kind: 'progress', title: 'working', key: 'job'});
  notifySuccess('done');
  const kinds = store.getSnapshot().map((toast) => toast.kind);
  assert(kinds.length === MAX_TOASTS, 'still capped');
  assert(kinds.includes('progress'), 'progress is never dropped');
  assert(!kinds.includes('success'), 'success drops before any error');
}

{
  const {store, notifyError} = setup();
  for (let index = 1; index <= MAX_TOASTS + 1; index += 1) {
    notifyError(`err ${index}`, 'x');
  }
  const titles = store.getSnapshot().map((toast) => toast.title);
  assert(titles.length === MAX_TOASTS, 'errors cap too');
  assert(!titles.includes('err 1'), 'the oldest error drops when no success/info is left');
}

// Pause and resume.
{
  const {clock, store, notifySuccess} = setup();
  notifySuccess('hovered');
  store.pause();
  assert(clock.pending() === 0, 'pausing clears every timer');
  clock.advance(10 * 60 * 1000);
  assert(store.getSnapshot().length === 1, 'a paused toast stays');
  notifySuccess('arrived while paused');
  assert(clock.pending() === 0, 'a toast raised while paused gets no timer');
  store.resume();
  assert(clock.pending() === 2, 'resuming restarts every timer');
  clock.advance(TOAST_SUCCESS_MS - 1);
  assert(store.getSnapshot().length === 2, 'resume grants a fresh full delay');
  clock.advance(1);
  assert(store.getSnapshot().length === 0, 'then they dismiss');
}

{
  const {clock, store, notifyError} = setup();
  notifyError('sticky', 'x');
  store.pause();
  store.resume();
  assert(clock.pending() === 0, 'errors get no timer across pause and resume');
}

// Subscribe, snapshot stability, and clear.
{
  const {clock, store, notifyInfo} = setup();
  let changes = 0;
  const unsubscribe = store.subscribe(() => {
    changes += 1;
  });
  const empty = store.getSnapshot();
  assert(store.getSnapshot() === empty, 'the snapshot is stable while nothing changes');
  notifyInfo('a');
  assert(store.getSnapshot() !== empty, 'a change yields a new snapshot');
  store.dismiss(store.getSnapshot()[0].id);
  unsubscribe();
  notifyInfo('b');
  assert(changes === 2, 'the listener saw notify and dismiss, then unsubscribed');
  store.clear();
  assert(store.getSnapshot().length === 0 && clock.pending() === 0, 'clear empties the stack and its timers');
}

// Path display.
{
  assert(shortenHomePath('/Users/ana/Downloads') === '~/Downloads', 'macOS homes shorten to ~');
  assert(shortenHomePath('/Users/ana') === '~', 'the home itself is ~');
  assert(shortenHomePath('/home/ana/Videos') === '~/Videos', 'Linux homes shorten to ~');
  assert(shortenHomePath('/Users/Shared/x') === '/Users/Shared/x', '/Users/Shared is not a home');
  assert(shortenHomePath('/Volumes/exports') === '/Volumes/exports', 'other paths stay whole');
  const saved = splitSavedPath('/Users/ana/Downloads/poster.png');
  assert(saved.name === 'poster.png' && saved.folder === '~/Downloads', 'a saved path splits into name and folder');
  assert(splitSavedPath('/poster.png').folder === '/', 'a root file keeps the root folder');
}

// trackSave and notifyError are async-free except trackSave, which runs last.
async function trackSaveAssertions(): Promise<void> {
  {
    const {clock, store, trackSave} = setup();
    const path = await trackSave('image', Promise.resolve('/Users/ana/Downloads/poster.png'));
    assert(path === '/Users/ana/Downloads/poster.png', 'trackSave resolves the path');
    const [toast] = store.getSnapshot();
    assert(store.getSnapshot().length === 1 && toast.kind === 'success', 'a save shows one success toast');
    assert(toast.title === 'Saved image', 'the title names what was saved');
    assert(toast.message === 'poster.png', 'the message names the file');
    assert(toast.detail === 'in ~/Downloads', 'the detail names the folder');
    clock.advance(TOAST_SUCCESS_MS);
    assert(store.getSnapshot().length === 0, 'the success toast auto-dismisses');
  }
  {
    const {store, trackSave} = setup();
    const path = await trackSave('video', Promise.resolve(''));
    assert(path === null, 'a cancelled dialog resolves null');
    assert(store.getSnapshot().length === 0, 'a cancel shows nothing');
  }
  {
    const {clock, store, trackSave} = setup();
    const path = await trackSave('audio', Promise.reject(new Error('disk full')));
    assert(path === null, 'a failure resolves null');
    const [toast] = store.getSnapshot();
    assert(store.getSnapshot().length === 1 && toast.kind === 'error', 'a failure shows one error toast');
    assert(toast.title === "Couldn't save audio", 'the error names the action');
    assert(toast.message === 'disk full', 'the error carries the cause');
    clock.advance(10 * 60 * 1000);
    assert(store.getSnapshot().length === 1, 'the error stays up');
  }
  {
    const {store, trackSave} = setup();
    await trackSave('transcript', Promise.reject('artifact is not a transcript'));
    assert(store.getSnapshot()[0].message === 'artifact is not a transcript', 'string rejections pass through');
  }
}

trackSaveAssertions().then(() => {
  console.log('notifications assertions passed');
});
