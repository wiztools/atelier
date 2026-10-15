import {useEffect, useMemo, useRef, useState} from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import * as AppBindings from '../../wailsjs/go/main/App';
import {SaveMarkdown} from '../../wailsjs/go/main/App';
import {main} from '../../wailsjs/go/models';
import {EditsPanel} from './EditsPanel';
import {EditorHeader} from './EditorHeader';
import {
  MarkdownEditState,
  continueListOnEnter,
  cycleHeading,
  indentSelection,
  insertBlockSnippet,
  insertCodeFence,
  insertLink,
  insertTable,
  markdownOutline,
  markdownStats,
  markdownWarnings,
  offsetOfLine,
  toggleLineAffix,
  wrapSelection,
} from './markdownModel';
import './markdownEditor.css';

export type MarkdownEditorHandle = {
  source: main.MarkdownSourceInfo;
};

type EditorLogEntry = {
  at: string;
  artifactId: string;
  url: string;
  wordCount: number;
};

// The Edits panel's height clamp needs this editor's content floor: header +
// toolbar + a usable split-pane minimum + paddings.
const editsPanelBodyReservePx = 520;

const undoLimit = 100;

// Typing bursts land as one undo entry: a push happens only when the previous
// keystroke is older than this. Ops (toolbar, shortcuts, Enter/Tab) always
// push explicitly.
const typingCoalesceMs = 400;

function formatEditorError(error: unknown): string {
  if (typeof error === 'string') return error;
  if (error instanceof Error) return error.message;
  try {
    return JSON.stringify(error);
  } catch {
    return String(error);
  }
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

  const textareaRef = useRef<HTMLTextAreaElement | null>(null);
  const previewRef = useRef<HTMLDivElement | null>(null);
  const lastPushRef = useRef(0);
  const pendingSelection = useRef<{start: number; end: number} | null>(null);
  const scrollLockRef = useRef(false);

  const dirty = text !== baseline;
  const stats = useMemo(() => markdownStats(text), [text]);
  const outline = useMemo(() => markdownOutline(text), [text]);
  const warnings = useMemo(() => markdownWarnings(text), [text]);

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
      const next = continueListOnEnter({
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

  // Proportional scroll sync between the panes; the lock stops the mirror
  // from feeding back into the pane the user is scrolling.
  const syncScroll = (from: HTMLTextAreaElement | HTMLDivElement, to: HTMLTextAreaElement | HTMLDivElement | null) => {
    if (!to || scrollLockRef.current) {
      return;
    }
    const fromRange = from.scrollHeight - from.clientHeight;
    if (fromRange <= 0) {
      return;
    }
    scrollLockRef.current = true;
    to.scrollTop = (from.scrollTop / fromRange) * (to.scrollHeight - to.clientHeight);
    requestAnimationFrame(() => {
      scrollLockRef.current = false;
    });
  };

  const jumpToLine = (line: number) => {
    const textarea = textareaRef.current;
    if (!textarea) {
      return;
    }
    const offset = offsetOfLine(text, line);
    textarea.focus();
    textarea.setSelectionRange(offset, offset);
    const lineRatio = text.split('\n').length > 1 ? line / (text.split('\n').length - 1) : 0;
    textarea.scrollTop = lineRatio * (textarea.scrollHeight - textarea.clientHeight);
  };

  // saveDoc commits the edited document into the conversation: a new md_
  // artifact plus a turn recording the edit. The draft is never touched.
  const saveDoc = async () => {
    setSaving(true);
    setSaveError('');
    try {
      const result = await AppBindings.SaveMarkdownEdit(main.SaveMarkdownEditRequest.createFrom({
        conversationId: sourceConversationID,
        sourceArtifactId: activeArtifactID,
        text,
      }));
      setLog((entries) => [
        {at: new Date().toLocaleTimeString(), artifactId: result.artifactId, url: result.url, wordCount: result.wordCount},
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
              placeholder="Write in Markdown…"
              onChange={(event) => onType(event.target.value)}
              onKeyDown={onKeyDown}
              onScroll={(event) => syncScroll(event.currentTarget, previewRef.current)}
              spellCheck
            />
            <div
              className="markdown-preview markdown-body"
              ref={previewRef}
              onScroll={(event) => syncScroll(event.currentTarget, textareaRef.current)}
            >
              <ReactMarkdown remarkPlugins={[remarkGfm]}>{text}</ReactMarkdown>
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
            <button
              type="button"
              className="editor-generate"
              disabled={saving || text.trim() === ''}
              title="Write the document back into the conversation as a new artifact — the original stays intact. Download the saved file from the Edits panel below."
              onClick={() => void saveDoc()}
            >
              {saving ? 'Saving…' : 'Save Markdown to conversation'}
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
                  Saved · {entry.wordCount} words · {entry.artifactId.slice(0, 12)}
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
                <button type="button" onClick={() => void downloadSaved(entry.url, index)}>Download</button>
              </div>
            </li>
          ))}
        </ul>
      </EditsPanel>
    </section>
  );
}
