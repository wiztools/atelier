import {useEffect, useMemo, useRef, useState} from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import './App.css';
import {
  CancelStream,
  CheckFalConnection,
  CheckForUpdates,
  CheckOllama,
  ChooseToolWorkspace,
  ClearOpenAICompatibleAPIKey,
  CreateLibrary,
  CreateProject,
  DeleteConversation,
  DeleteLibrary,
  DeleteProject,
  GetConversation,
  GetConfig,
  HasFalAPIKey,
  HasOpenAICompatibleAPIKey,
  HasOpenRouterAPIKey,
  InstallUpdate,
  ListConversationAssets,
  ListConversations,
  ListFalModels,
  ListFalImageEditModels,
  ListFalVideoModels,
  ListFalVideoImageModels,
  ListFalVideoExtendModels,
  ListFalVideoMotionModels,
  ListFalVideoUpscaleModels,
  ListFalVideoDurations,
  ListFalSpeechModels,
  ListFalSoundEffectModels,
  ListFalTranscribeModels,
  ListFalUpscaleModels,
  ListFalLipsyncImageModels,
  ListFalLipsyncVideoModels,
  ListLibraries,
  ListLibraryAssets,
  ListModels,
  ListOpenAICompatibleModels,
  ListPrimaryModels,
  ListProjectConversations,
  MoveConversationToProject,
  PurgeArchivedConversations,
  RandomEmptyStatePrompt,
  RenameLibrary,
  RenameProject,
  ResolveToolPermission,
  SaveImage,
  SearchConversations,
  SaveVideo,
  SaveAudio,
  SaveConfig,
  SaveFalAPIKey,
  SaveOpenAICompatibleAPIKey,
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
  // providerResponse.tool block from a persisted media turn — the legacy
  // record of which generation model ran, kept so media usage can be
  // summarized for turns saved before tool_call activities carried media
  // fields (conv_a27d6008).
  mediaTool?: MediaToolSummaryView;
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

type UpdateAvailableEvent = {
  current: string;
  latest: string;
  notes?: string;
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

// Model-call step kinds carry per-call token usage; bookkeeping steps
// (queued/saved/evaluation/tool_call) never do, so summing over just these
// counts each model call exactly once.
const MODEL_CALL_STEP_KINDS = ['triage', 'skill', 'planning', 'model_call', 'streaming'];

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
  promptTokens?: number;
  firstTokenMs?: number;
  request?: HarnessRequestSnapshotView;
  tools?: HarnessToolActivityView[];
};

// Fingerprint of one model request (hashes/sizes only — raw prompts are never
// persisted). promptHash covers the prefix-cache-sensitive prefix, so equal
// hashes across consecutive same-model steps mean the prefix cache stayed
// warm.
type HarnessRequestSnapshotView = {
  promptHash?: string;
  toolsHash?: string;
  promptChars?: number;
  toolMode?: string;
  numCtx?: number;
  truncatedMessages?: number;
};

type HarnessToolActivityView = {
  name?: string;
  status?: string;
  // Media-generation telemetry: the backend that rendered the media
  // ("fal"/"ollama"/"openai-compatible"), the resolved generation model a
  // media tool actually ran, what it produced ("video"/"audio"/"image"), and
  // how many. Media models burn no tokens, so these — not token counts — are
  // their consumption record. Empty for non-media tools and failed calls.
  provider?: string;
  model?: string;
  mediaKind?: string;
  mediaCount?: number;
  path?: string;
  command?: string[];
  exitCode?: number;
  stdoutPreview?: string;
  stderrPreview?: string;
  durationMs?: number;
  error?: string;
  permission?: string;
  permissionWaitMs?: number;
};

// Payload of the atelier:harness-run event: the full run snapshot after every
// step transition, identical in shape to the persisted harnessRun record.
type HarnessRunEventView = {
  requestId: string;
  conversationId?: string;
  run: unknown;
};

// providerResponse.tool block persisted with media turns: which generation
// family ran (video_generation / image_generation / audio_generation), on
// which model, and how much it produced. Superseded for new turns by the
// media fields on tool_call activities; still the only record on old ones.
type MediaToolSummaryView = {
  name?: string;
  model?: string;
  videoCount?: number;
  audioCount?: number;
  imageCount?: number;
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
// The assets panel snaps closed when a drag crosses minAssetsPanelWidth —
// below it the media cards no longer render properly. maxAssetsPanelWidth
// keeps room for the chat column, mirroring the sidebar clamps.
const minAssetsPanelWidth = 240;
const maxAssetsPanelWidth = 560;
const defaultAssetsPanelWidth = 300;
const compactHistoryLimit = 10;
const expandedHistoryBatchSize = 20;
const defaultImageAspectRatio = '1:1';
const defaultImageSizePreset = '1k';
const defaultImageSteps = 24;
const imageAspectRatioOptions = ['1:1', '16:9', '9:16', '4:3', '3:4', '3:2', '2:3', '21:9'];
type ImageSizePreset = {value: string; label: string; longEdge: number};
const imageSizePresetOptions: ImageSizePreset[] = [
  {value: '1k', label: '1K', longEdge: 1024},
  {value: '2k', label: '2K', longEdge: 2048},
  {value: '4k', label: '4K', longEdge: 4096},
];
const defaultFalImageModel = 'fal-ai/flux/schnell';
const defaultFalImageEditModel = 'fal-ai/flux/dev/image-to-image';
const defaultFalVideoModel = 'fal-ai/kling-video/v2/master/text-to-video';
const defaultFalVideoImageModel = 'fal-ai/kling-video/v2/master/image-to-video';
const defaultFalVideoExtendModel = 'fal-ai/veo3.1/extend-video';
const defaultFalVideoMotionModel = 'fal-ai/kling-video/v2.6/pro/motion-control';
const defaultFalVideoUpscaleModel = 'fal-ai/video-upscaler';
const defaultFalAudioModel = 'fal-ai/elevenlabs/tts/multilingual-v2';
const defaultFalAudioCloneModel = 'fal-ai/f5-tts';
const defaultFalSoundEffectsModel = 'fal-ai/elevenlabs/sound-effects/v2';
const defaultFalTranscribeModel = 'fal-ai/wizper';
const defaultFalLipsyncImageModel = 'fal-ai/kling-video/lipsync/audio-to-video';
const defaultFalLipsyncVideoModel = 'fal-ai/sync-lipsync/v2/pro';
const defaultFalUpscaleModel = 'fal-ai/esrgan';
const defaultVideoDuration = '5';
const defaultVideoAspectRatio = '16:9';
// 'auto' lets the video model size the clip to the prompt (Seedance supports it;
// other models drop it with a notice via the backend enum-guard). These are the
// generic fallback options shown when a model's published schema can't be
// loaded (offline / no key); each picker fetches its own model-specific set via
// ListFalVideoDurations. Modern models accept clips up to 30s, so the fallback
// ladder tops out there — a value the selected model rejects is dropped with a
// notice by the backend enum-guard. Labels map raw values to friendlier option
// text — 'auto' reads better than a bare token.
const defaultVideoDurationOptions = ['auto', '5', '10', '15', '30'];
const videoDurationLabels: Record<string, string> = { auto: 'Auto' };
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
  type ChatProviderID = 'ollama' | 'openrouter' | 'openai-compatible';
  const [harnessProvider, setHarnessProvider] = useState<ChatProviderID>('ollama');
  // The harness model is remembered per provider, mirroring primaryModels, so
  // switching providers restores that provider's last selection instead of
  // stranding an Ollama model name under OpenRouter.
  const [harnessModels, setHarnessModels] = useState<Record<ChatProviderID, string>>({ollama: '', openrouter: '', 'openai-compatible': ''});
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
  const [primaryProvider, setPrimaryProvider] = useState<ChatProviderID>('ollama');
  // The primary model is remembered per provider so switching providers
  // restores that provider's last selection (falling back to the first
  // available model when the remembered one isn't in the current list).
  const [primaryModels, setPrimaryModels] = useState<Record<ChatProviderID, string>>({ollama: '', openrouter: '', 'openai-compatible': ''});
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
  // Ollama is no longer offered as an image provider in the UI (its runtime
  // dropped image-generation support); a config still saying "ollama" loads as
  // the local OpenAI-compatible server. The Go backend keeps accepting the id.
  const [imageProvider, setImageProvider] = useState<'fal' | 'openai-compatible'>('openai-compatible');
  const [openaiCompatibleBaseURL, setOpenaiCompatibleBaseURL] = useState('http://localhost:8080');
  const [openaiCompatibleModel, setOpenaiCompatibleModel] = useState('');
  const [openaiCompatibleModels, setOpenaiCompatibleModels] = useState<string[]>([]);
  const [openaiCompatibleKeyInput, setOpenaiCompatibleKeyInput] = useState('');
  const [openaiCompatibleHasKey, setOpenaiCompatibleHasKey] = useState(false);
  const [openaiCompatibleStatus, setOpenaiCompatibleStatus] = useState<'unknown' | 'ok' | 'error'>('unknown');
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
  const [falVideoMotionModel, setFalVideoMotionModel] = useState(defaultFalVideoMotionModel);
  const [falVideoMotionModels, setFalVideoMotionModels] = useState<main.FalModel[]>([]);
  const [falVideoUpscaleModel, setFalVideoUpscaleModel] = useState(defaultFalVideoUpscaleModel);
  const [falVideoUpscaleModels, setFalVideoUpscaleModels] = useState<main.FalModel[]>([]);
  const [falAudioModel, setFalAudioModel] = useState(defaultFalAudioModel);
  const [falAudioModels, setFalAudioModels] = useState<main.FalModel[]>([]);
  const [falAudioCloneModel, setFalAudioCloneModel] = useState(defaultFalAudioCloneModel);
  const [falSoundEffectsModel, setFalSoundEffectsModel] = useState(defaultFalSoundEffectsModel);
  const [falSoundEffectModels, setFalSoundEffectModels] = useState<main.FalModel[]>([]);
  const [falTranscribeModel, setFalTranscribeModel] = useState(defaultFalTranscribeModel);
  const [falTranscribeModels, setFalTranscribeModels] = useState<main.FalModel[]>([]);
  const [falLipsyncImageModel, setFalLipsyncImageModel] = useState(defaultFalLipsyncImageModel);
  const [falLipsyncVideoModel, setFalLipsyncVideoModel] = useState(defaultFalLipsyncVideoModel);
  const [falLipsyncImageModels, setFalLipsyncImageModels] = useState<main.FalModel[]>([]);
  const [falLipsyncVideoModels, setFalLipsyncVideoModels] = useState<main.FalModel[]>([]);
  const [falUpscaleModel, setFalUpscaleModel] = useState(defaultFalUpscaleModel);
  const [falUpscaleModels, setFalUpscaleModels] = useState<main.FalModel[]>([]);
  // Each video picker owns its duration options + value, driven by its model's
  // published schema (ListFalVideoDurations). videoDuration (text-to-video) is
  // the canonical value persisted to config.generation.video.duration — the
  // backend reads one shared duration for all three video paths
  // (tools_registry.go), and resolveVideoBody drops values invalid for the
  // actually-selected model with a notice, so the image/extend pickers are a
  // per-model discovery/preview aid that stays correct where the value is valid.
  const [videoDuration, setVideoDuration] = useState(defaultVideoDuration);
  const [videoDurationImage, setVideoDurationImage] = useState(defaultVideoDuration);
  const [videoDurationExtend, setVideoDurationExtend] = useState(defaultVideoDuration);
  const [videoDurationOptions, setVideoDurationOptions] = useState<string[]>(defaultVideoDurationOptions);
  const [videoDurationImageOptions, setVideoDurationImageOptions] = useState<string[]>(defaultVideoDurationOptions);
  const [videoDurationExtendOptions, setVideoDurationExtendOptions] = useState<string[]>(defaultVideoDurationOptions);
  const [videoAspectRatio, setVideoAspectRatio] = useState(defaultVideoAspectRatio);
  const [system, setSystem] = useState('You are Atelier, a precise local AI collaborator.');
  const [prompt, setPrompt] = useState('');
  const [attachments, setAttachments] = useState<Attachment[]>([]);
  const [composerDragging, setComposerDragging] = useState(false);
  const composerDragDepth = useRef(0);
  // @-mention autocomplete state. mentionOpen/mentionIndex drive renders;
  // mentionStateRef holds the active {@-position, query} for the keydown and
  // accept handlers to read synchronously (avoiding stale-closure reads of
  // prompt/selection during rapid typing).
  const [mentionOpen, setMentionOpen] = useState(false);
  const [mentionIndex, setMentionIndex] = useState(0);
  const mentionStateRef = useRef<{ at: number; query: string } | null>(null);
  const composerRef = useRef<HTMLDivElement | null>(null);
  const [chat, setChat] = useState<ChatEntry[]>([]);
  const [emptyPrompt, setEmptyPrompt] = useState<main.EmptyStatePrompt | null>(null);
  const [collapsedThinkingIDs, setCollapsedThinkingIDs] = useState<Record<string, boolean>>({});
  const [copiedMessageID, setCopiedMessageID] = useState('');
  const [copiedConversationID, setCopiedConversationID] = useState('');
  const [conversations, setConversations] = useState<main.ConversationSummary[]>([]);
  const [historyExpanded, setHistoryExpanded] = useState(false);
  const [visibleHistoryCount, setVisibleHistoryCount] = useState(compactHistoryLimit);
  // historyQuery drives the sidebar's full-history search; results replace
  // the conversation list while non-empty.
  const [historyQuery, setHistoryQuery] = useState('');
  // searchOpen reveals the sidebar search field; it collapses back to the icon
  // only when both closed by the user and the query is empty.
  const [searchOpen, setSearchOpen] = useState(false);
  const [historyResults, setHistoryResults] = useState<main.ConversationSearchResult[]>([]);
  const [historySearchBusy, setHistorySearchBusy] = useState(false);
  const [historySearchError, setHistorySearchError] = useState('');
  const [historySearchTruncated, setHistorySearchTruncated] = useState(false);
  const historySearchSeqRef = useRef(0);
  const [activeConversationID, setActiveConversationID] = useState('');
  // Libraries & projects (FCP-inspired tree): libraries state mirrors
  // ListLibraries; expandedLibraryIDs/expandedProjectIDs drive the sidebar
  // tree; projectConversations caches each expanded project's listing.
  // librariesRefreshTick re-fetches both when a turn finishes or a mutation
  // lands, the same role assetsRefreshTick plays for the assets panel.
  const [libraries, setLibraries] = useState<main.LibrarySummary[]>([]);
  const [librariesOpen, setLibrariesOpen] = useState(true);
  const [expandedLibraryIDs, setExpandedLibraryIDs] = useState<Record<string, boolean>>({});
  const [expandedProjectIDs, setExpandedProjectIDs] = useState<Record<string, boolean>>({});
  const [projectConversations, setProjectConversations] = useState<Record<string, main.ConversationSummary[]>>({});
  const [librariesRefreshTick, setLibrariesRefreshTick] = useState(0);
  // Inline creation/rename state for the library tree; editingContainerID is
  // the lib_/proj_ record being renamed (prefix tells the submitter which
  // binding to call).
  const [creatingLibrary, setCreatingLibrary] = useState(false);
  const [newLibraryName, setNewLibraryName] = useState('');
  const [creatingProjectLibraryID, setCreatingProjectLibraryID] = useState('');
  const [newProjectName, setNewProjectName] = useState('');
  const [editingContainerID, setEditingContainerID] = useState('');
  const [editingContainerName, setEditingContainerName] = useState('');
  const [openContainerMenuID, setOpenContainerMenuID] = useState('');
  const [confirmDeleteContainerID, setConfirmDeleteContainerID] = useState('');
  const [containerBusy, setContainerBusy] = useState(false);
  // pendingProject is the {projectID, libraryID} a NEW chat will be filed into
  // on its first send (ChatRequest.projectId). Set by "New chat" inside a
  // project; cleared once the conversation exists (the record carries its own
  // membership from there) and when opening any existing conversation.
  const [pendingProject, setPendingProject] = useState<{projectID: string; libraryID: string} | null>(null);
  // Mirror refs: the File-menu event subscriptions and the ⌘N keydown listener
  // run with empty deps, and executeChatStream reads the pending project at
  // send time — both need current values, not first-render captures.
  const pendingProjectRef = useRef<{projectID: string; libraryID: string} | null>(null);
  const lastProjectRef = useRef<{projectID: string; libraryID: string} | null>(null);
  const lastExpandedLibraryIDRef = useRef('');
  // Assets panel: closed by default; lists the active conversation's derived
  // media assets (ListConversationAssets) — or, when the conversation is
  // project-scoped, every asset in its library (ListLibraryAssets).
  // assetsRefreshTick re-runs the fetch when a turn finishes — the index
  // derives from persisted history, so it lags until the turn is saved.
  const [assetsPanelOpen, setAssetsPanelOpen] = useState(false);
  const [assetsWidth, setAssetsWidth] = useState(loadAssetsPanelWidth);
  const [resizingAssets, setResizingAssets] = useState(false);
  const [conversationAssets, setConversationAssets] = useState<main.ConversationAsset[]>([]);
  const [libraryAssets, setLibraryAssets] = useState<main.ConversationAsset[]>([]);
  const [assetsRefreshTick, setAssetsRefreshTick] = useState(0);
  // Accepted asset mentions ({label, id}) from the current composition. Ref,
  // not state: read synchronously at send time, cleared on send and on
  // conversation switch. submitChat drops any mention whose @token no longer
  // appears in the prompt text.
  const mentionedAssetsRef = useRef<{label: string; id: string}[]>([]);
  useEffect(() => {
    // Mentions recorded for the previous conversation must not leak into the
    // next one's send. Runs on conversation switch only — not on
    // assetsRefreshTick, which fires mid-composition when a turn finishes.
    mentionedAssetsRef.current = [];
  }, [activeConversationID]);
  useEffect(() => {
    if (!activeConversationID) {
      setConversationAssets([]);
      return;
    }
    let cancelled = false;
    ListConversationAssets(activeConversationID)
      .then((assets) => {
        if (!cancelled) setConversationAssets(asArray(assets));
      })
      .catch(() => {
        if (!cancelled) setConversationAssets([]);
      });
    return () => {
      cancelled = true;
    };
  }, [activeConversationID, assetsRefreshTick]);
  useEffect(() => {
    pendingProjectRef.current = pendingProject;
    if (pendingProject) {
      lastProjectRef.current = pendingProject;
    }
  }, [pendingProject]);
  // The library a project belongs to, from the loaded tree. Empty when the
  // project is unknown (dangling record) — callers treat that as no context.
  function libraryIDForProject(projectID: string): string {
    if (!projectID) {
      return '';
    }
    for (const library of libraries) {
      if (asArray(library.projects).some((project) => project.id === projectID)) {
        return library.id;
      }
    }
    return '';
  }
  // The project the ACTIVE conversation belongs to (from its summary), and the
  // library scope the composer + assets panel use: the pending project while
  // composing a new chat inside one, else the active conversation's project.
  const activeConversationProjectID = activeConversationID
    ? conversations.find((item) => item.id === activeConversationID)?.projectId ?? ''
    : '';
  const composerLibraryID = pendingProject?.libraryID
    || (activeConversationProjectID ? libraryIDForProject(activeConversationProjectID) : '');
  // Library-scoped assets refresh like the conversation's do: the fold derives
  // from persisted history, so a finished turn's artifacts need a re-fetch.
  useEffect(() => {
    if (!composerLibraryID) {
      setLibraryAssets([]);
      return;
    }
    let cancelled = false;
    ListLibraryAssets(composerLibraryID)
      .then((assets) => {
        if (!cancelled) setLibraryAssets(asArray(assets));
      })
      .catch(() => {
        if (!cancelled) setLibraryAssets([]);
      });
    return () => {
      cancelled = true;
    };
  }, [composerLibraryID, assetsRefreshTick]);
  // Library tree: fetch on mount and whenever a turn finishes or a mutation
  // lands (librariesRefreshTick). Pure setters, safe from empty-deps event
  // handlers via the tick.
  useEffect(() => {
    let cancelled = false;
    ListLibraries()
      .then((items) => {
        if (!cancelled) setLibraries(asArray(items));
      })
      .catch(() => {
        if (!cancelled) setLibraries([]);
      });
    return () => {
      cancelled = true;
    };
  }, [librariesRefreshTick]);
  // Expanded projects' conversation listings refresh with the tree. Collapse
  // drops stale entries from the cache on the next expand via the same fetch.
  useEffect(() => {
    const openProjectIDs = Object.entries(expandedProjectIDs)
      .filter(([, open]) => open)
      .map(([projectID]) => projectID);
    if (!openProjectIDs.length) {
      return;
    }
    let cancelled = false;
    Promise.all(openProjectIDs.map((projectID) =>
      ListProjectConversations(projectID).catch(() => [] as main.ConversationSummary[]),
    )).then((results) => {
      if (cancelled) {
        return;
      }
      setProjectConversations((current) => {
        const next = {...current};
        openProjectIDs.forEach((projectID, index) => {
          next[projectID] = asArray(results[index]);
        });
        return next;
      });
    });
    return () => {
      cancelled = true;
    };
  }, [expandedProjectIDs, librariesRefreshTick]);
  // Re-roll the empty-screen greeting whenever the transcript becomes empty or
  // the active conversation changes, so a fresh prompt shows each time.
  const chatIsEmpty = chat.length === 0;
  useEffect(() => {
    if (!chatIsEmpty) return;
    let cancelled = false;
    RandomEmptyStatePrompt()
      .then((prompt) => {
        if (!cancelled) setEmptyPrompt(prompt);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [chatIsEmpty, activeConversationID]);
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
  // Self-update UI state: the banner rides the atelier:update-available event
  // (raised by startup, ticker, and manual checks alike); an install clicked
  // mid-turn queues until the last conversation finishes.
  const [updateAvailable, setUpdateAvailable] = useState<UpdateAvailableEvent | null>(null);
  const [updateBusy, setUpdateBusy] = useState(false);
  const [updateQueued, setUpdateQueued] = useState(false);
  const [updateError, setUpdateError] = useState('');
  const [updateCheckBusy, setUpdateCheckBusy] = useState(false);
  const [updateCheckStatus, setUpdateCheckStatus] = useState('');
  const [updatesConfig, setUpdatesConfig] = useState<main.ConfigUpdates | null>(null);
  // Updates wait out any running conversation; both the banner's queue and
  // the backend's InstallUpdate gate read this shape of "busy".
  const anyConversationInFlight = activeStream !== null || Object.keys(inFlightConversations).length > 0;
  const shellRef = useRef<HTMLElement | null>(null);
  const transcriptRef = useRef<HTMLDivElement | null>(null);
  const shouldFollowTranscriptRef = useRef(true);
  const visibleStreamRef = useRef<string | null>(null);
  const inFlightConversationsRef = useRef<Record<string, InFlightConversation>>({});
  const requestConversationRef = useRef<Record<string, {conversationID: string; kind: ConversationKind}>>({});
  const chatStreamDraftsRef = useRef<Record<string, ChatStreamDraft>>({});
  // Live harness runs keyed by requestID, mirrored from atelier:harness-run
  // events. The chat:chunk effect pattern: refs only, so the empty-deps event
  // listener never closes over stale state. Used to re-attach the live run
  // when the user switches conversations mid-stream.
  const harnessRunDraftsRef = useRef<Record<string, HarnessRunView>>({});
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
  // The standalone chats list hides project-scoped conversations — they live
  // under their library's project rows instead, so they are never orphaned in
  // neither place.
  const conversationList = asArray(conversations).filter((item) => !item.projectId);
  const visibleConversations = historyExpanded
    ? conversationList.slice(0, visibleHistoryCount)
    : conversationList.slice(0, compactHistoryLimit);
  const hasMoreConversations = visibleConversations.length < conversationList.length;
  const selectedConversationID = activeConversationID;
  const latestHarnessRun = [...chat].reverse().find((entry) => entry.role === 'assistant' && entry.harnessRun)?.harnessRun;
  // Live turns receive real run snapshots via atelier:harness-run, so the
  // latest entry's run is always current — streamed or persisted.
  const visibleHarnessRun = latestHarnessRun ?? null;
  const modelUsage = useMemo(() => summarizeModelUsage(chat), [chat]);
  const mediaUsage = useMemo(() => summarizeMediaUsage(chat), [chat]);
  // Panel view of the asset list: library-scoped when the composer sits in a
  // project context (every asset across the library's conversations), else the
  // active conversation's own. Deduped by ID (a @-mention re-reference adds
  // another entry for the same artifact — keep the newest turn's). The
  // conversation listing arrives oldest-first (reversed to newest-first); the
  // library fold already arrives newest-first.
  const panelAssets = useMemo(() => {
    const source = composerLibraryID ? libraryAssets : conversationAssets;
    const byID = new Map<string, main.ConversationAsset>();
    for (const asset of source) {
      byID.set(asset.id, asset);
    }
    const list = [...byID.values()];
    return composerLibraryID ? list : list.reverse();
  }, [composerLibraryID, libraryAssets, conversationAssets]);

  // Flatten the library tree into move targets for the conversation ⋮ menu.
  const moveTargets = useMemo(() => {
    const targets: {projectID: string; label: string}[] = [];
    for (const library of libraries) {
      for (const project of asArray(library.projects)) {
        targets.push({projectID: project.id, label: `${library.name} › ${project.name}`});
      }
    }
    return targets;
  }, [libraries]);

  // Names for the toolbar breadcrumb and composer chip: the library (and
  // project) the current composition is scoped to.
  const composerProjectNames = useMemo(() => {
    if (!composerLibraryID) {
      return null;
    }
    const library = libraries.find((item) => item.id === composerLibraryID);
    if (!library) {
      return null;
    }
    const projectID = pendingProject?.projectID || activeConversationProjectID;
    const project = projectID ? asArray(library.projects).find((item) => item.id === projectID) : undefined;
    return {libraryName: library.name, projectName: project?.name ?? ''};
  }, [composerLibraryID, libraries, pendingProject, activeConversationProjectID]);

  // One conversation row, shared by the standalone chats list and the project
  // listings: open/rename/archive behave identically; the ⋮ menu gains
  // project-move targets (standalone rows) or a move-to-standalone action
  // (project rows).
  function renderConversationRow(conversation: main.ConversationSummary) {
    const inFlight = inFlightConversations[conversation.id];
    const selected = selectedConversationID === conversation.id;
    const targets = conversation.projectId ? [] : moveTargets;
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
              {/* The running indicator mounts only while the turn streams —
                  idle rows don't reserve the slot, so the title runs to the
                  full row width and the kebab sits flush after hover reveal. */}
              {inFlight ? (
                <small
                  className="history-kind in-flight"
                  title="Running"
                  aria-label="Conversation running"
                >
                  <span className="history-spinner" />
                </small>
              ) : null}
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
                  {targets.length ? (
                    <>
                      <div className="history-menu-label">Move to project</div>
                      {targets.map((target) => (
                        <button key={target.projectID} onClick={() => void moveConversation(conversation, target.projectID)}>
                          {target.label}
                        </button>
                      ))}
                    </>
                  ) : null}
                  {conversation.projectId ? (
                    <button onClick={() => void moveConversation(conversation, '')}>Move to standalone</button>
                  ) : null}
                </div>
              ) : null}
            </div>
          </>
        )}
      </div>
    );
  }

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

  // Debounced sidebar search over full conversation history. The sequence
  // ref lets a slow earlier response be discarded when the query changes
  // again before it lands.
  useEffect(() => {
    const seq = ++historySearchSeqRef.current;
    const query = historyQuery.trim();
    if (!query) {
      setHistoryResults([]);
      setHistorySearchError('');
      setHistorySearchTruncated(false);
      setHistorySearchBusy(false);
      return;
    }
    setHistorySearchBusy(true);
    const timer = window.setTimeout(() => {
      SearchConversations(query, new main.SearchOptions())
        .then((response) => {
          if (seq !== historySearchSeqRef.current) {
            return;
          }
          setHistoryResults(asArray(response?.results));
          setHistorySearchTruncated(Boolean(response?.truncated));
          setHistorySearchError('');
        })
        .catch((error) => {
          if (seq !== historySearchSeqRef.current) {
            return;
          }
          setHistoryResults([]);
          setHistorySearchTruncated(false);
          setHistorySearchError(formatError(error));
        })
        .finally(() => {
          if (seq === historySearchSeqRef.current) {
            setHistorySearchBusy(false);
          }
        });
    }, 250);
    return () => window.clearTimeout(timer);
  }, [historyQuery]);

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
            videoMotionModel: falVideoMotionModel,
            videoUpscaleModel: falVideoUpscaleModel,
            audioModel: falAudioModel,
            soundEffectsModel: falSoundEffectsModel,
            audioCloneModel: falAudioCloneModel,
            transcribeModel: falTranscribeModel,
            upscaleModel: falUpscaleModel,
            lipsyncImageModel: falLipsyncImageModel,
            lipsyncVideoModel: falLipsyncVideoModel,
          },
          openaiCompatible: {
            baseURL: openaiCompatibleBaseURL,
            primary: primaryModels['openai-compatible'],
            harness: harnessModels['openai-compatible'],
            model: openaiCompatibleModel,
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
        // Round-trip the updater section untouched: it has no Settings UI yet,
        // and dropping it here would reset a manually-edited manifestUrl or
        // autoCheck on every unrelated settings save.
        updates: updatesConfig ?? undefined,
      })).catch((error) => {
        setStatus((current) => current ? {...current, error: String(error)} : current);
      });
    }, 400);
    return () => window.clearTimeout(timeout);
  }, [baseURL, configLoaded, falHasKey, falModel, falImageEditModel, falVideoModel, falVideoImageModel, falVideoExtendModel, falVideoMotionModel, falVideoUpscaleModel, falAudioModel, falAudioCloneModel, falSoundEffectsModel, falTranscribeModel, falUpscaleModel, falLipsyncImageModel, falLipsyncVideoModel, harnessModels, harnessProvider, imageAspectRatio, imageModel, imageProvider, imageSizePreset, imageSteps, openaiCompatibleBaseURL, openaiCompatibleModel, openRouterHasKey, primaryModels, primaryProvider, storageConfig, system, toolConfig, updatesConfig, videoAspectRatio, videoDuration]);

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
        // The finished turn's artifacts are now in history — re-derive the
        // asset list (and the library tree, whose project listings may have
        // gained a conversation) so an open panel shows them.
        setAssetsRefreshTick((tick) => tick + 1);
        setLibrariesRefreshTick((tick) => tick + 1);
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

  // Live harness telemetry: every step transition arrives as a full run
  // snapshot (the same shape history persists), so the panel renders real
  // in-flight state. Mirrors the chat:chunk effect — refs + setState only.
  useEffect(() => {
    const onHarnessRun = (event: HarnessRunEventView) => {
      if (!event?.requestId) {
        return;
      }
      const run = parseHarnessRun(event.run);
      if (!run) {
        return;
      }
      harnessRunDraftsRef.current[event.requestId] = run;
      const entryID = `assistant-${event.requestId}`;
      setChat((current) => current.map((entry) => entry.id === entryID ? {...entry, harnessRun: run} : entry));
    };
    EventsOn('atelier:harness-run', onHarnessRun);
    return () => EventsOff('atelier:harness-run');
  }, []);

  useEffect(() => {
    const onToolPermission = (event: ToolPermissionEvent) => {
      setToolPermissions((current) => current.some((item) => item.id === event.id) ? current : [...current, event]);
    };
    EventsOn('atelier:tool-permission', onToolPermission);
    return () => EventsOff('atelier:tool-permission');
  }, []);

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
    EventsOn('atelier:update-available', onUpdateAvailable);
    return () => EventsOff('atelier:update-available');
  }, []);

  // File-menu creation actions (main.go's File menu emits these; see the
  // menuNew* handlers in app.go). The refs always point at the latest handler
  // closures, so the empty-deps subscription never fires a stale one.
  useEffect(() => {
    EventsOn('atelier:menu-new-conversation', () => newConversationActionRef.current());
    EventsOn('atelier:menu-new-library', () => newLibraryActionRef.current());
    EventsOn('atelier:menu-new-project', () => newProjectActionRef.current());
    return () => {
      EventsOff('atelier:menu-new-conversation');
      EventsOff('atelier:menu-new-library');
      EventsOff('atelier:menu-new-project');
    };
  }, []);

  async function resolveToolPermission(permissionID: string, approved: boolean) {
    setToolPermissions((current) => current.filter((item) => item.id !== permissionID));
    try {
      await ResolveToolPermission(permissionID, approved);
    } catch (error) {
      setStartupError(formatError(error));
    }
  }

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

  async function checkForUpdates() {
    setUpdateCheckBusy(true);
    setUpdateCheckStatus('');
    try {
      const status = await CheckForUpdates();
      if (status.state === 'available') {
        setUpdateCheckStatus(`Atelier ${status.latestVersion} is available — install from the banner.`);
      } else if (status.state === 'current') {
        setUpdateCheckStatus(`Atelier is up to date (${status.currentVersion}).`);
      } else {
        setUpdateCheckStatus(`Update check failed: ${status.error || 'unknown error'}`);
      }
    } catch (error) {
      setUpdateCheckStatus(formatError(error));
    } finally {
      setUpdateCheckBusy(false);
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
    if (!resizingAssets) {
      return;
    }
    const onMouseMove = (event: MouseEvent) => {
      const right = shellRef.current?.getBoundingClientRect().right ?? window.innerWidth;
      const width = right - event.clientX;
      // Snap to close: dragging below the minimum renderable width closes the
      // panel (the toolbar toggle follows) instead of clamping to an unusable
      // sliver. The stored width never drops below the minimum, so reopening
      // restores the last good width.
      if (width < minAssetsPanelWidth) {
        setAssetsPanelOpen(false);
        setResizingAssets(false);
        return;
      }
      const max = Math.min(maxAssetsPanelWidth, window.innerWidth - 420);
      setAssetsWidth(clampAssetsPanelWidth(width, max));
    };
    const onMouseUp = () => setResizingAssets(false);
    window.addEventListener('mousemove', onMouseMove);
    window.addEventListener('mouseup', onMouseUp);
    return () => {
      window.removeEventListener('mousemove', onMouseMove);
      window.removeEventListener('mouseup', onMouseUp);
    };
  }, [resizingAssets]);

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
    window.localStorage.setItem('atelier.assetsPanelWidth', String(assetsWidth));
  }, [assetsWidth]);

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

  // Close the @-mention menu on outside click or document-level Escape. The
  // textarea's own Escape handling (above) closes it too; this covers clicks
  // elsewhere in the window. Clicks inside the composer are ignored so picking
  // an item doesn't immediately dismiss via the same gesture.
  useEffect(() => {
    if (!mentionOpen) {
      return;
    }
    const onPointerDown = (event: MouseEvent) => {
      if (composerRef.current && event.target instanceof Node && composerRef.current.contains(event.target)) {
        return;
      }
      closeMention();
    };
    document.addEventListener('mousedown', onPointerDown);
    return () => document.removeEventListener('mousedown', onPointerDown);
  }, [mentionOpen]);

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
  const falModelOptions = useMemo(() => falModelOptionList(falModels), [falModels]);
  const openaiCompatibleModelOptions = useMemo(
    () => openaiCompatibleModels.map((id) => ({value: id, label: id})),
    [openaiCompatibleModels],
  );

  const primaryModelOptions = useMemo(() => {
    if (primaryProvider === 'openrouter') {
      return asArray(openRouterModels)
        .map((item) => ({value: item.id, label: item.displayName || item.id}))
        .sort((a, b) => a.label.localeCompare(b.label));
    }
    if (primaryProvider === 'openai-compatible') {
      return openaiCompatibleModelOptions;
    }
    return modelOptions.map((name) => ({value: name, label: name}));
  }, [modelOptions, openRouterModels, openaiCompatibleModelOptions, primaryProvider]);
  const primaryModelIsValid = primaryModelOptions.some((option) => option.value === model);
  const harnessModelOptions = useMemo(() => {
    if (harnessProvider === 'openrouter') {
      return asArray(openRouterModels)
        .map((item) => ({value: item.id, label: item.displayName || item.id}))
        .sort((a, b) => a.label.localeCompare(b.label));
    }
    if (harnessProvider === 'openai-compatible') {
      return openaiCompatibleModelOptions;
    }
    return modelOptions.map((name) => ({value: name, label: name}));
  }, [harnessProvider, modelOptions, openRouterModels, openaiCompatibleModelOptions]);

  const falImageEditModelOptions = useMemo(() => falModelOptionList(falImageEditModels), [falImageEditModels]);

  const falVideoModelOptions = useMemo(() => falModelOptionList(falVideoModels), [falVideoModels]);

  const falVideoImageModelOptions = useMemo(() => falModelOptionList(falVideoImageModels), [falVideoImageModels]);

  const falVideoExtendModelOptions = useMemo(() => falModelOptionList(falVideoExtendModels), [falVideoExtendModels]);
  const falVideoMotionModelOptions = useMemo(() => falModelOptionList(falVideoMotionModels), [falVideoMotionModels]);
  const falVideoUpscaleModelOptions = useMemo(() => falModelOptionList(falVideoUpscaleModels), [falVideoUpscaleModels]);

  const falAudioModelOptions = useMemo(() => falModelOptionList(falAudioModels), [falAudioModels]);

  const falSoundEffectModelOptions = useMemo(() => falModelOptionList(falSoundEffectModels), [falSoundEffectModels]);

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

  // Each video picker fetches the duration values its selected model accepts
  // (ListFalVideoDurations → the model's published schema enum), then reconciles
  // the picker's current value into that set — so switching models never leaves
  // a duration selected that the model would 422 on (e.g. Seedance-only "auto"
  // on an integer-only model; see conv_4feb919a). The fetch refires only on
  // model change; the value reconcile reads the latest state inside the setter.
  // An empty/error result falls back to the generic option set.
  useEffect(() => {
    let cancelled = false;
    ListFalVideoDurations(falVideoModel)
      .then((durations) => {
        if (cancelled) return;
        const opts = durations && durations.length ? durations : defaultVideoDurationOptions;
        setVideoDurationOptions(opts);
        setVideoDuration((current) => (opts.includes(current) ? current : opts[0] ?? defaultVideoDuration));
      })
      .catch(() => setVideoDurationOptions(defaultVideoDurationOptions));
    return () => {
      cancelled = true;
    };
  }, [falVideoModel]);

  useEffect(() => {
    let cancelled = false;
    ListFalVideoDurations(falVideoImageModel)
      .then((durations) => {
        if (cancelled) return;
        const opts = durations && durations.length ? durations : defaultVideoDurationOptions;
        setVideoDurationImageOptions(opts);
        setVideoDurationImage((current) => (opts.includes(current) ? current : opts[0] ?? defaultVideoDuration));
      })
      .catch(() => setVideoDurationImageOptions(defaultVideoDurationOptions));
    return () => {
      cancelled = true;
    };
  }, [falVideoImageModel]);

  useEffect(() => {
    let cancelled = false;
    ListFalVideoDurations(falVideoExtendModel)
      .then((durations) => {
        if (cancelled) return;
        const opts = durations && durations.length ? durations : defaultVideoDurationOptions;
        setVideoDurationExtendOptions(opts);
        setVideoDurationExtend((current) => (opts.includes(current) ? current : opts[0] ?? defaultVideoDuration));
      })
      .catch(() => setVideoDurationExtendOptions(defaultVideoDurationOptions));
    return () => {
      cancelled = true;
    };
  }, [falVideoExtendModel]);

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
    const needsLocalCatalog = primaryProvider === 'openai-compatible' || harnessProvider === 'openai-compatible';
    if (needsLocalCatalog && openaiCompatibleModels.length === 0 && openaiCompatibleStatus !== 'error') {
      refreshOpenAICompatibleModels();
    }
  }, [primaryProvider, harnessProvider, openRouterHasKey, openRouterModels.length, openRouterStatus, openaiCompatibleModels.length, openaiCompatibleStatus]);

  async function loadConfig() {
    const config = await GetConfig();
    const nextBaseURL = config.providers?.ollama?.baseURL || defaultBaseURL;
    const nextPrimaryModel = config.providers?.ollama?.models?.primary ?? '';
    const nextOpenRouterModel = config.providers?.openrouter?.primary ?? '';
    const nextOpenAICompatiblePrimary = config.providers?.openaiCompatible?.primary ?? '';
    const nextPrimaryProvider: ChatProviderID = config.models?.primaryProvider === 'openrouter'
      ? 'openrouter'
      : config.models?.primaryProvider === 'openai-compatible'
        ? 'openai-compatible'
        : 'ollama';
    const nextHarnessModel = config.providers?.ollama?.models?.harness || nextPrimaryModel;
    const nextOpenRouterHarness = config.providers?.openrouter?.harness ?? '';
    const nextOpenAICompatibleHarness = config.providers?.openaiCompatible?.harness ?? '';
    const nextHarnessProvider: ChatProviderID = config.models?.harnessProvider === 'openrouter'
      ? 'openrouter'
      : config.models?.harnessProvider === 'openai-compatible'
        ? 'openai-compatible'
        : 'ollama';
    const nextImageModel = config.providers?.ollama?.models?.image ?? '';
    const nextSystem = config.prompts?.system || 'You are Atelier, a precise local AI collaborator.';
    const nextImageAspectRatio = config.generation?.image?.aspectRatio || defaultImageAspectRatio;
    const nextImageSizePreset = config.generation?.image?.sizePreset || defaultImageSizePreset;
    const nextImageSteps = config.generation?.image?.steps || defaultImageSteps;
    const nextImageProvider = config.models?.imageProvider === 'fal' ? 'fal' : 'openai-compatible';
    const nextOpenAICompatibleBaseURL = config.providers?.openaiCompatible?.baseURL || 'http://localhost:8080';
    const nextOpenAICompatibleModel = config.providers?.openaiCompatible?.model ?? '';
    const nextFalModel = config.providers?.fal?.model || defaultFalImageModel;
    const nextFalImageEditModel = config.providers?.fal?.imageEditModel || defaultFalImageEditModel;
	const nextFalVideoModel = config.providers?.fal?.videoModel || defaultFalVideoModel;
	const nextFalVideoImageModel = config.providers?.fal?.videoImageModel || defaultFalVideoImageModel;
	const nextFalVideoExtendModel = config.providers?.fal?.videoExtendModel || defaultFalVideoExtendModel;
	const nextFalVideoMotionModel = config.providers?.fal?.videoMotionModel || defaultFalVideoMotionModel;
	const nextFalVideoUpscaleModel = config.providers?.fal?.videoUpscaleModel || defaultFalVideoUpscaleModel;
	const nextFalAudioModel = config.providers?.fal?.audioModel || defaultFalAudioModel;
	const nextFalAudioCloneModel = config.providers?.fal?.audioCloneModel || defaultFalAudioCloneModel;
	const nextFalSoundEffectsModel = config.providers?.fal?.soundEffectsModel || defaultFalSoundEffectsModel;
	const nextFalTranscribeModel = config.providers?.fal?.transcribeModel || defaultFalTranscribeModel;
	const nextFalLipsyncImageModel = config.providers?.fal?.lipsyncImageModel || defaultFalLipsyncImageModel;
	const nextFalLipsyncVideoModel = config.providers?.fal?.lipsyncVideoModel || defaultFalLipsyncVideoModel;
    const nextFalUpscaleModel = config.providers?.fal?.upscaleModel || defaultFalUpscaleModel;
    const nextVideoDuration = config.generation?.video?.duration || defaultVideoDuration;
    const nextVideoAspectRatio = config.generation?.video?.aspectRatio || defaultVideoAspectRatio;

    setStartupError('');
    setStorageConfig(config.storage ?? null);
    setToolConfig(config.tools ?? null);
    setUpdatesConfig(config.updates ?? null);
    setBaseURL(nextBaseURL);
    setPrimaryModels({ollama: nextPrimaryModel, openrouter: nextOpenRouterModel, 'openai-compatible': nextOpenAICompatiblePrimary});
    setPrimaryProvider(nextPrimaryProvider);
    setHarnessModels({ollama: nextHarnessModel, openrouter: nextOpenRouterHarness, 'openai-compatible': nextOpenAICompatibleHarness});
    setHarnessProvider(nextHarnessProvider);
    setImageModel(nextImageModel);
    setSystem(nextSystem);
    setImageAspectRatio(nextImageAspectRatio);
    setImageSizePreset(nextImageSizePreset);
    setImageSteps(nextImageSteps);
    setImageProvider(nextImageProvider);
    setOpenaiCompatibleBaseURL(nextOpenAICompatibleBaseURL);
    setOpenaiCompatibleModel(nextOpenAICompatibleModel);
    setFalModel(nextFalModel);
    setFalImageEditModel(nextFalImageEditModel);
    setFalVideoModel(nextFalVideoModel);
    setFalVideoImageModel(nextFalVideoImageModel);
    setFalVideoExtendModel(nextFalVideoExtendModel);
    setFalVideoMotionModel(nextFalVideoMotionModel);
    setFalVideoUpscaleModel(nextFalVideoUpscaleModel);
    setFalAudioModel(nextFalAudioModel);
    setFalAudioCloneModel(nextFalAudioCloneModel);
    setFalSoundEffectsModel(nextFalSoundEffectsModel);
    setFalTranscribeModel(nextFalTranscribeModel);
    setFalLipsyncImageModel(nextFalLipsyncImageModel);
    setFalLipsyncVideoModel(nextFalLipsyncVideoModel);
    setFalUpscaleModel(nextFalUpscaleModel);
    setVideoDuration(nextVideoDuration);
    // The backend persists one shared video duration; seed the image/extend
    // pickers from it too. Their per-picker effects correct to a value valid
    // for the selected model once its options load.
    setVideoDurationImage(nextVideoDuration);
    setVideoDurationExtend(nextVideoDuration);
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
      HasOpenAICompatibleAPIKey().then((hasKey) => {
        setOpenaiCompatibleHasKey(hasKey);
      }).catch(() => setOpenaiCompatibleHasKey(false)),
      refreshOpenAICompatibleModels(nextOpenAICompatibleBaseURL),
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

  // ---- Libraries & projects (FCP-inspired sidebar tree) ----

  function bumpLibrariesRefresh() {
    setLibrariesRefreshTick((tick) => tick + 1);
  }

  function startCreatingLibrary() {
    setView('app');
    setLibrariesOpen(true);
    setCreatingLibrary(true);
    setNewLibraryName('');
  }

  function startCreatingProject(library: main.LibrarySummary) {
    setView('app');
    setLibrariesOpen(true);
    setExpandedLibraryIDs((current) => ({...current, [library.id]: true}));
    lastExpandedLibraryIDRef.current = library.id;
    setCreatingProjectLibraryID(library.id);
    setNewProjectName('');
  }

  async function submitNewLibrary() {
    const name = newLibraryName.trim();
    setCreatingLibrary(false);
    if (!name) {
      return;
    }
    try {
      await CreateLibrary(name);
      setNewLibraryName('');
      bumpLibrariesRefresh();
    } catch (error) {
      setStartupError(formatError(error));
    }
  }

  async function submitNewProject(libraryID: string) {
    const name = newProjectName.trim();
    setCreatingProjectLibraryID('');
    if (!name) {
      return;
    }
    try {
      await CreateProject(libraryID, name);
      setNewProjectName('');
      bumpLibrariesRefresh();
    } catch (error) {
      setStartupError(formatError(error));
    }
  }

  function startEditingContainer(id: string, name: string) {
    setOpenContainerMenuID('');
    setEditingContainerID(id);
    setEditingContainerName(name);
  }

  function cancelEditingContainer() {
    setEditingContainerID('');
    setEditingContainerName('');
  }

  async function saveContainerName() {
    const id = editingContainerID;
    const name = editingContainerName.trim();
    cancelEditingContainer();
    if (!id || !name) {
      return;
    }
    try {
      if (id.startsWith('lib_')) {
        await RenameLibrary(id, name);
      } else {
        await RenameProject(id, name);
      }
      bumpLibrariesRefresh();
    } catch (error) {
      setStartupError(formatError(error));
    }
  }

  function handleContainerNameKeyDown(event: React.KeyboardEvent<HTMLInputElement>) {
    if (event.key === 'Enter') {
      event.preventDefault();
      void saveContainerName();
    }
    if (event.key === 'Escape') {
      event.preventDefault();
      cancelEditingContainer();
    }
  }

  // Opening/toggling a container ⋮ menu always disarms any pending delete
  // confirmation, so the destructive second step can never linger into a
  // different menu.
  function toggleContainerMenu(id: string) {
    setConfirmDeleteContainerID('');
    setOpenContainerMenuID((current) => current === id ? '' : id);
  }

  // Deleting a library or project is a HARD delete — conversations and their
  // artifacts leave the disk — so the ⋮ item is a two-step confirm showing
  // what goes with it. The backend refuses while any member chat is running.
  async function confirmDeleteContainer(id: string) {
    if (containerBusy) {
      return;
    }
    setContainerBusy(true);
    setOpenContainerMenuID('');
    setConfirmDeleteContainerID('');
    try {
      if (id.startsWith('lib_')) {
        await DeleteLibrary(id);
      } else {
        await DeleteProject(id);
      }
      const remaining = await refreshConversations();
      bumpLibrariesRefresh();
      // Drop session drafts for conversations that no longer exist, and reset
      // the view if the open conversation was deleted with its project.
      const liveIDs = new Set(remaining.map((item) => item.id));
      for (const draftID of Object.keys(composerDraftsRef.current)) {
        if (draftID !== '' && !liveIDs.has(draftID)) {
          delete composerDraftsRef.current[draftID];
        }
      }
      if (activeConversationID && !liveIDs.has(activeConversationID)) {
        await resetWorkspace();
      }
    } catch (error) {
      setStartupError(formatError(error));
    } finally {
      setContainerBusy(false);
    }
  }

  function toggleLibraryExpanded(libraryID: string) {
    setExpandedLibraryIDs((current) => {
      const open = !current[libraryID];
      if (open) {
        lastExpandedLibraryIDRef.current = libraryID;
      }
      return {...current, [libraryID]: open};
    });
  }

  function toggleProjectExpanded(projectID: string) {
    setExpandedProjectIDs((current) => ({...current, [projectID]: !current[projectID]}));
  }

  // New chat inside a project: resets the composer and stashes the project so
  // the first send pins ChatRequest.projectId onto the new conversation.
  async function startNewChatInProject(projectID: string, libraryID: string) {
    const context = {projectID, libraryID};
    lastProjectRef.current = context;
    await resetWorkspace();
    setPendingProject(context);
  }

  // The File-menu / ⌘N "New Conversation": FCP-style context awareness —
  // keep composing in the pending project if one is already active, else the
  // project of the conversation being viewed, else the last project the user
  // worked in, else a standalone chat.
  function handleNewConversationAction() {
    if (pendingProjectRef.current) {
      void startNewChat();
      return;
    }
    const activeProjectID = activeConversationIDRef.current
      ? conversations.find((item) => item.id === activeConversationIDRef.current)?.projectId ?? ''
      : '';
    const context = activeProjectID
      ? {projectID: activeProjectID, libraryID: libraryIDForProject(activeProjectID)}
      : lastProjectRef.current;
    if (context && context.libraryID) {
      void startNewChatInProject(context.projectID, context.libraryID);
    } else {
      void startNewChat();
    }
  }

  // File → New Project…: target the last expanded (or first) library; with no
  // library yet, fall through to the new-library input — a project needs a home.
  function handleNewProjectAction() {
    const targetLibraryID = lastExpandedLibraryIDRef.current
      || asArray(libraries)[0]?.id
      || '';
    if (!targetLibraryID) {
      startCreatingLibrary();
      return;
    }
    const library = libraries.find((item) => item.id === targetLibraryID) ?? asArray(libraries)[0];
    if (library) {
      startCreatingProject(library);
    }
  }

  async function moveConversation(conversation: main.ConversationSummary, projectID: string) {
    setOpenHistoryMenuID('');
    try {
      await MoveConversationToProject(conversation.id, projectID);
      await refreshConversations();
      bumpLibrariesRefresh();
    } catch (error) {
      setStartupError(formatError(error));
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
      setPrimaryModels((current) => (current.ollama ? current : {...current, ollama: firstModel}));
      // Target the Ollama slot explicitly: this default comes from the Ollama
      // catalog and must not land in the OpenRouter slot when that provider is
      // the active one.
      setHarnessModels((current) => (current.ollama ? current : {...current, ollama: firstModel}));
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
      setFalVideoMotionModels(asArray(await ListFalVideoMotionModels()));
    } catch {
      setFalVideoMotionModels([]);
    }
    try {
      setFalVideoUpscaleModels(asArray(await ListFalVideoUpscaleModels()));
    } catch {
      setFalVideoUpscaleModels([]);
    }
    try {
      setFalAudioModels(asArray(await ListFalSpeechModels()));
    } catch {
      setFalAudioModels([]);
    }
    try {
      setFalSoundEffectModels(asArray(await ListFalSoundEffectModels()));
    } catch {
      setFalSoundEffectModels([]);
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
      setFalSoundEffectModels([]);
      setFalTranscribeModels([]);
      setFalLipsyncImageModels([]);
      setFalLipsyncVideoModels([]);
      setFalStatus('unknown');
      setFalError('');
      setImageProvider((current) => current === 'fal' ? 'openai-compatible' : current);
    } catch (error) {
      setStatus((current) => current ? {...current, error: String(error)} : current);
    }
  }

  // The OpenAI-compatible image server list is a discovery aid like fal's
  // catalog: a server without /v1/models only loses the suggestions, never the
  // free-text model field. The status feeds the Providers tab endpoint row.
  async function refreshOpenAICompatibleModels(endpoint = openaiCompatibleBaseURL) {
    try {
      const ids = await ListOpenAICompatibleModels(endpoint);
      setOpenaiCompatibleModels(asArray(ids));
      setOpenaiCompatibleStatus('ok');
    } catch {
      setOpenaiCompatibleModels([]);
      setOpenaiCompatibleStatus('error');
    }
  }

  async function saveOpenAICompatibleKey() {
    try {
      await SaveOpenAICompatibleAPIKey(openaiCompatibleKeyInput);
      setOpenaiCompatibleKeyInput('');
      setOpenaiCompatibleHasKey(await HasOpenAICompatibleAPIKey());
    } catch (error) {
      setStatus((current) => current ? {...current, error: String(error)} : current);
    }
  }

  async function clearOpenAICompatibleKey() {
    try {
      await ClearOpenAICompatibleAPIKey();
      setOpenaiCompatibleHasKey(false);
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
    // A blank composer is standalone unless the caller (startNewChatInProject)
    // immediately re-stashes a project context for this composition.
    setPendingProject(null);
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
        // Same context-aware flow as File → New Conversation: the native menu
        // accelerator usually intercepts ⌘N first; this web-side listener
        // covers the case where it doesn't (both paths are idempotent).
        newConversationActionRef.current();
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

  // focusTurnID, when set, scrolls the opened transcript to that turn's
  // message — used by history search results to land on the match.
  async function openConversationSummary(conversation: main.ConversationSummary, focusTurnID = '') {
    try {
      // Opening an existing conversation supersedes any pending in-project
      // composition — the panel and mention scope follow its own project.
      setPendingProject(null);
      const detail = await GetConversation(conversation.id);
      setView('app');
      hydrateChatConversation(detail);
      if (focusTurnID) {
        window.setTimeout(() => {
          document.getElementById(`msg-${focusTurnID}`)?.scrollIntoView({behavior: 'smooth', block: 'center'});
        }, 80);
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
      mediaTool: turn.providerResponse?.tool as MediaToolSummaryView | undefined,
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
        harnessRun: harnessRunDraftsRef.current[visibleRequestID],
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
      setProjectConversations((current) => {
        const next: Record<string, main.ConversationSummary[]> = {};
        for (const [projectID, members] of Object.entries(current)) {
          next[projectID] = members.filter((item) => item.id !== conversation.id);
        }
        return next;
      });
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
  // referencedAssetIds carries @-mentioned asset IDs; the retry path omits it
  // and the backend's latest-wins walk picks up the persisted mention refs.
  async function executeChatStream(opts: {requestID: string; requestMessages: main.ChatMessage[]; referencedAssetIds?: string[]}) {
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
        // Same turn-1-only lifecycle as the workspace: the pending project the
        // composition started in, pinned onto the new conversation's record.
        ...(activeConversationID ? {} : pendingProjectRef.current?.projectID ? {projectId: pendingProjectRef.current.projectID} : {}),
        ...(opts.referencedAssetIds?.length ? {referencedAssetIds: opts.referencedAssetIds} : {}),
      }));
      markConversationInFlight(start.conversationId, start.requestID, 'chat');
      setActiveConversationID(start.conversationId);
      // The conversation now carries its own membership — the pending context
      // has done its job.
      setPendingProject(null);
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
    // @-mentioned assets ride the request as IDs (backend resolves them into
    // the tool attachment slots); the readable @token stays in the message
    // text for the model. A mention whose token was deleted from the text is
    // dropped here.
    const referencedAssetIds = mentionedAssetsRef.current
      .filter((mention) => trimmed.includes(`@${mention.label}`))
      .map((mention) => mention.id);
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
    mentionedAssetsRef.current = [];
    // The composer contents are being sent, not stashed — drop any stored draft
    // for the current key ('' for a brand-new chat) so it isn't resurrected.
    delete composerDraftsRef.current[activeConversationIDRef.current];
    shouldFollowTranscriptRef.current = true;
    setChat((entries) => [
      ...entries,
      userEntry,
      {id: `assistant-${requestID}`, role: 'assistant', content: '', streaming: true, provider: primaryProvider},
    ]);
    await executeChatStream({requestID, requestMessages, referencedAssetIds});
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

  // Latest filtered mention candidates, mirrored into a ref so the keydown and
  // accept handlers read the same list that the rendered menu shows without
  // racing against React state updates during rapid typing.
  const [mentionMatchesState, setMentionMatchesState] = useState<MentionCandidate[]>([]);
  const mentionMatchesRef = useRef<MentionCandidate[]>([]);
  mentionMatchesRef.current = mentionMatchesState;

  // File-menu handler mirrors: the menu-event subscription runs with empty
  // deps, so it must call through to the latest closure, not the first one.
  const newConversationActionRef = useRef(() => {});
  const newLibraryActionRef = useRef(() => {});
  const newProjectActionRef = useRef(() => {});
  newConversationActionRef.current = handleNewConversationAction;
  newLibraryActionRef.current = startCreatingLibrary;
  newProjectActionRef.current = handleNewProjectAction;

  // mentionCandidates is the autocomplete pool: this turn's attachments first
  // (most immediate), then the referable assets newest-first. In a project
  // context the pool is the whole library — every conversation's uploads and
  // generated media, per-conversation provenance in the hint — so an
  // @-mention can cite any asset in the library. Asset candidates carry their
  // assetID so acceptMention can record the reference.
  function mentionCandidates(): MentionCandidate[] {
    const source = composerLibraryID ? libraryAssets : panelAssets;
    const assets: MentionCandidate[] = source.map((asset) => ({
      name: assetMentionLabel(asset),
      src: asset.url ?? '',
      payload: '',
      kind: asset.kind === 'audio' ? 'audio' : asset.kind === 'video' ? 'video' : 'image',
      assetID: asset.id,
      hint: composerLibraryID
        ? `${asset.role === 'user' ? 'attached' : 'generated'} · ${asset.conversationTitle || 'another chat'}`
        : `${asset.role === 'user' ? 'attached' : 'generated'} · ${assetTurnLabel(asset.originTurnId)}`,
    }));
    return [...attachmentsRef.current, ...assets];
  }

  function handleChatPromptChange(event: React.ChangeEvent<HTMLTextAreaElement>) {
    const value = event.target.value;
    const caret = event.target.selectionStart ?? value.length;
    setPrompt(value);
    const match = detectMentionAt(value, caret);
    mentionStateRef.current = match;
    if (match) {
      const matches = mentionMatches(match.query, mentionCandidates());
      setMentionMatchesState(matches);
      setMentionIndex(0);
      setMentionOpen(matches.length > 0);
    } else {
      setMentionOpen(false);
    }
  }

  function closeMention() {
    setMentionOpen(false);
    mentionStateRef.current = null;
  }

  // acceptMention replaces the open @-token with @<name> (trailing space) and
  // places the caret after it. Called from Enter/Tab/Click. No-op if the menu
  // is closed or the index is out of range.
  function acceptMention(index: number) {
    const matches = mentionMatchesRef.current;
    const match = mentionStateRef.current;
    if (!match || index < 0 || index >= matches.length) {
      closeMention();
      return;
    }
    const chosen = matches[index];
    const name = chosen.name;
    // A conversation-asset mention is recorded by ID; the inserted @token is
    // the readable label, and submitChat ships the IDs via
    // referencedAssetIds (dropping any whose token was later deleted).
    if (chosen.assetID && !mentionedAssetsRef.current.some((mention) => mention.id === chosen.assetID)) {
      mentionedAssetsRef.current.push({label: name, id: chosen.assetID});
    }
    const before = prompt.slice(0, match.at);
    const afterCaret = prompt.slice(match.at + 1 + match.query.length);
    const next = `${before}@${name} ${afterCaret}`;
    setPrompt(next);
    closeMention();
    // Restore focus and caret after React re-renders the new value.
    const caret = (before + `@${name} `).length;
    requestAnimationFrame(() => {
      const el = chatPromptRef.current;
      if (el) {
        el.focus();
        el.setSelectionRange(caret, caret);
      }
    });
  }

  function handleChatPromptKeyDown(event: React.KeyboardEvent<HTMLTextAreaElement>) {
    if (mentionOpen) {
      const matches = mentionMatchesRef.current;
      if (event.key === 'ArrowDown') {
        event.preventDefault();
        setMentionIndex((index) => (matches.length ? (index + 1) % matches.length : 0));
        return;
      }
      if (event.key === 'ArrowUp') {
        event.preventDefault();
        setMentionIndex((index) => (matches.length ? (index - 1 + matches.length) % matches.length : 0));
        return;
      }
      if (event.key === 'Enter' && !event.shiftKey) {
        event.preventDefault();
        acceptMention(mentionIndex);
        return;
      }
      if (event.key === 'Tab' && !event.shiftKey) {
        event.preventDefault();
        acceptMention(mentionIndex);
        return;
      }
      if (event.key === 'Escape') {
        event.preventDefault();
        closeMention();
        return;
      }
    } else if (event.key === 'Enter' && !event.shiftKey) {
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

  async function copyMessage(entry: ChatEntry) {
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
    appendAttachments(next);
  }

  // appendAttachments merges new attachments into the list, renaming any whose
  // name collides with an existing (or another incoming) attachment so every
  // name is unique. Names are the key @-mentions resolve against and back the
  // React key for attachment chips, so uniqueness must hold.
  function appendAttachments(incoming: Attachment[]) {
    setAttachments((items) => {
      const used = items.map((item) => item.name);
      const renamed = incoming.map((item) => {
        const name = uniqueAttachmentName(item.name, used);
        used.push(name);
        return name === item.name ? item : { ...item, name };
      });
      return [...items, ...renamed];
    });
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
    appendAttachments(next);
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
    appendAttachments(next);
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
      className={[
        'shell',
        view === 'settings' ? 'settings-open' : '',
        resizingSidebar ? 'resizing resizing-sidebar' : '',
        resizingAssets ? 'resizing resizing-assets' : '',
        view !== 'settings' && assetsPanelOpen ? 'assets-open' : '',
      ].filter(Boolean).join(' ')}
      style={view === 'settings' ? undefined : {
        '--sidebar-width': `${sidebarWidth}px`,
        '--assets-width': `${assetsWidth}px`,
      } as Record<string, string>}
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
              <div className="nav-top-row">
                <button className="nav-new-chat" onClick={startNewChat}>
                  <span className="nav-icon">+</span>
                  New chat
                </button>
                <button
                  className={`nav-search-toggle${searchOpen || historyQuery.trim() ? ' active' : ''}`}
                  onClick={() => setSearchOpen(true)}
                  aria-label="Search chats"
                  aria-expanded={searchOpen || Boolean(historyQuery.trim())}
                  title="Search chats"
                >
                  <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true">
                    <circle cx="7" cy="7" r="4.5" fill="none" stroke="currentColor" strokeWidth="1.6" />
                    <line x1="10.5" y1="10.5" x2="14" y2="14" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" />
                  </svg>
                </button>
              </div>
              {searchOpen || historyQuery.trim() ? (
                <div className="history-search">
                  <input
                    type="search"
                    autoFocus
                    value={historyQuery}
                    onChange={(event) => setHistoryQuery(event.target.value)}
                    onBlur={() => {
                      if (!historyQuery.trim()) {
                        setSearchOpen(false);
                      }
                    }}
                    onKeyDown={(event) => {
                      if (event.key === 'Escape') {
                        event.preventDefault();
                        setHistoryQuery('');
                        setSearchOpen(false);
                      }
                    }}
                    placeholder="Search chats…"
                    aria-label="Search conversation history"
                  />
                </div>
              ) : null}
            </nav>

            <div className="history-area" onScroll={handleHistoryScroll}>
              {historyQuery.trim() ? (
                <>
                  <div className="section-label">{historySearchBusy ? 'Searching…' : 'Results'}</div>
                  {historySearchError ? (
                    <div className="history-empty">{historySearchError}</div>
                  ) : historyResults.length ? (
                    historyResults.map((result) => (
                      <div key={result.conversation.id} className="search-result">
                        <button
                          className="search-result-open"
                          onClick={() => openConversationSummary(result.conversation, result.matches[0]?.turnId ?? '')}
                          title={result.conversation.title}
                        >
                          <span className={`search-result-title${result.titleMatched ? ' matched' : ''}`}>
                            {result.conversation.title || 'Untitled'}
                          </span>
                          {result.matches.map((match, index) => (
                            <small key={index} className="search-result-snippet">
                              <em>{match.role === 'user' ? 'You' : 'Model'}</em>
                              <span className="search-result-text">
                                {match.before}<mark>{match.match}</mark>{match.after}
                              </span>
                            </small>
                          ))}
                        </button>
                      </div>
                    ))
                  ) : !historySearchBusy ? (
                    <div className="history-empty">No matches in chat history.</div>
                  ) : null}
                  {historySearchTruncated ? (
                    <div className="history-empty">Older matches hidden — refine the query to narrow results.</div>
                  ) : null}
                </>
              ) : (
                <>
                  <div className="section-label">Chats</div>
                  {conversationList.length ? (
                    visibleConversations.map(renderConversationRow)
                  ) : (
                    <div className="history-empty">No conversations yet.</div>
                  )}
                  {hasMoreConversations ? (
                    <button className="history-more" onClick={showMoreConversations}>
                      More
                    </button>
                  ) : null}

                  <div className="libraries-section">
                    <div className="section-label libraries-header">
                      <button
                        type="button"
                        className="libraries-toggle"
                        onClick={() => setLibrariesOpen((open) => !open)}
                        aria-expanded={librariesOpen}
                      >
                        <span className={`tree-chevron${librariesOpen ? ' open' : ''}`} aria-hidden="true">▸</span>
                        Libraries
                      </button>
                      <button
                        type="button"
                        className="history-icon-button"
                        onClick={startCreatingLibrary}
                        aria-label="New library"
                        title="New library"
                      >
                        +
                      </button>
                    </div>
                    {librariesOpen ? (
                      <>
                        {creatingLibrary ? (
                          <input
                            className="container-name-input"
                            autoFocus
                            value={newLibraryName}
                            placeholder="Library name…"
                            onChange={(event) => setNewLibraryName(event.target.value)}
                            onKeyDown={(event) => {
                              if (event.key === 'Enter') {
                                event.preventDefault();
                                void submitNewLibrary();
                              }
                              if (event.key === 'Escape') {
                                event.preventDefault();
                                setCreatingLibrary(false);
                                setNewLibraryName('');
                              }
                            }}
                            onBlur={() => void submitNewLibrary()}
                          />
                        ) : null}
                        {libraries.length ? libraries.map((library) => {
                          const libraryOpen = Boolean(expandedLibraryIDs[library.id]);
                          return (
                            <div key={library.id} className="library-item">
                              {editingContainerID === library.id ? (
                                <input
                                  className="container-name-input"
                                  autoFocus
                                  value={editingContainerName}
                                  onChange={(event) => setEditingContainerName(event.target.value)}
                                  onKeyDown={handleContainerNameKeyDown}
                                  onBlur={() => void saveContainerName()}
                                />
                              ) : (
                                <div className={`container-row library-row${libraryOpen ? ' open' : ''}`}>
                                  <button
                                    type="button"
                                    className="container-open"
                                    onClick={() => toggleLibraryExpanded(library.id)}
                                    onDoubleClick={() => startEditingContainer(library.id, library.name)}
                                    title={library.name}
                                  >
                                    <span className={`tree-chevron${libraryOpen ? ' open' : ''}`} aria-hidden="true">▸</span>
                                    <span className="container-name">{library.name}</span>
                                    <small className="container-count">{asArray(library.projects).length || ''}</small>
                                  </button>
                                  <div className="history-actions">
                                    <button
                                      type="button"
                                      className="history-icon-button"
                                      onClick={() => startCreatingProject(library)}
                                      aria-label={`New project in ${library.name}`}
                                      title="New project"
                                    >
                                      +
                                    </button>
                                    <button
                                      type="button"
                                      className="history-icon-button"
                                      aria-label={`More actions for ${library.name}`}
                                      title="More"
                                      onClick={() => toggleContainerMenu(library.id)}
                                    >
                                      ⋮
                                    </button>
                                    {openContainerMenuID === library.id ? (
                                      <div className="history-menu">
                                        <button onClick={() => startCreatingProject(library)}>New Project</button>
                                        <button onClick={() => startEditingContainer(library.id, library.name)}>Rename</button>
                                        {confirmDeleteContainerID === library.id ? (
                                          <button className="menu-danger" disabled={containerBusy} onClick={() => void confirmDeleteContainer(library.id)}>
                                            Delete library and everything in it?
                                          </button>
                                        ) : (
                                          <button className="menu-danger" onClick={() => setConfirmDeleteContainerID(library.id)}>Delete…</button>
                                        )}
                                      </div>
                                    ) : null}
                                  </div>
                                </div>
                              )}
                              {libraryOpen && editingContainerID !== library.id ? (
                                <div className="library-children">
                                  {creatingProjectLibraryID === library.id ? (
                                    <input
                                      className="container-name-input"
                                      autoFocus
                                      value={newProjectName}
                                      placeholder="Project name…"
                                      onChange={(event) => setNewProjectName(event.target.value)}
                                      onKeyDown={(event) => {
                                        if (event.key === 'Enter') {
                                          event.preventDefault();
                                          void submitNewProject(library.id);
                                        }
                                        if (event.key === 'Escape') {
                                          event.preventDefault();
                                          setCreatingProjectLibraryID('');
                                          setNewProjectName('');
                                        }
                                      }}
                                      onBlur={() => void submitNewProject(library.id)}
                                    />
                                  ) : null}
                                  {asArray(library.projects).map((project) => {
                                    const projectOpen = Boolean(expandedProjectIDs[project.id]);
                                    return (
                                      <div key={project.id} className="project-item">
                                        {editingContainerID === project.id ? (
                                          <input
                                            className="container-name-input"
                                            autoFocus
                                            value={editingContainerName}
                                            onChange={(event) => setEditingContainerName(event.target.value)}
                                            onKeyDown={handleContainerNameKeyDown}
                                            onBlur={() => void saveContainerName()}
                                          />
                                        ) : (
                                          <div className={`container-row project-row${projectOpen ? ' open' : ''}`}>
                                            <button
                                              type="button"
                                              className="container-open"
                                              onClick={() => toggleProjectExpanded(project.id)}
                                              onDoubleClick={() => startEditingContainer(project.id, project.name)}
                                              title={project.name}
                                            >
                                              <span className={`tree-chevron${projectOpen ? ' open' : ''}`} aria-hidden="true">▸</span>
                                              <span className="container-name">{project.name}</span>
                                            </button>
                                            <div className="history-actions">
                                              <button
                                                type="button"
                                                className="history-icon-button"
                                                onClick={() => void startNewChatInProject(project.id, library.id)}
                                                aria-label={`New chat in ${project.name}`}
                                                title="New chat in project"
                                              >
                                                +
                                              </button>
                                              <button
                                                type="button"
                                                className="history-icon-button"
                                                aria-label={`More actions for ${project.name}`}
                                                title="More"
                                                onClick={() => toggleContainerMenu(project.id)}
                                              >
                                                ⋮
                                              </button>
                                              {openContainerMenuID === project.id ? (
                                                <div className="history-menu">
                                                  <button onClick={() => void startNewChatInProject(project.id, library.id)}>New Chat</button>
                                                  <button onClick={() => startEditingContainer(project.id, project.name)}>Rename</button>
                                                  {confirmDeleteContainerID === project.id ? (
                                                    <button className="menu-danger" disabled={containerBusy} onClick={() => void confirmDeleteContainer(project.id)}>
                                                      Delete project and its chats?
                                                    </button>
                                                  ) : (
                                                    <button className="menu-danger" onClick={() => setConfirmDeleteContainerID(project.id)}>Delete…</button>
                                                  )}
                                                </div>
                                              ) : null}
                                            </div>
                                          </div>
                                        )}
                                        {projectOpen && editingContainerID !== project.id ? (
                                          <div className="project-conversations">
                                            {asArray(projectConversations[project.id]).length ? (
                                              asArray(projectConversations[project.id]).map(renderConversationRow)
                                            ) : (
                                              <div className="history-empty">No chats in this project yet.</div>
                                            )}
                                          </div>
                                        ) : null}
                                      </div>
                                    );
                                  })}
                                  {!asArray(library.projects).length && creatingProjectLibraryID !== library.id ? (
                                    <button type="button" className="project-add" onClick={() => startCreatingProject(library)}>
                                      + Project
                                    </button>
                                  ) : null}
                                </div>
                              ) : null}
                            </div>
                          );
                        }) : (!creatingLibrary ? (
                          <div className="history-empty">No libraries yet.</div>
                        ) : null)}
                      </>
                    ) : null}
                  </div>
                </>
              )}
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
        {updateAvailable ? (
          <div className="update-banner">
            <div className="update-banner-content">
              <strong>Atelier {updateAvailable.latest} is available.</strong>
              {updateAvailable.notes ? <span>{updateAvailable.notes}</span> : null}
              {updateQueued ? (
                <span className="update-queued">Installing when the current conversation finishes…</span>
              ) : null}
              {updateError ? <span className="update-error">{updateError}</span> : null}
            </div>
            <div className="update-banner-actions">
              <button onClick={dismissUpdate} disabled={updateBusy}>Later</button>
              <button className="primary" onClick={requestUpdateInstall} disabled={updateBusy}>
                {updateBusy ? 'Installing…' : 'Install & Relaunch'}
              </button>
            </div>
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

              <section className="settings-section">
                <h3>OpenAI-compatible (local)</h3>
                <div className="connection">
                  <label htmlFor="openai-image-endpoint">
                    Local image server endpoint
                    <span className="help-tip" tabIndex={0}>
                      <span className="help-tip-icon" aria-hidden="true">?</span>
                      <span className="help-tip-text" role="tooltip">Any server speaking OpenAI's /v1/images/generations (LocalAI, a diffusers shim, ...). Used when Image Provider is set to OpenAI-compatible.</span>
                    </span>
                  </label>
                  <div className="endpoint-row">
                    <input
                      id="openai-image-endpoint"
                      value={openaiCompatibleBaseURL}
                      onChange={(event) => setOpenaiCompatibleBaseURL(event.target.value)}
                    />
                    <button onClick={() => refreshOpenAICompatibleModels()}>Refresh</button>
                  </div>
                  <div className={openaiCompatibleStatus === 'ok' ? 'status online' : 'status offline'}>
                    <span />
                    {openaiCompatibleStatus === 'ok'
                      ? `Online — ${openaiCompatibleModels.length} model${openaiCompatibleModels.length === 1 ? '' : 's'} listed`
                      : openaiCompatibleStatus === 'error'
                        ? 'Server unreachable or no /v1/models — enter model ids manually in Models.'
                        : 'Not checked.'}
                  </div>
                </div>
                <div className="connection">
                  <label htmlFor="openai-image-key">API Key (optional)</label>
                  <div className="endpoint-row">
                    <input
                      id="openai-image-key"
                      type="password"
                      placeholder={openaiCompatibleHasKey ? 'Key saved — enter a new key to replace it' : 'Leave empty for a local server without auth'}
                      value={openaiCompatibleKeyInput}
                      onChange={(event) => setOpenaiCompatibleKeyInput(event.target.value)}
                    />
                    <button type="button" className="icon-btn" onClick={saveOpenAICompatibleKey} disabled={!openaiCompatibleKeyInput} aria-label="Save key" title="Save key">
                      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                        <path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z" />
                        <polyline points="17 21 17 13 7 13 7 21" />
                        <polyline points="7 3 7 8 15 8" />
                      </svg>
                    </button>
                    {openaiCompatibleHasKey ? (
                      <button type="button" className="icon-btn" onClick={clearOpenAICompatibleKey} aria-label="Clear key" title="Clear key">
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                          <polyline points="3 6 5 6 21 6" />
                          <path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" />
                        </svg>
                      </button>
                    ) : <span className="icon-btn-placeholder" aria-hidden="true" />}
                  </div>
                  <div className={openaiCompatibleHasKey ? 'status online' : 'status offline'}>
                    <span />
                    {openaiCompatibleHasKey ? 'Bearer key saved' : 'No key — requests go unauthenticated.'}
                  </div>
                </div>
              </section>
              </>
              ) : null}

              {settingsTab === 'others' ? (
              <>
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
              <section className="settings-section">
                <h3>Updates</h3>
                <div className="storage-actions">
                  <button onClick={checkForUpdates} disabled={updateCheckBusy}>
                    {updateCheckBusy ? 'Checking...' : 'Check for Updates'}
                  </button>
                  {updateCheckStatus ? <span>{updateCheckStatus}</span> : null}
                </div>
                <div className="storage-hint">
                  Atelier checks for updates automatically once a day and installs only when you choose to.
                </div>
              </section>
              </>
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
                        onChange={(event) => setHarnessProvider(event.target.value as ChatProviderID)}
                      >
                        <option value="ollama">Ollama</option>
                        <option value="openrouter" disabled={!openRouterHasKey}>OpenRouter</option>
                        <option value="openai-compatible">OpenAI-compatible (local)</option>
                      </select>
                    </div>
                    <div className="field">
                      <label htmlFor="harness-model">Harness Model</label>
                      <div className="model-inline-control">
                        {harnessProvider === 'ollama' ? (
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
                        ) : (
                          <ModelCombobox
                            id="harness-model"
                            ariaLabel="Harness model"
                            placeholder="Type to filter models..."
                            value={harnessModel}
                            onChange={setHarnessModel}
                            options={harnessModelOptions}
                            allowCustom={harnessProvider === 'openai-compatible'}
                          />
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
                        onChange={(event) => setImageProvider(event.target.value as 'fal' | 'openai-compatible')}
                      >
                        <option value="fal">fal.ai (cloud)</option>
                        <option value="openai-compatible">OpenAI-compatible (local)</option>
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
                        <label htmlFor="openai-image-model">Default Image Model</label>
                        <ModelCombobox
                          id="openai-image-model"
                          ariaLabel="OpenAI-compatible image model"
                          placeholder="flux2-klein"
                          value={openaiCompatibleModel}
                          onChange={setOpenaiCompatibleModel}
                          options={openaiCompatibleModelOptions}
                          allowCustom
                        />
                        {openaiCompatibleModelOptions.length ? null : (
                          <span className="hint">Type a model id from your server — the model list couldn't be loaded.</span>
                        )}
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

                  <div className="field">
                    <label htmlFor="fal-upscale-model">Image-Upscale Model (fal.ai)</label>
                    <ModelCombobox
                      id="fal-upscale-model"
                      ariaLabel="fal.ai image-upscale model"
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
                  {/*
                    Each video picker is paired with its own duration dropdown,
                    whose options come from the selected model's published schema
                    (ListFalVideoDurations). Splitting per-picker means a model
                    that only accepts integers never offers Seedance-only "auto".
                    Only the text-to-video duration persists to config; the image
                    and extend pickers are a per-model preview that stays valid
                    where the value is acceptable to their model.
                  */}
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
                      <label htmlFor="video-duration">Text-to-Video Duration</label>
                      <select id="video-duration" value={videoDuration} onChange={(event) => setVideoDuration(event.target.value)}>
                        {videoDurationOptions.map((value) => <option key={value} value={value}>{videoDurationLabels[value] ?? value}</option>)}
                      </select>
                    </div>
                  </div>

                  <div className="two-column">
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

                    <div className="field">
                      <label htmlFor="video-duration-image">Image-to-Video Duration</label>
                      <select id="video-duration-image" value={videoDurationImage} onChange={(event) => setVideoDurationImage(event.target.value)}>
                        {videoDurationImageOptions.map((value) => <option key={value} value={value}>{videoDurationLabels[value] ?? value}</option>)}
                      </select>
                    </div>
                  </div>

                  <div className="two-column">
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

                    <div className="field">
                      <label htmlFor="video-duration-extend">Video-Extend Duration</label>
                      <select id="video-duration-extend" value={videoDurationExtend} onChange={(event) => setVideoDurationExtend(event.target.value)}>
                        {videoDurationExtendOptions.map((value) => <option key={value} value={value}>{videoDurationLabels[value] ?? value}</option>)}
                      </select>
                    </div>
                  </div>

                  <div className="field">
                    <label htmlFor="fal-video-motion-model">Motion-Control Model (fal.ai)</label>
                    <ModelCombobox
                      id="fal-video-motion-model"
                      ariaLabel="fal.ai motion-control model"
                      placeholder={defaultFalVideoMotionModel}
                      value={falVideoMotionModel}
                      onChange={setFalVideoMotionModel}
                      options={falVideoMotionModelOptions}
                      allowCustom
                    />
                  </div>

                  <div className="field">
                    <label htmlFor="fal-video-upscale-model">Video-Upscale Model (fal.ai)</label>
                    <ModelCombobox
                      id="fal-video-upscale-model"
                      ariaLabel="fal.ai video-upscale model"
                      placeholder={defaultFalVideoUpscaleModel}
                      value={falVideoUpscaleModel}
                      onChange={setFalVideoUpscaleModel}
                      options={falVideoUpscaleModelOptions}
                      allowCustom
                    />
                  </div>

                  <div className="field">
                    <label htmlFor="video-aspect">Video Aspect Ratio</label>
                    <select id="video-aspect" value={videoAspectRatio} onChange={(event) => setVideoAspectRatio(event.target.value)}>
                      {videoAspectRatioOptions.map((value) => <option key={value} value={value}>{value}</option>)}
                    </select>
                  </div>
                </div>
              </section>

              <section className="settings-section">
                <h3>Audio</h3>
                <div className="two-column">
                  <div className="field">
                    <label htmlFor="fal-audio-model">Speech Model (TTS, fal.ai)</label>
                    <ModelCombobox
                      id="fal-audio-model"
                      ariaLabel="fal.ai speech model"
                      placeholder={defaultFalAudioModel}
                      value={falAudioModel}
                      onChange={setFalAudioModel}
                      options={falAudioModelOptions}
                      allowCustom
                    />
                  </div>
                  <div className="field">
                    <label htmlFor="fal-audio-clone-model">Voice Cloning Model (fal.ai)</label>
                    <ModelCombobox
                      id="fal-audio-clone-model"
                      ariaLabel="fal.ai voice cloning model"
                      placeholder={defaultFalAudioCloneModel}
                      value={falAudioCloneModel}
                      onChange={setFalAudioCloneModel}
                      options={falAudioModelOptions}
                      allowCustom
                    />
                  </div>
                  <div className="field">
                    <label htmlFor="fal-sound-effects-model">Music &amp; Sound Effects Model (fal.ai)</label>
                    <ModelCombobox
                      id="fal-sound-effects-model"
                      ariaLabel="fal.ai sound effects model"
                      placeholder={defaultFalSoundEffectsModel}
                      value={falSoundEffectsModel}
                      onChange={setFalSoundEffectsModel}
                      options={falSoundEffectModelOptions}
                      allowCustom
                    />
                  </div>
                </div>
                {!falHasKey ? (
                  <span className="hint">Add a fal.ai API key above to generate speech, music, or sound effects.</span>
                ) : null}
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
                {composerProjectNames ? (
                  <div className="project-context" title="This chat is scoped to a library project">
                    {composerProjectNames.libraryName}{composerProjectNames.projectName ? ` › ${composerProjectNames.projectName}` : ''}
                  </div>
                ) : null}
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
              <div className="toolbar-right">
                <ConversationUsage usage={modelUsage} media={mediaUsage} />
                <button
                  type="button"
                  className={`assets-toggle${assetsPanelOpen ? ' active' : ''}`}
                  onClick={() => setAssetsPanelOpen((open) => !open)}
                  aria-label={assetsPanelOpen ? 'Hide assets panel' : 'Show assets panel'}
                  aria-pressed={assetsPanelOpen}
                  title={assetsPanelOpen ? 'Hide assets' : 'Show assets'}
                >
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                    <path d="m22 11-1.296-1.296a2.4 2.4 0 0 0-3.408 0L11 16" />
                    <path d="M4 8a2 2 0 0 0-2 2v10a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2" />
                    <circle cx="13" cy="7" r="1" fill="currentColor" />
                    <rect x="8" y="2" width="14" height="14" rx="2" />
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
                    <h2>{emptyPrompt?.heading ?? 'What are we making today?'}</h2>
                    <p>{emptyPrompt?.sub ?? 'Describe an image, video, or sound to begin.'}</p>
                  </div>
                ) : asArray(chat).map((entry) => {
                  const thinkingCollapsed = Boolean(entry.thinking && (collapsedThinkingIDs[entry.id] ?? !entry.streaming));
                  return (
                    <article key={entry.id} id={`msg-${entry.id}`} className={`message ${entry.role}`}>
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
                      {(entry.role === 'user' || entry.role === 'assistant') && entry.content ? (
                        <button
                          className="message-copy-button"
                          type="button"
                          aria-label={entry.role === 'user' ? 'Copy prompt' : 'Copy agent response'}
                          title={copiedMessageID === entry.id ? 'Copied' : entry.role === 'user' ? 'Copy prompt' : 'Copy response'}
                          onClick={() => copyMessage(entry)}
                        >
                          {copiedMessageID === entry.id ? '✓' : '⧉'}
                        </button>
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
                      {entry.role === 'assistant' && entry.harnessRun ? <TurnUsage run={entry.harnessRun} /> : null}
                      {entry.error ? <div className="error">{entry.error}</div> : null}
                    </article>
                  );
                })}
              </div>

              <div
                ref={composerRef}
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
                {mentionOpen && mentionMatchesState.length ? (
                  <ul className="mention-list" role="listbox">
                    {mentionMatchesState.map((item, index) => (
                      <li key={item.name} role="option" aria-selected={index === mentionIndex}>
                        <button
                          type="button"
                          className={index === mentionIndex ? 'mention-item active' : 'mention-item'}
                          onMouseDown={(event) => {
                            // mousedown (not click) fires before the textarea
                            // loses focus, so the accept caret-restore works.
                            event.preventDefault();
                            acceptMention(index);
                          }}
                          onMouseEnter={() => setMentionIndex(index)}
                        >
                          {item.kind === 'image' ? (
                            <img src={item.src} alt="" className="mention-thumb" />
                          ) : (
                            <span className="mention-icon" aria-hidden="true">
                              {item.kind === 'audio' ? '♪' : '▶'}
                            </span>
                          )}
                          <span className="mention-name">{item.name}</span>
                          {item.hint ? <span className="mention-hint">{item.hint}</span> : null}
                        </button>
                      </li>
                    ))}
                  </ul>
                ) : null}
                <textarea
                  ref={chatPromptRef}
                  value={prompt}
                  onChange={handleChatPromptChange}
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
                    {composerProjectNames ? (
                      <span
                        className="composer-project-chip"
                        title={`Filed under ${composerProjectNames.libraryName}${composerProjectNames.projectName ? ` › ${composerProjectNames.projectName}` : ''} — the assets panel and @-mentions scope to the library`}
                      >
                        <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                          <path d="M4 20h16a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13c0 1.1.9 2 2 2Z" />
                        </svg>
                        <code>{composerProjectNames.projectName || composerProjectNames.libraryName}</code>
                      </span>
                    ) : null}
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
                            onChange={(event) => setPrimaryProvider(event.target.value as ChatProviderID)}
                          >
                            <option value="ollama">Ollama</option>
                            <option value="openrouter">OpenRouter</option>
                            <option value="openai-compatible">OpenAI-compatible (local)</option>
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
                            allowCustom={primaryProvider === 'openai-compatible'}
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
      {view === 'settings' || !assetsPanelOpen ? null : (
        <div
          className="assets-resizer"
          role="separator"
          aria-orientation="vertical"
          aria-label="Resize assets panel"
          onMouseDown={(event) => {
            event.preventDefault();
            setResizingAssets(true);
          }}
        />
      )}
      {view === 'settings' || !assetsPanelOpen ? null : (
        <aside className="assets-panel" aria-label={composerLibraryID ? 'Library assets' : 'Conversation assets'}>
          <div className="assets-panel-header">
            <span className="assets-panel-title">
              {composerLibraryID && composerProjectNames ? `Assets · ${composerProjectNames.libraryName}` : 'Assets'}
            </span>
            <button
              type="button"
              className="assets-panel-close"
              onClick={() => setAssetsPanelOpen(false)}
              aria-label="Close assets panel"
              title="Close assets panel"
            >
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round">
                <path d="M18 6 6 18M6 6l12 12" />
              </svg>
            </button>
          </div>
          <div className="assets-list">
            {panelAssets.length === 0 ? (
              <p className="assets-empty">
                {composerLibraryID
                  ? 'No library assets yet. Uploads and generated media from every chat in this library show up here.'
                  : 'No assets yet. Images, audio, and video — attached or generated — show up here as the conversation produces them.'}
              </p>
            ) : (
              panelAssets.map((asset) => (
                <figure key={`${asset.id}-${asset.originTurnId}`} className={`asset-card asset-${asset.kind}`}>
                  <div className="asset-preview">
                    {asset.kind === 'image' && asset.url ? (
                      <button
                        type="button"
                        className="asset-image-button"
                        onClick={() => setPreviewImage(asset.url || '')}
                        aria-label="Preview image asset"
                        title="Preview image asset"
                      >
                        <img src={asset.url} alt="" loading="lazy" />
                      </button>
                    ) : asset.kind === 'audio' && asset.url ? (
                      <audio src={asset.url} controls preload="metadata" />
                    ) : asset.kind === 'video' && asset.url ? (
                      <video src={asset.url} controls preload="metadata" />
                    ) : (
                      <span className="asset-missing">Artifact file is missing on disk</span>
                    )}
                  </div>
                  <figcaption>
                    <span className="asset-kind">{asset.kind}</span>
                    <span className="asset-meta">
                      {asset.role === 'user' ? 'attached' : 'generated'} · {composerLibraryID ? (asset.conversationTitle || 'chat') : assetTurnLabel(asset.originTurnId)}
                    </span>
                  </figcaption>
                </figure>
              ))
            )}
          </div>
        </aside>
      )}
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

function loadAssetsPanelWidth(): number {
  const stored = Number(window.localStorage.getItem('atelier.assetsPanelWidth'));
  return clampAssetsPanelWidth(Number.isFinite(stored) && stored > 0 ? stored : defaultAssetsPanelWidth);
}

function clampAssetsPanelWidth(width: number, max = maxAssetsPanelWidth): number {
  return Math.round(Math.max(minAssetsPanelWidth, Math.min(Math.max(minAssetsPanelWidth, max), width)));
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

// Per-turn token footer: the turn's total in/out (plus duration once known)
// is always visible in the summary; expanding lists the per-model breakdown.
// Renders nothing when the run's providers reported no usage (e.g. fal media
// generation) — failed turns keep theirs, matching the persisted ledger.
function TurnUsage({run}: {run: HarnessRunView}) {
  const usage = summarizeRunUsage(run);
  if (!usage.length) {
    return null;
  }
  const totals = usage.reduce(
    (acc, row) => ({prompt: acc.prompt + row.promptTokens, completion: acc.completion + row.completionTokens}),
    {prompt: 0, completion: 0},
  );
  return (
    <details className="turn-usage">
      <summary>
        {[
          totals.prompt ? `${formatTokenCount(totals.prompt)} in` : '',
          totals.completion ? `${formatTokenCount(totals.completion)} out` : '',
          run.durationMs ? formatDuration(run.durationMs) : '',
        ]
          .filter(Boolean)
          .join(' · ')}
      </summary>
      <div className="turn-usage-rows">
        {usage.map((row) => (
          <div className="harness-usage-row" key={`${row.provider}-${row.model}`} title={`${row.provider} · ${row.calls} call${row.calls === 1 ? '' : 's'} in this turn`}>
            <span className="harness-usage-model">{row.model}</span>
            <span>
              {row.promptTokens ? `${formatTokenCount(row.promptTokens)} in` : '— in'}
              {' · '}
              {row.completionTokens ? `${formatTokenCount(row.completionTokens)} out` : '— out'}
            </span>
          </div>
        ))}
      </div>
    </details>
  );
}

// Conversation-wide token total that stays visible in the chat toolbar;
// expanding lists the per-model breakdown across every turn so far.
function ConversationUsage({usage, media = []}: {usage: ModelUsageRow[]; media?: MediaUsageRow[]}) {
  if (!usage.length && !media.length) {
    return null;
  }
  const totalTokens = usage.reduce((sum, row) => sum + row.promptTokens + row.completionTokens, 0);
  const mediaTotals = media.reduce(
    (acc, row) => ({video: acc.video + row.video, audio: acc.audio + row.audio, image: acc.image + row.image}),
    {video: 0, audio: 0, image: 0},
  );
  const mediaLabel = mediaCountsLabel(mediaTotals.video, mediaTotals.audio, mediaTotals.image);
  return (
    <details className="conversation-usage">
      <summary title="Token and media-generation usage across this conversation">
        {usage.length ? `${formatTokenCount(totalTokens)} tokens · ${usage.length} model${usage.length === 1 ? '' : 's'}` : 'No token usage'}
        {mediaLabel ? ` · ${mediaLabel}` : ''}
      </summary>
      {(usage.length || media.length) ? (
        <div className="conversation-usage-rows">
          {usage.map((row) => (
            <div className="harness-usage-row" key={`${row.provider}-${row.model}`} title={`${row.provider} · ${row.calls} call${row.calls === 1 ? '' : 's'} across this conversation`}>
              <span className="harness-usage-model">{row.model}</span>
              <span>
                {row.promptTokens ? `${formatTokenCount(row.promptTokens)} in` : '— in'}
                {' · '}
                {row.completionTokens ? `${formatTokenCount(row.completionTokens)} out` : '— out'}
              </span>
            </div>
          ))}
          {media.map((row) => (
            <div className="harness-usage-row harness-usage-media" key={`media-${row.provider}-${row.model}`} title={`${row.provider} · media generation · ${row.calls} call${row.calls === 1 ? '' : 's'} across this conversation`}>
              <span className="harness-usage-model">
                {row.provider !== '—' ? <span className="harness-usage-provider">{row.provider}</span> : null}
                {row.model}
              </span>
              <span>{mediaCountsLabel(row.video, row.audio, row.image)}</span>
            </div>
          ))}
        </div>
      ) : null}
    </details>
  );
}

function HarnessRunPanel({run}: {run: HarnessRunView}) {
  const steps = asArray(run.steps);
  const usage = summarizeRunUsage(run);
  const media = summarizeRunMediaUsage(run);
  const completed = steps.filter((step) => step.status === 'completed').length;
  const status = run.status ?? 'running';
  const stopReason = run.loop?.stopReason;
  const totals = usage.reduce(
    (acc, row) => ({prompt: acc.prompt + row.promptTokens, completion: acc.completion + row.completionTokens}),
    {prompt: 0, completion: 0},
  );
  const mediaTotals = media.reduce(
    (acc, row) => ({video: acc.video + row.video, audio: acc.audio + row.audio, image: acc.image + row.image}),
    {video: 0, audio: 0, image: 0},
  );
  const mediaLabel = mediaCountsLabel(mediaTotals.video, mediaTotals.audio, mediaTotals.image);
  // Prefix-cache signal: compare each model-call step's promptHash against the
  // previous request to the same provider+model. Equal hashes kept the cache
  // warm; a change (marked) invalidated it.
  const prefixChanged = new Set<number>();
  const lastHashByModel = new Map<string, string>();
  steps.forEach((step, index) => {
    const hash = step.request?.promptHash;
    if (!hash) {
      return;
    }
    const key = `${step.provider ?? ''}|${step.model ?? ''}`;
    const previous = lastHashByModel.get(key);
    if (previous && previous !== hash) {
      prefixChanged.add(index);
    }
    lastHashByModel.set(key, hash);
  });
  return (
    <details className="harness-panel">
      <summary>
        <span>Harness</span>
        <strong>{status}</strong>
        <small>
          {completed}/{steps.length} steps{run.durationMs ? ` · ${formatDuration(run.durationMs)}` : ''}
          {totals.prompt || totals.completion ? ` · ${formatTokenCount(totals.prompt + totals.completion)} tokens` : ''}
          {mediaLabel ? ` · ${mediaLabel}` : ''}
        </small>
      </summary>
      <div className="harness-meta">
        {run.loop?.iterations ? <span>{run.loop.iterations} iteration{run.loop.iterations === 1 ? '' : 's'}</span> : null}
        {stopReason ? <span>stop: {stopReason}</span> : null}
        {run.requestId ? <span>{run.requestId}</span> : null}
      </div>
      {usage.length ? (
        <div className="harness-usage">
          {usage.map((row) => (
            <div className="harness-usage-row" key={`${row.provider}-${row.model}`} title={`${row.provider} · ${row.calls} call${row.calls === 1 ? '' : 's'} in this run`}>
              <span className="harness-usage-model">{row.model}</span>
              <span>
                {row.promptTokens ? `${formatTokenCount(row.promptTokens)} in` : '— in'}
                {' · '}
                {row.completionTokens ? `${formatTokenCount(row.completionTokens)} out` : '— out'}
              </span>
            </div>
          ))}
        </div>
      ) : null}
      {media.length ? (
        <div className="harness-usage harness-usage-media">
          {media.map((row) => (
            <div className="harness-usage-row" key={`media-${row.provider}-${row.model}`} title={`${row.provider} · media generation · ${row.calls} call${row.calls === 1 ? '' : 's'} in this run`}>
              <span className="harness-usage-model">
                {row.provider !== '—' ? <span className="harness-usage-provider">{row.provider}</span> : null}
                {row.model}
              </span>
              <span>{mediaCountsLabel(row.video, row.audio, row.image)}</span>
            </div>
          ))}
        </div>
      ) : null}
      <ol className="harness-steps">
        {steps.map((step, index) => {
          const lane = harnessStepLane(step);
          const truncated = step.request?.truncatedMessages ?? 0;
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
              {step.promptTokens || step.tokens ? (
                <span>{[step.promptTokens ? `${formatTokenCount(step.promptTokens)} in` : '', step.tokens ? `${formatTokenCount(step.tokens)} out` : ''].filter(Boolean).join(' · ')}</span>
              ) : null}
              {step.firstTokenMs ? <span title="time to first token">ttft {formatDuration(step.firstTokenMs)}</span> : null}
              {step.durationMs ? <span>{formatDuration(step.durationMs)}</span> : null}
              {truncated ? <span className="harness-flag-warn" title="oldest messages dropped to fit num_ctx">trimmed {truncated} msg{truncated === 1 ? '' : 's'}</span> : null}
              {step.request?.promptHash ? (
                <code className={prefixChanged.has(index) ? 'harness-hash harness-hash-changed' : 'harness-hash'} title={`prompt prefix hash${prefixChanged.has(index) ? ' — changed since this model\'s previous request, prefix cache invalidated' : ''}`}>#{step.request.promptHash}</code>
              ) : null}
            </small>
            {asArray(step.tools).length ? (
              <div className="harness-tool-list">
                {asArray(step.tools).map((tool, toolIndex) => (
                  <div className={`harness-tool ${tool.status ?? 'pending'}`} key={`${tool.name}-${toolIndex}`}>
                    <div>
                      <strong>{formatToolName(tool.name)}</strong>
                      <span>{tool.status ?? 'pending'}{typeof tool.exitCode === 'number' ? ` · exit ${tool.exitCode}` : ''}{tool.durationMs ? ` · ${formatDuration(tool.durationMs)}` : ''}</span>
                    </div>
                    {tool.permission ? (
                      <span className={tool.permission === 'approved' ? 'harness-flag-ok' : 'harness-flag-warn'}>
                        permission {tool.permission}{tool.permissionWaitMs ? ` · waited ${formatDuration(tool.permissionWaitMs)}` : ''}
                      </span>
                    ) : null}
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

// One row per model that consumed tokens, folded from model-call steps. Only
// model-call steps count (triage, skill, planning, model_call, streaming):
// bookkeeping steps carry no usage, so each model call is counted exactly
// once. Generation models (fal image/video/audio) report no tokens and stay
// off the rows.
type ModelUsageRow = {
  provider: string;
  model: string;
  promptTokens: number;
  completionTokens: number;
  calls: number;
};

// Per-model usage for a single run — one row per model that consumed tokens.
function summarizeRunUsage(run?: HarnessRunView): ModelUsageRow[] {
  if (!run) {
    return [];
  }
  const byModel = new Map<string, ModelUsageRow>();
  for (const step of asArray(run.steps)) {
    if (!step.model || !MODEL_CALL_STEP_KINDS.includes(step.kind ?? '')) {
      continue;
    }
    const provider = step.provider || '—';
    const key = `${provider}|${step.model}`;
    const row = byModel.get(key) ?? {provider, model: step.model, promptTokens: 0, completionTokens: 0, calls: 0};
    row.promptTokens += step.promptTokens ?? 0;
    row.completionTokens += step.tokens ?? 0;
    row.calls += 1;
    byModel.set(key, row);
  }
  return [...byModel.values()].filter((row) => row.promptTokens > 0 || row.completionTokens > 0);
}

// Per-model usage for the whole conversation, merged from every assistant
// entry's run.
function summarizeModelUsage(chat: ChatEntry[]): ModelUsageRow[] {
  const byModel = new Map<string, ModelUsageRow>();
  for (const entry of chat) {
    if (entry.role !== 'assistant') {
      continue;
    }
    for (const row of summarizeRunUsage(entry.harnessRun)) {
      const key = `${row.provider}|${row.model}`;
      const merged = byModel.get(key) ?? {...row, promptTokens: 0, completionTokens: 0, calls: 0};
      merged.promptTokens += row.promptTokens;
      merged.completionTokens += row.completionTokens;
      merged.calls += row.calls;
      byModel.set(key, merged);
    }
  }
  return [...byModel.values()];
}

// One row per generation model that produced media. Media models (fal video/
// audio/image) burn no tokens — their consumption is what they produced — so
// rows count outputs by kind instead of prompt/completion tokens. They render
// as a sibling of the token table, never inside it. Provider attributes the
// model to its backend ("fal"/"ollama"/"openai-compatible") since image model
// families can run locally or in the cloud.
type MediaUsageRow = {
  provider: string;
  model: string;
  video: number;
  audio: number;
  image: number;
  calls: number;
};

// Per-model media generation for a single run, folded from tool_call step
// activities. Only completed media calls carry model/mediaKind/mediaCount, so
// failed calls drop out of the fold instead of counting phantom consumption.
function summarizeRunMediaUsage(run?: HarnessRunView): MediaUsageRow[] {
  if (!run) {
    return [];
  }
  const byModel = new Map<string, MediaUsageRow>();
  for (const step of asArray(run.steps)) {
    if (step.kind !== 'tool_call') {
      continue;
    }
    for (const tool of asArray(step.tools)) {
      const kind = tool.mediaKind;
      if (!tool.model || (kind !== 'video' && kind !== 'audio' && kind !== 'image')) {
        continue;
      }
      const provider = tool.provider || '—';
      const key = `${provider}|${tool.model}`;
      const row = byModel.get(key) ?? {provider, model: tool.model, video: 0, audio: 0, image: 0, calls: 0};
      row[kind] += tool.mediaCount ?? 0;
      row.calls += 1;
      byModel.set(key, row);
    }
  }
  return [...byModel.values()];
}

// Legacy turns (saved before tool_call activities carried media fields)
// recorded the generation model only in the providerResponse.tool block.
// Recover it so old conversations show their media consumption too.
function mediaUsageFromToolSummary(tool: MediaToolSummaryView | undefined): MediaUsageRow[] {
  if (!tool?.model) {
    return [];
  }
  const row: MediaUsageRow = {provider: legacyMediaProvider(tool.name, tool.model), model: tool.model, video: 0, audio: 0, image: 0, calls: 0};
  if (tool.name === 'video_generation') {
    row.video = tool.videoCount ?? 0;
    row.image = tool.imageCount ?? 0;
  } else if (tool.name === 'image_generation') {
    row.image = tool.imageCount ?? 0;
  } else if (tool.name === 'audio_generation') {
    row.audio = tool.audioCount ?? 0;
  } else {
    return [];
  }
  row.calls = 1;
  return [row];
}

// Best-effort provider for legacy media turns, which never recorded the routed
// backend: video/audio generation has always been fal-only; for images the
// fal-ai/ namespace is the closest available signal. New turns record the
// actual routed provider on the activity instead, so this only ever labels
// old data.
function legacyMediaProvider(name: string | undefined, model: string): string {
  if (name === 'image_generation') {
    return model.startsWith('fal-ai/') ? 'fal' : 'ollama';
  }
  return 'fal';
}

// Per-model media generation for the whole conversation. Each turn prefers its
// run's activities; the legacy tool-summary fallback applies only when the run
// recorded no media (old turns), so a turn is never counted through both.
function summarizeMediaUsage(chat: ChatEntry[]): MediaUsageRow[] {
  const byModel = new Map<string, MediaUsageRow>();
  for (const entry of chat) {
    if (entry.role !== 'assistant') {
      continue;
    }
    const runRows = summarizeRunMediaUsage(entry.harnessRun);
    const rows = runRows.length ? runRows : mediaUsageFromToolSummary(entry.mediaTool);
    for (const row of rows) {
      const key = `${row.provider}|${row.model}`;
      const merged = byModel.get(key) ?? {...row, video: 0, audio: 0, image: 0, calls: 0};
      merged.video += row.video;
      merged.audio += row.audio;
      merged.image += row.image;
      merged.calls += row.calls;
      byModel.set(key, merged);
    }
  }
  return [...byModel.values()].filter((row) => row.video + row.audio + row.image > 0);
}

// Human label for a media row's output counts, e.g. "1 video · 2 images".
function mediaCountsLabel(video: number, audio: number, image: number): string {
  const parts: string[] = [];
  if (video) {
    parts.push(`${video} video${video === 1 ? '' : 's'}`);
  }
  if (audio) {
    parts.push(`${audio} audio clip${audio === 1 ? '' : 's'}`);
  }
  if (image) {
    parts.push(`${image} image${image === 1 ? '' : 's'}`);
  }
  return parts.join(' · ');
}

function formatTokenCount(count: number): string {
  if (count >= 1000) {
    return `${(count / 1000).toFixed(1)}k`;
  }
  return `${count}`;
}

function harnessStepLane(step: HarnessStepView): {label: string; className: string} {
  switch (step.kind) {
    case 'triage':
      return {label: 'Chat model', className: 'harness-lane-chat'};
    case 'skill':
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

// uniqueAttachmentName returns a name that does not collide with any existing
// name, appending " (2)", " (3)", … before the extension as needed. Attachment
// names are the key @-mentions match against, and they back the React key for
// attachment chips, so duplicates would both break @ resolution and collide in
// the chip list. Pure/module-level so it can be unit-tested in isolation.
function uniqueAttachmentName(requested: string, existing: string[]): string {
  if (!requested) {
    requested = 'attachment';
  }
  if (!existing.includes(requested)) {
    return requested;
  }
  const taken = new Set(existing);
  const dot = requested.lastIndexOf('.');
  const stem = dot > 0 ? requested.slice(0, dot) : requested;
  const ext = dot > 0 ? requested.slice(dot) : '';
  for (let counter = 2; ; counter++) {
    const candidate = `${stem} (${counter})${ext}`;
    if (!taken.has(candidate)) {
      return candidate;
    }
  }
}

// MentionMatch describes an open @-token at the caret: `at` is the index of the
// '@', `query` is the text typed after it (no whitespace allowed mid-token).
type MentionMatch = { at: number; query: string };

// detectMentionAt finds an open @-mention token ending at the caret. A token is
// valid only when '@' sits at the start of text or right after whitespace (so
// "foo@bar" is not a mention), and contains no whitespace after the '@'. Returns
// null when no token is active. Pure/module-level for unit testing.
function detectMentionAt(text: string, caret: number): MentionMatch | null {
  if (caret <= 0) {
    return null;
  }
  const slice = text.slice(0, caret);
  const at = slice.lastIndexOf('@');
  if (at < 0) {
    return null;
  }
  // The '@' must start a token: begin-of-text or preceded by whitespace.
  if (at > 0 && !/\s/.test(text[at - 1])) {
    return null;
  }
  const query = slice.slice(at + 1);
  // The token ends at the first whitespace; if any is present, the mention is
  // no longer open at the caret.
  if (/\s/.test(query)) {
    return null;
  }
  return { at, query };
}

// assetTurnLabel renders an origin turn ID (turn_000003) as a human label
// ("turn 3") for the assets panel; anything unparsable passes through as-is.
function assetTurnLabel(turnID: string): string {
  const n = Number(turnID.replace(/^turn_/, ''));
  return Number.isFinite(n) && n > 0 ? `turn ${n}` : turnID;
}

// MentionCandidate extends an in-composer attachment with optional asset
// fields: when assetID is set, the candidate is a conversation asset from the
// panel's list — mentioned by ID (transport via referencedAssetIds) rather
// than by attached bytes. hint is the small provenance label the menu shows.
type MentionCandidate = Attachment & {
  assetID?: string;
  hint?: string;
};

// assetMentionLabel derives the @-token for a conversation asset from its ID —
// kind prefix plus enough hex to be unique in practice, and token-safe (no
// spaces) so the open-token detection and the send-time text scan agree.
function assetMentionLabel(asset: main.ConversationAsset): string {
  return asset.id.slice(0, 12);
}

// mentionMatches returns the candidates whose name contains the query as a
// case-insensitive substring, preserving order. An empty query lists every
// candidate (the menu is most useful right after typing '@').
function mentionMatches(query: string, items: MentionCandidate[]): MentionCandidate[] {
  const q = query.trim().toLowerCase();
  if (!q) {
    return items;
  }
  return items.filter((item) => item.name.toLowerCase().includes(q));
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
