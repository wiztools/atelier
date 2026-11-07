import {useEffect, useMemo, useRef, useState} from 'react';
import {
  CreateLibrary,
  CreateProject,
  DeleteLibrary,
  DeleteProject,
  ExportLibrary,
  ImportLibrary,
  InspectLibraryExport,
  ListLibraries,
  ListProjectConversations,
  MoveConversationToProject,
  RenameLibrary,
  RenameProject,
  SetProjectNotes,
} from '../../wailsjs/go/main/App';
import {main} from '../../wailsjs/go/models';
import {EventsOn} from '../../wailsjs/runtime/runtime';
import {notifications, notifyError, notifySuccess, savedFileAction, splitSavedPath} from '../notifications';
import {
  libraryArchiveToastKey,
  View,
  LibraryArchiveOp,
  LibraryArchiveEvent,
  ComposerDraft,
  MoveTargetGroup,
} from '../types';
import {asArray} from '../utils';

// LibrariesEnvironment is everything useLibraries needs from App() — the seam
// the hook crosses. Refs are typed structurally ({current}) so the hook stays
// independent of React's namespace types. Failures don't cross it: the hook
// toasts them itself through the shared notifications store.
type LibrariesEnvironment = {
  activeConversationID: string;
  activeConversationProjectID: string;
  closeConversationMenu: () => void;
  expandChatsSection: () => void;
  composerDraftsRef: {current: Record<string, ComposerDraft>};
  pendingProjectRef: {current: {projectID: string; libraryID: string} | null};
  refreshConversations: () => Promise<main.ConversationSummary[]>;
  resetWorkspace: () => Promise<void>;
  setPendingProject: (project: {projectID: string; libraryID: string} | null) => void;
  setView: (view: View) => void;
  startNewChat: () => Promise<void>;
};

