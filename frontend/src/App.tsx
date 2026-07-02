import {useEffect, useMemo, useRef, useState} from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import './App.css';
import {
  CancelStream,
  CheckOllama,
  ChooseToolWorkspace,
  DeleteConversation,
  GetConversation,
  GetConfig,
  HasOpenRouterAPIKey,
  ListConversations,
  ListModels,
  ListPrimaryModels,
  PurgeArchivedConversations,
  ResolveToolPermission,
  SaveImage,
  SaveConfig,
  SaveOpenRouterAPIKey,
  StreamChat,
  UpdateConversationTitle,
} from '../wailsjs/go/main/App';
import {main} from '../wailsjs/go/models';
import {EventsOff, EventsOn} from '../wailsjs/runtime/runtime';

type View = 'app' | 'settings';
type ConversationKind = 'chat';

type ChatEntry = {
  id: string;
  role: 'user' | 'assistant' | 'system';
  content: string;
  thinking?: string;
  images?: string[];
  harnessRun?: HarnessRunView;
  streaming?: boolean;
  error?: string;
  provider?: string;
};

type ChatChunk = {
  requestID: string;
  content?: string;
  thinking?: string;
  images?: string[];
  done: boolean;
  error?: string;
  model?: string;
  provider?: string;
  reason?: string;
  tokens?: number;
  conversationId?: string;
};

type ChatStreamDraft = {
  content: string;
  thinking: string;
  images: string[];
  streaming: boolean;
  error?: string;
  provider?: string;
};

type ToolPermissionEvent = {
  id: string;
  requestID?: string;
  conversationId?: string;
  toolName: string;
  action: string;
  summary: string;
  command?: string[];
  cwd?: string;
  path?: string;
  contentPreview?: string;
};

type InFlightConversation = {
  requestID: string;
  kind: ConversationKind;
};

type HarnessRunView = {
  id?: string;
  mode?: string;
  status?: string;
  startedAt?: string;
  completedAt?: string;
  durationMs?: number;
  requestId?: string;
  conversationId?: string;
  loop?: {
    maxSteps?: number;
    maxWallTimeMs?: number;
    iterations?: number;
    stopReason?: string;
  };
  steps?: HarnessStepView[];
};

type HarnessStepView = {
  id?: string;
  kind?: string;
  iteration?: number;
  provider?: string;
  model?: string;
  status?: string;
  startedAt?: string;
  completedAt?: string;
  durationMs?: number;
  decision?: string;
  doneReason?: string;
  summary?: string;
  error?: string;
  tokens?: number;
  tools?: HarnessToolActivityView[];
};

type HarnessToolActivityView = {
  name?: string;
  status?: string;
  path?: string;
  command?: string[];
  exitCode?: number;
  stdoutPreview?: string;
  stderrPreview?: string;
  durationMs?: number;
  error?: string;
};

type Attachment = {
  name: string;
  src: string;
  payload: string;
};

const defaultBaseURL = 'http://localhost:11434';
const defaultSidebarWidth = 320;
const minSidebarWidth = 240;
const maxSidebarWidth = 560;
const compactHistoryLimit = 10;
const expandedHistoryBatchSize = 20;
const defaultImageWidth = 768;
const defaultImageHeight = 768;
const defaultImageSteps = 24;

// Coerce a numeric settings input to a positive integer, falling back to the
// backend default when the field is cleared or otherwise invalid. Mirrors the
// `<= 0` fallback merge in app.go's mergeAppConfig.
function positiveIntOrDefault(value: string, fallback: number): number {
  const parsed = Number(value);
  return Number.isInteger(parsed) && parsed > 0 ? parsed : fallback;
}

