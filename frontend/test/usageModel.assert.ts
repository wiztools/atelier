import type {ChatEntry, HarnessRunView} from '../src/types';
import type {MediaUsageRow} from '../src/usage/usageModel';
import {
  formatCostMicros,
  formatDuration,
  formatStepKind,
  formatTokenCount,
  formatToolName,
  harnessStepLane,
  mediaCountsLabel,
  mediaUsageFromToolSummary,
  parseHarnessRun,
  summarizeMediaUsage,
  summarizeModelUsage,
  summarizeRunLatency,
  summarizeRunMediaUsage,
  summarizeRunUsage,
} from '../src/usage/usageModel';

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function same(actual: unknown, expected: unknown, message: string): void {
  const got = JSON.stringify(actual);
  const want = JSON.stringify(expected);
  if (got !== want) throw new Error(`${message}: expected ${want}, got ${got}`);
}

function media(provider: string, model: string, counts: {video?: number; audio?: number; image?: number}, calls: number, costMicros: number, costUnknown = false): MediaUsageRow {
  return {provider, model, video: counts.video ?? 0, audio: counts.audio ?? 0, image: counts.image ?? 0, calls, costMicros, costUnknown};
}

// One turn's run, every lane exercised: harness prep on a local model, a tool
// round, the final response on a cloud model, and bookkeeping around it.
const run: HarnessRunView = {
  status: 'completed',
  steps: [
    {kind: 'queued', durationMs: 3},
    {kind: 'triage', provider: 'ollama', model: 'qwen3', promptTokens: 1200, tokens: 40, durationMs: 800},
    {kind: 'skill', provider: 'ollama', model: 'tiny', durationMs: 300},
    {kind: 'planning', provider: 'ollama', model: 'qwen3', promptTokens: 2400, tokens: 120, durationMs: 1500},
    {kind: 'tool_call', durationMs: 9000, tools: [
      {name: 'generate_video', provider: 'fal', model: 'fal-ai/kling-video', mediaKind: 'video', mediaCount: 1, costMicros: 350000, permissionWaitMs: 2000},
      {name: 'transcribe_audio', provider: 'fal', model: 'fal-ai/wizper', costMicros: 1200},
      {name: 'generate_video', status: 'failed', error: 'quota exhausted'},
      {name: 'generate_image', provider: 'replicate', model: 'black-forest-labs/flux-schnell', mediaKind: 'image', mediaCount: 2, costUnknown: true},
      {name: 'generate_image', provider: 'fal', model: 'fal-ai/flux/schnell', mediaKind: 'image', mediaCount: 1, costMicros: 3000},
    ]},
    {kind: 'model_call', provider: 'openrouter', model: 'claude', promptTokens: 5000, costMicros: 15000, durationMs: 600, firstTokenMs: 550},
    {kind: 'streaming', provider: 'openrouter', model: 'claude', tokens: 800, costMicros: 12000, durationMs: 4000},
    {kind: 'evaluation', provider: 'ollama', model: 'qwen3', promptTokens: 999, tokens: 9, durationMs: 50},
    {kind: 'saved', durationMs: 5},
  ],
};

// Token rows fold model-call steps only: the evaluation step's usage stays out
// so each call counts once, and a model that consumed nothing (the skill
// step) gets no row.
same(summarizeRunUsage(run), [
  {provider: 'ollama', model: 'qwen3', promptTokens: 3600, completionTokens: 160, calls: 2, costMicros: 0, durationMs: 2300},
  {provider: 'openrouter', model: 'claude', promptTokens: 5000, completionTokens: 800, calls: 2, costMicros: 27000, durationMs: 4600},
], 'one row per model, summed across its model-call steps');
same(summarizeRunUsage({steps: [{kind: 'document', model: 'writer', tokens: 50}]}), [
  {provider: '—', model: 'writer', promptTokens: 0, completionTokens: 50, calls: 1, costMicros: 0, durationMs: 0},
], 'a step without a provider reads as —');
same(summarizeRunUsage(undefined), [], 'no run, no rows');

// Latency lanes are the pipeline's sequential phases; tool time includes the
// approval stall, which is also surfaced on its own.
same(summarizeRunLatency(run), {harnessMs: 2600, toolsMs: 9000, responseMs: 4600, permissionWaitMs: 2000, firstTokenMs: 550},
  'lanes fold the phases and leave bookkeeping out');
assert(summarizeRunLatency({status: 'completed', steps: [{kind: 'triage'}]}) === undefined, 'an untimed run has no lanes');
assert(summarizeRunLatency(undefined) === undefined, 'no run, no lanes');

// Media rows count outputs by kind. A failed call carries no model and drops
// out; a priced call with no media (fal transcription) joins with its cost so
// the rows reconcile with the turn's money total; an unpriceable Replicate
// call marks its row.
same(summarizeRunMediaUsage(run), [
  media('fal', 'fal-ai/kling-video', {video: 1}, 1, 350000),
  media('fal', 'fal-ai/wizper', {}, 1, 1200),
  media('replicate', 'black-forest-labs/flux-schnell', {image: 2}, 1, 0, true),
  media('fal', 'fal-ai/flux/schnell', {image: 1}, 1, 3000),
], 'media rows per generation model');

