import {Fragment, useEffect, useMemo, useRef, useState} from 'react';
import {ResolveDocumentCompare} from '../../wailsjs/go/main/App';
import {main} from '../../wailsjs/go/models';
import {notifyError} from '../notifications';
import {EditorHeader} from './EditorHeader';
import {
  DiffLine,
  compareContextLines,
  compareSummary,
  foldRows,
  hunkSideRange,
  sectionLabel,
  stepHunk,
  unifiedItems,
} from './documentCompareModel';
import './documentCompare.css';

// The compare workspace: two documents side by side (or unified), read-only.
// The diff arrives computed (ResolveDocumentCompare → document_diff.go); this
// component folds, lays out, and navigates it. There is nothing to save and
// nothing to lose, so it carries no dirty state — closing it never asks.
export type DocumentCompareHandle = {
  source: main.DocumentCompareInfo;
};

type ViewMode = 'split' | 'unified';

export function DocumentCompare(props: {
  handle: DocumentCompareHandle;
  // True while the workspace is hidden beneath another screen (Settings) —
  // detaches the keyboard shortcuts.
  suspended?: boolean;
  onClose: () => void;
  onOpenParent: (conversationID: string) => void;
  // Opens one side in the document editor (it replaces this workspace).
  onOpenDocument: (conversationID: string, artifactID: string) => void;
}) {
  // Swap and the whitespace toggle re-resolve, replacing the comparison in
  // place — the handle is only the opening state.
  const [info, setInfo] = useState<main.DocumentCompareInfo>(props.handle.source);
  const [view, setView] = useState<ViewMode>('split');
  const [showAll, setShowAll] = useState(false);
  const [expanded, setExpanded] = useState<ReadonlySet<number>>(() => new Set());
  const [current, setCurrent] = useState(-1);
  const [reloading, setReloading] = useState(false);
  const scrollRef = useRef<HTMLDivElement | null>(null);

  const rows = info.diff?.rows ?? [];
  const hunks = info.diff?.hunks ?? [];
  const stats = info.diff?.stats ?? {added: 0, removed: 0, changes: 0, identical: true};
  const conversationID = info.before.conversationId;
  const screenplay = info.before.format === 'fountain' && info.after.format === 'fountain';
  const mixedFormats = info.before.format !== info.after.format;

  const items = useMemo(() => foldRows(rows, compareContextLines, expanded, showAll), [rows, expanded, showAll]);
  const unified = useMemo(() => (view === 'unified' ? unifiedItems(rows, items) : []), [view, rows, items]);
  const currentHunk = current >= 0 ? hunks[current] : undefined;
  const inCurrent = (row: number) => !!currentHunk && row >= currentHunk.firstRow && row <= currentHunk.lastRow;

  // jumpTo selects a change and scrolls its first line into view. Changed
  // rows are never folded, so the target always exists in either layout.
  function jumpTo(index: number) {
    if (index < 0 || index >= hunks.length) return;
    setCurrent(index);
    const target = scrollRef.current?.querySelector<HTMLElement>(`[data-row="${hunks[index].firstRow}"]`);
    target?.scrollIntoView({block: 'center', behavior: 'smooth'});
  }

  // The shortcuts read the latest cursor through a ref, so the listener
  // subscribes once per suspension change rather than on every step.
  const stepRef = useRef<(delta: 1 | -1) => void>(() => {});
  stepRef.current = (delta) => jumpTo(stepHunk(current, delta, hunks.length));
  useEffect(() => {
    if (props.suspended) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.metaKey || event.ctrlKey || event.altKey) return;
      const target = event.target as HTMLElement | null;
      if (target && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.tagName === 'SELECT' || target.isContentEditable)) return;
      if (event.key === 'n') {
        event.preventDefault();
        stepRef.current(1);
      } else if (event.key === 'p') {
        event.preventDefault();
        stepRef.current(-1);
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [props.suspended]);

  async function reload(beforeID: string, afterID: string, ignoreWhitespace: boolean) {
    setReloading(true);
    try {
      const next = await ResolveDocumentCompare(main.DocumentCompareRequest.createFrom({
        conversationId: conversationID,
        beforeArtifactId: beforeID,
        afterArtifactId: afterID,
        ignoreWhitespace,
      }));
      setInfo(next);
      setExpanded(new Set());
      setCurrent(-1);
      scrollRef.current?.scrollTo({top: 0});
    } catch (error) {
      notifyError("Couldn't refresh the comparison", error);
    } finally {
      setReloading(false);
    }
  }

  const expand = (start: number) => setExpanded((previous) => new Set(previous).add(start));
  const fold = (key: string, start: number, count: number) => (
    <button key={key} type="button" className="compare-fold" onClick={() => expand(start)}>
      ⋯ {count} unchanged line{count === 1 ? '' : 's'}
    </button>
  );

  return (
    <section className="document-compare">
      <EditorHeader
        title={screenplay ? 'Compare Screenplays' : 'Compare Documents'}
        parentAvailable
        parentConversationID={conversationID}
        busyLabel={reloading ? 'Comparing…' : undefined}
        onClose={props.onClose}
        onOpenParent={props.onOpenParent}
      />

      <div className="editor-body">
        <div className="compare-main">
          <div className="compare-toolbar-row">
            <div className="compare-view-modes" role="tablist" aria-label="Comparison layout">
              {(['split', 'unified'] as ViewMode[]).map((mode) => (
                <button key={mode} type="button" role="tab" aria-selected={view === mode} className={view === mode ? 'selected' : ''} onClick={() => setView(mode)}>
                  {mode === 'split' ? 'Side by side' : 'Unified'}
                </button>
              ))}
            </div>
            <label className="compare-toggle" title="Treat lines that differ only in spacing as unchanged">
              <input
                type="checkbox"
                checked={info.ignoreWhitespace}
                disabled={reloading}
                onChange={(event) => void reload(info.before.artifactId, info.after.artifactId, event.target.checked)}
              />
              Ignore whitespace
            </label>
            <label className="compare-toggle" title="Show every line instead of folding the unchanged stretches">
              <input type="checkbox" checked={showAll} onChange={(event) => setShowAll(event.target.checked)} />
              Show unchanged lines
            </label>
            <div className="compare-toolbar-actions">
              <button type="button" disabled={hunks.length === 0} title="Previous change (p)" onClick={() => stepRef.current(-1)}>↑ Previous</button>
              <button type="button" disabled={hunks.length === 0} title="Next change (n)" onClick={() => stepRef.current(1)}>↓ Next</button>
              <button
                type="button"
                disabled={reloading}
                title="Swap the before and after documents"
                onClick={() => void reload(info.after.artifactId, info.before.artifactId, info.ignoreWhitespace)}
              >
                ⇄ Swap
              </button>
            </div>
          </div>

          <div className={`compare-panel compare-${view}`}>
            {view === 'split' ? (
              <div className="compare-columns">
                <span />
                <span title={info.before.artifactId}>Before · {info.before.artifactId.slice(0, 12)}</span>
                <span />
                <span title={info.after.artifactId}>After · {info.after.artifactId.slice(0, 12)}</span>
              </div>
            ) : (
              <div className="compare-columns">
                <span />
                <span />
                <span />
                <span title={`${info.before.artifactId} → ${info.after.artifactId}`}>
                  Before · {info.before.artifactId.slice(0, 12)} → After · {info.after.artifactId.slice(0, 12)}
                </span>
              </div>
            )}
            {stats.identical ? <div className="compare-identical" role="status">The documents are identical.</div> : null}
            <div className="compare-scroll" ref={scrollRef}>
              {view === 'split'
                ? items.map((item) => {
                    if (item.type === 'fold') return fold(`fold-${item.start}`, item.start, item.count);
                    const row = rows[item.index];
                    return (
                      <div key={item.index} className={`compare-row compare-${row.kind}${inCurrent(item.index) ? ' current' : ''}`} data-row={item.index}>
                        <span className="compare-num">{row.before?.number ?? ''}</span>
                        <div className={`compare-text compare-before${row.before ? '' : ' compare-empty'}`}><LineText line={row.before} /></div>
                        <span className="compare-num compare-num-after">{row.after?.number ?? ''}</span>
                        <div className={`compare-text compare-after${row.after ? '' : ' compare-empty'}`}><LineText line={row.after} /></div>
                      </div>
                    );
                  })
                : unified.map((item) => {
                    if (item.type === 'fold') return fold(`fold-${item.start}`, item.start, item.count);
                    const sign = item.sign === '-' ? 'minus' : item.sign === '+' ? 'plus' : 'context';
                    return (
                      <div key={`${item.row}${item.sign}`} className={`compare-row compare-line-${sign}${inCurrent(item.row) ? ' current' : ''}`} data-row={item.row}>
                        <span className="compare-num">{item.beforeNumber ?? ''}</span>
                        <span className="compare-num">{item.afterNumber ?? ''}</span>
                        <span className="compare-sign" aria-hidden="true">{item.sign}</span>
                        <div className={`compare-text ${item.sign === '-' ? 'compare-before' : 'compare-after'}`}>
                          <LineText line={{number: 0, text: item.text, segments: item.segments}} />
                        </div>
                      </div>
                    );
                  })}
            </div>
          </div>
        </div>

        <aside className="editor-inspector compare-inspector">
          <div className="compare-summary" aria-live="polite">
            <strong>{compareSummary(stats)}</strong>
            <span>
              {info.ignoreWhitespace ? 'Whitespace-only differences ignored. ' : ''}
              {mixedFormats ? 'One Markdown and one Fountain document — compared line by line as text.' : ''}
            </span>
          </div>

          <div className="compare-sides">
            <SideCard label="Before" source={info.before} onOpen={() => props.onOpenDocument(conversationID, info.before.artifactId)} />
            <SideCard label="After" source={info.after} onOpen={() => props.onOpenDocument(conversationID, info.after.artifactId)} />
          </div>

          <div className="compare-changes">
            <strong>Changes</strong>
            {hunks.length === 0 ? (
              <p className="hint">Nothing changed between these two documents.</p>
            ) : (
              <ul>
                {hunks.map((hunk, index) => (
                  <li key={index}>
                    <button
                      type="button"
                      className={index === current ? 'selected' : ''}
                      title={`Before ${hunkSideRange(hunk.beforeLine, hunk.beforeCount)} · after ${hunkSideRange(hunk.afterLine, hunk.afterCount)}`}
                      onClick={() => jumpTo(index)}
                    >
                      <span className="compare-change-section">{sectionLabel(hunk.section)}</span>
                      <span className="compare-change-counts">
                        {hunk.added ? <span className="compare-added">+{hunk.added}</span> : null}
                        {hunk.removed ? <span className="compare-removed">−{hunk.removed}</span> : null}
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
            {hunks.length > 0 ? <span className="hint">Press n / p to step through the changes.</span> : null}
          </div>
        </aside>
      </div>
    </section>
  );
}

// LineText renders one side's text, marking the changed words of a paired
// change line. A line without segments renders whole — unchanged, a pure
// insert or delete, or a pair too dissimilar to mark word by word.
function LineText({line}: {line?: DiffLine}) {
  if (!line) return null;
  if (!line.segments?.length) return <>{line.text}</>;
  return (
    <>
      {line.segments.map((segment, index) =>
        segment.changed ? <mark key={index}>{segment.text}</mark> : <Fragment key={index}>{segment.text}</Fragment>,
      )}
    </>
  );
}

function SideCard({label, source, onOpen}: {label: string; source: main.MarkdownSourceInfo; onOpen: () => void}) {
  return (
    <div className="compare-side">
      <span className="compare-side-label">{label}</span>
      <strong title={source.artifactId}>{source.artifactId.slice(0, 12)}</strong>
      <span className="hint">
        {source.format === 'fountain' ? 'Fountain screenplay' : 'Markdown'} · {source.wordCount} word{source.wordCount === 1 ? '' : 's'}
      </span>
      <button type="button" title={`Open ${source.artifactId} in the ${source.format === 'fountain' ? 'screenplay' : 'markdown'} editor`} onClick={onOpen}>
        Open in editor
      </button>
    </div>
  );
}
