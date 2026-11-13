// useAppModel is App's model: every piece of UI state and every handler the
// app's views render, assembled from the domain hooks (composer, chat stream,
// conversations, editors, libraries, config, catalogs, …) plus the few
// cross-domain orchestrators that join them (sending a chat, hydrating a
// conversation, archiving, loading config). Views take this object as one
// `app` prop; the names are App's own, so the JSX read `prompt` and now reads
// `app.prompt` — nothing else moved.
import {useEffect, useMemo, useRef, useState} from 'react';
import {
  CheckOllama,
  ChooseToolWorkspace,
  CreateEditorConversation,
  DeleteConversation,
  DeleteLibraryAsset,
  DetectLocalTools,
  GetConversation,
  GetConfig,
  HasFalAPIKey,
  HasReplicateAPIKey,
  HasOpenAICompatibleAPIKey,
  HasOpenRouterAPIKey,
  ListFalVideoDurations,
  ListFalSoundEffectDurations,
  ListReplicateVideoDurations,
  ListModels,
  ListOpenAICompatibleModels,
  LoadUIState,
  SaveImage,
  SaveVideo,
  SaveAudio,
  SetAssetAlias,
  SetConversationModelOverrides,
  SaveFalAPIKey,
  SaveReplicateAPIKey,
  SaveOpenRouterAPIKey,
  SaveUIState,
  SaveMarkdown,
  SaveComic,
  SaveComposition,
  SaveTranscript,
  StreamChat,
  RevealFile,
  UploadLibraryAsset,
} from '../../wailsjs/go/main/App';
import {main} from '../../wailsjs/go/models';
import {Environment, EventsOn} from '../../wailsjs/runtime/runtime';
import {detectEditorLaunchIntent} from '../editorLaunch';
import {readFileAsAttachment} from '../attachments';
import {DetachedDraft} from '../editor/detachedDraft';
import {
  composerHasMediaDrag,
  dropAcceptsFile,
  imagePayloadForOllama,
} from '../mediaIntake';
import {
  notifications,
  notifyError,
  notifySuccess,
  setSavedFileReveal,
  trackSave,
} from '../notifications';
import {
  settingsTabs,
  editOpKinds,
  View,
  SettingsTab,
  ChatProviderID,
  SpeechProviderID,
  ChatEntry,
  EditOpEvent,
  MediaToolSummaryView,
} from '../types';
import {
  defaultBaseURL,
  defaultVideoDurationOptions,
  defaultSoundDurationOptions,
} from '../modelDefaults';
import {asArray, formatError} from '../utils';
import {AnchoredMenu} from '../dialogs/AnchoredMenu';
import {
  chatModelOptionsFor,
  overrideKeysFromRecord,
  conversationOverridesPayload,
  ModelSelectionValue,
} from '../settings/ModelSelectionPanel';
import {parseHarnessRun} from '../usage/usageModel';
import {
  MentionCandidate,
  assetMentionLabel,
  assetTurnLabel,
  parseComposerMentions,
} from '../composerMentions';
import {
  historyAudios,
  historyComics,
  historyCompositions,
  historyImages,
  historyMarkdowns,
  historySubtitles,
  historyText,
  historyTranscripts,
  historyVideos,
} from '../historyContent';
import {useLibraries} from '../sidebar/useLibraries';
import {useHistorySearch} from '../sidebar/useHistorySearch';
import {useResizablePanels} from '../useResizablePanels';
import {useModelCatalogs} from '../settings/useModelCatalogs';
import {useProviderKeys} from '../settings/useProviderKeys';
import {useAppConfig} from '../useAppConfig';
import {useComposer} from '../composer/useComposer';
import {useConversations} from '../sidebar/useConversations';
import {useEditors} from '../editor/useEditors';
import {useChatStream} from '../chat/useChatStream';
import {useLocalTools} from '../settings/useLocalTools';
import {useDurationOptions} from '../settings/useDurationOptions';
import {useOverlays} from '../ui/useOverlays';
import {useAssets} from '../assets/useAssets';
import {useUsageReport} from '../usage/useUsageReport';
import {useUpdates} from '../updates/useUpdates';

export type AppModel = ReturnType<typeof useAppModel>;

