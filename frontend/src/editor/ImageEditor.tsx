import {Fragment, useCallback, useEffect, useMemo, useRef, useState} from 'react';
import {AddEditResultToConversation, CancelImageEdit, DeleteEditOperation, ListEditSession, ListInpaintModels, ReleaseEditSourcePreview, SubmitImageEdit} from '../../wailsjs/go/main/App';
import {main} from '../../wailsjs/go/models';
import {EventsOff, EventsOn} from '../../wailsjs/runtime/runtime';
import {EditsPanel} from './EditsPanel';
import {EditKebabMenu} from './EditKebabMenu';
import {EditorHeader} from './EditorHeader';
import {CanvasStage} from './CanvasStage';
import {MaskPoint, MaskStroke, exportMaskPNG, maskHasSelection, repaintMask} from './MaskBrushTool';
import {CropRect, fitCrop, moveCrop, pixelCrop} from './cropGeometry';

// ImageEditor is the edit workspace: canvas stage + mask brush on the left,
// the operation inspector on the right, the operation chain along the bottom.
// It is deliberately the shell only — canvas mechanics live in CanvasStage,
// mask mechanics in MaskBrushTool, and submission in one pipeline here. The
// shared chrome — the header bar and the resizable Edits footer — lives in
// EditorHeader/EditsPanel; the video editor composes the same shell with its
// own stage.
//
// Lifecycle: opening from an image card is an unsaved draft (no conversation
// exists; closing costs nothing). The first applied edit creates the session
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

