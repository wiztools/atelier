// Generation-model defaults and option ladders: the default endpoint ids per
// provider (mirroring the Go constants in fal_client.go / replicate_client.go),
// the aspect-ratio/resolution/duration option tables the settings panel and
// composer pickers share, and the small pure helpers over them. Data only —
// every default here must stay in sync with its Go counterpart.

const defaultBaseURL = 'http://localhost:11434';
// Mirrors Go's defaultOllamaNumCtx (app.go): 16K because planner prompts
// carry the tool catalog (~5K tokens in native tool mode) before any
// conversation or evidence. Must stay in sync with the Go default so a config
// saved without a visit to the field hydrates to the same value.
const defaultOllamaNumCtx = 16384;
// Mirrors Go's projectNotesMaxChars (app.go): the textarea's maxLength. The
// backend caps the injected block at the same bound, so a value at the limit
// is never silently trimmed at turn time.
const projectNotesMaxLength = 4000;
const numCtxOptions = [8192, 12288, 16384, 32768];
function numCtxLabel(value: number): string {
  return `${Math.round(value / 1024)}K (${value} tokens)`;
}

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
// The editor's mask-inpainting default mirrors the Go const (fal_client.go is
// the source of truth; the verified contract lives there).
const defaultFalInpaintModel = 'fal-ai/flux-pro/v1/fill';
const defaultFalVideoModel = 'fal-ai/kling-video/v2/master/text-to-video';
const defaultFalVideoImageModel = 'fal-ai/kling-video/v2/master/image-to-video';
const defaultFalVideoKeyframeModel = 'fal-ai/bytedance/seedance-2.0/image-to-video';
const defaultFalVideoExtendModel = 'fal-ai/veo3.1/extend-video';
const defaultFalVideoMotionModel = 'fal-ai/kling-video/v2.6/pro/motion-control';
const defaultFalVideoUpscaleModel = 'fal-ai/video-upscaler';
const defaultFalVideoReframeModel = 'fal-ai/ltx-2.3/reframe';
const defaultFalVideoRestyleModel = 'fal-ai/kling-video/o3/pro/video-to-video/edit';
const defaultFalAudioModel = 'fal-ai/elevenlabs/tts/multilingual-v2';
// The Ollama speech slot's placeholder — the Go resolver's belt
// (defaultOllamaSpeechModel); the picker lists whatever "speech"-capable
// models the local Ollama has pulled.
const defaultOllamaSpeechModel = 'phonon';
const defaultFalAudioCloneModel = 'fal-ai/minimax/voice-clone';
const defaultFalSoundEffectsModel = 'fal-ai/elevenlabs/sound-effects/v2';
// Video-to-sound-effects endpoint (the add_sound_effects tool) — Mirelo SFX
// 2.0 returns the clip with the generated soundtrack mixed in; the picker also
// surfaces the video-to-audio twin and MMAudio V2. Mirrors the Go constant in
// fal_client.go.
const defaultFalSoundEffectsVideoModel = 'mirelo-ai/sfx2.0/video-to-video';
const defaultFalVoiceDesignModel = 'fal-ai/minimax/voice-design';
const defaultFalAudioExtendModel = 'fal-ai/stable-audio-25/inpaint';
const defaultFalTranscribeModel = 'fal-ai/wizper';
const defaultReplicateTranscribeModel = 'openai/whisper';
// Replicate lipsync defaults — the collection's official endpoints the
// canonical request drives outright (the Go resolvers' fallback constants).
const defaultReplicateLipsyncImageModel = 'veed/fabric-1.0';
const defaultReplicateLipsyncVideoModel = 'sync/lipsync-2';
// Kling's lipsync/audio-to-video takes video_url + audio_url — a video-face
// endpoint despite the name, so it defaults the video+audio slot; the
// image+audio slot needs sync-lipsync's image-to-video endpoint (kling has no
// image input and 422s there). Mirrors the Go constants in fal_client.go.
const defaultFalLipsyncImageModel = 'fal-ai/sync-lipsync/v3/image-to-video';
const defaultFalLipsyncVideoModel = 'fal-ai/kling-video/lipsync/audio-to-video';
const defaultFalUpscaleModel = 'fal-ai/esrgan';
// Replicate backend defaults — owner/name slugs, kept in sync with the Go
// consts in replicate_client.go (the single source of truth; these mirror it
// for the Settings pickers the same way the fal constants above do).
const defaultReplicateImageModel = 'black-forest-labs/flux-schnell';
// FLUX.1 Kontext [dev] on Runware (text-to-image and edits) — mirrors
// defaultRunwareImageModel in runware_client.go.
const defaultRunwareImageModel = 'runware:106@1';
// The runware video-family, inpaint, lipsync, speech, and sound defaults —
// AIR ids kept in sync with the Go consts in runware_client.go (the single
// source of truth; each one's schema was read before it was pinned there).
const defaultRunwareVideoModel = 'klingai:kling-video@4.0';
const defaultRunwareVideoImageModel = 'klingai:kling-video@4.0';
const defaultRunwareVideoExtendModel = 'google:3@2';
const defaultRunwareVideoUpscaleModel = 'bfl:flux@video-upscale';
const defaultRunwareVideoRestyleModel = 'bfl:flux@video-edit-fast';
// upscale_image's runware model — Real-ESRGAN, the ESRGAN family fal's and
// Replicate's defaults speak. Mirrors the Go const in runware_client.go.
const defaultRunwareUpscaleModel = 'runware:504@1';
const defaultRunwareInpaintModel = 'bfl:1@2';
const defaultRunwareLipsyncImageModel = 'veed:fabric@1.0-hosted';
const defaultRunwareLipsyncVideoModel = 'klingai:7@1';
const defaultRunwareSpeechModel = 'minimax:speech@2.8';
// transcribe_audio's runware model — a Gemini text-inference model with audio
// inputs (the transcript is plain text; no cue track). Mirrors the Go const
// in runware_client.go.
const defaultRunwareTranscribeModel = 'google:gemini@3.5-flash';
const defaultRunwareSoundEffectsModel = 'mirelo:1@1';
const defaultRunwareSoundEffectsVideoModel = 'mirelo:1@1';
const defaultReplicateImageEditModel = 'black-forest-labs/flux-kontext-pro';
const defaultReplicateVideoModel = 'wan-video/wan-2.5-t2v';
const defaultReplicateVideoImageModel = 'wan-video/wan-2.5-i2v';
const defaultReplicateUpscaleModel = 'nightmareai/real-esrgan';
const defaultReplicateInpaintModel = 'black-forest-labs/flux-fill-pro';
const defaultReplicateVideoUpscaleModel = 'topazlabs/video-upscale';
const defaultReplicateVideoRestyleModel = 'kwaivgi/kling-v3-omni-video';
const defaultReplicateVideoReframeModel = 'luma/reframe-video';
const defaultReplicateVideoExtendModel = 'xai/grok-imagine-video-extension';
// Replicate non-voice audio defaults — generate_sound's text-to-audio model
// (the ai-music-generation collection's music-and-sound-effects official
// endpoint) and add_sound_effects' video-to-sound model (MMAudio V2; its
// soundtrack arrives as a standalone clip replace_audio lays under the
// video). Mirrors the Go constants in replicate_client.go.
const defaultReplicateSoundEffectsModel = 'stability-ai/stable-audio-2.5';
const defaultReplicateSoundEffectsVideoModel = 'zsxkib/mmaudio';
// generate_speech's Replicate model — minimax/speech-2.8-hd, the
// text-to-speech collection's featured official endpoint (catalog voice ids,
// an emotion enum, a 0.5–2 speed). Mirrors the Go constant.
const defaultReplicateSpeechModel = 'minimax/speech-2.8-hd';
// extend_audio's Replicate continuation model — meta/musicgen continues an
// attached clip forward within a ~30s total ceiling. Mirrors the Go constant
// in replicate_client.go.
const defaultReplicateAudioExtendModel = 'meta/musicgen';
const defaultVideoDuration = '5';
const defaultVideoAspectRatio = '16:9';
// '' (the default) lets the video model pick its own resolution — the
// pre-setting behavior; the backend drops a tier the selected model's enum
// doesn't list with a notice, so the fixed ladder never 422s a generation.
const defaultVideoResolution = '';
const videoResolutionOptions = ['', '480p', '720p', '1080p', '4k'];
const videoResolutionLabels: Record<string, string> = {'': 'Auto (model chooses)'};
// 'auto' lets the video model size the clip to the prompt (Seedance supports it;
// other models drop it with a notice via the backend enum-guard). These are the
// generic fallback options shown when a model's published schema can't be
// loaded (offline / no key); each picker fetches its own model-specific set via
// ListFalVideoDurations. Modern models accept clips up to 30s, so the fallback
// ladder tops out there — a value the selected model rejects is dropped with a
// notice by the backend enum-guard. Labels map raw values to friendlier option
// text — 'auto' reads better than a bare token.
const defaultVideoDurationOptions = ['auto', '5', '10', '15', '30'];

