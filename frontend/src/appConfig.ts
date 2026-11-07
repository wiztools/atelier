// The persisted AppConfig and the Settings fields App edits, in both
// directions. configValues reads a loaded config into each field's value —
// defaults filled, provider ids validated — keyed by the state it feeds;
// appConfigPayload writes the values back in the shape SaveConfig takes. A
// field written under one key but read from another resets on every restart,
// so the round trip is tested (test:app-config).
import type {main} from '../wailsjs/go/models';
import type {ChatProviderID, SpeechProviderID} from './types';
import {
  defaultAudioExtendDuration,
  defaultBaseURL,
  defaultFalAudioCloneModel,
  defaultFalAudioExtendModel,
  defaultFalAudioModel,
  defaultFalImageEditModel,
  defaultFalImageModel,
  defaultFalInpaintModel,
  defaultFalLipsyncImageModel,
  defaultFalLipsyncVideoModel,
  defaultFalSoundEffectsModel,
  defaultFalSoundEffectsVideoModel,
  defaultFalTranscribeModel,
  defaultFalUpscaleModel,
  defaultFalVideoExtendModel,
  defaultFalVideoImageModel,
  defaultFalVideoKeyframeModel,
  defaultFalVideoModel,
  defaultFalVideoMotionModel,
  defaultFalVideoReframeModel,
  defaultFalVideoRestyleModel,
  defaultFalVideoUpscaleModel,
  defaultFalVoiceDesignModel,
  defaultImageAspectRatio,
  defaultImageSizePreset,
  defaultImageSteps,
  defaultMediaTimeoutSeconds,
  defaultOllamaNumCtx,
  defaultReplicateAudioExtendModel,
  defaultReplicateImageEditModel,
  defaultReplicateImageModel,
  defaultReplicateInpaintModel,
  defaultReplicateLipsyncImageModel,
  defaultReplicateLipsyncVideoModel,
  defaultReplicateSoundEffectsModel,
  defaultReplicateSoundEffectsVideoModel,
  defaultReplicateSpeechModel,
  defaultReplicateTranscribeModel,
  defaultReplicateUpscaleModel,
  defaultReplicateVideoExtendModel,
  defaultReplicateVideoImageModel,
  defaultReplicateVideoModel,
  defaultReplicateVideoReframeModel,
  defaultReplicateVideoRestyleModel,
  defaultReplicateVideoUpscaleModel,
  defaultSoundDuration,
  defaultVideoAspectRatio,
  defaultVideoDuration,
  defaultVideoResolution,
  positiveIntOrDefault,
} from './modelDefaults';

