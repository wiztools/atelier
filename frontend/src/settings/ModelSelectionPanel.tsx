// The Settings → Models field set (shared verbatim by the per-conversation
// models screen), its value shape and override-payload mapping, and the
// model capability popover the composer pickers also render.

import {useEffect, useMemo, useState} from 'react';
import {ListInpaintModels} from '../../wailsjs/go/main/App';
import {main} from '../../wailsjs/go/models';
import {ChatProviderID, SpeechProviderID} from '../types';
import {
  defaultImageSteps,
  imageAspectRatioOptions,
  imageSizePresetOptions,
  defaultFalImageModel,
  defaultFalImageEditModel,
  defaultFalVideoModel,
  defaultFalVideoImageModel,
  defaultFalVideoKeyframeModel,
  defaultFalVideoExtendModel,
  defaultFalVideoMotionModel,
  defaultFalVideoUpscaleModel,
  defaultFalVideoReframeModel,
  defaultFalVideoRestyleModel,
  defaultFalAudioModel,
  defaultOllamaSpeechModel,
  defaultFalAudioCloneModel,
  defaultFalSoundEffectsModel,
  defaultFalSoundEffectsVideoModel,
  defaultFalVoiceDesignModel,
  defaultFalAudioExtendModel,
  defaultFalTranscribeModel,
  defaultReplicateTranscribeModel,
  defaultReplicateLipsyncImageModel,
  defaultReplicateLipsyncVideoModel,
  defaultFalLipsyncImageModel,
  defaultFalLipsyncVideoModel,
  defaultFalUpscaleModel,
  defaultReplicateImageModel,
  defaultRunwareImageModel,
  defaultRunwareUpscaleModel,
  defaultRunwareVideoModel,
  defaultRunwareVideoImageModel,
  defaultRunwareVideoExtendModel,
  defaultRunwareVideoUpscaleModel,
  defaultRunwareVideoRestyleModel,
  defaultRunwareInpaintModel,
  defaultRunwareLipsyncImageModel,
  defaultRunwareLipsyncVideoModel,
  defaultRunwareSpeechModel,
  defaultRunwareTranscribeModel,
  defaultRunwareSoundEffectsModel,
  defaultRunwareSoundEffectsVideoModel,
  defaultReplicateImageEditModel,
  defaultReplicateVideoModel,
  defaultReplicateVideoImageModel,
  defaultReplicateUpscaleModel,
  defaultReplicateVideoUpscaleModel,
  defaultReplicateVideoRestyleModel,
  defaultReplicateVideoReframeModel,
  defaultReplicateVideoExtendModel,
  defaultReplicateSoundEffectsModel,
  defaultReplicateSoundEffectsVideoModel,
  defaultReplicateSpeechModel,
  defaultReplicateAudioExtendModel,
  videoResolutionOptions,
  videoResolutionLabels,
  videoDurationPickerOptions,
  formatSupportedDurations,
  videoDurationLabels,
  soundDurationLabels,
  audioExtendDurationOptions,
  audioExtendDurationLabels,
  videoAspectRatioOptions,
  videoAspectRatioLabels,
  positiveIntOrDefault,
} from '../modelDefaults';
import {asArray, formatModelSize} from '../utils';
import {speechModelOptionsWithVoiceDesignBadge, falModelOptionList} from './modelOptions';
import {ModelCombobox} from './ModelCombobox';
import {InfoHint, AirplaneHint} from '../ui/Hints';

