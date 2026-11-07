// Wire-view types shared across the app's screens: the chat transcript's
// entry/chunk shapes, the harness run/step/activity views, the Wails event
// payloads App listens for, and the composer's attachment/draft shapes —
// plus the few value vocabularies tied to them (settings tabs, edit-op
// labels, model-call step kinds, the archive toast key).
import {main} from '../wailsjs/go/models';

type View = 'app' | 'settings' | 'conversation-models' | 'usage';
// Settings tabs draw one line: Providers are endpoints and keys you connect to
// and then select as a backend under Models (cloud or local servers alike);
// Local Tools are binaries installed on this machine that chat shells out to
// and never selects as a backend — the ConfigLocalProviders set in Go.
type SettingsTab = 'providers' | 'models' | 'tools' | 'general';
// The runtime form of the union, for validating the tab restored from the
// persisted UI state — a value a past build wrote that no longer exists
// falls back to the screen's default instead of selecting a dead tab.
const settingsTabs: SettingsTab[] = ['providers', 'models', 'tools', 'general'];
type ChatProviderID = 'ollama' | 'openrouter' | 'openai-compatible';
// The generate_speech backends: fal.ai's cloud endpoints, a local Ollama
// speech model, or Replicate's text-to-speech collection. Mirrors the Go
// resolveSpeechProvider vocabulary (anything else normalizes to fal).
type SpeechProviderID = 'fal' | 'ollama' | 'replicate';
// 'edit' marks an image-editor session child conversation: the sidebar lists
// it, but opening it routes to the editor workspace, not the chat transcript.
type ConversationKind = 'chat' | 'edit';