export function useAppModel() {
  const {
    checkFalConnection, checkOpenRouterConnection, checkReplicateConnection, clearOpenAICompatibleKey,
    falAPIKeyInput, falError, falHasKey, falStatus, openRouterAPIKeyInput, openRouterError, openRouterHasKey,
    openRouterStatus, openaiCompatibleHasKey, openaiCompatibleKeyInput, openaiCompatibleStatus,
    replicateAPIKeyInput, replicateError, replicateHasKey, replicateStatus, saveOpenAICompatibleKey,
    setFalAPIKeyInput, setFalError, setFalHasKey, setFalStatus, setOpenRouterAPIKeyInput, setOpenRouterError,
    setOpenRouterHasKey, setOpenRouterStatus, setOpenaiCompatibleHasKey, setOpenaiCompatibleKeyInput,
    setOpenaiCompatibleStatus, setReplicateAPIKeyInput, setReplicateError, setReplicateHasKey,
    setReplicateStatus, checkRunwareConnection, clearRunwareKey, loadRunwareKeyState, runwareAPIKeyInput,
    runwareError, runwareHasKey, runwareStatus, saveRunwareKey, setRunwareAPIKeyInput,
  } = useProviderKeys();
  const {
    airplaneMode, applyConfig, audioExtendDuration, audioProvider, baseURL, chooseToolWorkspace,
    configLoaded, defaultWorkspaceRoot, falAudioCloneModel, falAudioExtendModel, falAudioModel,
    falImageEditModel, falInpaintModel, falLipsyncImageModel, falLipsyncVideoModel, falModel,
    falSoundEffectsModel, falSoundEffectsVideoModel, falTranscribeModel, falUpscaleModel,
    falVideoExtendModel, falVideoImageModel, falVideoKeyframeModel, falVideoModel, falVideoMotionModel,
    falVideoReframeModel, falVideoRestyleModel, falVideoUpscaleModel, falVoiceDesignModel, ffmpegBinary,
    ffprobeBinary, globalModelSelection, harnessModel, harnessModels, harnessProvider, imageAspectRatio,
    imageModel, imageProvider, imageSizePreset, imageSteps, inpaintProvider, lipsyncProvider, magickBinary,
    mediaTimeoutMinutes, model, ollamaNumCtx, ollamaSpeechModel, openaiCompatibleBaseURL,
    openaiCompatibleModel, patchSettingsModelSelection, primaryModels, primaryProvider,
    replicateAudioExtendModel, replicateImageEditModel, replicateInpaintModel, replicateLipsyncImageModel,
    replicateLipsyncVideoModel, replicateModel, runwareModel, replicateSoundEffectsModel, replicateSoundEffectsVideoModel,
    replicateSpeechModel, replicateTranscribeModel, replicateUpscaleModel, replicateVideoExtendModel,
    replicateVideoImageModel, replicateVideoModel, replicateVideoReframeModel, replicateVideoRestyleModel,
    replicateVideoUpscaleModel, setAirplaneMode, setBaseURL, setConfigLoaded, setFfmpegBinary,
    setFfprobeBinary, setHarnessModels, setHarnessProvider, setImageProvider, setLipsyncProvider,
    setMagickBinary, setMediaTimeoutMinutes, setModel, setOllamaNumCtx, setOpenaiCompatibleBaseURL,
    setPrimaryModels, setPrimaryProvider, setSipsBinary, setSoundDuration, setSystem,
    setTranscriptionProvider, setVideoDuration, setVideoProvider, setWhisperBinary, setWhisperChunkMinutes,
    setWhisperModel, setWhisperVADModel, sipsBinary, soundDuration, speechProvider, storageConfig, system,
    toolConfig, transcriptionProvider, videoAspectRatio, videoDuration, videoProvider, videoResolution,
    whisperBinary, whisperChunkMinutes, whisperModel, whisperVADModel,
  } = useAppConfig({openRouterHasKey, falHasKey, replicateHasKey, runwareHasKey});
  const [status, setStatus] = useState<main.OllamaStatus | null>(null);
  const [models, setModels] = useState<main.OllamaModel[]>([]);
  const [refreshing, setRefreshing] = useState(false);
  const {
    clearFalCatalogs,
    clearReplicateCatalogs,
    falAudioExtendModels,
    falAudioModels,
    falImageEditModels,
    falLipsyncImageModels,
    falLipsyncVideoModels,
    falModels,
    falSoundEffectModels,
    falSoundEffectsVideoModels,
    falTranscribeModels,
    falUpscaleModels,
    falVideoExtendModels,
    falVideoImageModels,
    falVideoKeyframeModels,
    falVideoModels,
    falVideoMotionModels,
    falVideoReframeModels,
    falVideoRestyleModels,
    falVideoUpscaleModels,
    falVoiceDesignModels,
    openRouterModels,
    openaiCompatibleModelOptions,
    openaiCompatibleModels,
    refreshFalModels,
    refreshOpenRouterModels,
    refreshReplicateModels,
    refreshRunwareModels,
    runwareModels,
    replicateAudioExtendModels,
    replicateImageEditModels,
    replicateLipsyncImageModels,
    replicateLipsyncVideoModels,
    replicateModels,
    replicateSoundEffectsModels,
    replicateSoundEffectsVideoModels,
    replicateSpeechModels,
    replicateTranscribeModels,
    replicateUpscaleModels,
    replicateVideoExtendModels,
    replicateVideoImageModels,
    replicateVideoModels,
    replicateVideoReframeModels,
    replicateVideoRestyleModels,
    replicateVideoUpscaleModels,
    setOpenRouterModels,
    setOpenaiCompatibleModels,
  } = useModelCatalogs();
  const {
    ffmpegStatus,
    ffprobeStatus,
    localToolsDetecting,
    magickStatus,
    redetectLocalTools,
    setLocalToolsReport,
    sipsStatus,
    whisperStatus,
  } = useLocalTools({configLoaded, whisperBinary, ffmpegBinary, ffprobeBinary, sipsBinary, magickBinary});
  const [assetsRefreshTick, setAssetsRefreshTick] = useState(0);
  const [libraryUploadsBusy, setLibraryUploadsBusy] = useState(false);
  const [activeConversationID, setActiveConversationID] = useState('');
  const copyResetRef = useRef<number | null>(null);
  // useChatStream registers its chunk listener before the conversation list
  // and the libraries tree exist (its transcript feeds the composer, which both
  // of those need), so it reaches them through these hoisted forwarders,
  // resolved when a chunk arrives. Like the functions they forward to, they
  // touch only stable setters, so the empty-deps listener never goes stale.
  function refreshConversationsOnChunk() {
    return refreshConversations();
  }
  function bumpLibrariesOnChunk() {
    bumpLibrariesRefresh();
  }
  const {
    activeStream, anyConversationInFlight, chat, chatIsEmpty, chatStreamDraftsRef, collapsedThinkingIDs,
    copiedMessageID, copyMessage, harnessRunDraftsRef, inFlightConversations, inFlightConversationsRef,
    markConversationInFlight, mediaUsage, modelUsage, resolveToolPermission, setActiveStream, setChat,
    setCollapsedThinkingIDs, shouldFollowTranscriptRef, stopChat, toggleThinkingCollapsed, toolPermissions,
    transcriptRef, visibleHarnessRun, visibleStreamRef,
  } = useChatStream({setActiveConversationID, setAssetsRefreshTick, copyResetRef, refreshConversationsOnChunk, bumpLibrariesOnChunk});
  const activeConversationIDRef = useRef('');
  const {
    acceptMention, addFiles, attachments, attachmentsRef, chatPromptMirrorRef, chatPromptRef,
    composerDraftsRef, composerDragging, composerRef, emptyPrompt, handleChatPromptChange,
    handleChatPromptKeyDown, handleChatPromptPaste, handleChatPromptScroll, handleComposerDragEnter,
    handleComposerDragLeave, handleComposerDrop, insertAssetReference, mentionIndex, mentionListRef,
    mentionMatchesState, mentionOpen, prompt, restoreDraftFor, setAttachments, setMentionIndex, setPrompt,
    stagingAttachments, stashCurrentDraft,
  } = useComposer({chatIsEmpty, activeConversationID, activeConversationIDRef, mentionCandidates, submitChat});
  const {
    cancelEditingConversationTitle, chatsOpen, confirmPurgeArchived, conversationList, conversations,
    copiedConversationID, copyConversationID, editingTitle, editingTitleID, handleConversationTitleKeyDown,
    handleHistoryScroll, hasMoreConversations, moveDialogConversation, openHistoryMenuID,
    purgeArchivedConversations, purgeBusy, refreshConversations, saveConversationTitle, setChatsOpen,
    setConfirmPurgeArchived, setConversations, setEditingTitle, setMoveDialogConversation,
    setOpenHistoryMenuID, showMoreConversations, startEditingConversationTitle, visibleConversations,
  } = useConversations({composerDraftsRef, copyResetRef});
  const {
    historyQuery,
    historyResults,
    historySearchBusy,
    historySearchError,
    historySearchTruncated,
    searchOpen,
    setHistoryQuery,
    setSearchOpen,
  } = useHistorySearch();
  // uiStateLoaded flips once the persisted navigation state is hydrated; the
  // save effect below waits for it so launch-time defaults can't clobber the
  // saved file.
  const [uiStateLoaded, setUiStateLoaded] = useState(false);
  // pendingProject is the {projectID, libraryID} a NEW chat will be filed into
  // on its first send (ChatRequest.projectId). Set by "New chat" inside a
  // project; cleared once the conversation exists (the record carries its own
  // membership from there) and when opening any existing conversation. The
  // library tree's own state and actions live in the useLibraries hook.
  const [pendingProject, setPendingProject] = useState<{projectID: string; libraryID: string} | null>(null);
  // Mirror ref for the empty-deps menu/keyboard listeners and the send path,
  // which read the pending project synchronously rather than from state.
  const pendingProjectRef = useRef<{projectID: string; libraryID: string} | null>(null);
  useEffect(() => {
    pendingProjectRef.current = pendingProject;
  }, [pendingProject]);
  const [view, setView] = useState<View>('app');
  const {
    exportUsageReport,
    openConversationUsageReport,
    openLibraryUsageReport,
    openProjectUsageReport,
    usageError,
    usageExporting,
    usageLoading,
    usageReport,
  } = useUsageReport({setView});
  // The project the ACTIVE conversation belongs to (from its summary). Feeds
  // the useLibraries env, the composer's library scope, and the mention pool.
  const activeConversationProjectID = activeConversationID
    ? conversations.find((item) => item.id === activeConversationID)?.projectId ?? ''
    : '';
  // draftWorkspace holds the per-conversation workspace selected for a NEW
  // chat before its first message locks it as immutable on the record. Empty
  // means "inherit the configured default." Reset whenever a new chat starts.
  const [draftWorkspace, setDraftWorkspace] = useState('');
  // startupError is boot-only: the one genuine startup failure (the launch
  // loadConfig rejecting), cleared by applyConfig or the card's Dismiss.
  // Every other failure is an action result and toasts instead.
  const [startupError, setStartupError] = useState('');
  const [settingsTab, setSettingsTab] = useState<SettingsTab>('providers');
  // Per-conversation model-override editing state: the draft is a snapshot of
  // the effective selection taken when the conversation-models view opens
  // (the panel edits it; the record is the truth it converges to), and the
  // keys are the fields this conversation pins. Mutator failures (e.g.
  // changing overrides while the turn streams) toast under 'conv-models'.
  const [convModelsDraft, setConvModelsDraft] = useState<ModelSelectionValue | null>(null);
  const [convModelsKeys, setConvModelsKeys] = useState<ReadonlySet<string>>(() => new Set());
  // draftModelOverrides is the turn-1 twin of the record's override: what the
  // NEXT conversation (none active yet) will run on, from its first message.
  // It travels once with ChatRequest.modelOverrides and the backend pins it
  // onto the new record; cleared once that conversation exists, after which
  // the record is the truth. Dormant while an older conversation is open —
  // same posture as draftWorkspace.
  const [draftModelOverrides, setDraftModelOverrides] = useState<main.ConversationModelOverrides | null>(null);
  const {openCapabilityID, previewImage, setOpenCapabilityID, setPreviewImage} = useOverlays();
  const {
    checkForUpdates,
    dismissUpdate,
    requestUpdateInstall,
    updateAvailable,
    updateBusy,
    updateCheckBusy,
    updateError,
    updateQueued,
  } = useUpdates({anyConversationInFlight});
  const shellRef = useRef<HTMLElement | null>(null);
  // The conversation whose open is in flight; a repeat click on the same row
  // (double-click enters rename) must not fetch it twice.
  const openingConversationRef = useRef<string>('');
  // The Libraries & projects sidebar tree — data, expansion/creation/rename/
  // delete state, and every tree action — lives in the useLibraries hook;
  // App destructures the same names its JSX uses. Everything crossing the
  // boundary (conversation refresh, workspace reset, the composer's pending
  // project) arrives via env, so the hook never reaches into App state; its
  // failures toast directly through the shared notifications store.
  const {
    libraries, librariesOpen, expandedLibraryIDs, expandedProjectIDs, projectConversations,
    creatingLibrary, newLibraryName, creatingProjectLibraryID, newProjectName,
    editingContainerID, editingContainerName, openContainerMenuID, confirmDeleteContainerID, containerBusy,
    exportPlan, importingLibrary,
    moveTargetGroups, libraryIDForProject, bumpLibrariesRefresh,
    startCreatingLibrary, startCreatingProject, submitNewLibrary, submitNewProject,
    startEditingContainer, cancelEditingContainer, saveContainerName, toggleContainerMenu, closeContainerMenu,
    confirmDeleteContainer, toggleLibraryExpanded, toggleProjectExpanded,
    startNewChatInProject, revealConversationInProject, handleNewConversationAction, handleNewProjectAction, moveConversation,
    forgetConversation, toggleLibrariesOpen, cancelCreatingLibrary, cancelCreatingProject, armContainerDelete,
    armLibraryExport, confirmExportLibrary, importLibrary, applySidebarState,
    notesDialogProject, notesDraft, notesSaving, openProjectNotes, closeProjectNotes, saveProjectNotes,
    setNewLibraryName, setNewProjectName, setEditingContainerName, setNotesDraft,
  } = useLibraries({
    activeConversationID,
    activeConversationProjectID,
    closeConversationMenu: () => setOpenHistoryMenuID(''),
    expandChatsSection: () => setChatsOpen(true),
    composerDraftsRef,
    pendingProjectRef,
    refreshConversations,
    resetWorkspace,
    setPendingProject,
    setView,
    startNewChat,
  });

  const {
    audioEditorDirty, audioEditorHandle, audioEditorOpening, cancelVideoPreparation, closeImageEditor,
    comicEditorDirty, comicEditorHandle, compositionEditorDirty, compositionEditorHandle, confirmLeaveEditor,
    discardEditorDraft, documentCompareHandle, documentCompareOpening, editSessionOnScreen, editorHandle,
    editorOpen, editorOpenRef, editorOpening, leaveEditorThen, markdownEditorDirty, markdownEditorHandle,
    openAudioEditor, openComicEditor, openCompositionEditor, openDocumentCompare, openEditSession,
    openEditorSessionsRef, openFinishedEditSessionRef, openImageEditor, openMarkdownEditor, openNewComic,
    openNewComposition, openSubtitleEditor, openVideoEditor, previewEditContext, resolveEditorNavigation,
    setPreviewEditContext, subtitleEditorDirty, subtitleEditorHandle, videoEditorDirty, videoEditorHandle,
    videoEditorOpening,
  } = useEditors({setView, openSettings, libraryIDForProject, captureDetachedDraft: () => captureDetachedDraftRef.current()});
  // The AI-routed launch listener below subscribes once at mount (empty
  // deps), so it must never call these open functions directly: they close
  // over libraryIDForProject, whose libraries list loads after mount — a
  // mount-time capture would read the empty initial list forever and open
  // the composer (and the comic tray, the subtitle picker) without its
  // library scope, so the picker would list only the conversation's own
  // media. Mirrors the File-menu action refs: re-pointed at the latest
  // closures every render; the listener calls through the ref.
  const editorLaunchActionsRef = useRef({
    openDocumentCompare, openNewComposition, openNewComic, openVideoEditor,
    openAudioEditor, openSubtitleEditor, openMarkdownEditor, openImageEditor,
  });
  editorLaunchActionsRef.current = {
    openDocumentCompare, openNewComposition, openNewComic, openVideoEditor,
    openAudioEditor, openSubtitleEditor, openMarkdownEditor, openImageEditor,
  };
  // Restore the left navigation's collapsed/expanded sections from the last
  // session. applySidebarState is a first-render copy by design (it only
  // touches stable setters and a ref), so this mount effect never refires.
  useEffect(() => {
    let cancelled = false;
    LoadUIState()
      .then((state) => {
        if (cancelled) {
          return;
        }
        const sidebar = state?.sidebar;
        setChatsOpen(!sidebar?.chatsCollapsed);
        applySidebarState(sidebar);
        // Restore the Settings screen's last tab so a fresh launch opens it
        // where the user left off. The save effect only runs once
        // uiStateLoaded flips, so this value cannot be overwritten first.
        const lastTab = state?.settings?.lastTab;
        if (settingsTabs.includes(lastTab as SettingsTab)) {
          setSettingsTab(lastTab as SettingsTab);
        }
      })
      .catch(() => {
        // No saved state (first launch) or an unreadable file — defaults stand.
      })
      .finally(() => {
        if (!cancelled) {
          setUiStateLoaded(true);
        }
      });
    return () => {
      cancelled = true;
    };
  }, []);

  // Persist the navigation state on every change rather than at quit: each
  // toggle is one tiny atomic write, so the file is already current whenever
  // the app shuts down — cleanly or not. A failure toasts under one key (this
  // fires on every sidebar toggle and tab switch, so repeats count in place
  // instead of stacking), and the next successful write dismisses it.
  useEffect(() => {
    if (!uiStateLoaded) {
      return;
    }
    SaveUIState(main.UIState.createFrom({
      sidebar: {
        chatsCollapsed: !chatsOpen,
        librariesCollapsed: !librariesOpen,
        expandedLibraries: Object.keys(expandedLibraryIDs).filter((id) => expandedLibraryIDs[id]),
        expandedProjects: Object.keys(expandedProjectIDs).filter((id) => expandedProjectIDs[id]),
      },
      settings: {
        lastTab: settingsTab,
      },
    })).then(() => notifications.dismissKey('ui-state-save')).catch((error) => {
      notifyError("Couldn't save the window state", error, {key: 'ui-state-save'});
    });
  }, [uiStateLoaded, chatsOpen, librariesOpen, expandedLibraryIDs, expandedProjectIDs, settingsTab]);

  // The library scope the composer + assets panel use: the pending project
  // while composing a new chat inside one, else the active conversation's
  // project.
  const composerLibraryID = pendingProject?.libraryID
    || (activeConversationProjectID ? libraryIDForProject(activeConversationProjectID) : '');
  const {
    assetsPanelOpen,
    libraryAssets,
    panelAssets,
    setAssetsPanelOpen,
  } = useAssets({activeConversationID, composerLibraryID, assetsRefreshTick});

  const selectedConversationID = activeConversationID;
  // Names for the toolbar breadcrumb and composer chip: the library (and
  // project) the current composition is scoped to.
  const composerProjectNames = useMemo(() => {
    if (!composerLibraryID) {
      return null;
    }
    const library = libraries.find((item) => item.id === composerLibraryID);
    if (!library) {
      return null;
    }
    const projectID = pendingProject?.projectID || activeConversationProjectID;
    const project = projectID ? asArray(library.projects).find((item) => item.id === projectID) : undefined;
    return {libraryName: library.name, projectName: project?.name ?? ''};
  }, [composerLibraryID, libraries, pendingProject, activeConversationProjectID]);

  // One conversation row, shared by the standalone chats list and the project
  // listings: open/rename/archive behave identically; the ⋮ menu's Move to…
  // opens the picker dialog for both row kinds (project rows included, so a
  // project → project move no longer detours through standalone).
  function renderConversationRow(conversation: main.ConversationSummary) {
    const inFlight = inFlightConversations[conversation.id];
    const selected = selectedConversationID === conversation.id;
    // A standalone row with no defined projects has nowhere to move; a
    // project row always offers at least the return to standalone.
    const canMove = moveTargetGroups.length > 0 || Boolean(conversation.projectId);
    return (
      <div key={conversation.id} className={`history-item${selected ? ' selected' : ''}`}>
        {editingTitleID === conversation.id ? (
          <input
            value={editingTitle}
            onChange={(event) => setEditingTitle(event.target.value)}
            onBlur={() => saveConversationTitle(conversation)}
            onKeyDown={(event) => handleConversationTitleKeyDown(event, conversation)}
            autoFocus
          />
        ) : (
          <>
            <button className="history-open" onClick={() => openConversationSummary(conversation)} onDoubleClick={(event) => { event.preventDefault(); startEditingConversationTitle(conversation); }}>
              <span>{conversation.title}</span>
              {/* The running indicator mounts only while the turn streams —
                  idle rows don't reserve the slot, so the title runs to the
                  full row width and the kebab sits flush after hover reveal. */}
              {inFlight ? (
                <small
                  className="history-kind in-flight"
                  title="Running"
                  aria-label="Conversation running"
                >
                  <span className="history-spinner" />
                </small>
              ) : null}
            </button>
            <div className="history-actions">
              <AnchoredMenu
                label={`More actions for ${conversation.title}`}
                open={openHistoryMenuID === conversation.id}
                onToggle={() => {
                  // Opening one menu family closes the other — both float in
                  // portals now, and two open at once reads as broken.
                  if (openHistoryMenuID !== conversation.id) {
                    closeContainerMenu();
                  }
                  setOpenHistoryMenuID((current) => current === conversation.id ? '' : conversation.id);
                }}
                onClose={() => setOpenHistoryMenuID('')}
              >
                <button onClick={() => startEditingConversationTitle(conversation)}>Rename</button>
                <button onClick={() => copyConversationID(conversation)}>
                  {copiedConversationID === conversation.id ? '✓ Copied' : 'Copy ID'}
                </button>
                <button onClick={() => {
                  setOpenHistoryMenuID('');
                  openConversationUsageReport(conversation.id);
                }}>Usage report</button>
                <button onClick={() => archiveConversation(conversation)}>Archive</button>
                {canMove ? (
                  <button onClick={() => {
                    setOpenHistoryMenuID('');
                    setMoveDialogConversation(conversation);
                  }}>Move to…</button>
                ) : null}
              </AnchoredMenu>
            </div>
          </>
        )}
      </div>
    );
  }

  useEffect(() => {
    loadConfig().catch((error) => {
      setStartupError(formatError(error));
      setConfigLoaded(true);
      refreshOllama(defaultBaseURL).catch((refreshError) => setStatus({
        online: false,
        baseURL: defaultBaseURL,
        error: formatError(refreshError),
      }));
    });
  }, []);

  useEffect(() => {
    return () => {
      if (copyResetRef.current) {
        window.clearTimeout(copyResetRef.current);
      }
    };
  }, []);

  // On a fresh launch, put the cursor in the chat box so the user can start
  // typing immediately. Fires once, when config finishes loading.
  useEffect(() => {
    if (!configLoaded || view !== 'app') {
      return;
    }
    const timeout = window.setTimeout(() => {
      chatPromptRef.current?.focus();
    }, 0);
    return () => window.clearTimeout(timeout);
  }, [configLoaded]);

  // AI-routed editor launches: the harness's open_editor tool fired (its
  // evidence line names what was opened) and the named conversation's asset
  // should appear in the matching editor. Two guards keep a mid-stream launch
  // from hijacking the workspace: the event's conversation must be the one on
  // screen, and an already-open editor is never replaced — an unsaved draft is
  // not discarded by a tool-timed open, and the reply's evidence still tells
  // the user what was opened. Mirrors the chat:chunk pattern — refs only, so
  // the empty-deps subscription never fires a stale closure: the guards read
  // refs, and the open functions ride editorLaunchActionsRef.
  useEffect(() => {
    const onEditorLaunch = (event: {conversationId?: string; artifactId?: string; kind?: string; seedArtifactIds?: string[]}) => {
      const actions = editorLaunchActionsRef.current;
      const conversationID = event?.conversationId ?? '';
      const artifactID = event?.artifactId ?? '';
      const kind = event?.kind;
      // The composer is the one kind that opens without an artifact — its
      // identity is the conversation, and the seeds ride the payload.
      const composition = kind === 'composition';
      // The comic editor is the other kind that opens without an artifact —
      // its seeds (this turn's images) ride the payload.
      const comic = kind === 'comic';
      // The compare workspace opens on two documents — before, then after —
      // riding the seeds the same way (compare_documents' launch).
      const compare = kind === 'compare';
      if (!conversationID || (!artifactID && !composition && !comic && !compare) ||
          (kind !== 'image' && kind !== 'video' && kind !== 'audio' && kind !== 'subtitle' && kind !== 'markdown' && kind !== 'composition' && kind !== 'comic' && kind !== 'compare')) {
        return;
      }
      if (conversationID !== activeConversationIDRef.current || editorOpenRef.current) {
        return;
      }
      if (compare) {
        const seeds = asArray(event?.seedArtifactIds);
        if (seeds.length === 2) void actions.openDocumentCompare(conversationID, seeds[0], seeds[1]);
        return;
      }
      if (composition) {
        void actions.openNewComposition(conversationID, asArray(event?.seedArtifactIds));
        return;
      }
      if (comic) {
        void actions.openNewComic(conversationID, asArray(event?.seedArtifactIds));
        return;
      }
      if (kind === 'video') {
        void actions.openVideoEditor(conversationID, artifactID);
      } else if (kind === 'audio') {
        void actions.openAudioEditor(conversationID, artifactID);
      } else if (kind === 'subtitle') {
        void actions.openSubtitleEditor(conversationID, artifactID);
      } else if (kind === 'markdown') {
        void actions.openMarkdownEditor(conversationID, artifactID);
      } else {
        void actions.openImageEditor(conversationID, artifactID);
      }
    };
    return EventsOn('atelier:editor-launch', onEditorLaunch);
  }, []);

  // Toasts that name a saved file offer to show it (trackSave's saves, the
  // library export). notifications.ts can't import bindings, so App installs
  // the action; the label follows the platform's file manager — Finder
  // selects the file, elsewhere RevealFile opens its folder. A reveal that
  // fails (the file moved since) toasts why.
  useEffect(() => {
    let disposed = false;
    void Environment().then((env) => {
      if (disposed) {
        return;
      }
      setSavedFileReveal({
        label: env.platform === 'darwin' ? 'Show in Finder' : 'Show in folder',
        reveal: (path) => {
          void RevealFile(path).catch((error) => notifyError("Couldn't show the file", error));
        },
      });
    }).catch(() => {});
    return () => {
      disposed = true;
      setSavedFileReveal(null);
    };
  }, []);

  // Background jobs report when they finish. App owns the RESULT, so it
  // reaches the user even when the job's editor closed mid-run (the editors
  // fold the same events into their own rows). Composition EXPORTS: a
  // completed one adopted a new artifact into its conversation — toast it and
  // refresh what shows that conversation; a failed one toasts. Previews and
  // cancels report nothing (the stage and the row are their feedback). Edit
  // ops: a failure always toasts; a completion toasts only when its editor
  // isn't on screen (otherwise the stage shows the result), with an Open
  // action back to the session. Each op reports once.
  useEffect(() => {
    const reported = new Set<string>();
    const offComposition = EventsOn('atelier:composition-op', (event: unknown) => {
      const op = main.CompositionRenderOperation.createFrom(event);
      if (op.mode !== 'export' || (op.status !== 'completed' && op.status !== 'failed') || reported.has(op.id)) {
        return;
      }
      reported.add(op.id);
      if (op.status === 'failed') {
        notifyError('Composition export failed', op.error || 'The render stopped without reporting why.');
        return;
      }
      notifySuccess('Exported to the conversation', {message: op.audioOnly ? 'Composition audio' : 'Composition video'});
      if (op.conversationId) {
        refreshAfterConversationWriteRef.current(op.conversationId);
      }
    });
    const offEdit = EventsOn('atelier:edit-op', (event: EditOpEvent) => {
      const op = event?.operation;
      const sessionID = event?.sessionConversationId ?? '';
      if (!op?.id || (op.status !== 'completed' && op.status !== 'failed') || reported.has(op.id)) {
        return;
      }
      reported.add(op.id);
      const kind = editOpKinds[op.kind];
      const label = kind?.label ?? 'Edit';
      if (op.status === 'failed') {
        notifyError(`${label} failed`, op.error || 'The edit stopped without reporting why.');
        return;
      }
      if (kind && editSessionOnScreen(kind.editor, sessionID)) {
        return;
      }
      notifySuccess(`${label} finished`, {
        message: 'Open the editor to review the result.',
        action: sessionID && kind ? {label: 'Open', run: () => openFinishedEditSessionRef.current(kind.editor, sessionID)} : undefined,
      });
    });
    return () => {
      offComposition();
      offEdit();
    };
  }, []);

  // File-menu creation actions (main.go's File menu emits these; see the
  // menuNew* handlers in app.go). The refs always point at the latest handler
  // closures, so the empty-deps subscription never fires a stale one.
  useEffect(() => {
    const offs = [
      EventsOn('atelier:menu-new-conversation', () => newConversationActionRef.current()),
      EventsOn('atelier:menu-new-library', () => newLibraryActionRef.current()),
      EventsOn('atelier:menu-new-project', () => newProjectActionRef.current()),
    ];
    return () => offs.forEach((off) => off());
  }, []);

  const {
    assetsWidth,
    resizingAssets,
    resizingSidebar,
    setResizingAssets,
    setResizingSidebar,
    sidebarWidth,
  } = useResizablePanels({shellRef, setAssetsPanelOpen});

  // modelOptions feeds the Ollama-only lists (primary picker's Ollama branch,
  // harness dropdown, image-model fallback). It is built from the fetched
  // Ollama models plus the configured harness/image models (so those stay
  // selectable), but deliberately NOT the primary model: if the stored primary
  // isn't a real Ollama model (e.g. an OpenRouter id that leaked in), it must
  // fall out of the list so the validation effect below heals it to a real
  // model instead of letting the bad value self-validate.
  const modelOptions = useMemo(() => {
    return Array.from(new Set([...asArray(models).map((item) => item.name), harnessModels.ollama, imageModel].filter(Boolean)));
  }, [harnessModels.ollama, imageModel, models]);
  const primaryModelOptions = useMemo(
    () => chatModelOptionsFor(primaryProvider, {modelOptions, openRouterModels, openaiCompatibleModels: openaiCompatibleModelOptions}),
    [modelOptions, openRouterModels, openaiCompatibleModelOptions, primaryProvider],
  );
  const primaryModelIsValid = primaryModelOptions.some((option) => option.value === model);

  // ---- Per-conversation model overrides ---------------------------------
  // The active conversation's record may pin model selections that override
  // the global Settings (differential: unset fields inherit). Everything the
  // UI shows derives from globalModelSelection ⊕ conversationOverrides so the
  // composer, the badge, and the conversation-models screen agree.

  const activeConversation = useMemo(
    () => conversations.find((conversation) => conversation.id === activeConversationID) ?? null,
    [conversations, activeConversationID],
  );
  const conversationOverrides = activeConversation?.modelOverrides ?? null;
  // Before any conversation exists, the pending draft stands in for the
  // record everywhere the UI derives effective selections.
  const uiModelOverrides = activeConversationID ? conversationOverrides : draftModelOverrides;
  const uiOverridesActive = overrideKeysFromRecord(uiModelOverrides).size > 0;

  const conversationModelSelection = useMemo<ModelSelectionValue>(() => {
    const global = globalModelSelection;
    const overrides = uiModelOverrides;
    if (!overrides) {
      return global;
    }
    const isChatProvider = (value?: string): value is ChatProviderID =>
      value === 'ollama' || value === 'openrouter' || value === 'openai-compatible';
    const harnessProvider = isChatProvider(overrides.harnessProvider) ? overrides.harnessProvider : global.harnessProvider;
    const primaryProvider = isChatProvider(overrides.primaryProvider) ? overrides.primaryProvider : global.primaryProvider;
    const imageProvider = overrides.imageProvider === 'fal' || overrides.imageProvider === 'replicate' || overrides.imageProvider === 'openai-compatible' ? overrides.imageProvider : global.imageProvider;
    const videoProvider = overrides.videoProvider === 'replicate' ? 'replicate' : overrides.videoProvider === 'fal' ? 'fal' : global.videoProvider;
    const inpaintProvider = overrides.inpaintProvider === 'replicate' ? 'replicate' : overrides.inpaintProvider === 'fal' ? 'fal' : global.inpaintProvider;
    const lipsyncProvider = overrides.lipsyncProvider === 'replicate' ? 'replicate' : overrides.lipsyncProvider === 'fal' ? 'fal' : global.lipsyncProvider;
    const speechProvider: SpeechProviderID = overrides.speechProvider === 'ollama' || overrides.speechProvider === 'replicate' || overrides.speechProvider === 'fal' ? overrides.speechProvider : global.speechProvider;
    const audioProvider = overrides.audioProvider === 'replicate' ? 'replicate' : overrides.audioProvider === 'fal' ? 'fal' : global.audioProvider;
    const effectiveTranscriptionProvider = overrides.transcriptionProvider === 'fal' || overrides.transcriptionProvider === 'local-whisper' || overrides.transcriptionProvider === 'replicate' ? overrides.transcriptionProvider : global.transcriptionProvider;
    const next: ModelSelectionValue = {
      ...global,
      primaryProvider,
      primaryModel: overrides.primaryModel || primaryModels[primaryProvider],
      harnessProvider,
      harnessModel: overrides.harnessModel || harnessModels[harnessProvider],
      imageProvider,
      videoProvider,
      inpaintProvider,
      lipsyncProvider,
      speechProvider,
      audioProvider,
      falModel: overrides.imageModel && imageProvider === 'fal' ? overrides.imageModel : global.falModel,
      openaiImageModel: overrides.imageModel && imageProvider === 'openai-compatible' ? overrides.imageModel : global.openaiImageModel,
      replicateModel: overrides.imageModel && imageProvider === 'replicate' ? overrides.imageModel : global.replicateModel,
      runwareModel: overrides.imageModel && imageProvider === 'runware' ? overrides.imageModel : global.runwareModel,
      replicateImageEditModel: overrides.imageEditModel && imageProvider === 'replicate' ? overrides.imageEditModel : global.replicateImageEditModel,
      falImageEditModel: overrides.imageEditModel && imageProvider !== 'replicate' ? overrides.imageEditModel : global.falImageEditModel,
      replicateUpscaleModel: overrides.upscaleModel && imageProvider === 'replicate' ? overrides.upscaleModel : global.replicateUpscaleModel,
      falUpscaleModel: overrides.upscaleModel && imageProvider !== 'replicate' ? overrides.upscaleModel : global.falUpscaleModel,
      falInpaintModel: overrides.inpaintModel && inpaintProvider !== 'replicate' ? overrides.inpaintModel : global.falInpaintModel,
      replicateInpaintModel: overrides.inpaintModel && inpaintProvider === 'replicate' ? overrides.inpaintModel : global.replicateInpaintModel,
      falVideoModel: overrides.videoModel && videoProvider === 'fal' ? overrides.videoModel : global.falVideoModel,
      falVideoImageModel: overrides.videoImageModel && videoProvider === 'fal' ? overrides.videoImageModel : global.falVideoImageModel,
      falVideoUpscaleModel: overrides.videoUpscaleModel && videoProvider !== 'replicate' ? overrides.videoUpscaleModel : global.falVideoUpscaleModel,
      falVideoRestyleModel: overrides.videoRestyleModel && videoProvider !== 'replicate' ? overrides.videoRestyleModel : global.falVideoRestyleModel,
      falVideoReframeModel: overrides.videoReframeModel && videoProvider !== 'replicate' ? overrides.videoReframeModel : global.falVideoReframeModel,
      replicateVideoModel: overrides.videoModel && videoProvider === 'replicate' ? overrides.videoModel : global.replicateVideoModel,
      replicateVideoUpscaleModel: overrides.videoUpscaleModel && videoProvider === 'replicate' ? overrides.videoUpscaleModel : global.replicateVideoUpscaleModel,
      replicateVideoRestyleModel: overrides.videoRestyleModel && videoProvider === 'replicate' ? overrides.videoRestyleModel : global.replicateVideoRestyleModel,
      replicateVideoReframeModel: overrides.videoReframeModel && videoProvider === 'replicate' ? overrides.videoReframeModel : global.replicateVideoReframeModel,
      replicateVideoExtendModel: overrides.videoExtendModel && videoProvider === 'replicate' ? overrides.videoExtendModel : global.replicateVideoExtendModel,
      replicateVideoImageModel: overrides.videoImageModel && videoProvider === 'replicate' ? overrides.videoImageModel : global.replicateVideoImageModel,
      falVideoKeyframeModel: overrides.videoKeyframeModel || global.falVideoKeyframeModel,
      falVideoExtendModel: overrides.videoExtendModel && videoProvider !== 'replicate' ? overrides.videoExtendModel : global.falVideoExtendModel,
      falVideoMotionModel: overrides.videoMotionModel || global.falVideoMotionModel,
      falAudioModel: overrides.audioModel && speechProvider === 'fal' ? overrides.audioModel : global.falAudioModel,
      ollamaSpeechModel: overrides.audioModel && speechProvider === 'ollama' ? overrides.audioModel : global.ollamaSpeechModel,
      replicateSpeechModel: overrides.audioModel && speechProvider === 'replicate' ? overrides.audioModel : global.replicateSpeechModel,
      falAudioCloneModel: overrides.audioCloneModel || global.falAudioCloneModel,
      falSoundEffectsModel: overrides.soundEffectsModel && audioProvider !== 'replicate' ? overrides.soundEffectsModel : global.falSoundEffectsModel,
      falSoundEffectsVideoModel: overrides.soundEffectsVideoModel && audioProvider !== 'replicate' ? overrides.soundEffectsVideoModel : global.falSoundEffectsVideoModel,
      replicateSoundEffectsModel: overrides.soundEffectsModel && audioProvider === 'replicate' ? overrides.soundEffectsModel : global.replicateSoundEffectsModel,
      replicateSoundEffectsVideoModel: overrides.soundEffectsVideoModel && audioProvider === 'replicate' ? overrides.soundEffectsVideoModel : global.replicateSoundEffectsVideoModel,
      falVoiceDesignModel: overrides.voiceDesignModel || global.falVoiceDesignModel,
      falAudioExtendModel: overrides.audioExtendModel && audioProvider !== 'replicate' ? overrides.audioExtendModel : global.falAudioExtendModel,
      replicateAudioExtendModel: overrides.audioExtendModel && audioProvider === 'replicate' ? overrides.audioExtendModel : global.replicateAudioExtendModel,
      transcriptionProvider: effectiveTranscriptionProvider,
      falTranscribeModel: overrides.transcribeModel && effectiveTranscriptionProvider !== 'replicate' ? overrides.transcribeModel : global.falTranscribeModel,
      replicateTranscribeModel: overrides.transcribeModel && effectiveTranscriptionProvider === 'replicate' ? overrides.transcribeModel : global.replicateTranscribeModel,
      falLipsyncImageModel: overrides.lipsyncImageModel && lipsyncProvider !== 'replicate' ? overrides.lipsyncImageModel : global.falLipsyncImageModel,
      falLipsyncVideoModel: overrides.lipsyncVideoModel && lipsyncProvider !== 'replicate' ? overrides.lipsyncVideoModel : global.falLipsyncVideoModel,
      replicateLipsyncImageModel: overrides.lipsyncImageModel && lipsyncProvider === 'replicate' ? overrides.lipsyncImageModel : global.replicateLipsyncImageModel,
      replicateLipsyncVideoModel: overrides.lipsyncVideoModel && lipsyncProvider === 'replicate' ? overrides.lipsyncVideoModel : global.replicateLipsyncVideoModel,
      imageAspectRatio: overrides.imageAspectRatio || global.imageAspectRatio,
      imageSizePreset: overrides.imageSizePreset || global.imageSizePreset,
      imageSteps: overrides.imageSteps || global.imageSteps,
      videoDuration: overrides.videoDuration || global.videoDuration,
      videoAspectRatio: overrides.videoAspectRatio || global.videoAspectRatio,
      videoResolution: overrides.videoResolution || global.videoResolution,
      soundDuration: overrides.soundDuration || global.soundDuration,
      audioExtendDuration: overrides.audioExtendDuration || global.audioExtendDuration,
    };
    return next;
  }, [globalModelSelection, uiModelOverrides, primaryModels, harnessModels]);

  // The composer speaks the effective pair: while a primary override is
  // pinned, the picker shows it and edits rewrite the override instead of the
  // global config (the backend record wins either way).
  const conversationPrimaryOverride =
    uiModelOverrides && (uiModelOverrides.primaryProvider || uiModelOverrides.primaryModel) ? uiModelOverrides : null;
  const composerProvider: ChatProviderID = conversationPrimaryOverride?.primaryProvider === 'openrouter'
    || conversationPrimaryOverride?.primaryProvider === 'openai-compatible'
    || conversationPrimaryOverride?.primaryProvider === 'ollama'
    ? conversationPrimaryOverride.primaryProvider as ChatProviderID
    : primaryProvider;
  const composerModel = conversationPrimaryOverride?.primaryModel || primaryModels[composerProvider];
  const composerModelOptions = useMemo(
    () => chatModelOptionsFor(composerProvider, {modelOptions, openRouterModels, openaiCompatibleModels: openaiCompatibleModelOptions}),
    [composerProvider, modelOptions, openRouterModels, openaiCompatibleModelOptions],
  );
  const composerModelIsValid = composerModelOptions.some((option) => option.value === composerModel);

  // captureDetachedDraft reads the pending "New chat" state for a DETACHED
  // editor draft — the composer/comic ＋ actions on a brand-new conversation,
  // which has no id until its first send. The draft carries the same
  // turn-1 lifecycle a first send would pin (project, library scope,
  // workspace, model selection) and hands it back at its first save, which
  // gives birth to the host conversation. Read through a ref assigned after
  // the composer values are derived, so useEditors' open functions never
  // see a stale closure.
  const captureDetachedDraftRef = useRef<() => DetachedDraft>(() => ({
    projectID: '', libraryID: '', workspace: '', chatModel: '', chatSystem: '', modelOverrides: null,
  }));
  captureDetachedDraftRef.current = () => ({
    projectID: pendingProjectRef.current?.projectID ?? '',
    libraryID: pendingProjectRef.current?.libraryID ?? '',
    workspace: draftWorkspace,
    chatModel: composerModel,
    chatSystem: system,
    modelOverrides: draftModelOverrides,
  });

  const openConversationModels = () => {
    // With no conversation active this edits the PENDING override the next
    // conversation carries from its first message; otherwise the record's.
    setConvModelsDraft(conversationModelSelection);
    setConvModelsKeys(overrideKeysFromRecord(uiModelOverrides));
    setView('conversation-models');
  };

  // patchConversationModels applies a panel edit to the conversation draft.
  // A provider switch also carries the new provider's inherited global model
  // into the draft (so the paired picker displays it) and drops any model
  // pinned to the old provider — the patch argument drives the override keys,
  // the derived display values never do.
  const patchConversationModels = (patch: Partial<ModelSelectionValue>) => {
    const draftPatch: Partial<ModelSelectionValue> = {...patch};
    if (draftPatch.harnessProvider !== undefined && patch.harnessModel === undefined) {
      draftPatch.harnessModel = harnessModels[draftPatch.harnessProvider];
    }
    if (draftPatch.imageProvider !== undefined && patch.falModel === undefined && patch.openaiImageModel === undefined && patch.replicateModel === undefined && patch.runwareModel === undefined) {
      if (draftPatch.imageProvider === 'fal') {
        draftPatch.falModel = falModel;
      } else if (draftPatch.imageProvider === 'replicate') {
        draftPatch.replicateModel = replicateModel;
      } else if (draftPatch.imageProvider === 'runware') {
        draftPatch.runwareModel = runwareModel;
      } else {
        draftPatch.openaiImageModel = openaiCompatibleModel;
      }
    }
    // A video-provider switch carries the new backend's inherited models into
    // the draft so the paired pickers display them (the image-provider rule).
    if (draftPatch.videoProvider !== undefined && patch.falVideoModel === undefined && patch.replicateVideoModel === undefined) {
      if (draftPatch.videoProvider === 'replicate') {
        draftPatch.replicateVideoModel = replicateVideoModel;
        draftPatch.replicateVideoImageModel = replicateVideoImageModel;
        draftPatch.replicateVideoExtendModel = replicateVideoExtendModel;
      } else {
        draftPatch.falVideoModel = falVideoModel;
        draftPatch.falVideoImageModel = falVideoImageModel;
        draftPatch.falVideoExtendModel = falVideoExtendModel;
      }
    }
    // A transcription-provider switch carries the new backend's inherited
    // model into the draft so the picker displays it (the video pair's rule).
    if (draftPatch.transcriptionProvider !== undefined && patch.falTranscribeModel === undefined && patch.replicateTranscribeModel === undefined) {
      if (draftPatch.transcriptionProvider === 'replicate') {
        draftPatch.replicateTranscribeModel = replicateTranscribeModel;
      } else {
        draftPatch.falTranscribeModel = falTranscribeModel;
      }
    }
    // An audio-provider switch carries the new backend's inherited sound
    // models into the draft so the paired pickers display them (the video
    // pair's rule).
    if (draftPatch.audioProvider !== undefined && patch.falSoundEffectsModel === undefined && patch.replicateSoundEffectsModel === undefined) {
      if (draftPatch.audioProvider === 'replicate') {
        draftPatch.replicateSoundEffectsModel = replicateSoundEffectsModel;
        draftPatch.replicateSoundEffectsVideoModel = replicateSoundEffectsVideoModel;
        draftPatch.replicateAudioExtendModel = replicateAudioExtendModel;
      } else {
        draftPatch.falSoundEffectsModel = falSoundEffectsModel;
        draftPatch.falSoundEffectsVideoModel = falSoundEffectsVideoModel;
        draftPatch.falAudioExtendModel = falAudioExtendModel;
      }
    }
    if (draftPatch.primaryProvider !== undefined && patch.primaryModel === undefined) {
      draftPatch.primaryModel = primaryModels[draftPatch.primaryProvider];
    }
    setConvModelsDraft((prev) => (prev ? {...prev, ...draftPatch} : prev));
    setConvModelsKeys((prev) => {
      const next = new Set(prev);
      if (patch.primaryProvider !== undefined || patch.primaryModel !== undefined) next.add('primary');
      if (patch.harnessProvider !== undefined) {
        next.add('harnessProvider');
        next.delete('harnessModel');
      }
      if (patch.harnessModel !== undefined) next.add('harnessModel');
      if (patch.imageProvider !== undefined) {
        next.add('imageProvider');
        next.delete('imageModel');
      }
      if (patch.falModel !== undefined || patch.openaiImageModel !== undefined || patch.replicateModel !== undefined || patch.runwareModel !== undefined) next.add('imageModel');
      if (patch.videoProvider !== undefined) {
        next.add('videoProvider');
        next.delete('videoModel');
        next.delete('videoImageModel');
      }
      if (patch.falImageEditModel !== undefined || patch.replicateImageEditModel !== undefined) next.add('imageEditModel');
      if (patch.falUpscaleModel !== undefined || patch.replicateUpscaleModel !== undefined) next.add('upscaleModel');
      if (patch.inpaintProvider !== undefined) {
        next.add('inpaintProvider');
        next.delete('inpaintModel');
      }
      if (patch.falInpaintModel !== undefined || patch.replicateInpaintModel !== undefined) next.add('inpaintModel');
      if (patch.falVideoModel !== undefined || patch.replicateVideoModel !== undefined) next.add('videoModel');
      if (patch.falVideoImageModel !== undefined || patch.replicateVideoImageModel !== undefined) next.add('videoImageModel');
      if (patch.falVideoKeyframeModel !== undefined) next.add('videoKeyframeModel');
      if (patch.falVideoExtendModel !== undefined || patch.replicateVideoExtendModel !== undefined) next.add('videoExtendModel');
      if (patch.falVideoMotionModel !== undefined) next.add('videoMotionModel');
      if (patch.falVideoUpscaleModel !== undefined || patch.replicateVideoUpscaleModel !== undefined) next.add('videoUpscaleModel');
      if (patch.falVideoReframeModel !== undefined || patch.replicateVideoReframeModel !== undefined) next.add('videoReframeModel');
      if (patch.falVideoRestyleModel !== undefined || patch.replicateVideoRestyleModel !== undefined) next.add('videoRestyleModel');
      if (patch.speechProvider !== undefined) {
        // The imageProvider recipe: the stored speech model belongs to the
        // previous provider's slot, so a provider change drops it.
        next.add('speechProvider');
        next.delete('audioModel');
      }
      if (patch.falAudioModel !== undefined || patch.ollamaSpeechModel !== undefined || patch.replicateSpeechModel !== undefined) next.add('audioModel');
      if (patch.falAudioCloneModel !== undefined) next.add('audioCloneModel');
      if (patch.audioProvider !== undefined) {
        // The speechProvider recipe: the stored sound models belong to the
        // previous provider's slots, so a provider change drops them.
        next.add('audioProvider');
        next.delete('soundEffectsModel');
        next.delete('soundEffectsVideoModel');
      }
      if (patch.falSoundEffectsModel !== undefined || patch.replicateSoundEffectsModel !== undefined) next.add('soundEffectsModel');
      if (patch.falSoundEffectsVideoModel !== undefined || patch.replicateSoundEffectsVideoModel !== undefined) next.add('soundEffectsVideoModel');
      if (patch.falAudioExtendModel !== undefined || patch.replicateAudioExtendModel !== undefined) next.add('audioExtendModel');
      if (patch.falVoiceDesignModel !== undefined) next.add('voiceDesignModel');
      if (patch.falAudioExtendModel !== undefined) next.add('audioExtendModel');
      if (patch.falTranscribeModel !== undefined || patch.replicateTranscribeModel !== undefined) next.add('transcribeModel');
      if (patch.transcriptionProvider !== undefined) next.add('transcriptionProvider');
      if (patch.imageAspectRatio !== undefined) next.add('imageAspectRatio');
      if (patch.imageSizePreset !== undefined) next.add('imageSizePreset');
      if (patch.imageSteps !== undefined) next.add('imageSteps');
      if (patch.videoDuration !== undefined) next.add('videoDuration');
      if (patch.videoAspectRatio !== undefined) next.add('videoAspectRatio');
      if (patch.videoResolution !== undefined) next.add('videoResolution');
      if (patch.soundDuration !== undefined) next.add('soundDuration');
      if (patch.audioExtendDuration !== undefined) next.add('audioExtendDuration');
      if (patch.falLipsyncImageModel !== undefined) next.add('lipsyncImageModel');
      if (patch.falLipsyncVideoModel !== undefined) next.add('lipsyncVideoModel');
      if (patch.lipsyncProvider !== undefined) next.add('lipsyncProvider');
      if (patch.replicateLipsyncImageModel !== undefined) next.add('lipsyncImageModel');
      if (patch.replicateLipsyncVideoModel !== undefined) next.add('lipsyncVideoModel');
      return next;
    });
  };

  const resetConversationModelField = (key: string) => {
    setConvModelsKeys((prev) => {
      const next = new Set(prev);
      next.delete(key);
      return next;
    });
    setConvModelsDraft((prev) => {
      if (!prev) {
        return prev;
      }
      const global = globalModelSelection;
      const next = {...prev};
      switch (key) {
        case 'primary':
          next.primaryProvider = global.primaryProvider;
          next.primaryModel = primaryModels[global.primaryProvider];
          break;
        case 'harnessProvider':
          next.harnessProvider = global.harnessProvider;
          break;
        case 'harnessModel':
          next.harnessModel = harnessModels[next.harnessProvider];
          break;
        case 'imageProvider':
          next.imageProvider = global.imageProvider;
          break;
        case 'imageModel':
          if (next.imageProvider === 'fal') {
            next.falModel = global.falModel;
          } else if (next.imageProvider === 'replicate') {
            next.replicateModel = global.replicateModel;
          } else if (next.imageProvider === 'runware') {
            next.runwareModel = global.runwareModel;
          } else {
            next.openaiImageModel = global.openaiImageModel;
          }
          break;
        case 'videoProvider':
          next.videoProvider = global.videoProvider;
          break;
        case 'imageEditModel':
          next.falImageEditModel = global.falImageEditModel;
          next.replicateImageEditModel = global.replicateImageEditModel;
          break;
        case 'upscaleModel':
          next.falUpscaleModel = global.falUpscaleModel;
          next.replicateUpscaleModel = global.replicateUpscaleModel;
          break;
        case 'inpaintProvider':
          next.inpaintProvider = global.inpaintProvider;
          break;
        case 'inpaintModel':
          next.falInpaintModel = global.falInpaintModel;
          next.replicateInpaintModel = global.replicateInpaintModel;
          break;
        case 'videoModel': next.falVideoModel = global.falVideoModel; next.replicateVideoModel = global.replicateVideoModel; break;
        case 'videoImageModel': next.falVideoImageModel = global.falVideoImageModel; next.replicateVideoImageModel = global.replicateVideoImageModel; break;
        case 'videoKeyframeModel': next.falVideoKeyframeModel = global.falVideoKeyframeModel; break;
        case 'videoExtendModel':
          next.falVideoExtendModel = global.falVideoExtendModel;
          next.replicateVideoExtendModel = global.replicateVideoExtendModel;
          break;
        case 'videoMotionModel': next.falVideoMotionModel = global.falVideoMotionModel; break;
        case 'videoUpscaleModel':
          next.falVideoUpscaleModel = global.falVideoUpscaleModel;
          next.replicateVideoUpscaleModel = global.replicateVideoUpscaleModel;
          break;
        case 'videoReframeModel':
          next.falVideoReframeModel = global.falVideoReframeModel;
          next.replicateVideoReframeModel = global.replicateVideoReframeModel;
          break;
        case 'videoRestyleModel':
          next.falVideoRestyleModel = global.falVideoRestyleModel;
          next.replicateVideoRestyleModel = global.replicateVideoRestyleModel;
          break;
        case 'audioModel':
          next.falAudioModel = global.falAudioModel;
          next.ollamaSpeechModel = global.ollamaSpeechModel;
          next.replicateSpeechModel = global.replicateSpeechModel;
          break;
        case 'speechProvider': next.speechProvider = global.speechProvider; break;
        case 'audioProvider': next.audioProvider = global.audioProvider; break;
        case 'audioCloneModel': next.falAudioCloneModel = global.falAudioCloneModel; break;
        case 'soundEffectsModel':
          next.falSoundEffectsModel = global.falSoundEffectsModel;
          next.replicateSoundEffectsModel = global.replicateSoundEffectsModel;
          break;
        case 'soundEffectsVideoModel':
          next.falSoundEffectsVideoModel = global.falSoundEffectsVideoModel;
          next.replicateSoundEffectsVideoModel = global.replicateSoundEffectsVideoModel;
          break;
        case 'voiceDesignModel': next.falVoiceDesignModel = global.falVoiceDesignModel; break;
        case 'audioExtendModel':
          next.falAudioExtendModel = global.falAudioExtendModel;
          next.replicateAudioExtendModel = global.replicateAudioExtendModel;
          break;
        case 'transcribeModel':
          next.falTranscribeModel = global.falTranscribeModel;
          next.replicateTranscribeModel = global.replicateTranscribeModel;
          break;
        case 'transcriptionProvider': next.transcriptionProvider = global.transcriptionProvider; break;
        case 'imageAspectRatio': next.imageAspectRatio = global.imageAspectRatio; break;
        case 'imageSizePreset': next.imageSizePreset = global.imageSizePreset; break;
        case 'imageSteps': next.imageSteps = global.imageSteps; break;
        case 'videoDuration': next.videoDuration = global.videoDuration; break;
        case 'videoAspectRatio': next.videoAspectRatio = global.videoAspectRatio; break;
        case 'videoResolution': next.videoResolution = global.videoResolution; break;
        case 'soundDuration': next.soundDuration = global.soundDuration; break;
        case 'audioExtendDuration': next.audioExtendDuration = global.audioExtendDuration; break;
        case 'lipsyncProvider': next.lipsyncProvider = global.lipsyncProvider; break;
        case 'lipsyncImageModel':
          next.falLipsyncImageModel = global.falLipsyncImageModel;
          next.replicateLipsyncImageModel = global.replicateLipsyncImageModel;
          break;
        case 'lipsyncVideoModel':
          next.falLipsyncVideoModel = global.falLipsyncVideoModel;
          next.replicateLipsyncVideoModel = global.replicateLipsyncVideoModel;
          break;
      }
      return next;
    });
  };

  const resetAllConversationModels = () => {
    setConvModelsKeys(new Set());
    setConvModelsDraft(globalModelSelection);
  };

  // closeConversationModels leaves the screen with a synchronous flush: the
  // debounced save below would otherwise be cancelled by the view change and
  // silently drop an edit made within the debounce window. Every override
  // write toasts its failure under one key (the flush, the debounced save,
  // and the composer's primary pin), and the next successful save dismisses
  // it.
  const closeConversationModels = () => {
    if (convModelsDraft && activeConversationID) {
      SetConversationModelOverrides(activeConversationID, conversationOverridesPayload(convModelsDraft, convModelsKeys))
        .then((summary) => {
          setConversations((prev) => prev.map((item) => (item.id === summary.id ? main.ConversationSummary.createFrom({...item, modelOverrides: summary.modelOverrides ?? undefined}) : item)));
          notifications.dismissKey('conv-models');
        })
        .catch((error) => notifyError("Couldn't save the conversation's models", error, {key: 'conv-models'}));
    }
    setView('app');
  };

  // Write the conversation draft through, debounced like the Settings
  // auto-save. With a conversation open it goes to the record via the
  // mutator — the returned summary keeps the local conversation list
  // current, so the toolbar badge and the composer pick up the override
  // without a refetch. With NO conversation yet (pending mode) the draft IS
  // the storage: it lands in draftModelOverrides and travels with the first
  // message (ChatRequest.modelOverrides), so no backend call happens.
  useEffect(() => {
    if (view !== 'conversation-models' || !convModelsDraft) {
      return;
    }
    if (!activeConversationID) {
      setDraftModelOverrides(convModelsKeys.size ? conversationOverridesPayload(convModelsDraft, convModelsKeys) : null);
      return;
    }
    const timeout = window.setTimeout(() => {
      SetConversationModelOverrides(activeConversationID, conversationOverridesPayload(convModelsDraft, convModelsKeys))
        .then((summary) => {
          setConversations((prev) => prev.map((item) => (item.id === summary.id ? main.ConversationSummary.createFrom({...item, modelOverrides: summary.modelOverrides ?? undefined}) : item)));
          notifications.dismissKey('conv-models');
        })
        .catch((error) => notifyError("Couldn't save the conversation's models", error, {key: 'conv-models'}));
    }, 300);
    return () => window.clearTimeout(timeout);
  }, [view, convModelsDraft, convModelsKeys, activeConversationID]);

  // The per-model duration selects in the conversation screen must follow the
  // DRAFT's video models (which may be overridden), not the global ones. Same
  // fetch-and-fallback as the global pickers; the current draft value is kept
  // selectable at render time even when the model's schema doesn't list it,
  // so the select never silently shows a value the conversation won't use.
  const [convDurationOptions, setConvDurationOptions] = useState<{video: string[]; image: string[]; keyframe: string[]; extend: string[]; sound: string[]}>({
    video: defaultVideoDurationOptions,
    image: defaultVideoDurationOptions,
    keyframe: defaultVideoDurationOptions,
    extend: defaultVideoDurationOptions,
    sound: defaultSoundDurationOptions,
  });
  const convDraftVideoModel = convModelsDraft?.falVideoModel ?? '';
  const convDraftVideoImageModel = convModelsDraft?.falVideoImageModel ?? '';
  const convDraftVideoKeyframeModel = convModelsDraft?.falVideoKeyframeModel ?? '';
  const convDraftVideoProvider = convModelsDraft?.videoProvider ?? videoProvider;
  // Extend routes by the draft's video provider like the t2v/i2v pair;
  // keyframes stay fal-only.
  const convDraftVideoExtendModel = convDraftVideoProvider === 'replicate'
    ? (convModelsDraft?.replicateVideoExtendModel ?? '')
    : (convModelsDraft?.falVideoExtendModel ?? '');
  const convDraftSoundEffectsModel = convModelsDraft?.falSoundEffectsModel ?? '';
  useEffect(() => {
    if (view !== 'conversation-models') {
      return;
    }
    let cancelled = false;
    // The t2v/i2v/extend duration lookups follow the draft's video provider
    // (the replicate backend reads its own schema cache); keyframes are a
    // fal-only tool and always read fal's.
    const fetchOptions = async (model: string, provider: 'fal' | 'replicate') => {
      const fetcher = provider === 'replicate' ? ListReplicateVideoDurations : ListFalVideoDurations;
      try {
        const durations = await fetcher(model);
        return durations && durations.length ? durations : defaultVideoDurationOptions;
      } catch {
        return defaultVideoDurationOptions;
      }
    };
    const fetchSoundOptions = async () => {
      // The duration lookup reads the selected model's published schema, a
      // fal binding — a Replicate-pinned draft falls back to the generic
      // ladder (the replicate resolver clamps or drops an unsupported length
      // with a notice at call time).
      if (convModelsDraft?.audioProvider === 'replicate') {
        return defaultSoundDurationOptions;
      }
      try {
        const durations = await ListFalSoundEffectDurations(convDraftSoundEffectsModel);
        const schemaOpts = durations && durations.length ? durations : defaultSoundDurationOptions;
        return schemaOpts.includes('auto') ? schemaOpts : ['auto', ...schemaOpts];
      } catch {
        return defaultSoundDurationOptions;
      }
    };
    Promise.all([
      fetchOptions(convDraftVideoProvider === 'replicate' ? (convModelsDraft?.replicateVideoModel ?? '') : convDraftVideoModel, convDraftVideoProvider),
      fetchOptions(convDraftVideoProvider === 'replicate' ? (convModelsDraft?.replicateVideoImageModel ?? '') : convDraftVideoImageModel, convDraftVideoProvider),
      fetchOptions(convDraftVideoKeyframeModel, 'fal'),
      fetchOptions(convDraftVideoExtendModel, convDraftVideoProvider),
      fetchSoundOptions(),
    ])
      .then(([video, image, keyframe, extend, sound]) => {
        if (!cancelled) {
          setConvDurationOptions({video, image, keyframe, extend, sound});
        }
      });
    return () => {
      cancelled = true;
    };
  }, [view, videoProvider, convDraftVideoProvider, convModelsDraft, convDraftVideoModel, convDraftVideoImageModel, convDraftVideoKeyframeModel, convDraftVideoExtendModel, convDraftSoundEffectsModel]);

  const saveComposerPrimaryOverride = async (provider: ChatProviderID, modelValue: string) => {
    if (!activeConversationID) {
      // No conversation yet: the pair pins onto the pending override the
      // first message will carry — no record exists to write.
      const pendingKeys = new Set(overrideKeysFromRecord(draftModelOverrides));
      pendingKeys.add('primary');
      setDraftModelOverrides(conversationOverridesPayload(
        {...conversationModelSelection, primaryProvider: provider, primaryModel: modelValue},
        pendingKeys,
      ));
      return;
    }
    const keys = new Set(overrideKeysFromRecord(conversationOverrides));
    keys.add('primary');
    const payload = conversationOverridesPayload(
      {...conversationModelSelection, primaryProvider: provider, primaryModel: modelValue},
      keys,
    );
    try {
      const summary = await SetConversationModelOverrides(activeConversationID, payload);
      setConversations((prev) => prev.map((item) => (item.id === summary.id ? main.ConversationSummary.createFrom({...item, modelOverrides: summary.modelOverrides ?? undefined}) : item)));
      notifications.dismissKey('conv-models');
    } catch (error) {
      notifyError("Couldn't save the conversation's models", error, {key: 'conv-models'});
    }
  };

  const selectComposerProvider = (next: ChatProviderID) => {
    if (conversationPrimaryOverride) {
      saveComposerPrimaryOverride(next, primaryModels[next] || composerModel);
      return;
    }
    setPrimaryProvider(next);
  };

  const selectComposerModel = (next: string) => {
    if (conversationPrimaryOverride) {
      saveComposerPrimaryOverride(composerProvider, next);
      return;
    }
    setModel(next);
  };

  // modelSelectionCatalogs feeds ModelSelectionPanel's pickers: the fetched
  // model lists plus the key/detection state. One object serves both the
  // Settings Models tab and the conversation-models screen.
  const modelSelectionCatalogs = {
    models,
    modelOptions,
    openRouterModels,
    openaiCompatibleModels: openaiCompatibleModelOptions,
    falModels,
    falImageEditModels,
    replicateModels,
    runwareModels,
    replicateImageEditModels,
    replicateUpscaleModels,
    replicateVideoUpscaleModels,
    replicateVideoRestyleModels,
    replicateVideoReframeModels,
    replicateVideoExtendModels,
    replicateVideoModels,
    replicateVideoImageModels,
    replicateTranscribeModels,
    replicateLipsyncImageModels,
    replicateLipsyncVideoModels,
    replicateSoundEffectsModels,
    replicateSoundEffectsVideoModels,
    replicateAudioExtendModels,
    replicateSpeechModels,
    falVideoModels,
    falVideoImageModels,
    falVideoKeyframeModels,
    falVideoExtendModels,
    falVideoMotionModels,
    falVideoUpscaleModels,
    falVideoReframeModels,
    falVideoRestyleModels,
    falAudioModels,
    falSoundEffectModels,
    falSoundEffectsVideoModels,
    falAudioExtendModels,
    falVoiceDesignModels,
    falTranscribeModels,
    falUpscaleModels,
    falLipsyncImageModels,
    falLipsyncVideoModels,
    openRouterHasKey,
    falHasKey,
    replicateHasKey,
    runwareHasKey,
    whisperStatus,
    openCapabilityID,
    setOpenCapabilityID,
  };
  // Called here, not beside the other Settings hooks, to keep its loader
  // effects in their original place in the mount order (they reconcile the
  // selected durations as the ladders land).
  const {
    soundDurationOptions,
    videoDurationExtendOptions,
    videoDurationImageOptions,
    videoDurationKeyframeOptions,
    videoDurationOptions,
  } = useDurationOptions({videoProvider, falVideoModel, replicateVideoModel, setVideoDuration, audioProvider, falSoundEffectsModel, setSoundDuration, falVideoImageModel, replicateVideoImageModel, falVideoKeyframeModel, falVideoExtendModel, replicateVideoExtendModel});
  useEffect(() => {
    // Only re-run when the option list itself changes (provider switch, or
    // the OpenRouter list finishing a load) — not on every keystroke of
    // `model`, since the primary-model field is now free-text (filterable)
    // and is expected to be transiently "invalid" while the user is typing.
    if (!primaryModelOptions.length) {
      return;
    }
    setModel((current) => (primaryModelOptions.some((option) => option.value === current) ? current : primaryModelOptions[0].value));
  }, [primaryModelOptions]);

  useEffect(() => {
    const needsCatalog = primaryProvider === 'openrouter' || harnessProvider === 'openrouter';
    if (needsCatalog && openRouterHasKey && openRouterModels.length === 0 && openRouterStatus !== 'error') {
      refreshOpenRouterModels();
    }
    const needsLocalCatalog = primaryProvider === 'openai-compatible' || harnessProvider === 'openai-compatible';
    if (needsLocalCatalog && openaiCompatibleModels.length === 0 && openaiCompatibleStatus !== 'error') {
      refreshOpenAICompatibleModels();
    }
  }, [primaryProvider, harnessProvider, openRouterHasKey, openRouterModels.length, openRouterStatus, openaiCompatibleModels.length, openaiCompatibleStatus]);

  async function loadConfig() {
    const config = await GetConfig();
    setStartupError('');
    const next = applyConfig(config);
    await Promise.all([
      refreshConversations(),
      refreshOllama(next.baseURL),
      // Every provider with a stored key gets the same startup pair: a catalog
      // refresh (discovery aid, errors swallowed) and a cheap authenticated key
      // ping that sets the Settings status light — so all three lights mean the
      // same thing ("last key validation") instead of OpenRouter's meaning
      // "public model list reachable".
      HasOpenRouterAPIKey().then((hasKey) => {
        setOpenRouterHasKey(hasKey);
        if (hasKey) {
          refreshOpenRouterModels();
          checkOpenRouterConnection();
        }
      }).catch(() => setOpenRouterHasKey(false)),
      HasFalAPIKey().then((hasKey) => {
        setFalHasKey(hasKey);
        if (hasKey) {
          refreshFalModels();
          checkFalConnection();
        }
      }).catch(() => setFalHasKey(false)),
      HasReplicateAPIKey().then((hasKey) => {
        setReplicateHasKey(hasKey);
        if (hasKey) {
          refreshReplicateModels();
          checkReplicateConnection();
        }
      }).catch(() => setReplicateHasKey(false)),
      loadRunwareKeyState(),
      refreshRunwareModels(),
      DetectLocalTools(new main.LocalToolOverrides()).then((report) => {
        setLocalToolsReport(report);
        // An unset provider auto-resolves in Go (fal when a key exists, else a
        // detected local whisper). Adopt that resolution here so the dropdown
        // shows — and the next save persists — what the backend would do.
        if (!config.models?.transcriptionProvider &&
            (report.transcriptionProvider === 'fal' || report.transcriptionProvider === 'local-whisper' || report.transcriptionProvider === 'replicate')) {
          setTranscriptionProvider(report.transcriptionProvider);
        }
      }).catch(() => setLocalToolsReport(null)),
      HasOpenAICompatibleAPIKey().then((hasKey) => {
        setOpenaiCompatibleHasKey(hasKey);
      }).catch(() => setOpenaiCompatibleHasKey(false)),
      refreshOpenAICompatibleModels(next.openaiCompatibleBaseURL),
    ]);
  }

  // chooseDraftWorkspace is the per-conversation picker for a NEW chat. It
  // reuses the same native dialog as the Settings default picker, but writes
  // the result into draftWorkspace (the per-conversation override) rather than
  // the global toolConfig. After the first send the workspace is immutable and
  // this handler is no longer reachable from the UI.
  async function chooseDraftWorkspace() {
    try {
      const seed = draftWorkspace || (toolConfig?.filesystem?.root ?? '');
      const selected = await ChooseToolWorkspace(seed);
      if (!selected) {
        return;
      }
      setDraftWorkspace(selected);
    } catch (error) {
      notifyError("Couldn't choose the workspace folder", error);
    }
  }

  // activeConversationWorkspace is the immutable workspace of the conversation
  // currently in view, looked up from the conversation list. Empty for a new
  // chat (where draftWorkspace takes over) or for legacy conversations without
  // one (the UI falls back to the configured default).
  const activeConversationWorkspace = activeConversationID
    ? conversations.find((item) => item.id === activeConversationID)?.workspace ?? ''
    : '';

  const displayedWorkspace = activeConversationID
    ? (activeConversationWorkspace || defaultWorkspaceRoot)
    : (draftWorkspace || defaultWorkspaceRoot);

  async function refreshOllama(endpoint = baseURL) {
    setRefreshing(true);
    try {
      const nextStatus = await CheckOllama(endpoint);
      setStatus(nextStatus);
      if (!nextStatus.online) {
        setModels([]);
        return;
      }
      const nextModels = asArray(await ListModels(endpoint));
      setModels(nextModels);
      const firstModel = nextModels[0]?.name ?? '';
      setPrimaryModels((current) => (current.ollama ? current : {...current, ollama: firstModel}));
      // Target the Ollama slot explicitly: this default comes from the Ollama
      // catalog and must not land in the OpenRouter slot when that provider is
      // the active one.
      setHarnessModels((current) => (current.ollama ? current : {...current, ollama: firstModel}));
    } finally {
      setRefreshing(false);
    }
  }

  async function saveOpenRouterKey() {
    try {
      await SaveOpenRouterAPIKey(openRouterAPIKeyInput);
      setOpenRouterAPIKeyInput('');
      const hasKey = await HasOpenRouterAPIKey();
      setOpenRouterHasKey(hasKey);
      setOpenRouterStatus('unknown');
      setOpenRouterError('');
      if (hasKey) {
        await refreshOpenRouterModels();
        await checkOpenRouterConnection();
      }
    } catch (error) {
      setOpenRouterStatus('error');
      setOpenRouterError(formatError(error));
    }
  }

  async function clearOpenRouterKey() {
    try {
      await SaveOpenRouterAPIKey('');
      setOpenRouterHasKey(false);
      setOpenRouterModels([]);
      setOpenRouterStatus('unknown');
      setOpenRouterError('');
      setPrimaryProvider((current) => current === 'openrouter' ? 'ollama' : current);
      // The harness cannot reach OpenRouter without a key either, and an
      // unreachable harness now fails the turn up front rather than degrading.
      setHarnessProvider((current) => current === 'openrouter' ? 'ollama' : current);
    } catch (error) {
      setOpenRouterStatus('error');
      setOpenRouterError(formatError(error));
    }
  }

  async function saveFalKey() {
    try {
      await SaveFalAPIKey(falAPIKeyInput);
      setFalAPIKeyInput('');
      const hasKey = await HasFalAPIKey();
      setFalHasKey(hasKey);
      setFalStatus('unknown');
      setFalError('');
      notifications.dismissKey('key-fal');
      if (hasKey) {
        await refreshFalModels();
        await checkFalConnection();
      }
    } catch (error) {
      notifyError("Couldn't save the fal.ai key", error, {key: 'key-fal'});
    }
  }

  async function clearFalKey() {
    try {
      await SaveFalAPIKey('');
      setFalHasKey(false);
      clearFalCatalogs();
      setFalStatus('unknown');
      setFalError('');
      notifications.dismissKey('key-fal');
      setImageProvider((current) => current === 'fal' ? 'openai-compatible' : current);
      setVideoProvider((current) => current === 'fal' ? 'replicate' : current);
    } catch (error) {
      notifyError("Couldn't remove the fal.ai key", error, {key: 'key-fal'});
    }
  }

  async function saveReplicateKey() {
    try {
      await SaveReplicateAPIKey(replicateAPIKeyInput);
      setReplicateAPIKeyInput('');
      const hasKey = await HasReplicateAPIKey();
      setReplicateHasKey(hasKey);
      setReplicateStatus('unknown');
      setReplicateError('');
      notifications.dismissKey('key-replicate');
      if (hasKey) {
        await refreshReplicateModels();
        await checkReplicateConnection();
      }
    } catch (error) {
      notifyError("Couldn't save the Replicate key", error, {key: 'key-replicate'});
    }
  }

  async function clearReplicateKey() {
    try {
      await SaveReplicateAPIKey('');
      setReplicateHasKey(false);
      clearReplicateCatalogs();
      setReplicateStatus('unknown');
      setReplicateError('');
      notifications.dismissKey('key-replicate');
      setImageProvider((current) => current === 'replicate' ? 'openai-compatible' : current);
      setVideoProvider((current) => current === 'replicate' ? 'fal' : current);
      setLipsyncProvider((current) => current === 'replicate' ? 'fal' : current);
    } catch (error) {
      notifyError("Couldn't remove the Replicate key", error, {key: 'key-replicate'});
    }
  }

  // The OpenAI-compatible image server list is a discovery aid like fal's
  // catalog: a server without /v1/models only loses the suggestions, never the
  // free-text model field. The status feeds the Providers tab endpoint row.
  async function refreshOpenAICompatibleModels(endpoint = openaiCompatibleBaseURL) {
    try {
      const ids = await ListOpenAICompatibleModels(endpoint);
      setOpenaiCompatibleModels(asArray(ids));
      setOpenaiCompatibleStatus('ok');
    } catch {
      setOpenaiCompatibleModels([]);
      setOpenaiCompatibleStatus('error');
    }
  }

  useEffect(() => {
    activeConversationIDRef.current = activeConversationID;
  }, [activeConversationID]);
  useEffect(() => {
    openEditorSessionsRef.current = {
      onScreen: view === 'app',
      image: editorHandle ? editorHandle.sessionID ?? '' : undefined,
      video: videoEditorHandle ? videoEditorHandle.sessionID ?? '' : undefined,
      audio: audioEditorHandle ? audioEditorHandle.sessionID ?? '' : undefined,
    };
  }, [view, editorHandle, videoEditorHandle, audioEditorHandle]);

  async function resetWorkspace() {
    stashCurrentDraft();
    visibleStreamRef.current = null;
    setActiveStream(null);
    setChat([]);
    setCollapsedThinkingIDs({});
    restoreDraftFor('');
    setActiveConversationID('');
    setDraftWorkspace('');
    // A blank composer is standalone unless the caller (startNewChatInProject)
    // immediately re-stashes a project context for this composition.
    setPendingProject(null);
    setView('app');
    window.setTimeout(() => {
      chatPromptRef.current?.focus();
    }, 0);
  }

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.repeat || event.altKey || event.shiftKey) {
        return;
      }
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'n') {
        event.preventDefault();
        // Same context-aware flow as File → New Conversation: the native menu
        // accelerator usually intercepts ⌘N first; this web-side listener
        // covers the case where it doesn't (both paths are idempotent).
        newConversationActionRef.current();
      }
      if ((event.metaKey || event.ctrlKey) && event.key === ',') {
        event.preventDefault();
        void openSettings();
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [activeStream, videoEditorHandle]);

  async function startNewChat() {
    if (!(await confirmLeaveEditor())) return;
    // Starting a new conversation leaves the editor — an unsaved draft has no
    // conversation to return to.
    closeImageEditor();
    await resetWorkspace();
  }

  // editArtifactIDFromURL extracts an img_<hex> artifact id from a hydrated
  // /atelier-artifact URL. Only persisted images are editable — data: URLs
  // (the live transcript's inline form) parse to null and hide the Edit
  // affordances rather than guessing identity.
  function editArtifactIDFromURL(url: string): string | null {
    const prefix = '/atelier-artifact/';
    if (!url || !url.startsWith(prefix)) {
      return null;
    }
    const file = url.slice(prefix.length).split(/[?#]/)[0].split('/').pop() ?? '';
    const stem = file.replace(/\.[^.]+$/, '');
    return stem.startsWith('img_') ? stem : null;
  }

  function videoArtifactIDFromURL(url: string): string | null {
    if (!url.startsWith('/atelier-artifact/')) return null;
    const filename = url.split(/[?#]/)[0].split('/').pop() || '';
    const stem = filename.replace(/\.[^.]+$/, '');
    return /^vid_[a-zA-Z0-9]+$/.test(stem) ? stem : null;
  }

  function audioArtifactIDFromURL(url: string): string | null {
    if (!url.startsWith('/atelier-artifact/')) return null;
    const filename = url.split(/[?#]/)[0].split('/').pop() || '';
    const stem = filename.replace(/\.[^.]+$/, '');
    return /^aud_[a-zA-Z0-9]+$/.test(stem) ? stem : null;
  }

  // transcriptArtifactIDFromURL matches subtitle artifacts only — trc_ stems
  // with a .vtt/.srt extension. Plain .txt transcripts are not editor
  // material, mirroring the assets layer's exclusion.
  function transcriptArtifactIDFromURL(url: string): string | null {
    if (!url.startsWith('/atelier-artifact/')) return null;
    const path = url.split(/[?#]/)[0];
    const filename = path.split('/').pop() || '';
    const stem = filename.replace(/\.[^.]+$/, '');
    if (!/\.(vtt|srt)$/i.test(filename)) return null;
    return /^trc_[a-zA-Z0-9]+$/.test(stem) ? stem : null;
  }

  // markdownArtifactIDFromURL matches document artifacts only — md_ stems
  // with a .md or .fountain extension (the two document dialects).
  function markdownArtifactIDFromURL(url: string): string | null {
    if (!url.startsWith('/atelier-artifact/')) return null;
    const path = url.split(/[?#]/)[0];
    const filename = path.split('/').pop() || '';
    const stem = filename.replace(/\.[^.]+$/, '');
    if (!/\.(md|fountain)$/i.test(filename)) return null;
    return /^md_[a-zA-Z0-9]+$/.test(stem) ? stem : null;
  }

  // compositionArtifactIDFromURL matches project documents only — cmp_ stems
  // with a .json extension, the video composer's saved revisions.
  function compositionArtifactIDFromURL(url: string): string | null {
    if (!url.startsWith('/atelier-artifact/')) return null;
    const path = url.split(/[?#]/)[0];
    const filename = path.split('/').pop() || '';
    const stem = filename.replace(/\.[^.]+$/, '');
    if (!/\.json$/i.test(filename)) return null;
    return /^cmp_[a-zA-Z0-9]+$/.test(stem) ? stem : null;
  }

  // comicArtifactIDFromURL matches comic documents only — cbc_ stems with a
  // .json extension, the comic editor's saved revisions.
  function comicArtifactIDFromURL(url: string): string | null {
    if (!url.startsWith('/atelier-artifact/')) return null;
    const path = url.split(/[?#]/)[0];
    const filename = path.split('/').pop() || '';
    const stem = filename.replace(/\.[^.]+$/, '');
    if (!/\.json$/i.test(filename)) return null;
    return /^cbc_[a-zA-Z0-9]+$/.test(stem) ? stem : null;
  }

  function openSettings() {
    // Settings overlays the editors (hidden, not unmounted) so Back returns to
    // the exact edit in progress — uncommitted tweaks included — and there is
    // nothing to discard-confirm.
    setView('settings');
  }

  // refreshAfterConversationWrite re-reads everything that shows a
  // conversation after something wrote into it from outside the chat stream
  // (an editor save, a finished export): the sidebar list, the assets panel,
  // and the transcript when that conversation is the one on screen.
  function refreshAfterConversationWrite(conversationID: string) {
    void refreshConversations();
    setAssetsRefreshTick((tick) => tick + 1);
    if (activeConversationIDRef.current === conversationID) {
      void GetConversation(conversationID).then((detail) => {
        if (activeConversationIDRef.current === conversationID) hydrateChatConversation(detail);
      }).catch((error) => notifyError("Couldn't refresh the conversation", error));
    }
  }

  // adoptEditorConversation lands the conversation a DETACHED composer or
  // comic draft's first save just gave birth to — the editor twin of a first
  // chat send's reveal. When the user is still on the new chat the draft
  // opened from, that chat BECOMES the new conversation: its unsent composer
  // text moves in, it turns active beneath the editor (so closing the editor
  // lands on it), and the pending project and model override are spent — the
  // record carries them now. The sidebar reveals the row where it was filed
  // either way; a user who has since moved to another conversation keeps it
  // on screen.
  async function adoptEditorConversation(conversationID: string, draft: DetachedDraft | undefined) {
    const fromNewChat = activeConversationIDRef.current === '';
    if (fromNewChat) {
      // Hydration stashes the live composer under the new-chat key and then
      // restores from the conversation's key, so the unsent draft is copied
      // to its new home first and dropped from the old one after — leaving it
      // under '' would resurrect it on the next New chat.
      stashCurrentDraft();
      const unsent = composerDraftsRef.current[''];
      if (unsent) {
        composerDraftsRef.current[conversationID] = unsent;
      }
      setPendingProject(null);
      setDraftModelOverrides(null);
    }
    if (draft?.projectID && draft.libraryID) {
      revealConversationInProject({projectID: draft.projectID, libraryID: draft.libraryID});
    } else {
      setChatsOpen(true);
    }
    void refreshConversations();
    setAssetsRefreshTick((tick) => tick + 1);
    if (!fromNewChat) {
      return;
    }
    try {
      const detail = await GetConversation(conversationID);
      // Still on the new chat: adopt it (a navigation while the record loaded
      // wins over the adoption).
      if (activeConversationIDRef.current === '') {
        hydrateChatConversation(detail);
        delete composerDraftsRef.current[''];
      }
    } catch (error) {
      notifyError("Couldn't open the new conversation", error);
    }
  }

  // Mirrors for the empty-deps background-job listener, which must call the
  // latest closures. The Open action returns to a finished edit's session:
  // when its editor is still open behind another view, just bring the editor
  // back; otherwise leave whatever editor is open (confirming unsaved edits)
  // and open the session on screen.
  const refreshAfterConversationWriteRef = useRef((_conversationID: string) => {});
  refreshAfterConversationWriteRef.current = refreshAfterConversationWrite;
  // focusTurnID, when set, scrolls the opened transcript to that turn's
  // message — used by history search results to land on the match.
  async function openConversationSummary(conversation: main.ConversationSummary, focusTurnID = '') {
    if (!(await confirmLeaveEditor())) return;
    // Skip redundant opens: the row being viewed (or already loading) needs
    // no re-fetch or transcript re-render. Search-result jumps re-enter with
    // focusTurnID and must still run to scroll to the requested turn.
    if (!focusTurnID && !editorOpen && (conversation.id === activeConversationID || openingConversationRef.current === conversation.id)) {
      return;
    }
    // Edit sessions are not chats: route them to the editor workspace.
    if (conversation.kind === 'edit') {
      await openEditSession(conversation.id);
      return;
    }
    openingConversationRef.current = conversation.id;
    try {
      // Opening an existing conversation supersedes any pending in-project
      // composition — the panel and mention scope follow its own project.
      setPendingProject(null);
      const detail = await GetConversation(conversation.id);
      closeImageEditor();
      setView('app');
      hydrateChatConversation(detail);
      if (focusTurnID) {
        window.setTimeout(() => {
          document.getElementById(`msg-${focusTurnID}`)?.scrollIntoView({behavior: 'smooth', block: 'center'});
        }, 80);
      }
    } catch (error) {
      notifyError("Couldn't open the conversation", error);
    } finally {
      if (openingConversationRef.current === conversation.id) {
        openingConversationRef.current = '';
      }
    }
  }

  function hydrateChatConversation(detail: main.ConversationDetail) {
    stashCurrentDraft();
    const inFlight = inFlightConversationsRef.current[detail.conversation.id];
    const visibleRequestID = inFlight?.kind === 'chat' ? inFlight.requestID : null;
    visibleStreamRef.current = visibleRequestID;
    setActiveStream(visibleRequestID);
    shouldFollowTranscriptRef.current = true;
    const entries: ChatEntry[] = asArray(detail.turns).map((turn) => ({
      id: turn.id,
      role: turn.role === 'user' || turn.role === 'system' ? turn.role : 'assistant',
      content: historyText(turn.content, 'text'),
      thinking: historyText(turn.content, 'thinking'),
      images: historyImages(turn.content),
      videos: historyVideos(turn.content),
      audios: historyAudios(turn.content),
      subtitles: turn.role === 'user' ? historySubtitles(turn.content) : undefined,
      markdowns: historyMarkdowns(turn.content),
      compositions: historyCompositions(turn.content),
      comics: historyComics(turn.content),
      transcripts: historyTranscripts(turn.content),
      harnessRun: parseHarnessRun(turn.providerResponse?.harnessRun),
      mediaTool: turn.providerResponse?.tool as MediaToolSummaryView | undefined,
      provider: turn.provider,
    }));
    if (visibleRequestID && !entries.some((entry) => entry.id === `assistant-${visibleRequestID}`)) {
      const draft = chatStreamDraftsRef.current[visibleRequestID];
      entries.push({
        id: `assistant-${visibleRequestID}`,
        role: 'assistant',
        content: draft?.content ?? '',
        thinking: draft?.thinking,
        images: draft?.images,
        videos: draft?.videos,
        audios: draft?.audios,
        transcripts: draft?.transcripts,
        markdowns: draft?.markdowns,
        harnessRun: harnessRunDraftsRef.current[visibleRequestID],
        streaming: draft?.streaming ?? true,
        error: draft?.error,
        provider: draft?.provider,
      });
    }
    setChat(entries);
    setCollapsedThinkingIDs({});
    setActiveConversationID(detail.conversation.id);
    restoreDraftFor(detail.conversation.id);
  }

  async function archiveConversation(conversation: main.ConversationSummary) {
    try {
      setOpenHistoryMenuID('');
      await DeleteConversation(conversation.id);
      delete composerDraftsRef.current[conversation.id];
      setConversations((items) => asArray(items).filter((item) => item.id !== conversation.id));
      forgetConversation(conversation.id);
      if (editingTitleID === conversation.id) {
        cancelEditingConversationTitle();
      }
      if (activeConversationID === conversation.id) {
        setActiveConversationID('');
        setChat([]);
      }
    } catch (error) {
      notifyError("Couldn't archive the conversation", error);
    }
  }

  // executeChatStream is the shared core of a chat turn: it wires up the
  // streaming-state refs, opens the StreamChat call, and on failure marks the
  // matching assistant entry with the error. Callers are responsible for
  // pushing/replacing the user + assistant entries in `chat` and building
  // requestMessages before calling this — submitChat for a fresh send,
  // retryFailedTurn for a retry. Extracted so both paths share one error path.
  // referencedAssetIds carries @-mentioned asset IDs; the retry path omits it
  // and the backend's latest-wins walk picks up the persisted mention refs.
  async function executeChatStream(opts: {requestID: string; requestMessages: main.ChatMessage[]; referencedAssetIds?: string[]; userEntryId?: string}) {
    const {requestID} = opts;
    // Captured before any await: whether this send creates a brand-new
    // conversation — the same turn-1 condition that gates the workspace/
    // projectId fields below and the sidebar reveal when the start lands.
    const isNewConversation = !activeConversationID;
    visibleStreamRef.current = requestID;
    chatStreamDraftsRef.current[requestID] = {content: '', thinking: '', images: [], videos: [], audios: [], transcripts: [], markdowns: [], streaming: true};
    setActiveStream(requestID);
    try {
      const start = await StreamChat(main.ChatRequest.createFrom({
        requestID,
        conversationId: activeConversationID || undefined,
        baseURL,
        // The composer's effective pair — the conversation's primary override
        // when one is pinned, else the global selection. The backend's record
        // wins regardless; this keeps the request honest with what was shown.
        provider: composerProvider,
        model: composerModel,
        selectedModel: composerModel,
        system,
        messages: opts.requestMessages,
        // Only sent for a new chat (turn 1). The backend ignores it for an
        // existing conversation — the record's immutable workspace wins.
        ...(activeConversationID ? {} : {workspace: draftWorkspace || undefined}),
        // Same turn-1-only lifecycle as the workspace: the pending project the
        // composition started in, pinned onto the new conversation's record.
        ...(activeConversationID ? {} : pendingProjectRef.current?.projectID ? {projectId: pendingProjectRef.current.projectID} : {}),
        // Same turn-1-only lifecycle: the pending model override the composer
        // pinned for this conversation, applied to the first turn and pinned
        // onto the record by the backend.
        ...(activeConversationID ? {} : draftModelOverrides ? {modelOverrides: draftModelOverrides} : {}),
        ...(opts.referencedAssetIds?.length ? {referencedAssetIds: opts.referencedAssetIds} : {}),
      }));
      markConversationInFlight(start.conversationId, start.requestID, 'chat');
      setActiveConversationID(start.conversationId);
      // The just-persisted upload URLs ride the start result
      // (ChatStreamStart.userVideos): swap the user entry's srcs for the
      // artifact srcs the moment the turn begins. A data: URL can carry no
      // poster, and a staged upload's URL points at a transient staging file
      // the backend's age sweep eventually reclaims — both must repoint to
      // the persisted artifact. Positional — both sides list the same
      // attachments in composer order; a count mismatch (a decode failure
      // persisted fewer) leaves the original srcs.
      const persistedVideos = asArray(start.userVideos);
      if (opts.userEntryId && persistedVideos.length) {
        setChat((entries) => entries.map((entry) =>
          entry.id === opts.userEntryId && entry.videos?.length === persistedVideos.length
            ? {...entry, videos: persistedVideos}
            : entry,
        ));
      }
      // Same swap for subtitles (ChatStreamStart.userSubtitles): the Save
      // binding reads artifact paths only, so the persisted URLs are what
      // make a just-attached subtitle downloadable without a reload.
      // Positional and count-guarded like the video swap.
      const persistedSubtitles = asArray(start.userSubtitles);
      if (opts.userEntryId && persistedSubtitles.length) {
        setChat((entries) => entries.map((entry) =>
          entry.id === opts.userEntryId && entry.subtitles?.length === persistedSubtitles.length
            ? {...entry, subtitles: persistedSubtitles}
            : entry,
        ));
      }
      // Documents swap the same way (ChatStreamStart.userMarkdowns).
      const persistedMarkdowns = asArray(start.userMarkdowns);
      if (opts.userEntryId && persistedMarkdowns.length) {
        setChat((entries) => entries.map((entry) =>
          entry.id === opts.userEntryId && entry.markdowns?.length === persistedMarkdowns.length
            ? {...entry, markdowns: persistedMarkdowns}
            : entry,
        ));
      }
      // The conversation now carries its own membership — the pending context
      // has done its job. Captured before the clear: it names where the new
      // row appears in the sidebar tree.
      const createdInProject = isNewConversation ? pendingProjectRef.current : null;
      setPendingProject(null);
      // The pending model override is pinned onto the record by this same
      // turn — the record is the truth now, so a future new conversation
      // shouldn't silently inherit it.
      if (isNewConversation) {
        setDraftModelOverrides(null);
      }
      // The new row is selected via activeConversationID; expand the section
      // that owns it so the selection is actually visible — the Chats list
      // for a standalone chat, the library → project chain for a pinned one.
      if (isNewConversation) {
        if (createdInProject?.projectID && createdInProject?.libraryID) {
          revealConversationInProject(createdInProject);
        } else {
          setChatsOpen(true);
        }
      }
      void refreshConversations();
    } catch (error) {
      chatStreamDraftsRef.current[requestID] = {
        ...(chatStreamDraftsRef.current[requestID] ?? {content: '', thinking: '', images: [], videos: [], audios: []}),
        streaming: false,
        error: formatError(error),
      };
      setActiveStream(null);
      if (visibleStreamRef.current === requestID) {
        visibleStreamRef.current = null;
      }
      setChat((entries) =>
        entries.map((entry) =>
          entry.id === `assistant-${requestID}` ? {...entry, streaming: false, error: formatError(error)} : entry,
        ),
      );
    }
  }

  async function submitChat() {
    const trimmed = prompt.trim();
    if (!trimmed || !composerModel || activeStream || !composerModelIsValid) {
      return;
    }

    // Editor-launch prompts ("Open this image in image editor") are UI
    // navigation, not chat: on a brand-new conversation they route straight to
    // the editor — no model call, no assistant turn. Inside an existing
    // conversation the same words stay a normal chat turn. The composer's
    // claim additionally needs media to seed with — the tier-0 path persists
    // a conversation from attachments, so an unattached "open the composer"
    // falls through to the AI tier (which creates the conversation, and
    // open_editor opens the composer unseeded).
    if (!activeConversationID) {
      const editorKind = detectEditorLaunchIntent(trimmed, attachments.map((item) => item.kind));
      const hasComposerMedia = attachments.some((item) => item.kind === 'video' || item.kind === 'audio');
      const hasComicMedia = attachments.some((item) => item.kind === 'image');
      if (editorKind && (editorKind !== 'composition' || hasComposerMedia) && (editorKind !== 'comic' || hasComicMedia)) {
        await launchEditorConversation(editorKind, trimmed);
        return;
      }
    }

    const userEntry: ChatEntry = {
      id: `user-${Date.now()}`,
      role: 'user',
      content: trimmed,
      images: attachments.filter((item) => item.kind === 'image').map((item) => item.src),
      audios: attachments.filter((item) => item.kind === 'audio').map((item) => item.src),
      videos: attachments.filter((item) => item.kind === 'video').map((item) => item.src),
      subtitles: attachments.filter((item) => item.kind === 'subtitle').map((item) => item.src),
      markdowns: attachments.filter((item) => item.kind === 'markdown').map((item) => item.src),
    };
    // @-mentioned assets ride the request as IDs (backend resolves them into
    // the tool attachment slots); the readable @token stays in the message
    // text for the model — multi-word names quoted (@"my clip.mp4") so the
    // reference boundary is unambiguous. IDs are derived from the text at send
    // time by the same parser the chip overlay renders from, against the same
    // binding pool: the prompt is the single source of truth, so a mention
    // deleted, half-edited, or re-typed by hand resolves (or drops out)
    // exactly as it reads.
    const referencedAssetIds = [
      ...new Set(
        parseComposerMentions(trimmed, mentionBindingPool()).flatMap((segment) =>
          segment.mention?.assetID ? [segment.mention.assetID] : [],
        ),
      ),
    ];
    const requestID = `chat-${Date.now()}-${Math.random().toString(36).slice(2)}`;
    // Only data: URLs are valid model-input parts; a staged upload's artifact
    // URL is not (the backend resolves the bytes from disk for the tools).
    const audioAttachments = attachments
      .filter((item) => item.kind === 'audio')
      .map((item) => item.payload)
      .filter((payload) => payload.startsWith('data:'));
    const imageAttachments = attachments.filter((item) => item.kind === 'image').map((item) => item.payload).filter(Boolean);
    const videoAttachments = attachments.filter((item) => item.kind === 'video').map((item) => item.payload).filter(Boolean);
    const subtitleAttachments = attachments.filter((item) => item.kind === 'subtitle').map((item) => item.payload).filter(Boolean);
    const markdownAttachments = attachments.filter((item) => item.kind === 'markdown').map((item) => item.payload).filter(Boolean);
    const requestMessages: main.ChatMessage[] = [
      ...chat
        .filter((entry) => entry.role !== 'system' && (entry.content || entry.images?.length || entry.audios?.length || entry.videos?.length || entry.subtitles?.length || entry.markdowns?.length))
        .map((entry) => ({
          role: entry.role,
          content: entry.content,
          ...(entry.images?.length ? {images: entry.images.map(imagePayloadForOllama).filter(Boolean)} : {}),
          // Hydrated history audios/videos are /atelier-artifact/ display URLs;
          // only inline data: URLs are valid payloads, so filter like images.
          ...(entry.audios?.length ? {audios: entry.audios.filter((audio) => audio.startsWith('data:'))} : {}),
          ...(entry.videos?.length ? {videos: entry.videos.filter((video) => video.startsWith('data:'))} : {}),
          ...(entry.subtitles?.length ? {subtitles: entry.subtitles.filter((subtitle) => subtitle.startsWith('data:'))} : {}),
          ...(entry.markdowns?.length ? {markdowns: entry.markdowns.filter((markdown) => markdown.startsWith('data:'))} : {}),
        }) as main.ChatMessage),
      {
        role: 'user',
        content: trimmed,
        ...(imageAttachments.length ? {images: imageAttachments} : {}),
        ...(audioAttachments.length ? {audios: audioAttachments} : {}),
        ...(videoAttachments.length ? {videos: videoAttachments} : {}),
        ...(subtitleAttachments.length ? {subtitles: subtitleAttachments} : {}),
        ...(markdownAttachments.length ? {markdowns: markdownAttachments} : {}),
      } as main.ChatMessage,
    ];

    setPrompt('');
    setAttachments([]);
    // The composer contents are being sent, not stashed — drop any stored draft
    // for the current key ('' for a brand-new chat) so it isn't resurrected.
    delete composerDraftsRef.current[activeConversationIDRef.current];
    shouldFollowTranscriptRef.current = true;
    setChat((entries) => [
      ...entries,
      userEntry,
      {id: `assistant-${requestID}`, role: 'assistant', content: '', streaming: true, provider: primaryProvider},
    ]);
    await executeChatStream({requestID, requestMessages, referencedAssetIds, userEntryId: userEntry.id});
  }

  // launchEditorConversation implements the editor-launch prompts submitChat
  // detects ("Open this image in image editor"): persist a new conversation
  // holding the user turn with the attachment — the same turn-1 creation path
  // a chat send uses, so workspace/project/override pinning and artifact
  // persistence are identical — then open the matching editor on the persisted
  // artifact, the editors' one identity input. On a creation failure the
  // composer keeps its contents: nothing was persisted, so the send must look
  // like it never happened.
  async function launchEditorConversation(kind: 'image' | 'video' | 'audio' | 'subtitle' | 'markdown' | 'composition' | 'comic', trimmed: string) {
    const referencedAssetIds = [
      ...new Set(
        parseComposerMentions(trimmed, mentionBindingPool()).flatMap((segment) =>
          segment.mention?.assetID ? [segment.mention.assetID] : [],
        ),
      ),
    ];
    const imagePayloads = attachments.filter((item) => item.kind === 'image').map((item) => item.payload).filter(Boolean);
    const videoPayloads = attachments.filter((item) => item.kind === 'video').map((item) => item.payload).filter(Boolean);
    const audioPayloads = attachments.filter((item) => item.kind === 'audio').map((item) => item.payload).filter(Boolean);
    const subtitlePayloads = attachments.filter((item) => item.kind === 'subtitle').map((item) => item.payload).filter(Boolean);
    const markdownPayloads = attachments.filter((item) => item.kind === 'markdown').map((item) => item.payload).filter(Boolean);
    let result: main.EditorLaunchResult;
    try {
      result = await CreateEditorConversation(main.ChatRequest.createFrom({
        provider: composerProvider,
        model: composerModel,
        selectedModel: composerModel,
        system,
        messages: [{
          role: 'user',
          content: trimmed,
          ...(imagePayloads.length ? {images: imagePayloads} : {}),
          ...(videoPayloads.length ? {videos: videoPayloads} : {}),
          ...(audioPayloads.length ? {audios: audioPayloads} : {}),
          ...(subtitlePayloads.length ? {subtitles: subtitlePayloads} : {}),
          ...(markdownPayloads.length ? {markdowns: markdownPayloads} : {}),
        } as main.ChatMessage],
        // Same turn-1-only lifecycle fields a streamed first turn carries.
        ...(draftWorkspace ? {workspace: draftWorkspace} : {}),
        ...(pendingProjectRef.current?.projectID ? {projectId: pendingProjectRef.current.projectID} : {}),
        ...(draftModelOverrides ? {modelOverrides: draftModelOverrides} : {}),
        ...(referencedAssetIds.length ? {referencedAssetIds} : {}),
      }));
    } catch (error) {
      notifyError("Couldn't open the editor", error);
      return;
    }
    // The launch is on disk — the composer contents are being sent, not
    // stashed; drop the new-chat draft so it isn't resurrected.
    setPrompt('');
    setAttachments([]);
    delete composerDraftsRef.current[activeConversationIDRef.current];
    try {
      await refreshConversations();
      // Same reveal choreography as a streamed first turn: the new row is
      // selected via the hydration below, so expand the sidebar section that
      // owns it to make the selection visible.
      const createdInProject = pendingProjectRef.current;
      setPendingProject(null);
      if (draftModelOverrides) {
        setDraftModelOverrides(null);
      }
      if (createdInProject?.projectID && createdInProject?.libraryID) {
        revealConversationInProject(createdInProject);
      } else {
        setChatsOpen(true);
      }
      const detail = await GetConversation(result.conversationId);
      hydrateChatConversation(detail);
      // The composer opens on the conversation itself, seeded with the
      // persisted media (videos, images as stills, then audio — the seed's
      // deterministic order, compositionSeedIDs' twin); every other editor
      // opens the turn's first artifact of kind.
      if (kind === 'composition') {
        await openNewComposition(result.conversationId, [
          ...asArray(result.videoArtifactIds),
          ...asArray(result.imageArtifactIds),
          ...asArray(result.audioArtifactIds),
        ]);
        return;
      }
      if (kind === 'comic') {
        await openNewComic(result.conversationId, asArray(result.imageArtifactIds));
        return;
      }
      const artifactId = (kind === 'image' ? result.imageArtifactIds
        : kind === 'video' ? result.videoArtifactIds
        : kind === 'subtitle' ? result.subtitleArtifactIds
        : kind === 'markdown' ? result.markdownArtifactIds
        : result.audioArtifactIds)?.[0];
      if (!artifactId) {
        throw new Error('the attached asset was not persisted');
      }
      if (kind === 'image') {
        await openImageEditor(result.conversationId, artifactId);
      } else if (kind === 'video') {
        await openVideoEditor(result.conversationId, artifactId);
      } else if (kind === 'subtitle') {
        await openSubtitleEditor(result.conversationId, artifactId);
      } else if (kind === 'markdown') {
        await openMarkdownEditor(result.conversationId, artifactId);
      } else {
        await openAudioEditor(result.conversationId, artifactId);
      }
    } catch (error) {
      notifyError("Couldn't open the editor", error);
    }
  }

  // retryFailedTurn resends the user message preceding a failed assistant entry,
  // replacing the failed entry in place with a fresh streaming placeholder. Used
  // by the Retry button shown next to an error. The preceding user entry stays in
  // the transcript; no duplicate is pushed — cleaner than re-typing the message,
  // which would show the user message twice. Note: StartChatTurn on the backend
  // will append a fresh user turn to disk, same as a retyped message; failed
  // turns are never persisted, so on reload the failed entry is gone.
  async function retryFailedTurn(failedAssistantId: string) {
    if (!composerModel || activeStream || !composerModelIsValid) {
      return;
    }
    const failedIdx = chat.findIndex((entry) => entry.id === failedAssistantId);
    if (failedIdx < 0) {
      return;
    }
    // Build request messages from everything before the failed assistant entry.
    // The preceding user message is included; the empty failed entry is not.
    const historyForRequest = chat.slice(0, failedIdx);
    if (!historyForRequest.some((entry) => entry.role === 'user')) {
      return;
    }
    const requestMessages: main.ChatMessage[] = historyForRequest
      .filter((entry) => entry.role !== 'system' && (entry.content || entry.images?.length || entry.audios?.length || entry.videos?.length))
      .map((entry) => ({
        role: entry.role,
        content: entry.content,
        ...(entry.images?.length ? {images: entry.images.map(imagePayloadForOllama).filter(Boolean)} : {}),
        ...(entry.audios?.length ? {audios: entry.audios.filter((audio) => audio.startsWith('data:'))} : {}),
        ...(entry.videos?.length ? {videos: entry.videos.filter((video) => video.startsWith('data:'))} : {}),
      }) as main.ChatMessage);
    const requestID = `chat-${Date.now()}-${Math.random().toString(36).slice(2)}`;
    shouldFollowTranscriptRef.current = true;
    setChat((entries) => entries.map((entry) =>
      entry.id === failedAssistantId
        ? {id: `assistant-${requestID}`, role: 'assistant', content: '', streaming: true, provider: primaryProvider}
        : entry,
    ));
    await executeChatStream({requestID, requestMessages});
  }

  // File-menu handler mirrors: the menu-event subscription runs with empty
  // deps, so it must call through to the latest closure, not the first one.
  const newConversationActionRef = useRef(() => {});
  const newLibraryActionRef = useRef(() => {});
  const newProjectActionRef = useRef(() => {});
  newConversationActionRef.current = () => {void leaveEditorThen(handleNewConversationAction);};
  newLibraryActionRef.current = () => {void leaveEditorThen(startCreatingLibrary);};
  newProjectActionRef.current = () => {void leaveEditorThen(handleNewProjectAction);};

  // mentionCandidates is the autocomplete pool: this turn's attachments first
  // (most immediate), then the referable assets newest-first. In a project
  // context the pool is the whole library — every conversation's uploads and
  // generated media, per-conversation provenance in the hint — so an
  // @-mention can cite any asset in the library.
  function mentionCandidates(): MentionCandidate[] {
    const source = composerLibraryID ? libraryAssets : panelAssets;
    const provenance = (asset: main.ConversationAsset) =>
      composerLibraryID
        ? `${asset.role === 'user' ? 'attached' : 'generated'} · ${asset.conversationTitle || 'another chat'}`
        : `${asset.role === 'user' ? 'attached' : 'generated'} · ${assetTurnLabel(asset.originTurnId)}`;
    const assets: MentionCandidate[] = source.map((asset) => ({
      // An aliased asset faces the menu under its alias — the name the user
      // gave it — and the id prefix stays resolvable through the binding pool
      // below, so old @id text in the prompt keeps resolving after the rename.
      name: asset.alias || assetMentionLabel(asset),
      src: asset.url ?? '',
      payload: '',
      kind: asset.kind === 'audio' ? 'audio' : asset.kind === 'video' ? 'video' : asset.kind === 'transcript' ? 'subtitle' : asset.kind === 'markdown' ? 'markdown' : 'image',
      hint: asset.alias ? `alias · ${provenance(asset)}` : provenance(asset),
    }));
    return [...attachmentsRef.current, ...assets];
  }

  // The mention binding pool for parseComposerMentions: this turn's
  // attachments plus the same asset list the menu offers (panelAssets already
  // scopes library-vs-conversation). Both the chip overlay and submit-time
  // referencedAssetIds resolve through this — the composer text is the single
  // source of truth; nothing is recorded at accept time.
  function mentionBindingPool(): { label: string; assetID?: string }[] {
    return [
      ...attachmentsRef.current.map((item) => ({ label: item.name })),
      // Both labels bind each asset: the id prefix (token-safe, stable) and
      // the alias when set (asset_aliases.go — globally unique, so a collision
      // between two assets' labels can't happen; first-wins only guards an
      // alias that happens to spell another asset's id prefix). Bare alias
      // tokens only work when the alias has no whitespace — aliases may carry
      // spaces, and those resolve as @"quoted alias" tokens only.
      ...panelAssets.flatMap((asset) => {
        const entries: { label: string; assetID?: string }[] = [{ label: assetMentionLabel(asset), assetID: asset.id }];
        if (asset.alias) {
          entries.push({ label: asset.alias, assetID: asset.id });
        }
        return entries;
      }),
    ];
  }

  // renderMentionOverlay renders the prompt as the chip mirror's children:
  // resolved mentions as chip spans, everything else as plain (invisible)
  // text so wrapping matches the textarea exactly.
  function renderMentionOverlay(text: string): React.ReactNode {
    return parseComposerMentions(text, mentionBindingPool()).map((segment, index) =>
      segment.mention ? <span key={index} className="mention-chip">{segment.mention.source}</span> : segment.text,
    );
  }

  function handleComposerDragOver(event: React.DragEvent<HTMLDivElement>) {
    if (!composerHasMediaDrag(Array.from(event.dataTransfer?.items ?? []), Array.from(event.dataTransfer?.types ?? []))) {
      return;
    }
    // Signal that dropping here is allowed and stop the browser from opening the file.
    event.preventDefault();
    event.dataTransfer.dropEffect = 'copy';
  }

  // The chat's download helpers: each hands its Save* binding to trackSave,
  // which toasts the saved file and its folder, stays silent when the save
  // dialog was cancelled, and toasts a failure.
  // Each saveGenerated* builds the request and toasts the outcome through
  // trackSave. suggestedName overrides the timestamp form — the assets
  // panel passes an upload's original filename (the record's Name);
  // sanitizeFilename on the Go side guards whatever it receives.
  function saveGeneratedImage(image: string, index: number, suggestedName = '') {
    return trackSave('image', SaveImage(main.SaveImageRequest.createFrom({
      image,
      suggestedName: suggestedName || `atelier-${Date.now()}-${index + 1}`,
    })));
  }

  function saveGeneratedVideo(video: string, index: number, suggestedName = '') {
    return trackSave('video', SaveVideo(main.SaveVideoRequest.createFrom({
      path: video,
      suggestedName: suggestedName || `atelier-${Date.now()}-${index + 1}`,
    })));
  }

  function saveGeneratedAudio(audio: string, index: number, suggestedName = '') {
    return trackSave('audio', SaveAudio(main.SaveAudioRequest.createFrom({
      path: audio,
      suggestedName: suggestedName || `atelier-${Date.now()}-${index + 1}`,
    })));
  }

  function saveGeneratedTranscript(transcript: string, index: number, suggestedName = '') {
    return trackSave('transcript', SaveTranscript(main.SaveTranscriptRequest.createFrom({
      path: transcript,
      suggestedName: suggestedName || `atelier-${Date.now()}-${index + 1}`,
    })));
  }

  function saveGeneratedMarkdown(markdown: string, index: number, suggestedName = '') {
    return trackSave('document', SaveMarkdown(main.SaveTranscriptRequest.createFrom({
      path: markdown,
      suggestedName: suggestedName || `atelier-${Date.now()}-${index + 1}`,
    })));
  }

  function saveGeneratedComposition(composition: string, index: number) {
    return trackSave('composition project', SaveComposition(main.SaveTranscriptRequest.createFrom({
      path: composition,
      suggestedName: `composition-${Date.now()}-${index + 1}`,
    })));
  }

  function saveGeneratedComic(comic: string, index: number) {
    return trackSave('comic project', SaveComic(main.SaveTranscriptRequest.createFrom({
      path: comic,
      suggestedName: `comic-${Date.now()}-${index + 1}`,
    })));
  }

  // downloadAsset is the assets-panel download affordance: every Save* binding
  // accepts the panel's /atelier-artifact URL (each resolves it back to the
  // artifact bytes on disk), so one per-kind dispatcher covers every card.
  function downloadAsset(asset: main.ConversationAsset) {
    if (!asset.url) return;
    // An upload keeps its original filename — the save dialogs suggest it
    // instead of the timestamp form (the record's Name, library_assets.go).
    const suggestedName = asset.name || '';
    if (asset.kind === 'audio') {
      return saveGeneratedAudio(asset.url, 0, suggestedName);
    }
    if (asset.kind === 'video') {
      return saveGeneratedVideo(asset.url, 0, suggestedName);
    }
    if (asset.kind === 'transcript') {
      return saveGeneratedTranscript(asset.url, 0, suggestedName);
    }
    if (asset.kind === 'markdown') {
      return saveGeneratedMarkdown(asset.url, 0, suggestedName);
    }
    if (asset.kind === 'composition') {
      return saveGeneratedComposition(asset.url, 0);
    }
    if (asset.kind === 'comic') {
      return saveGeneratedComic(asset.url, 0);
    }
    return saveGeneratedImage(asset.url, 0, suggestedName);
  }

  // refreshAssets re-runs the assets panel's fetches — the tick useAssets'
  // effects depend on. Uploads and deletes touch no conversation, so this is
  // the only refresh they need (refreshAfterConversationWrite's transcript
  // and sidebar work does not apply).
  function refreshAssets() {
    setAssetsRefreshTick((tick) => tick + 1);
  }

  // uploadLibraryAssets files picked or dropped files into the composer's
  // library as first-class upload records (library_assets.go) — no chat turn
  // behind them. Each file rides the composer's own reader
  // (readFileAsAttachment: data URL for small files, the chunked staged
  // upload for audio/video past 24 MiB), so validation and large-file
  // handling are exactly the attachment path's; the payload handed to the
  // binding is the same string a chat send would carry. Failures are
  // per-file: one bad drop member never sinks the rest.
  async function uploadLibraryAssets(files: FileList | File[]) {
    const libraryID = composerLibraryID;
    if (!libraryID) return;
    const list = Array.from(files).filter(dropAcceptsFile);
    if (list.length === 0) return;
    setLibraryUploadsBusy(true);
    try {
      for (const file of list) {
        try {
          const attachment = await readFileAsAttachment(file);
          await UploadLibraryAsset(
            libraryID,
            activeConversationIDRef.current || '',
            attachment.name,
            attachment.kind,
            attachment.src,
          );
        } catch (error) {
          notifyError(`Couldn't upload ${file.name}`, error);
        }
      }
    } finally {
      setLibraryUploadsBusy(false);
      refreshAssets();
    }
  }

  // deleteLibraryAsset removes an uploaded record and its media — the only
  // per-asset delete in the app, because uploads are the only assets the
  // library owns outright. Conversations that @-mentioned it keep dangling
  // entries (unresolvable mentions are dropped at resolution), and derived
  // assets are untouched: they still live in their conversations.
  async function deleteLibraryAsset(asset: main.ConversationAsset) {
    try {
      await DeleteLibraryAsset(asset.id);
    } catch (error) {
      notifyError(`Couldn't remove ${asset.name || 'the upload'}`, error);
    }
    refreshAssets();
  }

  // setAssetAlias stores (or, committed empty, clears) an asset's unique
  // search name (asset_aliases.go). The alias rides the listings, so success
  // only refetches; failure is a toast — the backend names the asset that
  // already holds the alias — and a false return, which the row's editor
  // reads to stay open with the rejected value for fixing.
  async function setAssetAlias(asset: main.ConversationAsset, alias: string): Promise<boolean> {
    try {
      await SetAssetAlias(asset.id, alias);
    } catch (error) {
      notifyError("Couldn't set the alias", error);
      return false;
    }
    refreshAssets();
    return true;
  }

  // openAssetConversation is the assets-panel navigation affordance: it opens
  // the conversation that owns the asset with the origin turn focused, so the
  // click lands on the message that attached or produced the media. In library
  // scope that switches chats; within a conversation the focus turn makes it a
  // jump-to-source shortcut. openConversationSummary only reads the id (it
  // re-fetches the detail), so a synthesized summary carries the asset's own
  // conversationId/Title.
  function openAssetConversation(asset: main.ConversationAsset) {
    if (!asset.conversationId) return;
    openConversationSummary(
      main.ConversationSummary.createFrom({ id: asset.conversationId, title: asset.conversationTitle || '' }),
      asset.originTurnId || '',
    );
  }


  return {
    saveOpenAICompatibleKey,
    checkRunwareConnection,
    clearRunwareKey,
    runwareAPIKeyInput,
    runwareError,
    runwareHasKey,
    runwareStatus,
    saveRunwareKey,
    setRunwareAPIKeyInput,
    checkOpenRouterConnection,
    checkReplicateConnection,
    clearOpenAICompatibleKey,
    replicateHasKey,
    openRouterAPIKeyInput,
    checkFalConnection,
    setFalAPIKeyInput,
    falError,
    setOpenaiCompatibleKeyInput,
    openRouterStatus,
    setReplicateAPIKeyInput,
    replicateError,
    replicateStatus,
    openaiCompatibleStatus,
    setOpenRouterAPIKeyInput,
    falAPIKeyInput,
    falStatus,
    openRouterError,
    replicateAPIKeyInput,
    falHasKey,
    openaiCompatibleKeyInput,
    openRouterHasKey,
    openaiCompatibleHasKey,
    airplaneMode,
    setMagickBinary,
    setWhisperBinary,
    setAirplaneMode,
    setOpenaiCompatibleBaseURL,
    ffmpegBinary,
    setWhisperChunkMinutes,
    setFfmpegBinary,
    openaiCompatibleBaseURL,
    falInpaintModel,
    setWhisperModel,
    chooseToolWorkspace,
    ollamaNumCtx,
    setBaseURL,
    toolConfig,
    inpaintProvider,
    magickBinary,
    videoDuration,
    storageConfig,
    setOllamaNumCtx,
    baseURL,
    setSystem,
    whisperChunkMinutes,
    system,
    whisperBinary,
    setSipsBinary,
    soundDuration,
    globalModelSelection,
    ffprobeBinary,
    sipsBinary,
    model,
    patchSettingsModelSelection,
    setWhisperVADModel,
    mediaTimeoutMinutes,
    whisperVADModel,
    setFfprobeBinary,
    whisperModel,
    replicateInpaintModel,
    setMediaTimeoutMinutes,
    status,
    models,
    refreshing,
    openaiCompatibleModels,
    openRouterModels,
    redetectLocalTools,
    localToolsDetecting,
    ffprobeStatus,
    ffmpegStatus,
    magickStatus,
    sipsStatus,
    whisperStatus,
    activeConversationID,
    activeStream,
    toggleThinkingCollapsed,
    chat,
    mediaUsage,
    modelUsage,
    transcriptRef,
    visibleHarnessRun,
    copyMessage,
    stopChat,
    shouldFollowTranscriptRef,
    collapsedThinkingIDs,
    copiedMessageID,
    resolveToolPermission,
    toolPermissions,
    chatPromptRef,
    handleComposerDragLeave,
    acceptMention,
    handleComposerDrop,
    insertAssetReference,
    composerDragging,
    handleChatPromptKeyDown,
    stagingAttachments,
    addFiles,
    emptyPrompt,
    setMentionIndex,
    prompt,
    mentionListRef,
    attachments,
    mentionOpen,
    handleComposerDragEnter,
    handleChatPromptScroll,
    composerRef,
    mentionMatchesState,
    handleChatPromptPaste,
    mentionIndex,
    setAttachments,
    chatPromptMirrorRef,
    handleChatPromptChange,
    refreshConversations,
    conversationList,
    purgeBusy,
    visibleConversations,
    setMoveDialogConversation,
    confirmPurgeArchived,
    handleHistoryScroll,
    moveDialogConversation,
    hasMoreConversations,
    conversations,
    purgeArchivedConversations,
    setChatsOpen,
    showMoreConversations,
    chatsOpen,
    setConfirmPurgeArchived,
    setSearchOpen,
    historyQuery,
    historyResults,
    historySearchError,
    searchOpen,
    historySearchBusy,
    setHistoryQuery,
    historySearchTruncated,
    setView,
    view,
    openProjectUsageReport,
    usageReport,
    openConversationUsageReport,
    usageError,
    usageLoading,
    usageExporting,
    exportUsageReport,
    openLibraryUsageReport,
    startupError,
    setStartupError,
    settingsTab,
    setSettingsTab,
    convModelsDraft,
    convModelsKeys,
    setPreviewImage,
    setOpenCapabilityID,
    previewImage,
    openCapabilityID,
    updateError,
    updateBusy,
    dismissUpdate,
    updateQueued,
    checkForUpdates,
    updateAvailable,
    updateCheckBusy,
    requestUpdateInstall,
    shellRef,
    submitNewProject,
    libraries,
    startNewChatInProject,
    toggleContainerMenu,
    moveConversation,
    setNewProjectName,
    cancelEditingContainer,
    startCreatingProject,
    newProjectName,
    saveContainerName,
    editingContainerName,
    expandedProjectIDs,
    armLibraryExport,
    projectConversations,
    openProjectNotes,
    newLibraryName,
    closeProjectNotes,
    librariesOpen,
    containerBusy,
    expandedLibraryIDs,
    confirmDeleteContainer,
    confirmDeleteContainerID,
    startCreatingLibrary,
    toggleProjectExpanded,
    cancelCreatingProject,
    importLibrary,
    confirmExportLibrary,
    saveProjectNotes,
    creatingProjectLibraryID,
    exportPlan,
    notesSaving,
    editingContainerID,
    closeContainerMenu,
    importingLibrary,
    toggleLibraryExpanded,
    setEditingContainerName,
    submitNewLibrary,
    toggleLibrariesOpen,
    moveTargetGroups,
    notesDialogProject,
    creatingLibrary,
    startEditingContainer,
    armContainerDelete,
    notesDraft,
    setNewLibraryName,
    cancelCreatingLibrary,
    openContainerMenuID,
    setNotesDraft,
    openNewComic,
    audioEditorOpening,
    comicEditorDirty,
    openImageEditor,
    resolveEditorNavigation,
    subtitleEditorDirty,
    markdownEditorHandle,
    compositionEditorHandle,
    audioEditorDirty,
    editorOpen,
    openComicEditor,
    videoEditorDirty,
    openCompositionEditor,
    compositionEditorDirty,
    markdownEditorDirty,
    closeImageEditor,
    openVideoEditor,
    discardEditorDraft,
    setPreviewEditContext,
    editorHandle,
    videoEditorOpening,
    editorOpening,
    comicEditorHandle,
    documentCompareHandle,
    audioEditorHandle,
    openSubtitleEditor,
    subtitleEditorHandle,
    previewEditContext,
    confirmLeaveEditor,
    videoEditorHandle,
    openNewComposition,
    openAudioEditor,
    cancelVideoPreparation,
    openMarkdownEditor,
    documentCompareOpening,
    composerLibraryID,
    panelAssets,
    setAssetsPanelOpen,
    assetsPanelOpen,
    composerProjectNames,
    renderConversationRow,
    assetsWidth,
    setResizingSidebar,
    resizingAssets,
    resizingSidebar,
    setResizingAssets,
    sidebarWidth,
    activeConversation,
    uiOverridesActive,
    conversationModelSelection,
    conversationPrimaryOverride,
    composerProvider,
    composerModel,
    composerModelOptions,
    composerModelIsValid,
    openConversationModels,
    patchConversationModels,
    resetConversationModelField,
    resetAllConversationModels,
    closeConversationModels,
    convDurationOptions,
    selectComposerProvider,
    selectComposerModel,
    modelSelectionCatalogs,
    videoDurationKeyframeOptions,
    videoDurationExtendOptions,
    soundDurationOptions,
    videoDurationImageOptions,
    videoDurationOptions,
    chooseDraftWorkspace,
    displayedWorkspace,
    refreshOllama,
    saveOpenRouterKey,
    clearOpenRouterKey,
    saveFalKey,
    clearFalKey,
    saveReplicateKey,
    clearReplicateKey,
    refreshOpenAICompatibleModels,
    resetWorkspace,
    startNewChat,
    editArtifactIDFromURL,
    videoArtifactIDFromURL,
    audioArtifactIDFromURL,
    transcriptArtifactIDFromURL,
    markdownArtifactIDFromURL,
    compositionArtifactIDFromURL,
    comicArtifactIDFromURL,
    openSettings,
    refreshAfterConversationWrite,
    adoptEditorConversation,
    openConversationSummary,
    submitChat,
    retryFailedTurn,
    renderMentionOverlay,
    handleComposerDragOver,
    saveGeneratedImage,
    saveGeneratedVideo,
    saveGeneratedAudio,
    saveGeneratedTranscript,
    saveGeneratedMarkdown,
    saveGeneratedComposition,
    saveGeneratedComic,
    downloadAsset,
    openAssetConversation,
    refreshAssets,
    uploadLibraryAssets,
    deleteLibraryAsset,
    setAssetAlias,
    libraryUploadsBusy,
  };
}
