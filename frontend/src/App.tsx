import {useEffect, useMemo, useRef, useState} from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import './App.css';
import {
  CancelStream,
  CheckFalConnection,
  CheckOllama,
  ChooseToolWorkspace,
  DeleteConversation,
  GetConversation,
  GetConfig,
  HasFalAPIKey,
  HasOpenRouterAPIKey,
  ListConversations,
  ListFalModels,
  ListFalImageEditModels,
  ListFalVideoModels,
  ListFalVideoImageModels,
  ListFalVideoExtendModels,
  ListFalAudioModels,
  ListFalTranscribeModels,
  ListFalUpscaleModels,
  ListFalLipsyncImageModels,
  ListFalLipsyncVideoModels,
  ListModels,
  ListPrimaryModels,
  PurgeArchivedConversations,
  ResolveToolPermission,
  SaveImage,
  SaveVideo,
  SaveAudio,
  SaveConfig,
  SaveFalAPIKey,
  SaveOpenRouterAPIKey,
  StreamChat,
  UpdateConversationTitle,
} from '../wailsjs/go/main/App';
import {main} from '../wailsjs/go/models';
import {EventsOff, EventsOn} from '../wailsjs/runtime/runtime';

type View = 'app' | 'settings';
type SettingsTab = 'providers' | 'models' | 'others';
type ConversationKind = 'chat';

type ChatEntry = {
  id: string;
  role: 'user' | 'assistant' | 'system';
  content: string;
  thinking?: string;
  images?: string[];
  videos?: string[];
  audios?: string[];
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
  videos?: string[];
  audios?: string[];
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
  videos: string[];
  audios: string[];
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
  // 'image' attachments strip the data: prefix into payload (Ollama's base64
  // shape); 'audio' and 'video' attachments keep the full data URL as payload,
  // since the OpenRouter input_audio part needs the bytes + a format derived
  // from the data:audio/<fmt>; prefix, and video input is tool-only so the
  // backend keeps the full data URL for AttachedVideo consumers. The kind
  // drives chip rendering and request building in submitChat.
  kind: 'image' | 'audio' | 'video';
};

// A per-conversation composer snapshot, held in memory for the session so
// switching conversations or pressing Cmd+N restores what was being typed.
// Attachments are already base64 data URLs at attach time (see readImageFile),
// so a draft is just serializable state keyed by conversationID. The empty
// string key belongs to the not-yet-saved "new chat" composer.
type ComposerDraft = {
  prompt: string;
  attachments: Attachment[];
};