type ChatEntry = {
  id: string;
  role: 'user' | 'assistant' | 'system';
  content: string;
  thinking?: string;
  images?: string[];
  videos?: string[];
  audios?: string[];
  // Subtitle files (.vtt/.srt) attached to a user turn — data URLs live,
  // /atelier-artifact URLs after reload. Rendered as chips; never model input.
  subtitles?: string[];
  // Markdown documents (.md) attached to a user turn or saved into the
  // conversation by the markdown editor — /atelier-artifact URLs. Rendered as
  // chips with an Edit door into the editor; never model input.
  markdowns?: string[];
  // Composition project documents (cmp_ .json) saved into the conversation by
  // the video composer — one per saved revision. Rendered as chips with an
  // Edit door reopening that revision; never model input.
  compositions?: string[];
  // Comic book documents (cbc_ .json) saved into the conversation by the
  // comic editor — the same chip shape as compositions.
  comics?: string[];
  // Timestamped-transcript artifacts (.vtt) produced by transcribe_audio —
  // "/atelier-artifact" URLs rendered as downloadable chips.
  transcripts?: string[];
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
  transcripts?: string[];
  markdowns?: string[];
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
  transcripts: string[];
  markdowns: string[];
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

// LibraryArchiveEvent mirrors app.go's LibraryArchiveEvent: one tick per
// verified archive file (import's "verify") or written conversation ("write").
// Emitted only, never returned by a binding, so it isn't in models.ts.
type LibraryArchiveOp = 'export' | 'import';
type LibraryArchiveEvent = {
  op: LibraryArchiveOp;
  libraryId?: string;
  phase: 'verify' | 'write';
  done: number;
  total: number;
};

function libraryArchiveToastKey(op: LibraryArchiveOp): string {
  return `library-archive:${op}`;
}

// EditOpEvent is atelier:edit-op's payload (edit_session.go). The editors
// type it fully; App's background-job listener reads only what a toast needs.
type EditOpEvent = {
  sessionConversationId?: string;
  operation?: {id: string; kind: string; status: string; error?: string};
};

type EditSessionEditor = 'image' | 'video' | 'audio';

// editOpKinds names each edit kind for its toast and the editor that owns it.
const editOpKinds: Record<string, {label: string; editor: EditSessionEditor}> = {
  inpaint: {label: 'Inpaint', editor: 'image'},
  crop: {label: 'Crop', editor: 'image'},
  'video-reframe': {label: 'Reframe', editor: 'video'},
  'video-trim': {label: 'Trim', editor: 'video'},
  'audio-trim': {label: 'Audio trim', editor: 'audio'},
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
// counts each model call exactly once. Mirrors usageModelCallStepKinds in
// usage_report.go plus the streaming body.
const MODEL_CALL_STEP_KINDS = ['triage', 'skill', 'planning', 'document', 'model_call', 'streaming'];

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
  // Server-billed cost of this model call in USD millionths (OpenRouter
  // usage.cost); absent when the provider reports none (Ollama, local).
  costMicros?: number;
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
  // Estimated cost of a media generation or transcription in USD millionths,
  // computed at call time from fal's pricing API; absent for local backends
  // and calls pricing could not price.
  costMicros?: number;
  // True when the generation is paid but unpriceable (the Replicate backend
  // reports no cost) — renders "?" in the money column instead of the empty
  // slot a genuinely free local call renders.
  costUnknown?: boolean;
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
  // backend keeps the full data URL for AttachedVideo consumers. 'subtitle'
  // attachments (.vtt/.srt) keep the full data URL like audio/video — they are
  // tool-only too (convert_subtitles reads the persisted artifact, never the
  // message bytes), and 'markdown' (.md) is the document twin. The kind drives
  // chip rendering and request building in submitChat.
  kind: 'image' | 'audio' | 'video' | 'subtitle' | 'markdown';
  // Set when the file was too large to carry as a data URL: it was streamed
  // to the backend in chunks at attach time (stageMediaFileAsAttachment) and
  // src/payload are the staged /atelier-artifact/ URL. The backend adopts the
  // staged file into the conversation at turn persistence; stagedPath exists
  // so removing the chip can discard the never-sent upload.
  stagedPath?: string;
};

// One row of the composer's staging progress: a large media file currently
// streaming to the backend before its attachment chip exists.
type StagingAttachment = {
  key: string;
  name: string;
  kind: 'audio' | 'video';
  fraction: number;
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

// LibrariesEnvironment is everything useLibraries needs from App() — the seam
// the hook crosses. Refs are typed structurally ({current}) so the hook stays
// independent of React's namespace types. Failures don't cross it: the hook
// toasts them itself through the shared notifications store.
type LibrariesEnvironment = {
  activeConversationID: string;
  activeConversationProjectID: string;
  closeConversationMenu: () => void;
  expandChatsSection: () => void;
  composerDraftsRef: {current: Record<string, ComposerDraft>};
  pendingProjectRef: {current: {projectID: string; libraryID: string} | null};
  refreshConversations: () => Promise<main.ConversationSummary[]>;
  resetWorkspace: () => Promise<void>;
  setPendingProject: (project: {projectID: string; libraryID: string} | null) => void;
  setView: (view: View) => void;
  startNewChat: () => Promise<void>;
};

// MoveTargetGroup is one library's slice of the move dialog's target list:
// the dialog groups projects under their library instead of the flat
// `library › project` button list the ⋮ menu used to inline.
type MoveTargetGroup = {
  libraryID: string;
  libraryName: string;
  projects: {projectID: string; name: string}[];
};

export type {
  View,
  SettingsTab,
  ChatProviderID,
  SpeechProviderID,
  ConversationKind,
  ChatEntry,
  ChatChunk,
  ChatStreamDraft,
  ToolPermissionEvent,
  UpdateAvailableEvent,
  LibraryArchiveOp,
  LibraryArchiveEvent,
  EditOpEvent,
  EditSessionEditor,
  InFlightConversation,
  HarnessRunView,
  HarnessStepView,
  HarnessRequestSnapshotView,
  HarnessToolActivityView,
  HarnessRunEventView,
  MediaToolSummaryView,
  Attachment,
  StagingAttachment,
  ComposerDraft,
  LibrariesEnvironment,
  MoveTargetGroup,
};
export {settingsTabs, libraryArchiveToastKey, editOpKinds, MODEL_CALL_STEP_KINDS};
