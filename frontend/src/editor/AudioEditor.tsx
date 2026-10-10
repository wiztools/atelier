import {Fragment, useEffect, useMemo, useRef, useState} from 'react';
import * as AppBindings from '../../wailsjs/go/main/App';
import {main} from '../../wailsjs/go/models';
import {EventsOff, EventsOn} from '../../wailsjs/runtime/runtime';
import {EditsPanel} from './EditsPanel';
import {EditKebabMenu} from './EditKebabMenu';
import {EditorHeader} from './EditorHeader';
import {AudioStage} from './AudioStage';
import {AudioTimeline, formatTime} from './AudioTimeline';
import {mergeVideoEditOperations as mergeOperations, terminalVideoEditStatus as terminalStatus} from './videoEditState';
import {computeWaveformPeaks} from './audioWaveform';
import {
  AudioTrimDraft,
  AudioTrimParams,
  addCut,
  defaultAudioTrimDraft,
  deleteCut,
  draftFromSegments,
  keptSeconds,
  moveCut,
  regionIndexAt,
  sameTrimDraft,
  segmentsFromDraft,
  skipRangesForPlayback,
  toggleRegionRemoved,
  trimRegionBounds,
  validateAudioTrimParams,
} from './audioTrimModel';
import './audioEditor.css';

type AudioEditSourceInfo = main.EditSourceInfo;

export type AudioEditorHandle = {
  source: AudioEditSourceInfo;
  sessionID?: string;
};

type AudioEditOperation = Omit<main.EditOperation, 'convertValues' | 'audioTrim'> & {
  audioTrim?: main.AudioTrimParams | AudioTrimParams;
};

type AudioEditSessionState = Omit<main.EditSessionState, 'source' | 'operations' | 'convertValues'> & {
  source: AudioEditSourceInfo;
  operations: AudioEditOperation[];
};

type EditOperationEventView = {
  sessionConversationId: string;
  operation: AudioEditOperation;
};

type AudioEditSubmitRequest = {
  parentConversationId: string;
  sourceArtifactId: string;
  sessionConversationId: string;
  inputArtifactId: string;
  sourceDigest?: string;
  trim?: AudioTrimParams;
};

const editEventOp = 'atelier:edit-op';
function formatEditorError(error: unknown): string {
  if (typeof error === 'string') return error;
  if (error instanceof Error) return error.message;
  try {
    return JSON.stringify(error);
  } catch {
    return String(error);
  }
}

function operationTitle(op: AudioEditOperation): string {
  if (op.kind === 'audio-trim' && op.audioTrim) {
    try {
      const segments = validateAudioTrimParams(op.audioTrim).segments;
      const kept = segments.reduce((total, segment) => total + segment.endSeconds - segment.startSeconds, 0);
      return `Trim · ${kept.toFixed(1)}s · ${segments.length} ${segments.length === 1 ? 'segment' : 'segments'}`;
    } catch {
      return 'Trim';
    }
  }
  return op.kind || 'Audio edit';
}

function statusLabel(status: string): string {
  switch (status) {
    case 'queued':
      return 'queued';
    case 'running':
      return 'rendering';
    case 'failed':
      return 'failed';
    case 'cancelled':
      return 'cancelled';
    default:
      return 'done';
  }
}

// The Edits panel's height clamp needs this editor's content floor: header +
// stage minimum + the timeline row + paddings. The waveform compresses like
// the video preview does, so the video editor's floor transfers.
const editsPanelBodyReservePx = 640;

