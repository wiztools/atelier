import {useEffect, useMemo, useRef, useState} from 'react';
import * as AppBindings from '../../wailsjs/go/main/App';
import {CancelCompositionRender, SaveComposition, SaveVideo} from '../../wailsjs/go/main/App';
import {main} from '../../wailsjs/go/models';
import {EventsOff, EventsOn} from '../../wailsjs/runtime/runtime';
import {EditsPanel} from './EditsPanel';
import {EditorHeader} from './EditorHeader';
import {MediaPicker} from './MediaPicker';
import {VideoTimelineControls} from './VideoTimelineControls';
import {
  CompositionClip,
  CompositionDoc,
  CompositionFit,
  CompositionSegment,
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
  compositionTransitions,
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
  | {kind: 'junction'; clipID: string}
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

// compositionPosterURL mirrors the app-wide posterURLForVideoSrc convention:
// a /atelier-artifact video src swaps its extension for _poster.jpg (the
// renderer writes the sibling; a 404 degrades to the bare video element).
function compositionPosterURL(src: string): string {
  if (!src.startsWith('/atelier-artifact/')) return '';
  return src.replace(/\.[^.]+$/, '_poster.jpg');
}

function asArray<T>(value: T[] | null | undefined): T[] {
  return Array.isArray(value) ? value : [];
}

function findSegment(doc: CompositionDoc, segmentID: string): CompositionSegment | undefined {
  for (const lane of doc.audioLanes) {
    const segment = lane.segments.find((candidate) => candidate.id === segmentID);
    if (segment) {
      return segment;
    }
  }
  return undefined;
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

// DuplicateIcon/TrashIcon — the app's 24-viewBox stroke glyphs; the hover
// title carries the verb.
function DuplicateIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <rect x="9" y="9" width="13" height="13" rx="2" />
      <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" />
    </svg>
  );
}

function TrashIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M3 6h18" />
      <path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6" />
      <path d="M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" />
    </svg>
  );
}

