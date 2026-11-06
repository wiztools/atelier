// The notification store behind the app's toast stack. Pure TypeScript on
// purpose (no React, no wailsjs imports): the .cjs runner compiles it alone,
// so every timing rule here is table-testable (test:notifications). The React
// side lives in ToastStack.tsx; callers import the shared helpers at the
// bottom (notifySuccess/notifyInfo/notifyError/trackSave).
//
// Where feedback goes (AGENTS.md "Conventions"): a toast reports the RESULT
// of an action — every failure, and a success whose result lands somewhere
// other than the control the user clicked (a file on disk, an artifact in a
// conversation, a finished background job). State stays inline; decisions
// keep their own cards and dialogs.

// Auto-dismiss delays. Errors never auto-dismiss (a failure the user must act
// on can't vanish unread) and progress waits for its job; successes and info
// follow the old library notice's 6s.
export const TOAST_SUCCESS_MS = 6000;
export const TOAST_INFO_MS = 6000;

// The stack shows at most this many; overflow drops the oldest success/info,
// then the oldest error. Progress is never dropped — its job reports into it.
export const MAX_TOASTS = 5;

export type ToastKind = 'success' | 'error' | 'info' | 'progress';

export interface ToastProgress {
  done: number;
  total: number;
}

export interface ToastAction {
  label: string;
  run: () => void;
}

export interface ToastInput {
  kind: ToastKind;
  title: string;
  message?: string;
  // detail is secondary text, e.g. the folder a file was saved in.
  detail?: string;
  progress?: ToastProgress;
  action?: ToastAction;
  // key updates an existing toast in place (progress ticks, a repeated
  // failure); without one every notify adds a new toast.
  key?: string;
}

export interface Toast extends ToastInput {
  id: string;
  // count is how many times the same content was raised under one key ("×3").
  count: number;
}

export interface ToastClock {
  // Injected so tests drive time by hand; the app uses the globals.
  setTimer: (ms: number, run: () => void) => number;
  clearTimer: (handle: number) => void;
}

export interface NotificationStore {
  notify: (input: ToastInput) => Toast;
  dismiss: (id: string) => void;
  dismissKey: (key: string) => void;
  // pause/resume stop and restart every auto-dismiss timer (the stack pauses
  // while hovered); resume grants each toast a fresh full delay.
  pause: () => void;
  resume: () => void;
  clear: () => void;
  subscribe: (listener: () => void) => () => void;
  // getSnapshot returns the same array until something changes — the
  // contract useSyncExternalStore needs. Newest first.
  getSnapshot: () => Toast[];
}

const defaultClock: ToastClock = {
  setTimer: (ms, run) => setTimeout(run, ms),
  clearTimer: (handle) => clearTimeout(handle),
};

function autoDismissMs(kind: ToastKind): number | null {
  switch (kind) {
    case 'success':
      return TOAST_SUCCESS_MS;
    case 'info':
      return TOAST_INFO_MS;
    default:
      return null;
  }
}

function contentOf(input: ToastInput): Omit<ToastInput, 'key'> {
  return {
    kind: input.kind,
    title: input.title,
    message: input.message,
    detail: input.detail,
    progress: input.progress,
    action: input.action,
  };
}

// isRepeat: the same content raised again under one key. Progress ticks are
// never repeats — they are the job moving, not the same news twice.
function isRepeat(existing: Toast, input: ToastInput): boolean {
  return input.kind !== 'progress'
    && existing.kind === input.kind
    && existing.title === input.title
    && existing.message === input.message
    && existing.detail === input.detail;
}

export function createNotificationStore(clock: ToastClock = defaultClock): NotificationStore {
  let toasts: Toast[] = [];
  const timers = new Map<string, number>();
  const listeners = new Set<() => void>();
  let nextID = 1;
  let paused = false;

  function emit(): void {
    listeners.forEach((listener) => listener());
  }

  function stopTimer(id: string): void {
    const handle = timers.get(id);
    if (handle !== undefined) {
      clock.clearTimer(handle);
      timers.delete(id);
    }
  }

  function startTimer(toast: Toast): void {
    stopTimer(toast.id);
    const ms = autoDismissMs(toast.kind);
    if (ms === null || paused) {
      return;
    }
    timers.set(toast.id, clock.setTimer(ms, () => dismiss(toast.id)));
  }

  function dismiss(id: string): void {
    if (!toasts.some((toast) => toast.id === id)) {
      return;
    }
    stopTimer(id);
    toasts = toasts.filter((toast) => toast.id !== id);
    emit();
  }

  function dismissKey(key: string): void {
    const toast = toasts.find((candidate) => candidate.key === key);
    if (toast) {
      dismiss(toast.id);
    }
  }

  function evictionIndex(): number {
    for (const kinds of [['success', 'info'], ['error']]) {
      for (let index = toasts.length - 1; index >= 0; index -= 1) {
        if (kinds.includes(toasts[index].kind)) {
          return index;
        }
      }
    }
    return -1;
  }

  function notify(input: ToastInput): Toast {
    const existing = input.key === undefined ? undefined : toasts.find((toast) => toast.key === input.key);
    const toast: Toast = existing
      ? {...existing, ...contentOf(input), count: isRepeat(existing, input) ? existing.count + 1 : 1}
      : {...contentOf(input), id: `toast-${nextID}`, key: input.key, count: 1};
    if (!existing) {
      nextID += 1;
    }
    // Newest activity moves to the front (nearest the stack's corner).
    toasts = [toast, ...toasts.filter((candidate) => candidate.id !== toast.id)];
    while (toasts.length > MAX_TOASTS) {
      const index = evictionIndex();
      if (index === -1) {
        break;
      }
      stopTimer(toasts[index].id);
      toasts = toasts.filter((_, candidate) => candidate !== index);
    }
    if (toasts.includes(toast)) {
      startTimer(toast);
    }
    emit();
    return toast;
  }

  function pause(): void {
    if (paused) {
      return;
    }
    paused = true;
    timers.forEach((handle) => clock.clearTimer(handle));
    timers.clear();
  }

  function resume(): void {
    if (!paused) {
      return;
    }
    paused = false;
    toasts.forEach(startTimer);
  }

  function clear(): void {
    timers.forEach((handle) => clock.clearTimer(handle));
    timers.clear();
    if (toasts.length > 0) {
      toasts = [];
      emit();
    }
  }

  return {
    notify,
    dismiss,
    dismissKey,
    pause,
    resume,
    clear,
    subscribe(listener) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    getSnapshot() {
      return toasts;
    },
  };
}

