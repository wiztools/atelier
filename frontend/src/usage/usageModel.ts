// Pure usage/telemetry model: folds harness runs and chat history into
// token, latency, and media-generation rows, plus the formatters the usage
// views render them with. Pure functions over the run/history shapes — no
// React, no bindings.

import {MODEL_CALL_STEP_KINDS, ChatEntry, HarnessRunView, HarnessStepView, MediaToolSummaryView} from '../types';
import {asArray} from '../utils';

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
  costMicros: number;
  // Wall time of this model's calls. The two final-response step kinds are
  // sequential, not overlapping — model_call ends when the provider stream
  // opens, streaming covers the body after it — so their durations may be
  // summed; deduping them would halve the response time.
  durationMs: number;
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
    const row = byModel.get(key) ?? {provider, model: step.model, promptTokens: 0, completionTokens: 0, calls: 0, costMicros: 0, durationMs: 0};
    row.promptTokens += step.promptTokens ?? 0;
    row.completionTokens += step.tokens ?? 0;
    row.costMicros += step.costMicros ?? 0;
    row.durationMs += step.durationMs ?? 0;
    row.calls += 1;
    byModel.set(key, row);
  }
  return [...byModel.values()].filter((row) => row.promptTokens > 0 || row.completionTokens > 0 || row.costMicros > 0);
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
      const merged = byModel.get(key) ?? {...row, promptTokens: 0, completionTokens: 0, calls: 0, costMicros: 0, durationMs: 0};
      merged.promptTokens += row.promptTokens;
      merged.completionTokens += row.completionTokens;
      merged.costMicros += row.costMicros;
      merged.calls += row.calls;
      merged.durationMs += row.durationMs;
      byModel.set(key, merged);
    }
  }
  return [...byModel.values()];
}

// Where one turn's wall time went, folded from the run's steps. The lanes are
// the pipeline's sequential phases — harness-model prep (triage + skill +
// planning), tool rounds, final response (model_call + streaming, which are
// themselves sequential) — so they roughly compose into the run's total;
// bookkeeping steps (queued/saved/evaluation) are excluded. Tool time already
// includes permission waits, which are surfaced separately too so an approval
// stall reads as such rather than as a slow tool.
type RunLatencyLanes = {
  harnessMs: number;
  toolsMs: number;
  responseMs: number;
  permissionWaitMs: number;
  firstTokenMs: number;
};

function summarizeRunLatency(run?: HarnessRunView): RunLatencyLanes | undefined {
  if (!run) {
    return undefined;
  }
  const lanes = {harnessMs: 0, toolsMs: 0, responseMs: 0, permissionWaitMs: 0, firstTokenMs: 0};
  let timed = false;
  for (const step of asArray(run.steps)) {
    const duration = step.durationMs ?? 0;
    if (duration > 0) {
      timed = true;
    }
    switch (step.kind) {
      case 'triage':
      case 'skill':
      case 'planning':
        lanes.harnessMs += duration;
        break;
      case 'tool_call':
        lanes.toolsMs += duration;
        for (const tool of asArray(step.tools)) {
          lanes.permissionWaitMs += tool.permissionWaitMs ?? 0;
        }
        break;
      case 'model_call':
      case 'streaming':
      case 'document':
        lanes.responseMs += duration;
        if (step.firstTokenMs) {
          lanes.firstTokenMs = Math.max(lanes.firstTokenMs, step.firstTokenMs);
        }
        break;
      default:
        break;
    }
  }
  return timed ? lanes : undefined;
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
  costMicros: number;
  // True when any activity in the row was paid but unpriceable (Replicate):
  // the row renders "?" beside the counts and the totals render "~".
  costUnknown: boolean;
};

// Per-model media generation for a single run, folded from tool_call step
// activities. Only completed media calls carry model/mediaKind/mediaCount, so
// failed calls drop out of the fold instead of counting phantom consumption.
// Cost-carrying activities without a mediaKind (fal transcription) join too,
// and so do unpriceable ones (Replicate transcription) — rows then show a cost,
// or "?", with no output counts, keeping the fold's rows reconcilable with the
// turn's money total. usage_report.go keeps exactly the same rows.
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
      const cost = tool.costMicros ?? 0;
      if (!tool.model || (kind !== 'video' && kind !== 'audio' && kind !== 'image' && cost <= 0 && !tool.costUnknown)) {
        continue;
      }
      const provider = tool.provider || '—';
      const key = `${provider}|${tool.model}`;
      const row = byModel.get(key) ?? {provider, model: tool.model, video: 0, audio: 0, image: 0, calls: 0, costMicros: 0, costUnknown: false};
      if (kind === 'video' || kind === 'audio' || kind === 'image') {
        row[kind] += tool.mediaCount ?? 0;
      }
      row.costMicros += cost;
      if (tool.costUnknown) {
        row.costUnknown = true;
      }
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
  const row: MediaUsageRow = {provider: legacyMediaProvider(tool.name, tool.model), model: tool.model, video: 0, audio: 0, image: 0, calls: 0, costMicros: 0, costUnknown: false};
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
// recorded no media (old turns), so a turn is never counted through both. A
// row stays when it produced media or carries a cost, known or not: the
// conversation's money total sums these rows, so dropping a cost-only row
// (fal transcription) undercounted spend — usage_report.go keeps the same rows.
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
      const merged = byModel.get(key) ?? {...row, video: 0, audio: 0, image: 0, calls: 0, costMicros: 0, costUnknown: false};
      merged.video += row.video;
      merged.audio += row.audio;
      merged.image += row.image;
      merged.calls += row.calls;
      merged.costMicros += row.costMicros;
      if (row.costUnknown) {
        merged.costUnknown = true;
      }
      byModel.set(key, merged);
    }
  }
  return [...byModel.values()].filter((row) => row.video + row.audio + row.image > 0 || row.costMicros > 0 || row.costUnknown);
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

// Cost in USD millionths → a dollar string with decimals scaled to the
// magnitude: model/media calls range from fractions of a cent to a few
// dollars, and two decimals would round a $0.004 turn to "$0.00".
function formatCostMicros(micros?: number): string {
  if (!micros || micros <= 0) {
    return '';
  }
  const dollars = micros / 1e6;
  const decimals = dollars >= 1 ? 2 : dollars >= 0.01 ? 3 : 5;
  return `$${dollars.toFixed(decimals)}`;
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
    case 'document':
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

export type {ModelUsageRow, RunLatencyLanes, MediaUsageRow};
export {
  parseHarnessRun,
  summarizeRunUsage,
  summarizeModelUsage,
  summarizeRunLatency,
  summarizeRunMediaUsage,
  mediaUsageFromToolSummary,
  legacyMediaProvider,
  summarizeMediaUsage,
  mediaCountsLabel,
  formatTokenCount,
  formatCostMicros,
  harnessStepLane,
  formatStepKind,
  formatToolName,
  formatDuration,
};