function configValues(config: main.AppConfig) {
  const nextBaseURL = config.providers?.ollama?.baseURL || defaultBaseURL;
  const nextNumCtx = config.providers?.ollama?.numCtx || defaultOllamaNumCtx;
  const nextAirplaneMode = Boolean(config.airplaneMode);
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
  const nextImageProvider: 'fal' | 'replicate' | 'openai-compatible' = config.models?.imageProvider === 'fal' ? 'fal' : config.models?.imageProvider === 'replicate' ? 'replicate' : 'openai-compatible';
  const nextVideoProvider: 'fal' | 'replicate' = config.models?.videoProvider === 'replicate' ? 'replicate' : 'fal';
  const nextOpenAICompatibleBaseURL = config.providers?.openaiCompatible?.baseURL || 'http://localhost:8080';
  const nextOpenAICompatibleModel = config.providers?.openaiCompatible?.model ?? '';
  const nextReplicateModel = config.providers?.replicate?.model || defaultReplicateImageModel;
  const nextReplicateImageEditModel = config.providers?.replicate?.imageEditModel || defaultReplicateImageEditModel;
  const nextReplicateUpscaleModel = config.providers?.replicate?.upscaleModel || defaultReplicateUpscaleModel;
  const nextReplicateVideoUpscaleModel = config.providers?.replicate?.videoUpscaleModel || defaultReplicateVideoUpscaleModel;
  const nextReplicateVideoRestyleModel = config.providers?.replicate?.videoRestyleModel || defaultReplicateVideoRestyleModel;
  const nextReplicateVideoReframeModel = config.providers?.replicate?.videoReframeModel || defaultReplicateVideoReframeModel;
  const nextReplicateVideoExtendModel = config.providers?.replicate?.videoExtendModel || defaultReplicateVideoExtendModel;
  const nextReplicateVideoModel = config.providers?.replicate?.videoModel || defaultReplicateVideoModel;
  const nextReplicateVideoImageModel = config.providers?.replicate?.videoImageModel || defaultReplicateVideoImageModel;
  const nextFalModel = config.providers?.fal?.model || defaultFalImageModel;
  const nextFalImageEditModel = config.providers?.fal?.imageEditModel || defaultFalImageEditModel;
  const nextFalVideoModel = config.providers?.fal?.videoModel || defaultFalVideoModel;
  const nextFalVideoImageModel = config.providers?.fal?.videoImageModel || defaultFalVideoImageModel;
  const nextFalVideoKeyframeModel = config.providers?.fal?.videoKeyframeModel || defaultFalVideoKeyframeModel;
  const nextFalVideoExtendModel = config.providers?.fal?.videoExtendModel || defaultFalVideoExtendModel;
  const nextFalVideoMotionModel = config.providers?.fal?.videoMotionModel || defaultFalVideoMotionModel;
  const nextFalVideoUpscaleModel = config.providers?.fal?.videoUpscaleModel || defaultFalVideoUpscaleModel;
  const nextFalVideoReframeModel = config.providers?.fal?.videoReframeModel || defaultFalVideoReframeModel;
  const nextFalVideoRestyleModel = config.providers?.fal?.videoRestyleModel || defaultFalVideoRestyleModel;
  const nextFalAudioModel = config.providers?.fal?.audioModel || defaultFalAudioModel;
  const nextFalAudioCloneModel = config.providers?.fal?.audioCloneModel || defaultFalAudioCloneModel;
  const nextFalSoundEffectsModel = config.providers?.fal?.soundEffectsModel || defaultFalSoundEffectsModel;
  const nextFalSoundEffectsVideoModel = config.providers?.fal?.soundEffectsVideoModel || defaultFalSoundEffectsVideoModel;
  const nextFalVoiceDesignModel = config.providers?.fal?.voiceDesignModel || defaultFalVoiceDesignModel;
  const nextFalAudioExtendModel = config.providers?.fal?.audioExtendModel || defaultFalAudioExtendModel;
  const nextFalTranscribeModel = config.providers?.fal?.transcribeModel || defaultFalTranscribeModel;
  const nextReplicateTranscribeModel = config.providers?.replicate?.transcribeModel || defaultReplicateTranscribeModel;
  const nextTranscriptionProvider: 'fal' | 'local-whisper' | 'replicate' = config.models?.transcriptionProvider === 'local-whisper' ? 'local-whisper' : config.models?.transcriptionProvider === 'replicate' ? 'replicate' : 'fal';
  const nextWhisperModel = config.providers?.local?.whisper?.model ?? '';
  const nextWhisperBinary = config.providers?.local?.whisper?.binary ?? '';
  const nextWhisperVADModel = config.providers?.local?.whisper?.vadModel ?? '';
  const nextWhisperChunkMinutes = config.providers?.local?.whisper?.chunkMinutes ? String(config.providers.local.whisper.chunkMinutes) : '';
  const nextFfmpegBinary = config.providers?.local?.ffmpeg?.binary ?? '';
  const nextFfprobeBinary = config.providers?.local?.ffprobe?.binary ?? '';
  const nextSipsBinary = config.providers?.local?.sips?.binary ?? '';
  const nextMagickBinary = config.providers?.local?.magick?.binary ?? '';
  const nextFalLipsyncImageModel = config.providers?.fal?.lipsyncImageModel || defaultFalLipsyncImageModel;
  const nextFalLipsyncVideoModel = config.providers?.fal?.lipsyncVideoModel || defaultFalLipsyncVideoModel;
  const nextLipsyncProvider: 'fal' | 'replicate' = config.models?.lipsyncProvider === 'replicate' ? 'replicate' : 'fal';
  const nextSpeechProvider: SpeechProviderID = config.models?.speechProvider === 'ollama' || config.models?.speechProvider === 'replicate' ? config.models.speechProvider : 'fal';
  const nextOllamaSpeechModel = config.providers?.ollama?.models?.speech ?? '';
  const nextReplicateSpeechModel = config.providers?.replicate?.speechModel || defaultReplicateSpeechModel;
  const nextAudioProvider: 'fal' | 'replicate' = config.models?.audioProvider === 'replicate' ? 'replicate' : 'fal';
  const nextReplicateSoundEffectsModel = config.providers?.replicate?.soundEffectsModel || defaultReplicateSoundEffectsModel;
  const nextReplicateSoundEffectsVideoModel = config.providers?.replicate?.soundEffectsVideoModel || defaultReplicateSoundEffectsVideoModel;
  const nextReplicateAudioExtendModel = config.providers?.replicate?.audioExtendModel || defaultReplicateAudioExtendModel;
  const nextReplicateLipsyncImageModel = config.providers?.replicate?.lipsyncImageModel || defaultReplicateLipsyncImageModel;
  const nextReplicateLipsyncVideoModel = config.providers?.replicate?.lipsyncVideoModel || defaultReplicateLipsyncVideoModel;
  const nextFalUpscaleModel = config.providers?.fal?.upscaleModel || defaultFalUpscaleModel;
  const nextInpaintProvider: 'fal' | 'replicate' = config.models?.inpaintProvider === 'replicate' ? 'replicate' : 'fal';
  const nextFalInpaintModel = config.providers?.fal?.inpaintModel || defaultFalInpaintModel;
  const nextReplicateInpaintModel = config.providers?.replicate?.inpaintModel || defaultReplicateInpaintModel;
  const nextVideoDuration = config.generation?.video?.duration || defaultVideoDuration;
  const nextVideoAspectRatio = config.generation?.video?.aspectRatio || defaultVideoAspectRatio;
  const nextVideoResolution = config.generation?.video?.resolution || defaultVideoResolution;
  const nextSoundDuration = config.generation?.audio?.duration || defaultSoundDuration;
  const nextAudioExtendDuration = config.generation?.audio?.extendDuration || defaultAudioExtendDuration;
  const nextMediaTimeoutSeconds = config.generation?.mediaTimeoutSeconds ?? 0;
  const nextMediaTimeoutMinutes = Math.max(1, Math.round((nextMediaTimeoutSeconds > 0 ? nextMediaTimeoutSeconds : defaultMediaTimeoutSeconds) / 60));

  return {
    // The generated model types these sections as always present; the Settings
    // state holds null until a config has loaded.
    storageConfig: (config.storage ?? null) as main.ConfigStorage | null,
    toolConfig: (config.tools ?? null) as main.ConfigTools | null,
    updatesConfig: (config.updates ?? null) as main.ConfigUpdates | null,
    baseURL: nextBaseURL,
    ollamaNumCtx: nextNumCtx,
    airplaneMode: nextAirplaneMode,
    primaryModels: {ollama: nextPrimaryModel, openrouter: nextOpenRouterModel, 'openai-compatible': nextOpenAICompatiblePrimary},
    primaryProvider: nextPrimaryProvider,
    harnessModels: {ollama: nextHarnessModel, openrouter: nextOpenRouterHarness, 'openai-compatible': nextOpenAICompatibleHarness},
    harnessProvider: nextHarnessProvider,
    imageModel: nextImageModel,
    system: nextSystem,
    imageAspectRatio: nextImageAspectRatio,
    imageSizePreset: nextImageSizePreset,
    imageSteps: nextImageSteps,
    imageProvider: nextImageProvider,
    videoProvider: nextVideoProvider,
    inpaintProvider: nextInpaintProvider,
    falInpaintModel: nextFalInpaintModel,
    replicateInpaintModel: nextReplicateInpaintModel,
    soundDuration: nextSoundDuration,
    audioExtendDuration: nextAudioExtendDuration,
    mediaTimeoutMinutes: nextMediaTimeoutMinutes,
    openaiCompatibleBaseURL: nextOpenAICompatibleBaseURL,
    openaiCompatibleModel: nextOpenAICompatibleModel,
    replicateModel: nextReplicateModel,
    replicateImageEditModel: nextReplicateImageEditModel,
    replicateUpscaleModel: nextReplicateUpscaleModel,
    replicateVideoUpscaleModel: nextReplicateVideoUpscaleModel,
    replicateVideoRestyleModel: nextReplicateVideoRestyleModel,
    replicateVideoReframeModel: nextReplicateVideoReframeModel,
    replicateVideoExtendModel: nextReplicateVideoExtendModel,
    replicateVideoModel: nextReplicateVideoModel,
    replicateVideoImageModel: nextReplicateVideoImageModel,
    falModel: nextFalModel,
    falImageEditModel: nextFalImageEditModel,
    falVideoModel: nextFalVideoModel,
    falVideoImageModel: nextFalVideoImageModel,
    falVideoKeyframeModel: nextFalVideoKeyframeModel,
    falVideoExtendModel: nextFalVideoExtendModel,
    falVideoMotionModel: nextFalVideoMotionModel,
    falVideoUpscaleModel: nextFalVideoUpscaleModel,
    falVideoReframeModel: nextFalVideoReframeModel,
    falVideoRestyleModel: nextFalVideoRestyleModel,
    falAudioModel: nextFalAudioModel,
    falAudioCloneModel: nextFalAudioCloneModel,
    falSoundEffectsModel: nextFalSoundEffectsModel,
    falSoundEffectsVideoModel: nextFalSoundEffectsVideoModel,
    falVoiceDesignModel: nextFalVoiceDesignModel,
    falAudioExtendModel: nextFalAudioExtendModel,
    falTranscribeModel: nextFalTranscribeModel,
    replicateTranscribeModel: nextReplicateTranscribeModel,
    transcriptionProvider: nextTranscriptionProvider,
    whisperModel: nextWhisperModel,
    whisperBinary: nextWhisperBinary,
    whisperVADModel: nextWhisperVADModel,
    whisperChunkMinutes: nextWhisperChunkMinutes,
    ffmpegBinary: nextFfmpegBinary,
    ffprobeBinary: nextFfprobeBinary,
    sipsBinary: nextSipsBinary,
    magickBinary: nextMagickBinary,
    lipsyncProvider: nextLipsyncProvider,
    speechProvider: nextSpeechProvider,
    ollamaSpeechModel: nextOllamaSpeechModel,
    replicateSpeechModel: nextReplicateSpeechModel,
    replicateLipsyncImageModel: nextReplicateLipsyncImageModel,
    replicateLipsyncVideoModel: nextReplicateLipsyncVideoModel,
    audioProvider: nextAudioProvider,
    replicateSoundEffectsModel: nextReplicateSoundEffectsModel,
    replicateSoundEffectsVideoModel: nextReplicateSoundEffectsVideoModel,
    replicateAudioExtendModel: nextReplicateAudioExtendModel,
    falLipsyncImageModel: nextFalLipsyncImageModel,
    falLipsyncVideoModel: nextFalLipsyncVideoModel,
    falUpscaleModel: nextFalUpscaleModel,
    videoDuration: nextVideoDuration,
    videoAspectRatio: nextVideoAspectRatio,
    videoResolution: nextVideoResolution,
  };
}

