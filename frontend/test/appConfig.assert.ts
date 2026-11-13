import type {main} from '../wailsjs/go/models';
import {appConfigPayload, configValues} from '../src/appConfig';

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function same(actual: unknown, expected: unknown, message: string): void {
  const got = JSON.stringify(actual);
  const want = JSON.stringify(expected);
  if (got !== want) throw new Error(`${message}: expected ${want}, got ${got}`);
}

// GetConfig returns generated classes; the mapping reads only their fields,
// so the tests hand it plain objects in the wire shape.
function asConfig(value: unknown): main.AppConfig {
  return value as main.AppConfig;
}

const keys = {openRouterHasKey: true, falHasKey: false, replicateHasKey: true, runwareHasKey: true};

// An empty config (first launch) fills every field's default.
const defaults = configValues(asConfig({}));
same(defaults.baseURL, 'http://localhost:11434', 'Ollama base URL default');
same(defaults.openaiCompatibleBaseURL, 'http://localhost:8080', 'OpenAI-compatible base URL default');
same([defaults.primaryProvider, defaults.harnessProvider], ['ollama', 'ollama'], 'chat providers default to Ollama');
same(defaults.imageProvider, 'openai-compatible', 'image provider default');
same([defaults.videoProvider, defaults.speechProvider, defaults.transcriptionProvider], ['fal', 'fal', 'fal'], 'media providers default to fal');
same(defaults.system, 'You are Atelier, a precise local AI collaborator.', 'system prompt default');
same(defaults.mediaTimeoutMinutes, 15, 'media timeout default is 900s');
same([defaults.whisperChunkMinutes, defaults.videoResolution], ['', ''], 'unset chunking and resolution stay empty');
same([defaults.storageConfig, defaults.toolConfig, defaults.updatesConfig], [null, null, null], 'absent sections load as null');
assert(!defaults.airplaneMode, 'airplane mode defaults off');

// Provider ids are validated: an unknown or retired id falls back.
const odd = configValues(asConfig({models: {primaryProvider: 'gemini', imageProvider: 'ollama', speechProvider: 'ollama', transcriptionProvider: 'whisper.cpp'}}));
same(odd.primaryProvider, 'ollama', 'an unknown chat provider falls back to Ollama');
same(odd.imageProvider, 'openai-compatible', 'the retired Ollama image provider loads as openai-compatible');
same(odd.speechProvider, 'ollama', 'Ollama is a valid speech provider');
same(odd.transcriptionProvider, 'fal', 'an unknown transcription provider falls back to fal');

// The harness model inherits the primary when unset; the timeout rounds to
// whole minutes, never below one; a chunk size of 0 means chunking is off.
same(configValues(asConfig({providers: {ollama: {models: {primary: 'qwen3'}}}})).harnessModels.ollama, 'qwen3', 'the harness inherits the primary model');
same(configValues(asConfig({generation: {mediaTimeoutSeconds: 20}})).mediaTimeoutMinutes, 1, 'a sub-minute timeout rounds up to one minute');
same(configValues(asConfig({generation: {mediaTimeoutSeconds: 1250}})).mediaTimeoutMinutes, 21, 'timeouts round to whole minutes');
same(configValues(asConfig({providers: {local: {whisper: {chunkMinutes: 0}}}})).whisperChunkMinutes, '', 'chunk size 0 is off');