export function AudioEditor(props: {
  handle: AudioEditorHandle;
  // True while the editor is hidden beneath another screen (Settings). Pauses
  // preview playback and detaches the global shortcuts; all edit state stays.
  suspended?: boolean;
  onClose: () => void;
  onOpenParent: (conversationID: string) => void;
  onSessionCreated: (sessionID: string) => void;
  onResultAdded?: () => void;
  onDirtyChange?: (dirty: boolean) => void;
}) {
  const [parentID] = useState(props.handle.source.conversationId);
  const [sourceArtifactID] = useState(props.handle.source.artifactId);
  const [sessionID, setSessionID] = useState(props.handle.sessionID ?? '');
  const [session, setSession] = useState<AudioEditSessionState | null>(null);
  const [source, setSource] = useState<AudioEditSourceInfo>(props.handle.source);
  const [trimDraft, setTrimDraft] = useState<AudioTrimDraft>(() => defaultAudioTrimDraft());
  const [trimBaseline, setTrimBaseline] = useState<AudioTrimDraft>(() => defaultAudioTrimDraft());
  const [selectedCutIndex, setSelectedCutIndex] = useState(-1);
  const [trimUndoStack, setTrimUndoStack] = useState<AudioTrimDraft[]>([]);
  const [trimRedoStack, setTrimRedoStack] = useState<AudioTrimDraft[]>([]);
  const [currentTime, setCurrentTime] = useState(0);
  const [playing, setPlaying] = useState(false);
  const [previewMuted, setPreviewMuted] = useState(false);
  const [peaks, setPeaks] = useState<number[] | null>(null);
  const [loadError, setLoadError] = useState('');
  const [submitError, setSubmitError] = useState('');
  const [adoptError, setAdoptError] = useState('');
  const [adoptingID, setAdoptingID] = useState('');
  const [deletingID, setDeletingID] = useState('');
  const [deleteError, setDeleteError] = useState('');
  const [compareOpID, setCompareOpID] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [loading, setLoading] = useState(Boolean(props.handle.sessionID));
  const eventOperations = useRef(new Map<string, AudioEditOperation>());
  const submitLock = useRef(false);
  const hydratedSession = useRef('');
  const cutMoveBaseline = useRef<AudioTrimDraft | null>(null);
  const peaksGeneration = useRef(0);

  const ops = session?.operations ?? [];
  const runningOpID = session?.runningOperationId ?? '';
  const busy = loading || submitting || runningOpID !== '';
  const duration = source.durationSeconds && source.durationSeconds > 0 ? source.durationSeconds : 0.1;
  const dirty = useMemo(() => !sameTrimDraft(trimDraft, trimBaseline), [trimDraft, trimBaseline]);
  const orderedOps = useMemo(() => [...ops].reverse(), [ops]);
  const runningOp = runningOpID ? ops.find((op) => op.id === runningOpID) : undefined;
  // Trim derived views. trimSegments is the submission payload; trimNoOp
  // disables the render button while nothing is actually marked for removal.
  const trimSegments = useMemo(() => segmentsFromDraft(trimDraft, duration), [trimDraft, duration]);
  const trimRegions = useMemo(() => trimRegionBounds(trimDraft, duration), [trimDraft, duration]);
  const playheadRegion = regionIndexAt(trimDraft, duration, currentTime);
  const trimKept = useMemo(() => keptSeconds(trimDraft, duration), [trimDraft, duration]);
  const trimNoOp = trimSegments.length === 1 && trimSegments[0].startSeconds <= 0.001 && trimSegments[0].endSeconds >= duration - 0.001;

  useEffect(() => {
    props.onDirtyChange?.(dirty);
  }, [dirty, props.onDirtyChange]);

  useEffect(() => {
    if (props.suspended) setPlaying(false);
  }, [props.suspended]);

  useEffect(() => {
    window.scrollTo(0, 0);
  }, []);

  // One decode per source: the stage and the timeline strip share the peaks.
  // Slow decodes are superseded by a newer source via the generation counter.
  useEffect(() => {
    const generation = ++peaksGeneration.current;
    setPeaks(null);
    computeWaveformPeaks(source.url, duration).then((computed) => {
      if (generation === peaksGeneration.current) setPeaks(computed);
    });
  }, [source.url, duration]);

  useEffect(() => {
    if (!sessionID) return;
    let cancelled = false;
    setLoading(true);
    AppBindings.ListEditSession(sessionID)
      .then(async (state) => {
        if (cancelled) return;
        const merged = mergeOperations(state.operations || [], Array.from(eventOperations.current.values()).filter((op) => (state.operations || []).some((item) => item.id === op.id)));
        const restoredTrim = [...merged].reverse().find((op) => op.audioTrim);
        const nextSource = await AppBindings.ResolveAudioEditInput(sessionID, restoredTrim?.inputArtifactId || state.source.artifactId) as AudioEditSourceInfo;
        if (cancelled) return;
        // Thumbnail/source preparation can outlast the render. Reconcile
        // again after that await so a terminal event cannot be overwritten
        // by the older snapshot captured before source preparation began.
        setSession((current) => {
          const updates = [
            ...(current?.conversationId === state.conversationId ? current.operations : []),
            ...Array.from(eventOperations.current.values()).filter((op) => state.operations.some((item) => item.id === op.id)),
          ];
          const operations = mergeOperations(state.operations, updates);
          return {...state, operations, runningOperationId: operations.find((op) => !terminalStatus(op.status))?.id || ''};
        });
        setSource(nextSource);
        if (hydratedSession.current !== sessionID) {
          // The newest recorded operation names the cut set the session was
          // last edited with; it restores as the baseline.
          if (restoredTrim?.audioTrim) {
            const draft = draftFromSegments(validateAudioTrimParams(restoredTrim.audioTrim).segments, durationForSource(nextSource));
            setTrimDraft(draft);
            setTrimBaseline(draft);
            setSelectedCutIndex(-1);
            setTrimUndoStack([]);
            setTrimRedoStack([]);
          }
          hydratedSession.current = sessionID;
        }
      })
      .catch((error) => {
        if (!cancelled) setLoadError(formatEditorError(error));
      }).finally(() => {if (!cancelled) setLoading(false);});
    return () => {
      cancelled = true;
    };
  }, [sessionID]);

  useEffect(() => {
    const onUpdate = (event: EditOperationEventView) => {
      if (!event?.operation) return;
      const previous = eventOperations.current.get(event.operation.id);
      if (!previous || !terminalStatus(previous.status) || terminalStatus(event.operation.status)) {
        eventOperations.current.set(event.operation.id, event.operation);
      }
      setSession((current) => {
        if (!current || current.conversationId !== event.sessionConversationId) return current;
        const operations = mergeOperations(current.operations, [event.operation]);
        return {...current, operations, runningOperationId: operations.find((op) => !terminalStatus(op.status))?.id || ''};
      });
    };
    EventsOn(editEventOp, onUpdate);
    return () => EventsOff(editEventOp);
  }, []);

  function durationForSource(info: AudioEditSourceInfo): number {
    return info.durationSeconds && info.durationSeconds > 0 ? info.durationSeconds : 0.1;
  }

  // Trim draft edits — the video trim's cut-marker idiom.
  function commitTrim(next: AudioTrimDraft) {
    setTrimUndoStack((list) => [...list.slice(-99), trimDraft]);
    setTrimRedoStack([]);
    setTrimDraft(next);
  }

  function undoTrim() {
    const previous = trimUndoStack[trimUndoStack.length - 1];
    if (!previous) return;
    setTrimRedoStack((list) => [...list, trimDraft]);
    setTrimUndoStack((list) => list.slice(0, -1));
    setTrimDraft(previous);
    setSelectedCutIndex(-1);
  }

  function redoTrim() {
    const next = trimRedoStack[trimRedoStack.length - 1];
    if (!next) return;
    setTrimUndoStack((list) => [...list, trimDraft]);
    setTrimRedoStack((list) => list.slice(0, -1));
    setTrimDraft(next);
    setSelectedCutIndex(-1);
  }

  function resetTrimDraft() {
    commitTrim(defaultAudioTrimDraft());
    setSelectedCutIndex(-1);
  }

  function addCutAtPlayhead() {
    let next: AudioTrimDraft;
    try {
      next = addCut(trimDraft, duration, currentTime);
    } catch {
      return;
    }
    commitTrim(next);
    let nearest = -1;
    let distance = Number.POSITIVE_INFINITY;
    next.cuts.forEach((cut, index) => {
      const gap = Math.abs(cut.timeSeconds - currentTime);
      if (gap < distance) {
        nearest = index;
        distance = gap;
      }
    });
    setSelectedCutIndex(nearest);
  }

  function deleteSelectedCut() {
    if (selectedCutIndex < 0 || selectedCutIndex >= trimDraft.cuts.length) return;
    const next = deleteCut(trimDraft, selectedCutIndex);
    commitTrim(next);
    setSelectedCutIndex(Math.min(selectedCutIndex, next.cuts.length - 1));
  }

  function togglePlayheadRegion() {
    commitTrim(toggleRegionRemoved(trimDraft, playheadRegion));
  }

  function nudgeSelectedCut(steps: number) {
    if (steps === 0 || selectedCutIndex < 0 || selectedCutIndex >= trimDraft.cuts.length) return;
    const cut = trimDraft.cuts[selectedCutIndex];
    commitTrim(moveCut(trimDraft, selectedCutIndex, cut.timeSeconds + steps * 0.1));
  }

  function moveCutAt(index: number, timeSeconds: number) {
    if (!cutMoveBaseline.current) cutMoveBaseline.current = trimDraft;
    const next = moveCut(trimDraft, index, timeSeconds);
    setTrimDraft(next);
    setSelectedCutIndex(Math.min(index, next.cuts.length - 1));
  }

  function finishCutMove(index: number, timeSeconds: number) {
    const before = cutMoveBaseline.current || trimDraft;
    cutMoveBaseline.current = null;
    const next = moveCut(trimDraft, index, timeSeconds);
    if (sameTrimDraft(before, next)) return;
    setTrimUndoStack((list) => [...list.slice(-99), before]);
    setTrimRedoStack([]);
    setTrimDraft(next);
  }

  function cancelCutMove() {
    if (cutMoveBaseline.current) setTrimDraft(cutMoveBaseline.current);
    cutMoveBaseline.current = null;
  }

  async function submit() {
    if (submitLock.current || busy) return;
    submitLock.current = true;
    setSubmitting(true);
    setSubmitError('');
    setAdoptError('');
    try {
      const submittedTrim = validateAudioTrimParams({version: 1, source: {durationSeconds: duration}, segments: segmentsFromDraft(trimDraft, duration)});
      const request: AudioEditSubmitRequest = {
        parentConversationId: parentID,
        sourceArtifactId: sourceArtifactID,
        sessionConversationId: sessionID,
        inputArtifactId: sessionID ? source.artifactId : '',
        sourceDigest: sessionID ? '' : source.sourceDigest,
        trim: submittedTrim,
      };
      const state = await AppBindings.SubmitAudioEdit(main.AudioEditSubmitRequest.createFrom(request));
      const operation = eventOperations.current.get(state.operation.id) || state.operation;
      setSession((current) => {
        const operations = mergeOperations(current?.operations || [], [state.operation, operation]);
        return {
          source,
          parentConversationId: parentID,
          parentAvailable: true,
          parentStreaming: false,
          falConfigured: false,
          replicateConfigured: false,
          ...current,
          conversationId: state.sessionConversationId,
          operations,
          runningOperationId: operations.find((op) => !terminalStatus(op.status))?.id || '',
        };
      });
      setTrimBaseline(trimDraft);
      if (state.createdSession) {
        hydratedSession.current = state.sessionConversationId;
        setSessionID(state.sessionConversationId);
        props.onSessionCreated(state.sessionConversationId);
      }
      if (state.sourceUrl && state.sourceUrl !== source.url) {
        setSource((current) => ({...current, artifactId: state.operation.inputArtifactId || current.artifactId, url: state.sourceUrl || current.url}));
      }
    } catch (error) {
      setSubmitError(formatEditorError(error));
    } finally {
      submitLock.current = false;
      setSubmitting(false);
    }
  }

  async function cancelRunning() {
    if (!sessionID || !runningOpID) return;
    try {
      await AppBindings.CancelAudioEdit(sessionID, runningOpID);
    } catch (error) {
      setSubmitError(formatEditorError(error));
    }
  }

  async function useAsSource(op: AudioEditOperation) {
    if (!op.resultArtifactId || !op.resultUrl) return;
    let nextSource: AudioEditSourceInfo = {
      ...source,
      artifactId: op.resultArtifactId,
      url: op.resultUrl,
      mimeType: op.resultMimeType || source.mimeType,
      durationSeconds: op.resultDurationSeconds || source.durationSeconds,
      notices: op.notices || [],
    };
    try {
      nextSource = await AppBindings.ResolveAudioEditInput(sessionID, op.resultArtifactId);
    } catch (error) {
      setLoadError(formatEditorError(error));
      return;
    }
    setSource(nextSource);
    const nextTrim = defaultAudioTrimDraft();
    setTrimDraft(nextTrim);
    setTrimBaseline(nextTrim);
    setTrimUndoStack([]);
    setTrimRedoStack([]);
    setSelectedCutIndex(-1);
    setCurrentTime(0);
    setPlaying(false);
    setCompareOpID('');
  }

  // editCuts restores an op's cut set onto its input clip so the user can
  // adjust and re-render it.
  async function editCuts(op: AudioEditOperation) {
    if (!op.audioTrim || !op.inputArtifactId || busy) return;
    try {
      const input = await AppBindings.ResolveAudioEditInput(sessionID, op.inputArtifactId);
      const draft = draftFromSegments(validateAudioTrimParams(op.audioTrim).segments, durationForSource(input));
      setSource(input);
      setTrimDraft(draft);
      setTrimBaseline(draft);
      setSelectedCutIndex(-1);
      setCurrentTime(0);
      setPlaying(false);
      setTrimUndoStack([]);
      setTrimRedoStack([]);
      setLoadError('');
    } catch (error) {
      setLoadError(formatEditorError(error));
    }
  }

  useEffect(() => {
    const keydown = (event: KeyboardEvent) => {
      if (props.suspended) return;
      const target = event.target as HTMLElement | null;
      if (event.defaultPrevented || target?.closest('[role="dialog"]') || target?.isContentEditable ||
        (target && ['INPUT', 'TEXTAREA', 'SELECT', 'BUTTON'].includes(target.tagName))) return;
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'z' && !busy) {
        event.preventDefault();
        if (event.shiftKey) redoTrim(); else undoTrim();
      } else if (event.key === ' ') {
        event.preventDefault();
        setPlaying((value) => !value);
      } else if (event.key.startsWith('Arrow') && !busy) {
        event.preventDefault();
        // Arrows retime the selected cut by 0.1s (shift: one second).
        const step = event.shiftKey ? 10 : 1;
        nudgeSelectedCut(event.key === 'ArrowLeft' ? -step : event.key === 'ArrowRight' ? step : 0);
      }
    };
    window.addEventListener('keydown', keydown);
    return () => window.removeEventListener('keydown', keydown);
  }, [props.suspended, busy, trimDraft, trimUndoStack, trimRedoStack, selectedCutIndex]);

  async function addToOriginal(op: AudioEditOperation) {
    if (!sessionID || op.adoptedAt) return;
    setAdoptingID(op.id);
    setAdoptError('');
    try {
      await AppBindings.AddEditResultToConversation(sessionID, op.id);
      setSession((current) => {
        if (!current) return current;
        return {...current, operations: current.operations.map((item) => item.id === op.id ? {...item, adoptedAt: new Date().toISOString()} : item)};
      });
      props.onResultAdded?.();
    } catch (error) {
      setAdoptError(formatEditorError(error));
    } finally {
      setAdoptingID('');
    }
  }

  async function download(op: AudioEditOperation) {
    if (!op.resultUrl) return;
    try {
      await AppBindings.SaveAudio(main.SaveAudioRequest.createFrom({path: op.resultUrl, suggestedName: 'trimmed-audio.m4a'}));
    } catch (error) {
      setAdoptError(formatEditorError(error));
    }
  }

  // Delete one edit from the session — its turn and the result file it owns.
  // When the deleted result is the clip on the stage, fall back to the
  // session's own source copy with defaults re-derived for it.
  async function deleteOp(op: AudioEditOperation) {
    if (!sessionID || deletingID) return;
    setDeletingID(op.id);
    setDeleteError('');
    try {
      await AppBindings.DeleteEditOperation(sessionID, op.id);
      eventOperations.current.delete(op.id);
      setSession((current) => {
        if (!current) return current;
        const operations = current.operations.filter((item) => item.id !== op.id);
        return {...current, operations, runningOperationId: operations.find((item) => !terminalStatus(item.status))?.id || ''};
      });
      setCompareOpID((current) => (current === op.id ? '' : current));
      if (op.resultArtifactId && source.artifactId === op.resultArtifactId) {
        const fallback = session?.source;
        if (fallback?.artifactId && fallback.url) {
          const nextTrim = defaultAudioTrimDraft();
          setSource({...fallback, notices: []});
          setTrimDraft(nextTrim);
          setTrimBaseline(nextTrim);
          setTrimUndoStack([]);
          setTrimRedoStack([]);
          setSelectedCutIndex(-1);
          setCurrentTime(0);
          setPlaying(false);
          void AppBindings.ResolveAudioEditInput(sessionID, fallback.artifactId)
            .then((resolved) => setSource(resolved))
            .catch(() => {});
        }
      }
    } catch (error) {
      setDeleteError(formatEditorError(error));
    } finally {
      setDeletingID('');
    }
  }

  return (
    <section className="audio-editor">
      <EditorHeader
        title="Audio Editor"
        sessionID={sessionID || undefined}
        parentAvailable={session?.parentAvailable}
        parentConversationID={session?.parentConversationId ?? parentID}
        busyLabel={busy ? (runningOp?.progress ? `${Math.round(runningOp.progress * 100)}%` : 'rendering…') : ''}
        onClose={props.onClose}
        onOpenParent={props.onOpenParent}
      />

      {loadError ? <div className="editor-error">{loadError}</div> : null}

      <div className="editor-body">
        <div className="audio-editor-main">
          <AudioStage
            sourceUrl={source.url}
            duration={duration}
            peaks={peaks}
            currentTime={currentTime}
            playing={playing}
            muted={previewMuted}
            skipRanges={skipRangesForPlayback(trimDraft, duration)}
            onTimeChange={(time) => setCurrentTime(Math.min(duration, Math.max(0, time)))}
            onPlayingChange={setPlaying}
            onMutedChange={setPreviewMuted}
          />
          <AudioTimeline
            draft={trimDraft}
            duration={duration}
            currentTime={currentTime}
            playing={playing}
            muted={previewMuted}
            busy={busy}
            selectedCutIndex={selectedCutIndex}
            peaks={peaks}
            onSeek={(time) => {
              setPlaying(false);
              setCurrentTime(Math.min(duration, Math.max(0, time)));
            }}
            onTogglePlay={() => setPlaying((value) => !value)}
            onToggleMuted={() => setPreviewMuted((value) => !value)}
            onAddCut={addCutAtPlayhead}
            onDeleteCut={deleteSelectedCut}
            onSelectCut={setSelectedCutIndex}
            onMoveCut={moveCutAt}
            onCommitCutTime={finishCutMove}
            onCancelCutMove={cancelCutMove}
            onToggleRegionRemoved={togglePlayheadRegion}
          />
        </div>

        <aside className="editor-inspector audio-inspector">
          <div className="audio-framing-summary" aria-live="polite">
            <strong>{trimKept.toFixed(1)}s of {duration.toFixed(1)}s kept</strong>
            <span>{trimSegments.length} kept · {trimRegions.filter((region) => region.removed).length} removed</span>
          </div>
          <div className="audio-framing-summary">
            <span>
              Playhead in region {playheadRegion + 1} of {trimRegions.length} · {formatTime(trimRegions[playheadRegion]?.start ?? 0)} – {formatTime(trimRegions[playheadRegion]?.end ?? 0)}
              {trimRegions[playheadRegion]?.removed ? ' · removed' : ''}
            </span>
          </div>
          <div className="editor-tool-group">
            <button type="button" disabled={busy} onClick={togglePlayheadRegion}>
              {trimRegions[playheadRegion]?.removed ? 'Keep at playhead' : 'Remove at playhead'}
            </button>
          </div>
          <p className="hint">⚑ adds a cut at the playhead, ✂ removes the range it sits in. Playback skips removed audio.</p>
          <div className="editor-tool-group">
            <button type="button" disabled={busy || !trimUndoStack.length} onClick={undoTrim}>Undo</button>
            <button type="button" disabled={busy || !trimRedoStack.length} onClick={redoTrim}>Redo</button>
            <button type="button" disabled={busy} onClick={resetTrimDraft}>Reset</button>
          </div>
          {trimNoOp ? <span className="hint">Nothing is marked for removal yet — add cuts and remove the ranges you don't want.</span> : null}
          <button
            type="button"
            className="editor-generate"
            disabled={busy || Boolean(loadError) || trimNoOp}
            onClick={() => void submit()}
          >
            {loading ? 'Loading…' : busy ? 'Rendering…' : 'Trim audio'}
          </button>
          {[...(source.notices || [])].map((notice, index) => (
            <span key={index} className="editor-op-notice">{notice}</span>
          ))}
          {busy && runningOpID ? (
            <button type="button" className="editor-cancel" onClick={() => void cancelRunning()}>
              Cancel render
            </button>
          ) : null}
          {submitError ? <div className="editor-error">{submitError}</div> : null}
          <div className="editor-session-note">
            {sessionID ? 'Edits are saved.' : 'Your first render saves this edit.'}
          </div>
        </aside>
      </div>

      <EditsPanel
        count={ops.length}
        emptyText="No renders yet."
        adoptError={
          <>
            {adoptError ? <span className="editor-error">{adoptError}</span> : null}
            {deleteError ? <span className="editor-error">{deleteError}</span> : null}
          </>
        }
        bodyReservePx={editsPanelBodyReservePx}
      >
        <ul className="editor-op-list">
          {orderedOps.map((op) => (
              <Fragment key={op.id}>
                <li className={`editor-op editor-op-${op.status}${compareOpID === op.id ? ' selected' : ''}`}>
                  <button
                    type="button"
                    className="editor-op-thumb audio-op-thumb"
                    onClick={() => setCompareOpID((current) => (current === op.id ? '' : op.id))}
                    aria-label={compareOpID === op.id ? 'Hide result preview' : 'Preview this result'}
                  >
                    {op.resultUrl ? <span className="editor-op-thumb-glyph">♫</span> : <span className="editor-op-thumb-empty">{statusLabel(op.status)}</span>}
                  </button>
                  <div className="editor-op-meta">
                    <span className="editor-op-title">{operationTitle(op)} · {statusLabel(op.status)}</span>
                    <span className="editor-op-sub">
                      {op.backend ? 'Local ffmpeg' : op.provider && op.model ? `${op.provider} · ${op.model}` : ''}
                      {op.resultDurationSeconds ? ` · ${formatTime(op.resultDurationSeconds)}` : ''}
                      {op.status === 'running' && op.progress ? ` · ${Math.round(op.progress * 100)}%` : ''}
                      {op.status === 'completed' ? '' : op.error ? ` - ${op.error}` : ''}
                    </span>
                    {(op.notices ?? []).map((notice, index) => <span key={index} className="editor-op-notice">{notice}</span>)}
                  </div>
                  <div className="editor-op-actions">
                    {op.audioTrim ? <button type="button" disabled={busy} onClick={() => void editCuts(op)}>Edit cuts</button> : null}
                    {op.status === 'completed' ? (
                      <>
                        <button type="button" onClick={() => void useAsSource(op)} disabled={busy || source.artifactId === op.resultArtifactId}>Use as source</button>
                        <button type="button" onClick={() => void download(op)} disabled={!op.resultPath}>Download</button>
                        <button
                          type="button"
                          onClick={() => void addToOriginal(op)}
                          disabled={Boolean(op.adoptedAt) || adoptingID === op.id || (session ? !session.parentAvailable : true)}
                        >
                          {op.adoptedAt ? 'Added' : adoptingID === op.id ? 'Adding…' : 'Add to conversation'}
                        </button>
                      </>
                    ) : op.status === 'failed' || op.status === 'cancelled' ? (
                      <span className="editor-op-retry-hint">Adjust and render again</span>
                    ) : null}
                  </div>
                  <EditKebabMenu
                    label={`Options for ${operationTitle(op)}`}
                    disabled={busy || !sessionID || !terminalStatus(op.status) || deletingID !== ''}
                    deleteInProgress={deletingID === op.id}
                    onDelete={() => void deleteOp(op)}
                  />
                </li>
                {compareOpID === op.id && op.resultUrl ? (
                  <li className="editor-op-compare">
                    <div className="audio-result-preview">
                      <audio src={op.resultUrl} controls />
                    </div>
                  </li>
                ) : null}
              </Fragment>
            ))}
        </ul>
      </EditsPanel>
    </section>
  );
}