// TransitionControls is the Kind/Seconds/crossfade block for the transition
// ONTO the clip after clipID — shared by the clip inspector ("Transition to
// next") and the junction's own inspector.
function TransitionControls(props: {doc: CompositionDoc; clipID: string; commit: (doc: CompositionDoc) => void}) {
  const index = props.doc.video.findIndex((clip) => clip.id === props.clipID);
  const clip = props.doc.video[index];
  const bounds = transitionBounds(props.doc, index);
  if (!clip || index >= props.doc.video.length - 1) {
    return <p className="hint">The last clip has no next neighbor.</p>;
  }
  if (!bounds) {
    return <p className="hint">The adjacent clips are too short for even the minimum {compositionTransitionMinSeconds}s dissolve — trim elsewhere first.</p>;
  }
  const transition = clip.transitionToNext;
  return (
    <>
      <div className="composition-field-row">
        <label className="composition-field" title="The overlap effect between the two clips; every name is an ffmpeg xfade transition this composer renders (an install lacking one falls back to a plain dissolve)">
          <span>Kind</span>
          <select
            value={transition ? transition.kind : 'cut'}
            onChange={(event) => props.commit(setClipTransition(props.doc, props.clipID, event.target.value === 'cut' ? null : {kind: event.target.value, seconds: transition?.seconds ?? 1, audioCrossfade: transition?.audioCrossfade ?? true}))}
          >
            <option value="cut">cut</option>
            {compositionTransitions.map((name) => <option key={name} value={name}>{name}</option>)}
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
            onCommit={(value) => props.commit(setClipTransition(props.doc, props.clipID, {kind: transition.kind, seconds: value, audioCrossfade: transition.audioCrossfade}))}
          />
        ) : null}
      </div>
      {transition ? (
        <label className="composition-toggle" title="Crossfade the original audio across the overlap (equal power), or cut it hard at the junction">
          <input type="checkbox" checked={transition.audioCrossfade} onChange={(event) => props.commit(setClipTransition(props.doc, props.clipID, {kind: transition.kind, seconds: transition.seconds, audioCrossfade: event.target.checked}))} />
          crossfade original audio
        </label>
      ) : null}
      <p className="hint">Allowed {bounds.min.toFixed(1)}–{bounds.max.toFixed(1)}s (half the shorter clip).</p>
    </>
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

  // Render ops this session, newest first — every transition arrives as an
  // atelier:composition-op event and folds into the row (terminal states
  // never overwrite a later one because the op id keys the merge).
  const [renders, setRenders] = useState<main.CompositionRenderOperation[]>([]);
  const [renderError, setRenderError] = useState('');
  // The stage plays the chosen completed render; renderedDoc is the snapshot
  // it was built from, so edits after a render show the stale note.
  const [preview, setPreview] = useState<{url: string; audioOnly: boolean; offsetSeconds: number} | null>(null);
  const [renderedDoc, setRenderedDoc] = useState<CompositionDoc | null>(null);
  const [rangeFrom, setRangeFrom] = useState('');
  const [rangeTo, setRangeTo] = useState('');
  const [soloLaneID, setSoloLaneID] = useState('');

  // Export choices (AU-11's audio toggle; the advanced overrides are
  // per-export — empty inherits the revision's own output settings).
  const [exportIncludeAudio, setExportIncludeAudio] = useState(true);
  const [exportAdvancedOpen, setExportAdvancedOpen] = useState(false);
  const [exportWidth, setExportWidth] = useState('');
  const [exportHeight, setExportHeight] = useState('');
  const [exportFPS, setExportFPS] = useState('');
  const [exportCRF, setExportCRF] = useState('18');
  const [exporting, setExporting] = useState(false);
  const [exportError, setExportError] = useState('');

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
  // Drags coalesce to ONE undo entry, taken lazily: a pointerdown that never
  // moves (a plain select click) pushes nothing.
  const pushUndoSnapshot = () => {
    setUndoStack((stack) => [...stack, docRef.current].slice(-undoLimit));
    setRedoStack([]);
  };
  const dragChangedRef = useRef(false);
  const liveSetDoc = (next: CompositionDoc) => {
    setDoc(next);
    docRef.current = next;
  };
  const applyLive = (next: CompositionDoc) => {
    if (sameCompositionDoc(next, docRef.current)) {
      return;
    }
    if (!dragChangedRef.current) {
      dragChangedRef.current = true;
      pushUndoSnapshot();
    }
    liveSetDoc(next);
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

  // Render-op events fold into the rows; a completion also takes the stage,
  // and a completed EXPORT adopted a new artifact into the conversation —
  // the same refresh the save fires. The callback rides a ref because this
  // effect's deps are deliberately empty (one subscription).
  const savedToConversationRef = useRef(props.onSavedToConversation);
  useEffect(() => {
    savedToConversationRef.current = props.onSavedToConversation;
  }, [props.onSavedToConversation]);
  useEffect(() => {
    const onUpdate = (event: any) => {
      const op = main.CompositionRenderOperation.createFrom(event);
      setRenders((current) => {
        const index = current.findIndex((candidate) => candidate.id === op.id);
        if (index < 0) {
          return [op, ...current];
        }
        const next = [...current];
        next[index] = op;
        return next;
      });
      if (op.status === 'completed' && op.resultUrl) {
        if (op.mode === 'export') {
          savedToConversationRef.current?.();
          return;
        }
        setPreview({url: op.resultUrl, audioOnly: op.audioOnly, offsetSeconds: op.rangeStartSeconds ?? 0});
      }
    };
    EventsOn('atelier:composition-op', onUpdate);
    return () => {
      EventsOff('atelier:composition-op');
    };
  }, []);

  const runningRender = renders.find((op) => op.status === 'queued' || op.status === 'running') ?? null;
  // SubmitCompositionRender emits the queued op as an event BEFORE its
  // promise resolves, so the event handler usually inserts the row first —
  // the submit-side insert must not prepend a second copy of the same id
  // (the twin would freeze at 0%: events dedupe to the first match only).
  const addRenderOp = (op: main.CompositionRenderOperation) => {
    setRenders((current) => current.some((candidate) => candidate.id === op.id) ? current : [op, ...current]);
  };
  const renderDisabled = !!runningRender || problems.length > 0 || missingRefs.size > 0 ||
    (doc.video.length === 0 && doc.audioLanes.every((lane) => lane.segments.length === 0));
  const previewOutdated = !!preview && renderedDoc !== null && !sameCompositionDoc(renderedDoc, doc);

  // The playhead: a timeline line pinned to the preview player's position.
  // The line's left is set imperatively (a 60fps React re-render for one
  // moving pixel is waste); a rAF loop runs only while the player plays, and
  // time/seek events cover the paused cases. The offset maps a ranged render
  // back onto the full timeline.
  const previewMediaRef = useRef<HTMLMediaElement | null>(null);
  const playheadRef = useRef<HTMLDivElement | null>(null);
  const playheadLoopRef = useRef<number | null>(null);
  const updatePlayhead = () => {
    const media = previewMediaRef.current;
    const head = playheadRef.current;
    const track = laneElementRef.current;
    if (!media || !head || !track || timelineExtent <= 0) {
      return;
    }
    const trackRect = track.getBoundingClientRect();
    const timelineRect = (head.parentElement as HTMLElement).getBoundingClientRect();
    const at = Math.min(Math.max(0, ((preview?.offsetSeconds ?? 0) + media.currentTime) / timelineExtent), 1);
    head.style.left = `${trackRect.left - timelineRect.left + at * trackRect.width}px`;
  };
  const startPlayheadLoop = () => {
    if (playheadLoopRef.current !== null) {
      return;
    }
    const tick = () => {
      updatePlayhead();
      playheadLoopRef.current = requestAnimationFrame(tick);
    };
    playheadLoopRef.current = requestAnimationFrame(tick);
  };
  const stopPlayheadLoop = () => {
    if (playheadLoopRef.current !== null) {
      cancelAnimationFrame(playheadLoopRef.current);
      playheadLoopRef.current = null;
    }
    updatePlayhead();
  };
  useEffect(() => () => {
    if (playheadLoopRef.current !== null) {
      cancelAnimationFrame(playheadLoopRef.current);
    }
  }, []);
  const seekFromPointer = (clientX: number) => {
    const media = previewMediaRef.current;
    const track = laneElementRef.current;
    if (!media || !track) {
      return;
    }
    const rect = track.getBoundingClientRect();
    // Left of the track is the label column — a label click selects, it
    // never seeks.
    if (rect.width <= 0 || timelineExtent <= 0 || clientX < rect.left) {
      return;
    }
    const at = Math.min(Math.max(0, (clientX - rect.left) / rect.width), 1);
    media.currentTime = Math.min(Math.max(0, at * timelineExtent - (preview?.offsetSeconds ?? 0)), media.duration || Number.MAX_VALUE);
    handleMediaTime();
  };

  // The transport (VideoTimelineControls, the video editor's row) drives the
  // preview media; its readout tracks time updates (≈4Hz), while the line
  // stays on the rAF loop.
  const [transportPlaying, setTransportPlaying] = useState(false);
  const [transportMuted, setTransportMuted] = useState(false);
  const [transportTime, setTransportTime] = useState(0);
  const mediaTimelineTime = () => (previewMediaRef.current ? (preview?.offsetSeconds ?? 0) + previewMediaRef.current.currentTime : 0);
  const handleMediaTime = () => {
    updatePlayhead();
    setTransportTime(mediaTimelineTime());
  };
  const toggleTransportPlay = () => {
    const media = previewMediaRef.current;
    if (!media) {
      return;
    }
    if (media.paused) {
      void media.play();
    } else {
      media.pause();
    }
  };
  const toggleTransportMuted = () => {
    const media = previewMediaRef.current;
    if (!media) {
      return;
    }
    media.muted = !media.muted;
    setTransportMuted(media.muted);
  };
  const seekTransport = (time: number) => {
    const media = previewMediaRef.current;
    if (!media) {
      return;
    }
    media.currentTime = Math.min(Math.max(0, time - (preview?.offsetSeconds ?? 0)), media.duration || 0);
    setTransportTime(time);
    updatePlayhead();
  };

  // submitRender previews the WORKING document — the no-forced-save
  // contract; the export path (next milestone) will pin a saved revision.
  const submitRender = async () => {
    setRenderError('');
    const from = parseFloat(rangeFrom);
    const to = parseFloat(rangeTo);
    try {
      const op = await AppBindings.SubmitCompositionRender(main.CompositionRenderRequest.createFrom({
        conversationId: sourceConversationID,
        mode: 'preview',
        doc: toWireDoc(docRef.current),
        ...(Number.isFinite(from) ? {startSeconds: from} : {}),
        ...(Number.isFinite(to) ? {endSeconds: to} : {}),
        ...(soloLaneID ? {soloLaneId: soloLaneID} : {}),
      }));
      setRenderedDoc(normalizeCompositionDoc(docRef.current));
      addRenderOp(op);
    } catch (error) {
      setRenderError(formatEditorError(error));
    }
  };

  const cancelRender = async (operationID: string) => {
    try {
      await CancelCompositionRender(operationID);
    } catch (error) {
      setRenderError(formatEditorError(error));
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

  // ---- Direct timeline manipulation ----
  // One generic pointer drag covers every timeline gesture: a clip's body
  // reorders, its edges trim the source range, a junction badge resizes its
  // dissolve (dragging a cut right CREATES the dissolve), a segment's body
  // slides along its lane, its edges trim. All of them funnel into the
  // model's clamping ops, and all of them coalesce to a single undo entry.

  type DragState =
    | {kind: 'clip-move'; clipID: string; fromIndex: number; targetIndex: number; startClientX: number}
    | {kind: 'clip-trim'; clipID: string; edge: 'in' | 'out'; startClientX: number; startValue: number}
    | {kind: 'junction'; clipID: string; startClientX: number; startSeconds: number; hadTransition: boolean}
    | {kind: 'segment-move'; segmentID: string; grabOffset: number; startClientX: number}
    | {kind: 'segment-trim'; segmentID: string; edge: 'in' | 'out'; startClientX: number; startValue: number};
  const [drag, setDrag] = useState<DragState | null>(null);
  const laneElementRef = useRef<HTMLDivElement | null>(null);
  // WebKit fires a compatibility click after a pointerdown whose default was
  // prevented, and it bubbles to the lane's onClick — which would clobber the
  // selection the press just made (junction/clip/segment press "doesn't
  // select"). Item presses raise this flag; the lane's onClick consumes it.
  const suppressLaneClickRef = useRef(false);

  const pxPerSecond = () => {
    const width = laneElementRef.current?.getBoundingClientRect().width ?? 0;
    return width > 0 && timelineExtent > 0 ? width / timelineExtent : 1;
  };
  const pointerTime = (clientX: number) => {
    const rect = laneElementRef.current?.getBoundingClientRect();
    if (!rect || rect.width <= 0) {
      return 0;
    }
    return Math.max(0, ((clientX - rect.left) / rect.width) * timelineExtent);
  };
  const edgeAt = (element: Element, clientX: number) => {
    const rect = element.getBoundingClientRect();
    const within = clientX - rect.left;
    if (within <= 9) {
      return 'in' as const;
    }
    if (rect.width - within <= 9) {
      return 'out' as const;
    }
    return null;
  };

  const beginClipDrag = (event: React.PointerEvent, clip: CompositionClip, index: number) => {
    if (event.button !== 0 || props.suspended) {
      return;
    }
    event.preventDefault();
    setSelection({kind: 'clip', clipID: clip.id});
    suppressLaneClickRef.current = true;
    const edge = edgeAt(event.currentTarget, event.clientX);
    dragChangedRef.current = false;
    if (edge) {
      setDrag({kind: 'clip-trim', clipID: clip.id, edge, startClientX: event.clientX, startValue: edge === 'in' ? clip.inSeconds : clip.outSeconds});
    } else {
      setDrag({kind: 'clip-move', clipID: clip.id, fromIndex: index, targetIndex: index, startClientX: event.clientX});
    }
  };

  const beginJunctionDrag = (event: React.PointerEvent, clip: CompositionClip) => {
    if (event.button !== 0 || props.suspended) {
      return;
    }
    event.preventDefault();
    setSelection({kind: 'junction', clipID: clip.id});
    suppressLaneClickRef.current = true;
    dragChangedRef.current = false;
    setDrag({kind: 'junction', clipID: clip.id, startClientX: event.clientX, startSeconds: clip.transitionToNext?.seconds ?? 0, hadTransition: !!clip.transitionToNext});
  };

  const beginSegmentDrag = (event: React.PointerEvent, segment: CompositionSegment) => {
    if (event.button !== 0 || props.suspended) {
      return;
    }
    event.preventDefault();
    const laneID = docRef.current.audioLanes.find((lane) => lane.segments.some((candidate) => candidate.id === segment.id))?.id ?? '';
    setSelection({kind: 'segment', laneID, segmentID: segment.id});
    suppressLaneClickRef.current = true;
    const edge = edgeAt(event.currentTarget, event.clientX);
    dragChangedRef.current = false;
    if (edge) {
      setDrag({kind: 'segment-trim', segmentID: segment.id, edge, startClientX: event.clientX, startValue: edge === 'in' ? segment.inSeconds : segment.outSeconds});
    } else {
      const grabOffset = Math.max(0, pointerTime(event.clientX) - segment.startSeconds);
      setDrag({kind: 'segment-move', segmentID: segment.id, grabOffset, startClientX: event.clientX});
    }
  };

  useEffect(() => {
    if (!drag) {
      return;
    }
    const onMove = (event: PointerEvent) => {
      const scale = pxPerSecond();
      const delta = (event.clientX - drag.startClientX) / scale;
      switch (drag.kind) {
        case 'clip-move': {
          const current = docRef.current;
          const index = current.video.findIndex((clip) => clip.id === drag.clipID);
          if (index < 0) {
            return;
          }
          const averageWidth = current.video.length > 1
            ? current.video.reduce((total, clip) => total + clipDurationSeconds(clip), 0) / current.video.length
            : timelineExtent;
          const target = Math.min(Math.max(0, Math.round(drag.fromIndex + (event.clientX - drag.startClientX) / (averageWidth * scale))), current.video.length - 1);
          if (target !== drag.targetIndex && target !== index) {
            applyLive(moveClip(current, drag.clipID, target));
            setDrag({...drag, targetIndex: target});
          }
          return;
        }
        case 'clip-trim': {
          const current = docRef.current;
          const clip = current.video.find((candidate) => candidate.id === drag.clipID);
          if (!clip) {
            return;
          }
          const next = drag.edge === 'in'
            ? setClipRange(current, drag.clipID, drag.startValue + delta, clip.outSeconds)
            : setClipRange(current, drag.clipID, clip.inSeconds, drag.startValue + delta);
          applyLive(next);
          return;
        }
        case 'junction': {
          const current = docRef.current;
          const clip = current.video.find((candidate) => candidate.id === drag.clipID);
          if (!clip) {
            return;
          }
          const requested = Math.max(0, drag.startSeconds + delta);
          if (requested < compositionTransitionMinSeconds) {
            applyLive(setClipTransition(current, drag.clipID, null));
            return;
          }
          const next = setClipTransition(current, drag.clipID, {kind: clip.transitionToNext?.kind, seconds: requested, audioCrossfade: clip.transitionToNext?.audioCrossfade ?? true});
          applyLive(next);
          return;
        }
        case 'segment-move': {
          const current = docRef.current;
          const target = Math.max(0, pointerTime(event.clientX) - drag.grabOffset);
          const next = moveSegment(current, drag.segmentID, target);
          applyLive(next);
          return;
        }
        case 'segment-trim': {
          const current = docRef.current;
          const segment = findSegment(current, drag.segmentID);
          if (!segment) {
            return;
          }
          const next = drag.edge === 'in'
            ? setSegmentRange(current, drag.segmentID, drag.startValue + delta, segment.outSeconds)
            : setSegmentRange(current, drag.segmentID, segment.inSeconds, drag.startValue + delta);
          applyLive(next);
          return;
        }
      }
    };
    const finish = () => setDrag(null);
    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', finish);
    window.addEventListener('pointercancel', finish);
    return () => {
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', finish);
      window.removeEventListener('pointercancel', finish);
    };
  }, [drag, timelineExtent, props.suspended]);

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

  // persistRevision is the one save door both the Save button and
  // save-and-export walk through: it writes the working doc as a new
  // revision, re-baselines on the server's normalization, and returns the
  // revision's artifact id (what an export pins — EX-04).
  const persistRevision = async (): Promise<string> => {
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
    setBaseline(normalizeCompositionDoc(normalized));
    docRef.current = normalized;
    setNameDraft(normalized.name);
    setActiveArtifactID(result.artifactId);
    props.onSavedToConversation?.();
    return result.artifactId;
  };

  const saveDoc = async () => {
    setSaving(true);
    setSaveError('');
    try {
      await persistRevision();
    } catch (error) {
      setSaveError(formatEditorError(error));
    } finally {
      setSaving(false);
    }
  };

  // saveAndExport is the export door: pin a revision first (saving the
  // working doc when anything is unsaved), then render THAT artifact with
  // the house encode — later edits cannot touch what export produces.
  const saveAndExport = async () => {
    setExporting(true);
    setExportError('');
    try {
      let artifactID = activeArtifactID;
      if (dirty || !artifactID) {
        artifactID = await persistRevision();
      }
      const width = parseInt(exportWidth, 10);
      const height = parseInt(exportHeight, 10);
      const fps = parseFloat(exportFPS);
      const crf = parseInt(exportCRF, 10);
      const op = await AppBindings.SubmitCompositionRender(main.CompositionRenderRequest.createFrom({
        conversationId: sourceConversationID,
        mode: 'export',
        artifactId: artifactID,
        includeAudio: exportIncludeAudio,
        ...(exportAdvancedOpen && Number.isFinite(width) && width > 0 ? {width} : {}),
        ...(exportAdvancedOpen && Number.isFinite(height) && height > 0 ? {height} : {}),
        ...(exportAdvancedOpen && Number.isFinite(fps) && fps > 0 ? {fps} : {}),
        ...(exportAdvancedOpen && Number.isFinite(crf) && crf > 0 ? {crf} : {}),
      }));
      addRenderOp(op);
    } catch (error) {
      setExportError(formatEditorError(error));
    } finally {
      setExporting(false);
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
  const junctionClip = selection.kind === 'junction' ? doc.video.find((clip) => clip.id === selection.clipID) : undefined;
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

          <div className="composition-stage">
            {preview ? (
              preview.audioOnly ? (
                <audio key={preview.url} ref={(el) => { previewMediaRef.current = el; }} className="composition-preview composition-preview-audio" src={preview.url} controls
                  onTimeUpdate={handleMediaTime} onSeeked={handleMediaTime} onVolumeChange={() => { if (previewMediaRef.current) setTransportMuted(previewMediaRef.current.muted); }}
                  onPlay={() => { setTransportPlaying(true); startPlayheadLoop(); }} onPause={() => { setTransportPlaying(false); stopPlayheadLoop(); handleMediaTime(); }} onEnded={() => { setTransportPlaying(false); stopPlayheadLoop(); handleMediaTime(); }} />
              ) : (
                <video key={preview.url} ref={(el) => { previewMediaRef.current = el; }} className="composition-preview" src={preview.url} poster={compositionPosterURL(preview.url)} controls
                  onTimeUpdate={handleMediaTime} onSeeked={handleMediaTime} onVolumeChange={() => { if (previewMediaRef.current) setTransportMuted(previewMediaRef.current.muted); }}
                  onPlay={() => { setTransportPlaying(true); startPlayheadLoop(); }} onPause={() => { setTransportPlaying(false); stopPlayheadLoop(); handleMediaTime(); }} onEnded={() => { setTransportPlaying(false); stopPlayheadLoop(); handleMediaTime(); }} />
              )
            ) : (
              <div className="composition-preview-empty">
                No preview yet — render one to watch the assembled sequence. Trims, dissolves, and the audio mix are exactly what export will produce.
              </div>
            )}
            <div className="composition-stage-controls">
              <button
                type="button"
                className="editor-generate"
                disabled={renderDisabled}
                title={missingRefs.size > 0
                  ? 'A source file is missing — the render refuses until it is removed'
                  : problems.length > 0
                    ? 'Fix the timeline problems first'
                    : 'Render the working timeline to a preview file — no save required'}
                onClick={() => void submitRender()}
              >
                {runningRender ? `Rendering… ${Math.round((runningRender.progress ?? 0) * 100)}%` : 'Render preview'}
              </button>
              {runningRender ? (
                <button type="button" onClick={() => void cancelRender(runningRender.id)}>Cancel</button>
              ) : null}
              <label className="composition-field" title="Render only part of the output timeline — leave empty for the whole sequence">
                <span>From (s)</span>
                <input type="number" min={0} step={0.5} value={rangeFrom} placeholder="0" onChange={(event) => setRangeFrom(event.target.value)} />
              </label>
              <label className="composition-field" title="Range end — leave empty to run to the sequence end">
                <span>To (s)</span>
                <input type="number" min={0} step={0.5} value={rangeTo} placeholder={formatCompositionClock(timelineExtent)} onChange={(event) => setRangeTo(event.target.value)} />
              </label>
              {doc.audioLanes.length ? (
                <label className="composition-field" title="Preview with only this lane audible — every other lane and the clips' original audio drop out">
                  <span>Solo lane</span>
                  <select value={soloLaneID} onChange={(event) => setSoloLaneID(event.target.value)}>
                    <option value="">— none —</option>
                    {doc.audioLanes.map((lane) => <option key={lane.id} value={lane.id}>{lane.name}</option>)}
                  </select>
                </label>
              ) : null}
              {previewOutdated ? <span className="composition-warning" title="The timeline changed after this preview was rendered">preview outdated — re-render</span> : null}
              {renderError ? <span className="composition-warning" role="alert">{renderError}</span> : null}
            </div>
          </div>

          <div className="composition-timeline">
            {preview ? (
              <VideoTimelineControls
                playing={transportPlaying}
                muted={transportMuted}
                busy={false}
                currentTime={transportTime}
                duration={timelineExtent}
                stepTargets={layout.starts.length ? [...layout.starts, timelineExtent] : [0, timelineExtent]}
                hopLabel="clip boundary"
                frameSeconds={frame.fps > 0 ? 1 / frame.fps : 1 / 30}
                onTogglePlay={toggleTransportPlay}
                onToggleMuted={toggleTransportMuted}
                onSeek={seekTransport}
              />
            ) : null}
            <div className="composition-ruler" title="Click to move the playhead" onClick={(event) => seekFromPointer(event.clientX)}>
              {Array.from({length: 7}, (_, index) => {
                const at = (timelineExtent * index) / 6;
                return <span key={index} style={{left: pct(at)}}>{formatCompositionClock(at)}</span>;
              })}
            </div>
            {/* The lanes scroll under the pinned ruler — the timeline box
                itself stays bounded by the editor body. */}
            <div className="composition-lane-scroll">
            <div
              className={`composition-lane composition-lane-video${selection.kind === 'doc' ? ' focused' : ''}`}
              onClick={(event) => {
                if (suppressLaneClickRef.current) {
                  suppressLaneClickRef.current = false;
                  return;
                }
                // An empty-lane click moves the playhead there, FCP-style,
                // and clears to the project view.
                seekFromPointer(event.clientX);
                setSelection({kind: 'doc'});
              }}
            >
              {/* Every lane shares the label + track geometry, so clips,
                  segments, the ruler, and the playhead share one time axis. */}
              <span className="composition-lane-label">Video</span>
              <div className="composition-lane-track" ref={laneElementRef}>
              {doc.video.map((clip, index) => {
                const missing = missingRefs.has(clip.ref.artifactId);
                return (
                  <button
                    key={clip.id}
                    type="button"
                    className={`composition-clip${selection.kind === 'clip' && selection.clipID === clip.id ? ' selected' : ''}${missing ? ' missing' : ''}${clip.audio.muted ? ' audio-muted' : ''}${clip.audio.detached ? ' audio-detached' : ''}${drag?.kind === 'clip-move' && drag.clipID === clip.id ? ' dragging' : ''}`}
                    style={{left: pct(layout.starts[index]), width: pct(clipDurationSeconds(clip))}}
                    title={`${clip.ref.artifactId} · ${formatCompositionClock(clipDurationSeconds(clip))}${missing ? ' · source file missing' : ''}\nDrag to reorder · drag an edge to trim · select to edit`}
                    onPointerDown={(event) => beginClipDrag(event, clip, index)}
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
                    className={`composition-junction${transition ? ' dissolve' : ' cut'}${selection.kind === 'junction' && selection.clipID === clip.id ? ' selected' : ''}`}
                    style={{left: pct(junctionAt), width: transition ? pct(transition.seconds) : '3px'}}
                    title={transition
                      ? `${transition.kind} ${transition.seconds.toFixed(1)}s (audio ${transition.audioCrossfade ? 'crossfades' : 'cuts'}) — drag to resize, drag fully left for a cut`
                      : 'Cut — drag right to create a transition, or pick one in the inspector'}
                    onPointerDown={(event) => beginJunctionDrag(event, clip)}
                  >
                    {transition ? `${transition.seconds.toFixed(1)}s` : ''}
                  </button>
                );
              })}
              {doc.video.length === 0 ? <span className="composition-lane-empty">No clips — add media from the picker.</span> : null}
              </div>
            </div>
            {doc.audioLanes.map((lane) => (
              <div
                key={lane.id}
                className={`composition-lane composition-lane-audio${selection.kind === 'lane' && selection.laneID === lane.id ? ' selected' : ''}${lane.muted ? ' muted' : ''}`}
                onClick={(event) => {
                  if (suppressLaneClickRef.current) {
                    suppressLaneClickRef.current = false;
                    return;
                  }
                  seekFromPointer(event.clientX);
                  setSelection({kind: 'lane', laneID: lane.id});
                }}
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
                      title={`${segment.ref.artifactId} · ${formatCompositionClock(segmentDurationSeconds(segment))}${segment.ducks.length ? ` · ${segment.ducks.length} duck${segment.ducks.length === 1 ? '' : 's'}` : ''}\nDrag to move · drag an edge to trim · select to edit`}
                      onPointerDown={(event) => beginSegmentDrag(event, segment)}
                    >
                      {segment.ref.artifactId.slice(0, 10)}
                    </button>
                  ))}
                </div>
              </div>
            ))}
            </div>
            {/* The playhead rides the preview player's position (left set
                imperatively); pointer-events none so it never blocks the
                lanes beneath it. */}
            {preview ? <div className="composition-playhead" ref={playheadRef} aria-hidden="true" /> : null}
          </div>
        </div>

        <aside className="editor-inspector composition-inspector">
          {selection.kind === 'junction' && junctionClip ? (
            <div className="composition-inspect-junction">
              <strong>Transition</strong>
              <p className="hint composition-ref-line" title={junctionClip.ref.path}>
                {junctionClip.ref.artifactId.slice(0, 12)} → the next clip
              </p>
              <div className="composition-transition">
                <TransitionControls doc={doc} clipID={junctionClip.id} commit={commit} />
              </div>
              <div className="editor-tool-group">
                <button
                  type="button"
                  aria-label="Remove transition"
                  title="Remove transition — the clips join with a straight cut"
                  disabled={!junctionClip.transitionToNext}
                  onClick={() => commit(setClipTransition(docRef.current, junctionClip.id, null))}
                >
                  <TrashIcon />
                </button>
              </div>
            </div>
          ) : selection.kind === 'doc' || !selectedClip ? (
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
                {/* The fields show the frame the project will actually render
                    — the doc stores 0 for "inherit the first clip", so the
                    resolved value fills the box until an edit pins it. */}
                <NumberField label="Width" value={frame.width} step={2} min={0} title="Overrides the first clip's width — 0 restores the inheritance" onCommit={(value) => commit(setCompositionOutput(docRef.current, {width: value}))} />
                <NumberField label="Height" value={frame.height} step={2} min={0} title="Overrides the first clip's height — 0 restores the inheritance" onCommit={(value) => commit(setCompositionOutput(docRef.current, {height: value}))} />
                <NumberField label="FPS" value={frame.fps} step={1} min={0} max={60} title="Overrides the first clip's frame rate — 0 restores the inheritance" onCommit={(value) => commit(setCompositionOutput(docRef.current, {fps: value}))} />
              </div>
              <p className="hint">Output frame: {frame.width}×{frame.height} @ {Math.round(frame.fps)}fps · {doc.output.fit}</p>
              <div className="composition-problems">
                <strong>{problems.length === 0 ? 'No problems' : `${problems.length} problem${problems.length === 1 ? '' : 's'}`}</strong>
                {problems.length ? (
                  <ul>{problems.map((problem, index) => <li key={index}>{problem}</li>)}</ul>
                ) : (
                  <p className="hint">Select a clip, lane, or segment to edit it.</p>
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
                {/* Arrows only — the lane runs left to right, so direction reads
                    without words (the hover title carries them). */}
                <button type="button" aria-label="Move earlier" title="Move earlier" disabled={doc.video.findIndex((clip) => clip.id === selectedClip.id) === 0} onClick={() => commit(moveClip(docRef.current, selectedClip.id, doc.video.findIndex((clip) => clip.id === selectedClip.id) - 1))}>←</button>
                <button type="button" aria-label="Move later" title="Move later" disabled={doc.video.findIndex((clip) => clip.id === selectedClip.id) === doc.video.length - 1} onClick={() => commit(moveClip(docRef.current, selectedClip.id, doc.video.findIndex((clip) => clip.id === selectedClip.id) + 2))}>→</button>
                <button type="button" aria-label="Duplicate clip" title="Duplicate clip" onClick={() => commit(duplicateClip(docRef.current, selectedClip.id))}><DuplicateIcon /></button>
                <button type="button" aria-label="Remove clip" title="Remove clip" onClick={() => {commit(removeClip(docRef.current, selectedClip.id)); setSelection({kind: 'doc'});}}><TrashIcon /></button>
              </div>

              <div className="composition-transition">
                <strong>Transition to next</strong>
                <TransitionControls doc={doc} clipID={selectedClip.id} commit={commit} />
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
                <button type="button" aria-label="Duplicate segment" title="Duplicate segment" onClick={() => commit(duplicateSegment(docRef.current, selectedSegment.id))}><DuplicateIcon /></button>
                <button type="button" aria-label="Remove segment" title="Remove segment" onClick={() => {commit(removeSegment(docRef.current, selectedSegment.id)); setSelection({kind: 'lane', laneID: selectedSegmentLane.id});}}><TrashIcon /></button>
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

          <div className="composition-export-block">
            <strong>Export</strong>
            {/* EX-03's pre-render summary: what the file will be. */}
            <p className="hint">
              {frame.width}×{frame.height} @ {Math.round(frame.fps)}fps · {formatCompositionClock(sequenceDuration)}
              {exportIncludeAudio ? ' · audio included' : ' · no audio'}
              {exportIncludeAudio && overhang > 0 ? ` · ${overhang.toFixed(1)}s audio tail trims at the video end` : ''}
            </p>
            <label className="composition-toggle" title="The mix rides the video (original audio, lanes, ducks). Turn it off for a silent file.">
              <input type="checkbox" checked={exportIncludeAudio} onChange={(event) => setExportIncludeAudio(event.target.checked)} />
              include audio
            </label>
            <button
              type="button"
              className="editor-generate"
              disabled={exporting || renderDisabled}
              title={dirty || !activeArtifactID
                ? 'Save the working timeline as a revision, then export that revision — later edits cannot change the file'
                : 'Export the pinned revision into the conversation as a new video artifact'}
              onClick={() => void saveAndExport()}
            >
              {exporting ? 'Exporting…' : dirty || !activeArtifactID ? 'Save & export' : 'Export revision'}
            </button>
            {exportError ? <div className="editor-error">{exportError}</div> : null}
            <button type="button" className="composition-advanced-toggle" onClick={() => setExportAdvancedOpen((open) => !open)}>
              {exportAdvancedOpen ? '− Advanced' : '+ Advanced'}
            </button>
            {exportAdvancedOpen ? (
              <div className="composition-field-row">
                <NumberField label="Width" value={exportWidth === '' ? frame.width : parseFloat(exportWidth)} step={2} min={0} title="Overrides the saved width — 0 restores it" onCommit={(value) => setExportWidth(value > 0 ? String(value) : '')} />
                <NumberField label="Height" value={exportHeight === '' ? frame.height : parseFloat(exportHeight)} step={2} min={0} title="Overrides the saved height — 0 restores it" onCommit={(value) => setExportHeight(value > 0 ? String(value) : '')} />
                <NumberField label="FPS" value={exportFPS === '' ? frame.fps : parseFloat(exportFPS)} step={1} min={0} max={60} title="Overrides the saved frame rate — 0 restores it" onCommit={(value) => setExportFPS(value > 0 ? String(value) : '')} />
                <label className="composition-field" title="Lower is better quality (H.264 CRF); 18 is the house default">
                  <span>Quality</span>
                  <select value={exportCRF} onChange={(event) => setExportCRF(event.target.value)}>
                    <option value="18">High (18)</option>
                    <option value="23">Medium (23)</option>
                    <option value="28">Compact (28)</option>
                  </select>
                </label>
              </div>
            ) : null}
            <span className="editor-session-note">Export always renders a saved revision and lands in the conversation as a new artifact (downloadable below).</span>
          </div>
        </aside>
      </div>

      <EditsPanel count={log.length + renders.length} emptyText="Nothing rendered or saved yet." bodyReservePx={editsPanelBodyReservePx}>
        <ul className="editor-op-list">
          {renders.map((op) => (
            <li key={op.id} className={`editor-op editor-op-${op.status}`}>
              <span className="editor-op-thumb-glyph">{op.status === 'completed' ? '▶' : op.status === 'failed' ? '✕' : op.status === 'cancelled' ? '–' : '⋯'}</span>
              <div className="editor-op-meta">
                <span className="editor-op-title">
                  {op.mode === 'export' ? 'Export' : 'Preview'} · {op.audioOnly ? 'audio only' : `${op.width || '?'}×${op.height || '?'}`} · {formatCompositionClock(op.outputSeconds || 0)}
                  {op.rangeStartSeconds !== undefined && op.rangeEndSeconds !== undefined
                    ? ` · ${formatCompositionClock(op.rangeStartSeconds)}–${formatCompositionClock(op.rangeEndSeconds)}`
                    : ''}
                </span>
                <span className="editor-op-sub" title={op.error || undefined}>
                  {op.status === 'running' || op.status === 'queued'
                    ? `${Math.round((op.progress || 0) * 100)}%`
                    : op.error || asArray(op.notices).join(' · ') || 'rendered for this session'}
                </span>
              </div>
              <div className="editor-op-actions">
                {op.status === 'running' || op.status === 'queued' ? (
                  <button type="button" onClick={() => void cancelRender(op.id)}>Cancel</button>
                ) : null}
                {op.status === 'completed' && op.resultUrl && op.mode === 'export' ? (
                  <button
                    type="button"
                    title="Save the exported file to disk"
                    onClick={() => {
                      void SaveVideo(main.SaveVideoRequest.createFrom({
                        path: op.resultUrl,
                        suggestedName: `${doc.name.replace(/\s+/g, '-').toLowerCase() || 'composition'}-${(op.exportArtifactId || op.id).slice(0, 10)}`,
                      })).catch((error) => setRenderError(formatEditorError(error)));
                    }}
                  >
                    Download
                  </button>
                ) : null}
                {op.status === 'completed' && op.resultUrl && op.mode !== 'export' ? (
                  <button
                    type="button"
                    title="Load this render onto the stage"
                    onClick={() => {
                      const url = op.resultUrl;
                      if (url) {
                        setPreview({url, audioOnly: op.audioOnly, offsetSeconds: op.rangeStartSeconds ?? 0});
                      }
                    }}
                  >
                    Play
                  </button>
                ) : null}
              </div>
            </li>
          ))}
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