const defaultBaseURL = 'http://localhost:11434';
const defaultSidebarWidth = 320;
const minSidebarWidth = 240;
const maxSidebarWidth = 560;
const compactHistoryLimit = 10;
const expandedHistoryBatchSize = 20;
const defaultImageAspectRatio = '1:1';
const defaultImageSizePreset = 'standard';
const defaultImageSteps = 24;
const imageAspectRatioOptions = ['1:1', '16:9', '9:16', '4:3', '3:4', '3:2', '2:3', '21:9'];
type ImageSizePreset = {value: string; label: string; longEdge: number};
const imageSizePresetOptions: ImageSizePreset[] = [
  {value: 'draft', label: 'Draft', longEdge: 1024},
  {value: 'standard', label: 'Standard', longEdge: 1536},
  {value: 'high', label: 'High', longEdge: 2048},
  {value: 'high+', label: 'High+', longEdge: 2560},
];
const defaultFalImageModel = 'fal-ai/flux/schnell';
const defaultFalImageEditModel = 'fal-ai/flux/dev/image-to-image';
const defaultFalVideoModel = 'fal-ai/kling-video/v2/master/text-to-video';
const defaultFalVideoImageModel = 'fal-ai/kling-video/v2/master/image-to-video';
const defaultFalVideoExtendModel = 'fal-ai/veo3.1/extend-video';
const defaultFalAudioModel = 'fal-ai/elevenlabs/tts/multilingual-v2';
const defaultFalTranscribeModel = 'fal-ai/wizper';
const defaultFalLipsyncImageModel = 'fal-ai/kling-video/lipsync/audio-to-video';
const defaultFalLipsyncVideoModel = 'fal-ai/sync-lipsync/v2/pro';
const defaultFalUpscaleModel = 'fal-ai/esrgan';
const defaultVideoDuration = '5';
const defaultVideoAspectRatio = '16:9';
const videoDurationOptions = ['5', '10', '15'];
const videoAspectRatioOptions = ['16:9', '9:16', '1:1'];

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
  const [harnessProvider, setHarnessProvider] = useState<'ollama' | 'openrouter'>('ollama');
  // The harness model is remembered per provider, mirroring primaryModels, so
  // switching providers restores that provider's last selection instead of
  // stranding an Ollama model name under OpenRouter.
  const [harnessModels, setHarnessModels] = useState<Record<'ollama' | 'openrouter', string>>({ollama: '', openrouter: ''});
  const harnessModel = harnessModels[harnessProvider];
  const setHarnessModel = (next: string | ((current: string) => string)) => {
    setHarnessModels((prev) => {
      const current = prev[harnessProvider];
      const resolved = typeof next === 'function' ? (next as (c: string) => string)(current) : next;
      if (resolved === current) {
        return prev;
      }
      return {...prev, [harnessProvider]: resolved};
    });
  };
  const [imageModel, setImageModel] = useState('');
  const [primaryProvider, setPrimaryProvider] = useState<'ollama' | 'openrouter'>('ollama');
  // The primary model is remembered per provider so switching providers
  // restores that provider's last selection (falling back to the first
  // available model when the remembered one isn't in the current list).
  const [primaryModels, setPrimaryModels] = useState<Record<'ollama' | 'openrouter', string>>({ollama: '', openrouter: ''});
  const model = primaryModels[primaryProvider];
  const setModel = (next: string | ((current: string) => string)) => {
    setPrimaryModels((prev) => {
      const current = prev[primaryProvider];
      const resolved = typeof next === 'function' ? (next as (c: string) => string)(current) : next;
      if (resolved === current) {
        return prev;
      }
      return {...prev, [primaryProvider]: resolved};
    });
  };
  const [openRouterModels, setOpenRouterModels] = useState<main.ModelInfo[]>([]);
  const [openRouterAPIKeyInput, setOpenRouterAPIKeyInput] = useState('');
  const [openRouterHasKey, setOpenRouterHasKey] = useState(false);
  const [openRouterStatus, setOpenRouterStatus] = useState<'unknown' | 'connected' | 'error'>('unknown');
  const [openRouterError, setOpenRouterError] = useState('');
  const [imageProvider, setImageProvider] = useState<'ollama' | 'fal'>('ollama');
  const [falAPIKeyInput, setFalAPIKeyInput] = useState('');
  const [falHasKey, setFalHasKey] = useState(false);
  const [falStatus, setFalStatus] = useState<'unknown' | 'connected' | 'error'>('unknown');
  const [falError, setFalError] = useState('');
  const [falModel, setFalModel] = useState(defaultFalImageModel);
  const [falModels, setFalModels] = useState<main.FalModel[]>([]);
  const [falImageEditModel, setFalImageEditModel] = useState(defaultFalImageEditModel);
  const [falImageEditModels, setFalImageEditModels] = useState<main.FalModel[]>([]);
  const [falVideoModel, setFalVideoModel] = useState(defaultFalVideoModel);
  const [falVideoModels, setFalVideoModels] = useState<main.FalModel[]>([]);
  const [falVideoImageModel, setFalVideoImageModel] = useState(defaultFalVideoImageModel);
  const [falVideoImageModels, setFalVideoImageModels] = useState<main.FalModel[]>([]);
  const [falVideoExtendModel, setFalVideoExtendModel] = useState(defaultFalVideoExtendModel);
  const [falVideoExtendModels, setFalVideoExtendModels] = useState<main.FalModel[]>([]);
  const [falAudioModel, setFalAudioModel] = useState(defaultFalAudioModel);
  const [falAudioModels, setFalAudioModels] = useState<main.FalModel[]>([]);
  const [falTranscribeModel, setFalTranscribeModel] = useState(defaultFalTranscribeModel);
  const [falTranscribeModels, setFalTranscribeModels] = useState<main.FalModel[]>([]);
  const [falLipsyncImageModel, setFalLipsyncImageModel] = useState(defaultFalLipsyncImageModel);
  const [falLipsyncVideoModel, setFalLipsyncVideoModel] = useState(defaultFalLipsyncVideoModel);
  const [falLipsyncImageModels, setFalLipsyncImageModels] = useState<main.FalModel[]>([]);
  const [falLipsyncVideoModels, setFalLipsyncVideoModels] = useState<main.FalModel[]>([]);
  const [falUpscaleModel, setFalUpscaleModel] = useState(defaultFalUpscaleModel);
  const [falUpscaleModels, setFalUpscaleModels] = useState<main.FalModel[]>([]);
  const [videoDuration, setVideoDuration] = useState(defaultVideoDuration);
  const [videoAspectRatio, setVideoAspectRatio] = useState(defaultVideoAspectRatio);
  const [system, setSystem] = useState('You are Atelier, a precise local AI collaborator.');
  const [prompt, setPrompt] = useState('');
  const [attachments, setAttachments] = useState<Attachment[]>([]);
  const [composerDragging, setComposerDragging] = useState(false);
  const composerDragDepth = useRef(0);
  const [chat, setChat] = useState<ChatEntry[]>([]);
  const [collapsedThinkingIDs, setCollapsedThinkingIDs] = useState<Record<string, boolean>>({});
  const [copiedMessageID, setCopiedMessageID] = useState('');
  const [copiedConversationID, setCopiedConversationID] = useState('');
  const [conversations, setConversations] = useState<main.ConversationSummary[]>([]);
  const [historyExpanded, setHistoryExpanded] = useState(false);
  const [visibleHistoryCount, setVisibleHistoryCount] = useState(compactHistoryLimit);
  const [activeConversationID, setActiveConversationID] = useState('');
  // draftWorkspace holds the per-conversation workspace selected for a NEW
  // chat before its first message locks it as immutable on the record. Empty
  // means "inherit the configured default." Reset whenever a new chat starts.
  const [draftWorkspace, setDraftWorkspace] = useState('');
  const [activeStream, setActiveStream] = useState<string | null>(null);
  const [inFlightConversations, setInFlightConversations] = useState<Record<string, InFlightConversation>>({});
  const [imageAspectRatio, setImageAspectRatio] = useState(defaultImageAspectRatio);
  const [imageSizePreset, setImageSizePreset] = useState(defaultImageSizePreset);
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
  const [settingsTab, setSettingsTab] = useState<SettingsTab>('providers');
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
  // Per-conversation composer drafts, keyed by conversationID ('' = new chat).
  // Mirror refs keep the latest prompt/attachments/activeConversationID so the
  // Cmd+N keydown listener (whose closure goes stale between activeStream
  // changes) reads current values rather than captured ones.
  const composerDraftsRef = useRef<Record<string, ComposerDraft>>({});
  const promptRef = useRef('');
  const attachmentsRef = useRef<Attachment[]>([]);
  const activeConversationIDRef = useRef('');
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
              primary: primaryModels.ollama,
              harness: harnessModels.ollama,
              image: imageModel,
            },
          },
          openrouter: {
            enabled: openRouterHasKey,
            primary: primaryModels.openrouter,
            harness: harnessModels.openrouter,
          },
          fal: {
            enabled: falHasKey,
            model: falModel,
            imageEditModel: falImageEditModel,
            videoModel: falVideoModel,
            videoImageModel: falVideoImageModel,
            videoExtendModel: falVideoExtendModel,
            audioModel: falAudioModel,
            transcribeModel: falTranscribeModel,
            upscaleModel: falUpscaleModel,
            lipsyncImageModel: falLipsyncImageModel,
            lipsyncVideoModel: falLipsyncVideoModel,
          },
        },
        models: {
          primaryProvider,
          harnessProvider,
          imageProvider,
        },
        prompts: {
          system,
        },
        generation: {
          image: {
            aspectRatio: imageAspectRatio,
            sizePreset: imageSizePreset,
            steps: imageSteps,
          },
          video: {
            duration: videoDuration,
            aspectRatio: videoAspectRatio,
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
  }, [baseURL, configLoaded, falHasKey, falModel, falImageEditModel, falVideoModel, falVideoImageModel, falVideoExtendModel, falAudioModel, falTranscribeModel, falUpscaleModel, falLipsyncImageModel, falLipsyncVideoModel, harnessModels, harnessProvider, imageAspectRatio, imageModel, imageProvider, imageSizePreset, imageSteps, openRouterHasKey, primaryModels, primaryProvider, storageConfig, system, toolConfig, videoAspectRatio, videoDuration]);

  // On a fresh launch, put the cursor in the chat box so the user can start
  // typing immediately. Fires once, when config finishes loading.
  useEffect(() => {
    if (!configLoaded || view !== 'app') {
      return;
    }
    const timeout = window.setTimeout(() => {
      chatPromptRef.current?.focus();
    }, 0);
    return () => window.clearTimeout(timeout);
  }, [configLoaded]);

  useEffect(() => {
    const onChunk = (chunk: ChatChunk) => {
      const isVisibleStream = visibleStreamRef.current === chunk.requestID;
      if (chunk.conversationId) {
        markConversationInFlight(chunk.conversationId, chunk.requestID, 'chat');
      }
      const draft = chatStreamDraftsRef.current[chunk.requestID] ?? {content: '', thinking: '', images: [], videos: [], audios: [], streaming: true};
      chatStreamDraftsRef.current[chunk.requestID] = {
        content: `${draft.content}${chunk.content ?? ''}`,
        thinking: `${draft.thinking}${chunk.thinking ?? ''}`,
        images: chunk.images?.length ? chunk.images : draft.images,
        videos: chunk.videos?.length ? chunk.videos : draft.videos,
        audios: chunk.audios?.length ? chunk.audios : draft.audios,
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
            videos: nextDraft.videos,
            audios: nextDraft.audios,
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

  // modelOptions feeds the Ollama-only lists (primary picker's Ollama branch,
  // harness dropdown, image-model fallback). It is built from the fetched
  // Ollama models plus the configured harness/image models (so those stay
  // selectable), but deliberately NOT the primary model: if the stored primary
  // isn't a real Ollama model (e.g. an OpenRouter id that leaked in), it must
  // fall out of the list so the validation effect below heals it to a real
  // model instead of letting the bad value self-validate.
  const modelOptions = useMemo(() => {
    return Array.from(new Set([...asArray(models).map((item) => item.name), harnessModels.ollama, imageModel].filter(Boolean)));
  }, [harnessModels.ollama, imageModel, models]);
  const primaryModelOptions = useMemo(() => {
    if (primaryProvider === 'openrouter') {
      return asArray(openRouterModels)
        .map((item) => ({value: item.id, label: item.displayName || item.id}))
        .sort((a, b) => a.label.localeCompare(b.label));
    }
    return modelOptions.map((name) => ({value: name, label: name}));
  }, [modelOptions, openRouterModels, primaryProvider]);
  const primaryModelIsValid = primaryModelOptions.some((option) => option.value === model);
  const harnessModelOptions = useMemo(() => {
    if (harnessProvider === 'openrouter') {
      return asArray(openRouterModels)
        .map((item) => ({value: item.id, label: item.displayName || item.id}))
        .sort((a, b) => a.label.localeCompare(b.label));
    }
    return modelOptions.map((name) => ({value: name, label: name}));
  }, [harnessProvider, modelOptions, openRouterModels]);
  const imageModelOptions = useMemo(() => {
    const detected = asArray(models).filter((item) => item.imageGeneration).map((item) => item.name).filter(Boolean);
    return detected.length ? detected : modelOptions;
  }, [modelOptions, models]);
  const falModelOptions = useMemo(() => falModelOptionList(falModels), [falModels]);

  const falImageEditModelOptions = useMemo(() => falModelOptionList(falImageEditModels), [falImageEditModels]);

  const falVideoModelOptions = useMemo(() => falModelOptionList(falVideoModels), [falVideoModels]);

  const falVideoImageModelOptions = useMemo(() => falModelOptionList(falVideoImageModels), [falVideoImageModels]);

  const falVideoExtendModelOptions = useMemo(() => falModelOptionList(falVideoExtendModels), [falVideoExtendModels]);

  const falAudioModelOptions = useMemo(() => falModelOptionList(falAudioModels), [falAudioModels]);

  const falTranscribeModelOptions = useMemo(() => falModelOptionList(falTranscribeModels), [falTranscribeModels]);

  const falUpscaleModelOptions = useMemo(() => falModelOptionList(falUpscaleModels), [falUpscaleModels]);

  const falLipsyncImageModelOptions = useMemo(() => falModelOptionList(falLipsyncImageModels), [falLipsyncImageModels]);

  const falLipsyncVideoModelOptions = useMemo(() => falModelOptionList(falLipsyncVideoModels), [falLipsyncVideoModels]);

  // imageSizeOptions derives one labeled option per Size preset, annotated with
  // the concrete pixels the backend will receive for the selected aspect ratio
  // (e.g. "Standard (1536×864)"). Mirrors the Go imageSizeForAspectRatio math
  // (round to multiple of 16, floor 256) so the dropdown labels stay in sync
  // with imageSizeForPresetAndRatio in tools_registry.go. Re-derived when the
  // aspect ratio changes; each preset's long edge differs, so every option
  // updates together.
  const imageSizeOptions = useMemo(() => {
    const parts = imageAspectRatio.split(':').map((value) => Number(value));
    const valid = parts.length === 2 && parts.every((value) => Number.isFinite(value) && value > 0);
    let wr = valid ? parts[0] : 1;
    let hr = valid ? parts[1] : 1;
    const roundTo16 = (n: number) => {
      const rounded = Math.round(n / 16) * 16;
      return rounded < 256 ? 256 : rounded;
    };
    return imageSizePresetOptions.map((preset) => {
      const baseLong = preset.longEdge;
      const longEdge = roundTo16(baseLong);
      let shortRatio = wr;
      let longRatio = hr;
      if (shortRatio > longRatio) {
        [shortRatio, longRatio] = [longRatio, shortRatio];
      }
      const shortEdge = roundTo16((baseLong * shortRatio) / longRatio);
      const dims = wr >= hr ? {width: longEdge, height: shortEdge} : {width: shortEdge, height: longEdge};
      return {value: preset.value, label: `${preset.label} (${dims.width}×${dims.height})`};
    });
  }, [imageAspectRatio]);

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
    const needsCatalog = primaryProvider === 'openrouter' || harnessProvider === 'openrouter';
    if (needsCatalog && openRouterHasKey && openRouterModels.length === 0 && openRouterStatus !== 'error') {
      refreshOpenRouterModels();
    }
  }, [primaryProvider, harnessProvider, openRouterHasKey, openRouterModels.length, openRouterStatus]);

  async function loadConfig() {
    const config = await GetConfig();
    const nextBaseURL = config.providers?.ollama?.baseURL || defaultBaseURL;
    const nextPrimaryModel = config.providers?.ollama?.models?.primary ?? '';
    const nextOpenRouterModel = config.providers?.openrouter?.primary ?? '';
    const nextPrimaryProvider = config.models?.primaryProvider === 'openrouter' ? 'openrouter' : 'ollama';
    const nextHarnessModel = config.providers?.ollama?.models?.harness || nextPrimaryModel;
    const nextOpenRouterHarness = config.providers?.openrouter?.harness ?? '';
    const nextHarnessProvider = config.models?.harnessProvider === 'openrouter' ? 'openrouter' : 'ollama';
    const nextImageModel = config.providers?.ollama?.models?.image ?? '';
    const nextSystem = config.prompts?.system || 'You are Atelier, a precise local AI collaborator.';
    const nextImageAspectRatio = config.generation?.image?.aspectRatio || defaultImageAspectRatio;
    const nextImageSizePreset = config.generation?.image?.sizePreset || defaultImageSizePreset;
    const nextImageSteps = config.generation?.image?.steps || defaultImageSteps;
    const nextImageProvider = config.models?.imageProvider === 'fal' ? 'fal' : 'ollama';
    const nextFalModel = config.providers?.fal?.model || defaultFalImageModel;
    const nextFalImageEditModel = config.providers?.fal?.imageEditModel || defaultFalImageEditModel;
	const nextFalVideoModel = config.providers?.fal?.videoModel || defaultFalVideoModel;
	const nextFalVideoImageModel = config.providers?.fal?.videoImageModel || defaultFalVideoImageModel;
	const nextFalVideoExtendModel = config.providers?.fal?.videoExtendModel || defaultFalVideoExtendModel;
	const nextFalAudioModel = config.providers?.fal?.audioModel || defaultFalAudioModel;
	const nextFalTranscribeModel = config.providers?.fal?.transcribeModel || defaultFalTranscribeModel;
	const nextFalLipsyncImageModel = config.providers?.fal?.lipsyncImageModel || defaultFalLipsyncImageModel;
	const nextFalLipsyncVideoModel = config.providers?.fal?.lipsyncVideoModel || defaultFalLipsyncVideoModel;
    const nextFalUpscaleModel = config.providers?.fal?.upscaleModel || defaultFalUpscaleModel;
    const nextVideoDuration = config.generation?.video?.duration || defaultVideoDuration;
    const nextVideoAspectRatio = config.generation?.video?.aspectRatio || defaultVideoAspectRatio;

    setStartupError('');
    setStorageConfig(config.storage ?? null);
    setToolConfig(config.tools ?? null);
    setBaseURL(nextBaseURL);
    setPrimaryModels({ollama: nextPrimaryModel, openrouter: nextOpenRouterModel});
    setPrimaryProvider(nextPrimaryProvider);
    setHarnessModels({ollama: nextHarnessModel, openrouter: nextOpenRouterHarness});
    setHarnessProvider(nextHarnessProvider);
    setImageModel(nextImageModel);
    setSystem(nextSystem);
    setImageAspectRatio(nextImageAspectRatio);
    setImageSizePreset(nextImageSizePreset);
    setImageSteps(nextImageSteps);
    setImageProvider(nextImageProvider);
    setFalModel(nextFalModel);
    setFalImageEditModel(nextFalImageEditModel);
    setFalVideoModel(nextFalVideoModel);
    setFalVideoImageModel(nextFalVideoImageModel);
    setFalVideoExtendModel(nextFalVideoExtendModel);
    setFalAudioModel(nextFalAudioModel);
    setFalTranscribeModel(nextFalTranscribeModel);
    setFalLipsyncImageModel(nextFalLipsyncImageModel);
    setFalLipsyncVideoModel(nextFalLipsyncVideoModel);
    setFalUpscaleModel(nextFalUpscaleModel);
    setVideoDuration(nextVideoDuration);
    setVideoAspectRatio(nextVideoAspectRatio);
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
      HasFalAPIKey().then((hasKey) => {
        setFalHasKey(hasKey);
        if (hasKey) {
          refreshFalModels();
        }
      }).catch(() => setFalHasKey(false)),
    ]);
  }

  async function refreshConversations(): Promise<main.ConversationSummary[]> {
    try {
      const nextConversations = await ListConversations();
      setConversations(asArray(nextConversations));
      setVisibleHistoryCount((current) => historyExpanded ? Math.max(current, compactHistoryLimit) : compactHistoryLimit);
      return asArray(nextConversations);
    } catch (error) {
      setStartupError(formatError(error));
      setConversations([]);
      return [];
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

  // chooseDraftWorkspace is the per-conversation picker for a NEW chat. It
  // reuses the same native dialog as the Settings default picker, but writes
  // the result into draftWorkspace (the per-conversation override) rather than
  // the global toolConfig. After the first send the workspace is immutable and
  // this handler is no longer reachable from the UI.
  async function chooseDraftWorkspace() {
    try {
      const seed = draftWorkspace || (toolConfig?.filesystem?.root ?? '');
      const selected = await ChooseToolWorkspace(seed);
      if (!selected) {
        return;
      }
      setDraftWorkspace(selected);
    } catch (error) {
      setStartupError(formatError(error));
    }
  }

  // activeConversationWorkspace is the immutable workspace of the conversation
  // currently in view, looked up from the conversation list. Empty for a new
  // chat (where draftWorkspace takes over) or for legacy conversations without
  // one (the UI falls back to the configured default).
  const activeConversationWorkspace = activeConversationID
    ? conversations.find((item) => item.id === activeConversationID)?.workspace ?? ''
    : '';

  // displayedWorkspace is what the composer chip shows: the active
  // conversation's immutable root for an existing chat, the draft selection
  // (or the default) for a new chat. Always falls back to the configured
  // default root so the user is never left guessing where a message will run.
  const defaultWorkspaceRoot = toolConfig?.filesystem?.root ?? '~/Documents';
  const displayedWorkspace = activeConversationID
    ? (activeConversationWorkspace || defaultWorkspaceRoot)
    : (draftWorkspace || defaultWorkspaceRoot);

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
      setPrimaryModels((current) => (current.ollama ? current : {...current, ollama: firstModel}));
      // Target the Ollama slot explicitly: this default comes from the Ollama
      // catalog and must not land in the OpenRouter slot when that provider is
      // the active one.
      setHarnessModels((current) => (current.ollama ? current : {...current, ollama: firstModel}));
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

  async function refreshFalModels() {
    // The fal catalog is a discovery aid only — a load failure leaves the field
    // as free text, so swallow the error rather than surfacing it as a fal
    // connection error (which would confuse "key works" vs "catalog fetch").
    try {
      setFalModels(asArray(await ListFalModels()));
    } catch {
      setFalModels([]);
    }
    try {
      setFalImageEditModels(asArray(await ListFalImageEditModels()));
    } catch {
      setFalImageEditModels([]);
    }
    try {
      setFalVideoModels(asArray(await ListFalVideoModels()));
    } catch {
      setFalVideoModels([]);
    }
    try {
      setFalVideoImageModels(asArray(await ListFalVideoImageModels()));
    } catch {
      setFalVideoImageModels([]);
    }
    try {
      setFalVideoExtendModels(asArray(await ListFalVideoExtendModels()));
    } catch {
      setFalVideoExtendModels([]);
    }
    try {
      setFalAudioModels(asArray(await ListFalAudioModels()));
    } catch {
      setFalAudioModels([]);
    }
    try {
      setFalTranscribeModels(asArray(await ListFalTranscribeModels()));
    } catch {
      setFalTranscribeModels([]);
    }
    try {
      setFalUpscaleModels(asArray(await ListFalUpscaleModels()));
    } catch {
      setFalUpscaleModels([]);
    }
    try {
      setFalLipsyncImageModels(asArray(await ListFalLipsyncImageModels()));
    } catch {
      setFalLipsyncImageModels([]);
    }
    try {
      setFalLipsyncVideoModels(asArray(await ListFalLipsyncVideoModels()));
    } catch {
      setFalLipsyncVideoModels([]);
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
      // The harness cannot reach OpenRouter without a key either, and an
      // unreachable harness now fails the turn up front rather than degrading.
      setHarnessProvider((current) => current === 'openrouter' ? 'ollama' : current);
    } catch (error) {
      setOpenRouterStatus('error');
      setOpenRouterError(formatError(error));
    }
  }

  async function saveFalKey() {
    try {
      await SaveFalAPIKey(falAPIKeyInput);
      setFalAPIKeyInput('');
      const hasKey = await HasFalAPIKey();
      setFalHasKey(hasKey);
      setFalStatus('unknown');
      setFalError('');
      if (hasKey) {
        await refreshFalModels();
      }
    } catch (error) {
      setStatus((current) => current ? {...current, error: String(error)} : current);
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

  async function clearFalKey() {
    try {
      await SaveFalAPIKey('');
      setFalHasKey(false);
      setFalModels([]);
      setFalVideoModels([]);
      setFalVideoImageModels([]);
      setFalVideoExtendModels([]);
      setFalAudioModels([]);
      setFalTranscribeModels([]);
      setFalLipsyncImageModels([]);
      setFalLipsyncVideoModels([]);
      setFalStatus('unknown');
      setFalError('');
      setImageProvider((current) => current === 'fal' ? 'ollama' : current);
    } catch (error) {
      setStatus((current) => current ? {...current, error: String(error)} : current);
    }
  }

  // Keep the mirror refs in sync so navigation handlers (and the stale Cmd+N
  // keydown closure) always read the latest composer/active-conversation state.
  useEffect(() => {
    promptRef.current = prompt;
  }, [prompt]);
  useEffect(() => {
    attachmentsRef.current = attachments;
  }, [attachments]);
  useEffect(() => {
    activeConversationIDRef.current = activeConversationID;
  }, [activeConversationID]);

  // Snapshot the current composer (prompt + attachments) under the conversation
  // being left, so it can be restored when returning. Empty drafts are deleted
  // to keep the store bounded and avoid resurrecting blank composes.
  function stashCurrentDraft() {
    const id = activeConversationIDRef.current;
    const next: ComposerDraft = {
      prompt: promptRef.current,
      attachments: attachmentsRef.current,
    };
    if (!next.prompt.trim() && next.attachments.length === 0) {
      delete composerDraftsRef.current[id];
    } else {
      composerDraftsRef.current[id] = next;
    }
  }

  // Load the composer from the store for the conversation being entered, or
  // clear it if no draft exists for that key.
  function restoreDraftFor(conversationID: string) {
    const draft = composerDraftsRef.current[conversationID];
    setPrompt(draft?.prompt ?? '');
    setAttachments(draft?.attachments ?? []);
  }

  async function resetWorkspace() {
    stashCurrentDraft();
    visibleStreamRef.current = null;
    setActiveStream(null);
    setChat([]);
    setCollapsedThinkingIDs({});
    restoreDraftFor('');
    setActiveConversationID('');
    setDraftWorkspace('');
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
    stashCurrentDraft();
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
      videos: historyVideos(turn.content),
      audios: historyAudios(turn.content),
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
        videos: draft?.videos,
        audios: draft?.audios,
        streaming: draft?.streaming ?? true,
        error: draft?.error,
        provider: draft?.provider,
      });
    }
    setChat(entries);
    setCollapsedThinkingIDs({});
    setActiveConversationID(detail.conversation.id);
    restoreDraftFor(detail.conversation.id);
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
      delete composerDraftsRef.current[conversation.id];
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
      const remaining = await refreshConversations();
      // Drop any session drafts for conversations that no longer exist, but
      // always keep the '' new-chat key.
      const liveIDs = new Set(remaining.map((item) => item.id));
      for (const id of Object.keys(composerDraftsRef.current)) {
        if (id !== '' && !liveIDs.has(id)) {
          delete composerDraftsRef.current[id];
        }
      }
      setConfirmPurgeArchived(false);
      setPurgeStatus(`${result.deletedConversations} archived ${result.deletedConversations === 1 ? 'conversation' : 'conversations'} and ${result.deletedAssets} ${result.deletedAssets === 1 ? 'asset' : 'assets'} deleted.`);
    } catch (error) {
      setPurgeStatus('');
      setStartupError(formatError(error));
    } finally {
      setPurgeBusy(false);
    }
  }


  // executeChatStream is the shared core of a chat turn: it wires up the
  // streaming-state refs, opens the StreamChat call, and on failure marks the
  // matching assistant entry with the error. Callers are responsible for
  // pushing/replacing the user + assistant entries in `chat` and building
  // requestMessages before calling this — submitChat for a fresh send,
  // retryFailedTurn for a retry. Extracted so both paths share one error path.
  async function executeChatStream(opts: {requestID: string; requestMessages: main.ChatMessage[]}) {
    const {requestID} = opts;
    visibleStreamRef.current = requestID;
    chatStreamDraftsRef.current[requestID] = {content: '', thinking: '', images: [], videos: [], audios: [], streaming: true};
    setActiveStream(requestID);
    try {
      const start = await StreamChat(main.ChatRequest.createFrom({
        requestID,
        conversationId: activeConversationID || undefined,
        baseURL,
        provider: primaryProvider,
        model,
        selectedModel: model,
        system,
        messages: opts.requestMessages,
        // Only sent for a new chat (turn 1). The backend ignores it for an
        // existing conversation — the record's immutable workspace wins.
        ...(activeConversationID ? {} : {workspace: draftWorkspace || undefined}),
      }));
      markConversationInFlight(start.conversationId, start.requestID, 'chat');
      setActiveConversationID(start.conversationId);
      void refreshConversations();
    } catch (error) {
      chatStreamDraftsRef.current[requestID] = {
        ...(chatStreamDraftsRef.current[requestID] ?? {content: '', thinking: '', images: [], videos: [], audios: []}),
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

  async function submitChat() {
    const trimmed = prompt.trim();
    if (!trimmed || !model || activeStream || !primaryModelIsValid) {
      return;
    }

    const userEntry: ChatEntry = {
      id: `user-${Date.now()}`,
      role: 'user',
      content: trimmed,
      images: attachments.filter((item) => item.kind === 'image').map((item) => item.src),
      audios: attachments.filter((item) => item.kind === 'audio').map((item) => item.src),
      videos: attachments.filter((item) => item.kind === 'video').map((item) => item.src),
    };
    const requestID = `chat-${Date.now()}-${Math.random().toString(36).slice(2)}`;
    const audioAttachments = attachments.filter((item) => item.kind === 'audio').map((item) => item.payload).filter(Boolean);
    const imageAttachments = attachments.filter((item) => item.kind === 'image').map((item) => item.payload).filter(Boolean);
    const videoAttachments = attachments.filter((item) => item.kind === 'video').map((item) => item.payload).filter(Boolean);
    const requestMessages: main.ChatMessage[] = [
      ...chat
        .filter((entry) => entry.role !== 'system' && (entry.content || entry.images?.length || entry.audios?.length || entry.videos?.length))
        .map((entry) => ({
          role: entry.role,
          content: entry.content,
          ...(entry.images?.length ? {images: entry.images.map(imagePayloadForOllama).filter(Boolean)} : {}),
          // Hydrated history audios/videos are /atelier-artifact/ display URLs;
          // only inline data: URLs are valid payloads, so filter like images.
          ...(entry.audios?.length ? {audios: entry.audios.filter((audio) => audio.startsWith('data:'))} : {}),
          ...(entry.videos?.length ? {videos: entry.videos.filter((video) => video.startsWith('data:'))} : {}),
        }) as main.ChatMessage),
      {
        role: 'user',
        content: trimmed,
        ...(imageAttachments.length ? {images: imageAttachments} : {}),
        ...(audioAttachments.length ? {audios: audioAttachments} : {}),
        ...(videoAttachments.length ? {videos: videoAttachments} : {}),
      } as main.ChatMessage,
    ];

    setPrompt('');
    setAttachments([]);
    // The composer contents are being sent, not stashed — drop any stored draft
    // for the current key ('' for a brand-new chat) so it isn't resurrected.
    delete composerDraftsRef.current[activeConversationIDRef.current];
    shouldFollowTranscriptRef.current = true;
    setChat((entries) => [
      ...entries,
      userEntry,
      {id: `assistant-${requestID}`, role: 'assistant', content: '', streaming: true, provider: primaryProvider},
    ]);
    await executeChatStream({requestID, requestMessages});
  }

  // retryFailedTurn resends the user message preceding a failed assistant entry,
  // replacing the failed entry in place with a fresh streaming placeholder. Used
  // by the Retry button shown next to an error. The preceding user entry stays in
  // the transcript; no duplicate is pushed — cleaner than re-typing the message,
  // which would show the user message twice. Note: StartChatTurn on the backend
  // will append a fresh user turn to disk, same as a retyped message; failed
  // turns are never persisted, so on reload the failed entry is gone.
  async function retryFailedTurn(failedAssistantId: string) {
    if (!model || activeStream || !primaryModelIsValid) {
      return;
    }
    const failedIdx = chat.findIndex((entry) => entry.id === failedAssistantId);
    if (failedIdx < 0) {
      return;
    }
    // Build request messages from everything before the failed assistant entry.
    // The preceding user message is included; the empty failed entry is not.
    const historyForRequest = chat.slice(0, failedIdx);
    if (!historyForRequest.some((entry) => entry.role === 'user')) {
      return;
    }
    const requestMessages: main.ChatMessage[] = historyForRequest
      .filter((entry) => entry.role !== 'system' && (entry.content || entry.images?.length || entry.audios?.length || entry.videos?.length))
      .map((entry) => ({
        role: entry.role,
        content: entry.content,
        ...(entry.images?.length ? {images: entry.images.map(imagePayloadForOllama).filter(Boolean)} : {}),
        ...(entry.audios?.length ? {audios: entry.audios.filter((audio) => audio.startsWith('data:'))} : {}),
        ...(entry.videos?.length ? {videos: entry.videos.filter((video) => video.startsWith('data:'))} : {}),
      }) as main.ChatMessage);
    const requestID = `chat-${Date.now()}-${Math.random().toString(36).slice(2)}`;
    shouldFollowTranscriptRef.current = true;
    setChat((entries) => entries.map((entry) =>
      entry.id === failedAssistantId
        ? {id: `assistant-${requestID}`, role: 'assistant', content: '', streaming: true, provider: primaryProvider}
        : entry,
    ));
    await executeChatStream({requestID, requestMessages});
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
        ...(chatStreamDraftsRef.current[activeStream] ?? {content: '', thinking: '', images: [], videos: [], audios: []}),
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

  async function addFiles(files: FileList | null) {
    if (!files) {
      return;
    }
    const next = await Promise.all(Array.from(files).map((file) => readFileAsAttachment(file)));
    setAttachments((items) => [...items, ...next]);
  }

  async function handleChatPromptPaste(event: React.ClipboardEvent<HTMLTextAreaElement>) {
    const mediaFiles = Array.from(event.clipboardData?.items ?? [])
      .filter((item) => item.kind === 'file' && (item.type.startsWith('image/') || item.type.startsWith('audio/') || item.type.startsWith('video/')))
      .map((item) => item.getAsFile())
      .filter((file): file is File => file !== null);
    if (!mediaFiles.length) {
      return;
    }
    // Keep the pasted bytes out of the text field and route them to attachments.
    event.preventDefault();
    const stamp = Date.now();
    const next = await Promise.all(
      mediaFiles.map((file, index) => {
        const extension = file.name.includes('.') ? '' : mediaExtensionForType(file.type);
        return readFileAsAttachment(file, `pasted-${stamp}-${index + 1}${file.name ? `-${file.name}` : extension}`);
      }),
    );
    setAttachments((items) => [...items, ...next]);
  }

  function composerHasMediaDrag(event: React.DragEvent<HTMLDivElement>): boolean {
    return Array.from(event.dataTransfer?.items ?? []).some(
      (item) => item.kind === 'file' && (item.type.startsWith('image/') || item.type.startsWith('audio/') || item.type.startsWith('video/')),
    );
  }

  function handleComposerDragEnter(event: React.DragEvent<HTMLDivElement>) {
    if (!composerHasMediaDrag(event)) {
      return;
    }
    event.preventDefault();
    composerDragDepth.current += 1;
    setComposerDragging(true);
  }

  function handleComposerDragOver(event: React.DragEvent<HTMLDivElement>) {
    if (!composerHasMediaDrag(event)) {
      return;
    }
    // Signal that dropping here is allowed and stop the browser from opening the file.
    event.preventDefault();
    event.dataTransfer.dropEffect = 'copy';
  }

  function handleComposerDragLeave(event: React.DragEvent<HTMLDivElement>) {
    if (composerDragDepth.current === 0) {
      return;
    }
    composerDragDepth.current -= 1;
    if (composerDragDepth.current === 0) {
      setComposerDragging(false);
    }
  }

  async function handleComposerDrop(event: React.DragEvent<HTMLDivElement>) {
    composerDragDepth.current = 0;
    setComposerDragging(false);
    const mediaFiles = Array.from(event.dataTransfer?.files ?? []).filter(
      (file) => file.type.startsWith('image/') || file.type.startsWith('audio/') || file.type.startsWith('video/'),
    );
    if (!mediaFiles.length) {
      return;
    }
    // Keep the browser from navigating to the dropped file and route it to attachments.
    event.preventDefault();
    const stamp = Date.now();
    const next = await Promise.all(
      mediaFiles.map((file, index) => {
        const extension = file.name.includes('.') ? '' : mediaExtensionForType(file.type);
        return readFileAsAttachment(file, file.name || `dropped-${stamp}-${index + 1}${extension}`);
      }),
    );
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

  async function saveGeneratedVideo(video: string, index: number) {
    try {
      await SaveVideo(main.SaveVideoRequest.createFrom({
        path: video,
        suggestedName: `atelier-${Date.now()}-${index + 1}`,
      }));
    } catch (error) {
      setStartupError(error instanceof Error ? error.message : String(error));
    }
  }

  async function saveGeneratedAudio(audio: string, index: number) {
    try {
      await SaveAudio(main.SaveAudioRequest.createFrom({
        path: audio,
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
                <p>AI Workshop</p>
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

              <div className="settings-tabs" role="tablist">
                <button
                  type="button"
                  role="tab"
                  aria-selected={settingsTab === 'providers'}
                  className={settingsTab === 'providers' ? 'active' : ''}
                  onClick={() => setSettingsTab('providers')}
                >Providers</button>
                <button
                  type="button"
                  role="tab"
                  aria-selected={settingsTab === 'models'}
                  className={settingsTab === 'models' ? 'active' : ''}
                  onClick={() => setSettingsTab('models')}
                >Models</button>
                <button
                  type="button"
                  role="tab"
                  aria-selected={settingsTab === 'others'}
                  className={settingsTab === 'others' ? 'active' : ''}
                  onClick={() => setSettingsTab('others')}
                >Others</button>
              </div>

              {settingsTab === 'providers' ? (
              <>
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
                    <button type="button" className="icon-btn" onClick={saveOpenRouterKey} disabled={!openRouterAPIKeyInput} aria-label="Save key" title="Save key">
                      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                        <path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z" />
                        <polyline points="17 21 17 13 7 13 7 21" />
                        <polyline points="7 3 7 8 15 8" />
                      </svg>
                    </button>
                    <button type="button" className={`icon-btn${openRouterStatus === 'connected' ? ' spinning' : ''}`} onClick={refreshOpenRouterModels} disabled={!openRouterHasKey} aria-label="Check connection" title="Check connection">
                      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                        <path d="M21 12a9 9 0 0 1-9 9 9 9 0 0 1-6.7-3" />
                        <path d="M3 12a9 9 0 0 1 9-9 9 9 0 0 1 6.7 3" />
                        <polyline points="21 4 21 9 16 9" />
                        <polyline points="3 20 3 15 8 15" />
                      </svg>
                    </button>
                    {openRouterHasKey ? (
                      <button type="button" className="icon-btn" onClick={clearOpenRouterKey} aria-label="Clear key" title="Clear key">
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                          <polyline points="3 6 5 6 21 6" />
                          <path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" />
                        </svg>
                      </button>
                    ) : <span className="icon-btn-placeholder" aria-hidden="true" />}
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
                <h3>fal.ai</h3>
                <div className="connection">
                  <label htmlFor="fal-key">API Key</label>
                  <div className="endpoint-row">
                    <input
                      id="fal-key"
                      type="password"
                      placeholder={falHasKey ? 'Key saved — enter a new key to replace it' : 'fal-...'}
                      value={falAPIKeyInput}
                      onChange={(event) => setFalAPIKeyInput(event.target.value)}
                    />
                    <button type="button" className="icon-btn" onClick={saveFalKey} disabled={!falAPIKeyInput} aria-label="Save key" title="Save key">
                      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                        <path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z" />
                        <polyline points="17 21 17 13 7 13 7 21" />
                        <polyline points="7 3 7 8 15 8" />
                      </svg>
                    </button>
                    <button type="button" className={`icon-btn${falStatus === 'connected' ? ' spinning' : ''}`} onClick={checkFalConnection} disabled={!falHasKey} aria-label="Check connection" title="Check connection">
                      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                        <path d="M21 12a9 9 0 0 1-9 9 9 9 0 0 1-6.7-3" />
                        <path d="M3 12a9 9 0 0 1 9-9 9 9 0 0 1 6.7 3" />
                        <polyline points="21 4 21 9 16 9" />
                        <polyline points="3 20 3 15 8 15" />
                      </svg>
                    </button>
                    {falHasKey ? (
                      <button type="button" className="icon-btn" onClick={clearFalKey} aria-label="Clear key" title="Clear key">
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                          <polyline points="3 6 5 6 21 6" />
                          <path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" />
                        </svg>
                      </button>
                    ) : <span className="icon-btn-placeholder" aria-hidden="true" />}
                  </div>
                  <div className={falStatus === 'connected' ? 'status online' : 'status offline'}>
                    <span />
                    {falStatus === 'connected'
                      ? 'Connected'
                      : falStatus === 'error'
                        ? `fal.ai: ${falError}`
                        : falHasKey
                          ? 'API key saved — not checked'
                          : 'No key saved.'}
                  </div>
                </div>
              </section>
              </>
              ) : null}

              {settingsTab === 'others' ? (
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
                    <span>Default workspace</span>
                    <div className="workspace-picker">
                      <code>{shortenHomePath(toolConfig?.filesystem?.root ?? '~/Documents')}</code>
                      <button onClick={chooseToolWorkspace}>Choose</button>
                    </div>
                    <div className="storage-hint">
                      Used when a new conversation doesn't pick its own folder. Each conversation's workspace is locked at creation.
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
              ) : null}

              {settingsTab === 'models' ? (
              <>
              <section className="settings-section">
                <h3>Harness</h3>
                <div className="settings-rows">
                  <div className="two-column">
                    <div className="field">
                      <label htmlFor="harness-provider">Harness Provider</label>
                      <select
                        id="harness-provider"
                        value={harnessProvider}
                        onChange={(event) => setHarnessProvider(event.target.value as 'ollama' | 'openrouter')}
                      >
                        <option value="ollama">Ollama</option>
                        <option value="openrouter" disabled={!openRouterHasKey}>OpenRouter</option>
                      </select>
                    </div>
                    <div className="field">
                      <label htmlFor="harness-model">Harness Model</label>
                      <div className="model-inline-control">
                        {harnessProvider === 'openrouter' ? (
                          <ModelCombobox
                            id="harness-model"
                            ariaLabel="Harness model"
                            placeholder="Type to filter models..."
                            value={harnessModel}
                            onChange={setHarnessModel}
                            options={harnessModelOptions}
                          />
                        ) : (
                          <>
                            <select id="harness-model" value={harnessModel} onChange={(event) => setHarnessModel(event.target.value)}>
                              {harnessModelOptions.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
                            </select>
                            <ModelCapabilityLink
                              id="settings-tools"
                              modelName={harnessModel}
                              models={models}
                              openID={openCapabilityID}
                              setOpenID={setOpenCapabilityID}
                              variant="icon"
                            />
                          </>
                        )}
                      </div>
                    </div>
                  </div>
                </div>
              </section>

              <section className="settings-section">
                <h3>Image</h3>
                <div className="settings-rows">
                  <div className="two-column">
                    <div className="field">
                      <label htmlFor="image-provider">Image Provider</label>
                      <select
                        id="image-provider"
                        value={imageProvider}
                        onChange={(event) => setImageProvider(event.target.value as 'ollama' | 'fal')}
                      >
                        <option value="ollama">Ollama (local)</option>
                        <option value="fal">fal.ai (cloud)</option>
                      </select>
                    </div>

                    {imageProvider === 'fal' ? (
                      <div className="field">
                        <label htmlFor="fal-model">fal.ai Model</label>
                        <ModelCombobox
                          id="fal-model"
                          ariaLabel="fal.ai model"
                          placeholder={defaultFalImageModel}
                          value={falModel}
                          onChange={setFalModel}
                          options={falModelOptions}
                          allowCustom
                        />
                        {!falHasKey ? (
                          <span className="hint">Add a fal.ai API key above before generating images.</span>
                        ) : falModelOptions.length ? null : (
                          <span className="hint">Type a fal.ai endpoint id — the model list couldn't be loaded.</span>
                        )}
                      </div>
                    ) : (
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
                    )}
                  </div>

                  {imageProvider === 'fal' ? (
                    <div className="field">
                      <label htmlFor="fal-image-edit-model">Image-to-Image Model (fal.ai)</label>
                      <ModelCombobox
                        id="fal-image-edit-model"
                        ariaLabel="fal.ai image-to-image model"
                        placeholder={defaultFalImageEditModel}
                        value={falImageEditModel}
                        onChange={setFalImageEditModel}
                        options={falImageEditModelOptions}
                        allowCustom
                      />
                    </div>
                  ) : null}

                  <div className="three-column">
                    <div className="field">
                      <label htmlFor="image-aspect">Aspect Ratio</label>
                      <select id="image-aspect" value={imageAspectRatio} onChange={(event) => setImageAspectRatio(event.target.value)}>
                        {imageAspectRatioOptions.map((value) => <option key={value} value={value}>{value}</option>)}
                      </select>
                    </div>

                    <div className="field">
                      <label htmlFor="image-size">Size</label>
                      <select id="image-size" value={imageSizePreset} onChange={(event) => setImageSizePreset(event.target.value)}>
                        {imageSizeOptions.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
                      </select>
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
                  </div>
                </div>
              </section>

              <section className="settings-section">
                <h3>Video</h3>
                <div className="settings-rows">
                  <div className="two-column">
                    <div className="field">
                      <label htmlFor="fal-video-model">Text-to-Video Model (fal.ai)</label>
                      <ModelCombobox
                        id="fal-video-model"
                        ariaLabel="fal.ai text-to-video model"
                        placeholder={defaultFalVideoModel}
                        value={falVideoModel}
                        onChange={setFalVideoModel}
                        options={falVideoModelOptions}
                        allowCustom
                      />
                      {!falHasKey ? (
                        <span className="hint">Add a fal.ai API key above to generate videos.</span>
                      ) : falVideoModelOptions.length ? null : (
                        <span className="hint">Type a fal.ai text-to-video endpoint id.</span>
                      )}
                    </div>

                    <div className="field">
                      <label htmlFor="fal-video-image-model">Image-to-Video Model (fal.ai)</label>
                      <ModelCombobox
                        id="fal-video-image-model"
                        ariaLabel="fal.ai image-to-video model"
                        placeholder={defaultFalVideoImageModel}
                        value={falVideoImageModel}
                        onChange={setFalVideoImageModel}
                        options={falVideoImageModelOptions}
                        allowCustom
                      />
                    </div>
                  </div>

                  <div className="field">
                    <label htmlFor="fal-video-extend-model">Video-Extend Model (fal.ai)</label>
                    <ModelCombobox
                      id="fal-video-extend-model"
                      ariaLabel="fal.ai video-extend model"
                      placeholder={defaultFalVideoExtendModel}
                      value={falVideoExtendModel}
                      onChange={setFalVideoExtendModel}
                      options={falVideoExtendModelOptions}
                      allowCustom
                    />
                  </div>

                  <div className="two-column">
                    <div className="field">
                      <label htmlFor="video-duration">Video Duration (s)</label>
                      <select id="video-duration" value={videoDuration} onChange={(event) => setVideoDuration(event.target.value)}>
                        {videoDurationOptions.map((value) => <option key={value} value={value}>{value}</option>)}
                      </select>
                    </div>

                    <div className="field">
                      <label htmlFor="video-aspect">Video Aspect Ratio</label>
                      <select id="video-aspect" value={videoAspectRatio} onChange={(event) => setVideoAspectRatio(event.target.value)}>
                        {videoAspectRatioOptions.map((value) => <option key={value} value={value}>{value}</option>)}
                      </select>
                    </div>
                  </div>
                </div>
              </section>

              <section className="settings-section">
                <h3>Audio</h3>
                <div className="field">
                  <label htmlFor="fal-audio-model">Audio Model (fal.ai)</label>
                  <ModelCombobox
                    id="fal-audio-model"
                    ariaLabel="fal.ai audio model"
                    placeholder={defaultFalAudioModel}
                    value={falAudioModel}
                    onChange={setFalAudioModel}
                    options={falAudioModelOptions}
                    allowCustom
                  />
                  {!falHasKey ? (
                    <span className="hint">Add a fal.ai API key above to generate audio.</span>
                  ) : null}
                </div>
              </section>

              <section className="settings-section">
                <h3>Transcription</h3>
                <div className="field">
                  <label htmlFor="fal-transcribe-model">Transcription Model (fal.ai)</label>
                  <ModelCombobox
                    id="fal-transcribe-model"
                    ariaLabel="fal.ai transcription model"
                    placeholder={defaultFalTranscribeModel}
                    value={falTranscribeModel}
                    onChange={setFalTranscribeModel}
                    options={falTranscribeModelOptions}
                    allowCustom
                  />
                  {!falHasKey ? (
                    <span className="hint">Add a fal.ai API key above to transcribe audio.</span>
                  ) : null}
                </div>
              </section>

              <section className="settings-section">
                <h3>Lip Sync</h3>
                <div className="settings-rows">
                  <div className="two-column">
                    <div className="field">
                      <label htmlFor="fal-lipsync-image-model">Audio-to-Video Model (fal.ai)</label>
                      <ModelCombobox
                        id="fal-lipsync-image-model"
                        ariaLabel="fal.ai audio-to-video lip sync model"
                        placeholder={defaultFalLipsyncImageModel}
                        value={falLipsyncImageModel}
                        onChange={setFalLipsyncImageModel}
                        options={falLipsyncImageModelOptions}
                        allowCustom
                      />
                    </div>

                    <div className="field">
                      <label htmlFor="fal-lipsync-video-model">Video-to-Video Model (fal.ai)</label>
                      <ModelCombobox
                        id="fal-lipsync-video-model"
                        ariaLabel="fal.ai video-to-video lip sync model"
                        placeholder={defaultFalLipsyncVideoModel}
                        value={falLipsyncVideoModel}
                        onChange={setFalLipsyncVideoModel}
                        options={falLipsyncVideoModelOptions}
                        allowCustom
                      />
                    </div>
                  </div>
                  {!falHasKey ? (
                    <span className="hint">Add a fal.ai API key above to use lip sync.</span>
                  ) : null}
                </div>
              </section>

              <section className="settings-section">
                <h3>Upscale</h3>
                <div className="field">
                  <label htmlFor="fal-upscale-model">Upscale Model (fal.ai)</label>
                  <ModelCombobox
                    id="fal-upscale-model"
                    ariaLabel="fal.ai upscale model"
                    placeholder={defaultFalUpscaleModel}
                    value={falUpscaleModel}
                    onChange={setFalUpscaleModel}
                    options={falUpscaleModelOptions}
                    allowCustom
                  />
                  {!falHasKey ? (
                    <span className="hint">Add a fal.ai API key above to upscale images.</span>
                  ) : falUpscaleModelOptions.length ? null : (
                    <span className="hint">Type a fal.ai endpoint id — the model list couldn't be loaded.</span>
                  )}
                </div>
              </section>
              </>
              ) : null}

              {settingsTab === 'others' ? (
              <section className="settings-section">
                <div className="field">
                  <label htmlFor="system">System</label>
                  <textarea id="system" value={system} onChange={(event) => setSystem(event.target.value)} />
                </div>
              </section>
              ) : null}
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
                    <p>Your desktop AI workshop — agentic chat and image, audio, and video generation, local or cloud.</p>
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
                      {entry.role === 'user' && entry.audios?.length ? (
                        <div className="chat-user-audios">
                          {entry.audios.map((audio, index) => (
                            <audio key={`${entry.id}-audio-${index}`} src={audio} controls preload="metadata" />
                          ))}
                        </div>
                      ) : null}
                      {entry.role === 'user' && entry.videos?.length ? (
                        <div className="chat-user-videos">
                          {entry.videos.map((video, index) => (
                            <video key={`${entry.id}-video-${index}`} src={video} controls preload="metadata" />
                          ))}
                        </div>
                      ) : null}
                      {entry.role === 'assistant' && entry.videos?.length ? (
                        <div className="chat-video-results">
                          {entry.videos.map((video, index) => (
                            <figure key={`${entry.id}-video-${index}`} className="chat-video-card">
                              <video src={video} controls preload="metadata" />
                              <figcaption>
                                <button type="button" onClick={() => saveGeneratedVideo(video, index)}>Download video</button>
                              </figcaption>
                            </figure>
                          ))}
                        </div>
                      ) : null}
                      {entry.role === 'assistant' && entry.audios?.length ? (
                        <div className="chat-audio-results">
                          {entry.audios.map((audio, index) => (
                            <figure key={`${entry.id}-audio-${index}`} className="chat-audio-card">
                              <audio src={audio} controls preload="metadata" />
                              <figcaption>
                                <button type="button" onClick={() => saveGeneratedAudio(audio, index)}>Download audio</button>
                              </figcaption>
                            </figure>
                          ))}
                        </div>
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
                      {entry.error ? (
                        <div className="message-actions">
                          <button
                            className="message-retry-button"
                            type="button"
                            disabled={Boolean(activeStream)}
                            aria-label="Retry last turn"
                            title="Retry last turn"
                            onClick={() => retryFailedTurn(entry.id)}
                          >
                            ↻ Retry
                          </button>
                        </div>
                      ) : null}
                      {entry.error ? <div className="error">{entry.error}</div> : null}
                    </article>
                  );
                })}
              </div>

              <div
                className={`composer${composerDragging ? ' composer--dragging' : ''}`}
                onDragEnter={handleComposerDragEnter}
                onDragOver={handleComposerDragOver}
                onDragLeave={handleComposerDragLeave}
                onDrop={handleComposerDrop}
              >
                {composerDragging ? (
                  <div className="composer-drop-overlay">Drop media to attach</div>
                ) : null}
                {asArray(attachments).length ? (
                  <div className="attachment-strip">
                    {asArray(attachments).map((item) => (
                      <button key={item.name} onClick={() => setAttachments((items) => items.filter((next) => next.name !== item.name))}>
                        {item.kind === 'audio' ? (
                          <span className="attachment-audio-chip" aria-hidden="true">
                            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                              <path d="M3 18v-6a9 9 0 0 1 18 0v6" />
                              <path d="M21 19a2 2 0 0 1-2 2h-1a2 2 0 0 1-2-2v-3a2 2 0 0 1 2-2h3zM3 19a2 2 0 0 0 2 2h1a2 2 0 0 0 2-2v-3a2 2 0 0 0-2-2H3z" />
                            </svg>
                          </span>
                        ) : item.kind === 'video' ? (
                          <span className="attachment-video-chip" aria-hidden="true">
                            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                              <path d="m22 8-6 4 6 4V8Z" />
                              <rect width="14" height="12" x="2" y="6" rx="2" ry="2" />
                            </svg>
                          </span>
                        ) : (
                          <img src={item.src} alt="" />
                        )}
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
                  onPaste={handleChatPromptPaste}
                  placeholder="Prompt Atelier..."
                />
                <div className="composer-actions">
                  <div className="composer-actions-left">
                    <button
                      type="button"
                      className="composer-workspace-chip"
                      // The workspace is immutable once a conversation exists.
                      // For a new chat, clicking opens the folder picker to
                      // choose the per-conversation root before the first send.
                      disabled={Boolean(activeConversationID) || Boolean(activeStream)}
                      onClick={chooseDraftWorkspace}
                      aria-label={activeConversationID
                        ? `Workspace locked to ${displayedWorkspace}`
                        : 'Choose workspace for this conversation'}
                      title={activeConversationID
                        ? `Workspace locked to ${displayedWorkspace}`
                        : 'Choose workspace for this conversation'}
                    >
                      <svg className="workspace-chip-icon" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                        <path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z" />
                      </svg>
                      <code>{shortenHomePath(displayedWorkspace)}</code>
                    </button>
                    <label className="file-button" aria-label="Attach file" title="Attach file">
                      {/* Inline icon keeps the composer row compact — the text
                          label was pushing the submit row onto a new line at
                          narrow widths. The title/aria-label preserve meaning.
                          A paperclip reads as generic attach (image or audio)
                          rather than implying image-only. */}
                      <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                        <path d="M21.44 11.05l-9.19 9.19a6 6 0 0 1-8.49-8.49l9.19-9.19a4 4 0 0 1 5.66 5.66l-9.2 9.19a2 2 0 0 1-2.83-2.83l8.49-8.48" />
                      </svg>
                      <input type="file" accept="image/*,audio/*,video/*" multiple onChange={(event) => addFiles(event.target.files)} />
                    </label>
                  </div>
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
                          <ModelCombobox
                            id="primary-model"
                            ariaLabel="Model for next message"
                            placeholder="Type to filter models..."
                            value={model}
                            onChange={setModel}
                            options={primaryModelOptions}
                          />
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

function ModelCombobox({
  id,
  value,
  onChange,
  options,
  placeholder,
  ariaLabel,
  allowCustom = false,
}: {
  id: string;
  value: string;
  onChange: (value: string) => void;
  options: {value: string; label: string}[];
  placeholder?: string;
  ariaLabel?: string;
  // When true, text typed that matches no option is committed verbatim (on
  // Enter or click-away) instead of being discarded. Used for fal, whose
  // catalog is a discovery aid but where an arbitrary endpoint id is still
  // valid input.
  allowCustom?: boolean;
}) {
  // `filter` is null whenever the user isn't actively typing; in that state
  // the input shows the committed `value` prop DIRECTLY, so a provider switch
  // (which changes `value`) is reflected immediately with no sync-effect in
  // between. While focused/typing, `filter` holds the local search text.
  const [filter, setFilter] = useState<string | null>(null);
  const [open, setOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement | null>(null);

  // commitCustom persists free-typed text (only when allowCustom). It reads the
  // latest filter via a ref so the document-level listeners below don't need to
  // re-subscribe on every keystroke.
  const filterRef = useRef<string | null>(null);
  filterRef.current = filter;
  const commitCustom = () => {
    const typed = (filterRef.current ?? '').trim();
    if (allowCustom && typed && typed !== value) {
      onChange(typed);
    }
  };

  const close = (commit = false) => {
    if (commit) {
      commitCustom();
    }
    setFilter(null);
    setOpen(false);
  };

  useEffect(() => {
    if (!open) {
      return;
    }
    const onPointerDown = (event: MouseEvent) => {
      if (containerRef.current && event.target instanceof Node && containerRef.current.contains(event.target)) {
        return;
      }
      close(true);
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        close();
      }
    };
    document.addEventListener('mousedown', onPointerDown);
    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('mousedown', onPointerDown);
      document.removeEventListener('keydown', onKeyDown);
    };
  }, [open]);

  const normalizedFilter = (filter ?? '').trim().toLowerCase();
  const filtered = normalizedFilter
    ? options.filter((option) => option.label.toLowerCase().includes(normalizedFilter) || option.value.toLowerCase().includes(normalizedFilter))
    : options;

  const selectOption = (option: {value: string; label: string}) => {
    onChange(option.value);
    close();
  };

  const displayValue = filter !== null ? filter : value;

  return (
    <div className="model-combobox" ref={containerRef}>
      <input
        id={id}
        type="text"
        autoComplete="off"
        aria-label={ariaLabel}
        aria-expanded={open}
        role="combobox"
        placeholder={placeholder}
        value={displayValue}
        onFocus={() => {
          // Start a fresh filter over the full list; closing without a pick
          // restores the committed value.
          setFilter('');
          setOpen(true);
        }}
        onChange={(event) => {
          // Typing only updates the local filter text; the parent `model` is
          // committed solely via selectOption. Pushing every keystroke up to
          // the parent would churn the option list and trigger its
          // "snap to a valid model" effect, reverting the input mid-type.
          setFilter(event.target.value);
          setOpen(true);
        }}
        onKeyDown={(event) => {
          if (event.key !== 'Enter' || !open) {
            return;
          }
          event.preventDefault();
          if (filtered.length) {
            selectOption(filtered[0]);
          } else if (allowCustom) {
            close(true);
          }
        }}
      />
      {open && filtered.length ? (
        <ul className="model-combobox-list" role="listbox">
          {filtered.map((option) => (
            <li key={option.value}>
              <button
                type="button"
                className={option.value === value ? 'active' : undefined}
                role="option"
                aria-selected={option.value === value}
                onMouseDown={(event) => event.preventDefault()}
                onClick={() => selectOption(option)}
              >
                {option.label}
              </button>
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
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

// falModelOptionList builds sorted combobox options for a fal model catalog.
// fal's catalog reuses one display name across endpoint variants — e.g.
// fal-ai/speech-to-text, .../turbo, .../stream, and .../turbo/stream all carry
// the display name "Speech-to-Text", making them indistinguishable in a
// dropdown. To disambiguate without guessing fal's org/name split (ambiguous
// from the id alone), we only annotate when a display name is shared by more
// than one endpoint: each colliding entry gets its distinguishing id tail
// (everything after the shared prefix) appended. Non-colliding entries keep
// their clean display name.
function falModelOptionList(models: main.FalModel[] | null | undefined): {value: string; label: string}[] {
  const items = asArray(models).map((item) => ({id: item.id || '', base: item.displayName || item.id || ''}));
  // Group by display name to find collisions.
  const counts: Record<string, number> = {};
  for (const item of items) {
    const key = item.base.toLowerCase();
    counts[key] = (counts[key] ?? 0) + 1;
  }
  return items
    .map((item) => {
      let label = item.base;
      if ((counts[item.base.toLowerCase()] ?? 0) > 1) {
        // Find the longest id prefix shared by every colliding endpoint, then
        // append whatever follows it as the distinguishing tail.
        const colliders = items.filter((other) => other.base.toLowerCase() === item.base.toLowerCase()).map((other) => other.id);
        const tail = idTailAfterSharedPrefix(item.id, colliders);
        if (tail) {
          label = `${item.base} (${tail})`;
        }
      }
      return {value: item.id, label};
    })
    .sort((a, b) => a.label.localeCompare(b.label));
}

// idTailAfterSharedPrefix returns the portion of id that follows the longest
// slash-delimited prefix shared by all the given ids. For the speech-to-text
// collision (fal-ai/speech-to-text, .../turbo, .../stream, .../turbo/stream)
// the shared prefix is "fal-ai/speech-to-text", so the tails are "", "turbo",
// "stream", "turbo/stream" — and an empty tail collapses back to the base name.
function idTailAfterSharedPrefix(id: string, ids: string[]): string {
  if (ids.length === 0) {
    return '';
  }
  const splitIds = ids.map((other) => other.split('/').filter(Boolean));
  const thisSegments = id.split('/').filter(Boolean);
  let shared = 0;
  const minLen = Math.min(...splitIds.map((segments) => segments.length));
  for (let i = 0; i < minLen; i++) {
    if (splitIds.every((segments) => segments[i] === thisSegments[i])) {
      shared = i + 1;
    } else {
      break;
    }
  }
  return thisSegments.slice(shared).join('/');
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

function historyVideos(contents: main.HistoryContent[] | null | undefined): string[] {
  return asArray(contents)
    .filter((content) => content.type === 'video')
    .map((content) => content.text || content.path || '')
    .filter(Boolean);
}

function historyAudios(contents: main.HistoryContent[] | null | undefined): string[] {
  return asArray(contents)
    .filter((content) => content.type === 'audio')
    .map((content) => content.text || content.path || '')
    .filter(Boolean);
}

function imagePayloadForOllama(image: string): string {
  const match = /^data:image\/[a-z+.-]+;base64,(.*)$/i.exec(image);
  if (match) {
    return match[1];
  }
  // Not an inline data URL — e.g. a hydrated /atelier-artifact/ history URL or a
  // file path. These are display references, not valid model image payloads, so
  // drop them rather than sending a string Ollama can't base64-decode.
  return '';
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

function formatFalError(error: unknown): string {
  const message = formatError(error);
  const lower = message.toLowerCase();
  if (lower.includes('authentication failed') || lower.includes('401') || lower.includes('unauthorized')) {
    return 'Invalid API key — check your fal.ai key in Settings';
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

async function readImageFile(file: File, nameOverride?: string): Promise<Attachment> {
  const dataURL = await new Promise<string>((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.onerror = () => reject(reader.error);
    reader.readAsDataURL(file);
  });

  return {
    name: nameOverride ?? file.name,
    src: dataURL,
    payload: imagePayloadForOllama(dataURL),
    kind: 'image',
  };
}

// readAudioFile mirrors readImageFile but keeps the full data URL as the
// payload — unlike Ollama-bound images, the OpenRouter input_audio part needs
// the data:audio/<fmt>;base64,... wrapper so openRouterInputAudio can split off
// the format. Audio input is OpenRouter-only; the harness rejects an audio turn
// on any other provider before it runs.
async function readAudioFile(file: File, nameOverride?: string): Promise<Attachment> {
  const dataURL = await new Promise<string>((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.onerror = () => reject(reader.error);
    reader.readAsDataURL(file);
  });

  return {
    name: nameOverride ?? file.name,
    src: dataURL,
    payload: dataURL,
    kind: 'audio',
  };
}

// readVideoFile mirrors readAudioFile: it keeps the full data URL as the
// payload, since video input is tool-only and the backend resolves
// AttachedVideo from the data URL the frontend sends (decodeVideoPayload /
// readVideoArtifactAsDataURL).
async function readVideoFile(file: File, nameOverride?: string): Promise<Attachment> {
  const dataURL = await new Promise<string>((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.onerror = () => reject(reader.error);
    reader.readAsDataURL(file);
  });

  return {
    name: nameOverride ?? file.name,
    src: dataURL,
    payload: dataURL,
    kind: 'video',
  };
}

// readFileAsAttachment dispatches on MIME type to the right reader. Audio files
// route to readAudioFile, video files to readVideoFile; everything else (images
// today, and an unknown type the OS picker let through) routes to readImageFile,
// which produced the historical behavior.
async function readFileAsAttachment(file: File, nameOverride?: string): Promise<Attachment> {
  if (file.type.startsWith('audio/')) {
    return readAudioFile(file, nameOverride);
  }
  if (file.type.startsWith('video/')) {
    return readVideoFile(file, nameOverride);
  }
  return readImageFile(file, nameOverride);
}

function imageExtensionForType(type: string): string {
  const subtype = type.split('/')[1]?.split(';')[0]?.trim();
  if (!subtype) {
    return '.png';
  }
  return `.${subtype === 'jpeg' ? 'jpg' : subtype}`;
}

function audioExtensionForType(type: string): string {
  const subtype = type.split('/')[1]?.split(';')[0]?.trim().toLowerCase();
  switch (subtype) {
    case 'wav':
    case 'wave':
    case 'x-wav':
      return '.wav';
    case 'ogg':
    case 'opus':
      return '.ogg';
    case 'flac':
      return '.flac';
    case 'mp4':
    case 'aac':
    case 'x-m4a':
      return '.m4a';
    default:
      return '.mp3';
  }
}

function videoExtensionForType(type: string): string {
  const subtype = type.split('/')[1]?.split(';')[0]?.trim().toLowerCase();
  switch (subtype) {
    case 'webm':
      return '.webm';
    case 'quicktime':
      return '.mov';
    default:
      return '.mp4';
  }
}

// mediaExtensionForType picks a fallback extension for a synthesized filename
// (pasted/dropped media that has no name), branching on the MIME category.
function mediaExtensionForType(type: string): string {
  if (type.startsWith('audio/')) {
    return audioExtensionForType(type);
  }
  if (type.startsWith('video/')) {
    return videoExtensionForType(type);
  }
  return imageExtensionForType(type);
}

export default App;