// The duration picker's ladder always reaches 30s even when the selected
// model's published schema caps lower: modern models generate 30s clips while
// their schemas lag, and the picker must not hide the option. A value the
// model truly rejects is dropped with a notice by the backend's enum/bounds
// guard (videoDurationSendable + videoDurationDroppedNotice), so the wider
// ladder never 422s a generation. Picker-only — the "Supported clip lengths"
// hovers keep reading the raw schema set.
const maxSelectableVideoDuration = 30;
function videoDurationPickerOptions(options: string[]): string[] {
  let max = 0;
  for (const option of options) {
    const n = Number(option);
    if (option !== '' && Number.isFinite(n) && String(n) === option.trim()) max = Math.max(max, n);
  }
  if (max <= 0 || max >= maxSelectableVideoDuration) return options;
  const widened = [...options];
  for (let v = Math.floor(max) + 1; v <= maxSelectableVideoDuration; v++) widened.push(String(v));
  return widened;
}

// formatSupportedDurations turns a fal duration enum (e.g. ['auto','4','5',…,'15'])
// into a compact caption like "auto, 4–15 s": non-numeric values (auto) are kept
// in order, consecutive integer runs are collapsed into ranges, and a trailing
// " s" is added when any numeric value is present.
function formatSupportedDurations(options: string[]): string {
  const nonNumeric: string[] = [];
  const numbers: number[] = [];
  for (const opt of options) {
    const n = Number(opt);
    if (opt !== '' && Number.isFinite(n) && String(n) === opt.trim()) numbers.push(n);
    else if (opt) nonNumeric.push(opt);
  }
  numbers.sort((a, b) => a - b);
  const ranges: string[] = [];
  for (let i = 0; i < numbers.length; ) {
    let j = i;
    while (j + 1 < numbers.length && numbers[j + 1] === numbers[j] + 1) j++;
    ranges.push(i === j ? String(numbers[i]) : `${numbers[i]}–${numbers[j]}`);
    i = j + 1;
  }
  const parts = [...nonNumeric, ...ranges];
  if (parts.length === 0) return '';
  return parts.join(', ') + (numbers.length ? ' s' : '');
}
const videoDurationLabels: Record<string, string> = { auto: 'Auto' };
// 'auto' sends no duration and the sound model picks the clip length from the
// prompt (elevenlabs' documented null behavior) — the pre-setting default, so
// the setting changes nothing until the user opts into a fixed length. The
// fallback ladder tops out at 22s, elevenlabs sound-effects' ceiling; each
// picker fetches the selected model's own range via ListFalSoundEffectDurations.
const defaultSoundDuration = 'auto';
const defaultSoundDurationOptions = ['auto', '5', '10', '15', '22'];
const soundDurationLabels: Record<string, string> = { auto: 'Auto (model decides)' };
// 'auto' keeps each extend model's own behavior (the inpaint/outpaint models'
// built-in 30s; sonauto decides from the audio). Static ladder — the extend
// models' length inputs are flavor-specific masks/side fields with no shared
// schema key to read, and each flavor validates its own cap (sonauto clamps
// 2–85s, ace-step caps at 107s, the inpaint model errors past 190s total).
const defaultAudioExtendDuration = 'auto';
const audioExtendDurationOptions = ['auto', '10', '20', '30', '60', '90', '120'];
const audioExtendDurationLabels: Record<string, string> = { auto: 'Auto (model default)' };
// Mirrors Go's defaultMediaGenerationTimeoutSeconds (900s) — the Settings input
// edits minutes, the config field stores seconds.
const defaultMediaTimeoutSeconds = 900;
// 'auto' defers the output shape to the model (its aspect_ratio default) —
// reference-guided and text-to-video turns read this setting; image-to-video
// and extend inherit the attached media's orientation. Label maps the raw
// value to friendlier option text, like videoDurationLabels.
const videoAspectRatioOptions = ['16:9', '9:16', '1:1', 'auto'];
const videoAspectRatioLabels: Record<string, string> = { auto: 'Auto (model decides)' };