const cropRatios = ['free', 'original', '1:1', '4:3', '3:2', '16:9', '4:5', '9:16'];
const terminalStatus = (status: string) => ['completed', 'failed', 'cancelled'].includes(status);
function mergeOperations(base: main.EditOperation[], updates: main.EditOperation[]): main.EditOperation[] {
  const merged = [...base];
  for (const update of updates) {
    const index = merged.findIndex((op) => op.id === update.id);
    if (index < 0) merged.push(update);
    else if (!terminalStatus(merged[index].status) || terminalStatus(update.status)) merged[index] = update;
  }
  return merged;
}
function operationTitle(op: main.EditOperation): string {
  if (op.kind === 'crop' && op.crop) return `Crop · ${op.crop.aspectRatio || 'Free'} · ${op.resultWidth || op.crop.width} × ${op.resultHeight || op.crop.height}`;
  if (op.kind === 'inpaint') return op.inpaint?.prompt?.slice(0, 60) || 'Inpaint';
  return op.kind || 'Edit';
}

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
      return 'working…';
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
  const [canvas, setCanvas] = useState<{url: string; width: number; height: number; artifactID: string; sourceKey: string}>({
    url: handle.source.url,
    width: handle.source.width ?? 0,
    height: handle.source.height ?? 0,
    artifactID: handle.sessionID ? handle.source.artifactId : '',
    sourceKey: handle.source.artifactId,
  });
  const maskCanvasRef = useRef<HTMLCanvasElement | null>(null);
  const [maskVersion, setMaskVersion] = useState(0);
  const [strokes, setStrokes] = useState<MaskStroke[]>([]);
  const [redoStrokes, setRedoStrokes] = useState<MaskStroke[]>([]);
  const [liveStroke, setLiveStroke] = useState<MaskStroke | null>(null);
  const [cursor, setCursor] = useState<MaskPoint | null>(null);
  const [brushSize, setBrushSize] = useState(28);
  const [brushMode, setBrushMode] = useState<'add' | 'subtract'>('add');

  const [tool, setTool] = useState<'inpaint' | 'crop'>('crop');
  const [cropRatio, setCropRatio] = useState('free');
  const [cropRect, setCropRect] = useState<CropRect>(() => fitCrop(handle.source.width || 0, handle.source.height || 0, null));
  const [cropUndo, setCropUndo] = useState<{rect: CropRect; ratio: string}[]>([]);
  const [cropRedo, setCropRedo] = useState<{rect: CropRect; ratio: string}[]>([]);
  const cropAspect = cropRatio === 'free' ? null : cropRatio === 'original'
    ? canvas.width / canvas.height : cropRatio.split(':').map(Number).reduce((a, b) => a / b);
  const canonicalCrop = pixelCrop(cropRect, canvas.width, canvas.height, cropAspect);
  const cropChanged = canonicalCrop.x !== 0 || canonicalCrop.y !== 0 || canonicalCrop.width !== canvas.width || canonicalCrop.height !== canvas.height;
  const eventOperations = useRef(new Map<string, main.EditOperation>());
  const submitLock = useRef(false);
  const previewReleases = useRef(new Map<string, ReturnType<typeof setTimeout>>());
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
  const [deletingID, setDeletingID] = useState('');
  const [deleteError, setDeleteError] = useState('');
  const [compareOpID, setCompareOpID] = useState('');

  // The Edits panel's scroll pane: the compare-scroll effect scrolls it
  // directly (contained), the panel itself owns the height/collapse logic.
  const resultsScrollRef = useRef<HTMLDivElement | null>(null);

  const ops = session?.operations ?? [];
  // Newest iteration first: the list is chronological (the op chain advances
  // by appending), the strip renders it reversed so the latest attempt —
  // often the one you just cancelled or want to retry — is on top.
  const orderedOps = useMemo(() => [...ops].reverse(), [ops]);
  const runningOpID = session?.runningOperationId ?? '';
  const busy = submitting || runningOpID !== '';

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
    if (session?.source?.artifactId && session.source.url) {
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

  // Reset tool drafts whenever the displayed source identity changes.
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
    setLiveStroke(null);
    setCursor(null);
    setCropRatio('free');
    setCropRect(fitCrop(canvas.width, canvas.height, null));
    setCropUndo([]);
    setCropRedo([]);
  }, [canvas.width, canvas.height, canvas.sourceKey]);

  useEffect(() => {
    const url = canvas.url;
    const pending = previewReleases.current.get(url);
    if (pending !== undefined) clearTimeout(pending);
    previewReleases.current.delete(url);
    // Defer cleanup so StrictMode's setup/cleanup replay cannot delete a
    // preview that the remounted effect still displays.
    return () => {
      previewReleases.current.set(url, setTimeout(() => {
        previewReleases.current.delete(url);
        void ReleaseEditSourcePreview(url).catch(() => {});
      }, 0));
    };
  }, [canvas.url]);

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
        setSession((current) => {
          const operations = mergeOperations(state.operations, [
            ...(current?.conversationId === state.conversationId ? current.operations : []),
            ...Array.from(eventOperations.current.values()).filter((op) => state.operations.some((item) => item.id === op.id)),
          ]);
          return main.EditSessionState.createFrom({...state, operations,
            runningOperationId: operations.find((op) => !terminalStatus(op.status))?.id || ''});
        });
        if (state.source.url && state.source.width) {
          setCanvas((current) =>
            !current.artifactID
              ? {url: state.source.url, width: state.source.width ?? 0, height: state.source.height ?? 0, artifactID: state.source.artifactId, sourceKey: current.sourceKey}
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

  // Subscribe before the first submit: local edits can finish before a new
  // session ID reaches React. Buffer transitions and reconcile the snapshot.
  useEffect(() => {
    const onUpdate = (event: EditOperationEventView) => {
      if (!event?.operation) return;
      eventOperations.current.set(event.operation.id, event.operation);
      setSession((current) => {
        if (!current || current.conversationId !== event.sessionConversationId) return current;
        const operations = mergeOperations(current.operations, [event.operation]);
        return main.EditSessionState.createFrom({...current, operations,
          runningOperationId: operations.find((op) => !terminalStatus(op.status))?.id || ''});
      });
    };
    EventsOn(editEventOp, onUpdate);
    return () => EventsOff(editEventOp);
  }, []);

  function commitCrop(before: CropRect, ratio = cropRatio) {
    setCropUndo((list) => [...list.slice(-99), {rect: before, ratio}]);
    setCropRedo([]);
  }
  function changeRatio(next: string) {
    commitCrop(cropRect);
    setCropRatio(next);
    const ratio = next === 'free' ? null : next === 'original' ? canvas.width / canvas.height
      : next.split(':').map(Number).reduce((a, b) => a / b);
    setCropRect(fitCrop(canvas.width, canvas.height, ratio, {x: cropRect.x + cropRect.width / 2, y: cropRect.y + cropRect.height / 2}));
  }
  function undoCrop() {
    const previous = cropUndo[cropUndo.length - 1];
    if (!previous) return;
    setCropRedo((list) => [...list, {rect: cropRect, ratio: cropRatio}]);
    setCropUndo((list) => list.slice(0, -1));
    setCropRect(previous.rect); setCropRatio(previous.ratio);
  }
  function redoCrop() {
    const next = cropRedo[cropRedo.length - 1];
    if (!next) return;
    setCropUndo((list) => [...list, {rect: cropRect, ratio: cropRatio}]);
    setCropRedo((list) => list.slice(0, -1));
    setCropRect(next.rect); setCropRatio(next.ratio);
  }
  function cancelCrop() {
    setCropRect(fitCrop(canvas.width, canvas.height, null));
    setCropRatio('free'); setCropUndo([]); setCropRedo([]); setTool('inpaint');
  }

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
      if (busy) return;
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'z') {
        event.preventDefault();
        if (event.shiftKey) {
          if (tool === 'crop') redoCrop(); else redo();
        } else {
          if (tool === 'crop') undoCrop(); else undo();
        }
        return;
      }
      if (tool === 'crop') {
        if (event.key === 'Escape') {event.preventDefault(); cancelCrop();}
        if (event.key === 'Enter' && target?.tagName !== 'BUTTON') {event.preventDefault(); void generate();}
        if (event.key.startsWith('Arrow') && target?.tagName !== 'BUTTON') {
          event.preventDefault();
          const step = event.shiftKey ? 10 : 1;
          commitCrop(cropRect);
          setCropRect(moveCrop(cropRect, event.key === 'ArrowLeft' ? -step : event.key === 'ArrowRight' ? step : 0,
            event.key === 'ArrowUp' ? -step : event.key === 'ArrowDown' ? step : 0, canvas.width, canvas.height));
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
  }, [undo, redo, tool, busy, cropRect, cropRatio, cropUndo, cropRedo, canvas.width, canvas.height, sessionID, prompt, provider, falModel, replicateModel, strokes]);

  const effectiveModel = provider === 'replicate' ? replicateModel : falModel;
  const providerKeyed = provider === 'replicate' ? props.replicateHasKey : props.falHasKey;
  const selectionPresent = useMemo(() => {
    const mask = maskCanvasRef.current;
    return strokes.length > 0 || (!!mask && maskHasSelection(mask));
  }, [strokes, maskVersion]);

  const canGenerate =
    !busy &&
    (!sessionID || !!canvas.artifactID) &&
    prompt.trim().length > 0 &&
    selectionPresent &&
    effectiveModel.trim().length > 0 &&
    providerKeyed;

  const canCrop = !busy && (!sessionID || !!canvas.artifactID) && canvas.width > 0 && canvas.height > 0 && cropChanged && canonicalCrop.width > 0 && canonicalCrop.height > 0;

  async function generate() {
    if (submitLock.current || busy || !(tool === 'crop' ? canCrop : canGenerate)) {
      return;
    }
    const mask = maskCanvasRef.current;
    if (tool === 'inpaint' && !mask) return;
    submitLock.current = true;
    setSubmitting(true);
    setSubmitError('');
    setAdoptError('');
    try {
      const state = await SubmitImageEdit(main.ImageEditSubmitRequest.createFrom({
        kind: tool,
        parentConversationId: parentID,
        sourceArtifactId: sourceArtifactID,
        sessionConversationId: sessionID,
        inputArtifactId: sessionID ? canvas.artifactID : '',
        sourceDigest: sessionID ? '' : handle.source.sourceDigest,
        crop: tool === 'crop' ? {...canonicalCrop, sourceWidth: canvas.width, sourceHeight: canvas.height, aspectRatio: cropRatio === 'free' ? '' : cropRatio} : undefined,
        prompt: prompt.trim(),
        maskPng: tool === 'inpaint' && mask ? exportMaskPNG(mask) : '',
        provider: tool === 'inpaint' ? provider : '',
        model: tool === 'inpaint' ? effectiveModel : '',
      }));
      setSession((current) => {
        const operations = mergeOperations(current?.operations || [], [state.operation, eventOperations.current.get(state.operation.id) || state.operation]);
        return main.EditSessionState.createFrom({source: handle.source, parentConversationId: parentID, parentAvailable: true, ...current, conversationId: state.sessionConversationId,
          operations, runningOperationId: operations.find((op) => !terminalStatus(op.status))?.id || ''});
      });
      if (state.createdSession) {
        setSessionID(state.sessionConversationId);
        props.onSessionCreated(state.sessionConversationId);
      }
    } catch (error) {
      setSubmitError(formatEditorError(error));
    } finally {
      submitLock.current = false;
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
      sourceKey: op.resultArtifactId,
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

  // Delete one edit from the session — its turn and the files it owns. The
  // backend copies results on adoption, so "Added" parent entries survive.
  // When the deleted result is what the canvas shows, fall back to the newest
  // surviving completed result, else the session's own source copy (the
  // sourceKey change resets the mask strokes via the canvas effect).
  async function deleteOp(op: main.EditOperation) {
    if (!sessionID || deletingID) {
      return;
    }
    setDeletingID(op.id);
    setDeleteError('');
    try {
      await DeleteEditOperation(sessionID, op.id);
      eventOperations.current.delete(op.id);
      const remaining = ops.filter((item) => item.id !== op.id);
      setSession((current) => {
        if (!current) {
          return current;
        }
        const operations = current.operations.filter((item) => item.id !== op.id);
        return main.EditSessionState.createFrom({...current, operations,
          runningOperationId: operations.find((item) => !terminalStatus(item.status))?.id || ''});
      });
      setCompareOpID((current) => (current === op.id ? '' : current));
      if (op.resultArtifactId && canvas.artifactID === op.resultArtifactId) {
        let next: {url: string; width: number; height: number; artifactID: string; sourceKey: string} | null = null;
        for (let i = remaining.length - 1; i >= 0 && !next; i--) {
          const item = remaining[i];
          if (item.status === 'completed' && item.resultArtifactId && item.resultUrl) {
            next = {url: item.resultUrl, width: item.resultWidth || canvas.width, height: item.resultHeight || canvas.height, artifactID: item.resultArtifactId, sourceKey: item.resultArtifactId};
          }
        }
        const source = session?.source;
        if (!next && source?.url && source.artifactId) {
          next = {url: source.url, width: source.width ?? 0, height: source.height ?? 0, artifactID: source.artifactId, sourceKey: source.artifactId};
        }
        if (next) {
          setCanvas(next);
          setCompareOpID('');
        }
      }
    } catch (error) {
      setDeleteError(formatEditorError(error));
    } finally {
      setDeletingID('');
    }
  }

  return (
    <section className="image-editor">
      <EditorHeader
        title={session?.parentTitle || handle.source.conversationTitle || 'Image edit'}
        sessionID={sessionID || undefined}
        parentAvailable={session?.parentAvailable}
        parentConversationID={session?.parentConversationId ?? parentID}
        busyLabel={busy ? 'edit in progress…' : ''}
        onClose={props.onClose}
        onOpenParent={props.onOpenParent}
      />

      {loadError ? <div className="editor-error">{loadError}</div> : null}

      <div className="editor-body">
        <div className="editor-canvas-column">
          <CanvasStage
            crop={tool === 'crop' ? {rect: cropRect, ratio: cropAspect, onChange: setCropRect, onCommit: commitCrop} : undefined}
            disabled={busy}
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
            {tool === 'crop' ? <>
              <span className="hint">Drag to move · handles to resize · Shift-drag to pan</span>
              <div className="editor-tool-group">
                <button type="button" disabled={busy || !cropUndo.length} onClick={undoCrop}>Undo</button>
                <button type="button" disabled={busy || !cropRedo.length} onClick={redoCrop}>Redo</button>
              </div>
            </> : <>
            <div className="editor-tool-group" role="group" aria-label="Brush mode">
              <button
                type="button"
                disabled={busy}
                className={brushMode === 'add' ? 'active' : ''}
                onClick={() => setBrushMode('add')}
                aria-pressed={brushMode === 'add'}
                title="Brush — paint the region the AI may change (B)"
              >
                Brush
              </button>
              <button
                type="button"
                disabled={busy}
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
                disabled={busy}
                min={4}
                max={200}
                value={brushSize}
                onChange={(event) => setBrushSize(Number(event.target.value))}
                aria-label="Brush size"
              />
              <span>{brushSize}px</span>
            </label>
            <div className="editor-tool-group">
              <button type="button" onClick={undo} disabled={busy || !strokes.length} title="Undo stroke (⌘Z)">Undo</button>
              <button type="button" onClick={redo} disabled={busy || !redoStrokes.length} title="Redo stroke (⇧⌘Z)">Redo</button>
              <button type="button" onClick={clearMask} disabled={busy || (!strokes.length && !redoStrokes.length)}>Clear selection</button>
            </div>
            </>}
          </div>
        </div>

        <aside className="editor-inspector">
          <div className="editor-tool-selector" role="group" aria-label="Editing tool">
            <button type="button" className={tool === 'crop' ? 'active' : ''} aria-pressed={tool === 'crop'} disabled={busy} onClick={() => setTool('crop')}>Crop</button>
            <button type="button" className={tool === 'inpaint' ? 'active' : ''} aria-pressed={tool === 'inpaint'} disabled={busy} onClick={() => setTool('inpaint')}>Inpaint</button>
          </div>
          {tool === 'crop' ? <>
            <div className="field">
              <label htmlFor="editor-crop-ratio">Aspect ratio</label>
              <div className="editor-crop-ratio-row">
                <select id="editor-crop-ratio" value={cropRatio} disabled={busy} onChange={(event) => changeRatio(event.target.value)}>
                  {Array.from(new Set([...cropRatios, cropRatio])).map((ratio) => <option key={ratio} value={ratio}>{ratio === 'free' ? 'Free' : ratio === 'original' ? 'Original' : ratio}</option>)}
                </select>
                <button type="button" title="Swap portrait and landscape" aria-label="Swap crop orientation" disabled={busy || !cropAspect || cropAspect === 1}
                  onClick={() => changeRatio(cropRatio === 'original' ? `${canvas.height}:${canvas.width}` : cropRatio.split(':').reverse().join(':'))}>⇄</button>
              </div>
            </div>
            <div className="editor-crop-summary" aria-live="polite">
              <strong>{canonicalCrop.width} × {canonicalCrop.height} px</strong>
              <span>{cropAspect ? 'Aspect ratio locked' : 'Unconstrained rectangle'}</span>
            </div>
            <p className="hint">Move the crop to frame your subject. Drag outside it to draw a new selection.</p>
            <div className="editor-tool-group">
              <button type="button" disabled={busy} onClick={() => {commitCrop(cropRect); setCropRect(fitCrop(canvas.width, canvas.height, cropAspect));}}>Reset</button>
              <button type="button" disabled={busy} onClick={cancelCrop}>Cancel crop</button>
            </div>
            <button type="button" className="editor-generate" disabled={!canCrop} onClick={() => void generate()}>{busy ? 'Working…' : 'Apply crop'}</button>
            <span className="hint">Applied locally. The original stays untouched.</span>
          </> : <>

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
          </>}
          {busy && runningOpID ? (
            <button type="button" className="editor-cancel" onClick={() => void cancelRunning()}>
              Cancel
            </button>
          ) : null}
          {submitError ? <div className="editor-error">{submitError}</div> : null}
          {tool === 'inpaint' && !selectionPresent && prompt.trim() ? (
            <span className="hint">Paint a selection on the image first — only the painted region is edited.</span>
          ) : null}

          <div className="editor-session-note">
            {sessionID
              ? 'Every applied edit adds a result to this session. Use as source to continue from a result.'
              : 'Applying an edit creates a session attached to the original chat. Unapplied drafts are not saved.'}
          </div>
        </aside>
      </div>

      <EditsPanel
        count={ops.length}
        emptyText={tool === 'crop' ? 'No edits yet — adjust the crop and apply.' : 'No edits yet — paint a selection and generate.'}
        adoptError={
          <>
            {adoptError ? <span className="editor-error">{adoptError}</span> : null}
            {deleteError ? <span className="editor-error">{deleteError}</span> : null}
          </>
        }
        scrollRef={resultsScrollRef}
      >
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
                    {operationTitle(op)} · {statusLabel(op.status)}
                  </span>
                  <span className="editor-op-sub">
                    {op.backend ? 'Local' : op.provider && op.model ? `${op.provider} · ${op.model}` : ''}
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
                      <button type="button" onClick={() => useAsSource(op)} disabled={busy || canvas.artifactID === op.resultArtifactId}>
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
                    <span className="editor-op-retry-hint">{op.kind === 'crop' ? 'Adjust and apply to retry' : 'Adjust and generate to retry'}</span>
                  ) : null}
                </div>
                <EditKebabMenu
                  label={`Options for ${operationTitle(op)}`}
                  disabled={busy || !sessionID || !terminalStatus(op.status) || deletingID !== ''}
                  deleteInProgress={deletingID === op.id}
                  onDelete={() => void deleteOp(op)}
                />
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
      </EditsPanel>
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