function App() {
  const [baseURL, setBaseURL] = useState(defaultBaseURL);
  const [status, setStatus] = useState<main.OllamaStatus | null>(null);
  const [models, setModels] = useState<main.OllamaModel[]>([]);
  const [refreshing, setRefreshing] = useState(false);
  const [model, setModel] = useState('');
  const [harnessModel, setHarnessModel] = useState('');
  const [imageModel, setImageModel] = useState('');
  const [primaryProvider, setPrimaryProvider] = useState<'ollama' | 'openrouter'>('ollama');
  const [openRouterModels, setOpenRouterModels] = useState<main.ModelInfo[]>([]);
  const [openRouterAPIKeyInput, setOpenRouterAPIKeyInput] = useState('');
  const [openRouterHasKey, setOpenRouterHasKey] = useState(false);
  const [openRouterStatus, setOpenRouterStatus] = useState<'unknown' | 'connected' | 'error'>('unknown');
  const [openRouterError, setOpenRouterError] = useState('');
  const [system, setSystem] = useState('You are Atelier, a precise local AI collaborator.');
  const [prompt, setPrompt] = useState('');
  const [attachments, setAttachments] = useState<Attachment[]>([]);
  const [chat, setChat] = useState<ChatEntry[]>([]);
  const [collapsedThinkingIDs, setCollapsedThinkingIDs] = useState<Record<string, boolean>>({});
  const [copiedMessageID, setCopiedMessageID] = useState('');
  const [copiedConversationID, setCopiedConversationID] = useState('');
  const [conversations, setConversations] = useState<main.ConversationSummary[]>([]);
  const [historyExpanded, setHistoryExpanded] = useState(false);
  const [visibleHistoryCount, setVisibleHistoryCount] = useState(compactHistoryLimit);
  const [activeConversationID, setActiveConversationID] = useState('');
  const [activeStream, setActiveStream] = useState<string | null>(null);
  const [inFlightConversations, setInFlightConversations] = useState<Record<string, InFlightConversation>>({});
  const [imageWidth, setImageWidth] = useState(defaultImageWidth);
  const [imageHeight, setImageHeight] = useState(defaultImageHeight);
  const [imageSteps, setImageSteps] = useState(defaultImageSteps);
  const [configLoaded, setConfigLoaded] = useState(false);
  const [storageConfig, setStorageConfig] = useState<main.ConfigStorage | null>(null);
  const [toolConfig, setToolConfig] = useState<main.ConfigTools | null>(null);
  const [toolPermissions, setToolPermissions] = useState<ToolPermissionEvent[]>([]);
  const [startupError, setStartupError] = useState('');
  const [editingTitleID, setEditingTitleID] = useState('');
  const [editingTitle, setEditingTitle] = useState('');
  const [openHistoryMenuID, setOpenHistoryMenuID] = useState('');
  const [sidebarWidth, setSidebarWidth] = useState(loadSidebarWidth);
  const [resizingSidebar, setResizingSidebar] = useState(false);
  const [view, setView] = useState<View>('app');
  const [previewImage, setPreviewImage] = useState('');
  const [purgeBusy, setPurgeBusy] = useState(false);
  const [confirmPurgeArchived, setConfirmPurgeArchived] = useState(false);
  const [purgeStatus, setPurgeStatus] = useState('');
  const [openCapabilityID, setOpenCapabilityID] = useState('');
  const shellRef = useRef<HTMLElement | null>(null);
  const transcriptRef = useRef<HTMLDivElement | null>(null);
  const shouldFollowTranscriptRef = useRef(true);
  const visibleStreamRef = useRef<string | null>(null);
  const inFlightConversationsRef = useRef<Record<string, InFlightConversation>>({});
  const requestConversationRef = useRef<Record<string, {conversationID: string; kind: ConversationKind}>>({});
  const chatStreamDraftsRef = useRef<Record<string, ChatStreamDraft>>({});
  const chatPromptRef = useRef<HTMLTextAreaElement | null>(null);
  const copyResetRef = useRef<number | null>(null);

  const assistantEntryID = activeStream ? `assistant-${activeStream}` : '';
  const conversationList = asArray(conversations);
  const visibleConversations = historyExpanded
    ? conversationList.slice(0, visibleHistoryCount)
    : conversationList.slice(0, compactHistoryLimit);
  const hasMoreConversations = visibleConversations.length < conversationList.length;
  const selectedConversationID = activeConversationID;
  const latestHarnessRun = [...chat].reverse().find((entry) => entry.role === 'assistant' && entry.harnessRun)?.harnessRun;
  const visibleHarnessRun = latestHarnessRun ?? (activeStream ? buildRunningHarnessRun(activeStream, activeConversationID, model) : null);

  function markConversationInFlight(conversationID: string, requestID: string, kind: ConversationKind) {
    requestConversationRef.current[requestID] = {conversationID, kind};
    const next = {
      ...inFlightConversationsRef.current,
      [conversationID]: {requestID, kind},
    };
    inFlightConversationsRef.current = next;
    setInFlightConversations(next);
  }

  function clearConversationInFlight(requestID: string) {
    const tracked = requestConversationRef.current[requestID];
    if (!tracked) {
      return;
    }
    delete requestConversationRef.current[requestID];
    const next = {...inFlightConversationsRef.current};
    if (next[tracked.conversationID]?.requestID === requestID) {
      delete next[tracked.conversationID];
    }
    inFlightConversationsRef.current = next;
    setInFlightConversations(next);
  }

  useEffect(() => {
    loadConfig().catch((error) => {
      setStartupError(formatError(error));
      setConfigLoaded(true);
      refreshOllama(defaultBaseURL).catch((refreshError) => setStatus({
        online: false,
        baseURL: defaultBaseURL,
        error: formatError(refreshError),
      }));
    });
  }, []);

  useEffect(() => {
    return () => {
      if (copyResetRef.current) {
        window.clearTimeout(copyResetRef.current);
      }
    };
  }, []);

  useEffect(() => {
    if (!configLoaded) {
      return;
    }
    const timeout = window.setTimeout(() => {
      SaveConfig(main.AppConfig.createFrom({
        version: 1,
        storage: storageConfig ?? undefined,
        providers: {
          ollama: {
            baseURL,
            models: {
              primary: model,
              harness: harnessModel,
              image: imageModel,
            },
          },
        },
        prompts: {
          system,
        },
        generation: {
          image: {
            width: imageWidth,
            height: imageHeight,
            steps: imageSteps,
          },
        },
        tools: toolConfig ?? undefined,
        ui: {
          mode: 'chat',
        },
      })).catch((error) => {
        setStatus((current) => current ? {...current, error: String(error)} : current);
      });
    }, 400);
    return () => window.clearTimeout(timeout);
  }, [baseURL, configLoaded, harnessModel, imageHeight, imageModel, imageSteps, imageWidth, model, storageConfig, system, toolConfig]);

  useEffect(() => {
    const onChunk = (chunk: ChatChunk) => {
      const isVisibleStream = visibleStreamRef.current === chunk.requestID;
      if (chunk.conversationId) {
        markConversationInFlight(chunk.conversationId, chunk.requestID, 'chat');
      }
      const draft = chatStreamDraftsRef.current[chunk.requestID] ?? {content: '', thinking: '', images: [], streaming: true};
      chatStreamDraftsRef.current[chunk.requestID] = {
        content: `${draft.content}${chunk.content ?? ''}`,
        thinking: `${draft.thinking}${chunk.thinking ?? ''}`,
        images: chunk.images?.length ? chunk.images : draft.images,
        streaming: !chunk.done && !chunk.error,
        error: chunk.error ?? draft.error,
        provider: chunk.provider ?? draft.provider,
      };
      setChat((entries) =>
        entries.map((entry) => {
          if (entry.id !== `assistant-${chunk.requestID}`) {
            return entry;
          }
          const nextDraft = chatStreamDraftsRef.current[chunk.requestID];
          return {
            ...entry,
            content: nextDraft.content,
            thinking: nextDraft.thinking,
            images: nextDraft.images,
            streaming: nextDraft.streaming,
            error: nextDraft.error,
            provider: nextDraft.provider ?? entry.provider,
          };
        }),
      );
      if (chunk.done || chunk.error) {
        clearConversationInFlight(chunk.requestID);
        setActiveStream((current) => current === chunk.requestID ? null : current);
        if (isVisibleStream) {
          visibleStreamRef.current = null;
        }
      }
      if (chunk.conversationId && isVisibleStream) {
        setActiveConversationID(chunk.conversationId);
      }
      if (chunk.conversationId || chunk.done || chunk.error) {
        void refreshConversations();
      }
    };
    EventsOn('chat:chunk', onChunk);
    return () => EventsOff('chat:chunk');
  }, []);

  useEffect(() => {
    const onToolPermission = (event: ToolPermissionEvent) => {
      setToolPermissions((current) => current.some((item) => item.id === event.id) ? current : [...current, event]);
    };
    EventsOn('atelier:tool-permission', onToolPermission);
    return () => EventsOff('atelier:tool-permission');
  }, []);

  async function resolveToolPermission(permissionID: string, approved: boolean) {
    setToolPermissions((current) => current.filter((item) => item.id !== permissionID));
    try {
      await ResolveToolPermission(permissionID, approved);
    } catch (error) {
      setStartupError(formatError(error));
    }
  }

  useEffect(() => {
    const transcript = transcriptRef.current;
    if (!transcript || !shouldFollowTranscriptRef.current) {
      return;
    }
    transcript.scrollTo({top: transcript.scrollHeight, behavior: 'smooth'});
  }, [chat]);

  useEffect(() => {
    if (!resizingSidebar) {
      return;
    }
    const onMouseMove = (event: MouseEvent) => {
      const left = shellRef.current?.getBoundingClientRect().left ?? 0;
      const max = Math.min(maxSidebarWidth, window.innerWidth - 420);
      setSidebarWidth(clampSidebarWidth(event.clientX - left, max));
    };
    const onMouseUp = () => setResizingSidebar(false);
    window.addEventListener('mousemove', onMouseMove);
    window.addEventListener('mouseup', onMouseUp);
    return () => {
      window.removeEventListener('mousemove', onMouseMove);
      window.removeEventListener('mouseup', onMouseUp);
    };
  }, [resizingSidebar]);

  useEffect(() => {
    if (!previewImage) {
      return;
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setPreviewImage('');
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [previewImage]);

  useEffect(() => {
    window.localStorage.setItem('atelier.sidebarWidth', String(sidebarWidth));
  }, [sidebarWidth]);

  useEffect(() => {
    if (!openCapabilityID) {
      return;
    }
    const onPointerDown = (event: MouseEvent) => {
      if (event.target instanceof Element && event.target.closest('.model-capability')) {
        return;
      }
      setOpenCapabilityID('');
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setOpenCapabilityID('');
      }
    };
    document.addEventListener('mousedown', onPointerDown);
    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('mousedown', onPointerDown);
      document.removeEventListener('keydown', onKeyDown);
    };
  }, [openCapabilityID]);

  const modelOptions = useMemo(() => {
    return Array.from(new Set([...asArray(models).map((item) => item.name), model, harnessModel, imageModel].filter(Boolean)));
  }, [harnessModel, imageModel, model, models]);
  const primaryModelOptions = useMemo(() => {
    if (primaryProvider === 'openrouter') {
      return asArray(openRouterModels).map((item) => ({value: item.id, label: item.displayName || item.id}));
    }
    return modelOptions.map((name) => ({value: name, label: name}));
  }, [modelOptions, openRouterModels, primaryProvider]);
  const primaryModelIsValid = primaryModelOptions.some((option) => option.value === model);
  const imageModelOptions = useMemo(() => {
    const detected = asArray(models).filter((item) => item.imageGeneration).map((item) => item.name).filter(Boolean);
    return detected.length ? detected : modelOptions;
  }, [modelOptions, models]);

  useEffect(() => {
    if (!imageModelOptions.length || imageModelOptions.includes(imageModel)) {
      return;
    }
    setImageModel(imageModelOptions[0]);
  }, [imageModel, imageModelOptions]);

  useEffect(() => {
    // Only re-run when the option list itself changes (provider switch, or
    // the OpenRouter list finishing a load) — not on every keystroke of
    // `model`, since the primary-model field is now free-text (filterable)
    // and is expected to be transiently "invalid" while the user is typing.
    if (!primaryModelOptions.length) {
      return;
    }
    setModel((current) => (primaryModelOptions.some((option) => option.value === current) ? current : primaryModelOptions[0].value));
  }, [primaryModelOptions]);

  useEffect(() => {
    if (primaryProvider === 'openrouter' && openRouterHasKey && openRouterModels.length === 0 && openRouterStatus !== 'error') {
      refreshOpenRouterModels();
    }
  }, [primaryProvider, openRouterHasKey, openRouterModels.length, openRouterStatus]);

  async function loadConfig() {
    const config = await GetConfig();
    const nextBaseURL = config.providers?.ollama?.baseURL || defaultBaseURL;
    const nextPrimaryModel = config.providers?.ollama?.models?.primary ?? '';
    const nextHarnessModel = config.providers?.ollama?.models?.harness || nextPrimaryModel;
    const nextImageModel = config.providers?.ollama?.models?.image ?? '';
    const nextSystem = config.prompts?.system || 'You are Atelier, a precise local AI collaborator.';
    const nextImageWidth = config.generation?.image?.width || defaultImageWidth;
    const nextImageHeight = config.generation?.image?.height || nextImageWidth;
    const nextImageSteps = config.generation?.image?.steps || defaultImageSteps;

    setStartupError('');
    setStorageConfig(config.storage ?? null);
    setToolConfig(config.tools ?? null);
    setBaseURL(nextBaseURL);
    setModel(nextPrimaryModel);
    setHarnessModel(nextHarnessModel);
    setImageModel(nextImageModel);
    setSystem(nextSystem);
    setImageWidth(nextImageWidth);
    setImageHeight(nextImageHeight);
    setImageSteps(nextImageSteps);
    setConfigLoaded(true);
    await Promise.all([
      refreshConversations(),
      refreshOllama(nextBaseURL),
      HasOpenRouterAPIKey().then((hasKey) => {
        setOpenRouterHasKey(hasKey);
        if (hasKey) {
          refreshOpenRouterModels();
        }
      }).catch(() => setOpenRouterHasKey(false)),
    ]);
  }

  async function refreshConversations() {
    try {
      const nextConversations = await ListConversations();
      setConversations(asArray(nextConversations));
      setVisibleHistoryCount((current) => historyExpanded ? Math.max(current, compactHistoryLimit) : compactHistoryLimit);
    } catch (error) {
      setStartupError(formatError(error));
      setConversations([]);
    }
  }

  function showMoreConversations() {
    setHistoryExpanded(true);
    setVisibleHistoryCount((current) => Math.max(current, compactHistoryLimit) + expandedHistoryBatchSize);
  }

  async function chooseToolWorkspace() {
    try {
      const selected = await ChooseToolWorkspace(toolConfig?.filesystem?.root ?? '');
      if (!selected) {
        return;
      }
      setToolConfig((currentConfig) => main.ConfigTools.createFrom({
        filesystem: {
          ...(currentConfig?.filesystem ?? {}),
          root: selected,
        },
      }));
    } catch (error) {
      setStartupError(formatError(error));
    }
  }

  function handleHistoryScroll(event: React.UIEvent<HTMLDivElement>) {
    if (!historyExpanded || !hasMoreConversations || !isNearScrollBottom(event.currentTarget, 96)) {
      return;
    }
    setVisibleHistoryCount((current) => Math.min(current + expandedHistoryBatchSize, conversationList.length));
  }

  async function refreshOllama(endpoint = baseURL) {
    setRefreshing(true);
    try {
      const nextStatus = await CheckOllama(endpoint);
      setStatus(nextStatus);
      if (!nextStatus.online) {
        setModels([]);
        return;
      }
      const nextModels = asArray(await ListModels(endpoint));
      setModels(nextModels);
      const firstModel = nextModels[0]?.name ?? '';
      const firstImageModel = nextModels.find((item) => item.imageGeneration)?.name ?? firstModel;
      setModel((current) => current || firstModel);
      setHarnessModel((current) => current || firstModel);
      setImageModel((current) => current || firstImageModel);
    } finally {
      setRefreshing(false);
    }
  }

  async function refreshOpenRouterModels() {
    try {
      const nextModels = asArray(await ListPrimaryModels('openrouter', ''));
      setOpenRouterModels(nextModels);
      setOpenRouterStatus('connected');
      setOpenRouterError('');
    } catch (error) {
      setOpenRouterStatus('error');
      setOpenRouterError(formatOpenRouterError(error));
    }
  }

  async function saveOpenRouterKey() {
    try {
      await SaveOpenRouterAPIKey(openRouterAPIKeyInput);
      setOpenRouterAPIKeyInput('');
      const hasKey = await HasOpenRouterAPIKey();
      setOpenRouterHasKey(hasKey);
      await refreshOpenRouterModels();
    } catch (error) {
      setOpenRouterStatus('error');
      setOpenRouterError(formatError(error));
    }
  }

  async function clearOpenRouterKey() {
    try {
      await SaveOpenRouterAPIKey('');
      setOpenRouterHasKey(false);
      setOpenRouterModels([]);
      setOpenRouterStatus('unknown');
      setOpenRouterError('');
      setPrimaryProvider((current) => current === 'openrouter' ? 'ollama' : current);
    } catch (error) {
      setOpenRouterStatus('error');
      setOpenRouterError(formatError(error));
    }
  }

  async function resetWorkspace() {
    visibleStreamRef.current = null;
    setActiveStream(null);
    setChat([]);
    setCollapsedThinkingIDs({});
    setPrompt('');
    setAttachments([]);
    setActiveConversationID('');
    setView('app');
    window.setTimeout(() => {
      chatPromptRef.current?.focus();
    }, 0);
  }

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.repeat || event.altKey || event.shiftKey) {
        return;
      }
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'n') {
        event.preventDefault();
        void resetWorkspace();
      }
      if ((event.metaKey || event.ctrlKey) && event.key === ',') {
        event.preventDefault();
        setView('settings');
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [activeStream]);

  async function startNewChat() {
    await resetWorkspace();
  }

  async function openConversationSummary(conversation: main.ConversationSummary) {
    try {
      const detail = await GetConversation(conversation.id);
      setView('app');
      hydrateChatConversation(detail);
    } catch (error) {
      setStartupError(formatError(error));
    }
  }

  function startEditingConversationTitle(conversation: main.ConversationSummary) {
    setOpenHistoryMenuID('');
    setEditingTitleID(conversation.id);
    setEditingTitle(conversation.title);
  }

  function cancelEditingConversationTitle() {
    setEditingTitleID('');
    setEditingTitle('');
  }

  async function saveConversationTitle(conversation: main.ConversationSummary) {
    const title = editingTitle.trim();
    if (!title || title === conversation.title) {
      cancelEditingConversationTitle();
      return;
    }
    try {
      const updated = await UpdateConversationTitle(conversation.id, title);
      setConversations((items) =>
        asArray(items).map((item) => item.id === updated.id ? {...item, ...updated} : item),
      );
      cancelEditingConversationTitle();
    } catch (error) {
      setStartupError(formatError(error));
    }
  }

  function handleConversationTitleKeyDown(event: React.KeyboardEvent<HTMLInputElement>, conversation: main.ConversationSummary) {
    if (event.key === 'Enter') {
      event.preventDefault();
      void saveConversationTitle(conversation);
    }
    if (event.key === 'Escape') {
      event.preventDefault();
      cancelEditingConversationTitle();
    }
  }

  function hydrateChatConversation(detail: main.ConversationDetail) {
    const inFlight = inFlightConversationsRef.current[detail.conversation.id];
    const visibleRequestID = inFlight?.kind === 'chat' ? inFlight.requestID : null;
    visibleStreamRef.current = visibleRequestID;
    setActiveStream(visibleRequestID);
    shouldFollowTranscriptRef.current = true;
    const entries: ChatEntry[] = asArray(detail.turns).map((turn) => ({
      id: turn.id,
      role: turn.role === 'user' || turn.role === 'system' ? turn.role : 'assistant',
      content: historyText(turn.content, 'text'),
      thinking: historyText(turn.content, 'thinking'),
      images: historyImages(turn.content),
      harnessRun: parseHarnessRun(turn.providerResponse?.harnessRun),
      provider: turn.provider,
    }));
    if (visibleRequestID && !entries.some((entry) => entry.id === `assistant-${visibleRequestID}`)) {
      const draft = chatStreamDraftsRef.current[visibleRequestID];
      entries.push({
        id: `assistant-${visibleRequestID}`,
        role: 'assistant',
        content: draft?.content ?? '',
        thinking: draft?.thinking,
        images: draft?.images,
        streaming: draft?.streaming ?? true,
        error: draft?.error,
        provider: draft?.provider,
      });
    }
    setChat(entries);
    setCollapsedThinkingIDs({});
    setActiveConversationID(detail.conversation.id);
    setPrompt('');
    setAttachments([]);
  }

  async function copyConversationID(conversation: main.ConversationSummary) {
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(conversation.id);
      } else {
        copyTextWithTextarea(conversation.id);
      }
      setCopiedConversationID(conversation.id);
      if (copyResetRef.current) {
        window.clearTimeout(copyResetRef.current);
      }
      copyResetRef.current = window.setTimeout(() => setCopiedConversationID(''), 1600);
    } catch (error) {
      console.error('copy failed', error);
    }
  }

  async function archiveConversation(conversation: main.ConversationSummary) {
    try {
      setOpenHistoryMenuID('');
      await DeleteConversation(conversation.id);
      setConversations((items) => asArray(items).filter((item) => item.id !== conversation.id));
      if (editingTitleID === conversation.id) {
        cancelEditingConversationTitle();
      }
      if (activeConversationID === conversation.id) {
        setActiveConversationID('');
        setChat([]);
      }
    } catch (error) {
      setStartupError(formatError(error));
    }
  }

  async function purgeArchivedConversations() {
    if (purgeBusy) {
      return;
    }
    if (!confirmPurgeArchived) {
      setConfirmPurgeArchived(true);
      setPurgeStatus('');
      return;
    }
    try {
      setPurgeBusy(true);
      setPurgeStatus('');
      const result = await PurgeArchivedConversations();
      await refreshConversations();
      setConfirmPurgeArchived(false);
      setPurgeStatus(`${result.deletedConversations} archived ${result.deletedConversations === 1 ? 'conversation' : 'conversations'} and ${result.deletedAssets} ${result.deletedAssets === 1 ? 'asset' : 'assets'} deleted.`);
    } catch (error) {
      setPurgeStatus('');
      setStartupError(formatError(error));
    } finally {
      setPurgeBusy(false);
    }
  }


  async function submitChat() {
    const trimmed = prompt.trim();
    if (!trimmed || !model || activeStream || !primaryModelIsValid) {
      return;
    }

    const userEntry: ChatEntry = {
      id: `user-${Date.now()}`,
      role: 'user',
      content: trimmed,
      images: attachments.map((item) => item.src),
    };
    const requestID = `chat-${Date.now()}-${Math.random().toString(36).slice(2)}`;
    const requestMessages: main.ChatMessage[] = [
      ...chat
        .filter((entry) => entry.role !== 'system' && (entry.content || entry.images?.length))
        .map((entry) => ({
          role: entry.role,
          content: entry.content,
          ...(entry.images?.length ? {images: entry.images.map(imagePayloadForOllama).filter(Boolean)} : {}),
        })),
      {
        role: 'user',
        content: trimmed,
        ...(attachments.length ? {images: attachments.map((item) => item.payload)} : {}),
      },
    ];

    setPrompt('');
    setAttachments([]);
    shouldFollowTranscriptRef.current = true;
    visibleStreamRef.current = requestID;
    chatStreamDraftsRef.current[requestID] = {content: '', thinking: '', images: [], streaming: true};
    setActiveStream(requestID);
    setChat((entries) => [
      ...entries,
      userEntry,
      {id: `assistant-${requestID}`, role: 'assistant', content: '', streaming: true, provider: primaryProvider},
    ]);

    try {
      const start = await StreamChat(main.ChatRequest.createFrom({
        requestID,
        conversationId: activeConversationID || undefined,
        baseURL,
        provider: primaryProvider,
        model,
        selectedModel: model,
        system,
        messages: requestMessages,
      }));
      markConversationInFlight(start.conversationId, start.requestID, 'chat');
      setActiveConversationID(start.conversationId);
      void refreshConversations();
    } catch (error) {
      chatStreamDraftsRef.current[requestID] = {
        ...(chatStreamDraftsRef.current[requestID] ?? {content: '', thinking: '', images: []}),
        streaming: false,
        error: formatError(error),
      };
      setActiveStream(null);
      if (visibleStreamRef.current === requestID) {
        visibleStreamRef.current = null;
      }
      setChat((entries) =>
        entries.map((entry) =>
          entry.id === `assistant-${requestID}` ? {...entry, streaming: false, error: formatError(error)} : entry,
        ),
      );
    }
  }

  function handleChatPromptKeyDown(event: React.KeyboardEvent<HTMLTextAreaElement>) {
    if (event.key === 'Enter' && !event.shiftKey) {
      event.preventDefault();
      void submitChat();
    }
  }

  async function stopChat() {
    if (activeStream) {
      await CancelStream(activeStream);
      chatStreamDraftsRef.current[activeStream] = {
        ...(chatStreamDraftsRef.current[activeStream] ?? {content: '', thinking: '', images: []}),
        streaming: false,
        error: 'Stopped',
      };
      setActiveStream(null);
      visibleStreamRef.current = null;
      setChat((entries) =>
        entries.map((entry) =>
          entry.id === assistantEntryID ? {...entry, streaming: false, error: 'Stopped'} : entry,
        ),
      );
    }
  }

  function toggleThinkingCollapsed(entryID: string) {
    setCollapsedThinkingIDs((current) => ({
      ...current,
      [entryID]: !current[entryID],
    }));
  }

  async function copyAgentResponse(entry: ChatEntry) {
    if (!entry.content) {
      return;
    }
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(entry.content);
      } else {
        copyTextWithTextarea(entry.content);
      }
      setCopiedMessageID(entry.id);
      if (copyResetRef.current) {
        window.clearTimeout(copyResetRef.current);
      }
      copyResetRef.current = window.setTimeout(() => setCopiedMessageID(''), 1600);
    } catch (error) {
      console.error('copy failed', error);
    }
  }

  async function addImages(files: FileList | null) {
    if (!files) {
      return;
    }
    const next = await Promise.all(Array.from(files).map(readImageFile));
    setAttachments((items) => [...items, ...next]);
  }

  async function saveGeneratedImage(image: string, index: number) {
    try {
      await SaveImage(main.SaveImageRequest.createFrom({
        image,
        suggestedName: `atelier-${Date.now()}-${index + 1}`,
      }));
    } catch (error) {
      setStartupError(error instanceof Error ? error.message : String(error));
    }
  }

  return (
    <main
      ref={shellRef}
      className={view === 'settings' ? 'shell settings-open' : resizingSidebar ? 'shell resizing' : 'shell'}
      style={view === 'settings' ? undefined : {'--sidebar-width': `${sidebarWidth}px`} as Record<string, string>}
    >
      {view === 'settings' ? null : (
        <aside className="sidebar">
          <div className="sidebar-main">
            <div className="brand">
              <div className="mark">A</div>
              <div>
                <h1>Atelier</h1>
                <p>Local AI harness</p>
              </div>
            </div>

            <nav className="side-nav" aria-label="Atelier navigation">
              <button onClick={startNewChat}>
                <span className="nav-icon">+</span>
                New chat
              </button>
            </nav>

            <div className="history-area" onScroll={handleHistoryScroll}>
              <div className="section-label">Chats</div>
              {conversationList.length ? (
                visibleConversations.map((conversation) => {
                  const inFlight = inFlightConversations[conversation.id];
                  const selected = selectedConversationID === conversation.id;
                  return (
                    <div key={conversation.id} className={`history-item${selected ? ' selected' : ''}`}>
                      {editingTitleID === conversation.id ? (
                        <input
                          value={editingTitle}
                          onChange={(event) => setEditingTitle(event.target.value)}
                          onBlur={() => saveConversationTitle(conversation)}
                          onKeyDown={(event) => handleConversationTitleKeyDown(event, conversation)}
                          autoFocus
                        />
                      ) : (
                        <>
                          <button className="history-open" onClick={() => openConversationSummary(conversation)} onDoubleClick={(event) => { event.preventDefault(); startEditingConversationTitle(conversation); }}>
                            <span>{conversation.title}</span>
                            <small
                              className={`history-kind${inFlight ? ' in-flight' : ''}`}
                              title={inFlight ? 'Running' : 'Chat'}
                              aria-label={inFlight ? 'Conversation running' : 'Chat conversation'}
                            >
                              {inFlight ? <span className="history-spinner" /> : '◌'}
                            </small>
                          </button>
                          <div className="history-actions">
                            <button
                              className="history-icon-button"
                              aria-label={`More actions for ${conversation.title}`}
                              title="More"
                              onClick={() => setOpenHistoryMenuID((current) => current === conversation.id ? '' : conversation.id)}
                            >
                              ⋮
                            </button>
                            {openHistoryMenuID === conversation.id ? (
                              <div className="history-menu">
                                <button onClick={() => copyConversationID(conversation)}>
                                  {copiedConversationID === conversation.id ? '✓ Copied' : 'Copy ID'}
                                </button>
                                <button onClick={() => archiveConversation(conversation)}>Archive</button>
                              </div>
                            ) : null}
                          </div>
                        </>
                      )}
                    </div>
                  );
                })
              ) : (
                <div className="history-empty">No conversations yet.</div>
              )}
              {hasMoreConversations ? (
                <button className="history-more" onClick={showMoreConversations}>
                  More
                </button>
              ) : null}
            </div>
          </div>

          <button className="settings-button" onClick={() => setView('settings')}>
            <span className="gear-icon" aria-hidden="true" />
            Settings
          </button>
        </aside>
      )}
      {view === 'settings' ? null : (
        <div
          className="sidebar-resizer"
          role="separator"
          aria-orientation="vertical"
          aria-label="Resize sidebar"
          onMouseDown={(event) => {
            event.preventDefault();
            setResizingSidebar(true);
          }}
        />
      )}

      <section className="workspace">
        {startupError ? (
          <div className="startup-error">
            <strong>Atelier started with a local data warning.</strong>
            <span>{startupError}</span>
          </div>
        ) : null}
        {toolPermissions.length ? (
          <div className="tool-permission-panel">
            {toolPermissions.map((permission) => (
              <div className="tool-permission-card" key={permission.id}>
                <div className="tool-permission-content">
                  <strong>{toolPermissionTitle(permission)}</strong>
                  <span>{toolPermissionSummary(permission)}</span>
                  {permission.cwd ? <small>in {shortenHomePath(permission.cwd)}</small> : null}
                  {hasToolPermissionDetails(permission) ? (
                    <details className="tool-permission-details">
                      <summary>Details</summary>
                      {permission.command?.length ? (
                        <div>
                          <small>Command</small>
                          <code>{permission.command.join(' ')}</code>
                        </div>
                      ) : null}
                      {permission.summary && permission.summary !== toolPermissionSummary(permission) ? (
                        <div>
                          <small>Summary</small>
                          <pre>{permission.summary}</pre>
                        </div>
                      ) : null}
                      {permission.path ? (
                        <div>
                          <small>Path</small>
                          <code>{shortenHomePath(permission.path)}</code>
                        </div>
                      ) : null}
                      {permission.contentPreview ? (
                        <div>
                          <small>Preview</small>
                          <pre>{permission.contentPreview}</pre>
                        </div>
                      ) : null}
                    </details>
                  ) : null}
                </div>
                <div className="tool-permission-actions">
                  <button onClick={() => resolveToolPermission(permission.id, false)}>Deny</button>
                  <button className="primary" onClick={() => resolveToolPermission(permission.id, true)}>Allow</button>
                </div>
              </div>
            ))}
          </div>
        ) : null}
        {view === 'settings' ? (
          <>
            <div className="toolbar">
              <button className="back-button" onClick={() => setView('app')}>← Back</button>
              <div className="model-count">Saved to ~/.atelier/config.json</div>
            </div>
            <div className="settings-screen">
              <div className="settings-header">
                <h2>Settings</h2>
                <p>Ollama provider, model defaults, and prompt preferences.</p>
              </div>

              <section className="settings-section">
                <h3>Provider</h3>
                <div className="connection">
                  <label htmlFor="base-url">Ollama endpoint</label>
                  <div className="endpoint-row">
                    <input id="base-url" value={baseURL} onChange={(event) => setBaseURL(event.target.value)} />
                    <button onClick={() => refreshOllama()}>Refresh</button>
                  </div>
                  <div className={status?.online ? 'status online' : 'status offline'}>
                    <span />
                    {status?.online ? `Online ${status.version ?? ''}` : status?.error ?? 'Not checked'}
                  </div>
                </div>
              </section>

              <section className="settings-section">
                <h3>OpenRouter</h3>
                <div className="connection">
                  <label htmlFor="openrouter-key">API Key</label>
                  <div className="endpoint-row">
                    <input
                      id="openrouter-key"
                      type="password"
                      placeholder={openRouterHasKey ? 'Key saved — enter a new key to replace it' : 'sk-or-...'}
                      value={openRouterAPIKeyInput}
                      onChange={(event) => setOpenRouterAPIKeyInput(event.target.value)}
                    />
                    <button type="button" onClick={saveOpenRouterKey} disabled={!openRouterAPIKeyInput}>
                      Save Key
                    </button>
                    <button type="button" onClick={refreshOpenRouterModels} disabled={!openRouterHasKey}>
                      Check Connection
                    </button>
                    {openRouterHasKey ? (
                      <button type="button" onClick={clearOpenRouterKey}>
                        Clear Key
                      </button>
                    ) : null}
                  </div>
                  <div className={openRouterStatus === 'connected' ? 'status online' : 'status offline'}>
                    <span />
                    {openRouterStatus === 'connected'
                      ? `Connected — ${openRouterModels.length} models available`
                      : openRouterStatus === 'error'
                        ? `OpenRouter: ${openRouterError}`
                        : 'Not checked'}
                  </div>
                </div>
              </section>

              <section className="settings-section">
                <h3>Storage</h3>
                <div className="storage-list">
                  <div>
                    <span>Root</span>
                    <code>{shortenHomePath(storageConfig?.root ?? '~/.atelier')}</code>
                  </div>
                  <div>
                    <span>History</span>
                    <code>{shortenHomePath(storageConfig?.history ?? '~/.atelier/history')}</code>
                  </div>
                  <div>
                    <span>Workspace</span>
                    <div className="workspace-picker">
                      <code>{shortenHomePath(toolConfig?.filesystem?.root ?? '~/Documents')}</code>
                      <button onClick={chooseToolWorkspace}>Choose</button>
                    </div>
                  </div>
                </div>
                <div className="storage-actions">
                  <button className="danger" onClick={purgeArchivedConversations} disabled={purgeBusy}>
                    {purgeBusy ? 'Deleting...' : confirmPurgeArchived ? 'Confirm Delete' : 'Delete Archived Conversations'}
                  </button>
                  {confirmPurgeArchived && !purgeBusy ? (
                    <button onClick={() => setConfirmPurgeArchived(false)}>Cancel</button>
                  ) : null}
                  {purgeStatus ? <span>{purgeStatus}</span> : null}
                </div>
                {confirmPurgeArchived ? (
                  <div className="storage-confirmation">
                    This permanently deletes archived conversations and local assets from ~/.atelier/history.
                  </div>
                ) : null}
              </section>

              <section className="settings-section two-column">
                <div className="field">
                  <label htmlFor="harness-model">Harness Model</label>
                  <div className="model-inline-control">
                    <select id="harness-model" value={harnessModel} onChange={(event) => setHarnessModel(event.target.value)}>
                      {modelOptions.map((name) => <option key={name}>{name}</option>)}
                    </select>
                    <ModelCapabilityLink
                      id="settings-tools"
                      modelName={harnessModel}
                      models={models}
                      openID={openCapabilityID}
                      setOpenID={setOpenCapabilityID}
                      variant="icon"
                    />
                  </div>
                </div>

                <div className="field">
                  <label htmlFor="image-model">Default Image Model</label>
                  <div className="model-inline-control">
                    <select id="image-model" value={imageModel} onChange={(event) => setImageModel(event.target.value)}>
                      {imageModelOptions.map((name) => <option key={name}>{name}</option>)}
                    </select>
                    <ModelCapabilityLink
                      id="settings-image"
                      modelName={imageModel}
                      models={models}
                      openID={openCapabilityID}
                      setOpenID={setOpenCapabilityID}
                      variant="icon"
                    />
                  </div>
                </div>
              </section>

              <section className="settings-section three-column">
                <div className="field">
                  <label htmlFor="image-width">Image Width</label>
                  <input
                    id="image-width"
                    type="number"
                    min="1"
                    step="1"
                    value={imageWidth}
                    onChange={(event) => setImageWidth(positiveIntOrDefault(event.target.value, defaultImageWidth))}
                  />
                </div>

                <div className="field">
                  <label htmlFor="image-height">Image Height</label>
                  <input
                    id="image-height"
                    type="number"
                    min="1"
                    step="1"
                    value={imageHeight}
                    onChange={(event) => setImageHeight(positiveIntOrDefault(event.target.value, defaultImageHeight))}
                  />
                </div>

                <div className="field">
                  <label htmlFor="image-steps">Image Steps</label>
                  <input
                    id="image-steps"
                    type="number"
                    min="1"
                    step="1"
                    value={imageSteps}
                    onChange={(event) => setImageSteps(positiveIntOrDefault(event.target.value, defaultImageSteps))}
                  />
                </div>
              </section>

              <section className="settings-section">
                <div className="field">
                  <label htmlFor="system">System</label>
                  <textarea id="system" value={system} onChange={(event) => setSystem(event.target.value)} />
                </div>
              </section>
            </div>
          </>
        ) : (
          <>
            <div className="toolbar">
              <div className="toolbar-left">
                <div className="model-count">{asArray(models).length} local models</div>
                <button
                  className={`refresh-icon${refreshing ? ' spinning' : ''}`}
                  onClick={() => refreshOllama()}
                  disabled={refreshing}
                  aria-label="Refresh models"
                  title="Refresh models"
                >
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                    <path d="M21 12a9 9 0 0 1-9 9 9 9 0 0 1-6.7-3" />
                    <path d="M3 12a9 9 0 0 1 9-9 9 9 0 0 1 6.7 3" />
                    <polyline points="21 4 21 9 16 9" />
                    <polyline points="3 20 3 15 8 15" />
                  </svg>
                </button>
              </div>
            </div>

            <div className="chat-panel">
              <div
                className="transcript"
                ref={transcriptRef}
                onScroll={(event) => {
                  shouldFollowTranscriptRef.current = isNearScrollBottom(event.currentTarget);
                }}
              >
                {visibleHarnessRun ? <HarnessRunPanel run={visibleHarnessRun} /> : null}
                {asArray(chat).length === 0 ? (
                  <div className="empty-state">
                    <h2>Ask a model, attach an image, or stream a long answer.</h2>
                    <p>Atelier talks to Ollama directly through the local API.</p>
                  </div>
                ) : asArray(chat).map((entry) => {
                  const thinkingCollapsed = Boolean(entry.thinking && (collapsedThinkingIDs[entry.id] ?? !entry.streaming));
                  return (
                    <article key={entry.id} className={`message ${entry.role}`}>
                      <div className="message-meta">
                        {entry.role}{entry.streaming ? ' streaming' : ''}
                        {entry.provider ? <span className="turn-provider-badge">{entry.provider}</span> : null}
                      </div>
                      {entry.images?.length ? (
                        entry.role === 'assistant' ? (
                          <div className="chat-image-results">
                            {entry.images.map((image, index) => (
                              <figure key={`${entry.id}-image-${index}`} className="chat-image-card">
                                <button
                                  className="chat-image-preview"
                                  type="button"
                                  aria-label={`Open generated image ${index + 1}`}
                                  onClick={() => setPreviewImage(image)}
                                >
                                  <img src={image} alt="Generated result" />
                                </button>
                                <figcaption>
                                  <button type="button" onClick={() => saveGeneratedImage(image, index)}>Download image</button>
                                </figcaption>
                              </figure>
                            ))}
                          </div>
                        ) : (
                          <div className="thumb-row">
                            {entry.images.map((image, index) => (
                              <button
                                key={`${entry.id}-image-${index}`}
                                className="thumb-button"
                                type="button"
                                aria-label={`Open attached image ${index + 1}`}
                                onClick={() => setPreviewImage(image)}
                              >
                                <img src={image} alt="" />
                              </button>
                            ))}
                          </div>
                        )
                      ) : null}
                      {entry.thinking ? (
                        <div className="thinking-panel">
                          <button
                            className="thinking-toggle"
                            type="button"
                            aria-expanded={!thinkingCollapsed}
                            onClick={() => toggleThinkingCollapsed(entry.id)}
                          >
                            {thinkingCollapsed ? 'Show thinking' : 'Hide thinking'}
                          </button>
                          {thinkingCollapsed ? null : (
                            <div className="thinking markdown-body">
                              <ReactMarkdown remarkPlugins={[remarkGfm]}>
                                {entry.thinking}
                              </ReactMarkdown>
                            </div>
                          )}
                        </div>
                      ) : null}
                      {entry.role === 'assistant' || entry.role === 'system' ? (
                        <div className="markdown-body">
                          <ReactMarkdown remarkPlugins={[remarkGfm]}>
                            {entry.content || (entry.streaming ? '...' : '')}
                          </ReactMarkdown>
                        </div>
                      ) : (
                        <p>{entry.content || (entry.streaming ? '...' : '')}</p>
                      )}
                      {entry.role === 'assistant' && entry.content ? (
                        <div className="message-actions">
                          <button
                            className="message-copy-button"
                            type="button"
                            aria-label="Copy agent response"
                            title={copiedMessageID === entry.id ? 'Copied' : 'Copy response'}
                            onClick={() => copyAgentResponse(entry)}
                          >
                            {copiedMessageID === entry.id ? '✓' : '⧉'}
                          </button>
                        </div>
                      ) : null}
                      {entry.error ? <div className="error">{entry.error}</div> : null}
                    </article>
                  );
                })}
              </div>

              <div className="composer">
                {asArray(attachments).length ? (
                  <div className="attachment-strip">
                    {asArray(attachments).map((item) => (
                      <button key={item.name} onClick={() => setAttachments((items) => items.filter((next) => next.name !== item.name))}>
                        <img src={item.src} alt="" />
                        <span>{item.name}</span>
                      </button>
                    ))}
                  </div>
                ) : null}
                <textarea
                  ref={chatPromptRef}
                  value={prompt}
                  onChange={(event) => setPrompt(event.target.value)}
                  onKeyDown={handleChatPromptKeyDown}
                  placeholder="Prompt Atelier..."
                />
                <div className="composer-actions">
                  <label className="file-button">
                    Attach image
                    <input type="file" accept="image/*" multiple onChange={(event) => addImages(event.target.files)} />
                  </label>
                  <div className="composer-submit-row">
                    <div className="composer-model-switch">
                      <label className="model-inline" htmlFor="primary-provider">
                        <span>Provider</span>
                        <div className="model-inline-control">
                          <select
                            id="primary-provider"
                            aria-label="Provider for next message"
                            value={primaryProvider}
                            onChange={(event) => setPrimaryProvider(event.target.value as 'ollama' | 'openrouter')}
                          >
                            <option value="ollama">Ollama</option>
                            <option value="openrouter">OpenRouter</option>
                          </select>
                        </div>
                      </label>
                      <label className="model-inline" htmlFor="primary-model">
                        <span>Model</span>
                        <div className="model-inline-control">
                          <input
                            id="primary-model"
                            type="text"
                            list="primary-model-options"
                            aria-label="Model for next message"
                            placeholder="Type to filter models..."
                            autoComplete="off"
                            value={model}
                            onChange={(event) => setModel(event.target.value)}
                          />
                          <datalist id="primary-model-options">
                            {primaryModelOptions.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
                          </datalist>
                          {primaryProvider === 'ollama' ? (
                            <ModelCapabilityLink
                              id="primary-model"
                              modelName={model}
                              models={models}
                              openID={openCapabilityID}
                              setOpenID={setOpenCapabilityID}
                              variant="icon"
                            />
                          ) : null}
                        </div>
                      </label>
                    </div>
                    {activeStream ? (
                      <button className="danger" onClick={stopChat}>Stop</button>
                    ) : (
                      <button className="primary" onClick={submitChat} disabled={!prompt.trim() || !model || !primaryModelIsValid}>Send</button>
                    )}
                  </div>
                </div>
              </div>
            </div>
          </>
        )}
      </section>
      {previewImage ? (
        <div className="image-preview-overlay" role="presentation" onClick={() => setPreviewImage('')}>
          <div
            className="image-preview-dialog"
            role="dialog"
            aria-modal="true"
            aria-label="Attached image preview"
            onClick={(event) => event.stopPropagation()}
          >
            <button className="image-preview-close" type="button" aria-label="Close image preview" onClick={() => setPreviewImage('')}>
              ×
            </button>
            <button className="image-preview-download" type="button" aria-label="Download image" title="Download" onClick={() => saveGeneratedImage(previewImage, 0)}>
              ↓
            </button>
            <img src={previewImage} alt="Attached preview" />
          </div>
        </div>
      ) : null}
    </main>
  );
}