// Coerce a numeric settings input to a positive integer, falling back to the
// backend default when the field is cleared or otherwise invalid. Mirrors the
// `<= 0` fallback merge in app.go's mergeAppConfig.
function positiveIntOrDefault(value: string, fallback: number): number {
  const parsed = Number(value);
  return Number.isInteger(parsed) && parsed > 0 ? parsed : fallback;
}

export type {ImageSizePreset};
export {
  defaultBaseURL,
  defaultOllamaNumCtx,
  projectNotesMaxLength,
  numCtxOptions,
  numCtxLabel,
  defaultImageAspectRatio,
  defaultImageSizePreset,
  defaultImageSteps,
  imageAspectRatioOptions,
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
  defaultRunwareVideoModel,
  defaultRunwareVideoImageModel,
  defaultRunwareVideoExtendModel,
  defaultRunwareVideoUpscaleModel,
  defaultRunwareVideoRestyleModel,
  defaultRunwareUpscaleModel,
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
  videoResolutionOptions,
  videoResolutionLabels,
  defaultVideoDurationOptions,
  maxSelectableVideoDuration,
  videoDurationPickerOptions,
  formatSupportedDurations,
  videoDurationLabels,
  defaultSoundDuration,
  defaultSoundDurationOptions,
  soundDurationLabels,
  defaultAudioExtendDuration,
  audioExtendDurationOptions,
  audioExtendDurationLabels,
  defaultMediaTimeoutSeconds,
  videoAspectRatioOptions,
  videoAspectRatioLabels,
  positiveIntOrDefault,
};
