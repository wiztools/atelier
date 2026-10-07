import {Fragment, useCallback, useEffect, useMemo, useRef, useState} from 'react';
import {AddEditResultToConversation, CancelImageEdit, ListEditSession, ListInpaintModels, SubmitImageEdit} from '../../wailsjs/go/main/App';
import {main} from '../../wailsjs/go/models';
import {EventsOff, EventsOn} from '../../wailsjs/runtime/runtime';
import {CanvasStage} from './CanvasStage';
import {MaskPoint, MaskStroke, exportMaskPNG, maskHasSelection, repaintMask} from './MaskBrushTool';

// ImageEditor is the edit workspace: canvas stage + mask brush on the left,
// the operation inspector on the right, the operation chain along the bottom.
// It is deliberately the shell only — canvas mechanics live in CanvasStage,
// mask mechanics in MaskBrushTool, and submission in one pipeline here. A
// future video editor reuses this shell with its own stage; the pieces that
// stay media-neutral are the draft state, the submit pipeline, and the result
// strip (see the plan's workspace architecture).
//
// Lifecycle: opening from an image card is an unsaved draft (no conversation
// exists; closing costs nothing). The first Generate creates the session
// child conversation and every operation after that appends to it, live
// state arriving via the atelier:edit-op event.

export type ImageEditorHandle = {
  source: main.EditSourceInfo;
  sessionID?: string;
};

const editEventOp = 'atelier:edit-op';

// The atelier:edit-op event payload — emitted by the Go side (edit_session.go)
// and not part of the generated bindings (nothing bound returns it), so it is
// typed here.
type EditOperationEventView = {
  sessionConversationId: string;
  operation: main.EditOperation;
};

function formatOperationCost(op: main.EditOperation): string {
  if (op.costUnknown) {
    return '?';
  }
  if ((op.costMicros ?? 0) > 0) {
    return `$${((op.costMicros ?? 0) / 1_000_000).toFixed(2)}`;
  }
  return '';
}

function statusLabel(status: string): string {
  switch (status) {
    case 'queued':
      return 'queued';
    case 'running':
      return 'generating…';
    case 'failed':
      return 'failed';
    case 'cancelled':
      return 'cancelled';
    default:
      return 'done';
  }
}

