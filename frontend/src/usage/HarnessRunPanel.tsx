// HarnessRunPanel: the expandable per-turn harness telemetry — every step's
// kind, model, timing, token/cost usage, and tool activity.

import {HarnessRunView} from '../types';
import {asArray, shortenHomePath} from '../utils';
import {
  summarizeRunUsage,
  summarizeRunMediaUsage,
  mediaCountsLabel,
  formatTokenCount,
  formatCostMicros,
  harnessStepLane,
  formatStepKind,
  formatToolName,
  formatDuration,
} from './usageModel';

function HarnessRunPanel({run}: {run: HarnessRunView}) {
  const steps = asArray(run.steps);
  const usage = summarizeRunUsage(run);
  const media = summarizeRunMediaUsage(run);
  const completed = steps.filter((step) => step.status === 'completed').length;
  const status = run.status ?? 'running';
  const stopReason = run.loop?.stopReason;
  const totals = usage.reduce(
    (acc, row) => ({prompt: acc.prompt + row.promptTokens, completion: acc.completion + row.completionTokens, cost: acc.cost + row.costMicros}),
    {prompt: 0, completion: 0, cost: 0},
  );
  const mediaTotals = media.reduce(
    (acc, row) => ({video: acc.video + row.video, audio: acc.audio + row.audio, image: acc.image + row.image, cost: acc.cost + row.costMicros}),
    {video: 0, audio: 0, image: 0, cost: 0},
  );
  const mediaLabel = mediaCountsLabel(mediaTotals.video, mediaTotals.audio, mediaTotals.image);
  // "~" marks a run total with unpriceable (Replicate) media in it — the sum
  // covers only the costs that were reported.
  const anyCostUnknown = media.some((row) => row.costUnknown);
  const runCost = formatCostMicros(totals.cost + mediaTotals.cost)
    ? `${anyCostUnknown ? '~' : ''}${formatCostMicros(totals.cost + mediaTotals.cost)}`
    : (anyCostUnknown ? '~?' : '');
  // Prefix-cache signal: compare each model-call step's promptHash against the
  // previous request to the same provider+model. Equal hashes kept the cache
  // warm; a change (marked) invalidated it.
  const prefixChanged = new Set<number>();
  const lastHashByModel = new Map<string, string>();
  steps.forEach((step, index) => {
    const hash = step.request?.promptHash;
    if (!hash) {
      return;
    }
    const key = `${step.provider ?? ''}|${step.model ?? ''}`;
    const previous = lastHashByModel.get(key);
    if (previous && previous !== hash) {
      prefixChanged.add(index);
    }
    lastHashByModel.set(key, hash);
  });
  return (
    <details className="harness-panel">
      <summary>
        <span>Harness</span>
        <strong>{status}</strong>
        <small>
          {completed}/{steps.length} steps{run.durationMs ? ` · ${formatDuration(run.durationMs)}` : ''}
          {totals.prompt || totals.completion ? ` · ${formatTokenCount(totals.prompt + totals.completion)} tokens` : ''}
          {mediaLabel ? ` · ${mediaLabel}` : ''}
          {runCost ? ` · ${runCost}` : ''}
        </small>
      </summary>
      <div className="harness-meta">
        {run.loop?.iterations ? <span>{run.loop.iterations} iteration{run.loop.iterations === 1 ? '' : 's'}</span> : null}
        {stopReason ? <span>stop: {stopReason}</span> : null}
        {run.requestId ? <span>{run.requestId}</span> : null}
      </div>
      {usage.length ? (
        <div className="harness-usage">
          {usage.map((row) => (
            <div className="harness-usage-row" key={`${row.provider}-${row.model}`} title={`${row.provider} · ${row.calls} call${row.calls === 1 ? '' : 's'} in this run`}>
              <span className="harness-usage-model">{row.model}</span>
              <span>
                {row.promptTokens ? `${formatTokenCount(row.promptTokens)} in` : '— in'}
                {' · '}
                {row.completionTokens ? `${formatTokenCount(row.completionTokens)} out` : '— out'}
                {formatCostMicros(row.costMicros) ? ` · ${formatCostMicros(row.costMicros)}` : ''}
                {row.durationMs ? ` · ${formatDuration(row.durationMs)}` : ''}
              </span>
            </div>
          ))}
        </div>
      ) : null}
      {media.length ? (
        <div className="harness-usage harness-usage-media">
          {media.map((row) => (
            <div className="harness-usage-row" key={`media-${row.provider}-${row.model}`} title={`${row.provider} · media generation · ${row.calls} call${row.calls === 1 ? '' : 's'} in this run`}>
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
      <ol className="harness-steps">
        {steps.map((step, index) => {
          const lane = harnessStepLane(step);
          const truncated = step.request?.truncatedMessages ?? 0;
          return (
          <li key={step.id ?? `${step.kind}-${index}`} className={`harness-step ${step.status ?? 'pending'} ${lane.className}`}>
            <div className="harness-step-head">
              <div>
                <strong>{formatStepKind(step.kind)}</strong>
                <em>{lane.label}</em>
              </div>
              <span>{step.status ?? 'pending'}</span>
            </div>
            <p>{step.error || step.summary || step.decision || step.doneReason || step.model || ''}</p>
            <small className="harness-step-meta">
              {step.provider ? <span>{step.provider}</span> : null}
              {step.model ? <span>{step.model}</span> : null}
              {step.promptTokens || step.tokens ? (
                <span>{[step.promptTokens ? `${formatTokenCount(step.promptTokens)} in` : '', step.tokens ? `${formatTokenCount(step.tokens)} out` : ''].filter(Boolean).join(' · ')}</span>
              ) : null}
              {formatCostMicros(step.costMicros) ? <span title="server-billed cost of this call">{formatCostMicros(step.costMicros)}</span> : null}
              {step.firstTokenMs ? <span title="time to first token">ttft {formatDuration(step.firstTokenMs)}</span> : null}
              {step.durationMs ? <span>{formatDuration(step.durationMs)}</span> : null}
              {truncated ? <span className="harness-flag-warn" title="oldest messages dropped to fit num_ctx">trimmed {truncated} msg{truncated === 1 ? '' : 's'}</span> : null}
              {step.promptTokens && step.request?.numCtx && step.promptTokens > step.request.numCtx ? (
                <span
                  className="harness-flag-warn"
                  title={`The prompt (${formatTokenCount(step.promptTokens)} tokens) exceeded the ${formatTokenCount(step.request.numCtx)}-token context window — Ollama truncated it from the FRONT, so instructions may have been dropped. Raise the Context Window in Settings → Providers.`}
                >
                  ctx overflow {formatTokenCount(step.promptTokens)}/{formatTokenCount(step.request.numCtx)}
                </span>
              ) : null}
              {step.request?.promptHash ? (
                <code className={prefixChanged.has(index) ? 'harness-hash harness-hash-changed' : 'harness-hash'} title={`prompt prefix hash${prefixChanged.has(index) ? ' — changed since this model\'s previous request, prefix cache invalidated' : ''}`}>#{step.request.promptHash}</code>
              ) : null}
            </small>
            {asArray(step.tools).length ? (
              <div className="harness-tool-list">
                {asArray(step.tools).map((tool, toolIndex) => (
                  <div className={`harness-tool ${tool.status ?? 'pending'}`} key={`${tool.name}-${toolIndex}`}>
                    <div>
                      <strong>{formatToolName(tool.name)}</strong>
                      <span>{tool.status ?? 'pending'}{typeof tool.exitCode === 'number' ? ` · exit ${tool.exitCode}` : ''}{tool.durationMs ? ` · ${formatDuration(tool.durationMs)}` : ''}{tool.costUnknown ? ' · est. ?' : formatCostMicros(tool.costMicros) ? ` · est. ${formatCostMicros(tool.costMicros)}` : ''}</span>
                    </div>
                    {tool.permission ? (
                      <span className={tool.permission === 'approved' ? 'harness-flag-ok' : 'harness-flag-warn'}>
                        permission {tool.permission}{tool.permissionWaitMs ? ` · waited ${formatDuration(tool.permissionWaitMs)}` : ''}
                      </span>
                    ) : null}
                    {tool.command?.length ? <code>{tool.command.join(' ')}</code> : null}
                    {tool.path ? <small>{shortenHomePath(tool.path)}</small> : null}
                    {tool.stdoutPreview ? <pre><strong>stdout</strong>{'\n'}{tool.stdoutPreview}</pre> : null}
                    {tool.stderrPreview ? <pre><strong>stderr</strong>{'\n'}{tool.stderrPreview}</pre> : null}
                    {tool.error ? <p>{tool.error}</p> : null}
                  </div>
                ))}
              </div>
            ) : null}
          </li>
        )})}
      </ol>
    </details>
  );
}

export {HarnessRunPanel};