function loadSidebarWidth(): number {
  const stored = Number(window.localStorage.getItem('atelier.sidebarWidth'));
  return clampSidebarWidth(Number.isFinite(stored) && stored > 0 ? stored : defaultSidebarWidth);
}

function clampSidebarWidth(width: number, max = maxSidebarWidth): number {
  return Math.round(Math.max(minSidebarWidth, Math.min(Math.max(minSidebarWidth, max), width)));
}

function ModelCapabilityLink({
  id,
  modelName,
  models,
  openID,
  setOpenID,
  variant = 'text',
}: {
  id: string;
  modelName: string;
  models: main.OllamaModel[];
  openID: string;
  setOpenID: (id: string) => void;
  variant?: 'text' | 'icon';
}) {
  const selectedModel = asArray(models).find((item) => item.name === modelName);
  const capabilityLabels = selectedModel ? modelCapabilityLabels(selectedModel) : [];
  const isOpen = openID === id;
  const panelID = `${id}-capability-panel`;
  const isIcon = variant === 'icon';
  return (
    <div className={isIcon ? 'model-capability model-capability--icon' : 'model-capability'}>
      <button
        type="button"
        className="model-capability-link"
        aria-expanded={isOpen}
        aria-controls={panelID}
        aria-label={isIcon ? 'Model capability' : undefined}
        title={isIcon ? 'Model capability' : undefined}
        onClick={() => setOpenID(isOpen ? '' : id)}
      >
        {isIcon ? (
          <svg viewBox="0 0 16 16" width="16" height="16" fill="none" aria-hidden="true">
            <circle cx="8" cy="8" r="7" stroke="currentColor" strokeWidth="1.4" />
            <circle cx="8" cy="4.6" r="0.95" fill="currentColor" />
            <path d="M8 7v4.4" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
          </svg>
        ) : (
          'Capability'
        )}
      </button>
      {isOpen ? (
        <div id={panelID} className="model-capability-panel" role="dialog" aria-label={`${modelName || 'Selected model'} capabilities`}>
          <button
            type="button"
            className="model-capability-close"
            aria-label="Close capabilities"
            onClick={() => setOpenID('')}
          >
            ×
          </button>
          <div className="model-capability-title">{modelName || 'No model selected'}</div>
          {selectedModel ? (
            <>
              <div className="capability-chips">
                {capabilityLabels.length ? capabilityLabels.map((capability) => (
                  <span key={capability}>{capability}</span>
                )) : <span>Capabilities not reported</span>}
              </div>
              <dl>
                {selectedModel.family ? (
                  <>
                    <dt>Family</dt>
                    <dd>{selectedModel.family}</dd>
                  </>
                ) : null}
                {selectedModel.parameter ? (
                  <>
                    <dt>Parameters</dt>
                    <dd>{selectedModel.parameter}</dd>
                  </>
                ) : null}
                {selectedModel.size ? (
                  <>
                    <dt>Size</dt>
                    <dd>{formatModelSize(selectedModel.size)}</dd>
                  </>
                ) : null}
              </dl>
            </>
          ) : (
            <p>This model is not in the current Ollama model list.</p>
          )}
        </div>
      ) : null}
    </div>
  );
}

