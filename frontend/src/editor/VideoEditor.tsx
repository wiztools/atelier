import {Fragment, useEffect, useMemo, useRef, useState} from 'react';
import * as AppBindings from '../../wailsjs/go/main/App';
import {main} from '../../wailsjs/go/models';
import {EventsOff, EventsOn} from '../../wailsjs/runtime/runtime';
import {EditsPanel} from './EditsPanel';
import {EditorHeader} from './EditorHeader';
import {VideoStage} from './VideoStage';
import {VideoTimeline} from './VideoTimeline';
import {mergeVideoEditOperations as mergeOperations, terminalVideoEditStatus as terminalStatus} from './videoEditState';
import {
  VideoReframeAspectRatio,
  VideoReframeInterpolation,
  VideoReframeParams,
  VideoReframeRect,
  evaluateVideoReframe,
  validateVideoReframe,
} from './videoReframe';
import {
  VideoOutputPreset,
  changeVideoAspectWithOutput,
  defaultVideoReframeParams,
  deleteVideoReframeMarker,
  markerIndexAtTime,
  moveVideoReframeMarkerTime,
  outputForVideoPreset,
  sameVideoParams,
  selectedVideoRect,
  setVideoOutputPreset,
  updateVideoReframeMarker,
  upsertVideoReframeMarker,
  videoReframeAspects,
} from './videoTimelineModel';
import './videoEditor.css';

type VideoEditSourceInfo = main.EditSourceInfo;

export type VideoEditorHandle = {
  source: VideoEditSourceInfo;
  sessionID?: string;
};

type VideoEditOperation = Omit<main.EditOperation, 'convertValues' | 'reframe'> & {
  reframe?: main.VideoReframeParams | VideoReframeParams;
};

type VideoEditSessionState = Omit<main.EditSessionState, 'source' | 'operations' | 'convertValues'> & {
  source: VideoEditSourceInfo;
  operations: VideoEditOperation[];
};

type EditOperationEventView = {
  sessionConversationId: string;
  operation: VideoEditOperation;
};

