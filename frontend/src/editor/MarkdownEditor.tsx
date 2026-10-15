import {useEffect, useMemo, useRef, useState} from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import * as AppBindings from '../../wailsjs/go/main/App';
import {SaveDocumentExport, SaveMarkdown} from '../../wailsjs/go/main/App';
import {main} from '../../wailsjs/go/models';
import {EditsPanel} from './EditsPanel';
import {EditorHeader} from './EditorHeader';
import {
  MarkdownEditState,
  continueListOnEnter,
  cycleHeading,
  findMatches,
  indentSelection,
  insertBlockSnippet,
  insertCodeFence,
  insertLink,
  insertTable,
  markdownOutline,
  markdownStats,
  markdownWarnings,
  nextMatch,
  offsetOfLine,
  previousMatch,
  replaceLiteral,
  toggleLineAffix,
  wrapSelection,
} from './markdownModel';
import {
  FountainElement,
  fountainContinueElementOnEnter,
  fountainCycleElementOnTab,
  fountainOutline,
  fountainParse,
  fountainWarnings,
} from './fountainModel';
import './markdownEditor.css';

export type MarkdownEditorHandle = {
  source: main.MarkdownSourceInfo;
};

// The two document dialects — what the save format select offers and what
// the preview, outline, warnings, and keyboard model dispatch on.
type DocumentFormat = 'markdown' | 'fountain';

type EditorLogEntry = {
  at: string;
  artifactId: string;
  url: string;
  wordCount: number;
  format: DocumentFormat;
};

// The Edits panel's height clamp needs this editor's content floor: header +
// toolbar + a usable split-pane minimum + paddings.
const editsPanelBodyReservePx = 520;

const undoLimit = 100;

// Typing bursts land as one undo entry: a push happens only when the previous
// keystroke is older than this. Ops (toolbar, shortcuts, Enter/Tab) always
// push explicitly.
const typingCoalesceMs = 400;

// Scroll-echo suppression: programmatic scrollTop assignments record the
// value they set (read back after assigning, so clamping is exact); a scroll
// event landing on that value inside the window is the assignment's own
// echo, never the user. A timing-based lock (one rAF) raced scroll-event
// delivery on large documents — the echo landed after the release and the
// panes chased each other endlessly, with the browser's selection reveal
// chasing the caret each bounce shoved out of view.
const scrollEchoTolerancePx = 2;
const scrollEchoWindowMs = 250;

type ScrollEcho = {value: number; until: number};

function formatEditorError(error: unknown): string {
  if (typeof error === 'string') return error;
  if (error instanceof Error) return error.message;
  try {
    return JSON.stringify(error);
  } catch {
    return String(error);
  }
}

// The scroll position that puts the line containing `offset` at the top of
// the textarea's viewport, measured in a mirror div that reproduces the
// textarea's wrapping (font, padding, width). Line-count proportions go
// wrong wherever paragraphs wrap — a jump would land short of the target
// and leave it below the viewport. The marker span after the head text sits
// on the target line, so its offsetTop IS the scroll coordinate.
function lineScrollTop(textarea: HTMLTextAreaElement, text: string, offset: number): number {
  const cs = window.getComputedStyle(textarea);
  const mirror = document.createElement('div');
  for (const prop of [
    'font-family',
    'font-size',
    'font-style',
    'font-weight',
    'letter-spacing',
    'line-height',
    'tab-size',
    'padding-top',
    'padding-right',
    'padding-bottom',
    'padding-left',
    'box-sizing',
    'white-space',
    'overflow-wrap',
    'word-break',
  ]) {
    mirror.style.setProperty(prop, cs.getPropertyValue(prop));
  }
  mirror.style.position = 'absolute';
  mirror.style.top = '0';
  mirror.style.left = '-9999px';
  mirror.style.visibility = 'hidden';
  mirror.style.width = `${textarea.clientWidth}px`;
  const head = document.createElement('span');
  head.textContent = text.slice(0, offset);
  const marker = document.createElement('span');
  marker.textContent = 'x';
  mirror.append(head, marker);
  document.body.appendChild(mirror);
  const top = marker.offsetTop;
  mirror.remove();
  return Math.max(0, top);
}