// useLibraries owns the sidebar's Libraries tree: the library/project data,
// the expansion/creation/rename/delete UI state, and the actions over them.
// App() destructures the returned names directly into the JSX it renders, so
// the tree markup stays in App while its logic lives here. Handlers are plain
// functions recreated each render (App's own idiom) — the env object is
// re-created every render too, so ref-mirrored callers (File menu, ⌘N) and
// the empty-deps chat:chunk listener always observe current values; the only
// state they touch through closures is the stable useState setters.
function useLibraries(env: LibrariesEnvironment) {
  // libraries state mirrors ListLibraries; expandedLibraryIDs/
  // expandedProjectIDs drive the sidebar tree; projectConversations caches
  // each expanded project's listing. librariesRefreshTick re-fetches both
  // when a turn finishes or a mutation lands, the same role assetsRefreshTick
  // plays for the assets panel.
  const [libraries, setLibraries] = useState<main.LibrarySummary[]>([]);
  const [librariesOpen, setLibrariesOpen] = useState(true);
  const [expandedLibraryIDs, setExpandedLibraryIDs] = useState<Record<string, boolean>>({});
  const [expandedProjectIDs, setExpandedProjectIDs] = useState<Record<string, boolean>>({});
  const [projectConversations, setProjectConversations] = useState<Record<string, main.ConversationSummary[]>>({});
  const [librariesRefreshTick, setLibrariesRefreshTick] = useState(0);
  // Inline creation/rename state for the library tree; editingContainerID is
  // the lib_/proj_ record being renamed (prefix tells the submitter which
  // binding to call).
  const [creatingLibrary, setCreatingLibrary] = useState(false);
  const [newLibraryName, setNewLibraryName] = useState('');
  const [creatingProjectLibraryID, setCreatingProjectLibraryID] = useState('');
  const [newProjectName, setNewProjectName] = useState('');
  const [editingContainerID, setEditingContainerID] = useState('');
  const [editingContainerName, setEditingContainerName] = useState('');
  const [openContainerMenuID, setOpenContainerMenuID] = useState('');
  const [confirmDeleteContainerID, setConfirmDeleteContainerID] = useState('');
  const [containerBusy, setContainerBusy] = useState(false);
  // Project notes: notesDialogProject is the project whose instruction editor
  // overlay is open (null = closed) and notesDraft the textarea's live text;
  // notesSaving disables the actions while SetProjectNotes round-trips. The
  // tree refresh after a save re-renders the row's active-notes marker.
  const [notesDialogProject, setNotesDialogProject] = useState<main.ProjectSummary | null>(null);
  const [notesDraft, setNotesDraft] = useState('');
  const [notesSaving, setNotesSaving] = useState(false);
  // Library export/import: exportPlan holds the pre-flight for the ⋮ menu's
  // two-step confirm (counts, size, missing assets); importingLibrary disables
  // the header button while an archive is being verified and written.
  // archiveOpRef names the archive op in flight, so the progress listener
  // only paints ticks for a live op (an IPC tick landing after the call
  // settled must not resurrect its finished toast).
  const [exportPlan, setExportPlan] = useState<main.LibraryExportPlan | null>(null);
  const [importingLibrary, setImportingLibrary] = useState(false);
  const archiveOpRef = useRef<LibraryArchiveOp | null>(null);
  // The last project the user started a chat in / expanded a library into —
  // the fallback context for the File-menu actions.
  const lastProjectRef = useRef<{projectID: string; libraryID: string} | null>(null);
  const lastExpandedLibraryIDRef = useRef('');

  // Library tree: fetch on mount and whenever a turn finishes or a mutation
  // lands (librariesRefreshTick).
  useEffect(() => {
    let cancelled = false;
    ListLibraries()
      .then((items) => {
        if (!cancelled) setLibraries(asArray(items));
      })
      .catch(() => {
        if (!cancelled) setLibraries([]);
      });
    return () => {
      cancelled = true;
    };
  }, [librariesRefreshTick]);

  // Expanded projects' conversation listings refresh with the tree. Collapse
  // drops stale entries from the cache on the next expand via the same fetch.
  useEffect(() => {
    const openProjectIDs = Object.entries(expandedProjectIDs)
      .filter(([, open]) => open)
      .map(([projectID]) => projectID);
    if (!openProjectIDs.length) {
      return;
    }
    let cancelled = false;
    Promise.all(openProjectIDs.map((projectID) =>
      ListProjectConversations(projectID).catch(() => [] as main.ConversationSummary[]),
    )).then((results) => {
      if (cancelled) {
        return;
      }
      setProjectConversations((current) => {
        const next = {...current};
        openProjectIDs.forEach((projectID, index) => {
          next[projectID] = asArray(results[index]);
        });
        return next;
      });
    });
    return () => {
      cancelled = true;
    };
  }, [expandedProjectIDs, librariesRefreshTick]);

  // The library a project belongs to, from the loaded tree. Empty when the
  // project is unknown (dangling record) — callers treat that as no context.
  function libraryIDForProject(projectID: string): string {
    if (!projectID) {
      return '';
    }
    for (const library of libraries) {
      if (asArray(library.projects).some((project) => project.id === projectID)) {
        return library.id;
      }
    }
    return '';
  }

  // Group the library tree into the move dialog's targets — one group per
  // library that has projects. The ⋮ menu's old inline list flattened these
  // into one button per project, which buried Rename/Archive once the tree
  // grew past a handful of projects.
  const moveTargetGroups = useMemo<MoveTargetGroup[]>(() => {
    return libraries
      .map((library) => ({
        libraryID: library.id,
        libraryName: library.name,
        projects: asArray(library.projects).map((project) => ({projectID: project.id, name: project.name})),
      }))
      .filter((group) => group.projects.length > 0);
  }, [libraries]);

  function bumpLibrariesRefresh() {
    setLibrariesRefreshTick((tick) => tick + 1);
  }

  function startCreatingLibrary() {
    env.setView('app');
    setLibrariesOpen(true);
    setCreatingLibrary(true);
    setNewLibraryName('');
  }

  function startCreatingProject(library: main.LibrarySummary) {
    env.setView('app');
    setLibrariesOpen(true);
    setExpandedLibraryIDs((current) => ({...current, [library.id]: true}));
    lastExpandedLibraryIDRef.current = library.id;
    setCreatingProjectLibraryID(library.id);
    setNewProjectName('');
  }

  async function submitNewLibrary() {
    const name = newLibraryName.trim();
    setCreatingLibrary(false);
    if (!name) {
      return;
    }
    try {
      await CreateLibrary(name);
      setNewLibraryName('');
      bumpLibrariesRefresh();
    } catch (error) {
      notifyError("Couldn't create library", error);
    }
  }

  async function submitNewProject(libraryID: string) {
    const name = newProjectName.trim();
    setCreatingProjectLibraryID('');
    if (!name) {
      return;
    }
    try {
      await CreateProject(libraryID, name);
      setNewProjectName('');
      bumpLibrariesRefresh();
    } catch (error) {
      notifyError("Couldn't create project", error);
    }
  }

  function startEditingContainer(id: string, name: string) {
    setOpenContainerMenuID('');
    setEditingContainerID(id);
    setEditingContainerName(name);
  }

  function cancelEditingContainer() {
    setEditingContainerID('');
    setEditingContainerName('');
  }

  async function saveContainerName() {
    const id = editingContainerID;
    const name = editingContainerName.trim();
    cancelEditingContainer();
    if (!id || !name) {
      return;
    }
    try {
      if (id.startsWith('lib_')) {
        await RenameLibrary(id, name);
      } else {
        await RenameProject(id, name);
      }
      bumpLibrariesRefresh();
    } catch (error) {
      notifyError(id.startsWith('lib_') ? "Couldn't rename the library" : "Couldn't rename the project", error);
    }
  }

  // The project-notes editor: opened from the project's ⋮ menu seeded with
  // the stored block, saved through SetProjectNotes. The backend reads notes
  // live at every turn start, so a save applies to the project's very next
  // message — no per-conversation sync needed.
  function openProjectNotes(project: main.ProjectSummary) {
    setOpenContainerMenuID('');
    setNotesDialogProject(project);
    setNotesDraft(project.notes || '');
  }

  function closeProjectNotes() {
    setNotesDialogProject(null);
    setNotesDraft('');
  }

  async function saveProjectNotes() {
    const project = notesDialogProject;
    if (!project || notesSaving) {
      return;
    }
    setNotesSaving(true);
    try {
      await SetProjectNotes(project.id, notesDraft);
      closeProjectNotes();
      bumpLibrariesRefresh();
    } catch (error) {
      notifyError("Couldn't save project notes", error);
    } finally {
      setNotesSaving(false);
    }
  }

  // Opening/toggling a container ⋮ menu always disarms any pending delete
  // confirmation or export confirm, so a second-step click can never linger
  // into a different menu. Opening also closes the conversation ⋮ menu —
  // both float in portals now, and two open at once reads as broken.
  function toggleContainerMenu(id: string) {
    setConfirmDeleteContainerID('');
    setExportPlan(null);
    if (openContainerMenuID === id) {
      setOpenContainerMenuID('');
      return;
    }
    env.closeConversationMenu();
    setOpenContainerMenuID(id);
  }

  // Closing without toggling (outside press, Escape, scroll) disarms the
  // two-step confirms the same way — a pending confirmation must never
  // outlive its menu.
  function closeContainerMenu() {
    setConfirmDeleteContainerID('');
    setExportPlan(null);
    setOpenContainerMenuID('');
  }

  // Deleting a library or project is a HARD delete — conversations and their
  // artifacts leave the disk — so the ⋮ item is a two-step confirm showing
  // what goes with it. The backend refuses while any member chat is running.
  async function confirmDeleteContainer(id: string) {
    if (containerBusy) {
      return;
    }
    setContainerBusy(true);
    setOpenContainerMenuID('');
    setConfirmDeleteContainerID('');
    try {
      if (id.startsWith('lib_')) {
        await DeleteLibrary(id);
      } else {
        await DeleteProject(id);
      }
      const remaining = await env.refreshConversations();
      bumpLibrariesRefresh();
      // Drop session drafts for conversations that no longer exist, and reset
      // the view if the open conversation was deleted with its project.
      const liveIDs = new Set(remaining.map((item) => item.id));
      for (const draftID of Object.keys(env.composerDraftsRef.current)) {
        if (draftID !== '' && !liveIDs.has(draftID)) {
          delete env.composerDraftsRef.current[draftID];
        }
      }
      if (env.activeConversationID && !liveIDs.has(env.activeConversationID)) {
        await env.resetWorkspace();
      }
    } catch (error) {
      notifyError(id.startsWith('lib_') ? "Couldn't delete the library" : "Couldn't delete the project", error);
    } finally {
      setContainerBusy(false);
    }
  }

  function toggleLibraryExpanded(libraryID: string) {
    setExpandedLibraryIDs((current) => {
      const open = !current[libraryID];
      if (open) {
        lastExpandedLibraryIDRef.current = libraryID;
      }
      return {...current, [libraryID]: open};
    });
  }

  function toggleProjectExpanded(projectID: string) {
    setExpandedProjectIDs((current) => ({...current, [projectID]: !current[projectID]}));
  }

  // Hydration from the persisted UI state (App calls this once LoadUIState
  // resolves at launch): restore the tree's expanded nodes exactly as the
  // toggles above would have left them, and seed the File-menu fallback with
  // the first recorded library. Only stable setters and the ref close over —
  // safe for App to call from a mount effect holding a first-render copy.
  function applySidebarState(sidebar?: main.SidebarState) {
    if (!sidebar) {
      return;
    }
    setLibrariesOpen(!sidebar.librariesCollapsed);
    const libraryIDs = asArray(sidebar.expandedLibraries).filter(Boolean);
    const projectIDs = asArray(sidebar.expandedProjects).filter(Boolean);
    setExpandedLibraryIDs(Object.fromEntries(libraryIDs.map((id) => [id, true])));
    setExpandedProjectIDs(Object.fromEntries(projectIDs.map((id) => [id, true])));
    lastExpandedLibraryIDRef.current = libraryIDs[0] ?? '';
  }

  // New chat inside a project: resets the composer and stashes the project so
  // the first send pins ChatRequest.projectId onto the new conversation.
  async function startNewChatInProject(projectID: string, libraryID: string) {
    const context = {projectID, libraryID};
    lastProjectRef.current = context;
    await env.resetWorkspace();
    env.setPendingProject(context);
  }

  // A conversation created inside a project is selected via its row under the
  // owning library — expand the library → project chain so the selection is
  // visible even from a collapsed tree, and bump the refresh so an
  // already-expanded project lists the new row immediately (the listings
  // effect only refetches on expansion change or tick). The standalone Chats
  // section, which App owns, covers the no-project case.
  function revealConversationInProject(project: {projectID: string; libraryID: string}) {
    setLibrariesOpen(true);
    setExpandedLibraryIDs((current) => ({...current, [project.libraryID]: true}));
    setExpandedProjectIDs((current) => ({...current, [project.projectID]: true}));
    lastExpandedLibraryIDRef.current = project.libraryID;
    bumpLibrariesRefresh();
  }

  // The File-menu / ⌘N "New Conversation": FCP-style context awareness —
  // keep composing in the pending project if one is already active, else the
  // project of the conversation being viewed, else the last project the user
  // worked in, else a standalone chat. startNewChatInProject preserves the
  // context (resetWorkspace clears it, then this re-stashes it) — a plain
  // startNewChat() here would silently drop the pending project.
  function handleNewConversationAction() {
    const pending = env.pendingProjectRef.current;
    if (pending) {
      void startNewChatInProject(pending.projectID, pending.libraryID);
      return;
    }
    const context = env.activeConversationProjectID
      ? {projectID: env.activeConversationProjectID, libraryID: libraryIDForProject(env.activeConversationProjectID)}
      : lastProjectRef.current;
    if (context && context.libraryID) {
      void startNewChatInProject(context.projectID, context.libraryID);
    } else {
      void env.startNewChat();
    }
  }

  // File → New Project…: target the last expanded (or first) library; with no
  // library yet, fall through to the new-library input — a project needs a home.
  function handleNewProjectAction() {
    const targetLibraryID = lastExpandedLibraryIDRef.current
      || asArray(libraries)[0]?.id
      || '';
    if (!targetLibraryID) {
      startCreatingLibrary();
      return;
    }
    const library = libraries.find((item) => item.id === targetLibraryID) ?? asArray(libraries)[0];
    if (library) {
      startCreatingProject(library);
    }
  }

  async function moveConversation(conversation: main.ConversationSummary, projectID: string) {
    env.closeConversationMenu();
    try {
      await MoveConversationToProject(conversation.id, projectID);
      await env.refreshConversations();
      // The moved row's new home may be collapsed — expand it so the
      // conversation stays in view instead of vanishing into a closed
      // section. A project move expands its library → project chain (the
      // reveal bumps the tree refresh itself); a standalone move expands the
      // Chats section App owns.
      const libraryID = projectID ? libraryIDForProject(projectID) : '';
      if (projectID && libraryID) {
        revealConversationInProject({projectID, libraryID});
      } else {
        // Standalone move, or a target that dropped out of the loaded tree
        // (nothing to expand for it) — the standalone row lands in Chats.
        if (!projectID) {
          env.expandChatsSection();
        }
        bumpLibrariesRefresh();
      }
    } catch (error) {
      notifyError("Couldn't move the conversation", error);
    }
  }

  // Drop a conversation from every cached project listing — used when a
  // conversation is archived from the main list or one of the project rows.
  function forgetConversation(conversationID: string) {
    setProjectConversations((current) => {
      const next: Record<string, main.ConversationSummary[]> = {};
      for (const [projectID, members] of Object.entries(current)) {
        next[projectID] = members.filter((item) => item.id !== conversationID);
      }
      return next;
    });
  }

  function toggleLibrariesOpen() {
    setLibrariesOpen((open) => !open);
  }

  function cancelCreatingLibrary() {
    setCreatingLibrary(false);
    setNewLibraryName('');
  }

  function cancelCreatingProject() {
    setCreatingProjectLibraryID('');
    setNewProjectName('');
  }

  // Arms the two-step delete confirmation inside the open ⋮ menu;
  // toggleContainerMenu and the confirming click disarm it again.
  function armContainerDelete(id: string) {
    setConfirmDeleteContainerID(id);
  }

  // Arms the ⋮ menu's export confirm: fetch the pre-flight so the second step
  // can say what the archive will hold (and flag missing assets) before the
  // native save dialog opens.
  async function armLibraryExport(libraryID: string) {
    try {
      const plan = await InspectLibraryExport(libraryID);
      setExportPlan(plan ?? null);
    } catch (error) {
      notifyError("Couldn't prepare the library export", error);
    }
  }

  // Export confirm: the backend opens the save dialog and writes the archive;
  // a cancel comes back as an empty result and leaves everything as it was.
  // Progress ticks ride atelier:library-archive into a toast that this
  // function settles in place (success, error, or gone on cancel).
  async function confirmExportLibrary(libraryID: string) {
    if (containerBusy) {
      return;
    }
    setContainerBusy(true);
    setOpenContainerMenuID('');
    setExportPlan(null);
    archiveOpRef.current = 'export';
    const key = libraryArchiveToastKey('export');
    try {
      const result = await ExportLibrary(libraryID);
      if (result?.path) {
        const {name, folder} = splitSavedPath(result.path);
        notifySuccess('Exported library', {key, message: name, detail: folder ? `in ${folder}` : undefined, action: savedFileAction(result.path)});
      } else {
        notifications.dismissKey(key);
      }
    } catch (error) {
      notifyError("Couldn't export library", error, {key});
    } finally {
      archiveOpRef.current = null;
      setContainerBusy(false);
    }
  }

  // Import: the file picker is the confirmation; everything after it is
  // backend verification and writing, ticking a progress toast. Success
  // refreshes the tree and reveals the imported library.
  async function importLibrary() {
    if (importingLibrary) {
      return;
    }
    setImportingLibrary(true);
    archiveOpRef.current = 'import';
    const key = libraryArchiveToastKey('import');
    try {
      const result = await ImportLibrary();
      if (result?.library?.id) {
        bumpLibrariesRefresh();
        setExpandedLibraryIDs((current) => ({...current, [result.library.id]: true}));
        lastExpandedLibraryIDRef.current = result.library.id;
        notifySuccess(`Imported “${result.library.name}”`, {
          key,
          message: `${result.conversations} conversation${result.conversations === 1 ? '' : 's'}`,
        });
      } else {
        notifications.dismissKey(key);
      }
    } catch (error) {
      notifyError("Couldn't import library", error, {key});
    } finally {
      archiveOpRef.current = null;
      setImportingLibrary(false);
    }
  }

  // Archive progress: the backend ticks per verified file and per written
  // conversation. Ticks only paint while their op is in flight — the toast
  // first appears on the first tick, after the dialog closed, so "Exporting…"
  // never shows while the user is still picking a file.
  useEffect(() => EventsOn('atelier:library-archive', (event: LibraryArchiveEvent) => {
    if (!event?.op || event.op !== archiveOpRef.current) {
      return;
    }
    notifications.notify({
      kind: 'progress',
      key: libraryArchiveToastKey(event.op),
      title: event.op === 'export' ? 'Exporting library…' : 'Importing library…',
      message: event.phase === 'verify' ? 'Verifying archive files' : 'Writing conversations',
      progress: {done: event.done, total: event.total},
    });
  }), []);

  return {
    libraries,
    librariesOpen,
    expandedLibraryIDs,
    expandedProjectIDs,
    projectConversations,
    creatingLibrary,
    newLibraryName,
    creatingProjectLibraryID,
    newProjectName,
    editingContainerID,
    editingContainerName,
    openContainerMenuID,
    confirmDeleteContainerID,
    containerBusy,
    exportPlan,
    importingLibrary,
    moveTargetGroups,
    libraryIDForProject,
    bumpLibrariesRefresh,
    startCreatingLibrary,
    startCreatingProject,
    submitNewLibrary,
    submitNewProject,
    startEditingContainer,
    cancelEditingContainer,
    saveContainerName,
    toggleContainerMenu,
    closeContainerMenu,
    confirmDeleteContainer,
    toggleLibraryExpanded,
    toggleProjectExpanded,
    startNewChatInProject,
    revealConversationInProject,
    handleNewConversationAction,
    handleNewProjectAction,
    moveConversation,
    forgetConversation,
    toggleLibrariesOpen,
    cancelCreatingLibrary,
    cancelCreatingProject,
    armContainerDelete,
    armLibraryExport,
    confirmExportLibrary,
    importLibrary,
    applySidebarState,
    notesDialogProject,
    notesDraft,
    notesSaving,
    openProjectNotes,
    closeProjectNotes,
    saveProjectNotes,
    // Field bindings for the tree's inline inputs (ContainerNameInput's
    // onChange) — the one place raw setters are the natural API.
    setNewLibraryName,
    setNewProjectName,
    setEditingContainerName,
    setNotesDraft,
  };
}

export type {LibrariesEnvironment};
export {useLibraries};
