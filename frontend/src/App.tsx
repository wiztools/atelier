import {useEffect, useMemo, useRef, useState} from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import './App.css';
import {
  CancelStream,
  CheckOllama,
  DeleteConversation,
  GetConversation,
  GetConfig,
  ListConversations,
  ListModels,
  PurgeArchivedConversations,
  SaveImage,
  SaveConfig,
  StartImageGeneration,
  StreamChat,
  UpdateConversationTitle,
} from '../wailsjs/go/main/App';
import {main} from '../wailsjs/go/models';
import {EventsOff, EventsOn} from '../wailsjs/runtime/runtime';

type Mode = 'chat' | 'image';
type View = 'app' | 'settings';
type ConversationKind = 'chat' | 'image_generation';

type ChatEntry = {
  id: string;
  role: 'user' | 'assistant' | 'system';
  content: string;
  thinking?: string;
  images?: string[];
  harnessRun?: HarnessRunView;
  streaming?: boolean;
  error?: string;
};

type ChatChunk = {
  requestID: string;
  content?: string;
  thinking?: string;
  images?: string[];
  done: boolean;
  error?: string;
  model?: string;
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
};

type ImageGenerationEvent = {
  requestID: string;
  done: boolean;
  model?: string;
  text?: string;
  images?: string[];
  raw?: string;
  error?: string;
  conversationId?: string;
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
};

type Attachment = {
  name: string;
  src: string;
  payload: string;
};

type RatioPreset = {
  id: string;
  label: string;
  width: number;
  height: number;
};

type PixelPreset = {
  id: string;
  label: string;
  megapixels: number;
};

const defaultBaseURL = 'http://localhost:11434';
const defaultSidebarWidth = 320;
const minSidebarWidth = 240;
const maxSidebarWidth = 560;
const defaultImageRatio = '1:1';
const defaultImagePixels = '0.6';
const compactHistoryLimit = 10;
const expandedHistoryBatchSize = 20;
const ratioPresets: RatioPreset[] = [
  {id: '1:1', label: '1:1 Square', width: 1, height: 1},
  {id: '16:9', label: '16:9 Landscape', width: 16, height: 9},
  {id: '9:16', label: '9:16 Portrait', width: 9, height: 16},
  {id: '4:3', label: '4:3 Landscape', width: 4, height: 3},
  {id: '3:4', label: '3:4 Portrait', width: 3, height: 4},
];
const pixelPresets: PixelPreset[] = [
  {id: '0.6', label: '0.6 MP', megapixels: 0.6},
  {id: '1', label: '1 MP', megapixels: 1},
  {id: '2', label: '2 MP', megapixels: 2},
  {id: '4', label: '4 MP', megapixels: 4},
];

