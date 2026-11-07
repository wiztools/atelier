import {useMemo, useState} from 'react';
import {
  ListFalModels,
  ListFalImageEditModels,
  ListFalVideoModels,
  ListFalVideoImageModels,
  ListFalVideoKeyframeModels,
  ListFalVideoExtendModels,
  ListFalVideoMotionModels,
  ListFalVideoUpscaleModels,
  ListFalVideoReframeModels,
  ListFalVideoRestyleModels,
  ListFalSpeechModels,
  ListFalSoundEffectModels,
  ListFalAudioExtendModels,
  ListFalVoiceDesignModels,
  ListFalTranscribeModels,
  ListFalUpscaleModels,
  ListFalLipsyncImageModels,
  ListFalLipsyncVideoModels,
  ListFalSoundEffectsVideoModels,
  ListReplicateModels,
  ListReplicateImageEditModels,
  ListReplicateLipsyncImageModels,
  ListReplicateLipsyncVideoModels,
  ListReplicateUpscaleModels,
  ListReplicateVideoUpscaleModels,
  ListReplicateVideoRestyleModels,
  ListReplicateVideoReframeModels,
  ListReplicateVideoExtendModels,
  ListReplicateVideoModels,
  ListReplicateVideoImageModels,
  ListReplicateTranscribeModels,
  ListReplicateSoundEffectModels,
  ListReplicateSoundEffectsVideoModels,
  ListReplicateAudioExtendModels,
  ListReplicateSpeechModels,
  ListPrimaryModels,
} from '../../wailsjs/go/main/App';
import {main} from '../../wailsjs/go/models';
import {asArray} from '../utils';
import {falCatalogWithRetry} from './modelOptions';