type ViewMode = 'split' | 'source' | 'preview';

export function MarkdownEditor(props: {
  handle: MarkdownEditorHandle;
  // True while the editor is hidden beneath another screen (Settings).
  // Detaches the shortcuts; all edit state stays.
  suspended?: boolean;
  onClose: () => void;
  onOpenParent: (conversationID: string) => void;
  onDirtyChange?: (dirty: boolean) => void;
  // Fired after a save lands a new artifact on the conversation, so the
  // parent can refresh the sidebar and the assets panel.
  onSavedToConversation?: () => void;
}) {
  const {source} = props.handle;
  // The revision the editor is currently working from — the provenance the
  // next save records ("Use as source" retargets it).
  const [activeArtifactID, setActiveArtifactID] = useState(source.artifactId);
  const [loadingRevision, setLoadingRevision] = useState(false);
  const sourceConversationID = useState(source.conversationId)[0];

  const [text, setTextState] = useState(source.text ?? '');
  const [baseline, setBaseline] = useState(source.text ?? '');
  const [undoStack, setUndoStack] = useState<string[]>([]);
  const [redoStack, setRedoStack] = useState<string[]>([]);

  const [view, setView] = useState<ViewMode>('split');
  const [loadError, setLoadError] = useState('');
  const [log, setLog] = useState<EditorLogEntry[]>([]);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState('');
  // The dialect the document saves (and previews) as — seeded from the
  // source artifact's extension, switched from the save block's select.
  // Switching and saving IS the format conversion: the artifact extension
  // follows the format.
  const [exportFormat, setExportFormat] = useState<DocumentFormat>(source.format === 'fountain' ? 'fountain' : 'markdown');

  const [find, setFind] = useState('');
  const [replace, setReplace] = useState('');
  const [replaceCount, setReplaceCount] = useState<number | null>(null);

  const textareaRef = useRef<HTMLTextAreaElement | null>(null);
  const previewRef = useRef<HTMLDivElement | null>(null);
  const lastPushRef = useRef(0);
  const pendingSelection = useRef<{start: number; end: number} | null>(null);
  const scrollEchoes = useRef(new Map<HTMLElement, ScrollEcho>());

  const dirty = text !== baseline;
  const stats = useMemo(() => markdownStats(text), [text]);
  const fountain = exportFormat === 'fountain';
  const outline = useMemo(
    () => (fountain ? fountainOutline(text) : markdownOutline(text)),
    [fountain, text],
  );
  const fountainScript = useMemo(
    () => (fountain ? fountainParse(text) : null),
    [fountain, text],
  );
  const warnings = useMemo(
    () => (fountain ? fountainWarnings(fountainScript!) : markdownWarnings(text)),
    [fountain, fountainScript, text],
  );

  // Typing path: coalesce by idle gap so a burst of keystrokes is one undo
  // step, and clear any stale op timestamp so the burst starts its own entry.
  const onType = (value: string) => {
    const now = Date.now();
    if (now - lastPushRef.current > typingCoalesceMs) {
      setUndoStack((stack) => [...stack, text].slice(-undoLimit));
    }
    lastPushRef.current = now;
    setRedoStack([]);
    setTextState(value);
  };

  // run applies a model op: the pre-op text lands on the undo stack, the
  // post-op selection is restored once the textarea re-renders.
  const run = (op: (state: MarkdownEditState) => MarkdownEditState | null) => {
    const textarea = textareaRef.current;
    const current: MarkdownEditState = {
      text,
      selStart: textarea?.selectionStart ?? text.length,
      selEnd: textarea?.selectionEnd ?? text.length,
    };
    const next = op(current);
    if (!next || next.text === current.text) {
      return;
    }
    setUndoStack((stack) => [...stack, current.text].slice(-undoLimit));
    lastPushRef.current = Date.now();
    setRedoStack([]);
    setTextState(next.text);
    pendingSelection.current = {start: next.selStart, end: next.selEnd};
  };

  useEffect(() => {
    if (pendingSelection.current && textareaRef.current) {
      const {start, end} = pendingSelection.current;
      textareaRef.current.setSelectionRange(start, end);
      pendingSelection.current = null;
    }
  });

  const undo = () => {
    setUndoStack((stack) => {
      if (!stack.length) return stack;
      const previous = stack[stack.length - 1];
      setRedoStack((redo) => [...redo, text].slice(-undoLimit));
      setTextState(previous);
      lastPushRef.current = 0;
      return stack.slice(0, -1);
    });
  };
  const redo = () => {
    setRedoStack((stack) => {
      if (!stack.length) return stack;
      const next = stack[stack.length - 1];
      setUndoStack((undo) => [...undo, text].slice(-undoLimit));
      setTextState(next);
      lastPushRef.current = 0;
      return stack.slice(0, -1);
    });
  };

  useEffect(() => {
    props.onDirtyChange?.(dirty);
  }, [dirty]);

  // The textarea's own key handling — the shortcuts live on the element, not
  // the window, because the editor's whole stage IS a text field.
  const onKeyDown = (event: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (props.suspended) {
      return;
    }
    const meta = event.metaKey || event.ctrlKey;
    const key = event.key.toLowerCase();
    if (meta && key === 'z') {
      event.preventDefault();
      if (event.shiftKey) {
        redo();
      } else {
        undo();
      }
      return;
    }
    if (meta && key === 'b') {
      event.preventDefault();
      run((state) => wrapSelection(state, '**', 'bold text'));
      return;
    }
    if (meta && key === 'i') {
      event.preventDefault();
      run((state) => wrapSelection(state, '*', 'italic text'));
      return;
    }
    if (meta && key === 'k') {
      event.preventDefault();
      run(insertLink);
      return;
    }
    if (event.key === 'Enter' && !event.shiftKey && !meta && !event.altKey) {
      const op = fountain ? fountainContinueElementOnEnter : continueListOnEnter;
      const next = op({
        text,
        selStart: event.currentTarget.selectionStart,
        selEnd: event.currentTarget.selectionEnd,
      });
      if (next) {
        event.preventDefault();
        run(() => next);
      }
      return;
    }
    if (event.key === 'Tab') {
      if (fountain) {
        // The screenplay Tab re-types the line as the next element kind;
        // Shift+Tab walks the cycle backwards.
        event.preventDefault();
        run((state) => fountainCycleElementOnTab(state, event.shiftKey));
        return;
      }
      const next = indentSelection(
        {text, selStart: event.currentTarget.selectionStart, selEnd: event.currentTarget.selectionEnd},
        event.shiftKey,
      );
      if (next) {
        event.preventDefault();
        run(() => next);
      }
    }
  };

  // Assigns a scroll position and remembers it as ours, so the scroll event
  // it produces can be told apart from a user scroll.
  const setPaneScrollTop = (pane: HTMLTextAreaElement | HTMLDivElement, value: number) => {
    pane.scrollTop = value;
    scrollEchoes.current.set(pane, {value: pane.scrollTop, until: Date.now() + scrollEchoWindowMs});
  };

  // Proportional scroll sync between the panes; echo suppression stops the
  // mirror from feeding back into the pane the user is scrolling.
  const syncScroll = (from: HTMLTextAreaElement | HTMLDivElement, to: HTMLTextAreaElement | HTMLDivElement | null) => {
    if (!to) {
      return;
    }
    const echo = scrollEchoes.current.get(from);
    if (echo) {
      if (Date.now() < echo.until && Math.abs(from.scrollTop - echo.value) <= scrollEchoTolerancePx) {
        return;
      }
      scrollEchoes.current.delete(from);
    }
    const fromRange = from.scrollHeight - from.clientHeight;
    if (fromRange <= 0) {
      return;
    }
    setPaneScrollTop(to, (from.scrollTop / fromRange) * (to.scrollHeight - to.clientHeight));
  };

  // A jump drives BOTH panes itself instead of riding the scroll-event
  // chain, so no echo of the jump can bounce back into the textarea.
  const jumpTextareaTo = (textarea: HTMLTextAreaElement, offset: number) => {
    setPaneScrollTop(textarea, lineScrollTop(textarea, text, offset));
    const preview = previewRef.current;
    const sourceRange = textarea.scrollHeight - textarea.clientHeight;
    if (preview && sourceRange > 0) {
      setPaneScrollTop(preview, (textarea.scrollTop / sourceRange) * (preview.scrollHeight - preview.clientHeight));
    }
  };

  const jumpToLine = (line: number) => {
    const textarea = textareaRef.current;
    if (!textarea) {
      return;
    }
    const offset = offsetOfLine(text, line);
    textarea.focus();
    textarea.setSelectionRange(offset, offset);
    jumpTextareaTo(textarea, offset);
  };

  // Find & replace: literal, case-sensitive; the arrows walk the matches with
  // wrap-around, Replace all commits through the same undo path as the ops.
  const matchOffsets = useMemo(() => findMatches(text, find), [text, find]);

  const selectMatch = (offset: number) => {
    const textarea = textareaRef.current;
    if (!textarea) {
      return;
    }
    textarea.focus();
    textarea.setSelectionRange(offset, offset + find.length);
    jumpTextareaTo(textarea, offset);
  };

  const jumpNext = () => {
    const at = nextMatch(matchOffsets, textareaRef.current?.selectionEnd ?? 0);
    if (at >= 0) selectMatch(at);
  };
  const jumpPrev = () => {
    const at = previousMatch(matchOffsets, textareaRef.current?.selectionStart ?? 0);
    if (at >= 0) selectMatch(at);
  };

  const runReplaceAll = () => {
    const result = replaceLiteral(text, find, replace);
    if (result.count > 0) {
      setUndoStack((stack) => [...stack, text].slice(-undoLimit));
      lastPushRef.current = Date.now();
      setRedoStack([]);
      setTextState(result.text);
    }
    setReplaceCount(result.count);
  };

  // saveDoc commits the edited document into the conversation: a new md_
  // artifact in the selected format plus a turn recording the edit. The
  // draft is never touched.
  const saveDoc = async () => {
    setSaving(true);
    setSaveError('');
    try {
      const result = await AppBindings.SaveMarkdownEdit(main.SaveMarkdownEditRequest.createFrom({
        conversationId: sourceConversationID,
        sourceArtifactId: activeArtifactID,
        format: exportFormat,
        text,
      }));
      setLog((entries) => [
        {at: new Date().toLocaleTimeString(), artifactId: result.artifactId, url: result.url, wordCount: result.wordCount, format: result.format === 'fountain' ? 'fountain' : 'markdown'},
        ...entries,
      ]);
      props.onSavedToConversation?.();
    } catch (error) {
      setSaveError(formatEditorError(error));
    } finally {
      setSaving(false);
    }
  };

  // loadRevision is the save rows' "Use as source": an earlier revision
  // becomes the editing baseline. The current text lands on the undo stack
  // first, so picking up an old revision is never destructive.
  const loadRevision = async (entry: EditorLogEntry) => {
    if (loadingRevision || entry.artifactId === activeArtifactID) return;
    setLoadingRevision(true);
    setLoadError('');
    try {
      const revision = await AppBindings.ResolveMarkdownEditSource(sourceConversationID, entry.artifactId);
      setUndoStack((stack) => [...stack, text].slice(-undoLimit));
      setRedoStack([]);
      setTextState(revision.text);
      setBaseline(revision.text);
      setActiveArtifactID(revision.artifactId);
      setExportFormat(revision.format === 'fountain' ? 'fountain' : 'markdown');
      lastPushRef.current = 0;
    } catch (error) {
      setLoadError(formatEditorError(error));
    } finally {
      setLoadingRevision(false);
    }
  };

  const downloadSaved = async (url: string, index: number) => {
    try {
      await SaveMarkdown(main.SaveTranscriptRequest.createFrom({
        path: url,
        suggestedName: `${source.conversationTitle || 'document'}-${Date.now()}-${index + 1}`,
      }));
    } catch (error) {
      setSaveError(formatEditorError(error));
    }
  };

  // exportSaved is the fountain rows' derived-export door: the saved
  // artifact renders to PDF or Final Draft on its way to disk — never from
  // the editor's uncommitted draft.
  const exportSaved = async (url: string, format: 'pdf' | 'fdx', index: number) => {
    try {
      await SaveDocumentExport(main.SaveDocumentExportRequest.createFrom({
        path: url,
        format,
        suggestedName: `${source.conversationTitle || 'screenplay'}-${Date.now()}-${index + 1}`,
      }));
    } catch (error) {
      setSaveError(formatEditorError(error));
    }
  };

  const toolbarGroups: Array<Array<{label: string; title: string; op: () => void}>> = [
    [
      {label: 'H', title: 'Cycle heading level (none → h1 → h2 → h3)', op: () => run(cycleHeading)},
      {label: 'B', title: 'Bold (⌘B)', op: () => run((state) => wrapSelection(state, '**', 'bold text'))},
      {label: 'I', title: 'Italic (⌘I)', op: () => run((state) => wrapSelection(state, '*', 'italic text'))},
      {label: 'S', title: 'Strikethrough', op: () => run((state) => wrapSelection(state, '~~', 'struck text'))},
      {label: '</>', title: 'Inline code', op: () => run((state) => wrapSelection(state, '`', 'code'))},
    ],
    [
      {label: '•', title: 'Bullet list', op: () => run((state) => toggleLineAffix(state, '- '))},
      {label: '1.', title: 'Numbered list', op: () => run((state) => toggleLineAffix(state, '1. '))},
      {label: '☑', title: 'Task list', op: () => run((state) => toggleLineAffix(state, '- [ ] '))},
      {label: '❝', title: 'Blockquote', op: () => run((state) => toggleLineAffix(state, '> '))},
    ],
    [
      {label: '🔗', title: 'Link (⌘K)', op: () => run(insertLink)},
      {label: '⌨︎', title: 'Code block', op: () => run((state) => insertCodeFence(state, ''))},
      {label: '▦', title: 'Table', op: () => run(insertTable)},
      {label: '—', title: 'Horizontal rule', op: () => run((state) => insertBlockSnippet(state, '\n\n---\n\n'))},
    ],
  ];

  return (
    <section className="markdown-editor">
      <EditorHeader
        title="Markdown Editor"
        parentAvailable
        parentConversationID={sourceConversationID}
        onClose={props.onClose}
        onOpenParent={props.onOpenParent}
      />

      {loadError ? <div className="editor-error">{loadError}</div> : null}

      <div className="editor-body">
        <div className="markdown-editor-main">
          <div className="markdown-toolbar-row">
            <div className="markdown-view-modes" role="tablist" aria-label="Editor layout">
              {(['split', 'source', 'preview'] as ViewMode[]).map((mode) => (
                <button
                  key={mode}
                  type="button"
                  className={view === mode ? 'selected' : ''}
                  onClick={() => setView(mode)}
                >
                  {mode === 'split' ? 'Split' : mode === 'source' ? 'Source' : 'Preview'}
                </button>
              ))}
            </div>
            <div className="markdown-toolbar">
              {toolbarGroups.map((group, groupIndex) => (
                <div key={groupIndex} className="markdown-toolbar-group">
                  {group.map((item) => (
                    <button key={item.label} type="button" title={item.title} onClick={item.op}>
                      {item.label}
                    </button>
                  ))}
                </div>
              ))}
            </div>
          </div>

          <div className={`markdown-panes mode-${view}`}>
            <textarea
              ref={textareaRef}
              className="markdown-source"
              value={text}
              placeholder={fountain ? 'INT. SCENE - DAY' : 'Write in Markdown…'}
              onChange={(event) => onType(event.target.value)}
              onKeyDown={onKeyDown}
              onScroll={(event) => syncScroll(event.currentTarget, previewRef.current)}
              spellCheck
            />
            <div
              className={`markdown-preview ${fountain ? 'fountain-preview' : 'markdown-body'}`}
              ref={previewRef}
              onScroll={(event) => syncScroll(event.currentTarget, textareaRef.current)}
            >
              {fountain ? <FountainPreview text={text} /> : <ReactMarkdown remarkPlugins={[remarkGfm]}>{text}</ReactMarkdown>}
            </div>
          </div>
        </div>

        <aside className="editor-inspector markdown-inspector">
          <div className="markdown-framing-summary" aria-live="polite">
            <strong>{stats.words} words · {stats.chars} chars</strong>
            <span>
              {stats.readingMinutes > 0 ? `~${stats.readingMinutes} min read · ` : ''}
              editing {activeArtifactID.slice(0, 12)}{dirty ? ' · edited' : ''}
            </span>
          </div>

          <div className="markdown-outline">
            <strong>Outline</strong>
            {outline.length === 0 ? (
              <p className="hint">No headings yet — start a line with # for one.</p>
            ) : (
              <ul>
                {outline.map((entry, index) => (
                  <li key={index} className={`markdown-outline-${entry.level}`}>
                    <button type="button" title={`Jump to line ${entry.line + 1}`} onClick={() => jumpToLine(entry.line)}>
                      {entry.title || <em>(untitled)</em>}
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </div>

          <div className="markdown-warnings">
            <strong>{warnings.length === 0 ? 'No warnings' : `${warnings.length} warning${warnings.length === 1 ? '' : 's'}`}</strong>
            <ul>
              {warnings.slice(0, 30).map((warning, index) => (
                <li key={index}>
                  <button type="button" onClick={() => jumpToLine(warning.line - 1)}>{warning.message}</button>
                </li>
              ))}
              {warnings.length > 30 ? <li>…and {warnings.length - 30} more</li> : null}
            </ul>
          </div>

          <div className="markdown-search">
            <div className="markdown-search-row">
              <input
                type="text"
                placeholder="Find…"
                value={find}
                onChange={(event) => { setFind(event.target.value); setReplaceCount(null); }}
                onKeyDown={(event) => {
                  if (event.key === 'Enter') {
                    event.preventDefault();
                    if (event.shiftKey) jumpPrev(); else jumpNext();
                  }
                }}
              />
              <span className="markdown-search-count">{find ? matchOffsets.length : ''}</span>
              <button type="button" disabled={!matchOffsets.length} title="Previous match (Shift+Enter)" onClick={jumpPrev}>↑</button>
              <button type="button" disabled={!matchOffsets.length} title="Next match (Enter)" onClick={jumpNext}>↓</button>
            </div>
            <div className="markdown-search-row">
              <input
                type="text"
                placeholder="Replace with…"
                value={replace}
                onChange={(event) => setReplace(event.target.value)}
              />
              <button type="button" disabled={!find || !matchOffsets.length} onClick={runReplaceAll}>Replace all</button>
            </div>
            {replaceCount !== null ? <span className="hint">{replaceCount} replaced</span> : null}
          </div>

          <div className="editor-tool-group">
            <button type="button" disabled={!undoStack.length} onClick={undo}>Undo</button>
            <button type="button" disabled={!redoStack.length} onClick={redo}>Redo</button>
            <button
              type="button"
              disabled={dirty === false}
              onClick={() => { setTextState(baseline); setUndoStack([]); setRedoStack([]); lastPushRef.current = 0; }}
            >
              Reset
            </button>
          </div>

          <div className="markdown-save">
            <label>
              Format
              <select
                value={exportFormat}
                onChange={(event) => setExportFormat(event.target.value === 'fountain' ? 'fountain' : 'markdown')}
                title="The dialect the document saves and previews as — the artifact extension follows it, so switching and saving converts the format"
              >
                <option value="markdown">Markdown (.md)</option>
                <option value="fountain">Fountain screenplay (.fountain)</option>
              </select>
            </label>
            <button
              type="button"
              className="editor-generate"
              disabled={saving || text.trim() === ''}
              title="Write the document back into the conversation as a new artifact — the original stays intact. Download the saved file from the Edits panel below."
              onClick={() => void saveDoc()}
            >
              {saving ? 'Saving…' : `Save ${fountain ? 'Fountain' : 'Markdown'} to conversation`}
            </button>
            {saveError ? <div className="editor-error">{saveError}</div> : null}
            <span className="editor-session-note">Edits stay in the editor until you save them — download the saved file from the Edits panel below.</span>
          </div>
        </aside>
      </div>

      <EditsPanel count={log.length} emptyText="Nothing saved yet." bodyReservePx={editsPanelBodyReservePx}>
        <ul className="editor-op-list">
          {log.map((entry, index) => (
            <li key={index} className="editor-op editor-op-done">
              <span className="editor-op-thumb-glyph">✓</span>
              <div className="editor-op-meta">
                <span className="editor-op-title">
                  Saved · {entry.format === 'fountain' ? 'Fountain' : 'Markdown'} · {entry.wordCount} words · {entry.artifactId.slice(0, 12)}
                </span>
                <span className="editor-op-sub" title={entry.url}>{entry.at} → attached to the conversation</span>
              </div>
              <div className="editor-op-actions">
                <button
                  type="button"
                  disabled={loadingRevision || entry.artifactId === activeArtifactID}
                  title={entry.artifactId === activeArtifactID ? 'This revision is already on the stage' : 'Continue editing from this revision'}
                  onClick={() => void loadRevision(entry)}
                >
                  Use as source
                </button>
                {entry.format === 'fountain' ? (
                  <button type="button" title="Export the saved screenplay as a Final Draft .fdx file" onClick={() => void exportSaved(entry.url, 'fdx', index)}>FDX</button>
                ) : null}
                <button
                  type="button"
                  title={entry.format === 'fountain' ? 'Render the saved screenplay to a production-layout PDF' : 'Render the saved document to a print-layout PDF'}
                  onClick={() => void exportSaved(entry.url, 'pdf', index)}
                >
                  PDF
                </button>
                <button type="button" onClick={() => void downloadSaved(entry.url, index)}>Download</button>
              </div>
            </li>
          ))}
        </ul>
      </EditsPanel>
    </section>
  );
}

// FountainPreview renders the screenplay page: the title page block when the
// script opens with one, then every element in its industry column — the
// preview IS the formatted script, the way the PDF will print it. Element
// text renders through react-markdown (one inline block each) so Fountain's
// *emphasis* markers style instead of printing.
function FountainPreview({text}: {text: string}) {
  const script = useMemo(() => fountainParse(text), [text]);
  const titleKeys = Object.keys(script.titlePage);
  return (
    <div className="fountain-page">
      {titleKeys.length > 0 ? (
        <div className="fountain-title-page">
          {(['Title', 'Credit', 'Author', 'Authors', 'Source', 'Draft date'] as const).map((key) =>
            script.titlePage[key] ? (
              <div key={key} className={`fountain-title-${key.toLowerCase().replace(' ', '-')}`}>{script.titlePage[key]}</div>
            ) : null,
          )}
        </div>
      ) : null}
      {script.elements.map((element, index) => (
        <FountainElementView key={index} element={element} />
      ))}
    </div>
  );
}

function FountainElementView({element}: {element: FountainElement}) {
  if (element.kind === 'Page Break') {
    return <hr className="fountain-page-break" />;
  }
  const className = `fountain-${element.kind.toLowerCase().replace(' ', '-')}`;
  return (
    <div className={className}>
      <ReactMarkdown remarkPlugins={[remarkGfm]}>{element.text}</ReactMarkdown>
    </div>
  );
}