type VideoEditSubmitRequest = {
  parentConversationId: string;
  sourceArtifactId: string;
  sessionConversationId: string;
  inputArtifactId: string;
  sourceDigest?: string;
  reframe: VideoReframeParams;
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

function formatTime(value: number): string {
  const seconds = Math.max(0, Number.isFinite(value) ? value : 0);
  const whole = Math.floor(seconds);
  const minutes = Math.floor(whole / 60);
  const rest = whole % 60;
  return `${minutes}:${String(rest).padStart(2, '0')}`;
}

function operationTitle(op: VideoEditOperation): string {
  if (op.kind === 'video-reframe' && op.reframe) {
    return `Reframe · ${op.reframe.aspectRatio} · ${op.reframe.output.width} x ${op.reframe.output.height}`;
  }
  return op.kind || 'Video edit';
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

function posterURL(url: string): string {
  return url.replace(/\.[^/.?#]+(?:[?#].*)?$/, '_poster.jpg');
}

function sourceForParams(source: VideoEditSourceInfo): {width: number; height: number; durationSeconds: number} {
  return {
    width: Math.max(2, source.width ?? 2),
    height: Math.max(2, source.height ?? 2),
    durationSeconds: source.durationSeconds && source.durationSeconds > 0 ? source.durationSeconds : 0.1,
  };
}

function releaseVideoPreview(url: string): void {
  void AppBindings.ReleaseVideoEditSourcePreview(url).catch(() => {});
}

// The Edits panel's height clamp needs this editor's content floor: header +
// stage minimum (260px) + the timeline row + paddings, plus slack to match
// the image editor's proportions. The panel never grows past it.
const editsPanelBodyReservePx = 640;

export function VideoEditor(props: {
  handle: VideoEditorHandle;
  onClose: () => void;
  onOpenParent: (conversationID: string) => void;
  onSessionCreated: (sessionID: string) => void;
  onResultAdded?: () => void;
  onDirtyChange?: (dirty: boolean) => void;
  onOpenSettings?: () => void;
}) {
  const [parentID] = useState(props.handle.source.conversationId);
  const [sourceArtifactID] = useState(props.handle.source.artifactId);
  const [sessionID, setSessionID] = useState(props.handle.sessionID ?? '');
  const [session, setSession] = useState<VideoEditSessionState | null>(null);
  const [source, setSource] = useState<VideoEditSourceInfo>(props.handle.source);
  const [params, setParams] = useState<VideoReframeParams>(() => defaultVideoReframeParams(sourceForParams(props.handle.source)));
  const [baseline, setBaseline] = useState<VideoReframeParams>(() => defaultVideoReframeParams(sourceForParams(props.handle.source)));
  const [outputPreset, setOutputPreset] = useState<VideoOutputPreset>('source');
  const [selectedMarkerIndex, setSelectedMarkerIndex] = useState(0);
  const [currentTime, setCurrentTime] = useState(0);
  const [playing, setPlaying] = useState(false);
  const [undoStack, setUndoStack] = useState<VideoReframeParams[]>([]);
  const [redoStack, setRedoStack] = useState<VideoReframeParams[]>([]);
  const [loadError, setLoadError] = useState('');
  const [submitError, setSubmitError] = useState('');
  const [adoptError, setAdoptError] = useState('');
  const [adoptingID, setAdoptingID] = useState('');
  const [compareOpID, setCompareOpID] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [loading, setLoading] = useState(Boolean(props.handle.sessionID));
  const eventOperations = useRef(new Map<string, VideoEditOperation>());
  const submitLock = useRef(false);
  const hydratedSession = useRef('');
  const markerMoveBaseline = useRef<VideoReframeParams | null>(null);
  const previewResources = useRef(new Set<string>([props.handle.source.url, ...(props.handle.source.thumbnails || [])]));
  const pendingReleases = useRef(new Map<string, number>());

  const ops = session?.operations ?? [];
  const runningOpID = session?.runningOperationId ?? '';
  const busy = loading || submitting || runningOpID !== '';
  const outputNotice = outputForVideoPreset(params, outputPreset).notices;
  const dirty = useMemo(() => !sameVideoParams(params, baseline), [params, baseline]);
  const orderedOps = useMemo(() => [...ops].reverse(), [ops]);
  const runningOp = runningOpID ? ops.find((op) => op.id === runningOpID) : undefined;
  const selectedRect = selectedVideoRect(params, currentTime, selectedMarkerIndex);

  useEffect(() => {
    props.onDirtyChange?.(dirty);
  }, [dirty, props.onDirtyChange]);

  useEffect(() => {
    window.scrollTo(0, 0);
  }, []);

  useEffect(() => {
    previewResources.current.add(source.url);
    for (const thumbnail of source.thumbnails || []) previewResources.current.add(thumbnail);
  }, [source]);
  useEffect(() => {
    // Defer the cleanup release so StrictMode's setup/cleanup replay cannot
    // delete preview files the remounted editor still displays.
    pendingReleases.current.forEach((timer) => clearTimeout(timer));
    pendingReleases.current.clear();
    return () => {
      for (const url of previewResources.current) {
        if (pendingReleases.current.has(url)) continue;
        pendingReleases.current.set(url, window.setTimeout(() => {
          pendingReleases.current.delete(url);
          releaseVideoPreview(url);
        }, 0));
      }
    };
  }, []);

  useEffect(() => {
    if (!sessionID) return;
    let cancelled = false;
    setLoading(true);
    AppBindings.ListEditSession(sessionID)
      .then(async (state) => {
        if (cancelled) return;
        const merged = mergeOperations(state.operations || [], Array.from(eventOperations.current.values()).filter((op) => (state.operations || []).some((item) => item.id === op.id)));
        const restored = [...merged].reverse().find((op) => op.reframe);
        const nextSource = await AppBindings.ResolveVideoEditInput(sessionID, restored?.inputArtifactId || state.source.artifactId) as VideoEditSourceInfo;
        if (cancelled) {
          for (const thumbnail of nextSource.thumbnails || []) releaseVideoPreview(thumbnail);
          return;
        }
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
          const next = restored?.reframe ? validateVideoReframe(restored.reframe) : defaultVideoReframeParams(sourceForParams(nextSource));
          setParams(next);
          setBaseline(next);
          setSelectedMarkerIndex(0);
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

  function commit(next: VideoReframeParams, selectIndex = selectedMarkerIndex) {
    const valid = validateVideoReframe(next);
    setUndoStack((list) => [...list.slice(-99), params]);
    setRedoStack([]);
    setParams(valid);
    setSelectedMarkerIndex(Math.max(0, Math.min(selectIndex, valid.markers.length - 1)));
  }

  function undo() {
    const previous = undoStack[undoStack.length - 1];
    if (!previous) return;
    setRedoStack((list) => [...list, params]);
    setUndoStack((list) => list.slice(0, -1));
    setParams(previous);
    setSelectedMarkerIndex((index) => Math.min(index, previous.markers.length - 1));
  }

  function redo() {
    const next = redoStack[redoStack.length - 1];
    if (!next) return;
    setUndoStack((list) => [...list, params]);
    setRedoStack((list) => list.slice(0, -1));
    setParams(next);
    setSelectedMarkerIndex((index) => Math.min(index, next.markers.length - 1));
  }

  function resetDraft() {
    commit(defaultVideoReframeParams(sourceForParams(source), params.aspectRatio), 0);
  }

  function changeAspect(aspectRatio: VideoReframeAspectRatio) {
    commit(changeVideoAspectWithOutput(params, aspectRatio, outputPreset), selectedMarkerIndex);
  }

  function changeOutput(preset: VideoOutputPreset) {
    setOutputPreset(preset);
    commit(setVideoOutputPreset(params, preset), selectedMarkerIndex);
  }

  function commitFraming(timeSeconds: number, rect: VideoReframeRect) {
    const next = upsertVideoReframeMarker(params, timeSeconds, rect);
    commit(next, markerIndexAtTime(next, timeSeconds));
  }

  function nudgeSelected(dx: number, dy: number) {
    const marker = params.markers[selectedMarkerIndex];
    if (!marker) return;
    commit(updateVideoReframeMarker(params, selectedMarkerIndex, {x: marker.x + dx, y: marker.y + dy}), selectedMarkerIndex);
  }

  function setSelectedXY(axis: 'x' | 'y', value: number) {
    commit(updateVideoReframeMarker(params, selectedMarkerIndex, {[axis]: value}), selectedMarkerIndex);
  }

  function moveMarker(index: number, timeSeconds: number) {
    if (!markerMoveBaseline.current) markerMoveBaseline.current = params;
    const next = moveVideoReframeMarkerTime(params, index, timeSeconds);
    setParams(next);
    setSelectedMarkerIndex(Math.min(index, next.markers.length - 1));
  }

  function finishMoveMarker(index: number, timeSeconds: number) {
    const before = markerMoveBaseline.current || params;
    markerMoveBaseline.current = null;
    const next = moveVideoReframeMarkerTime(params, index, timeSeconds);
    if (sameVideoParams(before, next)) return;
    setUndoStack((list) => [...list.slice(-99), before]);
    setRedoStack([]);
    setParams(next);
  }

  function cancelMoveMarker() {
    if (markerMoveBaseline.current) setParams(markerMoveBaseline.current);
    markerMoveBaseline.current = null;
  }

  function setInterpolation(index: number, interpolationToNext: VideoReframeInterpolation) {
    commit(updateVideoReframeMarker(params, index, {interpolationToNext}), index);
  }

  function deleteMarker(index: number) {
    const next = deleteVideoReframeMarker(params, index);
    commit(next, Math.max(0, Math.min(index - 1, next.markers.length - 1)));
  }

  async function submit() {
    if (submitLock.current || busy) return;
    submitLock.current = true;
    setSubmitting(true);
    setSubmitError('');
    setAdoptError('');
    try {
      const request: VideoEditSubmitRequest = {
        parentConversationId: parentID,
        sourceArtifactId: sourceArtifactID,
        sessionConversationId: sessionID,
        inputArtifactId: sessionID ? source.artifactId : '',
        sourceDigest: sessionID ? '' : source.sourceDigest,
        reframe: validateVideoReframe(params),
      };
      const state = await AppBindings.SubmitVideoEdit(main.VideoEditSubmitRequest.createFrom(request));
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
      setBaseline(validateVideoReframe(params));
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
      await AppBindings.CancelVideoEdit(sessionID, runningOpID);
    } catch (error) {
      setSubmitError(formatEditorError(error));
    }
  }

  async function useAsSource(op: VideoEditOperation) {
    if (!op.resultArtifactId || !op.resultUrl) return;
    const previousURL = source.url;
    let nextSource: VideoEditSourceInfo = {
      ...source,
      artifactId: op.resultArtifactId,
      url: op.resultUrl,
      mimeType: op.resultMimeType || source.mimeType,
      width: op.resultWidth || op.reframe?.output.width || source.width,
      height: op.resultHeight || op.reframe?.output.height || source.height,
      durationSeconds: op.resultDurationSeconds || source.durationSeconds,
      thumbnails: [],
      notices: op.notices || [],
    };
    try {
      nextSource = await AppBindings.ResolveVideoEditInput(sessionID, op.resultArtifactId);
    } catch (error) {
      setLoadError(formatEditorError(error));
      return;
    }
    setSource(nextSource);
    if (previousURL && previousURL !== props.handle.source.url && previousURL !== nextSource.url) releaseVideoPreview(previousURL);
    const nextParams = defaultVideoReframeParams(sourceForParams(nextSource), params.aspectRatio);
    setParams(nextParams);
    setBaseline(nextParams);
    setUndoStack([]);
    setRedoStack([]);
    setSelectedMarkerIndex(0);
    setCurrentTime(0);
    setCompareOpID('');
  }

  async function editFraming(op: VideoEditOperation) {
    if (!op.reframe || !op.inputArtifactId || busy) return;
    try {
      const input = await AppBindings.ResolveVideoEditInput(sessionID, op.inputArtifactId);
      const next = validateVideoReframe(op.reframe);
      setSource(input);
      setParams(next);
      setBaseline(next);
      setSelectedMarkerIndex(0);
      setCurrentTime(0);
      setPlaying(false);
      setUndoStack([]);
      setRedoStack([]);
      setLoadError('');
    } catch (error) {
      setLoadError(formatEditorError(error));
    }
  }

  useEffect(() => {
    const keydown = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement | null;
      if (event.defaultPrevented || target?.closest('[role="dialog"]') || target?.isContentEditable ||
        (target && ['INPUT', 'TEXTAREA', 'SELECT', 'BUTTON'].includes(target.tagName))) return;
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'z' && !busy) {
        event.preventDefault();
        if (event.shiftKey) redo(); else undo();
      } else if (event.key === ' ') {
        event.preventDefault();
        setPlaying((value) => !value);
      } else if (event.key.startsWith('Arrow') && !busy) {
        event.preventDefault();
        const step = event.shiftKey ? 10 : 1;
        nudgeSelected(event.key === 'ArrowLeft' ? -step : event.key === 'ArrowRight' ? step : 0,
          event.key === 'ArrowUp' ? -step : event.key === 'ArrowDown' ? step : 0);
      }
    };
    window.addEventListener('keydown', keydown);
    return () => window.removeEventListener('keydown', keydown);
  }, [params, busy, undoStack, redoStack, selectedMarkerIndex]);

  async function addToOriginal(op: VideoEditOperation) {
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

  async function download(op: VideoEditOperation) {
    if (!op.resultUrl) return;
    try {
      await AppBindings.SaveVideo(main.SaveVideoRequest.createFrom({path: op.resultUrl, suggestedName: 'reframed-video.mp4'}));
    } catch (error) {
      setAdoptError(formatEditorError(error));
    }
  }

  return (
    <section className="video-editor">
      <EditorHeader
        title="Video Editor"
        sessionID={sessionID || undefined}
        parentAvailable={session?.parentAvailable}
        parentConversationID={session?.parentConversationId ?? parentID}
        busyLabel={busy ? (runningOp?.progress ? `${Math.round(runningOp.progress * 100)}%` : 'rendering…') : ''}
        onClose={props.onClose}
        onOpenParent={props.onOpenParent}
      />

      {loadError ? <div className="editor-error">{loadError}</div> : null}

      <div className="editor-body">
        <div className="video-editor-main">
          <VideoStage
            sourceUrl={source.url}
            params={params}
            currentTime={currentTime}
            playing={playing}
            busy={busy}
            selectedMarkerIndex={selectedMarkerIndex}
            onTimeChange={(time) => setCurrentTime(Math.min(params.source.durationSeconds, Math.max(0, time)))}
            onPlayingChange={setPlaying}
            onCommitFraming={commitFraming}
            onSelectMarker={setSelectedMarkerIndex}
          />
          <VideoTimeline
            params={params}
            currentTime={currentTime}
            playing={playing}
            busy={busy}
            selectedMarkerIndex={selectedMarkerIndex}
            thumbnails={source.thumbnails || []}
            onSeek={(time) => {
              setPlaying(false);
              setCurrentTime(Math.min(params.source.durationSeconds, Math.max(0, time)));
            }}
            onTogglePlay={() => setPlaying((value) => !value)}
            onAddMarker={() => commitFraming(currentTime, evaluateVideoReframe(params, currentTime))}
            onSelectMarker={setSelectedMarkerIndex}
            onMoveMarker={moveMarker}
            onCommitMarkerTime={finishMoveMarker}
            onCancelMarkerMove={cancelMoveMarker}
            onDeleteMarker={deleteMarker}
            onSetInterpolation={setInterpolation}
          />
        </div>

        <aside className="editor-inspector video-inspector">
          <div className="editor-tool-selector" role="group" aria-label="Editing tool">
            <button type="button" className="active" aria-pressed="true">Reframe</button>
          </div>
          <div className="field">
            <label htmlFor="video-aspect">Aspect ratio</label>
            <select id="video-aspect" value={params.aspectRatio} disabled={busy} onChange={(event) => changeAspect(event.target.value as VideoReframeAspectRatio)}>
              {videoReframeAspects.map((aspect) => <option key={aspect} value={aspect}>{aspect}</option>)}
            </select>
          </div>
          <div className="field">
            <label htmlFor="video-output">Output size</label>
            <select id="video-output" value={outputPreset} disabled={busy} onChange={(event) => changeOutput(event.target.value as VideoOutputPreset)}>
              <option value="source">Source crop</option>
              <option value="720p">720p</option>
              <option value="1080p">1080p</option>
            </select>
          </div>
          <div className="video-framing-summary">
            <strong>{selectedRect.width} x {selectedRect.height}</strong>
            <span>Marker {selectedMarkerIndex + 1} of {params.markers.length}</span>
          </div>
          <div className="two-column">
            <label className="field" htmlFor="video-marker-x">
              X
              <input id="video-marker-x" type="number" value={Math.round(selectedRect.x)} disabled={busy} onChange={(event) => setSelectedXY('x', Number(event.target.value))} />
            </label>
            <label className="field" htmlFor="video-marker-y">
              Y
              <input id="video-marker-y" type="number" value={Math.round(selectedRect.y)} disabled={busy} onChange={(event) => setSelectedXY('y', Number(event.target.value))} />
            </label>
          </div>
          <div className="editor-tool-group">
            <button type="button" disabled={busy} onClick={() => nudgeSelected(-1, 0)} title="Move left">←</button>
            <button type="button" disabled={busy} onClick={() => nudgeSelected(0, -1)} title="Move up">↑</button>
            <button type="button" disabled={busy} onClick={() => nudgeSelected(0, 1)} title="Move down">↓</button>
            <button type="button" disabled={busy} onClick={() => nudgeSelected(1, 0)} title="Move right">→</button>
          </div>
          <div className="editor-tool-group">
            <button type="button" disabled={busy || !undoStack.length} onClick={undo}>Undo</button>
            <button type="button" disabled={busy || !redoStack.length} onClick={redo}>Redo</button>
            <button type="button" disabled={busy} onClick={resetDraft}>Reset</button>
          </div>
          {[...(source.notices || []), ...outputNotice].map((notice, index) => (
            <span key={index} className="editor-op-notice">{notice}</span>
          ))}
          <button type="button" className="editor-generate" disabled={busy || Boolean(loadError)} onClick={() => void submit()}>
            {loading ? 'Loading…' : busy ? 'Rendering…' : 'Render video'}
          </button>
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
        adoptError={adoptError ? <span className="editor-error">{adoptError}</span> : null}
        bodyReservePx={editsPanelBodyReservePx}
      >
        <ul className="editor-op-list">
          {orderedOps.map((op) => (
              <Fragment key={op.id}>
                <li className={`editor-op editor-op-${op.status}${compareOpID === op.id ? ' selected' : ''}`}>
                  <button
                    type="button"
                    className="editor-op-thumb video-op-thumb"
                    onClick={() => setCompareOpID((current) => (current === op.id ? '' : op.id))}
                    aria-label={compareOpID === op.id ? 'Hide result preview' : 'Preview this result'}
                  >
                    {op.resultUrl ? <video src={op.resultUrl} poster={posterURL(op.resultUrl)} muted preload="metadata" /> : <span className="editor-op-thumb-empty">{statusLabel(op.status)}</span>}
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
                    {op.reframe ? <button type="button" disabled={busy} onClick={() => void editFraming(op)}>Edit framing</button> : null}
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
                </li>
                {compareOpID === op.id && op.resultUrl ? (
                  <li className="editor-op-compare">
                    <div className="video-result-preview">
                      <video src={op.resultUrl} controls />
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