export function ImageEditor(props: {
  handle: ImageEditorHandle;
  defaultProvider: string;
  falDefaultModel: string;
  replicateDefaultModel: string;
  falHasKey: boolean;
  replicateHasKey: boolean;
  onClose: () => void;
  onOpenParent: (conversationID: string) => void;
  onSessionCreated: (sessionID: string) => void;
}) {
  const {handle} = props;
  const [session, setSession] = useState<main.EditSessionState | null>(null);
  const [loadError, setLoadError] = useState('');
  const [parentID] = useState(handle.source.conversationId);
  const [sourceArtifactID] = useState(handle.source.artifactId);
  const [sessionID, setSessionID] = useState(handle.sessionID ?? '');

  // The canvas state: which image is being painted on. Starts at the session's
  // source copy; "Use as source" re-bases it onto a result (and clears the
  // mask — a stale selection must never apply to a different image).
  const [canvas, setCanvas] = useState<{url: string; width: number; height: number; artifactID: string}>({
    url: handle.source.url,
    width: handle.source.width ?? 0,
    height: handle.source.height ?? 0,
    artifactID: '',
  });
  const maskCanvasRef = useRef<HTMLCanvasElement | null>(null);
  const [maskVersion, setMaskVersion] = useState(0);
  const [strokes, setStrokes] = useState<MaskStroke[]>([]);
  const [redoStrokes, setRedoStrokes] = useState<MaskStroke[]>([]);
  const [liveStroke, setLiveStroke] = useState<MaskStroke | null>(null);
  const [cursor, setCursor] = useState<MaskPoint | null>(null);
  const [brushSize, setBrushSize] = useState(28);
  const [brushMode, setBrushMode] = useState<'add' | 'subtract'>('add');

  const [prompt, setPrompt] = useState('');
  const [provider, setProvider] = useState(props.defaultProvider === 'replicate' ? 'replicate' : 'fal');
  const [falModel, setFalModel] = useState(props.falDefaultModel);
  const [replicateModel, setReplicateModel] = useState(props.replicateDefaultModel);
  // The verified inpaint catalogs (small curated lists from the backend).
  const [catalogs, setCatalogs] = useState<{fal: main.InpaintModelOption[]; replicate: main.InpaintModelOption[]}>({fal: [], replicate: []});
  useEffect(() => {
    let cancelled = false;
    Promise.all([ListInpaintModels('fal'), ListInpaintModels('replicate')])
      .then(([falList, replicateList]) => {
        if (!cancelled) {
          setCatalogs({fal: falList, replicate: replicateList});
        }
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, []);

  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState('');
  const [adoptingID, setAdoptingID] = useState('');
  const [adoptError, setAdoptError] = useState('');
  const [compareOpID, setCompareOpID] = useState('');

  // The Edits panel's height: draggable via the divider above it, persisted
  // in localStorage (the sidebar-width convention), and collapsible —
  // dragging the divider down past the floor snaps the panel shut, leaving a
  // slim restore bar.
  const resultsScrollRef = useRef<HTMLDivElement | null>(null);
  const [resultsHeight, setResultsHeight] = useState(loadEditorResultsHeight);
  const [resultsCollapsed, setResultsCollapsed] = useState(false);
  const [resizingResults, setResizingResults] = useState(false);
  const resultsDragRef = useRef<{startY: number; startHeight: number} | null>(null);

  const ops = session?.operations ?? [];
  // Newest iteration first: the list is chronological (the op chain advances
  // by appending), the strip renders it reversed so the latest attempt —
  // often the one you just cancelled or want to retry — is on top.
  const orderedOps = useMemo(() => [...ops].reverse(), [ops]);
  const runningOpID = session?.runningOperationId ?? '';
  const busy = submitting || runningOpID !== '';

  // Edits-panel drag: the divider sits between the canvas body and the
  // footer, so moving the mouse UP grows the panel. Dropping near the floor
  // snaps it collapsed; the height keeps the last usable value for restore.
  useEffect(() => {
    if (!resizingResults) {
      return;
    }
    const onMove = (event: MouseEvent) => {
      const drag = resultsDragRef.current;
      if (!drag) {
        return;
      }
      const next = clampEditorResultsHeight(drag.startHeight + (drag.startY - event.clientY));
      if (next <= minEditorResultsHeight + collapseSnapSlack) {
        setResultsCollapsed(true);
        setResizingResults(false);
        return;
      }
      setResultsHeight(next);
    };
    const onUp = () => setResizingResults(false);
    window.addEventListener('mousemove', onMove);
    window.addEventListener('mouseup', onUp);
    return () => {
      window.removeEventListener('mousemove', onMove);
      window.removeEventListener('mouseup', onUp);
    };
  }, [resizingResults]);

  useEffect(() => {
    window.localStorage.setItem('atelier.editorResultsHeight', String(resultsHeight));
  }, [resultsHeight]);

  // Bring the inline before/after expansion into view when the selection
  // changes — it renders below the clicked op, which may be off-screen.
  // Deliberately a CONTAINED scroll on the results pane only: scrollIntoView
  // also nudges every overflow ancestor (including the document itself), which
  // shifted the whole editor and clipped the header.
  useEffect(() => {
    if (!compareOpID) {
      return;
    }
    const container = resultsScrollRef.current;
    const target = document.getElementById(`editor-compare-${compareOpID}`);
    if (!container || !target) {
      return;
    }
    const cRect = container.getBoundingClientRect();
    const tRect = target.getBoundingClientRect();
    let top = container.scrollTop;
    if (tRect.bottom > cRect.bottom) {
      top += tRect.bottom - cRect.bottom;
    } else if (tRect.top < cRect.top) {
      top -= cRect.top - tRect.top;
    }
    container.scrollTo({top, behavior: 'smooth'});
  }, [compareOpID]);

  // The editor owns the whole window; nothing outside it should ever be
  // scrolled. Reset any document-level scroll picked up earlier.
  useEffect(() => {
    window.scrollTo(0, 0);
  }, []);

  // Artifact URL table: the source copy plus every result, so the chain and
  // the compare strip can render from ids.
  const artifactURLs = useMemo(() => {
    const table: Record<string, string> = {};
    if (session?.source.artifactId && session.source.url) {
      table[session.source.artifactId] = session.source.url;
    }
    for (const op of ops) {
      if (op.resultArtifactId && op.resultUrl) {
        table[op.resultArtifactId] = op.resultUrl;
      }
    }
    if (canvas.artifactID && canvas.url) {
      table[canvas.artifactID] = canvas.url;
    }
    return table;
  }, [session, ops, canvas]);

  // Mask raster lifecycle: rebuild when the canvas image changes dimensions.
  useEffect(() => {
    if (!canvas.width || !canvas.height) {
      return;
    }
    const next = document.createElement('canvas');
    next.width = canvas.width;
    next.height = canvas.height;
    repaintMask(next, []);
    maskCanvasRef.current = next;
    setStrokes([]);
    setRedoStrokes([]);
    setMaskVersion((v) => v + 1);
  }, [canvas.width, canvas.height]);

  // Session mode: load the whole chain once on open.
  useEffect(() => {
    if (!sessionID) {
      return;
    }
    let cancelled = false;
    ListEditSession(sessionID)
      .then((state) => {
        if (cancelled) {
          return;
        }
        setSession(state);
        if (state.source.url && state.source.width) {
          setCanvas((current) =>
            current.url === state.source.url && !current.artifactID
              ? {url: state.source.url, width: state.source.width ?? 0, height: state.source.height ?? 0, artifactID: state.source.artifactId}
              : current,
          );
        }
      })
      .catch((error) => {
        if (!cancelled) {
          setLoadError(String(error));
        }
      });
    return () => {
      cancelled = true;
    };
  }, [sessionID]);

  // Live operation transitions.
  useEffect(() => {
    const onUpdate = (event: EditOperationEventView) => {
      if (!event || event.sessionConversationId !== sessionID) {
        return;
      }
      setSession((current) => {
        if (!current) {
          return current;
        }
        const operations = [...current.operations];
        const idx = operations.findIndex((op) => op.id === event.operation.id);
        if (idx >= 0) {
          operations[idx] = event.operation;
        } else {
          operations.push(event.operation);
        }
        const runningOperationId = event.operation.status === 'queued' || event.operation.status === 'running'
          ? event.operation.id
          : '';
        return main.EditSessionState.createFrom({...current, operations, runningOperationId});
      });
    };
    EventsOn(editEventOp, onUpdate);
    return () => EventsOff(editEventOp);
  }, [sessionID]);

  // Mask painting: strokes accumulate in image space — the overlay renders
  // the in-progress stroke live, and the committed raster rebuilds from the
  // stroke list when a stroke ends (the single source of truth, so
  // undo/redo/clear are free). The stroke radius arrives in image pixels
  // (the stage converts the display-pixel brush size by the zoom) so the
  // brush paints what the cursor ring shows at every zoom level.
  const onStroke = useCallback(
    (kind: 'start' | 'extend' | 'end', point: MaskPoint, subtract: boolean, imageRadius: number) => {
      if (!maskCanvasRef.current || busy) {
        return;
      }
      const mode: 'add' | 'subtract' = subtract ? 'subtract' : brushMode;
      if (kind === 'start') {
        setLiveStroke({points: [point], radius: imageRadius, mode});
        return;
      }
      if (kind === 'extend') {
        setLiveStroke((current) => (current ? {...current, points: [...current.points, point]} : current));
        return;
      }
      // end: commit the live stroke.
      setLiveStroke((current) => {
        if (current) {
          setStrokes((list) => [...list, current]);
          setRedoStrokes([]);
        }
        return null;
      });
    },
    [brushMode, busy],
  );

  // Repaint the mask raster whenever the committed stroke list changes
  // (undo/redo/clear/source change) — the single source of truth.
  useEffect(() => {
    const mask = maskCanvasRef.current;
    if (!mask) {
      return;
    }
    repaintMask(mask, strokes);
    setMaskVersion((v) => v + 1);
  }, [strokes]);

  const undo = useCallback(() => {
    setStrokes((list) => {
      if (!list.length) {
        return list;
      }
      const last = list[list.length - 1];
      setRedoStrokes((redo) => [...redo, last]);
      return list.slice(0, -1);
    });
  }, []);

  const redo = useCallback(() => {
    setRedoStrokes((list) => {
      if (!list.length) {
        return list;
      }
      const last = list[list.length - 1];
      setStrokes((strokes) => [...strokes, last]);
      return list.slice(0, -1);
    });
  }, []);

  const clearMask = useCallback(() => {
    setStrokes([]);
    setRedoStrokes([]);
  }, []);

  // Keyboard: brush controls, sized for the editor and inert in text fields.
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement | null;
      if (target && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.tagName === 'SELECT' || target.isContentEditable)) {
        return;
      }
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'z') {
        event.preventDefault();
        if (event.shiftKey) {
          redo();
        } else {
          undo();
        }
        return;
      }
      if (event.key === '[') {
        setBrushSize((size) => Math.max(4, Math.round(size * 0.8)));
      }
      if (event.key === ']') {
        setBrushSize((size) => Math.min(400, Math.round(size * 1.25)));
      }
      if (event.key.toLowerCase() === 'b') {
        setBrushMode('add');
      }
      if (event.key.toLowerCase() === 'e') {
        setBrushMode('subtract');
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [undo, redo]);

  const effectiveModel = provider === 'replicate' ? replicateModel : falModel;
  const providerKeyed = provider === 'replicate' ? props.replicateHasKey : props.falHasKey;
  const selectionPresent = useMemo(() => {
    const mask = maskCanvasRef.current;
    return strokes.length > 0 || (!!mask && maskHasSelection(mask));
  }, [strokes, maskVersion]);

  const canGenerate =
    !busy &&
    prompt.trim().length > 0 &&
    selectionPresent &&
    effectiveModel.trim().length > 0 &&
    providerKeyed;

  async function generate() {
    if (!canGenerate || busy) {
      return;
    }
    const mask = maskCanvasRef.current;
    if (!mask) {
      return;
    }
    setSubmitting(true);
    setSubmitError('');
    setAdoptError('');
    try {
      const state = await SubmitImageEdit(main.ImageEditSubmitRequest.createFrom({
        kind: 'inpaint',
        parentConversationId: parentID,
        sourceArtifactId: sourceArtifactID,
        sessionConversationId: sessionID,
        inputArtifactId: canvas.artifactID,
        prompt: prompt.trim(),
        maskPng: exportMaskPNG(mask),
        provider,
        model: effectiveModel,
      }));
      if (state.createdSession) {
        setSessionID(state.sessionConversationId);
        props.onSessionCreated(state.sessionConversationId);
      }
    } catch (error) {
      setSubmitError(formatEditorError(error));
    } finally {
      setSubmitting(false);
    }
  }

  async function cancelRunning() {
    if (!sessionID || !runningOpID) {
      return;
    }
    try {
      await CancelImageEdit(sessionID, runningOpID);
    } catch (error) {
      setSubmitError(formatEditorError(error));
    }
  }

  function useAsSource(op: main.EditOperation) {
    if (!op.resultArtifactId || !op.resultUrl) {
      return;
    }
    setCanvas({
      url: op.resultUrl,
      width: op.resultWidth || canvas.width,
      height: op.resultHeight || canvas.height,
      artifactID: op.resultArtifactId,
    });
    setCompareOpID('');
  }

  async function addToOriginal(op: main.EditOperation) {
    if (!sessionID || op.adoptedAt) {
      return;
    }
    setAdoptingID(op.id);
    setAdoptError('');
    try {
      await AddEditResultToConversation(sessionID, op.id);
      setSession((current) => {
        if (!current) {
          return current;
        }
        const operations = current.operations.map((item) =>
          item.id === op.id ? main.EditOperation.createFrom({...item, adoptedAt: new Date().toISOString()}) : item,
        );
        return main.EditSessionState.createFrom({...current, operations});
      });
    } catch (error) {
      setAdoptError(formatEditorError(error));
    } finally {
      setAdoptingID('');
    }
  }

  return (
    <section className={`image-editor${resizingResults ? ' resizing-results' : ''}`}>
      <header className="editor-header">
        <button type="button" className="editor-back" onClick={props.onClose} aria-label="Close editor">
          ← Close
        </button>
        <div className="editor-breadcrumb">
          <span className="editor-crumb-title">
            {session?.parentTitle || handle.source.conversationTitle || 'Image edit'}
          </span>
          {sessionID ? (
            session?.parentAvailable ? (
              <button type="button" className="editor-crumb-link" onClick={() => props.onOpenParent(session?.parentConversationId ?? parentID)}>
                Open original chat
              </button>
            ) : (
              <span className="editor-crumb-missing">original unavailable</span>
            )
          ) : (
            <span className="editor-crumb-draft">unsaved draft</span>
          )}
        </div>
        <div className="editor-header-status">
          {busy ? <span className="editor-busy">edit in progress…</span> : null}
        </div>
      </header>

      {loadError ? <div className="editor-error">{loadError}</div> : null}

      <div className="editor-body">
        <div className="editor-canvas-column">
          <CanvasStage
            imageUrl={canvas.url}
            imageWidth={canvas.width}
            imageHeight={canvas.height}
            maskCanvas={maskCanvasRef.current}
            maskVersion={maskVersion}
            liveStroke={liveStroke}
            cursor={cursor}
            brushRadius={brushSize / 2}
            onStroke={onStroke}
            onCursor={setCursor}
          />
          <div className="editor-canvas-tools">
            <div className="editor-tool-group" role="group" aria-label="Brush mode">
              <button
                type="button"
                className={brushMode === 'add' ? 'active' : ''}
                onClick={() => setBrushMode('add')}
                aria-pressed={brushMode === 'add'}
                title="Brush — paint the region the AI may change (B)"
              >
                Brush
              </button>
              <button
                type="button"
                className={brushMode === 'subtract' ? 'active' : ''}
                onClick={() => setBrushMode('subtract')}
                aria-pressed={brushMode === 'subtract'}
                title="Subtract — remove from the selection (E, or Option-drag)"
              >
                Subtract
              </button>
            </div>
            <label className="editor-brush-size">
              Size
              <input
                type="range"
                min={4}
                max={200}
                value={brushSize}
                onChange={(event) => setBrushSize(Number(event.target.value))}
                aria-label="Brush size"
              />
              <span>{brushSize}px</span>
            </label>
            <div className="editor-tool-group">
              <button type="button" onClick={undo} disabled={!strokes.length} title="Undo stroke (⌘Z)">Undo</button>
              <button type="button" onClick={redo} disabled={!redoStrokes.length} title="Redo stroke (⇧⌘Z)">Redo</button>
              <button type="button" onClick={clearMask} disabled={!strokes.length && !redoStrokes.length}>Clear selection</button>
            </div>
          </div>
        </div>

        <aside className="editor-inspector">
          <div className="field">
            <label htmlFor="editor-prompt">What should change inside the selection?</label>
            <textarea
              id="editor-prompt"
              value={prompt}
              onChange={(event) => setPrompt(event.target.value)}
              placeholder="Describe the edit — e.g. replace the background with a beach at sunset"
              rows={3}
            />
          </div>

          <div className="two-column">
            <div className="field">
              <label htmlFor="editor-provider">Provider</label>
              <select
                id="editor-provider"
                value={provider}
                onChange={(event) => setProvider(event.target.value as 'fal' | 'replicate')}
              >
                <option value="fal">fal.ai</option>
                <option value="replicate" disabled={!props.replicateHasKey}>Replicate</option>
              </select>
            </div>
            <div className="field">
              <label htmlFor="editor-model">Model</label>
              <select
                id="editor-model"
                value={effectiveModel}
                onChange={(event) => (provider === 'replicate' ? setReplicateModel(event.target.value) : setFalModel(event.target.value))}
              >
                {(provider === 'replicate' ? catalogs.replicate : catalogs.fal).map((option) => (
                  <option key={option.id} value={option.id}>{option.label}</option>
                ))}
                {effectiveModel && !(provider === 'replicate' ? catalogs.replicate : catalogs.fal).some((option) => option.id === effectiveModel) ? (
                  <option value={effectiveModel}>{effectiveModel}</option>
                ) : null}
              </select>
            </div>
          </div>
          {!providerKeyed ? (
            <span className="hint">Add a {provider === 'replicate' ? 'Replicate' : 'fal.ai'} API key in Settings → Providers to generate edits.</span>
          ) : null}

          <button type="button" className="editor-generate" onClick={() => void generate()} disabled={!canGenerate}>
            {busy ? 'Working…' : 'Generate'}
          </button>
          {busy && runningOpID ? (
            <button type="button" className="editor-cancel" onClick={() => void cancelRunning()}>
              Cancel
            </button>
          ) : null}
          {submitError ? <div className="editor-error">{submitError}</div> : null}
          {!selectionPresent && prompt.trim() ? (
            <span className="hint">Paint a selection on the image first — only the painted region is edited.</span>
          ) : null}

          <div className="editor-session-note">
            {sessionID
              ? 'Every Generate adds an iteration to this edit session. The original image stays untouched.'
              : 'Generating creates an edit session attached to the original chat. The draft itself costs nothing.'}
          </div>
        </aside>
      </div>

      {resultsCollapsed ? (
        <button
          type="button"
          className="editor-results-restore"
          onClick={() => setResultsCollapsed(false)}
          title="Show the edits panel"
        >
          ▲ Edits{ops.length ? ` (${ops.length})` : ''}
        </button>
      ) : (
        <>
          <div
            className={`editor-results-resizer${resizingResults ? ' active' : ''}`}
            role="separator"
            aria-orientation="horizontal"
            aria-label="Resize edits panel"
            tabIndex={0}
            onMouseDown={(event) => {
              event.preventDefault();
              resultsDragRef.current = {startY: event.clientY, startHeight: resultsHeight};
              setResizingResults(true);
            }}
            onDoubleClick={() => setResultsCollapsed(true)}
            onKeyDown={(event) => {
              if (event.key === 'ArrowUp') {
                event.preventDefault();
                setResultsCollapsed(false);
                setResultsHeight(clampEditorResultsHeight(resultsHeight + 32));
              }
              if (event.key === 'ArrowDown') {
                event.preventDefault();
                const next = clampEditorResultsHeight(resultsHeight - 32);
                if (next <= minEditorResultsHeight) {
                  setResultsCollapsed(true);
                } else {
                  setResultsHeight(next);
                }
              }
            }}
          />
          <footer className="editor-results" style={{height: resultsHeight}}>
            <div className="editor-results-header">
              <h4>Edits</h4>
              {adoptError ? <span className="editor-error">{adoptError}</span> : null}
        </div>
        <div className="editor-results-scroll" ref={resultsScrollRef}>
          {ops.length === 0 ? (
            <p className="editor-results-empty">No edits yet — paint a selection and generate.</p>
          ) : (
            <ul className="editor-op-list">
              {orderedOps.map((op) => (
              <Fragment key={op.id}>
              <li className={`editor-op editor-op-${op.status}${compareOpID === op.id ? ' selected' : ''}`}>
                <button
                  type="button"
                  className="editor-op-thumb"
                  onClick={() => setCompareOpID((current) => (current === op.id ? '' : op.id))}
                  aria-pressed={compareOpID === op.id}
                  aria-label={compareOpID === op.id ? 'Hide before and after' : 'Compare this edit'}
                  title={compareOpID === op.id ? 'Hide before/after' : 'Compare before/after'}
                >
                  {op.resultUrl ? (
                    <img src={op.resultUrl} alt="" loading="lazy" />
                  ) : (
                    <span className="editor-op-thumb-empty">{statusLabel(op.status)}</span>
                  )}
                </button>
                <div className="editor-op-meta">
                  <span className="editor-op-title">
                    {op.inpaint?.prompt?.slice(0, 60) || 'Inpaint'} · {statusLabel(op.status)}
                  </span>
                  <span className="editor-op-sub">
                    {op.provider && op.model ? `${op.provider} · ${op.model}` : ''}
                    {formatOperationCost(op) ? ` · ${formatOperationCost(op)}` : ''}
                    {op.status === 'completed' ? '' : op.error ? ` — ${op.error}` : ''}
                  </span>
                  {(op.notices ?? []).map((notice, index) => (
                    <span key={index} className="editor-op-notice">{notice}</span>
                  ))}
                </div>
                <div className="editor-op-actions">
                  {op.status === 'completed' ? (
                    <>
                      <button type="button" onClick={() => useAsSource(op)} disabled={canvas.artifactID === op.resultArtifactId}>
                        Use as source
                      </button>
                      <button
                        type="button"
                        onClick={() => void addToOriginal(op)}
                        disabled={Boolean(op.adoptedAt) || adoptingID === op.id || (session ? !session.parentAvailable : true)}
                        title={op.adoptedAt ? 'Already added to the original chat' : 'Copy this result into the original conversation'}
                      >
                        {op.adoptedAt ? 'Added' : adoptingID === op.id ? 'Adding…' : 'Add to original chat'}
                      </button>
                    </>
                  ) : op.status === 'failed' || op.status === 'cancelled' ? (
                    <span className="editor-op-retry-hint">Adjust and generate to retry</span>
                  ) : null}
                </div>
              </li>
              {compareOpID === op.id && op.status === 'completed' && op.resultUrl ? (
                <li className="editor-op-compare" id={`editor-compare-${op.id}`}>
                  <div className="editor-compare">
                    <figure>
                      <img src={artifactURLs[op.inputArtifactId ?? ''] ?? canvas.url} alt="Before" />
                      <figcaption>Before</figcaption>
                    </figure>
                    <figure>
                      <img src={op.resultUrl} alt="After" />
                      <figcaption>After</figcaption>
                    </figure>
                  </div>
                </li>
              ) : null}
              </Fragment>
            ))}
            </ul>
          )}
        </div>
      </footer>
        </>
      )}
    </section>
  );
}

function formatEditorError(error: unknown): string {
  if (typeof error === 'string') {
    return error;
  }
  if (error instanceof Error) {
    return error.message;
  }
  try {
    return JSON.stringify(error);
  } catch {
    return String(error);
  }
}

// Edits-panel sizing: the divider drag and the persisted height. The floor
// plus the snap slack define where a downward drag collapses the panel
// entirely (leaving the slim restore bar).
const minEditorResultsHeight = 120;
const collapseSnapSlack = 24;
const defaultEditorResultsHeight = 260;

function clampEditorResultsHeight(height: number): number {
  // The ceiling leaves room for the header, the canvas column's content floor
  // (stage minimum + tools row), the divider, and the editor's padding — past
  // that the footer would start clipping instead of trading space.
  const max = Math.max(minEditorResultsHeight, window.innerHeight - 430);
  return Math.round(Math.max(minEditorResultsHeight, Math.min(height, max)));
}

function loadEditorResultsHeight(): number {
  const stored = Number(window.localStorage.getItem('atelier.editorResultsHeight'));
  if (Number.isFinite(stored) && stored >= minEditorResultsHeight) {
    return clampEditorResultsHeight(stored);
  }
  return defaultEditorResultsHeight;
}
