import {useEffect, useMemo, useState} from 'react';
import {ChooseToolWorkspace, SaveConfig} from '../wailsjs/go/main/App';
import {main} from '../wailsjs/go/models';
import {notifications, notifyError} from './notifications';
import {appConfigPayload, configValues} from './appConfig';
import {ChatProviderID, SpeechProviderID} from './types';
import {
  defaultBaseURL,
  defaultOllamaNumCtx,
  defaultImageAspectRatio,
  defaultImageSizePreset,
  defaultImageSteps,
  imageSizePresetOptions,
  defaultFalImageModel,
  defaultFalImageEditModel,
  defaultFalInpaintModel,
  defaultFalVideoModel,
  defaultFalVideoImageModel,
  defaultFalVideoKeyframeModel,
  defaultFalVideoExtendModel,
  defaultFalVideoMotionModel,
  defaultFalVideoUpscaleModel,
  defaultFalVideoReframeModel,
  defaultFalVideoRestyleModel,
  defaultFalAudioModel,
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
  defaultRunwareVideoModel, defaultRunwareVideoImageModel, defaultRunwareVideoExtendModel, defaultRunwareVideoUpscaleModel, defaultRunwareVideoRestyleModel, defaultRunwareInpaintModel, defaultRunwareLipsyncImageModel, defaultRunwareLipsyncVideoModel, defaultRunwareSpeechModel, defaultRunwareTranscribeModel, defaultRunwareSoundEffectsModel, defaultRunwareSoundEffectsVideoModel,
  defaultReplicateImageEditModel,
  defaultReplicateVideoModel,
  defaultReplicateVideoImageModel,
  defaultReplicateUpscaleModel,
  defaultReplicateInpaintModel,
  defaultReplicateVideoUpscaleModel,
  defaultReplicateVideoRestyleModel,
  defaultReplicateVideoReframeModel,
  defaultReplicateVideoExtendModel,
  defaultReplicateSoundEffectsModel,
  defaultReplicateSoundEffectsVideoModel,
  defaultReplicateSpeechModel,
  defaultReplicateAudioExtendModel,
  defaultVideoDuration,
  defaultVideoAspectRatio,
  defaultVideoResolution,
  defaultSoundDuration,
  defaultAudioExtendDuration,
  defaultMediaTimeoutSeconds,
} from './modelDefaults';
import {ModelSelectionValue} from './settings/ModelSelectionPanel';