function formatCapability(capability: string): string {
  return capability
    .replace(/[-_]+/g, ' ')
    .replace(/\b\w/g, (letter) => letter.toUpperCase());
}

function modelCapabilityLabels(model: main.OllamaModel): string[] {
  const labels = new Set<string>();
  for (const capability of asArray(model.capabilities)) {
    const normalized = capability.toLowerCase().replace(/_/g, '-').trim();
    if (normalized === 'image' || normalized === 'images' || normalized === 'image-generation') {
      labels.add('Image generation');
      continue;
    }
    labels.add(formatCapability(capability));
  }
  if (model.imageGeneration) {
    labels.add('Image generation');
  }
  return Array.from(labels);
}

function formatModelSize(size: number): string {
  if (!Number.isFinite(size) || size <= 0) {
    return '';
  }
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let value = size;
  let unitIndex = 0;
  while (value >= 1024 && unitIndex < units.length - 1) {
    value /= 1024;
    unitIndex += 1;
  }
  return `${value >= 10 || unitIndex === 0 ? value.toFixed(0) : value.toFixed(1)} ${units[unitIndex]}`;
}

function HarnessRunPanel({run}: {run: HarnessRunView}) {
  const steps = asArray(run.steps);
  const completed = steps.filter((step) => step.status === 'completed').length;
  const status = run.status ?? 'running';
  const stopReason = run.loop?.stopReason;
  return (
    <details className="harness-panel">
      <summary>
        <span>Harness</span>
        <strong>{status}</strong>
        <small>{completed}/{steps.length || 6} steps{run.durationMs ? ` · ${formatDuration(run.durationMs)}` : ''}</small>
      </summary>
      <div className="harness-meta">
        {run.loop?.iterations ? <span>{run.loop.iterations} iteration{run.loop.iterations === 1 ? '' : 's'}</span> : null}
        {stopReason ? <span>stop: {stopReason}</span> : null}
        {run.requestId ? <span>{run.requestId}</span> : null}
      </div>
      <ol className="harness-steps">
        {steps.map((step, index) => {
          const lane = harnessStepLane(step);
          return (
          <li key={step.id ?? `${step.kind}-${index}`} className={`harness-step ${step.status ?? 'pending'} ${lane.className}`}>
            <div className="harness-step-head">
              <div>
                <strong>{formatStepKind(step.kind)}</strong>
                <em>{lane.label}</em>
              </div>
              <span>{step.status ?? 'pending'}</span>
            </div>
            <p>{step.error || step.summary || step.decision || step.doneReason || step.model || ''}</p>
            <small className="harness-step-meta">
              {step.provider ? <span>{step.provider}</span> : null}
              {step.model ? <span>{step.model}</span> : null}
              {step.tokens ? <span>{step.tokens} tokens</span> : null}
              {step.durationMs ? <span>{formatDuration(step.durationMs)}</span> : null}
            </small>
            {asArray(step.tools).length ? (
              <div className="harness-tool-list">
                {asArray(step.tools).map((tool, toolIndex) => (
                  <div className={`harness-tool ${tool.status ?? 'pending'}`} key={`${tool.name}-${toolIndex}`}>
                    <div>
                      <strong>{formatToolName(tool.name)}</strong>
                      <span>{tool.status ?? 'pending'}{typeof tool.exitCode === 'number' ? ` · exit ${tool.exitCode}` : ''}{tool.durationMs ? ` · ${formatDuration(tool.durationMs)}` : ''}</span>
                    </div>
                    {tool.command?.length ? <code>{tool.command.join(' ')}</code> : null}
                    {tool.path ? <small>{shortenHomePath(tool.path)}</small> : null}
                    {tool.stdoutPreview ? <pre><strong>stdout</strong>{'\n'}{tool.stdoutPreview}</pre> : null}
                    {tool.stderrPreview ? <pre><strong>stderr</strong>{'\n'}{tool.stderrPreview}</pre> : null}
                    {tool.error ? <p>{tool.error}</p> : null}
                  </div>
                ))}
              </div>
            ) : null}
          </li>
        )})}
      </ol>
    </details>
  );
}

