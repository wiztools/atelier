import {useEffect, useState} from 'react';
import {CheckForUpdates, InstallUpdate} from '../../wailsjs/go/main/App';
import {EventsOn} from '../../wailsjs/runtime/runtime';
import {notifyError, notifyInfo, notifySuccess} from '../notifications';
import {UpdateAvailableEvent} from '../types';
import {formatError} from '../utils';

// useUpdates owns the in-app updater: the available-update banner's payload,
// the install flow (busy/error and the queued install that runs when the
// conversation currently streaming finishes), the manual check's toast, and
// Installing is refused while any conversation is in flight — that flag
// arrives from useChatStream. The persisted updater section itself is
// useAppConfig's: no Settings UI edits it, so this hook never writes it.
function useUpdates({anyConversationInFlight}: {
  anyConversationInFlight: boolean;
}) {
  // Self-update UI state: the banner rides the atelier:update-available event
  // (raised by startup, ticker, and manual checks alike); an install clicked
  // mid-turn queues until the last conversation finishes.
  const [updateAvailable, setUpdateAvailable] = useState<UpdateAvailableEvent | null>(null);
  const [updateBusy, setUpdateBusy] = useState(false);
  const [updateQueued, setUpdateQueued] = useState(false);
  const [updateError, setUpdateError] = useState('');
  const [updateCheckBusy, setUpdateCheckBusy] = useState(false);

  // Update availability mirrors the tool-permission pattern: setState only, no
  // stale closures. Every check that finds a newer version (startup, daily
  // ticker, manual) re-emits, so a dismissed banner reappears on the next
  // check rather than being permanently silenced.
  useEffect(() => {
    const onUpdateAvailable = (event: UpdateAvailableEvent) => {
      if (!event?.latest) {
        return;
      }
      setUpdateAvailable(event);
      setUpdateError('');
    };
    return EventsOn('atelier:update-available', onUpdateAvailable);
  }, []);

  // Installing while a turn runs would kill the conversation mid-stream, so a
  // click during a turn queues the install; this effect fires it once the last
  // conversation finishes. The Go-side gate in InstallUpdate stays
  // authoritative — this queue is UX, not enforcement.
  useEffect(() => {
    if (!updateQueued || updateBusy || anyConversationInFlight) {
      return;
    }
    setUpdateQueued(false);
    void requestUpdateInstall();
  }, [updateQueued, updateBusy, anyConversationInFlight]);

  async function requestUpdateInstall() {
    if (anyConversationInFlight) {
      setUpdateQueued(true);
      return;
    }
    setUpdateBusy(true);
    setUpdateError('');
    try {
      await InstallUpdate();
      // Success ends the session: the backend quits, the detached helper
      // swaps the bundle, and the new version reopens. Nothing to render.
    } catch (error) {
      setUpdateError(formatError(error));
      setUpdateQueued(false);
    } finally {
      setUpdateBusy(false);
    }
  }

  function dismissUpdate() {
    setUpdateAvailable(null);
    setUpdateQueued(false);
    setUpdateError('');
  }

  // The manual check reports its outcome as a toast. A newer version needs no
  // toast of its own: the check raises atelier:update-available, so the update
  // banner appears beside the stack.
  async function checkForUpdates() {
    setUpdateCheckBusy(true);
    try {
      const outcome = await CheckForUpdates();
      if (outcome.state === 'available') {
        return;
      }
      if (outcome.state === 'current') {
        notifySuccess('Atelier is up to date', {message: `Version ${outcome.currentVersion}`});
      } else if (outcome.error?.startsWith('Airplane mode')) {
        // A policy refusal, not a failure — surface it as-is.
        notifyInfo(outcome.error);
      } else {
        notifyError('Update check failed', outcome.error || 'unknown error');
      }
    } catch (error) {
      notifyError('Update check failed', error);
    } finally {
      setUpdateCheckBusy(false);
    }
  }

  return {
    checkForUpdates,
    dismissUpdate,
    requestUpdateInstall,
    updateAvailable,
    updateBusy,
    updateCheckBusy,
    updateError,
    updateQueued,
  };
}

export {useUpdates};