// useModelCatalogs owns the model catalogs the pickers offer: fal's
// per-category lists, Replicate's collections, and the OpenRouter and
// OpenAI-compatible model lists, with the loaders that fill them. Every catalog
// is a discovery aid, so a failed load leaves a picker's field free text. It
// reads nothing from App: loadConfig and the key savers call the refreshers,
// and the key-clear handlers empty lists through the returned setters.
function useModelCatalogs() {
  const [openRouterModels, setOpenRouterModels] = useState<main.ModelInfo[]>([]);
  const [openaiCompatibleModels, setOpenaiCompatibleModels] = useState<string[]>([]);

  const [falModels, setFalModels] = useState<main.FalModel[]>([]);
  const [falImageEditModels, setFalImageEditModels] = useState<main.FalModel[]>([]);
  const [falVideoModels, setFalVideoModels] = useState<main.FalModel[]>([]);
  const [falVideoImageModels, setFalVideoImageModels] = useState<main.FalModel[]>([]);
  const [falVideoKeyframeModels, setFalVideoKeyframeModels] = useState<main.FalModel[]>([]);
  const [falVideoExtendModels, setFalVideoExtendModels] = useState<main.FalModel[]>([]);
  const [falVideoMotionModels, setFalVideoMotionModels] = useState<main.FalModel[]>([]);
  const [falVideoUpscaleModels, setFalVideoUpscaleModels] = useState<main.FalModel[]>([]);
  const [falVideoReframeModels, setFalVideoReframeModels] = useState<main.FalModel[]>([]);
  const [falVideoRestyleModels, setFalVideoRestyleModels] = useState<main.FalModel[]>([]);
  const [falAudioModels, setFalAudioModels] = useState<main.FalModel[]>([]);
  const [falSoundEffectModels, setFalSoundEffectModels] = useState<main.FalModel[]>([]);
  const [falSoundEffectsVideoModels, setFalSoundEffectsVideoModels] = useState<main.FalModel[]>([]);
  const [falAudioExtendModels, setFalAudioExtendModels] = useState<main.FalModel[]>([]);
  const [falVoiceDesignModels, setFalVoiceDesignModels] = useState<main.FalModel[]>([]);
  const [falTranscribeModels, setFalTranscribeModels] = useState<main.FalModel[]>([]);
  const [falLipsyncImageModels, setFalLipsyncImageModels] = useState<main.FalModel[]>([]);
  const [falLipsyncVideoModels, setFalLipsyncVideoModels] = useState<main.FalModel[]>([]);
  const [falUpscaleModels, setFalUpscaleModels] = useState<main.FalModel[]>([]);

  const [replicateSpeechModels, setReplicateSpeechModels] = useState<main.ReplicateModel[]>([]);
  const [replicateModels, setReplicateModels] = useState<main.ReplicateModel[]>([]);
  const [replicateImageEditModels, setReplicateImageEditModels] = useState<main.ReplicateModel[]>([]);
  const [replicateUpscaleModels, setReplicateUpscaleModels] = useState<main.ReplicateModel[]>([]);
  const [replicateVideoUpscaleModels, setReplicateVideoUpscaleModels] = useState<main.ReplicateModel[]>([]);
  const [replicateVideoRestyleModels, setReplicateVideoRestyleModels] = useState<main.ReplicateModel[]>([]);
  const [replicateVideoReframeModels, setReplicateVideoReframeModels] = useState<main.ReplicateModel[]>([]);
  const [replicateVideoExtendModels, setReplicateVideoExtendModels] = useState<main.ReplicateModel[]>([]);
  const [replicateVideoModels, setReplicateVideoModels] = useState<main.ReplicateModel[]>([]);
  const [replicateVideoImageModels, setReplicateVideoImageModels] = useState<main.ReplicateModel[]>([]);
  const [replicateTranscribeModels, setReplicateTranscribeModels] = useState<main.ReplicateModel[]>([]);
  const [replicateLipsyncImageModels, setReplicateLipsyncImageModels] = useState<main.ReplicateModel[]>([]);
  const [replicateLipsyncVideoModels, setReplicateLipsyncVideoModels] = useState<main.ReplicateModel[]>([]);
  const [replicateSoundEffectsModels, setReplicateSoundEffectsModels] = useState<main.ReplicateModel[]>([]);
  const [replicateSoundEffectsVideoModels, setReplicateSoundEffectsVideoModels] = useState<main.ReplicateModel[]>([]);
  const [replicateAudioExtendModels, setReplicateAudioExtendModels] = useState<main.ReplicateModel[]>([]);

  const openaiCompatibleModelOptions = useMemo(
    () => openaiCompatibleModels.map((id) => ({value: id, label: id})),
    [openaiCompatibleModels],
  );

  async function refreshOpenRouterModels() {
    // The catalog is a discovery aid like fal's and Replicate's collections
    // (the model field stays free text without it), and OpenRouter's /models
    // endpoint is public — it returns 200 even for an invalid key — so neither
    // its success nor its failure says anything about the key. The status light
    // comes from checkOpenRouterConnection's authenticated /key ping; swallow
    // catalog errors and keep the last-good list on a transient blip.
    try {
      setOpenRouterModels(asArray(await ListPrimaryModels('openrouter', '')));
    } catch {
      // Keep whatever list is already loaded; a cold-load failure just leaves it empty.
    }
  }

  async function refreshFalModels() {
    // The fal catalog is a discovery aid only — a load failure leaves the field
    // as free text, so swallow the error rather than surfacing it as a fal
    // connection error (which would confuse "key works" vs "catalog fetch").
    // Each load rides falCatalogWithRetry, and the Go transport waits out short
    // 429 windows itself — the screen fires ~30 paginated /v1/models requests
    // on load and fal rate-limits the tail of that burst, which used to leave
    // the audio pickers with zero options and no dropdown (the speech combobox
    // simply "didn't open").
    try {
      setFalModels(await falCatalogWithRetry(ListFalModels));
    } catch {
      setFalModels([]);
    }
    try {
      setFalImageEditModels(await falCatalogWithRetry(ListFalImageEditModels));
    } catch {
      setFalImageEditModels([]);
    }
    try {
      setFalVideoModels(await falCatalogWithRetry(ListFalVideoModels));
    } catch {
      setFalVideoModels([]);
    }
    try {
      setFalVideoImageModels(await falCatalogWithRetry(ListFalVideoImageModels));
    } catch {
      setFalVideoImageModels([]);
    }
    try {
      setFalVideoKeyframeModels(await falCatalogWithRetry(ListFalVideoKeyframeModels));
    } catch {
      setFalVideoKeyframeModels([]);
    }
    try {
      setFalVideoExtendModels(await falCatalogWithRetry(ListFalVideoExtendModels));
    } catch {
      setFalVideoExtendModels([]);
    }
    try {
      setFalVideoMotionModels(await falCatalogWithRetry(ListFalVideoMotionModels));
    } catch {
      setFalVideoMotionModels([]);
    }
    try {
      setFalVideoUpscaleModels(await falCatalogWithRetry(ListFalVideoUpscaleModels));
    } catch {
      setFalVideoUpscaleModels([]);
    }
    try {
      setFalVideoReframeModels(await falCatalogWithRetry(ListFalVideoReframeModels));
    } catch {
      setFalVideoReframeModels([]);
    }
    try {
      setFalVideoRestyleModels(await falCatalogWithRetry(ListFalVideoRestyleModels));
    } catch {
      setFalVideoRestyleModels([]);
    }
    try {
      setFalAudioModels(await falCatalogWithRetry(ListFalSpeechModels));
    } catch {
      setFalAudioModels([]);
    }
    try {
      setFalSoundEffectModels(await falCatalogWithRetry(ListFalSoundEffectModels));
    } catch {
      setFalSoundEffectModels([]);
    }
    try {
      setFalSoundEffectsVideoModels(await falCatalogWithRetry(ListFalSoundEffectsVideoModels));
    } catch {
      setFalSoundEffectsVideoModels([]);
    }
    try {
      setFalAudioExtendModels(await falCatalogWithRetry(ListFalAudioExtendModels));
    } catch {
      setFalAudioExtendModels([]);
    }
    try {
      setFalVoiceDesignModels(await falCatalogWithRetry(ListFalVoiceDesignModels));
    } catch {
      setFalVoiceDesignModels([]);
    }
    try {
      setFalTranscribeModels(await falCatalogWithRetry(ListFalTranscribeModels));
    } catch {
      setFalTranscribeModels([]);
    }
    try {
      setFalUpscaleModels(await falCatalogWithRetry(ListFalUpscaleModels));
    } catch {
      // Keep the last-good list on a transient fetch failure (the upscale and
      // edit pickers page fal's large image-to-image catalog and can time out);
      // clearing it would drop a loaded picker to the "couldn't load" hint on a
      // momentary blip. A genuine cold-load failure simply leaves it empty.
    }
    try {
      setFalLipsyncImageModels(await falCatalogWithRetry(ListFalLipsyncImageModels));
    } catch {
      setFalLipsyncImageModels([]);
    }
    try {
      setFalLipsyncVideoModels(await falCatalogWithRetry(ListFalLipsyncVideoModels));
    } catch {
      setFalLipsyncVideoModels([]);
    }
  }

  async function refreshReplicateModels() {
    // The Replicate collections are a discovery aid like fal's catalog — a
    // load failure leaves the field as free text, so swallow the error.
    try {
      setReplicateModels(asArray(await ListReplicateModels()));
    } catch {
      setReplicateModels([]);
    }
    try {
      setReplicateImageEditModels(asArray(await ListReplicateImageEditModels()));
    } catch {
      setReplicateImageEditModels([]);
    }
    try {
      setReplicateUpscaleModels(asArray(await ListReplicateUpscaleModels()));
    } catch {
      setReplicateUpscaleModels([]);
    }
    try {
      setReplicateVideoUpscaleModels(asArray(await ListReplicateVideoUpscaleModels()));
    } catch {
      setReplicateVideoUpscaleModels([]);
    }
    try {
      setReplicateVideoRestyleModels(asArray(await ListReplicateVideoRestyleModels()));
    } catch {
      setReplicateVideoRestyleModels([]);
    }
    try {
      setReplicateVideoReframeModels(asArray(await ListReplicateVideoReframeModels()));
    } catch {
      setReplicateVideoReframeModels([]);
    }
    try {
      setReplicateVideoExtendModels(asArray(await ListReplicateVideoExtendModels()));
    } catch {
      setReplicateVideoExtendModels([]);
    }
    try {
      setReplicateVideoModels(asArray(await ListReplicateVideoModels()));
    } catch {
      setReplicateVideoModels([]);
    }
    try {
      setReplicateVideoImageModels(asArray(await ListReplicateVideoImageModels()));
    } catch {
      setReplicateVideoImageModels([]);
    }
    try {
      setReplicateTranscribeModels(asArray(await ListReplicateTranscribeModels()));
    } catch {
      setReplicateTranscribeModels([]);
    }
    try {
      setReplicateLipsyncImageModels(asArray(await ListReplicateLipsyncImageModels()));
    } catch {
      setReplicateLipsyncImageModels([]);
    }
    try {
      setReplicateLipsyncVideoModels(asArray(await ListReplicateLipsyncVideoModels()));
    } catch {
      setReplicateLipsyncVideoModels([]);
    }
    try {
      setReplicateSoundEffectsModels(asArray(await ListReplicateSoundEffectModels()));
    } catch {
      setReplicateSoundEffectsModels([]);
    }
    try {
      setReplicateSpeechModels(asArray(await ListReplicateSpeechModels()));
    } catch {
      setReplicateSpeechModels([]);
    }
    try {
      setReplicateSoundEffectsVideoModels(asArray(await ListReplicateSoundEffectsVideoModels()));
    } catch {
      setReplicateSoundEffectsVideoModels([]);
    }
    try {
      setReplicateAudioExtendModels(asArray(await ListReplicateAudioExtendModels()));
    } catch {
      setReplicateAudioExtendModels([]);
    }
  }

  return {
    falAudioExtendModels,
    falAudioModels,
    falImageEditModels,
    falLipsyncImageModels,
    falLipsyncVideoModels,
    falModels,
    falSoundEffectModels,
    falSoundEffectsVideoModels,
    falTranscribeModels,
    falUpscaleModels,
    falVideoExtendModels,
    falVideoImageModels,
    falVideoKeyframeModels,
    falVideoModels,
    falVideoMotionModels,
    falVideoReframeModels,
    falVideoRestyleModels,
    falVideoUpscaleModels,
    falVoiceDesignModels,
    openRouterModels,
    openaiCompatibleModelOptions,
    openaiCompatibleModels,
    refreshFalModels,
    refreshOpenRouterModels,
    refreshReplicateModels,
    replicateAudioExtendModels,
    replicateImageEditModels,
    replicateLipsyncImageModels,
    replicateLipsyncVideoModels,
    replicateModels,
    replicateSoundEffectsModels,
    replicateSoundEffectsVideoModels,
    replicateSpeechModels,
    replicateTranscribeModels,
    replicateUpscaleModels,
    replicateVideoExtendModels,
    replicateVideoImageModels,
    replicateVideoModels,
    replicateVideoReframeModels,
    replicateVideoRestyleModels,
    replicateVideoUpscaleModels,
    setFalAudioExtendModels,
    setFalAudioModels,
    setFalLipsyncImageModels,
    setFalLipsyncVideoModels,
    setFalModels,
    setFalSoundEffectModels,
    setFalSoundEffectsVideoModels,
    setFalTranscribeModels,
    setFalVideoExtendModels,
    setFalVideoImageModels,
    setFalVideoKeyframeModels,
    setFalVideoModels,
    setOpenRouterModels,
    setOpenaiCompatibleModels,
    setReplicateImageEditModels,
    setReplicateLipsyncImageModels,
    setReplicateLipsyncVideoModels,
    setReplicateModels,
    setReplicateUpscaleModels,
    setReplicateVideoImageModels,
    setReplicateVideoModels,
    setReplicateVideoReframeModels,
    setReplicateVideoRestyleModels,
    setReplicateVideoUpscaleModels,
  };
}

export {useModelCatalogs};
