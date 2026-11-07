import {HarnessRunView} from '../types';
import {
  summarizeRunUsage,
  summarizeRunLatency,
  summarizeRunMediaUsage,
  mediaCountsLabel,
  formatTokenCount,
  formatCostMicros,
  formatDuration,
  ModelUsageRow,
  MediaUsageRow,
} from './usageModel';

// Per-turn token footer: the turn's total in/out (plus duration once known)
// is always visible in the summary; expanding lists the per-model breakdown.
// Renders nothing when the run's providers reported no usage (e.g. fal media
// generation) — failed turns keep theirs, matching the persisted ledger.
function TurnUsage({run}: {run: HarnessRunView}) {
  const usage = summarizeRunUsage(run);
  const latency = summarizeRunLatency(run);
  if (!usage.length && !latency) {
    return null;
  }
  // Media generation burns no tokens, so its cost would otherwise be missing
  // from a video/audio-only turn's footer; the harness panel shows the
  // per-model breakdown (tokens and media rows both). "~" marks a total that
  // includes unpriceable (Replicate) media.
  const mediaRows = summarizeRunMediaUsage(run);
  const mediaCost = mediaRows.reduce((sum, row) => sum + row.costMicros, 0);
  const anyCostUnknown = mediaRows.some((row) => row.costUnknown);
  const totals = usage.reduce(
    (acc, row) => ({prompt: acc.prompt + row.promptTokens, completion: acc.completion + row.completionTokens, cost: acc.cost + row.costMicros}),
    {prompt: 0, completion: 0, cost: mediaCost},
  );
  return (
    <details className="turn-usage">
      <summary>
        {[
          totals.prompt ? `${formatTokenCount(totals.prompt)} in` : '',
          totals.completion ? `${formatTokenCount(totals.completion)} out` : '',
          formatCostMicros(totals.cost)
            ? `${anyCostUnknown ? '~' : ''}${formatCostMicros(totals.cost)}`
            : (anyCostUnknown ? '~?' : ''),
          run.durationMs ? formatDuration(run.durationMs) : '',
        ]
          .filter(Boolean)
          .join(' · ')}
      </summary>
      <div className="turn-usage-rows">
        {latency ? (
          <div
            className="turn-latency"
            title="Where this turn's time went — the phases run in sequence; brief bookkeeping between them is not counted."
          >
            {latency.harnessMs ? (
              <span title="Triage, skill selection, and planning calls on the harness model.">harness {formatDuration(latency.harnessMs)}</span>
            ) : null}
            {latency.toolsMs ? (
              <span title="Tool execution — commands and local media tools (ffmpeg, whisper, …) — including time spent waiting for approvals.">tools {formatDuration(latency.toolsMs)}</span>
            ) : null}
            {latency.responseMs ? (
              <span title="The final response: opening the provider stream and streaming it to completion.">response {formatDuration(latency.responseMs)}</span>
            ) : null}
            {latency.firstTokenMs ? (
              <span title="Time from the response stream opening to its first visible output.">first token {formatDuration(latency.firstTokenMs)}</span>
            ) : null}
            {latency.permissionWaitMs ? (
              <span title="Time tools spent waiting for your approval — counted within tools above.">approval wait {formatDuration(latency.permissionWaitMs)}</span>
            ) : null}
          </div>
        ) : null}
        {usage.map((row) => (
          <div className="harness-usage-row" key={`${row.provider}-${row.model}`} title={`${row.provider} · ${row.calls} call${row.calls === 1 ? '' : 's'} in this turn`}>
            <span className="harness-usage-model">{row.model}</span>
            <span>
              {row.promptTokens ? `${formatTokenCount(row.promptTokens)} in` : '— in'}
              {' · '}
              {row.completionTokens ? `${formatTokenCount(row.completionTokens)} out` : '— out'}
              {formatCostMicros(row.costMicros) ? ` · ${formatCostMicros(row.costMicros)}` : ''}
              {row.durationMs ? <span title="wall time of this model's calls in this turn">{` · ${formatDuration(row.durationMs)}`}</span> : ''}
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
  const totalCost = usage.reduce((sum, row) => sum + row.costMicros, 0) + media.reduce((sum, row) => sum + row.costMicros, 0);
  // "~" marks a total with unpriceable (Replicate) media in it: the sum covers
  // only the costs that were reported.
  const anyMediaCostUnknown = media.some((row) => row.costUnknown);
  const costLabel = formatCostMicros(totalCost)
    ? `${anyMediaCostUnknown ? '~' : ''}${formatCostMicros(totalCost)}`
    : (anyMediaCostUnknown ? '~?' : '');
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
        {costLabel ? ` · ${costLabel}` : ''}
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
                {formatCostMicros(row.costMicros) ? ` · ${formatCostMicros(row.costMicros)}` : ''}
                {row.durationMs ? <span title="wall time of this model's calls across this conversation">{` · ${formatDuration(row.durationMs)}`}</span> : ''}
              </span>
            </div>
          ))}
          {media.map((row) => (
            <div className="harness-usage-row harness-usage-media" key={`media-${row.provider}-${row.model}`} title={`${row.provider} · media generation · ${row.calls} call${row.calls === 1 ? '' : 's'} across this conversation`}>
              <span className="harness-usage-model">
                {row.provider !== '—' ? <span className="harness-usage-provider">{row.provider}</span> : null}
                {row.model}
              </span>
              <span>
                {mediaCountsLabel(row.video, row.audio, row.image) || '—'}
                {row.costUnknown
                  ? ' · ?'
                  : formatCostMicros(row.costMicros) ? ` · ${formatCostMicros(row.costMicros)}` : ''}
              </span>
            </div>
          ))}
        </div>
      ) : null}
    </details>
  );
}

export {TurnUsage, ConversationUsage};
