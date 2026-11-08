import {useEffect, useMemo, useRef, useState} from 'react';
import * as AppBindings from '../../wailsjs/go/main/App';
import {CancelCompositionRender, SaveComposition, SaveVideo} from '../../wailsjs/go/main/App';
import {main} from '../../wailsjs/go/models';
import {EventsOn} from '../../wailsjs/runtime/runtime';
import {notifyError, notifySuccess, trackSave} from '../notifications';
import {AudioPeaksEntry, loadAudioPeaks, sliceAudioPeaks, waveformPath} from './audioPeaks';
import {EditsPanel} from './EditsPanel';
import {EditorHeader} from './EditorHeader';
import {MediaPicker} from './MediaPicker';
import {VideoTimelineControls} from './VideoTimelineControls';
import {
  CompositionClip,
  CompositionDoc,
  CompositionFit,
  CompositionMediaRef,
  CompositionPreviewFreshness,
  CompositionSegment,
  addClip,
  addDuck,
  addLane,
  addSegmentToLane,
  audioOverhangSeconds,
  clipDurationSeconds,
  compositionAudioExtent,
  compositionClipLayout,
  compositionIsAudioOnly,
  compositionMaxAudioLanes,
  compositionPreviewFreshness,
  compositionPreviewKey,
  compositionPreviewTakesStage,
  compositionAutoPreviewAction,
  compositionProblems,
  compositionSequenceDuration,
  compositionStillMaxSeconds,
  compositionStillMinSeconds,
  compositionTransitions,
  compositionTransitionMinSeconds,
  detachClipAudio,
  duplicateClip,
  duplicateSegment,
  formatCompositionClock,
  isStillClip,
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
  setStillDuration,
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
  // The ＋ Audio action's opening intent: an empty draft presents audio-first
  // (one starter lane, no video-frame chrome) instead of waiting for content
  // to decide. Once any video clip exists — or on any saved revision — the
  // content rule (compositionIsAudioOnly) takes over and this is moot.
  audioFirst?: boolean;
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

// StagePreview is the completed render on the stage. key is the fingerprint
// it was built from — the op's echo of the request's previewKey — which is
// what the stage compares against the timeline's key as it is now.
type StagePreview = {opId: string; url: string; audioOnly: boolean; offsetSeconds: number; key: string};

function stagePreviewFromOp(op: main.CompositionRenderOperation): StagePreview {
  return {opId: op.id, url: op.resultUrl ?? '', audioOnly: op.audioOnly, offsetSeconds: op.rangeStartSeconds ?? 0, key: op.previewKey ?? ''};
}

// Auto-update re-renders the preview on its own once the timeline settles:
// off by default (every preview re-encodes the whole timeline at full output
// size, so a long sequence would keep ffmpeg busy for most of a session), and
// remembered once turned on — the EditsPanel-height localStorage idiom.
const autoPreviewStorageKey = 'atelier.compositionAutoPreview';
const autoPreviewDelayMs = 1000;
// Restaging a listed render of the timeline (an undo back to a rendered
// state) costs nothing, so it only waits out rapid-fire edits.
const autoRestageDelayMs = 200;

function storedAutoPreview(): boolean {
  try {
    return window.localStorage.getItem(autoPreviewStorageKey) === '1';
  } catch {
    return false;
  }
}

function renderInFlight(op: main.CompositionRenderOperation | null | undefined): boolean {
  return op?.status === 'queued' || op?.status === 'running';
}

// previewRequestOptions reads the stage controls as a render request does:
// an unparsable range field means "unbounded", and the solo lane arrives
// already resolved against the lanes that still exist.
function previewRequestOptions(rangeFrom: string, rangeTo: string, soloLaneID: string): {startSeconds: number | null; endSeconds: number | null; soloLaneId: string} {
  const from = parseFloat(rangeFrom);
  const to = parseFloat(rangeTo);
  return {startSeconds: Number.isFinite(from) ? from : null, endSeconds: Number.isFinite(to) ? to : null, soloLaneId: soloLaneID};
}

// stalePreviewText is the stage badge's line for a preview that no longer
// matches the timeline.
function stalePreviewText(freshness: CompositionPreviewFreshness, renderingLatest: boolean, autoUpdating: boolean): string {
  if (renderingLatest) return 'Updating — rendering the latest changes…';
  if (autoUpdating) return 'Outdated — auto-update re-renders it in a moment';
  if (freshness === 'settings') return 'Outdated — the range or solo lane changed after this render';
  if (freshness === 'unknown') return "Can't confirm this preview matches the timeline";
  return 'Outdated — the timeline changed after this render';
}

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

// compositionRenderSummary is a render row's status line. Only a preview is
// ephemeral (its file is swept when the app restarts); a finished export was
// adopted into the conversation, and a cancelled render produced nothing — a
// replaced one says so, since nobody pressed Cancel.
function compositionRenderSummary(op: main.CompositionRenderOperation): string {
  switch (op.status) {
    case 'queued':
    case 'running':
      return `${Math.round((op.progress || 0) * 100)}%`;
    case 'failed':
      return op.error || 'failed';
    case 'cancelled':
      return op.supersededBy ? 'replaced by a newer render' : 'cancelled';
    default: {
      const outcome = op.mode !== 'export' ? 'rendered for this session' : op.exportArtifactId ? 'saved to the conversation' : 'export finished';
      return [outcome, ...asArray(op.notices)].join(' · ');
    }
  }
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
            {/* alphabetical for scanning; the model's array keeps the Go mirror's order */}
            {[...compositionTransitions].sort().map((name) => <option key={name} value={name}>{name}</option>)}
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

// segmentWaveformBars is each segment waveform's bar count — the SVG
// stretches (preserveAspectRatio="none") to whatever width the segment
// renders at, so one fixed resolution serves every zoom level.
const segmentWaveformBars = 140;

// initialCompositionDoc adapts the wire source. An audio-first draft (＋
// Audio) with nothing measured yet starts on one empty lane so the timeline
// reads as an audio project from the first frame; doc and baseline share the
// same instance, so the starter lane is scaffolding, not a pending edit (and
// an empty-lane save is valid — validateCompositionDoc normalizes it).
function initialCompositionDoc(source: main.CompositionSourceInfo, audioFirst?: boolean): CompositionDoc {
  const doc = normalizeCompositionDoc(source.doc);
  return audioFirst && doc.video.length === 0 && doc.audioLanes.length === 0 ? addLane(doc) : doc;
}

// compositionRefURL serves a ref's file through the asset handler — the
// /atelier-artifact + absolute-path convention every editor hydrates by.
function compositionRefURL(ref: CompositionMediaRef): string {
  return ref.path && ref.path.startsWith('/') ? `/atelier-artifact${ref.path}` : '';
}

// useAudioPeaksEntry resolves one ref's decoded waveform peaks through
// audioPeaks.ts's per-URL cache. Null while loading or when the file cannot
// be fetched/decoded — the segment then renders plain, as before.
function useAudioPeaksEntry(ref: CompositionMediaRef): AudioPeaksEntry | null {
  const url = compositionRefURL(ref);
  const [entry, setEntry] = useState<AudioPeaksEntry | null>(null);
  useEffect(() => {
    let cancelled = false;
    setEntry(null);
    if (!url) return;
    void loadAudioPeaks(url).then((result) => {
      if (!cancelled && result) setEntry(result);
    });
    return () => {
      cancelled = true;
    };
  }, [url]);
  return entry;
}

// SegmentWaveform draws the segment's source span as mirrored peak bars —
// the audio sees itself. Pure display: every drag/trims interaction stays on
// the button wrapping it.
function SegmentWaveform(props: {segment: CompositionSegment}) {
  const entry = useAudioPeaksEntry(props.segment.ref);
  if (!entry) return null;
  const bars = sliceAudioPeaks(entry.peaks, entry.durationSeconds, props.segment.inSeconds, props.segment.outSeconds, segmentWaveformBars);
  return (
    <svg className="composition-segment-waveform" viewBox={`0 0 ${segmentWaveformBars} 100`} preserveAspectRatio="none" aria-hidden="true">
      <path d={waveformPath(bars)} vectorEffect="non-scaling-stroke" />
    </svg>
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

  const [doc, setDoc] = useState<CompositionDoc>(() => initialCompositionDoc(source, props.handle.audioFirst));
  const [baseline, setBaseline] = useState<CompositionDoc>(() => initialCompositionDoc(source, props.handle.audioFirst));
  const [undoStack, setUndoStack] = useState<CompositionDoc[]>([]);
  const [redoStack, setRedoStack] = useState<CompositionDoc[]>([]);
  const [selection, setSelection] = useState<Selection>({kind: 'doc'});
  const [nameDraft, setNameDraft] = useState(() => initialCompositionDoc(source, props.handle.audioFirst).name);

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

  // Render ops this session, newest first — every transition arrives as an
  // atelier:composition-op event and folds into the row (terminal states
  // never overwrite a later one because the op id keys the merge).
  const [renders, setRenders] = useState<main.CompositionRenderOperation[]>([]);
  // The stage plays the chosen completed render, carrying the key it was
  // built from, so edits after a render light the Render button. previewRef
  // mirrors it synchronously for the app-global event handler.
  const [preview, setPreview] = useState<StagePreview | null>(null);
  const previewRef = useRef<StagePreview | null>(null);
  const [rangeFrom, setRangeFrom] = useState('');
  const [rangeTo, setRangeTo] = useState('');
  const [soloLaneID, setSoloLaneID] = useState('');

  // Auto-update. Its renders share ONE Edits row — autoRender, the newest —
  // instead of one row per edit: the first sight of a newer auto op retires
  // the previous one (Go cancels it as superseded), and a retired op's late
  // events change nothing.
  const [autoPreview, setAutoPreview] = useState(storedAutoPreview);
  const [autoRender, setAutoRender] = useState<main.CompositionRenderOperation | null>(null);
  const autoRenderRef = useRef<main.CompositionRenderOperation | null>(null);
  const retiredAutoIDsRef = useRef(new Set<string>());
  // The key auto-update gave up on: a preview of it failed or was cancelled
  // by hand, or the submit was refused. It never retries that timeline on
  // its own (a broken ffmpeg would loop); the next edit or the button does.
  const [autoSkipKey, setAutoSkipKey] = useState('');
  // Rows sort by first sight, newest first — the auto row lands among the
  // manual renders and exports where it arrived.
  const arrivalRef = useRef(new Map<string, number>());
  const noteArrival = (id: string) => {
    if (!arrivalRef.current.has(id)) arrivalRef.current.set(id, arrivalRef.current.size + 1);
  };
  // Completed auto renders' files. Once a newer render has replaced one in
  // the auto row and on the stage, nothing can reach it anymore, so it is
  // discarded rather than left for the launch sweep.
  const autoFilesRef = useRef(new Set<string>());
  // Where the viewer was when a newer render replaced the staged one — the
  // replacement resumes there, playing if it was.
  const resumeRef = useRef<{time: number; playing: boolean} | null>(null);
  // The timeline's key when the viewer played a row by hand: auto-update
  // leaves that choice on the stage until the next edit moves the key.
  const [stagePinnedKey, setStagePinnedKey] = useState<string | null>(null);
  // The timeline's current preview key, mirrored for the app-global event
  // handler (kept in step where the key is computed below).
  const currentPreviewKeyRef = useRef('');

  // Export choices (AU-11's audio toggle; the advanced overrides are
  // per-export — empty inherits the revision's own output settings).
  const [exportIncludeAudio, setExportIncludeAudio] = useState(true);
  const [exportAdvancedOpen, setExportAdvancedOpen] = useState(false);
  const [exportWidth, setExportWidth] = useState('');
  const [exportHeight, setExportHeight] = useState('');
  const [exportFPS, setExportFPS] = useState('');
  const [exportCRF, setExportCRF] = useState('18');
  const [exporting, setExporting] = useState(false);

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
  // The presentation flag: content decides (the renderer's plan.AudioOnly
  // rule) once anything is on the timeline; a brand-new empty draft falls
  // back to how it was opened, so a fresh video project still presents as
  // one. Adding a video clip flips an audio project into a video one.
  const audioOnly = compositionIsAudioOnly(doc) && (doc.audioLanes.length > 0 || props.handle.audioFirst === true);
  const refCounts = useMemo(() => {
    const counts: Record<string, number> = {};
    for (const clip of doc.video) counts[clip.ref.artifactId] = (counts[clip.ref.artifactId] ?? 0) + 1;
    for (const lane of doc.audioLanes) {
      for (const segment of lane.segments) counts[segment.ref.artifactId] = (counts[segment.ref.artifactId] ?? 0) + 1;
    }
    return counts;
  }, [doc]);
  const stillCount = useMemo(() => doc.video.reduce((count, clip) => count + (isStillClip(clip) ? 1 : 0), 0), [doc]);

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

  // stageRender puts a completed render on the stage. A render replacing one
  // already staged (not a replay of the same file) keeps the viewer's place.
  const stageRender = (op: main.CompositionRenderOperation, resume: boolean) => {
    const next = stagePreviewFromOp(op);
    const staged = previewRef.current;
    const media = previewMediaRef.current;
    resumeRef.current = resume && staged && media && staged.url !== next.url
      ? {time: staged.offsetSeconds + media.currentTime, playing: !media.paused}
      : null;
    previewRef.current = next;
    setPreview(next);
  };

  // applyRenderOp folds one op state into the rows: an auto op into the
  // shared auto row, anything else into its own. A completed preview takes
  // the stage unless the stage already shows the timeline and the newcomer
  // doesn't (an undo landed while it rendered — its row still plays it).
  // Refs and state setters only, so the app-global listener — registered
  // once — always acts on the current state.
  const applyRenderOp = (op: main.CompositionRenderOperation) => {
    if (op.auto && retiredAutoIDsRef.current.has(op.id)) {
      return;
    }
    noteArrival(op.id);
    if (op.auto) {
      const previous = autoRenderRef.current;
      if (previous && previous.id !== op.id) {
        retiredAutoIDsRef.current.add(previous.id);
      }
      if (op.status === 'completed' && op.resultUrl) {
        autoFilesRef.current.add(op.id);
      }
      autoRenderRef.current = op;
      setAutoRender(op);
    } else {
      setRenders((current) => {
        const index = current.findIndex((candidate) => candidate.id === op.id);
        if (index < 0) {
          return [op, ...current];
        }
        const next = [...current];
        next[index] = op;
        return next;
      });
    }
    if (op.mode !== 'export' && (op.status === 'failed' || (op.status === 'cancelled' && !op.supersededBy))) {
      setAutoSkipKey(op.previewKey ?? '');
    }
    if (op.status === 'completed' && op.resultUrl && op.mode !== 'export' &&
      compositionPreviewTakesStage(previewRef.current?.key ?? null, op.previewKey ?? '', currentPreviewKeyRef.current)) {
      stageRender(op, true);
    }
  };

  // Render-op events fold into the rows, and a completed preview takes the
  // stage. The event is app-global, so another conversation's op — a render
  // still running after its editor closed — is ignored. A completed EXPORT's
  // refresh and toast belong to App's listener, which hears it whether or not
  // this editor is still open.
  useEffect(() => {
    const onUpdate = (event: any) => {
      const op = main.CompositionRenderOperation.createFrom(event);
      if (op.conversationId !== sourceConversationID) {
        return;
      }
      applyRenderOp(op);
    };
    // The canceller, not EventsOff(name): that drops every listener for the
    // event, including App's own.
    return EventsOn('atelier:composition-op', onUpdate);
  }, []);

  // A render keeps running after its editor closes, so a reopened composer
  // seeds its rows from the conversation's registered render: the job is
  // visible and cancellable again instead of waiting for its next progress
  // tick (meanwhile a new render was refused as "already running" with
  // nothing on screen to cancel). A row an event already delivered wins — the
  // event is at least as new as this snapshot — and a snapshot never
  // displaces an auto row this editor already started. Best-effort: when the
  // list fails, the next event brings the row back.
  useEffect(() => {
    let stale = false;
    void AppBindings.ListCompositionRenders(sourceConversationID).then((ops) => {
      const seeded = asArray(ops).map((op) => main.CompositionRenderOperation.createFrom(op));
      if (stale || seeded.length === 0) {
        return;
      }
      for (const op of seeded.filter((candidate) => candidate.auto)) {
        if (!autoRenderRef.current && !retiredAutoIDsRef.current.has(op.id)) {
          applyRenderOp(op);
        }
      }
      const manual = seeded.filter((candidate) => !candidate.auto);
      manual.forEach((op) => noteArrival(op.id));
      setRenders((current) => [...manual.filter((op) => !current.some((candidate) => candidate.id === op.id)), ...current]);
    }).catch(() => {});
    return () => {
      stale = true;
    };
  }, [sourceConversationID]);

  // The Edits panel's render rows, newest first: the manual renders plus the
  // one auto row. A replaced render is still winding down for a moment, so
  // the running render is the newest one in flight.
  const renderRows = useMemo(() => {
    const arrival = (op: main.CompositionRenderOperation) => arrivalRef.current.get(op.id) ?? 0;
    return [...(autoRender ? [autoRender] : []), ...renders].sort((a, b) => arrival(b) - arrival(a));
  }, [autoRender, renders]);
  const runningRender = renderRows.find(renderInFlight) ?? null;
  // SubmitCompositionRender emits the queued op as an event BEFORE its
  // promise resolves, so the event handler usually inserts the row first —
  // the submit-side insert must not prepend a second copy of the same id
  // (the twin would freeze at 0%: events dedupe to the first match only),
  // nor replay a queued auto op the events already moved past.
  const addRenderOp = (op: main.CompositionRenderOperation) => {
    if (op.auto) {
      if (autoRenderRef.current?.id !== op.id) {
        applyRenderOp(op);
      }
      return;
    }
    noteArrival(op.id);
    setRenders((current) => current.some((candidate) => candidate.id === op.id) ? current : [op, ...current]);
  };
  // The timeline itself can't render — no click gets past these.
  const renderBlocked = problems.length > 0 || missingRefs.size > 0 ||
    (doc.video.length === 0 && doc.audioLanes.every((lane) => lane.segments.length === 0));

  // A soloed lane that was since removed solos nothing (the renderer would
  // mute every lane), so a stale id reads as "none" — in the select, the
  // request, and the key alike.
  const effectiveSoloLaneID = doc.audioLanes.some((lane) => lane.id === soloLaneID) ? soloLaneID : '';
  // Freshness: the key of what the controls would render now, against the
  // key the staged render was built from. Names never reach the renderer, so
  // renaming the composition or a lane leaves the preview current.
  const currentPreviewKey = useMemo(
    () => compositionPreviewKey(doc, previewRequestOptions(rangeFrom, rangeTo, effectiveSoloLaneID)),
    [doc, rangeFrom, rangeTo, effectiveSoloLaneID],
  );
  useEffect(() => {
    currentPreviewKeyRef.current = currentPreviewKey;
    // A render played by hand holds the stage only until the next edit —
    // an undo back to that timeline later is not the same choice.
    setStagePinnedKey((pinned) => (pinned !== null && pinned !== currentPreviewKey ? null : pinned));
  }, [currentPreviewKey]);
  const stageFreshness: CompositionPreviewFreshness | null = preview ? compositionPreviewFreshness(preview.key, currentPreviewKey) : null;
  const stageStale = stageFreshness !== null && stageFreshness !== 'current';
  // A running preview of exactly this timeline is the fix already underway;
  // one of an older timeline can be restarted with the edits (Replace).
  const renderingLatest = !!runningRender && runningRender.mode !== 'export' && runningRender.previewKey === currentPreviewKey;
  const restartable = !!runningRender && runningRender.mode !== 'export' && !renderingLatest && !renderBlocked;
  // The Render button doubles as the stage's freshness light: primary with
  // nothing staged, lit when the staged render no longer matches the
  // timeline, quiet when it does. A running preview of an older timeline
  // turns it into "Restart with changes"; an export holds it until done.
  const renderButton = ((): {label: string; className: string; title: string; disabled: boolean} => {
    if (runningRender && !restartable) {
      const percent = Math.round((runningRender.progress ?? 0) * 100);
      if (runningRender.mode === 'export') {
        return {label: `Exporting… ${percent}%`, className: 'editor-generate', title: 'An export is rendering — previews wait until it finishes', disabled: true};
      }
      return {
        label: `Rendering… ${percent}%`,
        className: 'editor-generate',
        title: renderingLatest
          ? 'Rendering this timeline — the stage switches to it when it lands'
          : "Rendering an earlier version of the timeline — the current one can't render until it's fixed",
        disabled: true,
      };
    }
    if (restartable) {
      return {
        label: 'Restart with changes',
        className: 'editor-generate composition-render-stale',
        title: 'The running render predates your latest edits — replace it with a render of the timeline as it is now',
        disabled: false,
      };
    }
    const label = stageStale ? 'Re-render preview' : 'Render preview';
    const className = stageFreshness === 'current' ? 'composition-render-current' : `editor-generate${stageStale ? ' composition-render-stale' : ''}`;
    if (missingRefs.size > 0) return {label, className, title: 'A source file is missing — the render refuses until it is removed', disabled: true};
    if (renderBlocked) return {label, className, title: problems.length > 0 ? 'Fix the timeline problems first' : 'Add clips or audio to render a preview', disabled: true};
    switch (stageFreshness) {
      case 'current':
        return {label, className, title: 'The preview already shows this timeline — render it again anyway', disabled: false};
      case 'settings':
        return {label, className, title: 'The range or solo lane changed after this preview — render it again', disabled: false};
      case 'timeline':
      case 'unknown':
        return {label, className, title: 'The timeline changed after this preview was rendered — render it again', disabled: false};
      default:
        return {label, className, title: 'Render the working timeline to a preview file — no save required', disabled: false};
    }
  })();

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

  // The Edits row's Play. A completed preview already took the stage, so
  // setting the same preview again re-renders the same keyed element and
  // starts nothing; and a different render remounts the element, so play()
  // must wait for the commit. The flag carries the intent across either.
  const playOnStageRef = useRef(false);
  const playRenderOnStage = (op: main.CompositionRenderOperation) => {
    if (!op.resultUrl) {
      return;
    }
    playOnStageRef.current = true;
    setStagePinnedKey(currentPreviewKeyRef.current);
    stageRender(op, false);
  };
  useEffect(() => {
    if (!playOnStageRef.current) {
      return;
    }
    playOnStageRef.current = false;
    void previewMediaRef.current?.play().catch(() => {});
  }, [preview]);
  // A render that replaced the staged one resumes where the viewer was, once
  // the new file's metadata says how long it is.
  const resumeStagedMedia = () => {
    const media = previewMediaRef.current;
    const resume = resumeRef.current;
    resumeRef.current = null;
    if (!media || !resume) {
      return;
    }
    const at = resume.time - (previewRef.current?.offsetSeconds ?? 0);
    if (at > 0 && at < (media.duration || 0)) {
      media.currentTime = at;
    }
    if (resume.playing) {
      void media.play().catch(() => {});
    }
    handleMediaTime();
  };

  // Every preview submit ticks this, so a pending auto-update timer can tell
  // that a click already rendered.
  const submitCountRef = useRef(0);
  // submitRender previews the WORKING document — the no-forced-save
  // contract; the export path pins a saved revision. The request carries the
  // key of what it asks to see, and the op echoes it back to the stage. It
  // always replaces a running preview (the button only offers that when the
  // running one is outdated); with auto-update on, every preview is an auto
  // render sharing the auto row, whoever started it. A refused submit is a
  // key auto-update won't retry on its own.
  const submitRender = async (fromAutoUpdate = false) => {
    const options = previewRequestOptions(rangeFrom, rangeTo, effectiveSoloLaneID);
    const key = compositionPreviewKey(docRef.current, options);
    const auto = autoPreview;
    submitCountRef.current += 1;
    try {
      const op = await AppBindings.SubmitCompositionRender(main.CompositionRenderRequest.createFrom({
        conversationId: sourceConversationID,
        mode: 'preview',
        doc: toWireDoc(docRef.current),
        ...(options.startSeconds !== null ? {startSeconds: options.startSeconds} : {}),
        ...(options.endSeconds !== null ? {endSeconds: options.endSeconds} : {}),
        ...(options.soloLaneId ? {soloLaneId: options.soloLaneId} : {}),
        previewKey: key,
        replace: true,
        ...(auto ? {auto: true} : {}),
      }));
      addRenderOp(op);
    } catch (error) {
      setAutoSkipKey(key);
      if (fromAutoUpdate) {
        notifyError("Auto-update couldn't start the preview", error, {key: 'composition-auto-update'});
      } else {
        notifyError("Couldn't start the preview", error);
      }
    }
  };

  const toggleAutoPreview = (enabled: boolean) => {
    setAutoPreview(enabled);
    if (enabled) {
      setAutoSkipKey('');
    }
    try {
      window.localStorage.setItem(autoPreviewStorageKey, enabled ? '1' : '0');
    } catch {
      // Storage refused (private mode): the choice holds for this session.
    }
  };


  const cancelRender = async (operationID: string) => {
    try {
      await CancelCompositionRender(operationID);
    } catch (error) {
      notifyError("Couldn't cancel the render", error);
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
  // reorders, its edges trim the source range (a still's edges resize its
  // length), a junction badge resizes its dissolve (dragging a cut right
  // CREATES the dissolve), a segment's body slides along its lane, its edges
  // trim. All of them funnel into the model's clamping ops, and all of them
  // coalesce to a single undo entry.

  type DragState =
    | {kind: 'clip-move'; clipID: string; fromIndex: number; targetIndex: number; startClientX: number}
    | {kind: 'clip-trim'; clipID: string; edge: 'in' | 'out'; startClientX: number; startValue: number}
    // A still's length has no source bound, so the drag keeps the scale it
    // began at: the timeline fits its extent, and a live scale would feed back
    // — every second the still grows shrinks what the next pixel is worth.
    | {kind: 'still-trim'; clipID: string; edge: 'in' | 'out'; startClientX: number; startLength: number; pxPerSecond: number}
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
    if (edge && isStillClip(clip)) {
      setDrag({kind: 'still-trim', clipID: clip.id, edge, startClientX: event.clientX, startLength: clipDurationSeconds(clip), pxPerSecond: pxPerSecond()});
    } else if (edge) {
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
        case 'still-trim': {
          // Either edge resizes: the out edge grows with the pointer, the in
          // edge shrinks with it — the lane is sequential, so the still keeps
          // its start and everything after it ripples, as a clip trim does.
          const seconds = (event.clientX - drag.startClientX) / drag.pxPerSecond;
          applyLive(setStillDuration(docRef.current, drag.clipID, drag.edge === 'in' ? drag.startLength - seconds : drag.startLength + seconds));
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

  // Auto-update: once the timeline has sat still for a beat — no drag in
  // flight, the editor on screen — a stage that doesn't show it is brought up
  // to date: a listed render of exactly this timeline is restaged, anything
  // else re-renders (restarting a preview still busy with an older edit). The
  // decision is the model's (compositionAutoPreviewAction); this only times
  // it. The timer checks the submit count when it fires, so a click that
  // rendered in the meantime wins.
  const completedPreviews = renderRows.filter((op) => op.mode !== 'export' && op.status === 'completed' && !!op.resultUrl);
  const autoAction = compositionAutoPreviewAction({
    enabled: autoPreview,
    paused: !!props.suspended || drag !== null,
    pinned: stagePinnedKey === currentPreviewKey,
    blocked: renderBlocked,
    currentKey: currentPreviewKey,
    stagedKey: preview?.key ?? null,
    running: runningRender ? {mode: runningRender.mode, previewKey: runningRender.previewKey} : null,
    completedKeys: completedPreviews.map((op) => op.previewKey ?? ''),
    skipKey: autoSkipKey,
  });
  const autoRenders = autoAction === 'render';
  const latestRef = useRef({submitRender, completedPreviews});
  latestRef.current = {submitRender, completedPreviews};
  useEffect(() => {
    if (autoAction === 'none') {
      return;
    }
    const submitsAtSchedule = submitCountRef.current;
    const timer = window.setTimeout(() => {
      if (autoAction === 'stage') {
        const listed = latestRef.current.completedPreviews.find((op) => op.previewKey === currentPreviewKey);
        if (listed) {
          stageRender(listed, true);
        }
        return;
      }
      if (submitCountRef.current === submitsAtSchedule) {
        void latestRef.current.submitRender(true);
      }
    }, autoAction === 'stage' ? autoRestageDelayMs : autoPreviewDelayMs);
    return () => window.clearTimeout(timer);
  }, [autoAction, currentPreviewKey]);

  // A completed auto render's file goes once a newer render has replaced it
  // in the auto row and on the stage — nothing can reach it after that.
  useEffect(() => {
    for (const id of [...autoFilesRef.current]) {
      if (id !== autoRender?.id && id !== preview?.opId) {
        autoFilesRef.current.delete(id);
        void AppBindings.DiscardCompositionPreview(id).catch(() => {});
      }
    }
  }, [autoRender?.id, preview?.opId]);

  // Closing the editor leaves no way back to its previews (a reopened
  // composer lists only renders still in flight), so their files go, and an
  // auto render still running is cancelled — it was upkeep for a stage that
  // no longer exists. A manual render keeps running and lands in a reopened
  // composer, as before.
  const rendersRef = useRef(renders);
  rendersRef.current = renders;
  useEffect(() => () => {
    const auto = autoRenderRef.current;
    if (auto && renderInFlight(auto)) {
      void CancelCompositionRender(auto.id).catch(() => {});
    }
    const finished = rendersRef.current.filter((op) => op.mode !== 'export' && op.status === 'completed' && op.resultUrl).map((op) => op.id);
    for (const id of new Set([...autoFilesRef.current, ...finished])) {
      void AppBindings.DiscardCompositionPreview(id).catch(() => {});
    }
  }, []);

  // addMedia resolves and measures one picked asset, then lands it: videos
  // and images (as stills held for the default length) append to the video
  // lane, audio queues onto the selected lane when one is selected, else the
  // last lane (a lane is created when none exists) at the sequence end — the
  // picker's landing rules and the seed's are one shape.
  const addMedia = async (asset: main.ConversationAsset) => {
    setPickerError('');
    setAddingIDs((ids) => [...ids, asset.id]);
    try {
      const wireRef = await AppBindings.ResolveCompositionMedia(sourceConversationID, asset.id);
      const ref = normalizeCompositionMediaRef(wireRef);
      if (ref.kind === 'audio') {
        let next = docRef.current;
        let targetLane = selection.kind === 'lane' ? next.audioLanes.find((lane) => lane.id === selection.laneID) : undefined;
        if (!targetLane) {
          if (!next.audioLanes.length) {
            next = addLane(next);
          }
          targetLane = next.audioLanes[next.audioLanes.length - 1];
        }
        commit(addSegmentToLane(next, targetLane.id, ref, compositionSequenceDuration(next)));
      } else {
        commit(addClip(docRef.current, ref));
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
    try {
      await persistRevision();
      notifySuccess('Saved to the conversation', {message: docRef.current.name || 'Composition'});
    } catch (error) {
      notifyError("Couldn't save to the conversation", error);
    } finally {
      setSaving(false);
    }
  };

  // saveAndExport is the export door: pin a revision first (saving the
  // working doc when anything is unsaved), then render THAT artifact with
  // the house encode — later edits cannot touch what export produces. The
  // save step's failure toasts its own outcome and stands the export down;
  // a successful submit toasts nothing here — the render runs in the
  // background and its completion reports through the op rows.
  const saveAndExport = async () => {
    setExporting(true);
    try {
      let artifactID = activeArtifactID;
      if (dirty || !artifactID) {
        try {
          artifactID = await persistRevision();
        } catch (error) {
          notifyError("Couldn't save the composition", error);
          return;
        }
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
        // A running preview yields to the export (Replace never stops an
        // export, and auto-update waits for this one).
        replace: true,
        ...(exportAdvancedOpen && Number.isFinite(width) && width > 0 ? {width} : {}),
        ...(exportAdvancedOpen && Number.isFinite(height) && height > 0 ? {height} : {}),
        ...(exportAdvancedOpen && Number.isFinite(fps) && fps > 0 ? {fps} : {}),
        ...(exportAdvancedOpen && Number.isFinite(crf) && crf > 0 ? {crf} : {}),
      }));
      addRenderOp(op);
    } catch (error) {
      notifyError("Couldn't start the export", error);
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
      notifyError("Couldn't open that revision", error);
    } finally {
      setLoadingRevision(false);
    }
  };

  const downloadSaved = async (url: string, index: number) => {
    await trackSave('composition project', SaveComposition(main.SaveTranscriptRequest.createFrom({
      path: url,
      suggestedName: `${doc.name.replace(/\s+/g, '-').toLowerCase() || 'composition'}-${Date.now()}-${index + 1}`,
    })));
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
    <section className={`composition-editor${audioOnly ? ' audio-first' : ''}`}>
      <EditorHeader
        title={audioOnly ? 'Audio Composer' : 'Video Composer'}
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
              {audioOnly ? (
                <>Audio mix · {formatCompositionClock(audioExtent)}{doc.audioLanes.length ? ` · ${doc.audioLanes.length} audio ${doc.audioLanes.length === 1 ? 'lane' : 'lanes'}` : ''}</>
              ) : (
                <>{doc.video.length} clips{stillCount > 0 ? ` (${stillCount} still${stillCount === 1 ? '' : 's'})` : ''} · {formatCompositionClock(sequenceDuration)}
                  {doc.audioLanes.length ? ` · ${doc.audioLanes.length} audio ${doc.audioLanes.length === 1 ? 'lane' : 'lanes'}` : ''}
                  {` · ${frame.width}×${frame.height} @ ${Math.round(frame.fps)}fps ${doc.output.fit}`}</>
              )}
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
              kinds={['video', 'audio', 'image']}
              addTitles={{image: 'Add this image to the end of the video lane as a still'}}
              refCounts={refCounts}
              busyIDs={addingIDs}
              error={pickerError}
              emptyText="No video, audio, or image assets found."
              onAdd={(asset) => void addMedia(asset)}
              onClose={() => setPickerOpen(false)}
            />
          ) : null}

          <div className="composition-stage">
            {preview ? (
              <div className={`composition-stage-media${preview.audioOnly ? ' audio' : ''}${stageStale ? ' stale' : ''}`}>
                {preview.audioOnly ? (
                  <audio key={preview.url} ref={(el) => { previewMediaRef.current = el; }} className="composition-preview composition-preview-audio" src={preview.url} controls
                    onLoadedMetadata={resumeStagedMedia} onTimeUpdate={handleMediaTime} onSeeked={handleMediaTime} onVolumeChange={() => { if (previewMediaRef.current) setTransportMuted(previewMediaRef.current.muted); }}
                    onPlay={() => { setTransportPlaying(true); startPlayheadLoop(); }} onPause={() => { setTransportPlaying(false); stopPlayheadLoop(); handleMediaTime(); }} onEnded={() => { setTransportPlaying(false); stopPlayheadLoop(); handleMediaTime(); }} />
                ) : (
                  <video key={preview.url} ref={(el) => { previewMediaRef.current = el; }} className="composition-preview" src={preview.url} poster={compositionPosterURL(preview.url)} controls
                    onLoadedMetadata={resumeStagedMedia} onTimeUpdate={handleMediaTime} onSeeked={handleMediaTime} onVolumeChange={() => { if (previewMediaRef.current) setTransportMuted(previewMediaRef.current.muted); }}
                    onPlay={() => { setTransportPlaying(true); startPlayheadLoop(); }} onPause={() => { setTransportPlaying(false); stopPlayheadLoop(); handleMediaTime(); }} onEnded={() => { setTransportPlaying(false); stopPlayheadLoop(); handleMediaTime(); }} />
                )}
                {/* The staged render lags the timeline: say so where the
                    eye already is. Pointer-transparent over the video so its
                    controls keep working. */}
                {stageFreshness !== null && stageStale ? (
                  <div className="composition-stage-badge" role="status">{stalePreviewText(stageFreshness, renderingLatest, autoRenders)}</div>
                ) : null}
              </div>
            ) : (
              <div className="composition-preview-empty">
                {runningRender && runningRender.mode !== 'export'
                  ? 'Rendering the preview…'
                  : 'No preview yet — render one to watch the assembled sequence. Trims, dissolves, and the audio mix are exactly what export will produce.'}
              </div>
            )}
          </div>

          {/* The render controls are their own row, outside the stage: a
              short window compresses the stage (the media letterboxes down),
              never this row — the Render button is the freshness light, so it
              must stay on screen whenever the badge can say "outdated". */}
          <div className="composition-stage-controls">
            <button
              type="button"
              className={renderButton.className}
              disabled={renderButton.disabled}
              title={renderButton.title}
              onClick={() => void submitRender()}
            >
              {renderButton.label}
            </button>
            {runningRender ? (
              <button type="button" onClick={() => void cancelRender(runningRender.id)}>Cancel</button>
            ) : null}
            <label
              className="composition-toggle composition-auto-toggle"
              title="Re-render the preview on its own about a second after each change settles — never mid-drag; a newer edit restarts a render still running. Each preview re-encodes the whole timeline (or the From/To range) at full output size, so a long sequence keeps ffmpeg busy while you edit."
            >
              <input type="checkbox" checked={autoPreview} onChange={(event) => toggleAutoPreview(event.target.checked)} />
              Auto-update
            </label>
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
                <select value={effectiveSoloLaneID} onChange={(event) => setSoloLaneID(event.target.value)}>
                  <option value="">— none —</option>
                  {doc.audioLanes.map((lane) => <option key={lane.id} value={lane.id}>{lane.name}</option>)}
                </select>
              </label>
            ) : null}
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
              className={`composition-lane composition-lane-video${audioOnly ? ' audio-first' : ''}${selection.kind === 'doc' ? ' focused' : ''}`}
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
                const still = isStillClip(clip);
                const refURL = compositionRefURL(clip.ref);
                return (
                  <button
                    key={clip.id}
                    type="button"
                    className={`composition-clip${still ? ' still' : ''}${selection.kind === 'clip' && selection.clipID === clip.id ? ' selected' : ''}${missing ? ' missing' : ''}${clip.audio.muted ? ' audio-muted' : ''}${clip.audio.detached ? ' audio-detached' : ''}${drag?.kind === 'clip-move' && drag.clipID === clip.id ? ' dragging' : ''}`}
                    style={{left: pct(layout.starts[index]), width: pct(clipDurationSeconds(clip))}}
                    title={`${clip.ref.artifactId}${still ? ' · still' : ''} · ${formatCompositionClock(clipDurationSeconds(clip))}${missing ? ' · source file missing' : ''}\nDrag to reorder · drag an edge to ${still ? 'resize' : 'trim'} · select to edit`}
                    onPointerDown={(event) => beginClipDrag(event, clip, index)}
                  >
                    {still && refURL && !missing ? (
                      <span className="composition-clip-thumb" aria-hidden="true"><img src={refURL} alt="" loading="lazy" draggable={false} /></span>
                    ) : null}
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
              {doc.video.length === 0 ? <span className="composition-lane-empty">{audioOnly ? 'Audio-only project — a video or image added from the picker opens its lane here.' : 'No clips — add videos or images from the picker.'}</span> : null}
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
                <span className="composition-lane-label" title={`${lane.name}${lane.gainDb !== 0 ? ` ${lane.gainDb > 0 ? '+' : ''}${lane.gainDb}dB` : ''}${lane.muted ? ' (muted)' : ''}`}>
                  <span className="composition-lane-name">{lane.name}{lane.gainDb !== 0 ? ` ${lane.gainDb > 0 ? '+' : ''}${lane.gainDb}dB` : ''}</span>
                  {audioOnly ? (
                    <button
                      type="button"
                      className={`composition-lane-mute${lane.muted ? ' on' : ''}`}
                      title={lane.muted ? 'Unmute this lane in the mix' : 'Mute this lane in the mix'}
                      onClick={(event) => {
                        event.stopPropagation();
                        commit(setLaneMix(docRef.current, lane.id, {muted: !lane.muted}));
                      }}
                    >
                      M
                    </button>
                  ) : null}
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
                      <SegmentWaveform segment={segment} />
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
              {audioOnly ? (
                <p className="hint" title="An audio-only project renders its lane mix as an M4A file — the video frame settings return if a clip is added">
                  Audio-only project — the lane mix renders as M4A · {formatCompositionClock(audioExtent)} long. Adding a video from the picker turns it into a video sequence.
                </p>
              ) : (
                <>
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
                </>
              )}
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
              <strong>{isStillClip(selectedClip) ? 'Still' : 'Clip'} {doc.video.findIndex((clip) => clip.id === selectedClip.id) + 1}</strong>
              <p className="hint composition-ref-line" title={selectedClip.ref.path}>
                {selectedClip.ref.artifactId} · {selectedClip.ref.width ?? '?'}×{selectedClip.ref.height ?? '?'} · {isStillClip(selectedClip) ? 'still image' : formatCompositionClock(selectedClip.ref.durationSeconds)}
                {selectedClip.ref.conversationId && selectedClip.ref.conversationId !== sourceConversationID ? ' · another chat' : ''}
              </p>
              {isStillClip(selectedClip) ? (
                <>
                  <div className="composition-field-row">
                    {/* A still's only timing is how long it holds: no source
                        to trim into, so the NumberField resizes the length
                        (its min is the floor, not a source bound). */}
                    <NumberField
                      label="Hold (s)"
                      value={clipDurationSeconds(selectedClip)}
                      step={0.5}
                      min={compositionStillMinSeconds}
                      max={compositionStillMaxSeconds}
                      title="How long the still holds — drag its edges on the lane to resize, or type here"
                      onCommit={(value) => commit(setStillDuration(docRef.current, selectedClip.id, value))}
                    />
                  </div>
                  <p className="hint">Holds {formatCompositionClock(clipDurationSeconds(selectedClip))} starting at {formatCompositionClock(layout.starts[doc.video.findIndex((clip) => clip.id === selectedClip.id)] ?? 0)}.</p>
                </>
              ) : (
                <>
                  <div className="composition-field-row">
                    <NumberField label="In (s)" value={selectedClip.inSeconds} step={0.1} min={0} onCommit={(value) => commit(setClipRange(docRef.current, selectedClip.id, value, selectedClip.outSeconds))} />
                    <NumberField label="Out (s)" value={selectedClip.outSeconds} step={0.1} onCommit={(value) => commit(setClipRange(docRef.current, selectedClip.id, selectedClip.inSeconds, value))} />
                  </div>
                  <p className="hint">Plays {formatCompositionClock(clipDurationSeconds(selectedClip))} starting at {formatCompositionClock(layout.starts[doc.video.findIndex((clip) => clip.id === selectedClip.id)] ?? 0)}</p>
                </>
              )}

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
                ) : isStillClip(selectedClip) ? (
                  <p className="hint">A still carries no sound — put music or narration on an audio lane beneath it.</p>
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
            <span className="editor-session-note">Edits stay in the editor until saved; every save is a new revision. Downloads land in the panel below.</span>
          </div>

          <div className="composition-export-block">
            <strong>Export</strong>
            {/* EX-03's pre-render summary: what the file will be. */}
            <p className="hint">
              {audioOnly ? (
                <>Audio-only mix · M4A · {formatCompositionClock(audioExtent)}</>
              ) : (
                <>{frame.width}×{frame.height} @ {Math.round(frame.fps)}fps · {formatCompositionClock(sequenceDuration)}
                  {exportIncludeAudio ? ' · audio included' : ' · no audio'}
                  {exportIncludeAudio && overhang > 0 ? ` · ${overhang.toFixed(1)}s audio tail trims at the video end` : ''}</>
              )}
            </p>
            {audioOnly ? null : (
              <label className="composition-toggle" title="The mix rides the video (original audio, lanes, ducks). Turn it off for a silent file.">
                <input type="checkbox" checked={exportIncludeAudio} onChange={(event) => setExportIncludeAudio(event.target.checked)} />
                include audio
              </label>
            )}
            <button
              type="button"
              className="editor-generate"
              disabled={exporting || renderBlocked || runningRender?.mode === 'export'}
              title={dirty || !activeArtifactID
                ? 'Save the working timeline as a revision, then export that revision — later edits cannot change the file'
                : `Export the pinned revision into the conversation as a new ${audioOnly ? 'audio' : 'video'} artifact`}
              onClick={() => void saveAndExport()}
            >
              {exporting ? 'Exporting…' : dirty || !activeArtifactID ? 'Save & export' : 'Export revision'}
            </button>
            {audioOnly ? null : (
              <button type="button" className="composition-advanced-toggle" onClick={() => setExportAdvancedOpen((open) => !open)}>
                {exportAdvancedOpen ? '− Advanced' : '+ Advanced'}
              </button>
            )}
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

      <EditsPanel count={log.length + renderRows.length} emptyText="Nothing rendered or saved yet." bodyReservePx={editsPanelBodyReservePx}>
        <ul className="editor-op-list">
          {renderRows.map((op) => (
            <li key={op.id} className={`editor-op editor-op-${op.status}`}>
              <span className="editor-op-thumb-glyph">{op.status === 'completed' ? '▶' : op.status === 'failed' ? '✕' : op.status === 'cancelled' ? '–' : '⋯'}</span>
              <div className="editor-op-meta">
                <span className="editor-op-title">
                  {op.mode === 'export' ? 'Export' : op.auto ? 'Auto preview' : 'Preview'} · {op.audioOnly ? 'audio only' : `${op.width || '?'}×${op.height || '?'}`} · {formatCompositionClock(op.outputSeconds || 0)}
                  {op.rangeStartSeconds !== undefined && op.rangeEndSeconds !== undefined
                    ? ` · ${formatCompositionClock(op.rangeStartSeconds)}–${formatCompositionClock(op.rangeEndSeconds)}`
                    : ''}
                </span>
                <span className="editor-op-sub" title={op.error || undefined}>
                  {compositionRenderSummary(op)}
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
                      void trackSave(op.audioOnly ? 'audio' : 'video', SaveVideo(main.SaveVideoRequest.createFrom({
                        path: op.resultUrl,
                        suggestedName: `${doc.name.replace(/\s+/g, '-').toLowerCase() || 'composition'}-${(op.exportArtifactId || op.id).slice(0, 10)}`,
                      })));
                    }}
                  >
                    Download
                  </button>
                ) : null}
                {op.status === 'completed' && op.resultUrl && op.mode !== 'export' ? (
                  <button
                    type="button"
                    title="Play this render on the stage"
                    onClick={() => playRenderOnStage(op)}
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