function parseHarnessRun(value: unknown): HarnessRunView | undefined {
  if (!value || typeof value !== 'object') {
    return undefined;
  }
  const run = value as HarnessRunView;
  return run.status || run.steps?.length ? run : undefined;
}

function buildRunningHarnessRun(requestID: string, conversationID: string, primaryModel: string): HarnessRunView {
  return {
    mode: 'chat',
    status: 'running',
    requestId: requestID,
    conversationId: conversationID,
    loop: {
      maxSteps: 3,
      iterations: 1,
    },
    steps: [
      {kind: 'queued', status: 'completed', summary: 'turn accepted by harness'},
      {kind: 'triage', status: 'completed', provider: 'ollama', model: primaryModel, summary: 'primary model triaged the turn'},
      {kind: 'model_call', status: 'completed', provider: 'ollama', model: primaryModel, summary: 'primary model stream opened'},
      {kind: 'streaming', status: 'running', provider: 'ollama', model: primaryModel, summary: 'primary model response streaming to UI'},
      {kind: 'evaluation', status: 'pending'},
      {kind: 'saved', status: 'pending'},
    ],
  };
}

function harnessStepLane(step: HarnessStepView): {label: string; className: string} {
  switch (step.kind) {
    case 'triage':
      return {label: 'Chat model', className: 'harness-lane-chat'};
    case 'planning':
      return {label: 'Tool model', className: 'harness-lane-model'};
    case 'tool_call':
      return {label: 'Tools', className: 'harness-lane-tools'};
    case 'model_call':
    case 'streaming':
      return {label: 'Chat model', className: 'harness-lane-chat'};
    case 'evaluation':
    case 'saved':
      return {label: 'Harness bookkeeping', className: 'harness-lane-bookkeeping'};
    default:
      return {label: 'Harness', className: 'harness-lane-system'};
  }
}

