import {useEffect, useMemo, useRef, useState} from 'react';
import * as AppBindings from '../../wailsjs/go/main/App';
import {SaveComposition} from '../../wailsjs/go/main/App';
import {main} from '../../wailsjs/go/models';
import {EditsPanel} from './EditsPanel';
import {EditorHeader} from './EditorHeader';
import {MediaPicker} from './MediaPicker';
import {
  CompositionDoc,
  CompositionFit,
  addClip,
  addDuck,
  addLane,
  addSegmentToLane,
  audioOverhangSeconds,
  clipDurationSeconds,
  compositionAudioExtent,
  compositionClipLayout,
  compositionMaxAudioLanes,
  compositionProblems,
  compositionSequenceDuration,
  compositionTransitionMinSeconds,
  detachClipAudio,
  duplicateClip,
  duplicateSegment,
  formatCompositionClock,
  moveClip,
  moveSegment,
  normalizeCompositionDoc,
  normalizeCompositionMediaRef,
  reattachClipAudio,
  removeClip,
  removeDuck,
  removeLane,
  removeSegment,
  renameComposition,
  renameLane,
  resolveOutputFrame,
  sameCompositionDoc,
  segmentDurationSeconds,
  setClipAudio,
  setClipRange,
  setClipTransition,
  setCompositionOutput,
  setLaneMix,
  setSegmentMix,
  setSegmentRange,
  toWireDoc,
  transitionBounds,
  updateDuck,
} from './compositionModel';
import './compositionEditor.css';

export type CompositionEditorHandle = {
  source: main.CompositionSourceInfo;
  // The composition conversation's library scope for the media picker,
  // resolved from its own record when the editor opens — the openSubtitleEditor
  // rule: a project conversation outside the sidebar's recency window keeps
  // its library scope, an independent conversation passes none.
  libraryID: string;
};

type EditorLogEntry = {
  name: string;
  clipCount: number;
  at: string;
  artifactId: string;
  url: string;
};

type Selection =
  | {kind: 'doc'}
  | {kind: 'clip'; clipID: string}
  | {kind: 'lane'; laneID: string}
  | {kind: 'segment'; laneID: string; segmentID: string};

// The Edits panel's height clamp needs this editor's content floor: header +
// toolbar + the timeline's two-row minimum + paddings.
const editsPanelBodyReservePx = 700;
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

function asArray<T>(value: T[] | null | undefined): T[] {
  return Array.isArray(value) ? value : [];
}

// NumberField commits on blur or Enter, so typing "0." mid-edit never fights
// the clamps. Unparsable input reverts silently.
function NumberField(props: {label: string; value: number; step?: number; min?: number; max?: number; title?: string; onCommit: (value: number) => void}) {
  const [draft, setDraft] = useState(String(props.value));
  useEffect(() => {
    setDraft(String(props.value));
  }, [props.value]);
  const commit = () => {
    const parsed = parseFloat(draft);
    if (Number.isFinite(parsed) && parsed !== props.value) {
      props.onCommit(parsed);
    } else {
      setDraft(String(props.value));
    }
  };
  return (
    <label className="composition-field" title={props.title}>
      <span>{props.label}</span>
      <input
        type="number"
        value={draft}
        step={props.step ?? 0.1}
        min={props.min}
        max={props.max}
        onChange={(event) => setDraft(event.target.value)}
        onBlur={commit}
        onKeyDown={(event) => {
          if (event.key === 'Enter') {
            event.preventDefault();
            event.currentTarget.blur();
          }
        }}
      />
    </label>
  );
}