// Conversation folds merge the assistant turns' runs; a user entry never
// counts, even with a run attached.
const secondRun: HarnessRunView = {status: 'completed', steps: [
  {kind: 'model_call', provider: 'openrouter', model: 'claude', promptTokens: 1000, tokens: 200, costMicros: 3000, durationMs: 900},
]};
const chat: ChatEntry[] = [
  {id: 'u1', role: 'user', content: 'make a video', harnessRun: run},
  {id: 'a1', role: 'assistant', content: 'done', harnessRun: run, mediaTool: {name: 'video_generation', model: 'fal-ai/kling-video', videoCount: 1}},
  {id: 'a2', role: 'assistant', content: 'again', harnessRun: secondRun},
  {id: 'a3', role: 'assistant', content: 'legacy turn', mediaTool: {name: 'video_generation', model: 'fal-ai/kling-video', videoCount: 1, imageCount: 1}},
];
same(summarizeModelUsage(chat), [
  {provider: 'ollama', model: 'qwen3', promptTokens: 3600, completionTokens: 160, calls: 2, costMicros: 0, durationMs: 2300},
  {provider: 'openrouter', model: 'claude', promptTokens: 6000, completionTokens: 1000, calls: 3, costMicros: 30000, durationMs: 5500},
], 'assistant runs merge per model');

// A turn counts through its run's activities or, for turns saved before those
// carried media fields, its legacy tool summary — never both (a1 has both).
const conversationMedia = summarizeMediaUsage(chat);
const mediaFor = (model: string) => conversationMedia.find((row) => row.model === model);
same(mediaFor('fal-ai/kling-video'), media('fal', 'fal-ai/kling-video', {video: 2, image: 1}, 2, 350000), 'run and legacy turns merge without double counting');
same(mediaFor('black-forest-labs/flux-schnell'), media('replicate', 'black-forest-labs/flux-schnell', {image: 2}, 1, 0, true), 'an unpriceable row stays marked');

// Legacy summaries never recorded the backend: video and audio were fal-only,
// and an image model outside the fal-ai/ namespace ran locally.
same(mediaUsageFromToolSummary({name: 'image_generation', model: 'fal-ai/flux/schnell', imageCount: 2}), [media('fal', 'fal-ai/flux/schnell', {image: 2}, 1, 0)], 'a legacy fal image turn');
same(mediaUsageFromToolSummary({name: 'image_generation', model: 'x/flux-local', imageCount: 1})[0].provider, 'ollama', 'a non-fal legacy image model ran locally');
same(mediaUsageFromToolSummary({name: 'audio_generation', model: 'fal-ai/elevenlabs/tts', audioCount: 1}), [media('fal', 'fal-ai/elevenlabs/tts', {audio: 1}, 1, 0)], 'legacy audio was fal');
same(mediaUsageFromToolSummary({name: 'web_search', model: 'x'}), [], 'only the media families have a legacy fold');
same(mediaUsageFromToolSummary(undefined), [], 'no summary, no rows');

// A persisted or streamed run snapshot counts only with a status or a step.
assert(parseHarnessRun(null) === undefined, 'null is not a run');
assert(parseHarnessRun('run') === undefined, 'a string is not a run');
assert(parseHarnessRun({steps: []}) === undefined, 'a run needs a status or at least one step');
const live = {status: 'running'};
assert(parseHarnessRun(live) === live, 'a status makes a run');
assert(parseHarnessRun({steps: [{kind: 'triage'}]}) !== undefined, 'a step makes a run');

same(harnessStepLane({kind: 'triage'}), {label: 'Chat model', className: 'harness-lane-chat'}, 'triage lands in the chat-model lane');
same(harnessStepLane({kind: 'planning'}), {label: 'Tool model', className: 'harness-lane-model'}, 'planning lands in the tool-model lane');
same(harnessStepLane({kind: 'tool_call'}), {label: 'Tools', className: 'harness-lane-tools'}, 'tool rounds have their own lane');
same(harnessStepLane({kind: 'document'}), {label: 'Chat model', className: 'harness-lane-chat'}, 'document authoring lands in the chat-model lane');
same(harnessStepLane({kind: 'saved'}), {label: 'Harness bookkeeping', className: 'harness-lane-bookkeeping'}, 'saves are bookkeeping');
same(harnessStepLane({kind: 'queued'}), {label: 'Harness', className: 'harness-lane-system'}, 'anything else is the harness itself');

same(formatTokenCount(999), '999', 'counts under 1000 stay whole');
same(formatTokenCount(12345), '12.3k', 'thousands read with one decimal');
// Calls range from fractions of a cent to dollars; two decimals would round a
// $0.004 turn to "$0.00".
same(formatCostMicros(undefined), '', 'no cost renders nothing');
same(formatCostMicros(0), '', 'zero cost renders nothing');
same(formatCostMicros(4000), '$0.00400', 'sub-cent costs keep five decimals');
same(formatCostMicros(27000), '$0.027', 'cents keep three decimals');
same(formatCostMicros(2500000), '$2.50', 'dollars keep two');
same(formatDuration(850), '850ms', 'sub-second durations in ms');
same(formatDuration(12345), '12.3s', 'longer durations in seconds');
same(formatStepKind('model_call'), 'model call', 'step kinds read with spaces');
same(formatStepKind(), 'step', 'a missing kind reads step');
same(formatToolName('generate_video'), 'generate video', 'tool names read with spaces');
same(formatToolName(), 'tool', 'a missing name reads tool');
same(mediaCountsLabel(1, 0, 2), '1 video · 2 images', 'counts join with a dot, pluralized');
same(mediaCountsLabel(2, 1, 0), '2 videos · 1 audio clip', 'zero kinds are omitted');
same(mediaCountsLabel(0, 0, 0), '', 'nothing produced, no label');