// useAppConfig owns the persisted Settings: every field the config file
// stores, applyConfig to hydrate them from GetConfig, and the debounced effect
// that writes them back through SaveConfig once a config has loaded (appConfig.ts
// maps both directions). The key flags are parameters: they come from the
// keychain and ride along only as each provider's enabled bit. loadConfig's
// startup orchestration stays in App.
function useAppConfig({openRouterHasKey, falHasKey, replicateHasKey, runwareHasKey}: {
  openRouterHasKey: boolean;
  falHasKey: boolean;
  replicateHasKey: boolean;
  runwareHasKey: boolean;
}) {
  const [baseURL, setBaseURL] = useState(defaultBaseURL);
  const [ollamaNumCtx, setOllamaNumCtx] = useState(defaultOllamaNumCtx);

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
  // Airplane mode: the offline guarantee (airplane_mode.go). While on, the
  // backend refuses every cloud call, so the UI restricts the provider
  // pickers to the local ones and hides the cloud-only surfaces rather than
  // letting turns fail mysteriously.
  const [airplaneMode, setAirplaneMode] = useState(false);
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

  // Ollama is no longer offered as an image provider in the UI (its runtime
  // dropped image-generation support); a config still saying "ollama" loads as
  // the local OpenAI-compatible server. The Go backend keeps accepting the id.
  const [imageProvider, setImageProvider] = useState<'fal' | 'replicate' | 'runware' | 'openai-compatible'>('openai-compatible');
  // The generate_video backend: fal.ai (the historical default) or Replicate.
  // A config written before the second backend existed says "fal" via the Go
  // merge, so the state mirrors that default.
  const [videoProvider, setVideoProvider] = useState<'fal' | 'replicate' | 'runware'>('fal');
  // The image editor's mask-inpainting backend — independent of ImageProvider
  // on purpose (the plan's Inpainting section): a user's ordinary image
  // generator and their verified mask-capable editor model need not share a
  // cloud. Defaults mirror the Go merge (fal).
  const [inpaintProvider, setInpaintProvider] = useState<'fal' | 'replicate' | 'runware'>('fal');
  const [lipsyncProvider, setLipsyncProvider] = useState<'fal' | 'replicate' | 'runware'>('fal');
  // generate_speech's backend — fal.ai or the local Ollama speech models
  // (phonon and friends). Scoped to the voice models alone: extension, voice
  // design, and cloning stay fal-only, and music/sound effects route by the
  // audio provider. Defaults mirror the Go merge (fal); the Ollama slot is
  // its own field so switching providers never loses the other side's model.
  const [speechProvider, setSpeechProvider] = useState<SpeechProviderID>('fal');
  const [ollamaSpeechModel, setOllamaSpeechModel] = useState('');
  // The Replicate speech slot — its own field for the same reason as the
  // Ollama one: a provider switch must never lose the other sides' models.
  const [replicateSpeechModel, setReplicateSpeechModel] = useState(defaultReplicateSpeechModel);
  // The NON-voice audio backend — generate_sound and add_sound_effects route
  // by it; speech routes by SpeechProvider, and extend/voice-design/cloning
  // stay fal-only. Defaults mirror the Go merge (fal); the per-provider
  // model slots are their own fields so switching providers never loses the
  // other side's model.
  const [audioProvider, setAudioProvider] = useState<'fal' | 'replicate' | 'runware'>('fal');
  const [replicateAudioExtendModel, setReplicateAudioExtendModel] = useState(defaultReplicateAudioExtendModel);
  const [falInpaintModel, setFalInpaintModel] = useState(defaultFalInpaintModel);
  const [replicateInpaintModel, setReplicateInpaintModel] = useState(defaultReplicateInpaintModel);
  const [openaiCompatibleBaseURL, setOpenaiCompatibleBaseURL] = useState('http://localhost:8080');
  const [openaiCompatibleModel, setOpenaiCompatibleModel] = useState('');
  const [falModel, setFalModel] = useState(defaultFalImageModel);
  const [falImageEditModel, setFalImageEditModel] = useState(defaultFalImageEditModel);
  const [falVideoModel, setFalVideoModel] = useState(defaultFalVideoModel);
  const [falVideoImageModel, setFalVideoImageModel] = useState(defaultFalVideoImageModel);
  const [falVideoKeyframeModel, setFalVideoKeyframeModel] = useState(defaultFalVideoKeyframeModel);
  const [falVideoExtendModel, setFalVideoExtendModel] = useState(defaultFalVideoExtendModel);
  const [falVideoMotionModel, setFalVideoMotionModel] = useState(defaultFalVideoMotionModel);
  const [falVideoUpscaleModel, setFalVideoUpscaleModel] = useState(defaultFalVideoUpscaleModel);
  const [falVideoReframeModel, setFalVideoReframeModel] = useState(defaultFalVideoReframeModel);
  const [falVideoRestyleModel, setFalVideoRestyleModel] = useState(defaultFalVideoRestyleModel);
  const [falAudioModel, setFalAudioModel] = useState(defaultFalAudioModel);
  const [falAudioCloneModel, setFalAudioCloneModel] = useState(defaultFalAudioCloneModel);
  const [falSoundEffectsModel, setFalSoundEffectsModel] = useState(defaultFalSoundEffectsModel);
  const [falSoundEffectsVideoModel, setFalSoundEffectsVideoModel] = useState(defaultFalSoundEffectsVideoModel);
  const [falVoiceDesignModel, setFalVoiceDesignModel] = useState(defaultFalVoiceDesignModel);
  const [falAudioExtendModel, setFalAudioExtendModel] = useState(defaultFalAudioExtendModel);
  const [falTranscribeModel, setFalTranscribeModel] = useState(defaultFalTranscribeModel);
  // Transcription provider selection ("fal" cloud, "replicate" cloud, or the
  // locally installed whisper CLI) plus the local-whisper knobs.
  // localToolsReport is the Go side's detection snapshot (DetectLocalTools):
  // which binaries exist and which provider the config resolves to — the
  // dropdown's local option only makes sense when a whisper was actually
  // found. The provider is a Models-tab selection (per-conversation
  // overridable); the model, binary, and VAD model are engine config edited
  // under Local Tools, global like ffmpeg's.
  const [transcriptionProvider, setTranscriptionProvider] = useState<'fal' | 'local-whisper' | 'replicate' | 'runware'>('fal');
  const [whisperModel, setWhisperModel] = useState('');
  const [whisperBinary, setWhisperBinary] = useState('');
  const [whisperVADModel, setWhisperVADModel] = useState('');
  const [whisperChunkMinutes, setWhisperChunkMinutes] = useState('');
  const [ffmpegBinary, setFfmpegBinary] = useState('');
  const [ffprobeBinary, setFfprobeBinary] = useState('');
  const [sipsBinary, setSipsBinary] = useState('');
  const [magickBinary, setMagickBinary] = useState('');

  const [falLipsyncImageModel, setFalLipsyncImageModel] = useState(defaultFalLipsyncImageModel);
  const [falLipsyncVideoModel, setFalLipsyncVideoModel] = useState(defaultFalLipsyncVideoModel);
  const [falUpscaleModel, setFalUpscaleModel] = useState(defaultFalUpscaleModel);
  const [replicateModel, setReplicateModel] = useState(defaultReplicateImageModel);
  const [runwareModel, setRunwareModel] = useState(defaultRunwareImageModel);
  const [runwareUpscaleModel, setRunwareUpscaleModel] = useState(defaultRunwareUpscaleModel);
  const [runwareVideoModel, setRunwareVideoModel] = useState(defaultRunwareVideoModel);
  const [runwareVideoImageModel, setRunwareVideoImageModel] = useState(defaultRunwareVideoImageModel);
  const [runwareVideoExtendModel, setRunwareVideoExtendModel] = useState(defaultRunwareVideoExtendModel);
  const [runwareVideoUpscaleModel, setRunwareVideoUpscaleModel] = useState(defaultRunwareVideoUpscaleModel);
  const [runwareVideoRestyleModel, setRunwareVideoRestyleModel] = useState(defaultRunwareVideoRestyleModel);
  const [runwareInpaintModel, setRunwareInpaintModel] = useState(defaultRunwareInpaintModel);
  const [runwareLipsyncImageModel, setRunwareLipsyncImageModel] = useState(defaultRunwareLipsyncImageModel);
  const [runwareLipsyncVideoModel, setRunwareLipsyncVideoModel] = useState(defaultRunwareLipsyncVideoModel);
  const [runwareSpeechModel, setRunwareSpeechModel] = useState(defaultRunwareSpeechModel);
  const [runwareTranscribeModel, setRunwareTranscribeModel] = useState(defaultRunwareTranscribeModel);
  const [runwareSoundEffectsModel, setRunwareSoundEffectsModel] = useState(defaultRunwareSoundEffectsModel);
  const [runwareSoundEffectsVideoModel, setRunwareSoundEffectsVideoModel] = useState(defaultRunwareSoundEffectsVideoModel);
  const [replicateImageEditModel, setReplicateImageEditModel] = useState(defaultReplicateImageEditModel);
  const [replicateUpscaleModel, setReplicateUpscaleModel] = useState(defaultReplicateUpscaleModel);
  const [replicateVideoUpscaleModel, setReplicateVideoUpscaleModel] = useState(defaultReplicateVideoUpscaleModel);
  const [replicateVideoRestyleModel, setReplicateVideoRestyleModel] = useState(defaultReplicateVideoRestyleModel);
  const [replicateVideoReframeModel, setReplicateVideoReframeModel] = useState(defaultReplicateVideoReframeModel);
  const [replicateVideoExtendModel, setReplicateVideoExtendModel] = useState(defaultReplicateVideoExtendModel);
  const [replicateVideoModel, setReplicateVideoModel] = useState(defaultReplicateVideoModel);
  const [replicateVideoImageModel, setReplicateVideoImageModel] = useState(defaultReplicateVideoImageModel);
  const [replicateTranscribeModel, setReplicateTranscribeModel] = useState(defaultReplicateTranscribeModel);
  const [replicateLipsyncImageModel, setReplicateLipsyncImageModel] = useState(defaultReplicateLipsyncImageModel);
  const [replicateLipsyncVideoModel, setReplicateLipsyncVideoModel] = useState(defaultReplicateLipsyncVideoModel);
  const [replicateSoundEffectsModel, setReplicateSoundEffectsModel] = useState(defaultReplicateSoundEffectsModel);
  const [replicateSoundEffectsVideoModel, setReplicateSoundEffectsVideoModel] = useState(defaultReplicateSoundEffectsVideoModel);
  // Each video picker owns its duration options + value, driven by its model's
  // published schema (ListFalVideoDurations). videoDuration (text-to-video) is
  // the canonical value persisted to config.generation.video.duration — the
  // backend reads one shared duration for all three video paths
  // (tools_registry.go), and resolveVideoBody drops values invalid for the
  // actually-selected model with a notice, so the image/extend pickers are a
  // per-model discovery/preview aid that stays correct where the value is valid.
  const [videoDuration, setVideoDuration] = useState(defaultVideoDuration);

  const [videoAspectRatio, setVideoAspectRatio] = useState(defaultVideoAspectRatio);
  const [videoResolution, setVideoResolution] = useState(defaultVideoResolution);
  // soundDuration is the generate_sound default clip length, persisted to
  // config.generation.audio.duration and fetched per-model like videoDuration.
  const [soundDuration, setSoundDuration] = useState(defaultSoundDuration);

  // audioExtendDuration is extend_audio's default ADDED length, persisted to
  // config.generation.audio.extendDuration; its option ladder is static.
  const [audioExtendDuration, setAudioExtendDuration] = useState(defaultAudioExtendDuration);
  // mediaTimeoutMinutes is the per-call bound for queued media generations
  // (fal / Replicate), persisted to config.generation.mediaTimeoutSeconds —
  // the UI edits minutes, the config stores seconds.
  const [mediaTimeoutMinutes, setMediaTimeoutMinutes] = useState(Math.round(defaultMediaTimeoutSeconds / 60));
  const [system, setSystem] = useState('You are Atelier, a precise local AI collaborator.');

  const [imageAspectRatio, setImageAspectRatio] = useState(defaultImageAspectRatio);
  const [imageSizePreset, setImageSizePreset] = useState(defaultImageSizePreset);
  const [imageSteps, setImageSteps] = useState(defaultImageSteps);
  const [configLoaded, setConfigLoaded] = useState(false);
  const [storageConfig, setStorageConfig] = useState<main.ConfigStorage | null>(null);
  const [toolConfig, setToolConfig] = useState<main.ConfigTools | null>(null);

  const [updatesConfig, setUpdatesConfig] = useState<main.ConfigUpdates | null>(null);

  useEffect(() => {
    if (!configLoaded) {
      return;
    }
    const timeout = window.setTimeout(() => {
      SaveConfig(main.AppConfig.createFrom(appConfigPayload({
        storageConfig, toolConfig, updatesConfig, baseURL, ollamaNumCtx, airplaneMode, primaryModels,
        primaryProvider, harnessModels, harnessProvider, imageModel, system, imageAspectRatio,
        imageSizePreset, imageSteps, imageProvider, videoProvider, inpaintProvider, falInpaintModel,
        replicateInpaintModel, soundDuration, audioExtendDuration, mediaTimeoutMinutes,
        runwareVideoModel, runwareVideoImageModel, runwareVideoExtendModel, runwareVideoUpscaleModel,
        runwareVideoRestyleModel, runwareInpaintModel, runwareLipsyncImageModel, runwareLipsyncVideoModel,
        runwareSpeechModel, runwareTranscribeModel, runwareSoundEffectsModel, runwareSoundEffectsVideoModel,
        openaiCompatibleBaseURL, openaiCompatibleModel, replicateModel, runwareModel, runwareUpscaleModel, replicateImageEditModel,
        replicateUpscaleModel, replicateVideoUpscaleModel, replicateVideoRestyleModel,
        replicateVideoReframeModel, replicateVideoExtendModel, replicateVideoModel, replicateVideoImageModel,
        falModel, falImageEditModel, falVideoModel, falVideoImageModel, falVideoKeyframeModel,
        falVideoExtendModel, falVideoMotionModel, falVideoUpscaleModel, falVideoReframeModel,
        falVideoRestyleModel, falAudioModel, falAudioCloneModel, falSoundEffectsModel,
        falSoundEffectsVideoModel, falVoiceDesignModel, falAudioExtendModel, falTranscribeModel,
        replicateTranscribeModel, transcriptionProvider, whisperModel, whisperBinary, whisperVADModel,
        whisperChunkMinutes, ffmpegBinary, ffprobeBinary, sipsBinary, magickBinary, lipsyncProvider,
        speechProvider, ollamaSpeechModel, replicateSpeechModel, replicateLipsyncImageModel,
        replicateLipsyncVideoModel, audioProvider, replicateSoundEffectsModel,
        replicateSoundEffectsVideoModel, replicateAudioExtendModel, falLipsyncImageModel,
        falLipsyncVideoModel, falUpscaleModel, videoDuration, videoAspectRatio, videoResolution,
        openRouterHasKey, falHasKey, replicateHasKey, runwareHasKey,
      }))).then(() => notifications.dismissKey('config-save')).catch((error) => {
        notifyError("Couldn't save settings", error, {key: 'config-save'});
      });
    }, 400);
    return () => window.clearTimeout(timeout);
  }, [airplaneMode, baseURL, configLoaded, falHasKey, falModel, falImageEditModel, falVideoModel, falVideoImageModel, falVideoKeyframeModel, falVideoExtendModel, falVideoMotionModel, falVideoUpscaleModel, falVideoReframeModel, falVideoRestyleModel, falAudioModel, falAudioCloneModel, falSoundEffectsModel, falSoundEffectsVideoModel, falVoiceDesignModel, falAudioExtendModel, falTranscribeModel, falUpscaleModel, falInpaintModel, replicateInpaintModel, inpaintProvider, falLipsyncImageModel, falLipsyncVideoModel, replicateHasKey, runwareHasKey, replicateModel, runwareModel, runwareUpscaleModel, runwareVideoModel, runwareVideoImageModel, runwareVideoExtendModel, runwareVideoUpscaleModel, runwareVideoRestyleModel, runwareInpaintModel, runwareLipsyncImageModel, runwareLipsyncVideoModel, runwareSpeechModel, runwareTranscribeModel, runwareSoundEffectsModel, runwareSoundEffectsVideoModel, replicateImageEditModel, replicateUpscaleModel, replicateVideoUpscaleModel, replicateVideoRestyleModel, replicateVideoReframeModel, replicateVideoExtendModel, replicateVideoModel, replicateVideoImageModel, replicateTranscribeModel, replicateLipsyncImageModel, replicateLipsyncVideoModel, lipsyncProvider, speechProvider, ollamaSpeechModel, replicateSpeechModel, audioProvider, replicateSoundEffectsModel, replicateSoundEffectsVideoModel, replicateAudioExtendModel, videoProvider, ffmpegBinary, ffprobeBinary, harnessModels, harnessProvider, imageAspectRatio, imageModel, imageProvider, imageSizePreset, imageSteps, magickBinary, ollamaNumCtx, openaiCompatibleBaseURL, openaiCompatibleModel, openRouterHasKey, primaryModels, primaryProvider, sipsBinary, soundDuration, audioExtendDuration, storageConfig, system, toolConfig, transcriptionProvider, updatesConfig, videoAspectRatio, videoDuration, videoResolution, whisperBinary, whisperModel, whisperVADModel, whisperChunkMinutes]);

  const globalModelSelection = useMemo<ModelSelectionValue>(() => ({
    primaryProvider,
    primaryModel: primaryModels[primaryProvider],
    harnessProvider,
    harnessModel: harnessModels[harnessProvider],
    imageProvider,
    videoProvider,
    inpaintProvider,
    lipsyncProvider,
    speechProvider,
    audioProvider,
    ollamaSpeechModel,
    replicateSpeechModel,
    replicateSoundEffectsModel,
    replicateSoundEffectsVideoModel,
    replicateAudioExtendModel,
    falModel,
    openaiImageModel: openaiCompatibleModel,
    replicateModel,
    runwareModel,
    runwareUpscaleModel,
    runwareVideoModel,
    runwareVideoImageModel,
    runwareVideoExtendModel,
    runwareVideoUpscaleModel,
    runwareVideoRestyleModel,
    runwareInpaintModel,
    runwareLipsyncImageModel,
    runwareLipsyncVideoModel,
    runwareSpeechModel,
    runwareTranscribeModel,
    runwareSoundEffectsModel,
    runwareSoundEffectsVideoModel,
    replicateImageEditModel,
    replicateUpscaleModel,
    falImageEditModel,
    falUpscaleModel,
    falInpaintModel,
    replicateInpaintModel,
    replicateVideoUpscaleModel,
    replicateVideoRestyleModel,
    replicateVideoReframeModel,
    replicateVideoExtendModel,
    falVideoModel,
    falVideoImageModel,
    replicateVideoModel,
    replicateVideoImageModel,
    falVideoKeyframeModel,
    falVideoExtendModel,
    falVideoMotionModel,
    falVideoUpscaleModel,
    falVideoReframeModel,
    falVideoRestyleModel,
    falAudioModel,
    falAudioCloneModel,
    falSoundEffectsModel,
    falSoundEffectsVideoModel,
    falVoiceDesignModel,
    falAudioExtendModel,
    transcriptionProvider,
    falTranscribeModel,
    replicateTranscribeModel,
    falLipsyncImageModel,
    falLipsyncVideoModel,
    replicateLipsyncImageModel,
    replicateLipsyncVideoModel,
    imageAspectRatio,
    imageSizePreset,
    imageSteps,
    videoDuration,
    videoAspectRatio,
    videoResolution,
    soundDuration,
    audioExtendDuration,
  }), [primaryProvider, primaryModels, harnessProvider, harnessModels, imageProvider, videoProvider, inpaintProvider, falModel, openaiCompatibleModel, replicateModel, runwareModel, runwareUpscaleModel, runwareVideoModel, runwareVideoImageModel, runwareVideoExtendModel, runwareVideoUpscaleModel, runwareVideoRestyleModel, runwareInpaintModel, runwareLipsyncImageModel, runwareLipsyncVideoModel, runwareSpeechModel, runwareTranscribeModel, runwareSoundEffectsModel, runwareSoundEffectsVideoModel, replicateImageEditModel, replicateUpscaleModel, falImageEditModel, falUpscaleModel, falInpaintModel, replicateInpaintModel, replicateVideoUpscaleModel, replicateVideoRestyleModel, replicateVideoReframeModel, replicateVideoExtendModel, falVideoModel, falVideoImageModel, replicateVideoModel, replicateVideoImageModel, falVideoKeyframeModel, falVideoExtendModel, falVideoMotionModel, falVideoUpscaleModel, falVideoReframeModel, falVideoRestyleModel, falAudioModel, falAudioCloneModel, falSoundEffectsModel, falSoundEffectsVideoModel, falVoiceDesignModel, falAudioExtendModel, transcriptionProvider, audioProvider, replicateSoundEffectsModel, replicateSoundEffectsVideoModel, replicateAudioExtendModel, falTranscribeModel, replicateTranscribeModel, falLipsyncImageModel, falLipsyncVideoModel, replicateLipsyncImageModel, replicateLipsyncVideoModel, lipsyncProvider, speechProvider, ollamaSpeechModel, replicateSpeechModel, imageAspectRatio, imageSizePreset, imageSteps, videoDuration, videoAspectRatio, videoResolution, soundDuration, audioExtendDuration]);

  const patchSettingsModelSelection = (patch: Partial<ModelSelectionValue>) => {
    if (patch.primaryProvider !== undefined) setPrimaryProvider(patch.primaryProvider);
    if (patch.primaryModel !== undefined) setModel(patch.primaryModel);
    if (patch.harnessProvider !== undefined) setHarnessProvider(patch.harnessProvider);
    if (patch.harnessModel !== undefined) setHarnessModel(patch.harnessModel);
    if (patch.imageProvider !== undefined) setImageProvider(patch.imageProvider);
    if (patch.videoProvider !== undefined) setVideoProvider(patch.videoProvider);
    if (patch.inpaintProvider !== undefined) setInpaintProvider(patch.inpaintProvider);
    if (patch.lipsyncProvider !== undefined) setLipsyncProvider(patch.lipsyncProvider);
    if (patch.replicateLipsyncImageModel !== undefined) setReplicateLipsyncImageModel(patch.replicateLipsyncImageModel);
    if (patch.replicateLipsyncVideoModel !== undefined) setReplicateLipsyncVideoModel(patch.replicateLipsyncVideoModel);
    if (patch.falInpaintModel !== undefined) setFalInpaintModel(patch.falInpaintModel);
    if (patch.replicateInpaintModel !== undefined) setReplicateInpaintModel(patch.replicateInpaintModel);
    if (patch.falModel !== undefined) setFalModel(patch.falModel);
    if (patch.openaiImageModel !== undefined) setOpenaiCompatibleModel(patch.openaiImageModel);
    if (patch.replicateModel !== undefined) setReplicateModel(patch.replicateModel);
    if (patch.runwareModel !== undefined) setRunwareModel(patch.runwareModel);
    if (patch.runwareUpscaleModel !== undefined) setRunwareUpscaleModel(patch.runwareUpscaleModel);
    if (patch.runwareVideoModel !== undefined) setRunwareVideoModel(patch.runwareVideoModel);
    if (patch.runwareVideoImageModel !== undefined) setRunwareVideoImageModel(patch.runwareVideoImageModel);
    if (patch.runwareVideoExtendModel !== undefined) setRunwareVideoExtendModel(patch.runwareVideoExtendModel);
    if (patch.runwareVideoUpscaleModel !== undefined) setRunwareVideoUpscaleModel(patch.runwareVideoUpscaleModel);
    if (patch.runwareVideoRestyleModel !== undefined) setRunwareVideoRestyleModel(patch.runwareVideoRestyleModel);
    if (patch.runwareInpaintModel !== undefined) setRunwareInpaintModel(patch.runwareInpaintModel);
    if (patch.runwareLipsyncImageModel !== undefined) setRunwareLipsyncImageModel(patch.runwareLipsyncImageModel);
    if (patch.runwareLipsyncVideoModel !== undefined) setRunwareLipsyncVideoModel(patch.runwareLipsyncVideoModel);
    if (patch.runwareSpeechModel !== undefined) setRunwareSpeechModel(patch.runwareSpeechModel);
    if (patch.runwareTranscribeModel !== undefined) setRunwareTranscribeModel(patch.runwareTranscribeModel);
    if (patch.runwareSoundEffectsModel !== undefined) setRunwareSoundEffectsModel(patch.runwareSoundEffectsModel);
    if (patch.runwareSoundEffectsVideoModel !== undefined) setRunwareSoundEffectsVideoModel(patch.runwareSoundEffectsVideoModel);
    if (patch.replicateImageEditModel !== undefined) setReplicateImageEditModel(patch.replicateImageEditModel);
    if (patch.replicateUpscaleModel !== undefined) setReplicateUpscaleModel(patch.replicateUpscaleModel);
    if (patch.replicateVideoUpscaleModel !== undefined) setReplicateVideoUpscaleModel(patch.replicateVideoUpscaleModel);
    if (patch.replicateVideoRestyleModel !== undefined) setReplicateVideoRestyleModel(patch.replicateVideoRestyleModel);
    if (patch.replicateVideoReframeModel !== undefined) setReplicateVideoReframeModel(patch.replicateVideoReframeModel);
    if (patch.replicateVideoExtendModel !== undefined) setReplicateVideoExtendModel(patch.replicateVideoExtendModel);
    if (patch.falImageEditModel !== undefined) setFalImageEditModel(patch.falImageEditModel);
    if (patch.falUpscaleModel !== undefined) setFalUpscaleModel(patch.falUpscaleModel);
    if (patch.falVideoModel !== undefined) setFalVideoModel(patch.falVideoModel);
    if (patch.falVideoImageModel !== undefined) setFalVideoImageModel(patch.falVideoImageModel);
    if (patch.replicateVideoModel !== undefined) setReplicateVideoModel(patch.replicateVideoModel);
    if (patch.replicateVideoImageModel !== undefined) setReplicateVideoImageModel(patch.replicateVideoImageModel);
    if (patch.falVideoKeyframeModel !== undefined) setFalVideoKeyframeModel(patch.falVideoKeyframeModel);
    if (patch.falVideoExtendModel !== undefined) setFalVideoExtendModel(patch.falVideoExtendModel);
    if (patch.falVideoMotionModel !== undefined) setFalVideoMotionModel(patch.falVideoMotionModel);
    if (patch.falVideoUpscaleModel !== undefined) setFalVideoUpscaleModel(patch.falVideoUpscaleModel);
    if (patch.falVideoReframeModel !== undefined) setFalVideoReframeModel(patch.falVideoReframeModel);
    if (patch.falVideoRestyleModel !== undefined) setFalVideoRestyleModel(patch.falVideoRestyleModel);
    if (patch.falAudioModel !== undefined) setFalAudioModel(patch.falAudioModel);
    if (patch.speechProvider !== undefined) setSpeechProvider(patch.speechProvider);
    if (patch.ollamaSpeechModel !== undefined) setOllamaSpeechModel(patch.ollamaSpeechModel);
    if (patch.replicateSpeechModel !== undefined) setReplicateSpeechModel(patch.replicateSpeechModel);
    if (patch.audioProvider !== undefined) setAudioProvider(patch.audioProvider);
    if (patch.replicateSoundEffectsModel !== undefined) setReplicateSoundEffectsModel(patch.replicateSoundEffectsModel);
    if (patch.replicateSoundEffectsVideoModel !== undefined) setReplicateSoundEffectsVideoModel(patch.replicateSoundEffectsVideoModel);
    if (patch.replicateAudioExtendModel !== undefined) setReplicateAudioExtendModel(patch.replicateAudioExtendModel);
    if (patch.falAudioCloneModel !== undefined) setFalAudioCloneModel(patch.falAudioCloneModel);
    if (patch.falSoundEffectsModel !== undefined) setFalSoundEffectsModel(patch.falSoundEffectsModel);
    if (patch.falSoundEffectsVideoModel !== undefined) setFalSoundEffectsVideoModel(patch.falSoundEffectsVideoModel);
    if (patch.falVoiceDesignModel !== undefined) setFalVoiceDesignModel(patch.falVoiceDesignModel);
    if (patch.falAudioExtendModel !== undefined) setFalAudioExtendModel(patch.falAudioExtendModel);
    if (patch.transcriptionProvider !== undefined) setTranscriptionProvider(patch.transcriptionProvider);
    if (patch.falTranscribeModel !== undefined) setFalTranscribeModel(patch.falTranscribeModel);
    if (patch.replicateTranscribeModel !== undefined) setReplicateTranscribeModel(patch.replicateTranscribeModel);
    if (patch.falLipsyncImageModel !== undefined) setFalLipsyncImageModel(patch.falLipsyncImageModel);
    if (patch.falLipsyncVideoModel !== undefined) setFalLipsyncVideoModel(patch.falLipsyncVideoModel);
    if (patch.lipsyncProvider !== undefined) setLipsyncProvider(patch.lipsyncProvider);
    if (patch.replicateLipsyncImageModel !== undefined) setReplicateLipsyncImageModel(patch.replicateLipsyncImageModel);
    if (patch.replicateLipsyncVideoModel !== undefined) setReplicateLipsyncVideoModel(patch.replicateLipsyncVideoModel);
    if (patch.imageAspectRatio !== undefined) setImageAspectRatio(patch.imageAspectRatio);
    if (patch.imageSizePreset !== undefined) setImageSizePreset(patch.imageSizePreset);
    if (patch.imageSteps !== undefined) setImageSteps(patch.imageSteps);
    if (patch.videoDuration !== undefined) setVideoDuration(patch.videoDuration);
    if (patch.videoAspectRatio !== undefined) setVideoAspectRatio(patch.videoAspectRatio);
    if (patch.videoResolution !== undefined) setVideoResolution(patch.videoResolution);
    if (patch.soundDuration !== undefined) setSoundDuration(patch.soundDuration);
    if (patch.audioExtendDuration !== undefined) setAudioExtendDuration(patch.audioExtendDuration);
  };

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

  // applyConfig hydrates every Settings field from a loaded config and marks
  // the config loaded, which arms the save effect.
  function applyConfig(config: main.AppConfig) {
    const next = configValues(config);
    setStorageConfig(next.storageConfig);
    setToolConfig(next.toolConfig);
    setUpdatesConfig(next.updatesConfig);
    setBaseURL(next.baseURL);
    setOllamaNumCtx(next.ollamaNumCtx);
    setAirplaneMode(next.airplaneMode);
    setPrimaryModels(next.primaryModels);
    setPrimaryProvider(next.primaryProvider);
    setHarnessModels(next.harnessModels);
    setHarnessProvider(next.harnessProvider);
    setImageModel(next.imageModel);
    setSystem(next.system);
    setImageAspectRatio(next.imageAspectRatio);
    setImageSizePreset(next.imageSizePreset);
    setImageSteps(next.imageSteps);
    setImageProvider(next.imageProvider);
    setVideoProvider(next.videoProvider);
    setInpaintProvider(next.inpaintProvider);
    setFalInpaintModel(next.falInpaintModel);
    setReplicateInpaintModel(next.replicateInpaintModel);
    setSoundDuration(next.soundDuration);
    setAudioExtendDuration(next.audioExtendDuration);
    setMediaTimeoutMinutes(next.mediaTimeoutMinutes);
    setOpenaiCompatibleBaseURL(next.openaiCompatibleBaseURL);
    setOpenaiCompatibleModel(next.openaiCompatibleModel);
    setReplicateModel(next.replicateModel);
    setRunwareModel(next.runwareModel);
    setRunwareUpscaleModel(next.runwareUpscaleModel);
    setRunwareVideoModel(next.runwareVideoModel);
    setRunwareVideoImageModel(next.runwareVideoImageModel);
    setRunwareVideoExtendModel(next.runwareVideoExtendModel);
    setRunwareVideoUpscaleModel(next.runwareVideoUpscaleModel);
    setRunwareVideoRestyleModel(next.runwareVideoRestyleModel);
    setRunwareInpaintModel(next.runwareInpaintModel);
    setRunwareLipsyncImageModel(next.runwareLipsyncImageModel);
    setRunwareLipsyncVideoModel(next.runwareLipsyncVideoModel);
    setRunwareSpeechModel(next.runwareSpeechModel);
    setRunwareTranscribeModel(next.runwareTranscribeModel);
    setRunwareSoundEffectsModel(next.runwareSoundEffectsModel);
    setRunwareSoundEffectsVideoModel(next.runwareSoundEffectsVideoModel);
    setReplicateImageEditModel(next.replicateImageEditModel);
    setReplicateUpscaleModel(next.replicateUpscaleModel);
    setReplicateVideoUpscaleModel(next.replicateVideoUpscaleModel);
    setReplicateVideoRestyleModel(next.replicateVideoRestyleModel);
    setReplicateVideoReframeModel(next.replicateVideoReframeModel);
    setReplicateVideoExtendModel(next.replicateVideoExtendModel);
    setReplicateVideoModel(next.replicateVideoModel);
    setReplicateVideoImageModel(next.replicateVideoImageModel);
    setFalModel(next.falModel);
    setFalImageEditModel(next.falImageEditModel);
    setFalVideoModel(next.falVideoModel);
    setFalVideoImageModel(next.falVideoImageModel);
    setFalVideoKeyframeModel(next.falVideoKeyframeModel);
    setFalVideoExtendModel(next.falVideoExtendModel);
    setFalVideoMotionModel(next.falVideoMotionModel);
    setFalVideoUpscaleModel(next.falVideoUpscaleModel);
    setFalVideoReframeModel(next.falVideoReframeModel);
    setFalVideoRestyleModel(next.falVideoRestyleModel);
    setFalAudioModel(next.falAudioModel);
    setFalAudioCloneModel(next.falAudioCloneModel);
    setFalSoundEffectsModel(next.falSoundEffectsModel);
    setFalSoundEffectsVideoModel(next.falSoundEffectsVideoModel);
    setFalVoiceDesignModel(next.falVoiceDesignModel);
    setFalAudioExtendModel(next.falAudioExtendModel);
    setFalTranscribeModel(next.falTranscribeModel);
    setReplicateTranscribeModel(next.replicateTranscribeModel);
    setTranscriptionProvider(next.transcriptionProvider);
    setWhisperModel(next.whisperModel);
    setWhisperBinary(next.whisperBinary);
    setWhisperVADModel(next.whisperVADModel);
    setWhisperChunkMinutes(next.whisperChunkMinutes);
    setFfmpegBinary(next.ffmpegBinary);
    setFfprobeBinary(next.ffprobeBinary);
    setSipsBinary(next.sipsBinary);
    setMagickBinary(next.magickBinary);
    setLipsyncProvider(next.lipsyncProvider);
    setSpeechProvider(next.speechProvider);
    setOllamaSpeechModel(next.ollamaSpeechModel);
    setReplicateSpeechModel(next.replicateSpeechModel);
    setReplicateLipsyncImageModel(next.replicateLipsyncImageModel);
    setReplicateLipsyncVideoModel(next.replicateLipsyncVideoModel);
    setAudioProvider(next.audioProvider);
    setReplicateSoundEffectsModel(next.replicateSoundEffectsModel);
    setReplicateSoundEffectsVideoModel(next.replicateSoundEffectsVideoModel);
    setReplicateAudioExtendModel(next.replicateAudioExtendModel);
    setFalLipsyncImageModel(next.falLipsyncImageModel);
    setFalLipsyncVideoModel(next.falLipsyncVideoModel);
    setFalUpscaleModel(next.falUpscaleModel);
    setVideoDuration(next.videoDuration);
    setVideoAspectRatio(next.videoAspectRatio);
    setVideoResolution(next.videoResolution);
    setConfigLoaded(true);
    return next;
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
      notifyError("Couldn't choose the workspace folder", error);
    }
  }

  // displayedWorkspace is what the composer chip shows: the active
  // conversation's immutable root for an existing chat, the draft selection
  // (or the default) for a new chat. Always falls back to the configured
  // default root so the user is never left guessing where a message will run.
  const defaultWorkspaceRoot = toolConfig?.filesystem?.root ?? '~/Documents';

  return {
    airplaneMode,
    applyConfig,
    audioExtendDuration,
    audioProvider,
    baseURL,
    chooseToolWorkspace,
    configLoaded,
    defaultWorkspaceRoot,
    falAudioCloneModel,
    falAudioExtendModel,
    falAudioModel,
    falImageEditModel,
    falInpaintModel,
    falLipsyncImageModel,
    falLipsyncVideoModel,
    falModel,
    falSoundEffectsModel,
    falSoundEffectsVideoModel,
    falTranscribeModel,
    falUpscaleModel,
    falVideoExtendModel,
    falVideoImageModel,
    falVideoKeyframeModel,
    falVideoModel,
    falVideoMotionModel,
    falVideoReframeModel,
    falVideoRestyleModel,
    falVideoUpscaleModel,
    falVoiceDesignModel,
    ffmpegBinary,
    ffprobeBinary,
    globalModelSelection,
    harnessModel,
    harnessModels,
    harnessProvider,
    imageAspectRatio,
    imageModel,
    imageProvider,
    imageSizePreset,
    imageSteps,
    inpaintProvider,
    lipsyncProvider,
    magickBinary,
    mediaTimeoutMinutes,
    model,
    ollamaNumCtx,
    ollamaSpeechModel,
    openaiCompatibleBaseURL,
    openaiCompatibleModel,
    patchSettingsModelSelection,
    primaryModels,
    primaryProvider,
    replicateAudioExtendModel,
    replicateImageEditModel,
    replicateInpaintModel,
    runwareVideoModel,
    runwareVideoImageModel,
    runwareVideoExtendModel,
    runwareVideoUpscaleModel,
    runwareVideoRestyleModel,
    runwareInpaintModel,
    runwareLipsyncImageModel,
    runwareLipsyncVideoModel,
    runwareSpeechModel,
    runwareTranscribeModel,
    runwareSoundEffectsModel,
    runwareSoundEffectsVideoModel,
    replicateLipsyncImageModel,
    replicateLipsyncVideoModel,
    replicateModel,
    runwareModel,
    runwareUpscaleModel,
    replicateSoundEffectsModel,
    replicateSoundEffectsVideoModel,
    replicateSpeechModel,
    replicateTranscribeModel,
    replicateUpscaleModel,
    replicateVideoExtendModel,
    replicateVideoImageModel,
    replicateVideoModel,
    replicateVideoReframeModel,
    replicateVideoRestyleModel,
    replicateVideoUpscaleModel,
    setAirplaneMode,
    setBaseURL,
    setConfigLoaded,
    setFfmpegBinary,
    setFfprobeBinary,
    setHarnessModels,
    setHarnessProvider,
    setImageProvider,
    setLipsyncProvider,
    setMagickBinary,
    setMediaTimeoutMinutes,
    setModel,
    setOllamaNumCtx,
    setOpenaiCompatibleBaseURL,
    setPrimaryModels,
    setPrimaryProvider,
    setSipsBinary,
    setSoundDuration,
    setSystem,
    setTranscriptionProvider,
    setVideoDuration,
    setVideoProvider,
    setWhisperBinary,
    setWhisperChunkMinutes,
    setWhisperModel,
    setWhisperVADModel,
    sipsBinary,
    soundDuration,
    speechProvider,
    storageConfig,
    system,
    toolConfig,
    transcriptionProvider,
    videoAspectRatio,
    videoDuration,
    videoProvider,
    videoResolution,
    whisperBinary,
    whisperChunkMinutes,
    whisperModel,
    whisperVADModel,
  };
}

export {useAppConfig};