export function CompositionEditor(props: {
  handle: CompositionEditorHandle;
  libraryID?: string;
  // True while hidden beneath another screen (Settings): shortcuts detach,
  // all edit state stays.
  suspended?: boolean;
  onClose: () => void;
  onOpenParent: (conversationID: string) => void;
  onDirtyChange?: (dirty: boolean) => void;
  // Fired after a save lands a new artifact on the conversation so the parent
  // can refresh the sidebar and the assets panel.
  onSavedToConversation?: () => void;
}) {
  const {source} = props.handle;
  const sourceConversationID = useState(source.conversationId)[0];

  const [doc, setDoc] = useState<CompositionDoc>(() => normalizeCompositionDoc(source.doc));
  const [baseline, setBaseline] = useState<CompositionDoc>(() => normalizeCompositionDoc(source.doc));
  const [undoStack, setUndoStack] = useState<CompositionDoc[]>([]);
  const [redoStack, setRedoStack] = useState<CompositionDoc[]>([]);
  const [selection, setSelection] = useState<Selection>({kind: 'doc'});
  const [nameDraft, setNameDraft] = useState(() => normalizeCompositionDoc(source.doc).name);

  const [activeArtifactID, setActiveArtifactID] = useState(source.artifactId ?? '');
  const [loadingRevision, setLoadingRevision] = useState(false);
  const [loadError, setLoadError] = useState('');
  const [missingRefs, setMissingRefs] = useState<Set<string>>(() => new Set(asArray(source.missingRefs)));

  const [mediaAssets, setMediaAssets] = useState<main.ConversationAsset[]>([]);
  const [pickerOpen, setPickerOpen] = useState(false);
  const [pickerError, setPickerError] = useState('');
  const [addingIDs, setAddingIDs] = useState<string[]>([]);

  const [log, setLog] = useState<EditorLogEntry[]>([]);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState('');

  // A ref mirror of the doc so async picker adds always commit against the
  // newest state (the video editor's stale-resolve discipline).
  const docRef = useRef(doc);
  useEffect(() => {
    docRef.current = doc;
  }, [doc]);

  const dirty = !sameCompositionDoc(doc, baseline);
  const layout = useMemo(() => compositionClipLayout(doc), [doc]);
  const sequenceDuration = layout.duration;
  const audioExtent = useMemo(() => compositionAudioExtent(doc), [doc]);
  const timelineExtent = Math.max(sequenceDuration, audioExtent, 1);
  const overhang = useMemo(() => audioOverhangSeconds(doc), [doc]);
  const problems = useMemo(() => compositionProblems(doc), [doc]);
  const frame = useMemo(() => resolveOutputFrame(doc), [doc]);
  const refCounts = useMemo(() => {
    const counts: Record<string, number> = {};
    for (const clip of doc.video) counts[clip.ref.artifactId] = (counts[clip.ref.artifactId] ?? 0) + 1;
    for (const lane of doc.audioLanes) {
      for (const segment of lane.segments) counts[segment.ref.artifactId] = (counts[segment.ref.artifactId] ?? 0) + 1;
    }
    return counts;
  }, [doc]);

  // commit folds one edit into the document: the previous doc lands on the
  // undo stack (capped), redo clears.
  const commit = (next: CompositionDoc) => {
    setUndoStack((stack) => [...stack, docRef.current].slice(-undoLimit));
    setRedoStack([]);
    setDoc(next);
    docRef.current = next;
  };
  const undo = () => {
    setUndoStack((stack) => {
      if (!stack.length) return stack;
      const previous = stack[stack.length - 1];
      setRedoStack((redo) => [...redo, docRef.current].slice(-undoLimit));
      setDoc(previous);
      docRef.current = previous;
      return stack.slice(0, -1);
    });
  };
  const redo = () => {
    setRedoStack((stack) => {
      if (!stack.length) return stack;
      const next = stack[stack.length - 1];
      setUndoStack((history) => [...history, docRef.current].slice(-undoLimit));
      setDoc(next);
      docRef.current = next;
      return stack.slice(0, -1);
    });
  };

  useEffect(() => {
    props.onDirtyChange?.(dirty);
  }, [dirty]);

  // The picker's dual-scope candidates: the conversation's assets widened to
  // its library — the same fold the subtitle editor performs.
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
          setMediaAssets(combined);
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
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'z') {
        event.preventDefault();
        if (event.shiftKey) {
          redo();
        } else {
          undo();
        }
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [props.suspended, undoStack, redoStack]);

  const commitName = () => {
    const trimmed = nameDraft.trim();
    if (trimmed && trimmed !== doc.name) {
      commit(renameComposition(docRef.current, trimmed));
    } else {
      setNameDraft(doc.name);
    }
  };

  // addMedia resolves and measures one picked asset, then lands it: videos
  // append to the video lane, audio queues onto the selected lane when one is
  // selected, else the last lane (a lane is created when none exists) at the
  // sequence end — the picker's landing rules and the seed's are one shape.
  const addMedia = async (asset: main.ConversationAsset) => {
    setPickerError('');
    setAddingIDs((ids) => [...ids, asset.id]);
    try {
      const wireRef = await AppBindings.ResolveCompositionMedia(sourceConversationID, asset.id);
      const ref = normalizeCompositionMediaRef(wireRef);
      if (ref.kind === 'video') {
        commit(addClip(docRef.current, ref));
      } else {
        let next = docRef.current;
        let targetLane = selection.kind === 'lane' ? next.audioLanes.find((lane) => lane.id === selection.laneID) : undefined;
        if (!targetLane) {
          if (!next.audioLanes.length) {
            next = addLane(next);
          }
          targetLane = next.audioLanes[next.audioLanes.length - 1];
        }
        commit(addSegmentToLane(next, targetLane.id, ref, compositionSequenceDuration(next)));
      }
    } catch (error) {
      setPickerError(formatEditorError(error));
    } finally {
      setAddingIDs((ids) => ids.filter((id) => id !== asset.id));
    }
  };

  const saveDoc = async () => {
    setSaving(true);
    setSaveError('');
    try {
      const result = await AppBindings.SaveCompositionEdit(main.SaveCompositionEditRequest.createFrom({
        conversationId: sourceConversationID,
        sourceArtifactId: activeArtifactID || undefined,
        doc: toWireDoc(docRef.current),
      }));
      const normalized = normalizeCompositionDoc(result.doc);
      setLog((entries) => [
        {name: normalized.name, clipCount: normalized.video.length, at: new Date().toLocaleTimeString(), artifactId: result.artifactId, url: result.url},
        ...entries,
      ]);
      // Re-baseline on exactly what landed (the server's normalization wins).
      setDoc(normalized);
      setBaseline(normalized);
      docRef.current = normalized;
      setNameDraft(normalized.name);
      setActiveArtifactID(result.artifactId);
      props.onSavedToConversation?.();
    } catch (error) {
      setSaveError(formatEditorError(error));
    } finally {
      setSaving(false);
    }
  };

  // loadRevision is the save rows' "Use as source": an earlier revision
  // becomes the editing baseline. The current doc lands on the undo stack
  // first, so picking up an old revision is never destructive.
  const loadRevision = async (entry: EditorLogEntry) => {
    if (loadingRevision || entry.artifactId === activeArtifactID) return;
    setLoadingRevision(true);
    setLoadError('');
    try {
      const revision = await AppBindings.ResolveCompositionEditSource(sourceConversationID, entry.artifactId);
      const next = normalizeCompositionDoc(revision.doc);
      setUndoStack((stack) => [...stack, docRef.current].slice(-undoLimit));
      setRedoStack([]);
      setDoc(next);
      setBaseline(normalizeCompositionDoc(next));
      docRef.current = next;
      setNameDraft(next.name);
      setActiveArtifactID(revision.artifactId ?? entry.artifactId);
      setMissingRefs(new Set(asArray(revision.missingRefs)));
      setSelection({kind: 'doc'});
    } catch (error) {
      setLoadError(formatEditorError(error));
    } finally {
      setLoadingRevision(false);
    }
  };

  const downloadSaved = async (url: string, index: number) => {
    try {
      await SaveComposition(main.SaveTranscriptRequest.createFrom({
        path: url,
        suggestedName: `${doc.name.replace(/\s+/g, '-').toLowerCase() || 'composition'}-${Date.now()}-${index + 1}`,
      }));
    } catch (error) {
      setSaveError(formatEditorError(error));
    }
  };

  const pct = (seconds: number) => `${Math.max(0, Math.min(100, (seconds / timelineExtent) * 100))}%`;
  const selectedClip = selection.kind === 'clip' ? doc.video.find((clip) => clip.id === selection.clipID) : undefined;
  const selectedLane = selection.kind === 'lane' ? doc.audioLanes.find((lane) => lane.id === selection.laneID) : undefined;
  const selectedSegment = selection.kind === 'segment'
    ? doc.audioLanes.find((lane) => lane.id === selection.laneID)?.segments.find((segment) => segment.id === selection.segmentID)
    : undefined;
  const selectedSegmentLane = selection.kind === 'segment' ? doc.audioLanes.find((lane) => lane.id === selection.laneID) : undefined;

  return (
    <section className="composition-editor">
      <EditorHeader
        title="Video Composer"
        parentAvailable
        parentConversationID={sourceConversationID}
        onClose={props.onClose}
        onOpenParent={props.onOpenParent}
      />

      {loadError ? <div className="editor-error">{loadError}</div> : null}
      {asArray(source.warnings).length ? (
        <div className="composition-seed-warnings">
          {asArray(source.warnings).map((warning, index) => <span key={index}>{warning}</span>)}
        </div>
      ) : null}

      <div className="editor-body">
        <div className="composition-editor-main">
          <div className="composition-toolbar">
            <input
              type="text"
              className="composition-name"
              value={nameDraft}
              placeholder="Composition name"
              onChange={(event) => setNameDraft(event.target.value)}
              onBlur={commitName}
              onKeyDown={(event) => {
                if (event.key === 'Enter') {
                  event.preventDefault();
                  event.currentTarget.blur();
                }
              }}
            />
            <span className="composition-summary" aria-live="polite">
              {doc.video.length} clips · {formatCompositionClock(sequenceDuration)}
              {doc.audioLanes.length ? ` · ${doc.audioLanes.length} audio ${doc.audioLanes.length === 1 ? 'lane' : 'lanes'}` : ''}
              {` · ${frame.width}×${frame.height} @ ${Math.round(frame.fps)}fps ${doc.output.fit}`}
              {activeArtifactID ? ` · ${activeArtifactID.slice(0, 12)}${dirty ? ' · edited' : ''}` : ' · unsaved draft'}
            </span>
            {missingRefs.size ? <span className="composition-warning">⚠ {missingRefs.size} missing source{missingRefs.size === 1 ? '' : 's'}</span> : null}
            {overhang > 0 ? <span className="composition-warning" title="Audio past the video sequence end — the default export ends at the video boundary (AU-11)">♪ +{overhang.toFixed(1)}s audio tail</span> : null}
            <span className="composition-toolbar-spacer" />
            <button type="button" className="editor-generate" onClick={() => setPickerOpen((open) => !open)}>
              {pickerOpen ? 'Close media' : '＋ Add media'}
            </button>
            <button
              type="button"
              disabled={doc.audioLanes.length >= compositionMaxAudioLanes}
              title={doc.audioLanes.length >= compositionMaxAudioLanes ? `At most ${compositionMaxAudioLanes} audio lanes` : 'Add an audio lane'}
              onClick={() => {
                const next = addLane(docRef.current);
                commit(next);
                const lane = next.audioLanes[next.audioLanes.length - 1];
                if (lane) setSelection({kind: 'lane', laneID: lane.id});
              }}
            >
              ＋ Lane
            </button>
          </div>

          {pickerOpen ? (
            <MediaPicker
              assets={mediaAssets}
              editedConversationID={sourceConversationID}
              refCounts={refCounts}
              busyIDs={addingIDs}
              error={pickerError}
              onAdd={(asset) => void addMedia(asset)}
              onClose={() => setPickerOpen(false)}
            />
          ) : null}

          <div className="composition-timeline">
            <div className="composition-ruler">
              {Array.from({length: 7}, (_, index) => {
                const at = (timelineExtent * index) / 6;
                return <span key={index} style={{left: pct(at)}}>{formatCompositionClock(at)}</span>;
              })}
            </div>
            <div
              className={`composition-lane composition-lane-video${selection.kind === 'doc' ? ' focused' : ''}`}
              onClick={() => setSelection({kind: 'doc'})}
            >
              {doc.video.map((clip, index) => {
                const missing = missingRefs.has(clip.ref.artifactId);
                return (
                  <button
                    key={clip.id}
                    type="button"
                    className={`composition-clip${selection.kind === 'clip' && selection.clipID === clip.id ? ' selected' : ''}${missing ? ' missing' : ''}${clip.audio.muted ? ' audio-muted' : ''}${clip.audio.detached ? ' audio-detached' : ''}`}
                    style={{left: pct(layout.starts[index]), width: pct(clipDurationSeconds(clip))}}
                    title={`${clip.ref.artifactId} · ${formatCompositionClock(clipDurationSeconds(clip))}${missing ? ' · source file missing' : ''}`}
                    onClick={(event) => {
                      event.stopPropagation();
                      setSelection({kind: 'clip', clipID: clip.id});
                    }}
                  >
                    <span className="composition-clip-name">{clip.ref.artifactId.slice(0, 12)}</span>
                    <span className="composition-clip-duration">{formatCompositionClock(clipDurationSeconds(clip))}</span>
                  </button>
                );
              })}
              {doc.video.slice(0, -1).map((clip, index) => {
                const junctionAt = layout.starts[index + 1];
                const transition = clip.transitionToNext;
                return (
                  <button
                    key={`junction-${clip.id}`}
                    type="button"
                    className={`composition-junction${transition ? ' dissolve' : ' cut'}${selection.kind === 'clip' && selection.clipID === clip.id ? ' selected' : ''}`}
                    style={{left: pct(junctionAt), width: transition ? pct(transition.seconds) : '2px'}}
                    title={transition
                      ? `Dissolve ${transition.seconds.toFixed(1)}s (audio ${transition.audioCrossfade ? 'crossfades' : 'cuts'}) — select the left clip to edit`
                      : 'Cut — select the left clip to add a transition'}
                    onClick={(event) => {
                      event.stopPropagation();
                      setSelection({kind: 'clip', clipID: clip.id});
                    }}
                  >
                    {transition ? `${transition.seconds.toFixed(1)}s` : '∥'}
                  </button>
                );
              })}
              {doc.video.length === 0 ? <span className="composition-lane-empty">No clips — add media from the picker.</span> : null}
            </div>
            {doc.audioLanes.map((lane) => (
              <div
                key={lane.id}
                className={`composition-lane composition-lane-audio${selection.kind === 'lane' && selection.laneID === lane.id ? ' selected' : ''}${lane.muted ? ' muted' : ''}`}
                onClick={() => setSelection({kind: 'lane', laneID: lane.id})}
              >
                <span className="composition-lane-label" title={`${lane.name}${lane.muted ? ' (muted)' : ''}`}>
                  {lane.name}{lane.gainDb !== 0 ? ` ${lane.gainDb > 0 ? '+' : ''}${lane.gainDb}dB` : ''}
                </span>
                <div className="composition-lane-track">
                  {lane.segments.map((segment) => (
                    <button
                      key={segment.id}
                      type="button"
                      className={`composition-segment${selection.kind === 'segment' && selection.segmentID === segment.id ? ' selected' : ''}${missingRefs.has(segment.ref.artifactId) ? ' missing' : ''}${segment.ducks.length ? ' ducked' : ''}`}
                      style={{left: pct(segment.startSeconds), width: pct(segmentDurationSeconds(segment))}}
                      title={`${segment.ref.artifactId} · ${formatCompositionClock(segmentDurationSeconds(segment))}${segment.ducks.length ? ` · ${segment.ducks.length} duck${segment.ducks.length === 1 ? '' : 's'}` : ''}`}
                      onClick={(event) => {
                        event.stopPropagation();
                        setSelection({kind: 'segment', laneID: lane.id, segmentID: segment.id});
                      }}
                    >
                      {segment.ref.artifactId.slice(0, 10)}
                    </button>
                  ))}
                </div>
              </div>
            ))}
          </div>
        </div>

        <aside className="editor-inspector composition-inspector">
          {selection.kind === 'doc' || !selectedClip ? (
            <div className="composition-inspect-doc">
              <strong>Project</strong>
              <div className="composition-field-row">
                <label className="composition-field" title="How differently-shaped clips conform to the output frame — pad letterboxes, crop center-crops, blur fills the margin with an enlarged blurred copy">
                  <span>Fit</span>
                  <select value={doc.output.fit} onChange={(event) => commit(setCompositionOutput(docRef.current, {fit: event.target.value as CompositionFit}))}>
                    <option value="pad">pad</option>
                    <option value="crop">crop</option>
                    <option value="blur">blur</option>
                  </select>
                </label>
                <NumberField label="Width" value={doc.output.width} step={2} min={0} title="0 inherits the first clip's width" onCommit={(value) => commit(setCompositionOutput(docRef.current, {width: value}))} />
                <NumberField label="Height" value={doc.output.height} step={2} min={0} title="0 inherits the first clip's height" onCommit={(value) => commit(setCompositionOutput(docRef.current, {height: value}))} />
                <NumberField label="FPS" value={doc.output.fps} step={1} min={0} max={60} title="0 inherits the first clip's frame rate" onCommit={(value) => commit(setCompositionOutput(docRef.current, {fps: value}))} />
              </div>
              <p className="hint">Output frame: {frame.width}×{frame.height} @ {Math.round(frame.fps)}fps · {doc.output.fit}</p>
              <div className="composition-problems">
                <strong>{problems.length === 0 ? 'No problems' : `${problems.length} problem${problems.length === 1 ? '' : 's'}`}</strong>
                {problems.length ? (
                  <ul>{problems.map((problem, index) => <li key={index}>{problem}</li>)}</ul>
                ) : (
                  <p className="hint">Select a clip, lane, or segment to edit it. Preview renders arrive with the next milestone.</p>
                )}
              </div>
            </div>
          ) : (
            <div className="composition-inspect-clip">
              <strong>Clip {doc.video.findIndex((clip) => clip.id === selectedClip.id) + 1}</strong>
              <p className="hint composition-ref-line" title={selectedClip.ref.path}>
                {selectedClip.ref.artifactId} · {selectedClip.ref.width ?? '?'}×{selectedClip.ref.height ?? '?'} · {formatCompositionClock(selectedClip.ref.durationSeconds)}
                {selectedClip.ref.conversationId && selectedClip.ref.conversationId !== sourceConversationID ? ' · another chat' : ''}
              </p>
              <div className="composition-field-row">
                <NumberField label="In (s)" value={selectedClip.inSeconds} step={0.1} min={0} onCommit={(value) => commit(setClipRange(docRef.current, selectedClip.id, value, selectedClip.outSeconds))} />
                <NumberField label="Out (s)" value={selectedClip.outSeconds} step={0.1} onCommit={(value) => commit(setClipRange(docRef.current, selectedClip.id, selectedClip.inSeconds, value))} />
              </div>
              <p className="hint">Plays {formatCompositionClock(clipDurationSeconds(selectedClip))} starting at {formatCompositionClock(layout.starts[doc.video.findIndex((clip) => clip.id === selectedClip.id)] ?? 0)}</p>

              <div className="editor-tool-group">
                <button type="button" disabled={doc.video.findIndex((clip) => clip.id === selectedClip.id) === 0} onClick={() => commit(moveClip(docRef.current, selectedClip.id, doc.video.findIndex((clip) => clip.id === selectedClip.id) - 1))}>↑ Move earlier</button>
                <button type="button" disabled={doc.video.findIndex((clip) => clip.id === selectedClip.id) === doc.video.length - 1} onClick={() => commit(moveClip(docRef.current, selectedClip.id, doc.video.findIndex((clip) => clip.id === selectedClip.id) + 2))}>↓ Move later</button>
                <button type="button" onClick={() => commit(duplicateClip(docRef.current, selectedClip.id))}>Duplicate</button>
                <button type="button" onClick={() => {commit(removeClip(docRef.current, selectedClip.id)); setSelection({kind: 'doc'});}}>Remove</button>
              </div>

              <div className="composition-transition">
                <strong>Transition to next</strong>
                {(() => {
                  const index = doc.video.findIndex((clip) => clip.id === selectedClip.id);
                  const bounds = transitionBounds(doc, index);
                  if (index >= doc.video.length - 1) {
                    return <p className="hint">The last clip has no next neighbor.</p>;
                  }
                  if (!bounds) {
                    return <p className="hint">The adjacent clips are too short for even the minimum {compositionTransitionMinSeconds}s dissolve — trim elsewhere first.</p>;
                  }
                  const transition = selectedClip.transitionToNext;
                  return (
                    <>
                      <div className="composition-field-row">
                        <label className="composition-field">
                          <span>Kind</span>
                          <select
                            value={transition ? 'dissolve' : 'cut'}
                            onChange={(event) => commit(setClipTransition(docRef.current, selectedClip.id, event.target.value === 'dissolve' ? {seconds: transition?.seconds ?? 1, audioCrossfade: transition?.audioCrossfade ?? true} : null))}
                          >
                            <option value="cut">cut</option>
                            <option value="dissolve">dissolve</option>
                          </select>
                        </label>
                        {transition ? (
                          <NumberField
                            label="Seconds"
                            value={transition.seconds}
                            step={0.1}
                            min={bounds.min}
                            max={bounds.max}
                            title={`The clips allow ${bounds.min.toFixed(1)}–${bounds.max.toFixed(1)}s`}
                            onCommit={(value) => commit(setClipTransition(docRef.current, selectedClip.id, {seconds: value, audioCrossfade: transition.audioCrossfade}))}
                          />
                        ) : null}
                      </div>
                      {transition ? (
                        <label className="composition-toggle" title="Crossfade the original audio across the dissolve (equal power), or cut it hard at the junction">
                          <input type="checkbox" checked={transition.audioCrossfade} onChange={(event) => commit(setClipTransition(docRef.current, selectedClip.id, {seconds: transition.seconds, audioCrossfade: event.target.checked}))} />
                          crossfade original audio
                        </label>
                      ) : null}
                      <p className="hint">Allowed {bounds.min.toFixed(1)}–{bounds.max.toFixed(1)}s (half the shorter clip).</p>
                    </>
                  );
                })()}
              </div>

              <div className="composition-audio-mix">
                <strong>Original audio</strong>
                {selectedClip.ref.hasAudio ? (
                  <>
                    <div className="composition-field-row">
                      <NumberField label="Gain (dB)" value={selectedClip.audio.gainDb} step={1} min={-60} max={12} onCommit={(value) => commit(setClipAudio(docRef.current, selectedClip.id, {gainDb: value}))} />
                    </div>
                    <label className="composition-toggle"><input type="checkbox" checked={selectedClip.audio.muted} onChange={(event) => commit(setClipAudio(docRef.current, selectedClip.id, {muted: event.target.checked}))} /> muted</label>
                    {selectedClip.audio.detached ? (
                      <button type="button" onClick={() => commit(reattachClipAudio(docRef.current, selectedClip.id))}>Reattach detached audio</button>
                    ) : (
                      <button type="button" title="Move this clip's sound onto the Detached audio lane at its timeline position — it keeps playing exactly once, and becomes independently editable" onClick={() => commit(detachClipAudio(docRef.current, selectedClip.id))}>Detach audio</button>
                    )}
                  </>
                ) : (
                  <p className="hint">This clip has no audio track.</p>
                )}
              </div>
            </div>
          )}

          {selection.kind === 'lane' && selectedLane ? (
            <div className="composition-inspect-lane">
              <strong>Lane</strong>
              <label className="composition-field">
                <span>Name</span>
                <input
                  type="text"
                  value={selectedLane.name}
                  onChange={(event) => commit(renameLane(docRef.current, selectedLane.id, event.target.value))}
                />
              </label>
              <div className="composition-field-row">
                <NumberField label="Gain (dB)" value={selectedLane.gainDb} step={1} min={-60} max={12} onCommit={(value) => commit(setLaneMix(docRef.current, selectedLane.id, {gainDb: value}))} />
              </div>
              <label className="composition-toggle"><input type="checkbox" checked={selectedLane.muted} onChange={(event) => commit(setLaneMix(docRef.current, selectedLane.id, {muted: event.target.checked}))} /> muted</label>
              <div className="editor-tool-group">
                <button type="button" onClick={() => setPickerOpen(true)} title="Pick an audio asset to place at the end of this lane">＋ Add audio to lane</button>
                <button type="button" disabled={selectedLane.segments.length > 0} title={selectedLane.segments.length > 0 ? 'Only an empty lane can be removed — remove its segments first' : ''} onClick={() => {commit(removeLane(docRef.current, selectedLane.id)); setSelection({kind: 'doc'});}}>Remove lane</button>
              </div>
              <p className="hint">Add from the picker while this lane is selected to queue audio here.</p>
            </div>
          ) : null}

          {selection.kind === 'segment' && selectedSegment && selectedSegmentLane ? (
            <div className="composition-inspect-segment">
              <strong>Audio segment</strong>
              <p className="hint composition-ref-line" title={selectedSegment.ref.path}>
                {selectedSegment.ref.artifactId} on {selectedSegmentLane.name}
                {selectedSegment.detachedFromClipId ? ' · detached' : ''}
              </p>
              <div className="composition-field-row">
                <NumberField label="Start (s)" value={selectedSegment.startSeconds} step={0.1} min={0} onCommit={(value) => commit(moveSegment(docRef.current, selectedSegment.id, value))} />
                <NumberField label="In (s)" value={selectedSegment.inSeconds} step={0.1} min={0} onCommit={(value) => commit(setSegmentRange(docRef.current, selectedSegment.id, value, selectedSegment.outSeconds))} />
                <NumberField label="Out (s)" value={selectedSegment.outSeconds} step={0.1} onCommit={(value) => commit(setSegmentRange(docRef.current, selectedSegment.id, selectedSegment.inSeconds, value))} />
              </div>
              <div className="composition-field-row">
                <NumberField label="Gain (dB)" value={selectedSegment.gainDb} step={1} min={-60} max={12} onCommit={(value) => commit(setSegmentMix(docRef.current, selectedSegment.id, {gainDb: value}))} />
                <NumberField label="Fade in (s)" value={selectedSegment.fadeInSeconds} step={0.1} min={0} onCommit={(value) => commit(setSegmentMix(docRef.current, selectedSegment.id, {fadeInSeconds: value}))} />
                <NumberField label="Fade out (s)" value={selectedSegment.fadeOutSeconds} step={0.1} min={0} onCommit={(value) => commit(setSegmentMix(docRef.current, selectedSegment.id, {fadeOutSeconds: value}))} />
              </div>
              <div className="composition-ducks">
                <strong>Ducks</strong>
                <p className="hint">Lower this segment's volume over a range — relative to the segment's own start.</p>
                {selectedSegment.ducks.map((duck, index) => (
                  <div key={index} className="composition-duck-row">
                    <NumberField label="From" value={duck.startSeconds} step={0.1} min={0} onCommit={(value) => commit(updateDuck(docRef.current, selectedSegment.id, index, {startSeconds: value}))} />
                    <NumberField label="To" value={duck.endSeconds} step={0.1} min={0} onCommit={(value) => commit(updateDuck(docRef.current, selectedSegment.id, index, {endSeconds: value}))} />
                    <NumberField label="dB" value={duck.gainDb} step={1} min={-60} max={12} onCommit={(value) => commit(updateDuck(docRef.current, selectedSegment.id, index, {gainDb: value}))} />
                    <button type="button" title="Remove this duck" onClick={() => commit(removeDuck(docRef.current, selectedSegment.id, index))}>✕</button>
                  </div>
                ))}
                <button
                  type="button"
                  title="Add a duck range at the segment's midpoint"
                  onClick={() => {
                    const span = segmentDurationSeconds(selectedSegment);
                    commit(addDuck(docRef.current, selectedSegment.id, span / 2 - 0.5, span / 2 + 0.5, -12));
                  }}
                >
                  ＋ Duck range
                </button>
              </div>
              <div className="editor-tool-group">
                <button type="button" onClick={() => commit(duplicateSegment(docRef.current, selectedSegment.id))}>Duplicate</button>
                <button type="button" onClick={() => {commit(removeSegment(docRef.current, selectedSegment.id)); setSelection({kind: 'lane', laneID: selectedSegmentLane.id});}}>Remove</button>
              </div>
            </div>
          ) : null}

          <div className="editor-tool-group">
            <button type="button" disabled={!undoStack.length} onClick={undo}>Undo</button>
            <button type="button" disabled={!redoStack.length} onClick={redo}>Redo</button>
            <button type="button" disabled={!dirty} onClick={() => {setDoc(normalizeCompositionDoc(baseline)); docRef.current = normalizeCompositionDoc(baseline); setNameDraft(baseline.name); setUndoStack([]); setRedoStack([]);}}>Reset</button>
          </div>

          <div className="composition-save">
            <button
              type="button"
              className="editor-generate"
              disabled={saving || problems.length > 0 || missingRefs.size > 0}
              title={missingRefs.size > 0
                ? 'A source file is missing — relink or remove it before saving is meaningful for rendering'
                : problems.length > 0
                  ? 'Fix the timeline problems first'
                  : 'Write this revision into the conversation as a new cmp_ artifact — earlier revisions stay intact'}
              onClick={() => void saveDoc()}
            >
              {saving ? 'Saving…' : 'Save to conversation'}
            </button>
            {saveError ? <div className="editor-error">{saveError}</div> : null}
            <span className="editor-session-note">Edits stay in the editor until saved; every save is a new revision. Downloads land in the panel below.</span>
          </div>
        </aside>
      </div>

      <EditsPanel count={log.length} emptyText="Nothing saved yet." bodyReservePx={editsPanelBodyReservePx}>
        <ul className="editor-op-list">
          {log.map((entry, index) => (
            <li key={index} className="editor-op editor-op-done">
              <span className="editor-op-thumb-glyph">✓</span>
              <div className="editor-op-meta">
                <span className="editor-op-title">Saved · {entry.name} · {entry.clipCount} clips · {entry.artifactId.slice(0, 12)}</span>
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
