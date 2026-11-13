import {useState} from 'react';
import {
  CheckFalConnection,
  CheckOpenRouterConnection,
  CheckReplicateConnection,
  CheckRunwareConnection,
  ClearOpenAICompatibleAPIKey,
  HasOpenAICompatibleAPIKey,
  HasRunwareAPIKey,
  SaveOpenAICompatibleAPIKey,
  SaveRunwareAPIKey,
} from '../../wailsjs/go/main/App';
import {notifications, notifyError} from '../notifications';
import {formatError, formatFalError, formatOpenRouterError} from '../utils';

// useProviderKeys owns each cloud provider's key state for the Settings
// Providers tab: the key being typed, whether the keychain holds one, and the
// connection status light with its error, for OpenRouter, fal.ai, Replicate,
// and the OpenAI-compatible endpoint. The save, clear, and check handlers stay
// in App: they also reach the catalogs, the provider selections, and toasts.
function useProviderKeys() {
  const [openRouterAPIKeyInput, setOpenRouterAPIKeyInput] = useState('');
  const [openRouterHasKey, setOpenRouterHasKey] = useState(false);
  const [openRouterStatus, setOpenRouterStatus] = useState<'unknown' | 'connected' | 'error'>('unknown');
  const [openRouterError, setOpenRouterError] = useState('');

  const [openaiCompatibleKeyInput, setOpenaiCompatibleKeyInput] = useState('');
  const [openaiCompatibleHasKey, setOpenaiCompatibleHasKey] = useState(false);
  const [openaiCompatibleStatus, setOpenaiCompatibleStatus] = useState<'unknown' | 'ok' | 'error'>('unknown');
  const [falAPIKeyInput, setFalAPIKeyInput] = useState('');
  const [falHasKey, setFalHasKey] = useState(false);
  const [falStatus, setFalStatus] = useState<'unknown' | 'connected' | 'error'>('unknown');
  const [falError, setFalError] = useState('');

  // Replicate backend state — the key row, the generation slots (text-to-image,
  // image edit, text-to-video, image-to-video, video extend), and the
  // transform slots that follow VideoProvider (upscale, reframe, restyle),
  // plus their catalogs. The non-voice audio slots follow AudioProvider the
  // same way; voice stays fal-only.
  const [replicateAPIKeyInput, setReplicateAPIKeyInput] = useState('');
  const [replicateHasKey, setReplicateHasKey] = useState(false);
  const [replicateStatus, setReplicateStatus] = useState<'unknown' | 'connected' | 'error'>('unknown');
  const [replicateError, setReplicateError] = useState('');

  // Runware (image generation only, for now). Its save/clear/check handlers
  // live here rather than in App: nothing else reacts to the key — the model
  // index is public, so the catalog loads with or without it.
  const [runwareAPIKeyInput, setRunwareAPIKeyInput] = useState('');
  const [runwareHasKey, setRunwareHasKey] = useState(false);
  const [runwareStatus, setRunwareStatus] = useState<'unknown' | 'connected' | 'error'>('unknown');
  const [runwareError, setRunwareError] = useState('');

  async function checkRunwareConnection() {
    try {
      await CheckRunwareConnection();
      setRunwareStatus('connected');
      setRunwareError('');
    } catch (error) {
      setRunwareStatus('error');
      setRunwareError(formatError(error));
    }
  }

  async function loadRunwareKeyState() {
    try {
      const hasKey = await HasRunwareAPIKey();
      setRunwareHasKey(hasKey);
      if (hasKey) await checkRunwareConnection();
    } catch {
      setRunwareHasKey(false);
    }
  }

  async function saveRunwareKey() {
    try {
      await SaveRunwareAPIKey(runwareAPIKeyInput);
      setRunwareAPIKeyInput('');
      setRunwareStatus('unknown');
      setRunwareError('');
      const hasKey = await HasRunwareAPIKey();
      setRunwareHasKey(hasKey);
      notifications.dismissKey('key-runware');
      if (hasKey) await checkRunwareConnection();
    } catch (error) {
      notifyError("Couldn't save the Runware key", error, {key: 'key-runware'});
    }
  }

  async function clearRunwareKey() {
    try {
      await SaveRunwareAPIKey('');
      setRunwareHasKey(false);
      setRunwareStatus('unknown');
      setRunwareError('');
      notifications.dismissKey('key-runware');
    } catch (error) {
      notifyError("Couldn't remove the Runware key", error, {key: 'key-runware'});
    }
  }

  async function checkOpenRouterConnection() {
    try {
      await CheckOpenRouterConnection();
      setOpenRouterStatus('connected');
      setOpenRouterError('');
    } catch (error) {
      setOpenRouterStatus('error');
      setOpenRouterError(formatOpenRouterError(error));
    }
  }

  async function checkFalConnection() {
    try {
      await CheckFalConnection();
      setFalStatus('connected');
      setFalError('');
    } catch (error) {
      setFalStatus('error');
      setFalError(formatFalError(error));
    }
  }

  async function checkReplicateConnection() {
    try {
      await CheckReplicateConnection();
      setReplicateStatus('connected');
      setReplicateError('');
    } catch (error) {
      setReplicateStatus('error');
      setReplicateError(formatError(error));
    }
  }

  async function saveOpenAICompatibleKey() {
    try {
      await SaveOpenAICompatibleAPIKey(openaiCompatibleKeyInput);
      setOpenaiCompatibleKeyInput('');
      setOpenaiCompatibleHasKey(await HasOpenAICompatibleAPIKey());
      notifications.dismissKey('key-openai-compatible');
    } catch (error) {
      notifyError("Couldn't save the OpenAI-compatible key", error, {key: 'key-openai-compatible'});
    }
  }

  async function clearOpenAICompatibleKey() {
    try {
      await ClearOpenAICompatibleAPIKey();
      setOpenaiCompatibleHasKey(false);
      notifications.dismissKey('key-openai-compatible');
    } catch (error) {
      notifyError("Couldn't remove the OpenAI-compatible key", error, {key: 'key-openai-compatible'});
    }
  }

  return {
    checkFalConnection,
    checkOpenRouterConnection,
    checkReplicateConnection,
    checkRunwareConnection,
    clearOpenAICompatibleKey,
    clearRunwareKey,
    loadRunwareKeyState,
    runwareAPIKeyInput,
    runwareError,
    runwareHasKey,
    runwareStatus,
    saveRunwareKey,
    setRunwareAPIKeyInput,
    falAPIKeyInput,
    falError,
    falHasKey,
    falStatus,
    openRouterAPIKeyInput,
    openRouterError,
    openRouterHasKey,
    openRouterStatus,
    openaiCompatibleHasKey,
    openaiCompatibleKeyInput,
    openaiCompatibleStatus,
    replicateAPIKeyInput,
    replicateError,
    replicateHasKey,
    replicateStatus,
    saveOpenAICompatibleKey,
    setFalAPIKeyInput,
    setFalError,
    setFalHasKey,
    setFalStatus,
    setOpenRouterAPIKeyInput,
    setOpenRouterError,
    setOpenRouterHasKey,
    setOpenRouterStatus,
    setOpenaiCompatibleHasKey,
    setOpenaiCompatibleKeyInput,
    setOpenaiCompatibleStatus,
    setReplicateAPIKeyInput,
    setReplicateError,
    setReplicateHasKey,
    setReplicateStatus,
  };
}

export {useProviderKeys};
