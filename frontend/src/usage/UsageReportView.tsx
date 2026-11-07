import {main} from '../../wailsjs/go/models';
import {asArray} from '../utils';
import {mediaCountsLabel, formatTokenCount, formatCostMicros} from './usageModel';

// The scope-level usage report (view === 'usage'): aggregated cost and
// tool-call telemetry folded from persisted history on the Go side. One stop
// per level — a conversation, a project (per-conversation child rows), or a
// library (per-project child rows) — and the child rows drill down one level,
// re-fetching at the narrower scope.
function UsageReportView({report, loading, error, onBack, onOpenChild}: {
  report: main.UsageReport | null;
  loading: boolean;
  error: string;
  onBack: () => void;
  onOpenChild: (child: main.UsageChildRow) => void;
}) {
  const scopeNoun = report?.scope === 'library' ? 'Library' : report?.scope === 'project' ? 'Project' : 'Conversation';
  const scopeLabel = report
    ? `${scopeNoun} report${report.title ? ` — ${report.title}` : ''}`
    : 'Usage report';
  // "~" marks a total with unpriceable media (Replicate) in it — the sum
  // covers only the costs that were reported.
  const costLabel = report
    ? (report.costUnknown ? '~' : '') + (formatCostMicros(report.costMicros) || '?')
    : '';
  const mediaLabel = report ? mediaCountsLabel(report.mediaVideos ?? 0, report.mediaAudios ?? 0, report.mediaImages ?? 0) : '';
  return (
    <>
      <div className="toolbar">
        <div className="toolbar-left">
          <button className="back-button" onClick={onBack}>← Back</button>
          <div className="model-count">{scopeLabel}</div>
        </div>
      </div>
      <div className="settings-screen usage-screen">
        <div className="settings-header">
          <h2>Usage</h2>
          <p>
            Cost and tool-call totals folded from this {scopeNoun.toLowerCase()}'s persisted turns — chat model calls,
            media generations, and editor operations alike. Media estimates come from the provider's own pricing;
            “~” marks a total that includes media whose cost could not be determined.
          </p>
        </div>
        {loading ? <div className="usage-note">Crunching the history…</div> : null}
        {error ? <div className="usage-note usage-error">{error}</div> : null}
        {report && !loading ? (
          <>
            <div className="usage-summary">
              <div className="usage-stat" title="Every reported cost in scope: model calls, media generations, editor operations">
                <span className="usage-stat-label">Cost</span>
                <span className="usage-stat-value">{costLabel || '—'}</span>
              </div>
              <div className="usage-stat" title="Model invocations: harness-model calls (triage, planning) plus the final responses">
                <span className="usage-stat-label">Model calls</span>
                <span className="usage-stat-value">{report.modelCalls}</span>
              </div>
              <div className="usage-stat" title={`Tool calls made${report.failedToolCalls ? ` — ${report.failedToolCalls} failed` : ''}`}>
                <span className="usage-stat-label">Tool calls</span>
                <span className="usage-stat-value">
                  {report.toolCalls}
                  {report.failedToolCalls ? <small className="usage-stat-failures"> {report.failedToolCalls} failed</small> : null}
                </span>
              </div>
              <div className="usage-stat" title="Assistant turns folded into this report">
                <span className="usage-stat-label">Turns</span>
                <span className="usage-stat-value">{report.turns}</span>
              </div>
              {report.scope !== 'conversation' ? (
                <div className="usage-stat" title="Conversations folded into this report">
                  <span className="usage-stat-label">Chats</span>
                  <span className="usage-stat-value">{report.conversations}</span>
                </div>
              ) : null}
              {mediaLabel ? (
                <div className="usage-stat" title="Media produced in scope">
                  <span className="usage-stat-label">Media</span>
                  <span className="usage-stat-value">{mediaLabel}</span>
                </div>
              ) : null}
            </div>
            {report.models.length ? (
              <div className="usage-section">
                <h3>Cost by model</h3>
                <table className="usage-table">
                  <thead>
                    <tr>
                      <th>Model</th>
                      <th className="usage-num">Calls</th>
                      <th className="usage-num">Tokens</th>
                      <th className="usage-num">Media</th>
                      <th className="usage-num">Cost</th>
                    </tr>
                  </thead>
                  <tbody>
                    {report.models.map((row) => (
                      <tr key={`${row.provider}-${row.model}`}>
                        <td>
                          <span className="usage-model-name">{row.model}</span>
                          {row.provider !== '—' ? <small className="usage-provider">{row.provider}</small> : null}
                        </td>
                        <td className="usage-num">{row.calls}</td>
                        <td className="usage-num">
                          {row.promptTokens || row.completionTokens
                            ? `${formatTokenCount(row.promptTokens ?? 0)} in / ${formatTokenCount(row.completionTokens ?? 0)} out`
                            : '—'}
                        </td>
                        <td className="usage-num">{mediaCountsLabel(row.mediaVideos ?? 0, row.mediaAudios ?? 0, row.mediaImages ?? 0) || '—'}</td>
                        <td className="usage-num">
                          {row.costUnknown && !formatCostMicros(row.costMicros) ? '?' : formatCostMicros(row.costMicros) || '—'}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : null}
            {report.tools.length ? (
              <div className="usage-section">
                <h3>Tool calls</h3>
                <table className="usage-table">
                  <thead>
                    <tr>
                      <th>Tool</th>
                      <th className="usage-num">Calls</th>
                      <th className="usage-num">Failed</th>
                      <th className="usage-num">Media produced</th>
                      <th className="usage-num">Cost</th>
                    </tr>
                  </thead>
                  <tbody>
                    {report.tools.map((row) => (
                      <tr key={row.name}>
                        <td><code>{row.name}</code></td>
                        <td className="usage-num">{row.calls}</td>
                        <td className="usage-num">{row.failures || '—'}</td>
                        <td className="usage-num">{mediaCountsLabel(row.mediaVideos ?? 0, row.mediaAudios ?? 0, row.mediaImages ?? 0) || '—'}</td>
                        <td className="usage-num">
                          {row.costUnknown && !formatCostMicros(row.costMicros) ? '?' : formatCostMicros(row.costMicros) || '—'}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : null}
            {asArray(report.children).length ? (
              <div className="usage-section">
                <h3>{report.scope === 'library' ? 'Projects' : 'Conversations'}</h3>
                <table className="usage-table">
                  <thead>
                    <tr>
                      <th>{report.scope === 'library' ? 'Project' : 'Chat'}</th>
                      <th className="usage-num">Turns</th>
                      <th className="usage-num">Tool calls</th>
                      <th className="usage-num">Cost</th>
                      <th aria-label="Actions" />
                    </tr>
                  </thead>
                  <tbody>
                    {asArray(report.children).map((child) => (
                      <tr key={child.id}>
                        <td>{child.title || child.id}</td>
                        <td className="usage-num">{child.turns}</td>
                        <td className="usage-num">{child.toolCalls}</td>
                        <td className="usage-num">
                          {(child.costUnknown ? '~' : '') + (formatCostMicros(child.costMicros) || '—')}
                        </td>
                        <td className="usage-num">
                          <button type="button" className="usage-drill" onClick={() => onOpenChild(child)}>
                            Report →
                          </button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : null}
            {!report.models.length && !report.tools.length ? (
              <div className="usage-note">No usage recorded in this {scopeNoun.toLowerCase()} yet.</div>
            ) : null}
          </>
        ) : null}
      </div>
    </>
  );
}

export {UsageReportView};