type AppConfigValues = ReturnType<typeof configValues>;

// The three key flags ride along as each provider's enabled bit; they come
// from the keychain, not from the config file.
type ConfigKeyFlags = {openRouterHasKey: boolean; falHasKey: boolean; replicateHasKey: boolean};

function appConfigPayload(s: AppConfigValues & ConfigKeyFlags) {
  return {
    version: 1,
    airplaneMode: s.airplaneMode,
    storage: s.storageConfig ?? undefined,
    providers: {
      ollama: {
        baseURL: s.baseURL,
        numCtx: s.ollamaNumCtx,
        models: {
          primary: s.primaryModels.ollama,
          harness: s.harnessModels.ollama,
          image: s.imageModel,
          speech: s.ollamaSpeechModel,
        },
      },
      openrouter: {
        enabled: s.openRouterHasKey,
        primary: s.primaryModels.openrouter,
        harness: s.harnessModels.openrouter,
      },
      fal: {
        enabled: s.falHasKey,
        model: s.falModel,
        imageEditModel: s.falImageEditModel,
        videoModel: s.falVideoModel,
        videoImageModel: s.falVideoImageModel,
        videoKeyframeModel: s.falVideoKeyframeModel,
        videoExtendModel: s.falVideoExtendModel,
        videoMotionModel: s.falVideoMotionModel,
        videoUpscaleModel: s.falVideoUpscaleModel,
        videoReframeModel: s.falVideoReframeModel,
        videoRestyleModel: s.falVideoRestyleModel,
        audioModel: s.falAudioModel,
        soundEffectsModel: s.falSoundEffectsModel,
        soundEffectsVideoModel: s.falSoundEffectsVideoModel,
        voiceDesignModel: s.falVoiceDesignModel,
        audioCloneModel: s.falAudioCloneModel,
        audioExtendModel: s.falAudioExtendModel,
        transcribeModel: s.falTranscribeModel,
        upscaleModel: s.falUpscaleModel,
        inpaintModel: s.falInpaintModel,
        lipsyncImageModel: s.falLipsyncImageModel,
        lipsyncVideoModel: s.falLipsyncVideoModel,
      },
      replicate: {
        enabled: s.replicateHasKey,
        model: s.replicateModel,
        imageEditModel: s.replicateImageEditModel,
        upscaleModel: s.replicateUpscaleModel,
        inpaintModel: s.replicateInpaintModel,
        videoUpscaleModel: s.replicateVideoUpscaleModel,
        videoRestyleModel: s.replicateVideoRestyleModel,
        videoReframeModel: s.replicateVideoReframeModel,
        videoExtendModel: s.replicateVideoExtendModel,
        videoModel: s.replicateVideoModel,
        videoImageModel: s.replicateVideoImageModel,
        transcribeModel: s.replicateTranscribeModel,
        lipsyncImageModel: s.replicateLipsyncImageModel,
        lipsyncVideoModel: s.replicateLipsyncVideoModel,
        soundEffectsModel: s.replicateSoundEffectsModel,
        soundEffectsVideoModel: s.replicateSoundEffectsVideoModel,
        audioExtendModel: s.replicateAudioExtendModel,
        speechModel: s.replicateSpeechModel,
      },
      openaiCompatible: {
        baseURL: s.openaiCompatibleBaseURL,
        primary: s.primaryModels['openai-compatible'],
        harness: s.harnessModels['openai-compatible'],
        model: s.openaiCompatibleModel,
      },
      local: {
        whisper: {
          binary: s.whisperBinary,
          model: s.whisperModel,
          vadModel: s.whisperVADModel,
          chunkMinutes: positiveIntOrDefault(s.whisperChunkMinutes, 0),
        },
        ffmpeg: {
          binary: s.ffmpegBinary,
        },
        ffprobe: {
          binary: s.ffprobeBinary,
        },
        sips: {
          binary: s.sipsBinary,
        },
        magick: {
          binary: s.magickBinary,
        },
      },
    },
    models: {
      primaryProvider: s.primaryProvider,
      harnessProvider: s.harnessProvider,
      imageProvider: s.imageProvider,
      videoProvider: s.videoProvider,
      inpaintProvider: s.inpaintProvider,
      lipsyncProvider: s.lipsyncProvider,
      speechProvider: s.speechProvider,
      audioProvider: s.audioProvider,
      transcriptionProvider: s.transcriptionProvider,
    },
    prompts: {
      system: s.system,
    },
    generation: {
      image: {
        aspectRatio: s.imageAspectRatio,
        sizePreset: s.imageSizePreset,
        steps: s.imageSteps,
      },
      video: {
        duration: s.videoDuration,
        aspectRatio: s.videoAspectRatio,
        resolution: s.videoResolution || undefined,
      },
      audio: {
        duration: s.soundDuration,
        extendDuration: s.audioExtendDuration,
      },
      mediaTimeoutSeconds: s.mediaTimeoutMinutes * 60,
    },
    tools: s.toolConfig ?? undefined,
    ui: {
      mode: 'chat',
    },
    // Round-trip the updater section untouched: it has no Settings UI yet,
    // and dropping it here would reset a manually-edited manifestUrl or
    // autoCheck on every unrelated settings save.
    updates: s.updatesConfig ?? undefined,
  };
}

export type {AppConfigValues, ConfigKeyFlags};
export {configValues, appConfigPayload};