// The round trip: a config with every field set away from its default must
// survive save -> load unchanged. A field written under one key but read from
// another would reset on every restart.
const fal = Object.fromEntries([
  'model', 'imageEditModel', 'videoModel', 'videoImageModel', 'videoKeyframeModel', 'videoExtendModel',
  'videoMotionModel', 'videoUpscaleModel', 'videoReframeModel', 'videoRestyleModel', 'audioModel',
  'soundEffectsModel', 'soundEffectsVideoModel', 'voiceDesignModel', 'audioCloneModel', 'audioExtendModel',
  'transcribeModel', 'upscaleModel', 'inpaintModel', 'lipsyncImageModel', 'lipsyncVideoModel',
].map((field) => [field, `fal-ai/custom/${field}`]));
const replicate = Object.fromEntries([
  'model', 'imageEditModel', 'upscaleModel', 'inpaintModel', 'videoUpscaleModel', 'videoRestyleModel',
  'videoReframeModel', 'videoExtendModel', 'videoModel', 'videoImageModel', 'transcribeModel',
  'lipsyncImageModel', 'lipsyncVideoModel', 'soundEffectsModel', 'soundEffectsVideoModel', 'audioExtendModel',
  'speechModel',
].map((field) => [field, `owner/custom-${field}`]));
const full = asConfig({
  airplaneMode: true,
  storage: {root: '/data/atelier'},
  tools: {workspaceRoot: '/work'},
  updates: {autoCheck: false, manifestUrl: 'https://example.invalid/manifest.json'},
  providers: {
    ollama: {baseURL: 'http://ollama.lan:11434', numCtx: 32768, models: {primary: 'qwen3:32b', harness: 'qwen3:8b', image: 'x/flux', speech: 'phonon-en'}},
    openrouter: {primary: 'anthropic/claude', harness: 'openai/gpt-5-mini'},
    openaiCompatible: {baseURL: 'http://localai.lan:8080', primary: 'local-chat', harness: 'local-tools', model: 'sd-xl'},
    fal,
    replicate,
    runware: {
      model: 'civitai:4201@130072',
      videoModel: 'klingai:kling-video@9.9', videoImageModel: 'klingai:kling-video@9.8',
      videoExtendModel: 'google:3@1', videoUpscaleModel: 'bfl:flux@video-upscale-9',
      videoRestyleModel: 'bfl:flux@video-edit-9', inpaintModel: 'bfl:9@9',
      lipsyncImageModel: 'veed:fabric@9', lipsyncVideoModel: 'klingai:9@9',
      speechModel: 'minimax:speech@9.9', transcribeModel: 'google:gemini@9.9',
      soundEffectsModel: 'mirelo:9@9',
      soundEffectsVideoModel: 'mirelo:9@8',
    },
    local: {
      whisper: {binary: '/opt/whisper', model: '~/models/ggml-large.bin', vadModel: '~/models/silero.bin', chunkMinutes: 20},
      ffmpeg: {binary: '/opt/ffmpeg'},
      ffprobe: {binary: '/opt/ffprobe'},
      sips: {binary: '/usr/local/bin/sips'},
      magick: {binary: '/opt/magick'},
    },
  },
  models: {
    primaryProvider: 'openrouter', harnessProvider: 'openai-compatible', imageProvider: 'replicate', videoProvider: 'replicate',
    inpaintProvider: 'replicate', lipsyncProvider: 'replicate', speechProvider: 'ollama', audioProvider: 'replicate',
    transcriptionProvider: 'local-whisper',
  },
  prompts: {system: 'Be terse.'},
  generation: {
    image: {aspectRatio: '16:9', sizePreset: '2k', steps: 30},
    video: {duration: '10', aspectRatio: '9:16', resolution: '1080p'},
    audio: {duration: '15', extendDuration: '60'},
    mediaTimeoutSeconds: 1200,
  },
});
const values = configValues(full);
// The fixture must move every field off its default, or the round trip below
// can't see that field's mapping; a field added to configValues without a
// fixture value fails here.
for (const [field, value] of Object.entries(values)) {
  assert(JSON.stringify(value) !== JSON.stringify((defaults as Record<string, unknown>)[field]), `the round-trip fixture sets ${field}`);
}
same(configValues(asConfig(appConfigPayload({...values, ...keys}))), values, 'every setting survives save -> load');

// The payload's own shape: the key flags ride as each provider's enabled bit,
// an automatic resolution is omitted, and chunking off saves as 0.
const payload = appConfigPayload({...values, ...keys});
same([payload.providers.openrouter.enabled, payload.providers.fal.enabled, payload.providers.replicate.enabled, payload.providers.runware.enabled], [true, false, true, true], 'key flags become enabled bits');
const autoPayload = appConfigPayload({...defaults, ...keys});
assert(autoPayload.generation.video.resolution === undefined, 'an automatic video resolution is omitted');
same(autoPayload.providers.local.whisper.chunkMinutes, 0, 'chunking off saves as 0');
same(autoPayload.generation.mediaTimeoutSeconds, 900, 'the timeout saves in seconds');
same([autoPayload.storage, autoPayload.tools, autoPayload.updates], [undefined, undefined, undefined], 'unloaded sections are omitted');
