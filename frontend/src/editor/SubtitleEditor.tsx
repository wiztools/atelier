import {useEffect, useMemo, useRef, useState} from 'react';
import * as AppBindings from '../../wailsjs/go/main/App';
import {SaveTranscript} from '../../wailsjs/go/main/App';
import {main} from '../../wailsjs/go/models';
import {EditsPanel} from './EditsPanel';
import {EditorHeader} from './EditorHeader';
import {
  SubtitleCue,
  SubtitleDoc,
  SubtitleFormat,
  activeSubtitleIndex,
  cloneSubtitleDoc,
  deleteSubtitleCue,
  formatSubtitleClock,
  insertSubtitleCueAfter,
  mergeCueWithNext,
  nudgeCueEdge,
  replaceAllInCues,
  sameSubtitleDoc,
  shiftAllCues,
  splitSubtitleCue,
  subtitleWarnings,
  updateCueText,
  updateCueTimes,
} from './subtitleModel';
import './subtitleEditor.css';

export type SubtitleEditorHandle = {
  source: main.SubtitleSourceInfo;
};

type SubtitleAsset = main.ConversationAsset;

type EditorLogEntry = {
  format: string;
  cueCount: number;
  at: string;
  artifactId: string;
  url: string;
};

// The Edits panel's height clamp needs this editor's content floor: header +
// the linked-media row + the cue-list minimum + paddings.
const editsPanelBodyReservePx = 620;

const undoLimit = 100;

function formatEditorError(error: unknown): string {
  if (typeof error === 'string') return error;
  if (error instanceof Error) return error.message;
  try {
    return JSON.stringify(error);
  } catch {
    return String(error);
  }
}

// newestLinkableAsset picks the linked-playback default: the newest video or
// audio asset of the conversation. The guess is always visible and one picker
// click away from being wrong — cross-conversation sources are the normal
// case, not the exception.
function newestLinkableAsset(assets: SubtitleAsset[]): SubtitleAsset | null {
  const linkable = assets
    .filter((asset) => (asset.kind === 'video' || asset.kind === 'audio') && asset.url)
    .sort((a, b) => ((a.createdAt ?? '') === (b.createdAt ?? '') ? (a.id < b.id ? 1 : -1) : (a.createdAt ?? '') < (b.createdAt ?? '') ? 1 : -1));
  return linkable[0] ?? null;
}