function formatStepKind(kind = 'step'): string {
  return kind.replace(/_/g, ' ');
}

function formatToolName(name = 'tool'): string {
  return name.replace(/_/g, ' ');
}

function formatDuration(durationMs: number): string {
  if (durationMs < 1000) {
    return `${durationMs}ms`;
  }
  return `${(durationMs / 1000).toFixed(1)}s`;
}

function asArray<T>(value: T[] | null | undefined): T[] {
  return Array.isArray(value) ? value : [];
}

function isNearScrollBottom(element: HTMLElement, threshold = 48): boolean {
  return element.scrollHeight - element.scrollTop - element.clientHeight < threshold;
}

function historyText(contents: main.HistoryContent[] | null | undefined, type: string): string {
  return asArray(contents)
    .filter((content) => content.type === type)
    .map((content) => content.text ?? '')
    .filter(Boolean)
    .join('\n\n');
}

function historyImages(contents: main.HistoryContent[] | null | undefined): string[] {
  return asArray(contents)
    .filter((content) => content.type === 'image')
    .map((content) => content.text || content.path || '')
    .filter(Boolean);
}

function imagePayloadForOllama(image: string): string {
  return image.replace(/^data:image\/[a-z+.-]+;base64,/, '');
}

function formatError(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function formatOpenRouterError(error: unknown): string {
  const message = formatError(error);
  const lower = message.toLowerCase();
  if (lower.includes('authentication failed') || lower.includes('401') || lower.includes('unauthorized')) {
    return 'Invalid API key — check your OpenRouter key in Settings';
  }
  return message;
}

function copyTextWithTextarea(text: string) {
  const textarea = document.createElement('textarea');
  textarea.value = text;
  textarea.setAttribute('readonly', 'true');
  textarea.style.position = 'fixed';
  textarea.style.left = '-9999px';
  textarea.style.top = '0';
  document.body.appendChild(textarea);
  textarea.select();
  document.execCommand('copy');
  document.body.removeChild(textarea);
}

function toolPermissionTitle(permission: ToolPermissionEvent): string {
  if (permission.toolName === 'run_command') {
    return 'Run command?';
  }
  if (permission.toolName === 'write_file') {
    return 'Write file?';
  }
  return 'Allow tool action?';
}

function toolPermissionSummary(permission: ToolPermissionEvent): string {
  if (permission.toolName === 'run_command' && permission.command?.length) {
    return permission.command.slice(0, 2).join(' ');
  }
  if (permission.path) {
    return shortenHomePath(permission.path);
  }
  return permission.summary;
}

function hasToolPermissionDetails(permission: ToolPermissionEvent): boolean {
  return Boolean(
    permission.command?.length ||
    permission.path ||
    permission.contentPreview ||
    (permission.summary && permission.summary !== toolPermissionSummary(permission)),
  );
}

function shortenHomePath(path: string): string {
  const home = inferHomePath(path);
  if (!home || !path.startsWith(home)) {
    return path;
  }
  if (path.length === home.length) {
    return '~';
  }
  return `~${path.slice(home.length)}`;
}

function inferHomePath(path: string): string {
  return path.match(/^\/Users\/[^/]+/)?.[0] ?? path.match(/^\/home\/[^/]+/)?.[0] ?? '';
}

async function readImageFile(file: File): Promise<Attachment> {
  const dataURL = await new Promise<string>((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.onerror = () => reject(reader.error);
    reader.readAsDataURL(file);
  });

  return {
    name: file.name,
    src: dataURL,
    payload: imagePayloadForOllama(dataURL),
  };
}

export default App;