function ModelCapabilityLink({
  id,
  modelName,
  models,
  openRouterModels,
  openID,
  setOpenID,
  variant = 'text',
}: {
  id: string;
  modelName: string;
  models?: main.OllamaModel[];
  openRouterModels?: main.ModelInfo[];
  openID: string;
  setOpenID: (id: string) => void;
  variant?: 'text' | 'icon';
}) {
  // Exactly one catalog is passed: the Ollama model list (keyed by name) or
  // the OpenRouter one (keyed by id). They carry different capability
  // vocabularies — OpenRouter's "image" is an input modality, while the
  // Ollama mapping reads "image" as image generation — so label derivation
  // follows the catalog rather than sharing one path.
  const isOllama = openRouterModels === undefined;
  const ollamaModel = isOllama ? asArray(models).find((item) => item.name === modelName) : undefined;
  const routerModel = isOllama ? undefined : asArray(openRouterModels).find((item) => item.id === modelName);
  const selectedModel = ollamaModel ?? routerModel;
  const capabilityLabels = ollamaModel
    ? modelCapabilityLabels(ollamaModel)
    : routerModel
      ? openRouterCapabilityLabels(routerModel)
      : [];
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
          <div className="model-capability-title">{routerModel?.displayName || modelName || 'No model selected'}</div>
          {selectedModel ? (
            <>
              <div className="capability-chips">
                {capabilityLabels.length ? capabilityLabels.map((capability) => (
                  <span key={capability}>{capability}</span>
                )) : <span>Capabilities not reported</span>}
              </div>
              {ollamaModel ? (
                <dl>
                  {ollamaModel.family ? (
                    <>
                      <dt>Family</dt>
                      <dd>{ollamaModel.family}</dd>
                    </>
                  ) : null}
                  {ollamaModel.parameter ? (
                    <>
                      <dt>Parameters</dt>
                      <dd>{ollamaModel.parameter}</dd>
                    </>
                  ) : null}
                  {ollamaModel.size ? (
                    <>
                      <dt>Size</dt>
                      <dd>{formatModelSize(ollamaModel.size)}</dd>
                    </>
                  ) : null}
                </dl>
              ) : null}
              {routerModel?.contextLength ? (
                <dl>
                  <dt>Context</dt>
                  <dd>{formatContextLength(routerModel.contextLength)} tokens</dd>
                </dl>
              ) : null}
            </>
          ) : (
            <p>This model is not in the current {isOllama ? 'Ollama' : 'OpenRouter'} model list.</p>
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

// ModelSelectionValue is every field the Settings → Models tab edits, in one
// object — the shared shape behind ModelSelectionPanel. The settings variant
// binds it to the global config state; the conversation variant binds it to a
// per-conversation draft whose untouched fields carry the inherited global
// values, so both screens render the identical field set. The image-to-video
// and extend duration fields are frontend-only previews in both variants —
// the backend reads one canonical video duration (videoDuration here).
type ModelSelectionValue = {
  primaryProvider: ChatProviderID;
  primaryModel: string;
  harnessProvider: ChatProviderID;
  harnessModel: string;
  imageProvider: 'fal' | 'replicate' | 'runware' | 'openai-compatible';
  videoProvider: 'fal' | 'replicate' | 'runware';
  inpaintProvider: 'fal' | 'replicate' | 'runware';
  falModel: string;
  openaiImageModel: string;
  replicateModel: string;
  runwareModel: string;
  runwareUpscaleModel: string;
  runwareVideoModel: string;
  runwareVideoImageModel: string;
  runwareVideoExtendModel: string;
  runwareVideoUpscaleModel: string;
  runwareVideoRestyleModel: string;
  runwareInpaintModel: string;
  runwareSpeechModel: string;
  runwareTranscribeModel: string;
  runwareSoundEffectsModel: string;
  runwareSoundEffectsVideoModel: string;
  runwareLipsyncImageModel: string;
  runwareLipsyncVideoModel: string;
  replicateImageEditModel: string;
  replicateUpscaleModel: string;
  falImageEditModel: string;
  falUpscaleModel: string;
  falInpaintModel: string;
  replicateInpaintModel: string;
  replicateVideoUpscaleModel: string;
  replicateVideoRestyleModel: string;
  replicateVideoReframeModel: string;
  replicateVideoExtendModel: string;
  falVideoModel: string;
  falVideoImageModel: string;
  replicateVideoModel: string;
  replicateVideoImageModel: string;
  falVideoKeyframeModel: string;
  falVideoExtendModel: string;
  falVideoMotionModel: string;
  falVideoUpscaleModel: string;
  falVideoReframeModel: string;
  falVideoRestyleModel: string;
  falAudioModel: string;
  falAudioCloneModel: string;
  falSoundEffectsModel: string;
  falSoundEffectsVideoModel: string;
  falVoiceDesignModel: string;
  falAudioExtendModel: string;
  transcriptionProvider: 'fal' | 'local-whisper' | 'replicate' | 'runware';
  falTranscribeModel: string;
  replicateTranscribeModel: string;
  falLipsyncImageModel: string;
  falLipsyncVideoModel: string;
  lipsyncProvider: 'fal' | 'replicate' | 'runware';
  speechProvider: SpeechProviderID;
  ollamaSpeechModel: string;
  replicateSpeechModel: string;
  audioProvider: 'fal' | 'replicate' | 'runware';
  replicateSoundEffectsModel: string;
  replicateSoundEffectsVideoModel: string;
  replicateAudioExtendModel: string;
  replicateLipsyncImageModel: string;
  replicateLipsyncVideoModel: string;
  imageAspectRatio: string;
  imageSizePreset: string;
  imageSteps: number;
  videoDuration: string;
  videoAspectRatio: string;
  videoResolution: string;
  soundDuration: string;
  audioExtendDuration: string;
};

// chatModelOptionsFor is the provider-aware option list shared by the primary
// and harness pickers (both the panel's and the composer's).
function chatModelOptionsFor(
  provider: ChatProviderID,
  base: {modelOptions: string[]; openRouterModels: main.ModelInfo[] | null; openaiCompatibleModels: {value: string; label: string}[]},
): {value: string; label: string}[] {
  if (provider === 'openrouter') {
    return asArray(base.openRouterModels)
      .map((item) => ({value: item.id, label: item.displayName || item.id}))
      .sort((a, b) => a.label.localeCompare(b.label));
  }
  if (provider === 'openai-compatible') {
    return base.openaiCompatibleModels;
  }
  return base.modelOptions.map((name) => ({value: name, label: name}));
}

// overrideKeysFromRecord maps a persisted ConversationModelOverrides onto the
// set of panel field keys it pins. 'primary' covers the provider+model pair —
// the two always pin together.
function overrideKeysFromRecord(overrides: main.ConversationModelOverrides | null | undefined): Set<string> {
  const keys = new Set<string>();
  if (!overrides) {
    return keys;
  }
  if (overrides.primaryProvider || overrides.primaryModel) keys.add('primary');
  if (overrides.harnessProvider) keys.add('harnessProvider');
  if (overrides.harnessModel) keys.add('harnessModel');
  if (overrides.imageProvider) keys.add('imageProvider');
  if (overrides.imageModel) keys.add('imageModel');
  if (overrides.videoProvider) keys.add('videoProvider');
  if (overrides.imageEditModel) keys.add('imageEditModel');
  if (overrides.upscaleModel) keys.add('upscaleModel');
  if (overrides.inpaintProvider) keys.add('inpaintProvider');
  if (overrides.inpaintModel) keys.add('inpaintModel');
  if (overrides.videoModel) keys.add('videoModel');
  if (overrides.videoImageModel) keys.add('videoImageModel');
  if (overrides.videoKeyframeModel) keys.add('videoKeyframeModel');
  if (overrides.videoExtendModel) keys.add('videoExtendModel');
  if (overrides.videoMotionModel) keys.add('videoMotionModel');
  if (overrides.videoUpscaleModel) keys.add('videoUpscaleModel');
  if (overrides.videoReframeModel) keys.add('videoReframeModel');
  if (overrides.videoRestyleModel) keys.add('videoRestyleModel');
  if (overrides.audioModel) keys.add('audioModel');
  if (overrides.speechProvider) keys.add('speechProvider');
  if (overrides.audioProvider) keys.add('audioProvider');
  if (overrides.audioCloneModel) keys.add('audioCloneModel');
  if (overrides.soundEffectsModel) keys.add('soundEffectsModel');
  if (overrides.soundEffectsVideoModel) keys.add('soundEffectsVideoModel');
  if (overrides.voiceDesignModel) keys.add('voiceDesignModel');
  if (overrides.audioExtendModel) keys.add('audioExtendModel');
  if (overrides.transcribeModel) keys.add('transcribeModel');
  if (overrides.transcriptionProvider) keys.add('transcriptionProvider');
  // The whisper model, binary, and VAD model are no longer per-conversation
  // fields (they're engine config under Settings → Local Tools), but a record
  // pinned before that still overlays in Go, so it still counts as
  // "overrides active" here; conversationOverridesPayload never re-emits
  // them, so the next save normalizes the stale pin away.
  if (overrides.whisperModel) keys.add('whisperModel');
  if (overrides.whisperBinary) keys.add('whisperBinary');
  if (overrides.whisperVADModel) keys.add('whisperVADModel');
  if (overrides.imageAspectRatio) keys.add('imageAspectRatio');
  if (overrides.imageSizePreset) keys.add('imageSizePreset');
  if (overrides.imageSteps) keys.add('imageSteps');
  if (overrides.videoDuration) keys.add('videoDuration');
  if (overrides.videoAspectRatio) keys.add('videoAspectRatio');
  if (overrides.videoResolution) keys.add('videoResolution');
  if (overrides.soundDuration) keys.add('soundDuration');
  if (overrides.audioExtendDuration) keys.add('audioExtendDuration');
  if (overrides.lipsyncProvider) keys.add('lipsyncProvider');
  if (overrides.lipsyncImageModel) keys.add('lipsyncImageModel');
  if (overrides.lipsyncVideoModel) keys.add('lipsyncVideoModel');
  return keys;
}

// conversationOverridesPayload builds the wire payload from the conversation
// draft + its overridden-key set: only pinned fields are stored, so unpinned
// ones keep inheriting the live global Settings (differential semantics).
function conversationOverridesPayload(draft: ModelSelectionValue, keys: ReadonlySet<string>): main.ConversationModelOverrides {
  const payload: Record<string, string | number> = {};
  if (keys.has('primary')) {
    payload.primaryProvider = draft.primaryProvider;
    payload.primaryModel = draft.primaryModel;
  }
  if (keys.has('harnessProvider')) payload.harnessProvider = draft.harnessProvider;
  if (keys.has('harnessModel')) payload.harnessModel = draft.harnessModel;
  if (keys.has('imageProvider')) payload.imageProvider = draft.imageProvider;
  if (keys.has('imageModel')) {
    payload.imageModel = draft.imageProvider === 'fal'
      ? draft.falModel
      : draft.imageProvider === 'replicate'
        ? draft.replicateModel
        : draft.imageProvider === 'runware'
          ? draft.runwareModel
          : draft.openaiImageModel;
  }
  if (keys.has('videoProvider')) payload.videoProvider = draft.videoProvider;
  // The edit/video model overrides ride the provider the draft has selected,
  // mirroring the backend's provider-conditional overlay.
  if (keys.has('imageEditModel')) payload.imageEditModel = draft.imageProvider === 'replicate' ? draft.replicateImageEditModel : draft.falImageEditModel;
  if (keys.has('upscaleModel')) payload.upscaleModel = draft.imageProvider === 'replicate' ? draft.replicateUpscaleModel : draft.imageProvider === 'runware' ? draft.runwareUpscaleModel : draft.falUpscaleModel;
  if (keys.has('inpaintProvider')) payload.inpaintProvider = draft.inpaintProvider;
  if (keys.has('inpaintModel')) payload.inpaintModel = draft.inpaintProvider === 'replicate' ? draft.replicateInpaintModel : draft.inpaintProvider === 'runware' ? draft.runwareInpaintModel : draft.falInpaintModel;
  if (keys.has('videoModel')) payload.videoModel = draft.videoProvider === 'replicate' ? draft.replicateVideoModel : draft.videoProvider === 'runware' ? draft.runwareVideoModel : draft.falVideoModel;
  if (keys.has('videoImageModel')) payload.videoImageModel = draft.videoProvider === 'replicate' ? draft.replicateVideoImageModel : draft.videoProvider === 'runware' ? draft.runwareVideoImageModel : draft.falVideoImageModel;
  if (keys.has('videoKeyframeModel')) payload.videoKeyframeModel = draft.falVideoKeyframeModel;
  if (keys.has('videoExtendModel')) payload.videoExtendModel = draft.videoProvider === 'replicate' ? draft.replicateVideoExtendModel : draft.videoProvider === 'runware' ? draft.runwareVideoExtendModel : draft.falVideoExtendModel;
  if (keys.has('videoMotionModel')) payload.videoMotionModel = draft.falVideoMotionModel;
  if (keys.has('videoUpscaleModel')) payload.videoUpscaleModel = draft.videoProvider === 'replicate' ? draft.replicateVideoUpscaleModel : draft.videoProvider === 'runware' ? draft.runwareVideoUpscaleModel : draft.falVideoUpscaleModel;
  if (keys.has('videoReframeModel')) payload.videoReframeModel = draft.videoProvider === 'replicate' ? draft.replicateVideoReframeModel : draft.falVideoReframeModel;
  if (keys.has('videoRestyleModel')) payload.videoRestyleModel = draft.videoProvider === 'replicate' ? draft.replicateVideoRestyleModel : draft.videoProvider === 'runware' ? draft.runwareVideoRestyleModel : draft.falVideoRestyleModel;
  if (keys.has('speechProvider')) payload.speechProvider = draft.speechProvider;
  if (keys.has('audioModel')) payload.audioModel = draft.speechProvider === 'ollama' ? draft.ollamaSpeechModel : draft.speechProvider === 'replicate' ? draft.replicateSpeechModel : draft.speechProvider === 'runware' ? draft.runwareSpeechModel : draft.falAudioModel;
  if (keys.has('audioCloneModel')) payload.audioCloneModel = draft.falAudioCloneModel;
  // The sound-slot overrides land on the slot the effective audio provider
  // reads, mirroring the backend's provider-conditional overlay.
  if (keys.has('soundEffectsModel')) payload.soundEffectsModel = draft.audioProvider === 'replicate' ? draft.replicateSoundEffectsModel : draft.audioProvider === 'runware' ? draft.runwareSoundEffectsModel : draft.falSoundEffectsModel;
  if (keys.has('soundEffectsVideoModel')) payload.soundEffectsVideoModel = draft.audioProvider === 'replicate' ? draft.replicateSoundEffectsVideoModel : draft.audioProvider === 'runware' ? draft.runwareSoundEffectsVideoModel : draft.falSoundEffectsVideoModel;
  if (keys.has('voiceDesignModel')) payload.voiceDesignModel = draft.falVoiceDesignModel;
  if (keys.has('audioExtendModel')) payload.audioExtendModel = draft.audioProvider === 'replicate' ? draft.replicateAudioExtendModel : draft.falAudioExtendModel;
  // The transcription model lands on the slot the effective provider reads,
  // mirroring the video pair's provider-conditional overlay.
  if (keys.has('transcribeModel')) payload.transcribeModel = draft.transcriptionProvider === 'replicate' ? draft.replicateTranscribeModel : draft.transcriptionProvider === 'runware' ? draft.runwareTranscribeModel : draft.falTranscribeModel;
  if (keys.has('transcriptionProvider')) payload.transcriptionProvider = draft.transcriptionProvider;
  if (keys.has('imageAspectRatio')) payload.imageAspectRatio = draft.imageAspectRatio;
  if (keys.has('imageSizePreset')) payload.imageSizePreset = draft.imageSizePreset;
  if (keys.has('imageSteps')) payload.imageSteps = draft.imageSteps;
  if (keys.has('videoDuration')) payload.videoDuration = draft.videoDuration;
  if (keys.has('videoAspectRatio')) payload.videoAspectRatio = draft.videoAspectRatio;
  if (keys.has('videoResolution')) payload.videoResolution = draft.videoResolution;
  if (keys.has('soundDuration')) payload.soundDuration = draft.soundDuration;
  if (keys.has('audioExtendDuration')) payload.audioExtendDuration = draft.audioExtendDuration;
  if (keys.has('lipsyncProvider')) payload.lipsyncProvider = draft.lipsyncProvider;
  if (keys.has('lipsyncImageModel')) payload.lipsyncImageModel = draft.lipsyncProvider === 'replicate' ? draft.replicateLipsyncImageModel : draft.lipsyncProvider === 'runware' ? draft.runwareLipsyncImageModel : draft.falLipsyncImageModel;
  if (keys.has('lipsyncVideoModel')) payload.lipsyncVideoModel = draft.lipsyncProvider === 'replicate' ? draft.replicateLipsyncVideoModel : draft.lipsyncProvider === 'runware' ? draft.runwareLipsyncVideoModel : draft.falLipsyncVideoModel;
  return main.ConversationModelOverrides.createFrom(payload);
}

// ModelSelectionPanel renders the Settings → Models field set. It is purely
// value-driven: the settings screen feeds it the global config state and the
// conversation-models screen feeds it the per-conversation draft, so the two
// screens stay pixel-identical by construction. The conversation variant adds
// a Primary section and — via overriddenKeys/onResetField — marks fields this
// conversation pins with a reset affordance; everything else, generation
// defaults included, renders in both variants. Engine config (the whisper
// model, binary, and VAD model, like ffmpeg's) is NOT here — it lives under
// Settings → Local Tools and is global; onOpenLocalTools lets the settings
// variant jump there from the Transcription section's hints.
function ModelSelectionPanel({
  variant,
  value,
  onChange,
  catalogs,
  durationOptions,
  overriddenKeys,
  onResetField,
  airplaneMode,
  onOpenLocalTools,
}: {
  variant: 'settings' | 'conversation';
  value: ModelSelectionValue;
  onChange: (patch: Partial<ModelSelectionValue>) => void;
  // Airplane mode: cloud providers and cloud-only sections render disabled or
  // hidden — the backend refuses them (airplane_mode.go), the panel explains.
  airplaneMode: boolean;
  onOpenLocalTools?: () => void;
  catalogs: {
    models: main.OllamaModel[];
    modelOptions: string[];
    openRouterModels: main.ModelInfo[];
    openaiCompatibleModels: {value: string; label: string}[];
    falModels: main.FalModel[];
    falImageEditModels: main.FalModel[];
    replicateModels: main.ReplicateModel[];
    runwareModels: main.RunwareModel[];
    runwareUpscaleModels: main.RunwareModel[];
    runwareVideoModels: main.RunwareModel[];
    runwareVideoImageModels: main.RunwareModel[];
    runwareVideoExtendModels: main.RunwareModel[];
    runwareVideoUpscaleModels: main.RunwareModel[];
    runwareVideoRestyleModels: main.RunwareModel[];
    runwareSpeechModels: main.RunwareModel[];
    runwareTranscribeModels: main.RunwareModel[];
    runwareSoundEffectModels: main.RunwareModel[];
    runwareSoundEffectsVideoModels: main.RunwareModel[];
    runwareLipsyncImageModels: main.RunwareModel[];
    runwareLipsyncVideoModels: main.RunwareModel[];
    replicateImageEditModels: main.ReplicateModel[];
    replicateUpscaleModels: main.ReplicateModel[];
    replicateVideoUpscaleModels: main.ReplicateModel[];
    replicateVideoRestyleModels: main.ReplicateModel[];
    replicateVideoReframeModels: main.ReplicateModel[];
    replicateVideoExtendModels: main.ReplicateModel[];
    replicateVideoModels: main.ReplicateModel[];
    replicateVideoImageModels: main.ReplicateModel[];
    replicateTranscribeModels: main.ReplicateModel[];
    replicateLipsyncImageModels: main.ReplicateModel[];
    replicateLipsyncVideoModels: main.ReplicateModel[];
    replicateSoundEffectsModels: main.ReplicateModel[];
    replicateSoundEffectsVideoModels: main.ReplicateModel[];
    replicateAudioExtendModels: main.ReplicateModel[];
    replicateSpeechModels: main.ReplicateModel[];
    falVideoModels: main.FalModel[];
    falVideoImageModels: main.FalModel[];
    falVideoKeyframeModels: main.FalModel[];
    falVideoExtendModels: main.FalModel[];
    falVideoMotionModels: main.FalModel[];
    falVideoUpscaleModels: main.FalModel[];
    falVideoReframeModels: main.FalModel[];
    falVideoRestyleModels: main.FalModel[];
    falAudioModels: main.FalModel[];
    falSoundEffectModels: main.FalModel[];
    falSoundEffectsVideoModels: main.FalModel[];
    falAudioExtendModels: main.FalModel[];
    falVoiceDesignModels: main.FalModel[];
    falTranscribeModels: main.FalModel[];
    falUpscaleModels: main.FalModel[];
    falLipsyncImageModels: main.FalModel[];
    falLipsyncVideoModels: main.FalModel[];
    openRouterHasKey: boolean;
    falHasKey: boolean;
    replicateHasKey: boolean;
    runwareHasKey: boolean;
    whisperStatus: main.LocalBinaryStatus | null;
    openCapabilityID: string;
    setOpenCapabilityID: (id: string) => void;
  };
  durationOptions: {video: string[]; image: string[]; keyframe: string[]; extend: string[]; sound: string[]};
  overriddenKeys?: ReadonlySet<string>;
  onResetField?: (key: string) => void;
}) {
  const isSettings = variant === 'settings';
  const isPinned = (key: string) => !isSettings && !!overriddenKeys?.has(key);
  const resetField = (key: string) => onResetField?.(key);
  // "Add a key above" only reads right inside Settings; the conversation
  // screen has no Providers tab, so point there instead. Local binaries live
  // on their own tab in both cases.
  const keyWhere = isSettings ? 'above' : 'in Settings → Providers';
  const toolWhere = isSettings ? 'under Local Tools' : 'in Settings → Local Tools';
  // The Transcription section's one actionable hint: what's missing (a fal key
  // or a whisper install) and where to fix it — never two hints saying the
  // same thing. The detection detail from Go is a full sentence, so the
  // pointer continues as its own sentence.
  const whisperFound = !!catalogs.whisperStatus?.available;
  const whisperDetail = catalogs.whisperStatus?.detail ?? 'Whisper not found.';
  const localTranscriptionSelected = airplaneMode || value.transcriptionProvider === 'local-whisper';
  const replicateTranscriptionSelected = !airplaneMode && value.transcriptionProvider === 'replicate';
  const openLocalToolsLink = isSettings && onOpenLocalTools ? (
    <>
      {' '}
      <button type="button" className="hint-link" onClick={onOpenLocalTools}>Open Local Tools</button>
    </>
  ) : null;

  const chatOptions = chatModelOptionsFor(value.harnessProvider, catalogs);
  const primaryOptions = chatModelOptionsFor(value.primaryProvider, catalogs);
  const falImageOptions = falModelOptionList(catalogs.falModels);
  const falImageEditOptions = falModelOptionList(catalogs.falImageEditModels);
  const replicateImageOptions = falModelOptionList(catalogs.replicateModels);
  const runwareImageOptions = falModelOptionList(catalogs.runwareModels);
  const runwareUpscaleOptions = falModelOptionList(catalogs.runwareUpscaleModels);
  const runwareVideoOptions = falModelOptionList(catalogs.runwareVideoModels);
  const runwareVideoImageOptions = falModelOptionList(catalogs.runwareVideoImageModels);
  const runwareVideoExtendOptions = falModelOptionList(catalogs.runwareVideoExtendModels);
  const runwareVideoUpscaleOptions = falModelOptionList(catalogs.runwareVideoUpscaleModels);
  const runwareVideoRestyleOptions = falModelOptionList(catalogs.runwareVideoRestyleModels);
  const runwareSpeechOptions = falModelOptionList(catalogs.runwareSpeechModels);
  const runwareTranscribeOptions = falModelOptionList(catalogs.runwareTranscribeModels);
  const runwareSoundEffectOptions = falModelOptionList(catalogs.runwareSoundEffectModels);
  const runwareSoundEffectsVideoOptions = falModelOptionList(catalogs.runwareSoundEffectsVideoModels);
  const runwareLipsyncImageOptions = falModelOptionList(catalogs.runwareLipsyncImageModels);
  const runwareLipsyncVideoOptions = falModelOptionList(catalogs.runwareLipsyncVideoModels);
  const replicateImageEditOptions = falModelOptionList(catalogs.replicateImageEditModels);
  const replicateUpscaleOptions = falModelOptionList(catalogs.replicateUpscaleModels);
  const replicateVideoUpscaleOptions = falModelOptionList(catalogs.replicateVideoUpscaleModels);
  const replicateVideoRestyleOptions = falModelOptionList(catalogs.replicateVideoRestyleModels);
  const replicateVideoReframeOptions = falModelOptionList(catalogs.replicateVideoReframeModels);
  const replicateVideoExtendOptions = falModelOptionList(catalogs.replicateVideoExtendModels);
  const replicateVideoOptions = falModelOptionList(catalogs.replicateVideoModels);
  const replicateVideoImageOptions = falModelOptionList(catalogs.replicateVideoImageModels);
  const replicateTranscribeOptions = falModelOptionList(catalogs.replicateTranscribeModels);
  const replicateLipsyncImageOptions = falModelOptionList(catalogs.replicateLipsyncImageModels);
  const replicateLipsyncVideoOptions = falModelOptionList(catalogs.replicateLipsyncVideoModels);
  const replicateSoundEffectOptions = falModelOptionList(catalogs.replicateSoundEffectsModels);
  const replicateSpeechOptions = falModelOptionList(catalogs.replicateSpeechModels);
  const replicateSoundEffectsVideoOptions = falModelOptionList(catalogs.replicateSoundEffectsVideoModels);
  const replicateAudioExtendOptions = falModelOptionList(catalogs.replicateAudioExtendModels);
  const falUpscaleOptions = falModelOptionList(catalogs.falUpscaleModels);
  const falVideoOptions = falModelOptionList(catalogs.falVideoModels);
  const falVideoImageOptions = falModelOptionList(catalogs.falVideoImageModels);
  const falVideoKeyframeOptions = falModelOptionList(catalogs.falVideoKeyframeModels);
  const falVideoExtendOptions = falModelOptionList(catalogs.falVideoExtendModels);
  const falVideoMotionOptions = falModelOptionList(catalogs.falVideoMotionModels);
  const falVideoUpscaleOptions = falModelOptionList(catalogs.falVideoUpscaleModels);
  const falVideoReframeOptions = falModelOptionList(catalogs.falVideoReframeModels);
  const falVideoRestyleOptions = falModelOptionList(catalogs.falVideoRestyleModels);
  const falAudioOptions = speechModelOptionsWithVoiceDesignBadge(falModelOptionList(catalogs.falAudioModels));
  // The Ollama speech picker filters the shared Ollama catalog by the "speech"
  // capability flag the Go side derives from /api/show — the image picker's
  // imageGeneration rule one modality over. An empty list means no speech
  // model is pulled (or the Ollama is too old to report one).
  const ollamaSpeechOptions = asArray(catalogs.models)
    .filter((item) => item.speechGeneration)
    .map((item) => ({value: item.name, label: item.name}));
  const falSoundEffectOptions = falModelOptionList(catalogs.falSoundEffectModels);
  const falSoundEffectsVideoOptions = falModelOptionList(catalogs.falSoundEffectsVideoModels);
  const falAudioExtendOptions = falModelOptionList(catalogs.falAudioExtendModels);
  const falVoiceDesignOptions = falModelOptionList(catalogs.falVoiceDesignModels);
  const falTranscribeOptions = falModelOptionList(catalogs.falTranscribeModels);
  const falLipsyncImageOptions = falModelOptionList(catalogs.falLipsyncImageModels);
  const falLipsyncVideoOptions = falModelOptionList(catalogs.falLipsyncVideoModels);

  // The inpainting catalog is a small VERIFIED list (a curated Go-side table,
  // not the broad image-edit category), so the panel fetches it directly
  // instead of riding the big shared catalogs fetch.
  const [inpaintCatalogs, setInpaintCatalogs] = useState<{fal: main.InpaintModelOption[]; replicate: main.InpaintModelOption[]; runware: main.InpaintModelOption[]}>({fal: [], replicate: [], runware: []});
  useEffect(() => {
    let cancelled = false;
    Promise.all([ListInpaintModels('fal'), ListInpaintModels('replicate'), ListInpaintModels('runware')])
      .then(([falList, replicateList, runwareList]) => {
        if (!cancelled) {
          setInpaintCatalogs({fal: falList, replicate: replicateList, runware: runwareList});
        }
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, []);
  const falInpaintOptions = inpaintCatalogs.fal.map((option) => ({value: option.id, label: option.label}));
  const replicateInpaintOptions = inpaintCatalogs.replicate.map((option) => ({value: option.id, label: option.label}));
  const runwareInpaintOptions = inpaintCatalogs.runware.map((option) => ({value: option.id, label: option.label}));

  // Airplane mode: a cloud-routed image provider can't serve generate_image
  // (the tool gate drops it), so the model pickers yield to an explanation
  // instead of offering models the backend will refuse to call.
  const imageProviderBlocked = airplaneMode && value.imageProvider !== 'openai-compatible';

  const imageSizeOptions = useMemo(() => {
    const parts = value.imageAspectRatio.split(':').map((item) => Number(item));
    const valid = parts.length === 2 && parts.every((item) => Number.isFinite(item) && item > 0);
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
  }, [value.imageAspectRatio]);

  // fieldLabel wraps a field's label with the conversation-variant reset
  // affordance when the field is pinned by this conversation's override.
  const fieldLabel = (htmlFor: string, text: string, key: string) => {
    if (!isPinned(key)) {
      return <label htmlFor={htmlFor}>{text}</label>;
    }
    return (
      <div className="field-label-row">
        <label htmlFor={htmlFor}>{text}</label>
        <button
          type="button"
          className="override-reset"
          onClick={() => resetField(key)}
          title="Stop overriding — follow the global Settings value"
        >
          ↺ reset
        </button>
      </div>
    );
  };

  const providerSelect = (
    id: string,
    provider: ChatProviderID,
    onPick: (next: ChatProviderID) => void,
    label: string,
  ) => (
    <div className="field">
      <label htmlFor={id}>{label}</label>
      <select id={id} value={provider} onChange={(event) => onPick(event.target.value as ChatProviderID)}>
        <option value="ollama">Ollama</option>
        <option value="openrouter" disabled={!catalogs.openRouterHasKey || airplaneMode}>OpenRouter</option>
        <option value="openai-compatible">OpenAI-compatible (local)</option>
      </select>
      {airplaneMode && provider === 'openrouter' ? (
        <span className="hint">Airplane mode refuses turns to OpenRouter — switch to a local provider.</span>
      ) : null}
    </div>
  );

  return (
    <>
      {!isSettings ? (
        <section className="settings-section">
          <h3>Primary</h3>
          <div className="settings-rows">
            <div className="two-column">
              {providerSelect('conv-primary-provider', value.primaryProvider, (next) => onChange({primaryProvider: next}), 'Provider')}
              <div className="field">
                {fieldLabel('conv-primary-model', 'Model', 'primary')}
                <div className="model-inline-control">
                  <ModelCombobox
                    id="conv-primary-model"
                    ariaLabel="Primary model"
                    placeholder="Type to filter models..."
                    value={value.primaryModel}
                    onChange={(next) => onChange({primaryModel: next})}
                    options={primaryOptions}
                    allowCustom={value.primaryProvider === 'openai-compatible'}
                  />
                  {value.primaryProvider === 'ollama' || value.primaryProvider === 'openrouter' ? (
                    <ModelCapabilityLink
                      id="conv-primary-model"
                      modelName={value.primaryModel}
                      models={value.primaryProvider === 'ollama' ? catalogs.models : undefined}
                      openRouterModels={value.primaryProvider === 'openrouter' ? catalogs.openRouterModels : undefined}
                      openID={catalogs.openCapabilityID}
                      setOpenID={catalogs.setOpenCapabilityID}
                      variant="icon"
                    />
                  ) : null}
                </div>
              </div>
            </div>
          </div>
        </section>
      ) : null}

      <section className="settings-section">
        <h3>Harness</h3>
        <div className="settings-rows">
          <div className="two-column">
            {providerSelect('harness-provider', value.harnessProvider, (next) => onChange({harnessProvider: next}), 'Harness Provider')}
            <div className="field">
              {fieldLabel('harness-model', 'Harness Model', 'harnessModel')}
              <div className="model-inline-control">
                {value.harnessProvider === 'ollama' ? (
                  <>
                    <select id="harness-model" value={value.harnessModel} onChange={(event) => onChange({harnessModel: event.target.value})}>
                      {chatOptions.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
                    </select>
                    <ModelCapabilityLink
                      id="settings-tools"
                      modelName={value.harnessModel}
                      models={catalogs.models}
                      openID={catalogs.openCapabilityID}
                      setOpenID={catalogs.setOpenCapabilityID}
                      variant="icon"
                    />
                  </>
                ) : (
                  <>
                    <ModelCombobox
                      id="harness-model"
                      ariaLabel="Harness model"
                      placeholder="Type to filter models..."
                      value={value.harnessModel}
                      onChange={(next) => onChange({harnessModel: next})}
                      options={chatOptions}
                      allowCustom={value.harnessProvider === 'openai-compatible'}
                    />
                    {value.harnessProvider === 'openrouter' ? (
                      <ModelCapabilityLink
                        id="settings-tools"
                        modelName={value.harnessModel}
                        openRouterModels={catalogs.openRouterModels}
                        openID={catalogs.openCapabilityID}
                        setOpenID={catalogs.setOpenCapabilityID}
                        variant="icon"
                      />
                    ) : null}
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
              <div className="field-label-row">
                {fieldLabel('image-provider', 'Image Provider', 'imageProvider')}
                {airplaneMode && imageProviderBlocked ? (
                  <AirplaneHint
                    label="Image provider unavailable"
                    text="The configured cloud provider is unavailable while airplane mode is on — pick a local provider to configure a model."
                  />
                ) : null}
              </div>
              <select
                id="image-provider"
                value={value.imageProvider}
                onChange={(event) => onChange({imageProvider: event.target.value as 'fal' | 'replicate' | 'runware' | 'openai-compatible'})}
              >
                <option value="fal" disabled={airplaneMode}>fal.ai (cloud)</option>
                <option value="replicate" disabled={airplaneMode}>Replicate (cloud)</option>
                <option value="runware" disabled={airplaneMode}>Runware (cloud)</option>
                <option value="openai-compatible">OpenAI-compatible (local)</option>
              </select>
            </div>

            {imageProviderBlocked ? null : value.imageProvider === 'fal' ? (
              <div className="field">
                {fieldLabel('fal-model', 'fal.ai Model', 'imageModel')}
                <ModelCombobox
                  id="fal-model"
                  ariaLabel="fal.ai model"
                  placeholder={defaultFalImageModel}
                  value={value.falModel}
                  onChange={(next) => onChange({falModel: next})}
                  options={falImageOptions}
                  allowCustom
                />
                {!catalogs.falHasKey ? (
                  <span className="hint">Add a fal.ai API key {keyWhere} before generating images.</span>
                ) : falImageOptions.length ? null : (
                  <span className="hint">Type a fal.ai endpoint id — the model list couldn't be loaded.</span>
                )}
              </div>
            ) : value.imageProvider === 'replicate' ? (
              <div className="field">
                {fieldLabel('replicate-model', 'Replicate Model', 'imageModel')}
                <ModelCombobox
                  id="replicate-model"
                  ariaLabel="Replicate model"
                  placeholder={defaultReplicateImageModel}
                  value={value.replicateModel}
                  onChange={(next) => onChange({replicateModel: next})}
                  options={replicateImageOptions}
                  allowCustom
                />
                {!catalogs.replicateHasKey ? (
                  <span className="hint">Add a Replicate API token {keyWhere} before generating images.</span>
                ) : replicateImageOptions.length ? null : (
                  <span className="hint">Type an owner/name model id — the model list couldn't be loaded.</span>
                )}
              </div>
            ) : value.imageProvider === 'runware' ? (
              <div className="field">
                <div className="field-label-row">
                  {fieldLabel('runware-model', 'Runware Model', 'imageModel')}
                  <InfoHint
                    label="About Runware models"
                    text="One model serves text-to-image and edits: an attached image rides the model's own image input (a seed image or reference images), so pick an edit-capable model for image edits. Any AIR id works, including community models (civitai:…@…). Cost is the amount Runware reports charging."
                  />
                </div>
                <ModelCombobox
                  id="runware-model"
                  ariaLabel="Runware model"
                  placeholder={defaultRunwareImageModel}
                  value={value.runwareModel}
                  onChange={(next) => onChange({runwareModel: next})}
                  options={runwareImageOptions}
                  allowCustom
                />
                {!catalogs.runwareHasKey ? (
                  <span className="hint">Add a Runware API key {keyWhere} before generating images.</span>
                ) : runwareImageOptions.length ? null : (
                  <span className="hint">Type an AIR model id (e.g. runware:106@1) — the model list couldn't be loaded.</span>
                )}
              </div>
            ) : (
              <div className="field">
                {fieldLabel('openai-image-model', 'Default Image Model', 'imageModel')}
                <ModelCombobox
                  id="openai-image-model"
                  ariaLabel="OpenAI-compatible image model"
                  placeholder="flux2-klein"
                  value={value.openaiImageModel}
                  onChange={(next) => onChange({openaiImageModel: next})}
                  options={catalogs.openaiCompatibleModels}
                  allowCustom
                />
                {catalogs.openaiCompatibleModels.length ? null : (
                  <span className="hint">Type a model id from your server — the model list couldn't be loaded.</span>
                )}
              </div>
            )}
          </div>

          {imageProviderBlocked ? null : value.imageProvider === 'fal' ? (
            <div className="field">
              {fieldLabel('fal-image-edit-model', 'Image-to-Image Model (fal.ai)', 'imageEditModel')}
              <ModelCombobox
                id="fal-image-edit-model"
                ariaLabel="fal.ai image-to-image model"
                placeholder={defaultFalImageEditModel}
                value={value.falImageEditModel}
                onChange={(next) => onChange({falImageEditModel: next})}
                options={falImageEditOptions}
                allowCustom
              />
            </div>
          ) : value.imageProvider === 'replicate' ? (
            <div className="field">
              {fieldLabel('replicate-image-edit-model', 'Image-to-Image Model (Replicate)', 'imageEditModel')}
              <ModelCombobox
                id="replicate-image-edit-model"
                ariaLabel="Replicate image-to-image model"
                placeholder={defaultReplicateImageEditModel}
                value={value.replicateImageEditModel}
                onChange={(next) => onChange({replicateImageEditModel: next})}
                options={replicateImageEditOptions}
                allowCustom
              />
            </div>
          ) : null}

          {/* Upscaling is cloud-only (fal, Replicate, or Runware): airplane mode drops it. */}
          {airplaneMode ? null : value.imageProvider === 'replicate' ? (
            <div className="field">
              {fieldLabel('replicate-upscale-model', 'Image-Upscale Model (Replicate)', 'upscaleModel')}
              <ModelCombobox
                id="replicate-upscale-model"
                ariaLabel="Replicate image-upscale model"
                placeholder={defaultReplicateUpscaleModel}
                value={value.replicateUpscaleModel}
                onChange={(next) => onChange({replicateUpscaleModel: next})}
                options={replicateUpscaleOptions}
                allowCustom
              />
              {!catalogs.replicateHasKey ? (
                <span className="hint">Add a Replicate API token {keyWhere} to upscale images.</span>
              ) : replicateUpscaleOptions.length ? null : (
                <span className="hint">Type an owner/name model id — the model list couldn't be loaded.</span>
              )}
            </div>
          ) : value.imageProvider === 'runware' ? (
            <div className="field">
              {fieldLabel('runware-upscale-model', 'Image-Upscale Model (Runware)', 'upscaleModel')}
              <ModelCombobox
                id="runware-upscale-model"
                ariaLabel="Runware image-upscale model"
                placeholder={defaultRunwareUpscaleModel}
                value={value.runwareUpscaleModel}
                onChange={(next) => onChange({runwareUpscaleModel: next})}
                options={runwareUpscaleOptions}
                allowCustom
              />
              {!catalogs.runwareHasKey ? (
                <span className="hint">Add a Runware API key {keyWhere} to upscale images.</span>
              ) : runwareUpscaleOptions.length ? null : (
                <span className="hint">Type an AIR model id (e.g. runware:504@1) — the model list couldn't be loaded.</span>
              )}
            </div>
          ) : (
          <div className="field">
            {fieldLabel('fal-upscale-model', 'Image-Upscale Model (fal.ai)', 'upscaleModel')}
            <ModelCombobox
              id="fal-upscale-model"
              ariaLabel="fal.ai image-upscale model"
              placeholder={defaultFalUpscaleModel}
              value={value.falUpscaleModel}
              onChange={(next) => onChange({falUpscaleModel: next})}
              options={falUpscaleOptions}
              allowCustom
            />
            {!catalogs.falHasKey ? (
              <span className="hint">Add a fal.ai API key {keyWhere} to upscale images.</span>
            ) : falUpscaleOptions.length ? null : (
              <span className="hint">Type a fal.ai endpoint id — the model list couldn't be loaded.</span>
            )}
          </div>
          )}

          <div className="three-column">
            <div className="field">
              {fieldLabel('image-aspect', 'Image Aspect Ratio', 'imageAspectRatio')}
              <select id="image-aspect" value={value.imageAspectRatio} onChange={(event) => onChange({imageAspectRatio: event.target.value})}>
                {imageAspectRatioOptions.map((option) => <option key={option} value={option}>{option}</option>)}
              </select>
            </div>

            <div className="field">
              {fieldLabel('image-size', 'Image Resolution', 'imageSizePreset')}
              <select id="image-size" value={value.imageSizePreset} onChange={(event) => onChange({imageSizePreset: event.target.value})}>
                {imageSizeOptions.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
              </select>
            </div>

            <div className="field">
              {fieldLabel('image-steps', 'Image Steps', 'imageSteps')}
              <input
                id="image-steps"
                type="number"
                min="1"
                step="1"
                value={value.imageSteps}
                onChange={(event) => onChange({imageSteps: positiveIntOrDefault(event.target.value, defaultImageSteps)})}
              />
            </div>
          </div>
        </div>
      </section>

      <section className="settings-section">
        <h3>
          Inpainting{' '}
          <InfoHint
            label="Inpainting"
            text="Only models with a verified mask contract are listed — the editor refuses any model that cannot honor the painted selection."
          />
          {airplaneMode ? (
            <AirplaneHint
              label="Inpainting unavailable"
              text="Inpainting runs on a cloud provider, which airplane mode blocks — the image editor's crop tool stays available."
            />
          ) : null}
        </h3>
        <div className="settings-rows">
          <div className="two-column">
            <div className="field">
              {fieldLabel('inpaint-provider', 'Inpaint Provider', 'inpaintProvider')}
              <select
                id="inpaint-provider"
                value={value.inpaintProvider}
                onChange={(event) => onChange({inpaintProvider: event.target.value as 'fal' | 'replicate' | 'runware'})}
              >
                <option value="fal">fal.ai (cloud)</option>
                <option value="replicate">Replicate (cloud)</option>
                <option value="runware">Runware (cloud)</option>
              </select>
            </div>
            {value.inpaintProvider === 'runware' ? (
              <div className="field">
                <div className="field-label-row">
                  {fieldLabel('runware-inpaint-model', 'Inpaint Model (Runware)', 'inpaintModel')}
                  <InfoHint
                    label="Runware inpaint models"
                    text="The verified Runware mask models (FLUX.1 Fill): the source and mask upload through Runware's media storage when large, white marks the area to edit, and the result keeps the source image's dimensions. An unverified id typed by hand is refused at the resolver."
                  />
                </div>
                <select
                  id="runware-inpaint-model"
                  value={value.runwareInpaintModel}
                  onChange={(event) => onChange({runwareInpaintModel: event.target.value})}
                >
                  {runwareInpaintOptions.some((option) => option.value === value.runwareInpaintModel) ? null : (
                    <option value={value.runwareInpaintModel}>{value.runwareInpaintModel}</option>
                  )}
                  {runwareInpaintOptions.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
                </select>
                {!catalogs.runwareHasKey ? (
                  <span className="hint">Add a Runware API key {keyWhere} to inpaint images.</span>
                ) : null}
              </div>
            ) : value.inpaintProvider === 'replicate' ? (
              <div className="field">
                {fieldLabel('replicate-inpaint-model', 'Inpaint Model (Replicate)', 'inpaintModel')}
                <select
                  id="replicate-inpaint-model"
                  value={value.replicateInpaintModel}
                  onChange={(event) => onChange({replicateInpaintModel: event.target.value})}
                >
                  {replicateInpaintOptions.some((option) => option.value === value.replicateInpaintModel) ? null : (
                    <option value={value.replicateInpaintModel}>{value.replicateInpaintModel}</option>
                  )}
                  {replicateInpaintOptions.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
                </select>
                {!catalogs.replicateHasKey ? (
                  <span className="hint">Add a Replicate API token {keyWhere} to inpaint images.</span>
                ) : null}
              </div>
            ) : (
              <div className="field">
                {fieldLabel('fal-inpaint-model', 'Inpaint Model (fal.ai)', 'inpaintModel')}
                <select
                  id="fal-inpaint-model"
                  value={value.falInpaintModel}
                  onChange={(event) => onChange({falInpaintModel: event.target.value})}
                >
                  {falInpaintOptions.some((option) => option.value === value.falInpaintModel) ? null : (
                    <option value={value.falInpaintModel}>{value.falInpaintModel}</option>
                  )}
                  {falInpaintOptions.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
                </select>
                {!catalogs.falHasKey ? (
                  <span className="hint">Add a fal.ai API key {keyWhere} to inpaint images.</span>
                ) : null}
              </div>
            )}
          </div>
        </div>
      </section>

      <section className="settings-section">
        <h3>
          Video{' '}
          {airplaneMode ? (
            <AirplaneHint
              label="Video generation unavailable"
              text="Video generation runs on a cloud provider (fal.ai, Replicate, or Runware), which airplane mode blocks. The local video tools — trim, crop, screenshot, split, join, convert — still work in chat and the video editor."
            />
          ) : null}
        </h3>
        <div className="settings-rows">
          <div className="field">
            <div className="field-label-row">
              {fieldLabel('video-provider', 'Video Provider', 'videoProvider')}              <InfoHint
                label="Video provider"
                text="Which cloud backend generate_video uses: fal.ai, Replicate, or Runware. All serve text-to-video, image-to-video, and extending an attached clip; motion control and keyframe transitions are fal.ai only (a request for those elsewhere fails with a note suggesting the switch). The video transforms follow this provider too — reframe stays fal/Replicate, since Runware's catalog carries no video-reframe model. Lip sync and audio are configured below regardless of this setting."
              />
            </div>
            <select
              id="video-provider"
              value={value.videoProvider}
              onChange={(event) => onChange({videoProvider: event.target.value as 'fal' | 'replicate' | 'runware'})}
            >
              <option value="fal">fal.ai</option>
              <option value="replicate">Replicate</option>
              <option value="runware" disabled={airplaneMode}>Runware (cloud)</option>
            </select>
          </div>

          <div className="three-column">
            <div className="field">
              <div className="field-label-row">
                {fieldLabel('video-aspect', 'Video Aspect Ratio', 'videoAspectRatio')}
                <InfoHint
                  label="Video aspect ratio"
                  text="The standing output shape for video generation. Image-to-video and extend inherit the attached image's or clip's orientation; reference media (character sheets, style references) and text-to-video use this setting. Auto defers the choice to the model — models without an aspect control use their own default."
                />
              </div>
              <select id="video-aspect" value={value.videoAspectRatio} onChange={(event) => onChange({videoAspectRatio: event.target.value})}>
                {videoAspectRatioOptions.map((option) => <option key={option} value={option}>{videoAspectRatioLabels[option] ?? option}</option>)}
              </select>
            </div>

            <div className="field">
              <div className="field-label-row">
                {fieldLabel('video-resolution', 'Video Resolution', 'videoResolution')}
                <InfoHint
                  label="Video resolution"
                  text="The default output resolution tier for video generation — the fallback when a request doesn't state one. Tiers vary by model: a tier the selected model doesn't support is ignored with a notice and the model's own default is used. Auto lets the model choose. Higher tiers cost more on per-second and token-billed models."
                />
              </div>
              <select id="video-resolution" value={value.videoResolution} onChange={(event) => onChange({videoResolution: event.target.value})}>
                {videoResolutionOptions.map((option) => <option key={option} value={option}>{videoResolutionLabels[option] ?? option}</option>)}
              </select>
            </div>

            <div className="field">
              <div className="field-label-row">
                {fieldLabel('video-duration', 'Default Video Duration', 'videoDuration')}
                <InfoHint
                  label="Default video duration"
                  text="Applies to all video modes as the default clip length. Your request can override it per generation."
                />
              </div>
              <select id="video-duration" value={value.videoDuration} onChange={(event) => onChange({videoDuration: event.target.value})}>
                {videoDurationPickerOptions(durationOptions.video).map((option) => <option key={option} value={option}>{videoDurationLabels[option] ?? option}</option>)}
              </select>
            </div>
          </div>

          {value.videoProvider === 'runware' ? (
          <div className="two-column">
            <div className="field">
              <div className="field-label-row">
                {fieldLabel('runware-video-model', 'Text-to-Video Model (Runware)', 'videoModel')}
                <InfoHint
                  label="Runware video models"
                  text="Kling 4.0 serves text-to-video and image-to-video (an attached frame animates through its frameImages input); Veo 3.1 is the extend default (its inputs.video continues an attached clip). Costs are the amounts Runware reports charging."
                />
              </div>
              <ModelCombobox
                id="runware-video-model"
                ariaLabel="Runware text-to-video model"
                placeholder={defaultRunwareVideoModel}
                value={value.runwareVideoModel}
                onChange={(next) => onChange({runwareVideoModel: next})}
                options={runwareVideoOptions}
                allowCustom
              />
              {!catalogs.runwareHasKey ? (
                <span className="hint">Add a Runware API key {keyWhere} to generate videos.</span>
              ) : runwareVideoOptions.length ? null : (
                <span className="hint">Type an AIR model id (e.g. klingai:kling-video@4.0).</span>
              )}
            </div>

            <div className="field">
              <div className="field-label-row">
                {fieldLabel('runware-video-image-model', 'Image-to-Video Model (Runware)', 'videoImageModel')}
                <InfoHint
                  label="Image-to-video clip lengths"
                  text={`Supported clip lengths: ${formatSupportedDurations(durationOptions.image)}`}
                />
              </div>
              <ModelCombobox
                id="runware-video-image-model"
                ariaLabel="Runware image-to-video model"
                placeholder={defaultRunwareVideoImageModel}
                value={value.runwareVideoImageModel}
                onChange={(next) => onChange({runwareVideoImageModel: next})}
                options={runwareVideoImageOptions}
                allowCustom
              />
            </div>
          </div>
          ) : value.videoProvider === 'replicate' ? (
          <div className="two-column">
            <div className="field">
              {fieldLabel('replicate-video-model', 'Text-to-Video Model (Replicate)', 'videoModel')}
              <ModelCombobox
                id="replicate-video-model"
                ariaLabel="Replicate text-to-video model"
                placeholder={defaultReplicateVideoModel}
                value={value.replicateVideoModel}
                onChange={(next) => onChange({replicateVideoModel: next})}
                options={replicateVideoOptions}
                allowCustom
              />
              {!catalogs.replicateHasKey ? (
                <span className="hint">Add a Replicate API token {keyWhere} to generate videos.</span>
              ) : replicateVideoOptions.length ? null : (
                <span className="hint">Type an owner/name model id.</span>
              )}
            </div>

            <div className="field">
              <div className="field-label-row">
                {fieldLabel('replicate-video-image-model', 'Image-to-Video Model (Replicate)', 'videoImageModel')}
                <InfoHint
                  label="Image-to-video clip lengths"
                  text={`Supported clip lengths: ${formatSupportedDurations(durationOptions.image)}`}
                />
              </div>
              <ModelCombobox
                id="replicate-video-image-model"
                ariaLabel="Replicate image-to-video model"
                placeholder={defaultReplicateVideoImageModel}
                value={value.replicateVideoImageModel}
                onChange={(next) => onChange({replicateVideoImageModel: next})}
                options={replicateVideoImageOptions}
                allowCustom
              />
            </div>
          </div>
          ) : (
          <div className="two-column">
            <div className="field">
              {fieldLabel('fal-video-model', 'Text-to-Video Model (fal.ai)', 'videoModel')}
              <ModelCombobox
                id="fal-video-model"
                ariaLabel="fal.ai text-to-video model"
                placeholder={defaultFalVideoModel}
                value={value.falVideoModel}
                onChange={(next) => onChange({falVideoModel: next})}
                options={falVideoOptions}
                allowCustom
              />
              {!catalogs.falHasKey ? (
                <span className="hint">Add a fal.ai API key {keyWhere} to generate videos.</span>
              ) : falVideoOptions.length ? null : (
                <span className="hint">Type a fal.ai text-to-video endpoint id.</span>
              )}
            </div>

            <div className="field">
              <div className="field-label-row">
                {fieldLabel('fal-video-image-model', 'Image-to-Video Model (fal.ai)', 'videoImageModel')}
                <InfoHint
                  label="Image-to-video clip lengths"
                  text={`Supported clip lengths: ${formatSupportedDurations(durationOptions.image)}`}
                />
              </div>
              <ModelCombobox
                id="fal-video-image-model"
                ariaLabel="fal.ai image-to-video model"
                placeholder={defaultFalVideoImageModel}
                value={value.falVideoImageModel}
                onChange={(next) => onChange({falVideoImageModel: next})}
                options={falVideoImageOptions}
                allowCustom
              />
            </div>
          </div>
          )}

          {value.videoProvider === 'runware' ? (
          <div className="two-column">
            <div className="field">
              <div className="field-label-row">
                {fieldLabel('runware-video-extend-model', 'Video-Extend Model (Runware)', 'videoExtendModel')}
                <InfoHint
                  label="Video-extend clip lengths"
                  text={`Supported clip lengths: ${formatSupportedDurations(durationOptions.extend)}`}
                />
              </div>
              <ModelCombobox
                id="runware-video-extend-model"
                ariaLabel="Runware video-extend model"
                placeholder={defaultRunwareVideoExtendModel}
                value={value.runwareVideoExtendModel}
                onChange={(next) => onChange({runwareVideoExtendModel: next})}
                options={runwareVideoExtendOptions}
                allowCustom
              />
            </div>

            <div className="field">
              <div className="field-label-row">
                {fieldLabel('runware-video-upscale-model', 'Video-Upscale Model (Runware)', 'videoUpscaleModel')}
                <InfoHint
                  label="Video upscaling on Runware"
                  text="upscale_video's task when Runware is the video provider. The default (FLUX 3 Upscaler) takes a 1.5–3× factor; a request outside that range scales by what the model can do, with a notice."
                />
              </div>
              <ModelCombobox
                id="runware-video-upscale-model"
                ariaLabel="Runware video-upscale model"
                placeholder={defaultRunwareVideoUpscaleModel}
                value={value.runwareVideoUpscaleModel}
                onChange={(next) => onChange({runwareVideoUpscaleModel: next})}
                options={runwareVideoUpscaleOptions}
                allowCustom
              />
            </div>
          </div>
          ) : value.videoProvider === 'fal' ? (
          <>
          <div className="two-column">
            <div className="field">
              <div className="field-label-row">
                {fieldLabel('fal-video-keyframe-model', 'Keyframes (Start→End) Model (fal.ai)', 'videoKeyframeModel')}
                <InfoHint
                  label="Keyframe clip lengths"
                  text={`Supported clip lengths: ${formatSupportedDurations(durationOptions.keyframe)}`}
                />
              </div>
              <ModelCombobox
                id="fal-video-keyframe-model"
                ariaLabel="fal.ai keyframe model"
                placeholder={defaultFalVideoKeyframeModel}
                value={value.falVideoKeyframeModel}
                onChange={(next) => onChange({falVideoKeyframeModel: next})}
                options={falVideoKeyframeOptions}
                allowCustom
              />
            </div>

            <div className="field">
              <div className="field-label-row">
                {fieldLabel('fal-video-extend-model', 'Video-Extend Model (fal.ai)', 'videoExtendModel')}
                <InfoHint
                  label="Video-extend clip lengths"
                  text={`Supported clip lengths: ${formatSupportedDurations(durationOptions.extend)}`}
                />
              </div>
              <ModelCombobox
                id="fal-video-extend-model"
                ariaLabel="fal.ai video-extend model"
                placeholder={defaultFalVideoExtendModel}
                value={value.falVideoExtendModel}
                onChange={(next) => onChange({falVideoExtendModel: next})}
                options={falVideoExtendOptions}
                allowCustom
              />
            </div>
          </div>

          <div className="two-column">
            <div className="field">
              {fieldLabel('fal-video-motion-model', 'Motion-Control Model (fal.ai)', 'videoMotionModel')}
              <ModelCombobox
                id="fal-video-motion-model"
                ariaLabel="fal.ai motion-control model"
                placeholder={defaultFalVideoMotionModel}
                value={value.falVideoMotionModel}
                onChange={(next) => onChange({falVideoMotionModel: next})}
                options={falVideoMotionOptions}
                allowCustom
              />
            </div>

            <div className="field">
              {fieldLabel('fal-video-upscale-model', 'Video-Upscale Model (fal.ai)', 'videoUpscaleModel')}
              <ModelCombobox
                id="fal-video-upscale-model"
                ariaLabel="fal.ai video-upscale model"
                placeholder={defaultFalVideoUpscaleModel}
                value={value.falVideoUpscaleModel}
                onChange={(next) => onChange({falVideoUpscaleModel: next})}
                options={falVideoUpscaleOptions}
                allowCustom
              />
            </div>
          </div>
          </>
          ) : (
          <div className="two-column">
            <div className="field">
              <div className="field-label-row">
                {fieldLabel('replicate-video-upscale-model', 'Video-Upscale Model (Replicate)', 'videoUpscaleModel')}
                <InfoHint
                  label="Video upscaling on Replicate"
                  text="upscale_video's endpoint when Replicate is the video provider. Factor-based models take the requested multiplier; resolution-tier models (topazlabs/video-upscale) derive their 720p/1080p/4K target from the source clip's own frame size times the factor, snapped up so an upscale never lands below the request."
                />
              </div>
              <ModelCombobox
                id="replicate-video-upscale-model"
                ariaLabel="Replicate video-upscale model"
                placeholder={defaultReplicateVideoUpscaleModel}
                value={value.replicateVideoUpscaleModel}
                onChange={(next) => onChange({replicateVideoUpscaleModel: next})}
                options={replicateVideoUpscaleOptions}
                allowCustom
              />
              {!catalogs.replicateHasKey ? (
                <span className="hint">Add a Replicate API token {keyWhere} to upscale videos.</span>
              ) : replicateVideoUpscaleOptions.length ? null : (
                <span className="hint">Type an owner/name model id — the model list couldn't be loaded.</span>
              )}
            </div>

            <div className="field">
              <div className="field-label-row">
                {fieldLabel('replicate-video-extend-model', 'Video-Extend Model (Replicate)', 'videoExtendModel')}
                <InfoHint
                  label="Video-extend clip lengths"
                  text={`Supported clip lengths: ${formatSupportedDurations(durationOptions.extend)}`}
                />
              </div>
              <ModelCombobox
                id="replicate-video-extend-model"
                ariaLabel="Replicate video-extend model"
                placeholder={defaultReplicateVideoExtendModel}
                value={value.replicateVideoExtendModel}
                onChange={(next) => onChange({replicateVideoExtendModel: next})}
                options={replicateVideoExtendOptions}
                allowCustom
              />
            </div>
          </div>
          )}

          {value.videoProvider === 'runware' ? (
          <div className="two-column">
            <div className="field">
              <div className="field-label-row">
                {fieldLabel('runware-video-restyle-model', 'Video-Restyle Model (Runware)', 'videoRestyleModel')}
                <InfoHint
                  label="Video restyling on Runware"
                  text="Re-renders an attached clip under a style prompt while keeping its motion — the restyle_video tool's task when Runware is the video provider. The default (FLUX Video Edit [fast]) edits via natural-language instruction and caps input clips at 15 seconds; Runware's catalog has no video-reframe model, so reframe_video stays on fal.ai or Replicate (the local transform_video crops or pads for free)."
                />
              </div>
              <ModelCombobox
                id="runware-video-restyle-model"
                ariaLabel="Runware video-restyle model"
                placeholder={defaultRunwareVideoRestyleModel}
                value={value.runwareVideoRestyleModel}
                onChange={(next) => onChange({runwareVideoRestyleModel: next})}
                options={runwareVideoRestyleOptions}
                allowCustom
              />
            </div>
          </div>
          ) : value.videoProvider === 'replicate' ? (
          <div className="two-column">
            <div className="field">
              <div className="field-label-row">
                {fieldLabel('replicate-video-reframe-model', 'Video-Reframe Model (Replicate)', 'videoReframeModel')}
                <InfoHint
                  label="Video reframing on Replicate"
                  text="Converts an attached clip to a new aspect ratio by generating the missing canvas area instead of cropping — the reframe_video tool's endpoint when Replicate is the video provider. The default (luma/reframe-video) reframes up to 30s at 720p."
                />
              </div>
              <ModelCombobox
                id="replicate-video-reframe-model"
                ariaLabel="Replicate video-reframe model"
                placeholder={defaultReplicateVideoReframeModel}
                value={value.replicateVideoReframeModel}
                onChange={(next) => onChange({replicateVideoReframeModel: next})}
                options={replicateVideoReframeOptions}
                allowCustom
              />
            </div>

            <div className="field">
              <div className="field-label-row">
                {fieldLabel('replicate-video-restyle-model', 'Video-Restyle Model (Replicate)', 'videoRestyleModel')}
                <InfoHint
                  label="Video restyling on Replicate"
                  text="Re-renders an attached clip under a style prompt while keeping its motion — the restyle_video tool's endpoint when Replicate is the video provider. The default (kling-v3-omni-video) edits via natural-language instruction and accepts attached reference images; wan-2.7-videoedit and luma/modify-video are the alternatives. Input clips are capped by the model."
                />
              </div>
              <ModelCombobox
                id="replicate-video-restyle-model"
                ariaLabel="Replicate video-restyle model"
                placeholder={defaultReplicateVideoRestyleModel}
                value={value.replicateVideoRestyleModel}
                onChange={(next) => onChange({replicateVideoRestyleModel: next})}
                options={replicateVideoRestyleOptions}
                allowCustom
              />
            </div>
          </div>
          ) : (
          <div className="two-column">
            <div className="field">
              <div className="field-label-row">
                {fieldLabel('fal-video-reframe-model', 'Video-Reframe Model (fal.ai)', 'videoReframeModel')}
                <InfoHint
                  label="Video reframing"
                  text="Converts an attached clip to a new aspect ratio (16:9 → 9:16 vertical for Reels/Shorts/TikTok) by generating the missing canvas area instead of cropping — the reframe_video tool's endpoint. Billed per second of the input clip; clips up to 60 seconds. For free local alternatives, transform_video can center-crop or fill the added canvas with a blurred copy or black bars."
                />
              </div>
              <ModelCombobox
                id="fal-video-reframe-model"
                ariaLabel="fal.ai video-reframe model"
                placeholder={defaultFalVideoReframeModel}
                value={value.falVideoReframeModel}
                onChange={(next) => onChange({falVideoReframeModel: next})}
                options={falVideoReframeOptions}
                allowCustom
              />
            </div>

            <div className="field">
              <div className="field-label-row">
                {fieldLabel('fal-video-restyle-model', 'Video-Restyle Model (fal.ai)', 'videoRestyleModel')}
                <InfoHint
                  label="Video restyling"
                  text="Re-renders an attached clip under a style prompt while keeping its motion — anime, claymation, a different look or characters (the restyle_video tool's endpoint). Attaching an image alongside the clip rides as a style/appearance reference on models that accept one. Generation, not a local filter: billed like video generation (commonly per second), and input clips are capped by the model (around 3–15 seconds on Kling, 2–10 on Wan)."
                />
              </div>
              <ModelCombobox
                id="fal-video-restyle-model"
                ariaLabel="fal.ai video-restyle model"
                placeholder={defaultFalVideoRestyleModel}
                value={value.falVideoRestyleModel}
                onChange={(next) => onChange({falVideoRestyleModel: next})}
                options={falVideoRestyleOptions}
                allowCustom
              />
            </div>
          </div>
          )}
        </div>
      </section>

      <section className="settings-section">
        <h3>
          Audio{' '}
          {airplaneMode ? (
            <AirplaneHint
              label="Audio generation unavailable"
              text={value.speechProvider === 'ollama'
                ? 'Music, sound effects, audio extension, voice design, and voice cloning run on fal.ai or Replicate, which airplane mode blocks. Speech still works — it runs on the local Ollama speech model — and the local audio tools (trim, cut, split, join, convert) keep working in chat and the audio editor.'
                : 'Speech, music, sound effects, and audio extension run on a cloud provider, which airplane mode blocks. The local audio tools — trim, cut, split, join, convert — still work in chat and the audio editor. Switching Speech Provider to Ollama keeps text-to-speech available offline.'}
            />
          ) : null}
        </h3>
        <div className="two-column">
          <div className="field">
            <div className="field-label-row">
              {fieldLabel('sound-duration', 'Default Sound Duration', 'soundDuration')}
              <InfoHint
                label="Default sound duration"
                text="Applies to music and sound-effect generation when a request doesn't state a length. Auto leaves the clip length to the model — it decides from the prompt. A request naming a length always wins, and speech generation is unaffected (its length follows the spoken text)."
              />
            </div>
            <select id="sound-duration" value={value.soundDuration} onChange={(event) => onChange({soundDuration: event.target.value})}>
              {durationOptions.sound.map((option) => <option key={option} value={option}>{soundDurationLabels[option] ?? option}</option>)}
            </select>
          </div>
          <div className="field">
            <div className="field-label-row">
              {fieldLabel('audio-extend-duration', 'Default Extension Length', 'audioExtendDuration')}
              <InfoHint
                label="Default extension length"
                text="Seconds extend_audio adds to a clip when a request doesn't state a length. Auto keeps each extend model's own behavior — the inpaint/outpaint models' built-in 30s, or sonauto deciding from the audio. A request naming a length always wins; each model caps what it accepts."
              />
            </div>
            <select id="audio-extend-duration" value={value.audioExtendDuration} onChange={(event) => onChange({audioExtendDuration: event.target.value})}>
              {audioExtendDurationOptions.map((option) => <option key={option} value={option}>{audioExtendDurationLabels[option] ?? option}</option>)}
            </select>
          </div>
          <div className="field">
            <div className="field-label-row">
              {fieldLabel('audio-provider', 'Audio Provider', 'audioProvider')}
              <InfoHint
                label="Audio provider"
                text="Where the non-voice audio tools run — generate_sound (music and sound effects) and add_sound_effects (a video's soundtrack). fal.ai's endpoints cover both; Replicate's defaults also cover both, and Runware's Mirelo SFX serves text-to-audio and mixes the soundtrack into the attached clip. Audio extension stays on fal.ai/Replicate (Runware has no input-audio model), voice design and voice cloning stay on fal.ai, and speech routes by the Speech Provider below."
              />
              {airplaneMode ? (
                <AirplaneHint
                  label="Cloud audio unavailable"
                  text="Music and sound-effect generation runs on a cloud provider, which airplane mode blocks."
                />
              ) : null}
            </div>
            <select
              id="audio-provider"
              value={value.audioProvider}
              onChange={(event) => onChange({audioProvider: event.target.value as 'fal' | 'replicate' | 'runware'})}
            >
              <option value="fal" disabled={airplaneMode}>fal.ai (cloud)</option>
              <option value="replicate" disabled={airplaneMode}>Replicate (cloud)</option>
              <option value="runware" disabled={airplaneMode}>Runware (cloud)</option>
            </select>
          </div>
          {value.audioProvider === 'runware' ? (
            <>
              <div className="field">
                {fieldLabel('runware-sound-effects-model', 'Music & Sound Effects Model (Runware)', 'soundEffectsModel')}
                <ModelCombobox
                  id="runware-sound-effects-model"
                  ariaLabel="Runware music and sound effects model"
                  placeholder={defaultRunwareSoundEffectsModel}
                  value={value.runwareSoundEffectsModel}
                  onChange={(next) => onChange({runwareSoundEffectsModel: next})}
                  options={runwareSoundEffectOptions}
                  allowCustom
                />
                {!catalogs.runwareHasKey ? (
                  <span className="hint">Add a Runware API key {keyWhere} to generate sound.</span>
                ) : runwareSoundEffectOptions.length ? null : (
                  <span className="hint">Type an AIR model id (e.g. mirelo:1@1 for SFX, minimax:music@2.6 for music).</span>
                )}
              </div>
              <div className="field">
                <div className="field-label-row">
                  {fieldLabel('runware-sound-effects-video-model', 'Video Sound Effects Model (Runware)', 'soundEffectsVideoModel')}
                  <InfoHint
                    label="Video sound effects model"
                    text="Watches an attached video and generates a synchronized soundtrack for it (the add_sound_effects tool). Runware's Mirelo SFX mixes the sound INTO the clip and returns the video directly; its text-only shape delivers a standalone soundtrack replace_audio can lay under the video."
                  />
                </div>
                <ModelCombobox
                  id="runware-sound-effects-video-model"
                  ariaLabel="Runware video sound effects model"
                  placeholder={defaultRunwareSoundEffectsVideoModel}
                  value={value.runwareSoundEffectsVideoModel}
                  onChange={(next) => onChange({runwareSoundEffectsVideoModel: next})}
                  options={runwareSoundEffectsVideoOptions}
                  allowCustom
                />
              </div>
            </>
          ) : value.audioProvider === 'replicate' ? (
            <>
              <div className="field">
                {fieldLabel('replicate-sound-effects-model', 'Music & Sound Effects Model (Replicate)', 'soundEffectsModel')}
                <ModelCombobox
                  id="replicate-sound-effects-model"
                  ariaLabel="Replicate music and sound effects model"
                  placeholder={defaultReplicateSoundEffectsModel}
                  value={value.replicateSoundEffectsModel}
                  onChange={(next) => onChange({replicateSoundEffectsModel: next})}
                  options={replicateSoundEffectOptions}
                  allowCustom
                />
              </div>
              <div className="field">
                <div className="field-label-row">
                  {fieldLabel('replicate-sound-effects-video-model', 'Video Sound Effects Model (Replicate)', 'soundEffectsVideoModel')}
                  <InfoHint
                    label="Video sound effects model"
                    text="Watches an attached video and generates a synchronized soundtrack for it (the add_sound_effects tool). The Replicate default (MMAudio) delivers the soundtrack as a standalone clip that replace_audio lays under the video; a video-with-soundtrack model would return the mixed clip directly."
                  />
                </div>
                <ModelCombobox
                  id="replicate-sound-effects-video-model"
                  ariaLabel="Replicate video sound effects model"
                  placeholder={defaultReplicateSoundEffectsVideoModel}
                  value={value.replicateSoundEffectsVideoModel}
                  onChange={(next) => onChange({replicateSoundEffectsVideoModel: next})}
                  options={replicateSoundEffectsVideoOptions}
                  allowCustom
                />
              </div>
            </>
          ) : (
            <>
              <div className="field">
                {fieldLabel('fal-sound-effects-model', 'Music & Sound Effects Model (fal.ai)', 'soundEffectsModel')}
                <ModelCombobox
                  id="fal-sound-effects-model"
                  ariaLabel="fal.ai sound effects model"
                  placeholder={defaultFalSoundEffectsModel}
                  value={value.falSoundEffectsModel}
                  onChange={(next) => onChange({falSoundEffectsModel: next})}
                  options={falSoundEffectOptions}
                  allowCustom
                />
              </div>
              <div className="field">
                <div className="field-label-row">
                  {fieldLabel('fal-sound-effects-video-model', 'Video Sound Effects Model (fal.ai)', 'soundEffectsVideoModel')}
                  <InfoHint
                    label="Video sound effects model"
                    text="Watches an attached video and generates a synchronized soundtrack for it (the add_sound_effects tool): foley, ambience, sound design layered over the picture. Mirelo SFX 2.0 returns the clip with the sound mixed in; video-to-audio endpoints deliver the soundtrack as a clip that replace_audio can lay under the video."
                  />
                </div>
                <ModelCombobox
                  id="fal-sound-effects-video-model"
                  ariaLabel="fal.ai video sound effects model"
                  placeholder={defaultFalSoundEffectsVideoModel}
                  value={value.falSoundEffectsVideoModel}
                  onChange={(next) => onChange({falSoundEffectsVideoModel: next})}
                  options={falSoundEffectsVideoOptions}
                  allowCustom
                />
              </div>
            </>
          )}
          {value.audioProvider === 'runware' ? (
            <span className="hint">Audio extension (extend_audio) runs on fal.ai or Replicate — Runware publishes no input-audio model. Pick this section's Audio Provider accordingly when a workflow needs extensions.</span>
          ) : value.audioProvider === 'replicate' ? (
            <div className="field">
              <div className="field-label-row">
                {fieldLabel('replicate-audio-extend-model', 'Audio Extend Model (Replicate)', 'audioExtendModel')}
                <InfoHint
                  label="Audio extend model"
                  text="Lengthens an existing clip with generated audio (the extend_audio tool). The Replicate default (MusicGen) continues a clip FORWARD from its end within a bounded total length (~30s including the source) — before-extensions, exact-preservation inpaint, and long sources need fal.ai."
                />
              </div>
              <ModelCombobox
                id="replicate-audio-extend-model"
                ariaLabel="Replicate audio extend model"
                placeholder={defaultReplicateAudioExtendModel}
                value={value.replicateAudioExtendModel}
                onChange={(next) => onChange({replicateAudioExtendModel: next})}
                options={replicateAudioExtendOptions}
                allowCustom
              />
            </div>
          ) : (
            <div className="field">
              <div className="field-label-row">
                {fieldLabel('fal-audio-extend-model', 'Audio Extend Model (fal.ai)', 'audioExtendModel')}
                <InfoHint
                  label="Audio extend model"
                  text="Lengthens an existing clip with generated audio (the extend_audio tool): the inpaint/outpaint models preserve the original exactly and sonauto continues from the audio; before-extensions and long sources work here."
                />
              </div>
              <ModelCombobox
                id="fal-audio-extend-model"
                ariaLabel="fal.ai audio extend model"
                placeholder={defaultFalAudioExtendModel}
                value={value.falAudioExtendModel}
                onChange={(next) => onChange({falAudioExtendModel: next})}
                options={falAudioExtendOptions}
                allowCustom
              />
            </div>
          )}
        </div>
        <div className="voice-model-group">
          <div className="voice-model-group-label">Voice models</div>
          <div className="two-column">
            <div className="field">
              <div className="field-label-row">
                {fieldLabel('speech-provider', 'Speech Provider', 'speechProvider')}
                <InfoHint
                  label="Speech provider"
                  text="Where generate_speech runs: fal.ai's cloud speech endpoints, Replicate's text-to-speech collection (MiniMax speech, Chatterbox, Qwen3-TTS, …), Runware's speech models (MiniMax Speech's ~300-voice catalog), or a speech model pulled into the local Ollama (phonon and its language variants — Ollama 0.40.11+, macOS). Ollama speech needs no API key and keeps working in airplane mode, but it speaks only its model's catalog voices. Voice design and voice cloning stay on fal.ai whichever provider is selected — on Ollama, Replicate, or Runware an attached clip is ignored with a notice — and music, sound effects, and audio extension follow the Audio Provider above."
                />
                {airplaneMode && value.speechProvider !== 'ollama' ? (
                  <AirplaneHint
                    label="Cloud speech unavailable"
                    text="Cloud speech (fal.ai or Replicate) is blocked while airplane mode is on — switch the provider to Ollama for local text-to-speech."
                  />
                ) : null}
              </div>
              <select
                id="speech-provider"
                value={value.speechProvider}
                onChange={(event) => onChange({speechProvider: event.target.value as SpeechProviderID})}
              >
                <option value="fal" disabled={airplaneMode}>fal.ai (cloud)</option>
                <option value="replicate" disabled={airplaneMode}>Replicate (cloud)</option>
                <option value="runware" disabled={airplaneMode}>Runware (cloud)</option>
                <option value="ollama">Ollama (local)</option>
              </select>
            </div>
            {value.speechProvider === 'ollama' ? (
              <div className="field">
                {fieldLabel('ollama-speech-model', 'Speech Model (TTS, Ollama)', 'audioModel')}
                <ModelCombobox
                  id="ollama-speech-model"
                  ariaLabel="Ollama speech model"
                  placeholder={defaultOllamaSpeechModel}
                  value={value.ollamaSpeechModel}
                  onChange={(next) => onChange({ollamaSpeechModel: next})}
                  options={ollamaSpeechOptions}
                  allowCustom
                />
                {ollamaSpeechOptions.length ? null : (
                  <span className="hint">No speech model is pulled — run <code>ollama pull phonon</code> (Ollama 0.40.11+ on macOS), then refresh the Ollama connection.</span>
                )}
              </div>
            ) : value.speechProvider === 'runware' ? (
              <div className="field">
                <div className="field-label-row">
                  {fieldLabel('runware-speech-model', 'Speech Model (TTS, Runware)', 'audioModel')}
                  <InfoHint
                    label="Runware speech model"
                    text="Runware's text-to-speech models, filtered to the ones that speak written text. The MiniMax speech models carry a ~300-voice catalog enum (English_expressive_narrator, Japanese_ColdQueen, …); a model applies whichever emotion/speed controls its schema declares and drops the rest with a notice. No voice cloning on Runware — an attached reference clip is ignored with a notice naming fal.ai. The reported cost is what Runware charged."
                  />
                </div>
                <ModelCombobox
                  id="runware-speech-model"
                  ariaLabel="Runware speech model"
                  placeholder={defaultRunwareSpeechModel}
                  value={value.runwareSpeechModel}
                  onChange={(next) => onChange({runwareSpeechModel: next})}
                  options={runwareSpeechOptions}
                  allowCustom
                />
                {!catalogs.runwareHasKey ? (
                  <span className="hint">Add a Runware API key {keyWhere} to generate speech.</span>
                ) : runwareSpeechOptions.length ? null : (
                  <span className="hint">Type an AIR model id (e.g. minimax:speech@2.8).</span>
                )}
              </div>
            ) : value.speechProvider === 'replicate' ? (
              <div className="field">
                <div className="field-label-row">
                  {fieldLabel('replicate-speech-model', 'Speech Model (TTS, Replicate)', 'audioModel')}
                  <InfoHint
                    label="Replicate speech model"
                    text="Replicate's text-to-speech collection, filtered to the models that speak written text (the voice-cloning utility that only mints a voice id drops out). The MiniMax speech models take their catalog voice names (Wise_Woman, English_Wiselady, …) as the voice plus an emotion and a speed; other models apply whichever of those controls their schema declares and drop the rest with a notice. Replicate reports no per-run price, so these calls show as “?” in usage."
                  />
                </div>
                <ModelCombobox
                  id="replicate-speech-model"
                  ariaLabel="Replicate speech model"
                  placeholder={defaultReplicateSpeechModel}
                  value={value.replicateSpeechModel}
                  onChange={(next) => onChange({replicateSpeechModel: next})}
                  options={replicateSpeechOptions}
                  allowCustom
                />
              </div>
            ) : (
              <div className="field">
                {fieldLabel('fal-audio-model', 'Speech Model (TTS, fal.ai)', 'audioModel')}
                <ModelCombobox
                  id="fal-audio-model"
                  ariaLabel="fal.ai speech model"
                  placeholder={defaultFalAudioModel}
                  value={value.falAudioModel}
                  onChange={(next) => onChange({falAudioModel: next})}
                  options={falAudioOptions}
                  allowCustom
                />
              </div>
            )}
            <div className="field">
              <div className="field-label-row">
                {fieldLabel('fal-voice-design-model', 'Voice Design Model (fal.ai)', 'voiceDesignModel')}
                <InfoHint
                  label="Voice design model"
                  text="Creates a NEW voice from a written description (the generate_voice tool) — a preview clip of the described voice plus its reusable voice id. The id pairs with the MiniMax speech endpoints (marked in the Speech Model list) to speak new text in the designed voice; speech from text on other models and cloning an attached voice stay on their own tools. Always fal.ai, whichever speech provider is selected."
                />
              </div>
              <ModelCombobox
                id="fal-voice-design-model"
                ariaLabel="fal.ai voice design model"
                placeholder={defaultFalVoiceDesignModel}
                value={value.falVoiceDesignModel}
                onChange={(next) => onChange({falVoiceDesignModel: next})}
                options={falVoiceDesignOptions}
                allowCustom
              />
            </div>
            <div className="field">
              <div className="field-label-row">
                {fieldLabel('fal-audio-clone-model', 'Voice Cloning Model (fal.ai)', 'audioCloneModel')}
                <InfoHint
                  label="Voice cloning model"
                  text="Speaks new text in an ATTACHED voice (the generate_speech tool's cloning path): a short clip of the speaker rides as the reference and this endpoint renders the words in that timbre. A designed voice id or a voice named in the request outranks the clip, and speech without an attached reference never clones. Always fal.ai — with Ollama or Replicate as the speech provider an attached clip is ignored with a notice, since those routes cannot clone."
                />
              </div>
              <ModelCombobox
                id="fal-audio-clone-model"
                ariaLabel="fal.ai voice cloning model"
                placeholder={defaultFalAudioCloneModel}
                value={value.falAudioCloneModel}
                onChange={(next) => onChange({falAudioCloneModel: next})}
                options={falAudioOptions}
                allowCustom
              />
            </div>
          </div>
        </div>
        {!airplaneMode && value.audioProvider === 'replicate' && !catalogs.replicateHasKey ? (
          <span className="hint">
            Add a Replicate API token {keyWhere} to generate music or sound effects{value.speechProvider === 'replicate' ? ' and speech' : ''} on Replicate — {value.speechProvider === 'fal' ? 'speech and ' : ''}the fal-only slots (design, clone) still need a fal.ai key.
          </span>
        ) : null}
        {!airplaneMode && value.audioProvider !== 'replicate' && value.speechProvider === 'replicate' && !catalogs.replicateHasKey ? (
          <span className="hint">
            Add a Replicate API token {keyWhere} to generate speech on Replicate — music, sound effects, voice design, and cloning still run on fal.ai.
          </span>
        ) : null}
        {!airplaneMode && value.audioProvider !== 'replicate' && !catalogs.falHasKey ? (
          <span className="hint">
            {value.speechProvider === 'ollama'
              ? <>Add a fal.ai API key {keyWhere} to generate music or sound effects, design voices, or clone one — speech already runs locally on Ollama.</>
              : value.speechProvider === 'replicate'
                ? <>Add a fal.ai API key {keyWhere} to generate music or sound effects, design voices, or clone one — speech runs on Replicate.</>
                : <>Add a fal.ai API key {keyWhere} to generate speech, music, or sound effects.</>}
          </span>
        ) : null}
      </section>

      <section className="settings-section">
        <h3>
          Transcription &amp; Subtitles{' '}
          <InfoHint
            label="What transcription powers"
            text="Powers transcribe_audio: plain transcripts, timestamped VTT/SRT subtitles, and English translation of foreign-language speech; convert_subtitles and burn_subtitles consume the result. The whisper binary, model, and VAD model themselves are configured under Local Tools."
          />
        </h3>
        <div className="settings-rows">
          <div className="two-column">
            <div className="field">
              <div className="field-label-row">
                {fieldLabel('transcription-provider', 'Transcription Provider', 'transcriptionProvider')}
                {airplaneMode ? (
                  <AirplaneHint
                    label="Transcription pinned to local whisper"
                    text="Airplane mode always transcribes locally — the fal.ai preference returns when it's off."
                  />
                ) : null}
              </div>
              <select
                id="transcription-provider"
                value={airplaneMode ? 'local-whisper' : value.transcriptionProvider}
                onChange={(event) => onChange({transcriptionProvider: event.target.value as 'fal' | 'local-whisper' | 'replicate' | 'runware'})}
              >
                <option value="fal" disabled={airplaneMode}>fal.ai (cloud)</option>
                <option value="replicate" disabled={airplaneMode}>Replicate (cloud)</option>
                <option value="runware" disabled={airplaneMode}>Runware (cloud)</option>
                <option
                  value="local-whisper"
                  disabled={!catalogs.whisperStatus?.available && value.transcriptionProvider !== 'local-whisper'}
                >
                  Whisper (local)
                </option>
              </select>
            </div>

            {localTranscriptionSelected ? null : value.transcriptionProvider === 'runware' ? (
              <div className="field">
                <div className="field-label-row">
                  {fieldLabel('runware-transcribe-model', 'Transcription Model (Runware)', 'transcribeModel')}
                  <InfoHint
                    label="Runware transcription"
                    text="Runware serves speech-to-text through its Gemini text-inference models (audio inputs on a text task) — not a dedicated whisper engine. The transcript is plain text: no word/segment timestamps, so a timestamped request degrades with a notice and produces no cue track; translate folds into the prompt. Costs are the amounts Runware reports."
                  />
                </div>
                <ModelCombobox
                  id="runware-transcribe-model"
                  ariaLabel="Runware transcription model"
                  placeholder={defaultRunwareTranscribeModel}
                  value={value.runwareTranscribeModel}
                  onChange={(next) => onChange({runwareTranscribeModel: next})}
                  options={runwareTranscribeOptions}
                  allowCustom
                />
              </div>
            ) : value.transcriptionProvider === 'replicate' ? (
              <div className="field">
                {fieldLabel('replicate-transcribe-model', 'Transcription Model (Replicate)', 'transcribeModel')}
                <ModelCombobox
                  id="replicate-transcribe-model"
                  ariaLabel="Replicate transcription model"
                  placeholder={defaultReplicateTranscribeModel}
                  value={value.replicateTranscribeModel}
                  onChange={(next) => onChange({replicateTranscribeModel: next})}
                  options={replicateTranscribeOptions}
                  allowCustom
                />
              </div>
            ) : (
              <div className="field">
                {fieldLabel('fal-transcribe-model', 'Transcription Model (fal.ai)', 'transcribeModel')}
                <ModelCombobox
                  id="fal-transcribe-model"
                  ariaLabel="fal.ai transcription model"
                  placeholder={defaultFalTranscribeModel}
                  value={value.falTranscribeModel}
                  onChange={(next) => onChange({falTranscribeModel: next})}
                  options={falTranscribeOptions}
                  allowCustom
                />
              </div>
            )}
          </div>

          {localTranscriptionSelected ? (
            whisperFound ? (
              <span className="hint">The whisper binary, model, and VAD model are configured {toolWhere}.{openLocalToolsLink}</span>
            ) : (
              <span className="hint">{whisperDetail} Then re-detect or set its path {toolWhere}.{openLocalToolsLink}</span>
            )
          ) : replicateTranscriptionSelected ? (
            catalogs.replicateHasKey ? null : (
              <span className="hint">
                Add a Replicate API token {keyWhere} — or {whisperFound ? 'switch to Whisper (local)' : <>set up whisper {toolWhere}</>} — to transcribe audio.
                {whisperFound ? null : openLocalToolsLink}
              </span>
            )
          ) : value.transcriptionProvider === 'runware' && !catalogs.runwareHasKey ? (
            <span className="hint">
              Add a Runware API key {keyWhere} — or {whisperFound ? 'switch to Whisper (local)' : <>set up whisper {toolWhere}</>} — to transcribe audio.
              {whisperFound ? null : openLocalToolsLink}
            </span>
          ) : !catalogs.falHasKey ? (
            <span className="hint">
              Add a fal.ai API key {keyWhere} — or {whisperFound ? 'switch to Whisper (local)' : <>set up whisper {toolWhere}</>} — to transcribe audio.
              {whisperFound ? null : openLocalToolsLink}
            </span>
          ) : whisperFound ? null : (
            <span className="hint">{whisperDetail} Then re-detect or set its path {toolWhere} to enable the local option.{openLocalToolsLink}</span>
          )}
        </div>
      </section>

      <section className="settings-section">
        <h3>
          Lip Sync{' '}
          {airplaneMode ? (
            <AirplaneHint
              label="Lip sync unavailable"
              text="Lip sync runs on a cloud provider (fal.ai, Replicate, or Runware), which airplane mode blocks."
            />
          ) : null}
        </h3>
        <div className="settings-rows">
          <div className="two-column">
            <div className="field">
              {fieldLabel('lipsync-provider', 'Lip Sync Provider', 'lipsyncProvider')}
              <select
                id="lipsync-provider"
                value={value.lipsyncProvider}
                onChange={(event) => onChange({lipsyncProvider: event.target.value as 'fal' | 'replicate' | 'runware'})}
              >
                <option value="fal" disabled={airplaneMode}>fal.ai (cloud)</option>
                <option value="replicate" disabled={airplaneMode}>Replicate (cloud)</option>
                <option value="runware" disabled={airplaneMode}>Runware (cloud)</option>
              </select>
            </div>
          </div>
          {value.lipsyncProvider === 'runware' ? (
            <div className="two-column">
              <div className="field">
                <div className="field-label-row">
                  {fieldLabel('runware-lipsync-image-model', 'Image+Audio Input (Runware)', 'lipsyncImageModel')}
                  <InfoHint
                    label="Runware image-face lipsync"
                    text="Produces a talking head from an attached image plus a driving audio clip. Runware's image-face model is VEED Fabric 1.0 (60-second audio ceiling); its video-face model (KlingAI Lip-Sync) re-syncs an attached clip instead. Both are schema-verified for the inputs the tool sends."
                  />
                </div>
                <ModelCombobox
                  id="runware-lipsync-image-model"
                  ariaLabel="Runware image+audio lip sync model"
                  placeholder={defaultRunwareLipsyncImageModel}
                  value={value.runwareLipsyncImageModel}
                  onChange={(next) => onChange({runwareLipsyncImageModel: next})}
                  options={runwareLipsyncImageOptions}
                  allowCustom
                />
              </div>

              <div className="field">
                {fieldLabel('runware-lipsync-video-model', 'Video+Audio Input (Runware)', 'lipsyncVideoModel')}
                <ModelCombobox
                  id="runware-lipsync-video-model"
                  ariaLabel="Runware video+audio lip sync model"
                  placeholder={defaultRunwareLipsyncVideoModel}
                  value={value.runwareLipsyncVideoModel}
                  onChange={(next) => onChange({runwareLipsyncVideoModel: next})}
                  options={runwareLipsyncVideoOptions}
                  allowCustom
                />
              </div>
            </div>
          ) : value.lipsyncProvider === 'replicate' ? (
            <div className="two-column">
              <div className="field">
                {fieldLabel('replicate-lipsync-image-model', 'Image+Audio Input (Replicate)', 'lipsyncImageModel')}
                <ModelCombobox
                  id="replicate-lipsync-image-model"
                  ariaLabel="Replicate image+audio lip sync model"
                  placeholder={defaultReplicateLipsyncImageModel}
                  value={value.replicateLipsyncImageModel}
                  onChange={(next) => onChange({replicateLipsyncImageModel: next})}
                  options={replicateLipsyncImageOptions}
                  allowCustom
                />
              </div>

              <div className="field">
                {fieldLabel('replicate-lipsync-video-model', 'Video+Audio Input (Replicate)', 'lipsyncVideoModel')}
                <ModelCombobox
                  id="replicate-lipsync-video-model"
                  ariaLabel="Replicate video+audio lip sync model"
                  placeholder={defaultReplicateLipsyncVideoModel}
                  value={value.replicateLipsyncVideoModel}
                  onChange={(next) => onChange({replicateLipsyncVideoModel: next})}
                  options={replicateLipsyncVideoOptions}
                  allowCustom
                />
              </div>
            </div>
          ) : (
            <div className="two-column">
              <div className="field">
                {fieldLabel('fal-lipsync-image-model', 'Image+Audio Input (fal.ai)', 'lipsyncImageModel')}
                <ModelCombobox
                  id="fal-lipsync-image-model"
                  ariaLabel="fal.ai image+audio lip sync model"
                  placeholder={defaultFalLipsyncImageModel}
                  value={value.falLipsyncImageModel}
                  onChange={(next) => onChange({falLipsyncImageModel: next})}
                  options={falLipsyncImageOptions}
                  allowCustom
                />
              </div>

              <div className="field">
                {fieldLabel('fal-lipsync-video-model', 'Video+Audio Input (fal.ai)', 'lipsyncVideoModel')}
                <ModelCombobox
                  id="fal-lipsync-video-model"
                  ariaLabel="fal.ai video+audio lip sync model"
                  placeholder={defaultFalLipsyncVideoModel}
                  value={value.falLipsyncVideoModel}
                  onChange={(next) => onChange({falLipsyncVideoModel: next})}
                  options={falLipsyncVideoOptions}
                  allowCustom
                />
              </div>
            </div>
          )}
          {!airplaneMode && value.lipsyncProvider === 'replicate' && !catalogs.replicateHasKey ? (
            <span className="hint">Add a Replicate API token {keyWhere} to use lip sync.</span>
          ) : null}
          {!airplaneMode && value.lipsyncProvider === 'runware' && !catalogs.runwareHasKey ? (
            <span className="hint">Add a Runware API key {keyWhere} to use lip sync.</span>
          ) : null}
          {!airplaneMode && value.lipsyncProvider !== 'replicate' && value.lipsyncProvider !== 'runware' && !catalogs.falHasKey ? (
            <span className="hint">Add a fal.ai API key {keyWhere} to use lip sync.</span>
          ) : null}
        </div>
      </section>
    </>
  );
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

// openRouterCapabilityLabels maps the capability vocabulary the Go side
// derives from OpenRouter's catalog (input modalities plus native tool
// support, openrouter_client.go) onto panel chips. It must NOT reuse
// modelCapabilityLabels: that mapping reads "image" as image generation
// (Ollama's vocabulary), while OpenRouter's "image" is an input modality —
// the vision capability a chat turn's attached images ride on.
function openRouterCapabilityLabels(model: main.ModelInfo): string[] {
  const labels = new Set<string>();
  for (const capability of asArray(model.capabilities)) {
    const normalized = capability.toLowerCase().trim();
    if (normalized === 'image') {
      labels.add('Image input');
      continue;
    }
    if (normalized === 'audio') {
      labels.add('Audio input');
      continue;
    }
    if (normalized === 'video') {
      labels.add('Video input');
      continue;
    }
    labels.add(formatCapability(capability));
  }
  return Array.from(labels);
}

function formatContextLength(context: number): string {
  if (!Number.isFinite(context) || context <= 0) {
    return '';
  }
  if (context >= 1000000) {
    const millions = context / 1000000;
    return `${millions >= 10 ? Math.round(millions) : Math.round(millions * 10) / 10}M`;
  }
  if (context >= 1000) {
    return `${Math.round(context / 1000)}K`;
  }
  return String(context);
}

export type {ModelSelectionValue};
export {
  ModelCapabilityLink,
  formatCapability,
  chatModelOptionsFor,
  overrideKeysFromRecord,
  conversationOverridesPayload,
  ModelSelectionPanel,
  modelCapabilityLabels,
  openRouterCapabilityLabels,
  formatContextLength,
};