// shortenHomePath renders a home-relative path in ~ form (macOS
// /Users/<name>, Linux /home/<name>; /Users/Shared is not a home).
export function shortenHomePath(path: string): string {
  const match = /^\/(?:Users\/(?!Shared(?:\/|$))[^/]+|home\/[^/]+)/.exec(path);
  return match ? `~${path.slice(match[0].length)}` : path;
}

// splitSavedPath separates a saved file's name from the folder it landed in,
// the folder shortened for display.
export function splitSavedPath(path: string): {name: string; folder: string} {
  const slash = path.lastIndexOf('/');
  if (slash === -1) {
    return {name: path, folder: ''};
  }
  return {
    name: path.slice(slash + 1),
    folder: slash === 0 ? '/' : shortenHomePath(path.slice(0, slash)),
  };
}

export function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

type ToastExtras = Omit<ToastInput, 'kind' | 'title'>;

// SavedFileReveal is the platform's "show this file" action for success
// toasts that name a file on disk ("Show in Finder" on macOS, opening the
// folder elsewhere). App configures it at startup through the RevealFile
// binding, which keeps this module free of wailsjs imports.
export interface SavedFileReveal {
  label: string;
  reveal: (path: string) => void;
}

export interface Notifiers {
  notifySuccess: (title: string, extras?: ToastExtras) => Toast;
  notifyInfo: (title: string, extras?: ToastExtras) => Toast;
  // notifyError formats the thrown value itself (string or Error).
  notifyError: (title: string, error: unknown, extras?: Omit<ToastExtras, 'message'>) => Toast;
  // trackSave owns one save-dialog binding's whole outcome: a returned path
  // is a success toast naming the file and folder (with the reveal action
  // when configured), the empty path a cancelled dialog (silent), a throw an
  // error toast. Resolves the path, or null when nothing was saved, for
  // callers that log the result (comic's Edits row).
  trackSave: (what: string, save: Promise<string>) => Promise<string | null>;
  // setSavedFileReveal installs (or, with null, removes) the reveal action.
  setSavedFileReveal: (reveal: SavedFileReveal | null) => void;
  // savedFileAction is the reveal action for one saved path, or undefined
  // while none is configured — for success toasts trackSave doesn't raise
  // (the library export).
  savedFileAction: (path: string) => ToastAction | undefined;
}

export function createNotifiers(store: NotificationStore): Notifiers {
  const notifySuccess = (title: string, extras: ToastExtras = {}) => store.notify({...extras, kind: 'success', title});
  const notifyInfo = (title: string, extras: ToastExtras = {}) => store.notify({...extras, kind: 'info', title});
  const notifyError = (title: string, error: unknown, extras: Omit<ToastExtras, 'message'> = {}) =>
    store.notify({...extras, kind: 'error', title, message: errorMessage(error)});

  let savedFileReveal: SavedFileReveal | null = null;
  function setSavedFileReveal(reveal: SavedFileReveal | null): void {
    savedFileReveal = reveal;
  }
  function savedFileAction(path: string): ToastAction | undefined {
    const reveal = savedFileReveal;
    return reveal ? {label: reveal.label, run: () => reveal.reveal(path)} : undefined;
  }

  async function trackSave(what: string, save: Promise<string>): Promise<string | null> {
    let path: string;
    try {
      path = await save;
    } catch (error) {
      notifyError(`Couldn't save ${what}`, error);
      return null;
    }
    if (!path) {
      return null;
    }
    const {name, folder} = splitSavedPath(path);
    notifySuccess(`Saved ${what}`, {message: name, detail: folder ? `in ${folder}` : undefined, action: savedFileAction(path)});
    return path;
  }

  return {notifySuccess, notifyInfo, notifyError, trackSave, setSavedFileReveal, savedFileAction};
}

// The app's shared store and helpers; tests build their own pair.
export const notifications = createNotificationStore();
export const {notifySuccess, notifyInfo, notifyError, trackSave, setSavedFileReveal, savedFileAction} = createNotifiers(notifications);