function App() {
  const [baseURL, setBaseURL] = useState(defaultBaseURL);
  const [status, setStatus] = useState<main.OllamaStatus | null>(null);
  const [models, setModels] = useState<main.OllamaModel[]>([]);
  const [model, setModel] = useState('');
  const [harnessModel, setHarnessModel] = useState('');
  const [imageModel, setImageModel] = useState('');
  const [mode, setMode] = useState<Mode>('chat');
  const [system, setSystem] = useState('You are Atelier, a precise local AI collaborator.');
  const [prompt, setPrompt] = useState('');
  const [attachments, setAttachments] = useState<Attachment[]>([]);
  const [chat, setChat] = useState<ChatEntry[]>([]);
  const [conversations, setConversations] = useState<main.ConversationSummary[]>([]);
  const [historyExpanded, setHistoryExpanded] = useState(false);
  const [visibleHistoryCount, setVisibleHistoryCount] = useState(compactHistoryLimit);
  const [activeConversationID, setActiveConversationID] = useState('');
  const [activeStream, setActiveStream] = useState<string | null>(null);
  const [inFlightConversations, setInFlightConversations] = useState<Record<string, InFlightConversation>>({});
  const [imagePrompt, setImagePrompt] = useState('');
  const [imageRatio, setImageRatio] = useState(defaultImageRatio);
  const [imagePixels, setImagePixels] = useState(defaultImagePixels);
  const [imageSteps, setImageSteps] = useState(24);
  const [imageResult, setImageResult] = useState<main.ImageGenerateResponse | null>(null);
  const [imageError, setImageError] = useState('');
  const [imageSaveStatus, setImageSaveStatus] = useState('');
  const [imageBusy, setImageBusy] = useState(false);
  const [configLoaded, setConfigLoaded] = useState(false);
  const [storageConfig, setStorageConfig] = useState<main.ConfigStorage | null>(null);
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
  const shellRef = useRef<HTMLElement | null>(null);
  const transcriptRef = useRef<HTMLDivElement | null>(null);
  const shouldFollowTranscriptRef = useRef(true);
  const visibleStreamRef = useRef<string | null>(null);
  const visibleImageRequestRef = useRef<string | null>(null);
  const inFlightConversationsRef = useRef<Record<string, InFlightConversation>>({});
  const requestConversationRef = useRef<Record<string, {conversationID: string; kind: ConversationKind}>>({});
  const chatStreamDraftsRef = useRef<Record<string, ChatStreamDraft>>({});
  const chatPromptRef = useRef<HTMLTextAreaElement | null>(null);
  const imagePromptRef = useRef<HTMLTextAreaElement | null>(null);

  const assistantEntryID = activeStream ? `assistant-${activeStream}` : '';
  const generatedImages = asArray(imageResult?.images);
  const imageDimensions = useMemo(() => computeImageDimensions(imageRatio, imagePixels), [imagePixels, imageRatio]);
  const conversationList = asArray(conversations);
  const visibleConversations = historyExpanded
    ? conversationList.slice(0, visibleHistoryCount)
    : conversationList.slice(0, compactHistoryLimit);
  const hasMoreConversations = visibleConversations.length < conversationList.length;
  const selectedConversationID = mode === 'image' ? imageResult?.conversationId ?? '' : activeConversationID;
  const latestHarnessRun = [...chat].reverse().find((entry) => entry.role === 'assistant' && entry.harnessRun)?.harnessRun;
  const visibleHarnessRun = latestHarnessRun ?? (activeStream ? buildRunningHarnessRun(activeStream, activeConversationID, harnessModel || model) : null);

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
              chat: model,
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
            width: imageDimensions.width,
            height: imageDimensions.height,
            steps: imageSteps,
          },
        },
        ui: {
          mode,
        },
      })).catch((error) => {
        setStatus((current) => current ? {...current, error: String(error)} : current);
      });
    }, 400);
    return () => window.clearTimeout(timeout);
  }, [baseURL, configLoaded, harnessModel, imageDimensions.height, imageDimensions.width, imageModel, imageSteps, mode, model, storageConfig, system]);

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
    EventsOn('ollama:chat:chunk', onChunk);
    return () => EventsOff('ollama:chat:chunk');
  }, []);

  useEffect(() => {
    const onImageResult = (event: ImageGenerationEvent) => {
      const isVisibleRequest = visibleImageRequestRef.current === event.requestID;
      if (event.conversationId) {
        markConversationInFlight(event.conversationId, event.requestID, 'image_generation');
        void refreshConversations();
      }
      if (event.done || event.error) {
        clearConversationInFlight(event.requestID);
      }
      if (!isVisibleRequest) {
        if (event.done || event.error) {
          void refreshConversations();
        }
        return;
      }
      if (event.conversationId) {
        setImageResult((current) => ({
          ...(current ?? main.ImageGenerateResponse.createFrom({images: []})),
          conversationId: event.conversationId,
          images: asArray(current?.images),
        }));
      }
      if (event.done || event.error) {
        visibleImageRequestRef.current = null;
        setImageBusy(false);
      }
      if (event.error) {
        setImageError(event.error);
        return;
      }
      if (event.done) {
        const nextImages = asArray(event.images);
        setImageResult(main.ImageGenerateResponse.createFrom({
          model: event.model,
          text: event.text,
          images: nextImages,
          raw: event.raw,
          conversationId: event.conversationId,
        }));
        if (!nextImages.length && !event.text) {
          setImageError('Ollama returned a response, but Atelier did not find image data in it.');
        }
      }
    };
    EventsOn('ollama:image:result', onImageResult);
    return () => EventsOff('ollama:image:result');
  }, []);

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

  const modelOptions = useMemo(() => {
    return Array.from(new Set([...asArray(models).map((item) => item.name), model, harnessModel, imageModel].filter(Boolean)));
  }, [harnessModel, imageModel, model, models]);
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

  async function loadConfig() {
    const config = await GetConfig();
    const nextBaseURL = config.providers?.ollama?.baseURL || defaultBaseURL;
    const nextChatModel = config.providers?.ollama?.models?.chat ?? '';
    const nextHarnessModel = config.providers?.ollama?.models?.harness || nextChatModel;
    const nextImageModel = config.providers?.ollama?.models?.image ?? '';
    const nextSystem = config.prompts?.system || 'You are Atelier, a precise local AI collaborator.';
    const nextImageWidth = config.generation?.image?.width || 768;
    const nextImageHeight = config.generation?.image?.height || nextImageWidth;
    const nextImageSteps = config.generation?.image?.steps || 24;
    const nextImagePreset = inferImagePreset(nextImageWidth, nextImageHeight);

    setStartupError('');
    setStorageConfig(config.storage ?? null);
    setBaseURL(nextBaseURL);
    setModel(nextChatModel);
    setHarnessModel(nextHarnessModel);
    setImageModel(nextImageModel);
    setSystem(nextSystem);
    setImageRatio(nextImagePreset.ratio);
    setImagePixels(nextImagePreset.pixels);
    setImageSteps(nextImageSteps);
    setMode(config.ui?.mode === 'image' ? 'image' : 'chat');
    setConfigLoaded(true);
    await Promise.all([
      refreshConversations(),
      refreshOllama(nextBaseURL),
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

  function handleHistoryScroll(event: React.UIEvent<HTMLDivElement>) {
    if (!historyExpanded || !hasMoreConversations || !isNearScrollBottom(event.currentTarget, 96)) {
      return;
    }
    setVisibleHistoryCount((current) => Math.min(current + expandedHistoryBatchSize, conversationList.length));
  }

  async function refreshOllama(endpoint = baseURL) {
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
  }

  async function resetWorkspace(nextMode: Mode) {
    visibleStreamRef.current = null;
    visibleImageRequestRef.current = null;
    setActiveStream(null);
    setImageBusy(false);
    setChat([]);
    setPrompt('');
    setAttachments([]);
    setActiveConversationID('');
    setImageResult(null);
    setImageError('');
    setImageSaveStatus('');
    setImagePrompt('');
    setView('app');
    setMode(nextMode);
    window.setTimeout(() => {
      if (nextMode === 'image') {
        imagePromptRef.current?.focus();
      } else {
        chatPromptRef.current?.focus();
      }
    }, 0);
  }

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.repeat || event.altKey || event.shiftKey) {
        return;
      }
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'n') {
        event.preventDefault();
        void resetWorkspace('chat');
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [activeStream]);

  async function startNewChat() {
    await resetWorkspace('chat');
  }

  function startNewImage() {
    visibleImageRequestRef.current = null;
    setImageResult(null);
    setImageError('');
    setImageSaveStatus('');
    setImageBusy(false);
    setImagePrompt('');
    setView('app');
    setMode('image');
    window.setTimeout(() => {
      imagePromptRef.current?.focus();
    }, 0);
  }

  async function openConversationSummary(conversation: main.ConversationSummary) {
    try {
      const detail = await GetConversation(conversation.id);
      setView('app');
      if (detail.conversation.kind === 'image_generation') {
        setMode('image');
        hydrateImageConversation(detail);
      } else {
        setMode('chat');
        hydrateChatConversation(detail);
      }
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
      });
    }
    setChat(entries);
    setActiveConversationID(detail.conversation.id);
    setPrompt('');
    setAttachments([]);
    visibleImageRequestRef.current = null;
    setImageBusy(false);
    setImageResult(null);
    setImageError('');
    setImageSaveStatus('');
  }

  function hydrateImageConversation(detail: main.ConversationDetail) {
    const inFlight = inFlightConversationsRef.current[detail.conversation.id];
    const visibleRequestID = inFlight?.kind === 'image_generation' ? inFlight.requestID : null;
    const userTurn = asArray(detail.turns).find((turn) => turn.role === 'user');
    const assistantTurn = asArray(detail.turns).find((turn) => turn.role === 'assistant');
    const images = historyImages(assistantTurn?.content);
    visibleImageRequestRef.current = visibleRequestID;
    setImageBusy(Boolean(visibleRequestID));
    setImagePrompt(historyText(userTurn?.content, 'text'));
    setImageResult(main.ImageGenerateResponse.createFrom({
      model: assistantTurn?.model ?? detail.conversation.defaults?.imageModel,
      images,
      conversationId: detail.conversation.id,
    }));
    setImageError('');
    setImageSaveStatus('');
    visibleStreamRef.current = null;
    setActiveStream(null);
    setChat([]);
    setActiveConversationID('');
    setPrompt('');
    setAttachments([]);
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
    if (!trimmed || !model || activeStream) {
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
      {id: `assistant-${requestID}`, role: 'assistant', content: '', streaming: true},
    ]);

    try {
      const start = await StreamChat(main.ChatRequest.createFrom({
        requestID,
        conversationId: activeConversationID || undefined,
        baseURL,
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

  function handleImagePromptKeyDown(event: React.KeyboardEvent<HTMLTextAreaElement>) {
    if (event.key === 'Enter' && !event.shiftKey) {
      event.preventDefault();
      void generateImage();
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

  async function addImages(files: FileList | null) {
    if (!files) {
      return;
    }
    const next = await Promise.all(Array.from(files).map(readImageFile));
    setAttachments((items) => [...items, ...next]);
  }

  async function generateImage() {
    if (!imageModel || !imagePrompt.trim() || imageBusy) {
      return;
    }
    const requestID = `image-${Date.now()}-${Math.random().toString(36).slice(2)}`;
    setImageBusy(true);
    setImageError('');
    setImageSaveStatus('');
    setImageResult(null);
    visibleImageRequestRef.current = requestID;
    try {
      await StartImageGeneration(main.ImageGenerateRequest.createFrom({
        requestID,
        baseURL,
        model: imageModel,
        prompt: imagePrompt.trim(),
        width: imageDimensions.width,
        height: imageDimensions.height,
        steps: imageSteps,
      }));
    } catch (error) {
      visibleImageRequestRef.current = null;
      setImageError(error instanceof Error ? error.message : String(error));
      setImageBusy(false);
    }
  }

  async function saveGeneratedImage(image: string, index: number) {
    setImageSaveStatus('');
    setImageError('');
    try {
      const path = await SaveImage(main.SaveImageRequest.createFrom({
        image,
        suggestedName: `atelier-${Date.now()}-${index + 1}`,
      }));
      if (path) {
        setImageSaveStatus(`Saved to ${path}`);
      }
    } catch (error) {
      setImageError(error instanceof Error ? error.message : String(error));
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
                Chat
              </button>
              <button onClick={startNewImage}>
                <span className="nav-icon">+</span>
                Image
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
                          <button className="history-open" onClick={() => openConversationSummary(conversation)}>
                            <span>{conversation.title}</span>
                            <small
                              className={`history-kind${inFlight ? ' in-flight' : ''}`}
                              title={inFlight ? 'Running' : conversation.kind === 'image_generation' ? 'Image' : 'Chat'}
                              aria-label={inFlight ? 'Conversation running' : conversation.kind === 'image_generation' ? 'Image conversation' : 'Chat conversation'}
                            >
                              {inFlight ? <span className="history-spinner" /> : conversation.kind === 'image_generation' ? '▧' : '◌'}
                            </small>
                          </button>
                          <div className="history-actions">
                            <button className="history-icon-button" aria-label={`Edit ${conversation.title}`} title="Edit" onClick={() => startEditingConversationTitle(conversation)}>
                              ✎
                            </button>
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
        {view === 'settings' ? (
          <>
            <div className="toolbar">
              <button className="back-button" onClick={() => setView('app')}>← Back</button>
              <div className="model-count">Saved to ~/.atelier/config.json</div>
            </div>
            <div className="settings-screen">
              <div className="settings-header">
                <h2>Settings</h2>
                <p>Ollama provider, harness defaults, and prompt preferences.</p>
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
                <h3>Storage</h3>
                <div className="storage-list">
                  <div>
                    <span>Root</span>
                    <code>{storageConfig?.root ?? '~/.atelier'}</code>
                  </div>
                  <div>
                    <span>History</span>
                    <code>{storageConfig?.history ?? '~/.atelier/history'}</code>
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
                  <select id="harness-model" value={harnessModel} onChange={(event) => setHarnessModel(event.target.value)}>
                    {modelOptions.map((name) => <option key={name}>{name}</option>)}
                  </select>
                </div>

                <div className="field">
                  <label htmlFor="image-model">Default Image Model</label>
                  <select id="image-model" value={imageModel} onChange={(event) => setImageModel(event.target.value)}>
                    {imageModelOptions.map((name) => <option key={name}>{name}</option>)}
                  </select>
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
              <div className="segmented" role="tablist">
                <button className={mode === 'chat' ? 'active' : ''} onClick={() => setMode('chat')}>Chat</button>
                <button className={mode === 'image' ? 'active' : ''} onClick={() => setMode('image')}>Image</button>
              </div>
              <div className="model-count">{asArray(models).length} local models</div>
            </div>

            {mode === 'chat' ? (
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
              ) : asArray(chat).map((entry) => (
                <article key={entry.id} className={`message ${entry.role}`}>
                  <div className="message-meta">{entry.role}{entry.streaming ? ' streaming' : ''}</div>
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
                  {entry.thinking ? <pre className="thinking">{entry.thinking}</pre> : null}
                  {entry.role === 'assistant' || entry.role === 'system' ? (
                    <div className="markdown-body">
                      <ReactMarkdown remarkPlugins={[remarkGfm]}>
                        {entry.content || (entry.streaming ? '...' : '')}
                      </ReactMarkdown>
                    </div>
                  ) : (
                    <p>{entry.content || (entry.streaming ? '...' : '')}</p>
                  )}
                  {entry.error ? <div className="error">{entry.error}</div> : null}
                </article>
              ))}
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
                  <label className="model-inline" htmlFor="chat-model">
                    <span>Model</span>
                    <select id="chat-model" value={model} onChange={(event) => setModel(event.target.value)}>
                      {modelOptions.map((name) => <option key={name}>{name}</option>)}
                    </select>
                  </label>
                  {activeStream ? (
                    <button className="danger" onClick={stopChat}>Stop</button>
                  ) : (
                    <button className="primary" onClick={submitChat} disabled={!prompt.trim() || !model}>Send</button>
                  )}
                </div>
              </div>
            </div>
              </div>
            ) : (
              <div className="image-panel">
            <div className="image-controls">
              <label htmlFor="image-prompt">Prompt</label>
              <textarea
                ref={imagePromptRef}
                id="image-prompt"
                value={imagePrompt}
                onChange={(event) => setImagePrompt(event.target.value)}
                onKeyDown={handleImagePromptKeyDown}
                placeholder="Prompt Atelier..."
              />
              <div className="inline-fields">
                <label>
                  Ratio
                  <select value={imageRatio} onChange={(event) => setImageRatio(event.target.value)}>
                    {ratioPresets.map((preset) => <option key={preset.id} value={preset.id}>{preset.label}</option>)}
                  </select>
                </label>
                <label>
                  Pixels
                  <select value={imagePixels} onChange={(event) => setImagePixels(event.target.value)}>
                    {pixelPresets.map((preset) => <option key={preset.id} value={preset.id}>{preset.label}</option>)}
                  </select>
                </label>
                <label>
                  Steps
                  <input type="number" min={1} max={80} value={imageSteps} onChange={(event) => setImageSteps(Number(event.target.value))} />
                </label>
              </div>
              <div className="dimension-note">{imageDimensions.width} x {imageDimensions.height}</div>
              <div className="image-generate-row">
                <label className="model-inline" htmlFor="image-tab-model">
                  <select id="image-tab-model" value={imageModel} onChange={(event) => setImageModel(event.target.value)}>
                    {imageModelOptions.map((name) => <option key={name}>{name}</option>)}
                  </select>
                </label>
                <button className="primary" onClick={generateImage} disabled={!imagePrompt.trim() || !imageModel || imageBusy}>
                  {imageBusy ? 'Generating' : 'Generate'}
                </button>
              </div>
            </div>
            <div className="image-output">
              {imageBusy ? (
                <div className="empty-state busy-state">
                  <div className="spinner" />
                  <h2>Generating image...</h2>
                  <p>Large local image models may take a minute on first load.</p>
                </div>
              ) : imageError ? (
                <div className="empty-state error-state">
                  <h2>Generation failed</h2>
                  <p>{imageError}</p>
                  {imageResult?.raw ? <pre>{summarizeRaw(imageResult.raw)}</pre> : null}
                </div>
              ) : generatedImages.length ? (
                <div className="generated-results">
                  {generatedImages.map((image: string, index: number) => (
                    <figure key={image} className="generated-image">
                      <img src={image} alt="Generated result" />
                      <figcaption>
                        <button className="primary" onClick={() => saveGeneratedImage(image, index)}>Save image</button>
                      </figcaption>
                    </figure>
                  ))}
                  {imageSaveStatus ? <div className="save-status">{imageSaveStatus}</div> : null}
                </div>
              ) : imageResult?.text ? (
                <div className="raw-output">
                  <h2>Ollama returned text</h2>
                  <pre>{imageResult.text}</pre>
                  {imageResult.raw ? <pre>{summarizeRaw(imageResult.raw)}</pre> : null}
                </div>
              ) : (
                <div className="empty-state">
                  <h2>Image generation lands here.</h2>
                  <p>Use an Ollama image-generation model; Atelier calls `/api/generate` directly.</p>
                </div>
              )}
            </div>
              </div>
            )}
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

function summarizeRaw(raw: string): string {
  return raw.length > 1200 ? `${raw.slice(0, 1200)}...` : raw;
}

function loadSidebarWidth(): number {
  const stored = Number(window.localStorage.getItem('atelier.sidebarWidth'));
  return clampSidebarWidth(Number.isFinite(stored) && stored > 0 ? stored : defaultSidebarWidth);
}

function clampSidebarWidth(width: number, max = maxSidebarWidth): number {
  return Math.round(Math.max(minSidebarWidth, Math.min(Math.max(minSidebarWidth, max), width)));
}

function computeImageDimensions(ratioID: string, pixelsID: string): {width: number; height: number} {
  const ratio = ratioPresets.find((preset) => preset.id === ratioID) ?? ratioPresets[0];
  const pixelPreset = pixelPresets.find((preset) => preset.id === pixelsID) ?? pixelPresets[0];
  const targetPixels = pixelPreset.megapixels * 1_000_000;
  const rawHeight = Math.sqrt(targetPixels * ratio.height / ratio.width);
  const rawWidth = rawHeight * ratio.width / ratio.height;
  let width = clampDimension(roundToMultiple(rawWidth, 16));
  let height = clampDimension(roundToMultiple(rawHeight, 16));
  while (width * height > targetPixels && width > 64 && height > 64) {
    width = clampDimension(width - 16);
    height = clampDimension(Math.round(width * ratio.height / ratio.width / 16) * 16);
  }
  return {width, height};
}

function inferImagePreset(width: number, height: number): {ratio: string; pixels: string} {
  const actualRatio = width / Math.max(height, 1);
  const ratio = ratioPresets.reduce((best, preset) => {
    const presetRatio = preset.width / preset.height;
    const bestRatio = best.width / best.height;
    return Math.abs(presetRatio - actualRatio) < Math.abs(bestRatio - actualRatio) ? preset : best;
  }, ratioPresets[0]);
  const megapixels = width * height / 1_000_000;
  const pixels = pixelPresets.reduce((best, preset) => {
    return Math.abs(preset.megapixels - megapixels) < Math.abs(best.megapixels - megapixels) ? preset : best;
  }, pixelPresets[0]);
  return {ratio: ratio.id, pixels: pixels.id};
}

function roundToMultiple(value: number, multiple: number): number {
  return Math.max(multiple, Math.round(value / multiple) * multiple);
}

function clampDimension(value: number): number {
  return Math.max(64, Math.min(4096, value));
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
        {steps.map((step, index) => (
          <li key={step.id ?? `${step.kind}-${index}`} className={`harness-step ${step.status ?? 'pending'}`}>
            <div>
              <strong>{formatStepKind(step.kind)}</strong>
              <span>{step.status ?? 'pending'}</span>
            </div>
            <p>{step.error || step.summary || step.decision || step.doneReason || step.model || ''}</p>
            <small>
              {step.provider ? `${step.provider} ` : ''}
              {step.tokens ? `${step.tokens} tokens ` : ''}
              {step.durationMs ? formatDuration(step.durationMs) : ''}
            </small>
          </li>
        ))}
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

function buildRunningHarnessRun(requestID: string, conversationID: string, model: string): HarnessRunView {
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
      {kind: 'preparing', status: 'completed', summary: 'request normalized and history turn prepared'},
      {kind: 'model_call', status: 'completed', provider: 'ollama', model, summary: 'provider stream opened'},
      {kind: 'streaming', status: 'running', provider: 'ollama', model, summary: 'assistant response streaming to UI'},
      {kind: 'evaluation', status: 'pending'},
      {kind: 'saved', status: 'pending'},
    ],
  };
}

function formatStepKind(kind = 'step'): string {
  return kind.replace(/_/g, ' ');
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