export function SubtitleEditor(props: {
  handle: SubtitleEditorHandle;
  // The conversation's library scope for the linked-media picker (empty when
  // the conversation belongs to no project — the picker then covers the
  // conversation only).
  libraryID?: string;
  // True while the editor is hidden beneath another screen (Settings). Pauses
  // playback and detaches the global shortcuts; all edit state stays.
  suspended?: boolean;
  onClose: () => void;
  onOpenParent: (conversationID: string) => void;
  onDirtyChange?: (dirty: boolean) => void;
  // Fired after a save lands a new artifact on the conversation, so the
  // parent can refresh the sidebar and the assets panel (the audio editor's
  // onResultAdded counterpart).
  onSavedToConversation?: () => void;
}) {
  const {source} = props.handle;
  const sourceArtifactID = useState(source.artifactId)[0];
  const sourceConversationID = useState(source.conversationId)[0];

  const [doc, setDoc] = useState<SubtitleDoc>(() => ({
    format: (source.format === 'vtt' || source.format === 'srt' ? source.format : 'vtt') as SubtitleFormat,
    cues: (source.cues ?? []).map((cue) => ({...cue})),
  }));
  const [baseline, setBaseline] = useState<SubtitleDoc>(() => ({
    format: (source.format === 'vtt' || source.format === 'srt' ? source.format : 'vtt') as SubtitleFormat,
    cues: (source.cues ?? []).map((cue) => ({...cue})),
  }));
  const [undoStack, setUndoStack] = useState<SubtitleDoc[]>([]);
  const [redoStack, setRedoStack] = useState<SubtitleDoc[]>([]);
  const [selected, setSelected] = useState(0);
  const [editingText, setEditingText] = useState(false);

  const [loadError, setLoadError] = useState('');
  const [exportFormat, setExportFormat] = useState<SubtitleFormat>(
    (source.format === 'vtt' || source.format === 'srt' ? source.format : 'vtt') as SubtitleFormat,
  );
  const [log, setLog] = useState<EditorLogEntry[]>([]);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState('');

  const [find, setFind] = useState('');
  const [replace, setReplace] = useState('');

  const [media, setMedia] = useState<SubtitleAsset | null>(null);
  const [mediaKind, setMediaKind] = useState<'video' | 'audio'>('video');
  const [mediaList, setMediaList] = useState<SubtitleAsset[]>([]);
  const [pickerOpen, setPickerOpen] = useState(false);
  const [pickerQuery, setPickerQuery] = useState('');
  const [offset, setOffset] = useState(0);
  const [follow, setFollow] = useState(true);
  const [loopCue, setLoopCue] = useState(false);
  const mediaRef = useRef<HTMLVideoElement | HTMLAudioElement | null>(null);
  const [mediaTime, setMediaTime] = useState(0);

  const listRef = useRef<HTMLDivElement | null>(null);
  const dirty = !sameSubtitleDoc(doc, baseline);
  const warnings = useMemo(() => subtitleWarnings(doc), [doc]);

  // commit folds one edit into the document: the previous doc lands on the
  // undo stack (capped), redo clears.
  const commit = (next: SubtitleDoc) => {
    setUndoStack((stack) => [...stack, doc].slice(-undoLimit));
    setRedoStack([]);
    setDoc(next);
  };
  const undo = () => {
    setUndoStack((stack) => {
      if (!stack.length) return stack;
      const previous = stack[stack.length - 1];
      setRedoStack((redo) => [...redo, doc].slice(-undoLimit));
      setDoc(previous);
      return stack.slice(0, -1);
    });
  };
  const redo = () => {
    setRedoStack((stack) => {
      if (!stack.length) return stack;
      const next = stack[stack.length - 1];
      setUndoStack((undo) => [...undo, doc].slice(-undoLimit));
      setDoc(next);
      return stack.slice(0, -1);
    });
  };

  useEffect(() => {
    props.onDirtyChange?.(dirty);
  }, [dirty]);

  useEffect(() => {
    if (!props.suspended) {
      return;
    }
    mediaRef.current?.pause();
  }, [props.suspended]);

  // The linked-media candidates: the conversation's assets, widened to the
  // library when the conversation belongs to one — the same dual scope the
  // composer's assets panel and @-mentions use.
  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      try {
        const conversationAssets = asArray(await AppBindings.ListConversationAssets(sourceConversationID));
        let combined = conversationAssets;
        if (props.libraryID) {
          const libraryAssets = asArray(await AppBindings.ListLibraryAssets(props.libraryID));
          const seen = new Set(combined.map((asset) => asset.id));
          combined = [...combined, ...libraryAssets.filter((asset) => !seen.has(asset.id))];
        }
        if (!cancelled) {
          setMediaList(combined);
          setMedia((current) => current ?? newestLinkableAsset(combined));
        }
      } catch (error) {
        if (!cancelled) {
          setLoadError(`Loading the conversation's media failed: ${formatEditorError(error)}`);
        }
      }
    };
    void load();
    return () => {
      cancelled = true;
    };
  }, [sourceConversationID, props.libraryID]);

  const activeIndex = activeSubtitleIndex(doc, mediaTime, offset);

  // Follow playback: keep the active cue visible (unless the reviewer is
  // reading elsewhere).
  useEffect(() => {
    if (!follow || activeIndex < 0 || props.suspended) {
      return;
    }
    const row = listRef.current?.querySelector(`[data-cue-index="${activeIndex}"]`);
    row?.scrollIntoView({block: 'nearest'});
  }, [activeIndex, follow, props.suspended]);

  // Loop the active cue: playback wraps at the cue's edges (media time minus
  // the session offset, so the offset applies here too).
  useEffect(() => {
    const element = mediaRef.current;
    if (!element || !loopCue || activeIndex < 0) {
      return;
    }
    const cue = doc.cues[activeIndex];
    const start = Math.max(0, cue.startSeconds + offset);
    const end = cue.endSeconds + offset;
    if (element.currentTime < start - 0.05 || element.currentTime > end) {
      element.currentTime = Math.max(0, start);
    }
  }, [mediaTime, loopCue, activeIndex, offset, doc]);

  const seekToCue = (index: number) => {
    const cue = doc.cues[index];
    if (!cue) return;
    const element = mediaRef.current;
    setSelected(index);
    if (element) {
      element.currentTime = Math.max(0, cue.startSeconds + offset);
      void element.play().catch(() => {});
    }
  };

  // saveDoc commits the edited doc into the conversation: a new trc_ artifact
  // plus a turn recording the edit — the media editors' "render + adopt"
  // door, minus the render. The machine draft is never touched.
  const saveDoc = async () => {
    setSaving(true);
    setSaveError('');
    try {
      const result = await AppBindings.SaveSubtitleEdit(main.SaveSubtitleEditRequest.createFrom({
        conversationId: sourceConversationID,
        sourceArtifactId: sourceArtifactID,
        format: exportFormat,
        cues: doc.cues,
      }));
      setLog((entries) => [
        {format: result.format, cueCount: result.cueCount, at: new Date().toLocaleTimeString(), artifactId: result.artifactId, url: result.url},
        ...entries,
      ]);
      props.onSavedToConversation?.();
    } catch (error) {
      setSaveError(formatEditorError(error));
    } finally {
      setSaving(false);
    }
  };

  const downloadSaved = async (url: string, index: number) => {
    try {
      await SaveTranscript(main.SaveTranscriptRequest.createFrom({
        path: url,
        suggestedName: `${source.conversationTitle || 'subtitles'}-${Date.now()}-${index + 1}`,
      }));
    } catch (error) {
      setSaveError(formatEditorError(error));
    }
  };

  // Global shortcuts, detached while suspended and skipped for text targets.
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (props.suspended) {
        return;
      }
      const target = event.target as HTMLElement | null;
      if (target && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.tagName === 'SELECT' || target.tagName === 'BUTTON' || target.closest?.('[role="dialog"]'))) {
        return;
      }
      const step = event.shiftKey ? 1 : 0.1;
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'z') {
        event.preventDefault();
        if (event.shiftKey) {
          redo();
        } else {
          undo();
        }
        return;
      }
      if (event.key === ' ') {
        event.preventDefault();
        const element = mediaRef.current;
        if (element) {
          if (element.paused) {
            void element.play().catch(() => {});
          } else {
            element.pause();
          }
        }
        return;
      }
      if (event.key === 'ArrowUp') {
        event.preventDefault();
        setSelected((current) => Math.max(0, current - 1));
        return;
      }
      if (event.key === 'ArrowDown') {
        event.preventDefault();
        setSelected((current) => Math.min(doc.cues.length - 1, current + 1));
        return;
      }
      if (event.key === '[') {
        event.preventDefault();
        commit(nudgeCueEdge(doc, selected, 'start', -step));
        return;
      }
      if (event.key === ']') {
        event.preventDefault();
        commit(nudgeCueEdge(doc, selected, 'start', step));
        return;
      }
      if (event.key === '{') {
        event.preventDefault();
        commit(nudgeCueEdge(doc, selected, 'end', -step));
        return;
      }
      if (event.key === '}') {
        event.preventDefault();
        commit(nudgeCueEdge(doc, selected, 'end', step));
        return;
      }
      if (event.key === 'Enter') {
        event.preventDefault();
        setEditingText(true);
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [props.suspended, doc, selected, undoStack, redoStack]);

  const startEdit = (index: number) => {
    setSelected(index);
    setEditingText(true);
  };

  const commitText = (index: number, text: string) => {
    setEditingText(false);
    if (doc.cues[index] && doc.cues[index].text !== text) {
      commit(updateCueText(doc, index, text));
    }
  };

  const commitTime = (index: number, edge: 'start' | 'end', raw: string) => {
    const cue = doc.cues[index];
    if (!cue) return;
    const parsed = parseClockInput(raw);
    if (parsed === null) return;
    commit(updateCueTimes(
      doc,
      index,
      edge === 'start' ? parsed : cue.startSeconds,
      edge === 'end' ? parsed : cue.endSeconds,
    ));
  };

  const runReplace = () => {
    const result = replaceAllInCues(doc, find, replace);
    if (result.count > 0) {
      commit(result.doc);
    }
    setReplace((current) => current);
    setLastReplaceCount(result.count);
  };
  const [lastReplaceCount, setLastReplaceCount] = useState<number | null>(null);

  const pickerCandidates = useMemo(() => {
    const query = pickerQuery.trim().toLowerCase();
    const seen = new Set<string>();
    return mediaList
      .filter((asset) => (asset.kind === 'video' || asset.kind === 'audio') && asset.url && !seen.has(asset.id) && seen.add(asset.id))
      .filter((asset) => !query || (asset.conversationTitle || '').toLowerCase().includes(query) || asset.id.toLowerCase().includes(query))
      .sort((a, b) => ((a.createdAt ?? '') === (b.createdAt ?? '') ? (a.id < b.id ? 1 : -1) : (a.createdAt ?? '') < (b.createdAt ?? '') ? 1 : -1));
  }, [mediaList, pickerQuery]);

  const warningCount = warnings.length;
  const spanSeconds = doc.cues.length ? Math.max(doc.cues[doc.cues.length - 1].endSeconds, 0) : 0;

  return (
    <section className="subtitle-editor">
      <EditorHeader
        title="Subtitle Editor"
        parentAvailable
        parentConversationID={sourceConversationID}
        onClose={props.onClose}
        onOpenParent={props.onOpenParent}
      />

      {loadError ? <div className="editor-error">{loadError}</div> : null}

      <div className="editor-body">
        <div className="subtitle-editor-main">
          <div className="subtitle-media-row">
            {media ? (
              media.kind === 'video' ? (
                <video
                  ref={mediaRef as React.RefObject<HTMLVideoElement>}
                  className="subtitle-media"
                  src={media.url}
                  controls
                  onTimeUpdate={(event) => setMediaTime(event.currentTarget.currentTime)}
                />
              ) : (
                <audio
                  ref={mediaRef as React.RefObject<HTMLAudioElement>}
                  className="subtitle-media"
                  src={media.url}
                  controls
                  onTimeUpdate={(event) => setMediaTime(event.currentTarget.currentTime)}
                />
              )
            ) : (
              <span className="subtitle-media-empty">No media linked — editing without playback.</span>
            )}
            <div className="subtitle-media-controls">
              <button
                type="button"
                className="subtitle-media-source"
                onClick={() => setPickerOpen((open) => !open)}
                title="Choose the clip this transcript plays against — from this conversation or its library"
              >
                {media ? `Linked: ${media.id.slice(0, 12)} (${media.kind})` : 'Link media'}
              </button>
              <label className="subtitle-offset" title="Session sync offset: shifts playback against the cues without editing them. Media time = cue time + offset.">
                offset
                <button type="button" onClick={() => setOffset((value) => Math.round((value - 0.1) * 10) / 10)}>-0.1s</button>
                <span>{offset >= 0 ? '+' : ''}{offset.toFixed(1)}s</span>
                <button type="button" onClick={() => setOffset((value) => Math.round((value + 0.1) * 10) / 10)}>+0.1s</button>
              </label>
              <label className="subtitle-toggle" title="Keep the active cue visible while playback runs">
                <input type="checkbox" checked={follow} onChange={(event) => setFollow(event.target.checked)} /> follow
              </label>
              <label className="subtitle-toggle" title="Restart playback at the active cue's start whenever it runs past the cue's end">
                <input type="checkbox" checked={loopCue} onChange={(event) => setLoopCue(event.target.checked)} /> loop cue
              </label>
            </div>
            {pickerOpen ? (
              <div className="subtitle-media-picker" role="dialog" aria-label="Choose linked media">
                <input
                  type="text"
                  value={pickerQuery}
                  placeholder="Filter by conversation or id…"
                  onChange={(event) => setPickerQuery(event.target.value)}
                />
                <ul>
                  <li>
                    <button type="button" onClick={() => { setMedia(null); setPickerOpen(false); setOffset(0); }}>
                      None — edit without playback
                    </button>
                  </li>
                  {pickerCandidates.map((asset) => (
                    <li key={`${asset.id}-${asset.conversationId}`}>
                      <button
                        type="button"
                        className={media?.id === asset.id ? 'selected' : ''}
                        onClick={() => {
                          setMedia(asset);
                          setMediaKind(asset.kind === 'audio' ? 'audio' : 'video');
                          setOffset(0);
                          setPickerOpen(false);
                        }}
                      >
                        <span className="subtitle-picker-name">{asset.id.slice(0, 16)} · {asset.kind}</span>
                        <span className="subtitle-picker-provenance">
                          {asset.role === 'user' ? 'attached' : 'generated'} · {asset.conversationTitle || 'another chat'}
                        </span>
                      </button>
                    </li>
                  ))}
                  {pickerCandidates.length === 0 ? <li><span className="subtitle-picker-empty">No video or audio assets found.</span></li> : null}
                </ul>
              </div>
            ) : null}
          </div>

          <div className="subtitle-cue-list" ref={listRef}>
            {doc.cues.map((cue, index) => (
              <div
                key={index}
                data-cue-index={index}
                className={`subtitle-cue${index === selected ? ' selected' : ''}${index === activeIndex ? ' active' : ''}`}
                onClick={() => setSelected(index)}
              >
                <span className="subtitle-cue-index">{index + 1}</span>
                <div className="subtitle-cue-times">
                  <button
                    type="button"
                    className="subtitle-cue-time"
                    title="Edit start time (or press [ / ] with the cue selected)"
                    onBlur={(event) => commitTime(index, 'start', event.currentTarget.textContent || '')}
                    onKeyDown={(event) => {
                      if (event.key === 'Enter') {
                        event.preventDefault();
                        event.currentTarget.blur();
                      }
                    }}
                    contentEditable
                    suppressContentEditableWarning
                  >
                    {formatSubtitleClock(cue.startSeconds, doc.format)}
                  </button>
                  <span>→</span>
                  <button
                    type="button"
                    className="subtitle-cue-time"
                    title="Edit end time (or press { / } with the cue selected)"
                    onBlur={(event) => commitTime(index, 'end', event.currentTarget.textContent || '')}
                    onKeyDown={(event) => {
                      if (event.key === 'Enter') {
                        event.preventDefault();
                        event.currentTarget.blur();
                      }
                    }}
                    contentEditable
                    suppressContentEditableWarning
                  >
                    {formatSubtitleClock(cue.endSeconds, doc.format)}
                  </button>
                </div>
                {editingText && index === selected ? (
                  <textarea
                    className="subtitle-cue-text editing"
                    autoFocus
                    defaultValue={cue.text}
                    onBlur={(event) => commitText(index, event.target.value)}
                    onKeyDown={(event) => {
                      if (event.key === 'Escape') {
                        setEditingText(false);
                      }
                      if (event.key === 'Enter' && (event.metaKey || event.ctrlKey)) {
                        commitText(index, event.currentTarget.value);
                      }
                    }}
                  />
                ) : (
                  <button type="button" className="subtitle-cue-text" onDoubleClick={() => startEdit(index)}>
                    {cue.text || <em>(empty — double-click to edit)</em>}
                  </button>
                )}
                <div className="subtitle-cue-actions">
                  <button type="button" title="Split at playhead-mapped time" onClick={() => {
                    const element = mediaRef.current;
                    const at = element ? Math.max(cue.startSeconds, Math.min(cue.endSeconds, element.currentTime - offset)) : (cue.startSeconds + cue.endSeconds) / 2;
                    commit(splitSubtitleCue(doc, index, at));
                  }}>Split</button>
                  <button type="button" title="Merge with the next cue" onClick={() => commit(mergeCueWithNext(doc, index))}>Merge</button>
                  <button type="button" title="Add a cue below" onClick={() => commit(insertSubtitleCueAfter(doc, index))}>Add</button>
                  <button type="button" title="Delete this cue" onClick={() => commit(deleteSubtitleCue(doc, index))}>✕</button>
                </div>
              </div>
            ))}
            {doc.cues.length === 0 ? <p className="subtitle-cue-empty">No cues — add one to begin.</p> : null}
          </div>
        </div>

        <aside className="editor-inspector subtitle-inspector">
          <div className="subtitle-framing-summary" aria-live="polite">
            <strong>{doc.cues.length} cues · {spanSeconds.toFixed(1)}s span</strong>
            <span>{doc.format.toUpperCase()} · imported from {sourceArtifactID.slice(0, 12)}{dirty ? ' · edited' : ''}</span>
          </div>

          <div className="subtitle-warnings">
            <strong>{warningCount === 0 ? 'No warnings' : `${warningCount} warning${warningCount === 1 ? '' : 's'}`}</strong>
            <ul>
              {warnings.slice(0, 30).map((warning, i) => (
                <li key={i}>
                  <button type="button" onClick={() => {
                    setSelected(warning.index);
                    listRef.current?.querySelector(`[data-cue-index="${warning.index}"]`)?.scrollIntoView({block: 'center'});
                  }}>{warning.message}</button>
                </li>
              ))}
              {warnings.length > 30 ? <li>…and {warnings.length - 30} more</li> : null}
            </ul>
          </div>

          <div className="subtitle-search">
            <input
              type="text"
              placeholder="Find in cues…"
              value={find}
              onChange={(event) => { setFind(event.target.value); setLastReplaceCount(null); }}
            />
            <input
              type="text"
              placeholder="Replace with…"
              value={replace}
              onChange={(event) => setReplace(event.target.value)}
            />
            <button type="button" disabled={!find} onClick={runReplace}>Replace all</button>
            {lastReplaceCount !== null ? <span className="hint">{lastReplaceCount} replaced</span> : null}
          </div>

          <div className="editor-tool-group">
            <button type="button" disabled={!undoStack.length} onClick={undo}>Undo</button>
            <button type="button" disabled={!redoStack.length} onClick={redo}>Redo</button>
            <button type="button" disabled={dirty === false} onClick={() => { setDoc(cloneSubtitleDoc(baseline)); setUndoStack([]); setRedoStack([]); }}>Reset</button>
          </div>
          <p className="hint">Space plays · ↑/↓ pick a cue · Enter edits its text · [ ] and {'{ }'} nudge the edges (Shift = 1s)</p>

          <div className="subtitle-export">
            <label>
              Format
              <select value={exportFormat} onChange={(event) => setExportFormat(event.target.value as SubtitleFormat)}>
                <option value="vtt">WebVTT (.vtt)</option>
                <option value="srt">SubRip (.srt)</option>
              </select>
            </label>
            <button
              type="button"
              className="editor-generate"
              disabled={saving || doc.cues.length === 0}
              title="Write the edited subtitles back into the conversation as a new artifact — the original stays intact. Download the saved file from the Edits panel below."
              onClick={() => void saveDoc()}
            >
              {saving ? 'Saving…' : `Save ${exportFormat.toUpperCase()} to conversation`}
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
                  Saved · {entry.format.toUpperCase()} · {entry.cueCount} cues · {entry.artifactId.slice(0, 12)}
                </span>
                <span className="editor-op-sub" title={entry.url}>{entry.at} → attached to the conversation</span>
              </div>
              <button type="button" onClick={() => void downloadSaved(entry.url, index)}>Download</button>
            </li>
          ))}
        </ul>
      </EditsPanel>
    </section>
  );
}

// parseClockInput reads the editable time cells: both formats' clock forms
// and bare seconds. Null for anything unparsable (the cell keeps its value).
function parseClockInput(raw: string): number | null {
  const text = raw.trim();
  if (!text) return null;
  const normalized = text.replace(',', '.');
  if (/^\d+(\.\d+)?$/.test(normalized)) {
    return Math.max(0, parseFloat(normalized));
  }
  const parts = normalized.split(':');
  if (parts.length < 2 || parts.length > 3) return null;
  let seconds = 0;
  for (const part of parts) {
    if (!/^\d+(\.\d+)?$/.test(part)) return null;
    seconds = seconds * 60 + parseFloat(part);
  }
  return Math.max(0, seconds);
}

// SubtitleCue re-export keeps the cue type importable without reaching into
// the model from call sites that already hold the editor.
export type {SubtitleCue};

function asArray<T>(value: T[] | null | undefined): T[] {
  return Array.isArray(value) ? value : [];
}
