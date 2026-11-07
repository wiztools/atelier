import {useEffect, useRef, useState} from 'react';
import {
  GetConversation,
  ListEditSession,
  ResolveAudioEditSource,
  ResolveComicEditSource,
  ResolveCompositionEditSource,
  ResolveEditSource,
  ResolveSubtitleEditSource,
  ResolveMarkdownEditSource,
  ResolveDocumentCompare,
  ResolveVideoEditSource,
  CancelVideoEditSource,
  ReleaseVideoEditSourcePreview,
  SeedComic,
  SeedComposition,
} from '../../wailsjs/go/main/App';
import {main} from '../../wailsjs/go/models';
import {ImageEditorHandle} from './ImageEditor';
import {VideoEditorHandle} from './VideoEditor';
import {AudioEditorHandle} from './AudioEditor';
import {SubtitleEditorHandle} from './SubtitleEditor';
import {MarkdownEditorHandle} from './MarkdownEditor';
import {CompositionEditorHandle} from './CompositionEditor';
import {ComicEditorHandle} from './ComicEditor';
import {DocumentCompareHandle} from './DocumentCompare';
import {notifyError} from '../notifications';
import {View, EditSessionEditor} from '../types';

// useEditors owns the editor workspaces: each editor's handle (what it is
// editing; null while closed) and opening flag, the open functions that resolve
// a source and mount a workspace, the navigation guards that confirm before
// discarding unsaved edits, and the edit-session bookkeeping. App mounts the
// workspaces in its editor slot; the hook reaches back only to switch the view,
// open Settings for a missing tool, and resolve a project's library for the
// pickers.
function useEditors({setView, openSettings, libraryIDForProject}: {
  setView: React.Dispatch<React.SetStateAction<View>>;
  openSettings: () => void;
  libraryIDForProject: (projectID: string) => string;
}) {
  // The edit context the preview's Edit button uses: the conversation whose
  // card opened the preview plus the artifact id parsed from its URL. Null
  // for data: URLs (the image isn't persisted yet, so there is nothing to
  // seed an edit from).
  const [previewEditContext, setPreviewEditContext] = useState<{conversationID: string; artifactID: string} | null>(null);
  // The image editor workspace: a draft seeded from a source image (no
  // conversation exists yet) or a reopened session. Rendering it replaces the
  // chat transcript area, like the settings screens do.
  const [editorHandle, setEditorHandle] = useState<ImageEditorHandle | null>(null);
  const [videoEditorHandle, setVideoEditorHandle] = useState<VideoEditorHandle | null>(null);
  const [audioEditorHandle, setAudioEditorHandle] = useState<AudioEditorHandle | null>(null);
  const [subtitleEditorHandle, setSubtitleEditorHandle] = useState<SubtitleEditorHandle | null>(null);
  const [markdownEditorHandle, setMarkdownEditorHandle] = useState<MarkdownEditorHandle | null>(null);
  const [compositionEditorHandle, setCompositionEditorHandle] = useState<CompositionEditorHandle | null>(null);
  const [comicEditorHandle, setComicEditorHandle] = useState<ComicEditorHandle | null>(null);
  // The compare workspace (two documents diffed, read-only) shares the
  // editor slot but holds nothing unsaved: no dirty ref, no confirm-leave.
  const [documentCompareHandle, setDocumentCompareHandle] = useState<DocumentCompareHandle | null>(null);
  const [videoEditorOpening, setVideoEditorOpening] = useState(false);
  const [imageEditorOpening, setImageEditorOpening] = useState(false);
  const [audioEditorOpening, setAudioEditorOpening] = useState(false);
  const [subtitleEditorOpening, setSubtitleEditorOpening] = useState(false);
  const [markdownEditorOpening, setMarkdownEditorOpening] = useState(false);
  const [compositionEditorOpening, setCompositionEditorOpening] = useState(false);
  const [comicEditorOpening, setComicEditorOpening] = useState(false);
  const [documentCompareOpening, setDocumentCompareOpening] = useState(false);
  const [discardEditorDraft, setDiscardEditorDraft] = useState<'video' | 'audio' | 'subtitle' | 'markdown' | 'composition' | 'comic' | null>(null);
  const videoEditorDirty = useRef(false);
  const audioEditorDirty = useRef(false);
  const subtitleEditorDirty = useRef(false);
  const markdownEditorDirty = useRef(false);
  const compositionEditorDirty = useRef(false);
  const comicEditorDirty = useRef(false);
  const videoNavigationResolver = useRef<((discard: boolean) => void) | null>(null);
  const audioNavigationResolver = useRef<((discard: boolean) => void) | null>(null);
  const subtitleNavigationResolver = useRef<((discard: boolean) => void) | null>(null);
  const markdownNavigationResolver = useRef<((discard: boolean) => void) | null>(null);
  const compositionNavigationResolver = useRef<((discard: boolean) => void) | null>(null);
  const comicNavigationResolver = useRef<((discard: boolean) => void) | null>(null);
  const videoOpenGeneration = useRef(0);
  const videoOpeningIdentity = useRef<{conversationID: string; artifactID: string} | null>(null);
  const imageOpenGeneration = useRef(0);
  const audioOpenGeneration = useRef(0);
  const subtitleOpenGeneration = useRef(0);
  const markdownOpenGeneration = useRef(0);
  const compositionOpenGeneration = useRef(0);
  const comicOpenGeneration = useRef(0);
  const documentCompareOpenGeneration = useRef(0);
  // Mirror of editorOpen for the atelier:editor-launch listener (an empty-deps
  // effect): a mid-stream AI launch must not replace an editor already on
  // screen — an unsaved draft is never discarded by a tool-timed open.
  const editorOpenRef = useRef(false);
  // Either editor's source resolve in flight — every edit button disables on
  // it, so a slow open can't collect repeated clicks (or race the other
  // editor's later-landing handle into view).
  const editorOpening = imageEditorOpening || videoEditorOpening || audioEditorOpening || subtitleEditorOpening || markdownEditorOpening || compositionEditorOpening || comicEditorOpening || documentCompareOpening;
  const editorOpen = !!editorHandle || !!videoEditorHandle || !!audioEditorHandle || !!subtitleEditorHandle || !!markdownEditorHandle || !!compositionEditorHandle || !!comicEditorHandle || !!documentCompareHandle;

  // editSessionOpen: an editor of this kind is open on that session — or on a
  // session App doesn't know yet, which is the only one an op can belong to.
  function editSessionOpen(editor: EditSessionEditor, sessionID: string): boolean {
    const open = openEditorSessionsRef.current[editor];
    return open !== undefined && (open === '' || open === sessionID);
  }

  function editSessionOnScreen(editor: EditSessionEditor, sessionID: string): boolean {
    return openEditorSessionsRef.current.onScreen && editSessionOpen(editor, sessionID);
  }

  useEffect(() => {
    editorOpenRef.current = editorOpen;
  }, [editorOpen]);
  // openEditorSessionsRef mirrors which edit-session editor is open, and
  // whether it's on screen, for the app-global edit-op listener: undefined =
  // no editor of that kind is open, '' = open but its session isn't known to
  // App yet (the editors mint a session on their first submit).
  const openEditorSessionsRef = useRef<{onScreen: boolean} & Partial<Record<EditSessionEditor, string>>>({onScreen: false});

  function confirmLeaveComicEditor(): Promise<boolean> {
    if (!comicEditorHandle || !comicEditorDirty.current) return Promise.resolve(true);
    if (comicNavigationResolver.current) return Promise.resolve(false);
    setDiscardEditorDraft('comic');
    return new Promise((resolve) => { comicNavigationResolver.current = resolve; });
  }

  function confirmLeaveVideoEditor(): Promise<boolean> {
    if (!videoEditorHandle || !videoEditorDirty.current) return Promise.resolve(true);
    if (videoNavigationResolver.current) return Promise.resolve(false);
    setDiscardEditorDraft('video');
    return new Promise((resolve) => { videoNavigationResolver.current = resolve; });
  }

  function confirmLeaveAudioEditor(): Promise<boolean> {
    if (!audioEditorHandle || !audioEditorDirty.current) return Promise.resolve(true);
    if (audioNavigationResolver.current) return Promise.resolve(false);
    setDiscardEditorDraft('audio');
    return new Promise((resolve) => { audioNavigationResolver.current = resolve; });
  }

  function confirmLeaveSubtitleEditor(): Promise<boolean> {
    if (!subtitleEditorHandle || !subtitleEditorDirty.current) return Promise.resolve(true);
    if (subtitleNavigationResolver.current) return Promise.resolve(false);
    setDiscardEditorDraft('subtitle');
    return new Promise((resolve) => { subtitleNavigationResolver.current = resolve; });
  }

  function confirmLeaveMarkdownEditor(): Promise<boolean> {
    if (!markdownEditorHandle || !markdownEditorDirty.current) return Promise.resolve(true);
    if (markdownNavigationResolver.current) return Promise.resolve(false);
    setDiscardEditorDraft('markdown');
    return new Promise((resolve) => { markdownNavigationResolver.current = resolve; });
  }

  function confirmLeaveCompositionEditor(): Promise<boolean> {
    if (!compositionEditorHandle || !compositionEditorDirty.current) return Promise.resolve(true);
    if (compositionNavigationResolver.current) return Promise.resolve(false);
    setDiscardEditorDraft('composition');
    return new Promise((resolve) => { compositionNavigationResolver.current = resolve; });
  }

  // confirmLeaveEditor walks whichever editors hold unsaved work — only one
  // confirm flow is ever active, so the dialog resolves both waiters.
  async function confirmLeaveEditor(): Promise<boolean> {
    if (!(await confirmLeaveVideoEditor())) return false;
    if (!(await confirmLeaveAudioEditor())) return false;
    if (!(await confirmLeaveSubtitleEditor())) return false;
    if (!(await confirmLeaveMarkdownEditor())) return false;
    if (!(await confirmLeaveCompositionEditor())) return false;
    return confirmLeaveComicEditor();
  }

  function resolveEditorNavigation(discard: boolean) {
    setDiscardEditorDraft(null);
    const videoResolve = videoNavigationResolver.current;
    videoNavigationResolver.current = null;
    videoResolve?.(discard);
    const audioResolve = audioNavigationResolver.current;
    audioNavigationResolver.current = null;
    audioResolve?.(discard);
    const subtitleResolve = subtitleNavigationResolver.current;
    subtitleNavigationResolver.current = null;
    subtitleResolve?.(discard);
    const markdownResolve = markdownNavigationResolver.current;
    markdownNavigationResolver.current = null;
    markdownResolve?.(discard);
    const compositionResolve = compositionNavigationResolver.current;
    compositionNavigationResolver.current = null;
    compositionResolve?.(discard);
    const comicResolve = comicNavigationResolver.current;
    comicNavigationResolver.current = null;
    comicResolve?.(discard);
  }

  async function leaveEditorThen(action: () => void) {
    if (!(await confirmLeaveEditor())) return;
    closeImageEditor();
    action();
  }

  async function openVideoEditor(conversationID: string, artifactID: string) {
    if (!(await confirmLeaveEditor())) return;
    cancelVideoPreparation();
    cancelImagePreparation();
    cancelAudioPreparation();
    const generation = ++videoOpenGeneration.current;
    videoOpeningIdentity.current = {conversationID, artifactID};
    setVideoEditorOpening(true);
    try {
      const owner = await GetConversation(conversationID);
      if (generation !== videoOpenGeneration.current) return;
      if (owner.conversation.kind === 'edit') {
        await openEditSession(conversationID);
        return;
      }
      const source = await ResolveVideoEditSource(conversationID, artifactID);
      if (generation !== videoOpenGeneration.current) {
        void ReleaseVideoEditSourcePreview(source.url);
        for (const thumbnail of source.thumbnails ?? []) void ReleaseVideoEditSourcePreview(thumbnail);
        return;
      }
      setEditorHandle(null);
      setDocumentCompareHandle(null);
      setAudioEditorHandle(null);
      setSubtitleEditorHandle(null);
      setMarkdownEditorHandle(null);
      subtitleEditorDirty.current = false;
      videoEditorDirty.current = false;
      markdownEditorDirty.current = false;
      setVideoEditorHandle({source, sessionID: ''});
      // Preparation can complete while the user is in Settings; loading the
      // editor must not yank them out of it — Back will reveal it.
      setView((current) => (current === 'settings' ? current : 'app'));
    } catch (error) {
      // The common cause is a missing ffmpeg/ffprobe (the editor's preview
      // needs both), so the toast offers the Settings door the old error
      // card carried.
      if (generation === videoOpenGeneration.current) {
        notifyError("Couldn't open the video editor", error, {action: {label: 'Open Settings', run: () => void openSettings()}});
      }
    } finally {
      if (generation === videoOpenGeneration.current) {
        setVideoEditorOpening(false);
        videoOpeningIdentity.current = null;
      }
    }
  }

  function cancelVideoPreparation() {
    videoOpenGeneration.current++;
    setVideoEditorOpening(false);
    const identity = videoOpeningIdentity.current;
    videoOpeningIdentity.current = null;
    if (identity) void CancelVideoEditSource(identity.conversationID, identity.artifactID).catch(() => {});
  }

  function cancelImagePreparation() {
    // The image resolve has no backend cancellation (unlike the video
    // source's CancelVideoEditSource) — dropping the generation only makes
    // the finished resolve's result land nowhere.
    imageOpenGeneration.current++;
    setImageEditorOpening(false);
  }

  function cancelAudioPreparation() {
    // The audio resolve is a probe, not a preparation: dropping the
    // generation only makes the finished resolve's result land nowhere.
    audioOpenGeneration.current++;
    setAudioEditorOpening(false);
  }

  async function openImageEditor(conversationID: string, artifactID: string) {
    if (!(await confirmLeaveEditor())) return;
    cancelVideoPreparation();
    cancelImagePreparation();
    cancelAudioPreparation();
    const generation = ++imageOpenGeneration.current;
    setImageEditorOpening(true);
    try {
      const source = await ResolveEditSource(conversationID, artifactID);
      if (generation !== imageOpenGeneration.current) return;
      setVideoEditorHandle(null);
      setAudioEditorHandle(null);
      setSubtitleEditorHandle(null);
      setMarkdownEditorHandle(null);
      setDocumentCompareHandle(null);
      subtitleEditorDirty.current = false;
      videoEditorDirty.current = false;
      markdownEditorDirty.current = false;
      setEditorHandle({source, sessionID: ''});
      setView((current) => (current === 'settings' ? current : 'app'));
    } catch (error) {
      if (generation === imageOpenGeneration.current) notifyError("Couldn't open the image editor", error);
    } finally {
      if (generation === imageOpenGeneration.current) setImageEditorOpening(false);
    }
  }

  async function openAudioEditor(conversationID: string, artifactID: string) {
    if (!(await confirmLeaveEditor())) return;
    cancelVideoPreparation();
    cancelImagePreparation();
    cancelAudioPreparation();
    const generation = ++audioOpenGeneration.current;
    setAudioEditorOpening(true);
    try {
      const owner = await GetConversation(conversationID);
      if (generation !== audioOpenGeneration.current) return;
      if (owner.conversation.kind === 'edit') {
        await openEditSession(conversationID);
        return;
      }
      const source = await ResolveAudioEditSource(conversationID, artifactID);
      if (generation !== audioOpenGeneration.current) return;
      setEditorHandle(null);
      setDocumentCompareHandle(null);
      setVideoEditorHandle(null);
      setSubtitleEditorHandle(null);
      setMarkdownEditorHandle(null);
      subtitleEditorDirty.current = false;
      videoEditorDirty.current = false;
      markdownEditorDirty.current = false;
      audioEditorDirty.current = false;
      setAudioEditorHandle({source, sessionID: ''});
      setView((current) => (current === 'settings' ? current : 'app'));
    } catch (error) {
      if (generation === audioOpenGeneration.current) notifyError("Couldn't open the audio editor", error);
    } finally {
      if (generation === audioOpenGeneration.current) setAudioEditorOpening(false);
    }
  }

  // openSubtitleEditor mirrors openAudioEditor minus the session machinery:
  // the subtitle editor is local-until-export, so the resolve is the whole
  // load — cues, format, and the artifact identity the reply chips opened by.
  async function openSubtitleEditor(conversationID: string, artifactID: string) {
    if (!(await confirmLeaveEditor())) return;
    cancelVideoPreparation();
    cancelImagePreparation();
    cancelAudioPreparation();
    const generation = ++subtitleOpenGeneration.current;
    setSubtitleEditorOpening(true);
    try {
      const owner = await GetConversation(conversationID);
      if (generation !== subtitleOpenGeneration.current) return;
      if (owner.conversation.kind === 'edit') {
        await openEditSession(conversationID);
        return;
      }
      const source = await ResolveSubtitleEditSource(conversationID, artifactID);
      if (generation !== subtitleOpenGeneration.current) return;
      setEditorHandle(null);
      setDocumentCompareHandle(null);
      setVideoEditorHandle(null);
      setAudioEditorHandle(null);
      setMarkdownEditorHandle(null);
      videoEditorDirty.current = false;
      audioEditorDirty.current = false;
      markdownEditorDirty.current = false;
      subtitleEditorDirty.current = false;
      // The linked-media picker's scope comes from the conversation's own
      // record: the library that owns its project, or none for an independent
      // conversation. Resolved here rather than at render time — the sidebar's
      // conversation list is only the newest slice by mtime, so a project
      // conversation outside it would otherwise silently lose its library
      // scope.
      setSubtitleEditorHandle({source, libraryID: libraryIDForProject(owner.conversation.projectId ?? '')});
      setView((current) => (current === 'settings' ? current : 'app'));
    } catch (error) {
      if (generation === subtitleOpenGeneration.current) notifyError("Couldn't open the subtitle editor", error);
    } finally {
      if (generation === subtitleOpenGeneration.current) setSubtitleEditorOpening(false);
    }
  }

  // openMarkdownEditor mirrors openSubtitleEditor: the markdown editor is
  // local-until-save, so the resolve is the whole load — the document text and
  // the artifact identity the reply chips opened by. No session machinery, no
  // edit conversation kind: documents are never media-edit subjects.
  async function openMarkdownEditor(conversationID: string, artifactID: string) {
    if (!(await confirmLeaveEditor())) return;
    cancelVideoPreparation();
    cancelImagePreparation();
    cancelAudioPreparation();
    const generation = ++markdownOpenGeneration.current;
    setMarkdownEditorOpening(true);
    try {
      const source = await ResolveMarkdownEditSource(conversationID, artifactID);
      if (generation !== markdownOpenGeneration.current) return;
      setEditorHandle(null);
      setDocumentCompareHandle(null);
      setVideoEditorHandle(null);
      setAudioEditorHandle(null);
      setSubtitleEditorHandle(null);
      videoEditorDirty.current = false;
      audioEditorDirty.current = false;
      subtitleEditorDirty.current = false;
      markdownEditorDirty.current = false;
      setMarkdownEditorHandle({source});
      setView((current) => (current === 'settings' ? current : 'app'));
    } catch (error) {
      if (generation === markdownOpenGeneration.current) notifyError("Couldn't open the document editor", error);
    } finally {
      if (generation === markdownOpenGeneration.current) setMarkdownEditorOpening(false);
    }
  }

  // openDocumentCompare opens the compare workspace on two of a
  // conversation's documents — the compare_documents tool's launch (the
  // pair rides the event's seeds, before then after). The resolve is the
  // whole load: the diff arrives computed. Read-only, so it takes the slot
  // from every editor (after their own confirm-leave) and needs none itself.
  async function openDocumentCompare(conversationID: string, beforeArtifactID: string, afterArtifactID: string) {
    if (!(await confirmLeaveEditor())) return;
    cancelVideoPreparation();
    cancelImagePreparation();
    cancelAudioPreparation();
    const generation = ++documentCompareOpenGeneration.current;
    setDocumentCompareOpening(true);
    try {
      const source = await ResolveDocumentCompare(main.DocumentCompareRequest.createFrom({
        conversationId: conversationID,
        beforeArtifactId: beforeArtifactID,
        afterArtifactId: afterArtifactID,
      }));
      if (generation !== documentCompareOpenGeneration.current) return;
      setEditorHandle(null);
      setVideoEditorHandle(null);
      setAudioEditorHandle(null);
      setSubtitleEditorHandle(null);
      setMarkdownEditorHandle(null);
      setCompositionEditorHandle(null);
      setComicEditorHandle(null);
      videoEditorDirty.current = false;
      audioEditorDirty.current = false;
      subtitleEditorDirty.current = false;
      markdownEditorDirty.current = false;
      compositionEditorDirty.current = false;
      comicEditorDirty.current = false;
      setDocumentCompareHandle({source});
      setView((current) => (current === 'settings' ? current : 'app'));
    } catch (error) {
      if (generation === documentCompareOpenGeneration.current) notifyError("Couldn't open the comparison", error);
    } finally {
      if (generation === documentCompareOpenGeneration.current) setDocumentCompareOpening(false);
    }
  }

  // openCompositionEditor reopens a saved composition revision — the assets
  // panel's and the reply chips' Edit door. The third session-free editor:
  // the resolve is the whole load, and the picker's library scope comes from
  // the conversation's own record (the openSubtitleEditor rule).
  async function openCompositionEditor(conversationID: string, artifactID: string) {
    if (!(await confirmLeaveEditor())) return;
    cancelVideoPreparation();
    cancelImagePreparation();
    cancelAudioPreparation();
    const generation = ++compositionOpenGeneration.current;
    setCompositionEditorOpening(true);
    try {
      const owner = await GetConversation(conversationID);
      if (generation !== compositionOpenGeneration.current) return;
      if (owner.conversation.kind === 'edit') {
        await openEditSession(conversationID);
        return;
      }
      const source = await ResolveCompositionEditSource(conversationID, artifactID);
      if (generation !== compositionOpenGeneration.current) return;
      setEditorHandle(null);
      setDocumentCompareHandle(null);
      setVideoEditorHandle(null);
      setAudioEditorHandle(null);
      setSubtitleEditorHandle(null);
      setMarkdownEditorHandle(null);
      videoEditorDirty.current = false;
      audioEditorDirty.current = false;
      subtitleEditorDirty.current = false;
      markdownEditorDirty.current = false;
      compositionEditorDirty.current = false;
      setCompositionEditorHandle({source, libraryID: libraryIDForProject(owner.conversation.projectId ?? '')});
      setView((current) => (current === 'settings' ? current : 'app'));
    } catch (error) {
      if (generation === compositionOpenGeneration.current) notifyError("Couldn't open the composer", error);
    } finally {
      if (generation === compositionOpenGeneration.current) setCompositionEditorOpening(false);
    }
  }

  // openNewComposition starts an unsaved draft — the "New composition" and
  // "New audio mix" actions and the editor-launch intent's door. Seeding
  // resolves and measures the referenced artifacts in order; nothing
  // persists until the editor's first save, which is what lets this run
  // mid-stream safely. audioFirst is the ＋ Audio intent: an empty draft
  // presents audio-first (starter lane, no video-frame chrome) until content
  // decides otherwise.
  async function openNewComposition(conversationID: string, seedArtifactIDs: string[], audioFirst = false) {
    if (!(await confirmLeaveEditor())) return;
    cancelVideoPreparation();
    cancelImagePreparation();
    cancelAudioPreparation();
    const generation = ++compositionOpenGeneration.current;
    setCompositionEditorOpening(true);
    try {
      const owner = await GetConversation(conversationID);
      const source = await SeedComposition(main.SeedCompositionRequest.createFrom({
        conversationId: conversationID,
        artifactIds: seedArtifactIDs,
      }));
      if (generation !== compositionOpenGeneration.current) return;
      setEditorHandle(null);
      setDocumentCompareHandle(null);
      setVideoEditorHandle(null);
      setAudioEditorHandle(null);
      setSubtitleEditorHandle(null);
      setMarkdownEditorHandle(null);
      videoEditorDirty.current = false;
      audioEditorDirty.current = false;
      subtitleEditorDirty.current = false;
      markdownEditorDirty.current = false;
      compositionEditorDirty.current = false;
      setCompositionEditorHandle({source, libraryID: libraryIDForProject(owner.conversation.projectId ?? ''), ...(audioFirst ? {audioFirst: true} : {})});
      setView((current) => (current === 'settings' ? current : 'app'));
    } catch (error) {
      if (generation === compositionOpenGeneration.current) notifyError("Couldn't open the composer", error);
    } finally {
      if (generation === compositionOpenGeneration.current) setCompositionEditorOpening(false);
    }
  }

  // openComicEditor reopens a saved comic revision — the assets panel's and
  // the reply chips' Edit door. The fourth session-free editor: the resolve
  // is the whole load, and the picker's library scope comes from the
  // conversation's own record (the openSubtitleEditor rule).
  async function openComicEditor(conversationID: string, artifactID: string) {
    if (!(await confirmLeaveEditor())) return;
    cancelVideoPreparation();
    cancelImagePreparation();
    cancelAudioPreparation();
    const generation = ++comicOpenGeneration.current;
    setComicEditorOpening(true);
    try {
      const owner = await GetConversation(conversationID);
      if (generation !== comicOpenGeneration.current) return;
      if (owner.conversation.kind === 'edit') {
        await openEditSession(conversationID);
        return;
      }
      const source = await ResolveComicEditSource(conversationID, artifactID);
      if (generation !== comicOpenGeneration.current) return;
      setEditorHandle(null);
      setDocumentCompareHandle(null);
      setVideoEditorHandle(null);
      setAudioEditorHandle(null);
      setSubtitleEditorHandle(null);
      setMarkdownEditorHandle(null);
      setCompositionEditorHandle(null);
      videoEditorDirty.current = false;
      audioEditorDirty.current = false;
      subtitleEditorDirty.current = false;
      markdownEditorDirty.current = false;
      compositionEditorDirty.current = false;
      comicEditorDirty.current = false;
      setComicEditorHandle({source, libraryID: libraryIDForProject(owner.conversation.projectId ?? '')});
      setView((current) => (current === 'settings' ? current : 'app'));
    } catch (error) {
      if (generation === comicOpenGeneration.current) notifyError("Couldn't open the comic editor", error);
    } finally {
      if (generation === comicOpenGeneration.current) setComicEditorOpening(false);
    }
  }

  // openNewComic starts an unsaved draft — the "New comic" action and the
  // editor-launch intent's door. Seeding resolves and measures the referenced
  // images in order into the unplaced tray; nothing persists until the
  // editor's first save, which is what lets this run mid-stream safely.
  async function openNewComic(conversationID: string, seedArtifactIDs: string[]) {
    if (!(await confirmLeaveEditor())) return;
    cancelVideoPreparation();
    cancelImagePreparation();
    cancelAudioPreparation();
    const generation = ++comicOpenGeneration.current;
    setComicEditorOpening(true);
    try {
      const owner = await GetConversation(conversationID);
      const source = await SeedComic(main.SeedComicRequest.createFrom({
        conversationId: conversationID,
        artifactIds: seedArtifactIDs,
      }));
      if (generation !== comicOpenGeneration.current) return;
      setEditorHandle(null);
      setDocumentCompareHandle(null);
      setVideoEditorHandle(null);
      setAudioEditorHandle(null);
      setSubtitleEditorHandle(null);
      setMarkdownEditorHandle(null);
      setCompositionEditorHandle(null);
      videoEditorDirty.current = false;
      audioEditorDirty.current = false;
      subtitleEditorDirty.current = false;
      markdownEditorDirty.current = false;
      compositionEditorDirty.current = false;
      comicEditorDirty.current = false;
      setComicEditorHandle({source, libraryID: libraryIDForProject(owner.conversation.projectId ?? '')});
      setView((current) => (current === 'settings' ? current : 'app'));
    } catch (error) {
      if (generation === comicOpenGeneration.current) notifyError("Couldn't open the comic editor", error);
    } finally {
      if (generation === comicOpenGeneration.current) setComicEditorOpening(false);
    }
  }

  const openFinishedEditSessionRef = useRef((_editor: EditSessionEditor, _sessionID: string) => {});
  openFinishedEditSessionRef.current = (editor, sessionID) => {
    if (editSessionOpen(editor, sessionID)) {
      setView('app');
      return;
    }
    void leaveEditorThen(() => {
      void openEditSession(sessionID).then(() => setView('app'));
    });
  };

  // openEditSession reopens a saved edit session — the sidebar/search path
  // for Kind "edit" conversations. The handle seeds from the loaded session's
  // own source copy, so the editor can submit iterations without guessing.
  async function openEditSession(sessionID: string) {
    try {
      const state = await ListEditSession(sessionID);
      if (state.source.mediaKind === 'video') {
        setEditorHandle(null);
        setDocumentCompareHandle(null);
        setAudioEditorHandle(null);
        setSubtitleEditorHandle(null);
        subtitleEditorDirty.current = false;
        audioEditorDirty.current = false;
        videoEditorDirty.current = false;
        setVideoEditorHandle({source: main.EditSourceInfo.createFrom({...state.source,
          conversationId: state.parentConversationId || state.source.conversationId,
          conversationTitle: state.parentTitle,
        }), sessionID});
        setView((current) => (current === 'settings' ? current : 'app'));
        return;
      }
      if (state.source.mediaKind === 'audio') {
        setEditorHandle(null);
        setDocumentCompareHandle(null);
        setVideoEditorHandle(null);
        setSubtitleEditorHandle(null);
        subtitleEditorDirty.current = false;
        videoEditorDirty.current = false;
        audioEditorDirty.current = false;
        setAudioEditorHandle({source: main.EditSourceInfo.createFrom({...state.source,
          conversationId: state.parentConversationId || state.source.conversationId,
          conversationTitle: state.parentTitle,
        }), sessionID});
        setView((current) => (current === 'settings' ? current : 'app'));
        return;
      }
      setVideoEditorHandle(null);
      setAudioEditorHandle(null);
      setSubtitleEditorHandle(null);
      setDocumentCompareHandle(null);
      subtitleEditorDirty.current = false;
      audioEditorDirty.current = false;
      setEditorHandle({
        source: main.EditSourceInfo.createFrom({
          conversationId: state.parentConversationId || state.source.conversationId,
          conversationTitle: state.parentTitle,
          artifactId: state.source.artifactId,
          url: state.source.url,
          width: state.source.width,
          height: state.source.height,
        }),
        sessionID,
      });
      setView((current) => (current === 'settings' ? current : 'app'));
    } catch (error) {
      notifyError("Couldn't open the edit session", error);
    }
  }

  function closeImageEditor() {
    cancelVideoPreparation();
    cancelImagePreparation();
    cancelAudioPreparation();
    setVideoEditorHandle(null);
    setAudioEditorHandle(null);
    setSubtitleEditorHandle(null);
    setMarkdownEditorHandle(null);
    setCompositionEditorHandle(null);
    setComicEditorHandle(null);
    videoEditorDirty.current = false;
    audioEditorDirty.current = false;
    subtitleEditorDirty.current = false;
    markdownEditorDirty.current = false;
    compositionEditorDirty.current = false;
    comicEditorDirty.current = false;
    setEditorHandle(null);
    setDocumentCompareHandle(null);
    setPreviewEditContext(null);
  }

  return {
    audioEditorDirty,
    audioEditorHandle,
    audioEditorOpening,
    cancelVideoPreparation,
    closeImageEditor,
    comicEditorDirty,
    comicEditorHandle,
    compositionEditorDirty,
    compositionEditorHandle,
    confirmLeaveEditor,
    discardEditorDraft,
    documentCompareHandle,
    documentCompareOpening,
    editSessionOnScreen,
    editorHandle,
    editorOpen,
    editorOpenRef,
    editorOpening,
    leaveEditorThen,
    markdownEditorDirty,
    markdownEditorHandle,
    openAudioEditor,
    openComicEditor,
    openCompositionEditor,
    openDocumentCompare,
    openEditSession,
    openEditorSessionsRef,
    openFinishedEditSessionRef,
    openImageEditor,
    openMarkdownEditor,
    openNewComic,
    openNewComposition,
    openSubtitleEditor,
    openVideoEditor,
    previewEditContext,
    resolveEditorNavigation,
    setPreviewEditContext,
    subtitleEditorDirty,
    subtitleEditorHandle,
    videoEditorDirty,
    videoEditorHandle,
    videoEditorOpening,
  };
}

export {useEditors};
