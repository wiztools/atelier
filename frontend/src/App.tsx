import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import './App.css';
import {
  CheckOllama,
  DiscardMediaUpload,
} from '../wailsjs/go/main/App';
import {main} from '../wailsjs/go/models';
import {ImageEditor} from './editor/ImageEditor';
import {VideoEditor} from './editor/VideoEditor';
import {AudioEditor} from './editor/AudioEditor';
import {SubtitleEditor} from './editor/SubtitleEditor';
import {MarkdownEditor} from './editor/MarkdownEditor';
import {CompositionEditor} from './editor/CompositionEditor';
import {ComicEditor} from './editor/ComicEditor';
import {DocumentCompare} from './editor/DocumentCompare';
import {
  videoThumbnailOnlySrc,
} from './mediaIntake';
import {ToastStack} from './ToastStack';
import {
  ChatProviderID,
} from './types';
import {
  defaultOllamaNumCtx,
  numCtxOptions,
  numCtxLabel,
  defaultMediaTimeoutSeconds,
} from './modelDefaults';
import {asArray, formatModelSize, shortenHomePath} from './utils';
import {ModelCombobox} from './settings/ModelCombobox';
import {InfoHint, AirplaneHint} from './ui/Hints';
import {InlineMediaActions, AudioCardActions} from './media/MediaActions';
import {VideoPlayer} from './media/VideoPlayer';
import {AudioPlayer} from './media/AudioPlayer';
import {LazyPanelVideo} from './media/LazyPanelVideo';
import {ContainerNameInput} from './dialogs/ContainerNameInput';
import {AnchoredMenu} from './dialogs/AnchoredMenu';
import {MoveConversationDialog} from './dialogs/MoveConversationDialog';
import {ProjectNotesDialog} from './dialogs/ProjectNotesDialog';
import {
  ModelCapabilityLink,
  ModelSelectionPanel,
} from './settings/ModelSelectionPanel';
import {TurnUsage, ConversationUsage} from './usage/TurnUsage';
import {UsageReportView} from './usage/UsageReportView';
import {HarnessRunPanel} from './usage/HarnessRunPanel';
import {StreamingActivity} from './usage/StreamingActivity';
import {
  assetTurnLabel,
} from './composerMentions';
import {isNearScrollBottom} from './dom';
import {hasToolPermissionDetails, toolPermissionSummary, toolPermissionTitle} from './toolPermission';
import {
  transcriptChipLabel,
} from './historyContent';
import {useAppModel} from './app/useAppModel';
function App() {
  const app = useAppModel();
  // Values the JSX null-checks and then captures in a callback stay const
  // locals: TypeScript narrows a const through a closure, never a property.
  const {
    videoEditorHandle,
    audioEditorHandle,
    subtitleEditorHandle,
    markdownEditorHandle,
    compositionEditorHandle,
    comicEditorHandle,
    previewEditContext,
    moveDialogConversation,
  } = app;

  return (
    <main
      ref={app.shellRef}
      className={[
        'shell',
        app.view !== 'app' || app.editorOpen ? 'settings-open' : '',
        app.resizingSidebar ? 'resizing resizing-sidebar' : '',
        app.resizingAssets ? 'resizing resizing-assets' : '',
        app.view === 'app' && app.assetsPanelOpen ? 'assets-open' : '',
      ].filter(Boolean).join(' ')}
      style={app.view !== 'app' || app.editorOpen ? undefined : {
        '--sidebar-width': `${app.sidebarWidth}px`,
        '--assets-width': `${app.assetsWidth}px`,
      } as Record<string, string>}
    >
      {app.view !== 'app' || app.editorOpen ? null : (
        <aside className="sidebar">
          <div className="sidebar-main">
            <div className="brand">
              <div className="mark">A</div>
              <div>
                <h1>Atelier</h1>
                <p>AI Workshop</p>
              </div>
            </div>

            <nav className="side-nav" aria-label="Atelier navigation">
              <div className="nav-top-row">
                <button className="nav-new-chat" onClick={app.startNewChat}>
                  <span className="nav-icon">+</span>
                  New chat
                </button>
                <button
                  className={`nav-search-toggle${app.searchOpen || app.historyQuery.trim() ? ' active' : ''}`}
                  onClick={() => app.setSearchOpen(true)}
                  aria-label="Search chats"
                  aria-expanded={app.searchOpen || Boolean(app.historyQuery.trim())}
                  title="Search chats"
                >
                  <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true">
                    <circle cx="7" cy="7" r="4.5" fill="none" stroke="currentColor" strokeWidth="1.6" />
                    <line x1="10.5" y1="10.5" x2="14" y2="14" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" />
                  </svg>
                </button>
              </div>
              {app.searchOpen || app.historyQuery.trim() ? (
                <div className="history-search">
                  <input
                    type="search"
                    autoFocus
                    value={app.historyQuery}
                    onChange={(event) => app.setHistoryQuery(event.target.value)}
                    onBlur={() => {
                      if (!app.historyQuery.trim()) {
                        app.setSearchOpen(false);
                      }
                    }}
                    onKeyDown={(event) => {
                      if (event.key === 'Escape') {
                        event.preventDefault();
                        app.setHistoryQuery('');
                        app.setSearchOpen(false);
                      }
                    }}
                    placeholder="Search chats…"
                    aria-label="Search conversation history"
                  />
                </div>
              ) : null}
            </nav>

            <div className="history-area">
              {app.historyQuery.trim() ? (
                <div className="history-chats">
                  <div className="section-label">{app.historySearchBusy ? 'Searching…' : 'Results'}</div>
                  {app.historySearchError ? (
                    <div className="history-empty">{app.historySearchError}</div>
                  ) : app.historyResults.length ? (
                    app.historyResults.map((result) => (
                      <div key={result.conversation.id} className="search-result">
                        <button
                          className="search-result-open"
                          onClick={() => app.openConversationSummary(result.conversation, result.matches[0]?.turnId ?? '')}
                          title={result.conversation.title}
                        >
                          <span className={`search-result-title${result.titleMatched ? ' matched' : ''}`}>
                            {result.conversation.title || 'Untitled'}
                          </span>
                          {result.matches.map((match, index) => (
                            <small key={index} className="search-result-snippet">
                              <em>{match.role === 'user' ? 'You' : 'Model'}</em>
                              <span className="search-result-text">
                                {match.before}<mark>{match.match}</mark>{match.after}
                              </span>
                            </small>
                          ))}
                        </button>
                      </div>
                    ))
                  ) : !app.historySearchBusy ? (
                    <div className="history-empty">No matches in chat history.</div>
                  ) : null}
                  {app.historySearchTruncated ? (
                    <div className="history-empty">Older matches hidden — refine the query to narrow results.</div>
                  ) : null}
                </div>
              ) : (
                <>
                  <div className={`history-chats${app.chatsOpen ? '' : ' collapsed'}`}>
                    <div className="section-label chats-header">
                      <button
                        type="button"
                        className="section-toggle"
                        onClick={() => app.setChatsOpen((open) => !open)}
                        aria-expanded={app.chatsOpen}
                      >
                        Chats
                      </button>
                      {/* Standalone by design (resetWorkspace clears any pending
                          project) — the project-scoped new chats live under
                          their project rows. */}
                      <button
                        type="button"
                        className="history-icon-button"
                        onClick={() => void app.startNewChat()}
                        aria-label="New chat"
                        title="New chat"
                      >
                        +
                      </button>
                    </div>
                    {app.chatsOpen ? (
                      <div className="chats-body" onScroll={app.handleHistoryScroll}>
                        {app.conversationList.length ? (
                          app.visibleConversations.map(app.renderConversationRow)
                        ) : (
                          <div className="history-empty">No conversations yet.</div>
                        )}
                        {app.hasMoreConversations ? (
                          <button className="history-more" onClick={app.showMoreConversations}>
                            More
                          </button>
                        ) : null}
                      </div>
                    ) : null}
                  </div>

                  <div className={`libraries-section${app.librariesOpen ? '' : ' collapsed'}`}>
                    <div className="section-label libraries-header">
                      <button
                        type="button"
                        className="section-toggle"
                        onClick={app.toggleLibrariesOpen}
                        aria-expanded={app.librariesOpen}
                      >
                        Libraries
                      </button>
                      <button
                        type="button"
                        className="history-icon-button"
                        onClick={() => void app.importLibrary()}
                        disabled={app.importingLibrary}
                        aria-label="Import library archive"
                        title={app.importingLibrary ? 'Importing…' : 'Import library archive'}
                      >
                        ⇩
                      </button>
                      <button
                        type="button"
                        className="history-icon-button"
                        onClick={app.startCreatingLibrary}
                        aria-label="New library"
                        title="New library"
                      >
                        +
                      </button>
                    </div>
                    {app.librariesOpen ? (
                      <div className="libraries-body">
                        {app.creatingLibrary ? (
                          <ContainerNameInput
                            value={app.newLibraryName}
                            placeholder="Library name…"
                            onChange={app.setNewLibraryName}
                            onSubmit={() => void app.submitNewLibrary()}
                            onCancel={app.cancelCreatingLibrary}
                          />
                        ) : null}
                        {app.libraries.length ? app.libraries.map((library) => {
                          const libraryOpen = Boolean(app.expandedLibraryIDs[library.id]);
                          return (
                            <div key={library.id} className="library-item">
                              {app.editingContainerID === library.id ? (
                                <ContainerNameInput
                                  value={app.editingContainerName}
                                  placeholder="Library name…"
                                  onChange={app.setEditingContainerName}
                                  onSubmit={() => void app.saveContainerName()}
                                  onCancel={app.cancelEditingContainer}
                                />
                              ) : (
                                <div className={`container-row library-row${libraryOpen ? ' open' : ''}`}>
                                  <button
                                    type="button"
                                    className="container-open"
                                    onClick={() => app.toggleLibraryExpanded(library.id)}
                                    onDoubleClick={() => app.startEditingContainer(library.id, library.name)}
                                    title={library.name}
                                  >
                                    <span className={`tree-chevron${libraryOpen ? ' open' : ''}`} aria-hidden="true">▸</span>
                                    <span className="container-name">{library.name}</span>
                                    <small className="container-count">{asArray(library.projects).length || ''}</small>
                                  </button>
                                  <div className="history-actions">
                                    <button
                                      type="button"
                                      className="history-icon-button"
                                      onClick={() => app.startCreatingProject(library)}
                                      aria-label={`New project in ${library.name}`}
                                      title="New project"
                                    >
                                      +
                                    </button>
                                    <AnchoredMenu
                                      label={`More actions for ${library.name}`}
                                      open={app.openContainerMenuID === library.id}
                                      onToggle={() => app.toggleContainerMenu(library.id)}
                                      onClose={app.closeContainerMenu}
                                    >
                                      <button onClick={() => app.startCreatingProject(library)}>New Project</button>
                                      <button onClick={() => app.startEditingContainer(library.id, library.name)}>Rename</button>
                                      <button onClick={() => {
                                        app.closeContainerMenu();
                                        app.openLibraryUsageReport(library.id);
                                      }}>Usage report</button>
                                      {app.exportPlan && app.exportPlan.libraryId === library.id ? (
                                        <button disabled={app.containerBusy} onClick={() => void app.confirmExportLibrary(library.id)}>
                                          {`Export ${app.exportPlan.conversations} conversation${app.exportPlan.conversations === 1 ? '' : 's'} (${formatModelSize(app.exportPlan.bytes)})`
                                            + (asArray(app.exportPlan.missingAssets).length
                                              ? ` — ${asArray(app.exportPlan.missingAssets).length} missing asset${asArray(app.exportPlan.missingAssets).length === 1 ? '' : 's'} marked`
                                              : '')
                                            + '?'}
                                        </button>
                                      ) : (
                                        <button onClick={() => void app.armLibraryExport(library.id)}>Export…</button>
                                      )}
                                      {app.confirmDeleteContainerID === library.id ? (
                                        <button className="menu-danger" disabled={app.containerBusy} onClick={() => void app.confirmDeleteContainer(library.id)}>
                                          Delete library and everything in it?
                                        </button>
                                      ) : (
                                        <button className="menu-danger" onClick={() => app.armContainerDelete(library.id)}>Delete…</button>
                                      )}
                                    </AnchoredMenu>
                                  </div>
                                </div>
                              )}
                              {libraryOpen && app.editingContainerID !== library.id ? (
                                <div className="library-children">
                                  {app.creatingProjectLibraryID === library.id ? (
                                    <ContainerNameInput
                                      value={app.newProjectName}
                                      placeholder="Project name…"
                                      onChange={app.setNewProjectName}
                                      onSubmit={() => void app.submitNewProject(library.id)}
                                      onCancel={app.cancelCreatingProject}
                                    />
                                  ) : null}
                                  {asArray(library.projects).map((project) => {
                                    const projectOpen = Boolean(app.expandedProjectIDs[project.id]);
                                    return (
                                      <div key={project.id} className="project-item">
                                        {app.editingContainerID === project.id ? (
                                          <ContainerNameInput
                                            value={app.editingContainerName}
                                            placeholder="Project name…"
                                            onChange={app.setEditingContainerName}
                                            onSubmit={() => void app.saveContainerName()}
                                            onCancel={app.cancelEditingContainer}
                                          />
                                        ) : (
                                          <div className={`container-row project-row${projectOpen ? ' open' : ''}`}>
                                            <button
                                              type="button"
                                              className="container-open"
                                              onClick={() => app.toggleProjectExpanded(project.id)}
                                              onDoubleClick={() => app.startEditingContainer(project.id, project.name)}
                                              title={project.name}
                                            >
                                              <span className={`tree-chevron${projectOpen ? ' open' : ''}`} aria-hidden="true">▸</span>
                                              <span className="container-name">{project.name}</span>
                                              {project.notes ? (
                                                <span
                                                  className="project-notes-mark"
                                                  title={`Project notes active: ${project.notes}`}
                                                  aria-label="Project notes active"
                                                >
                                                  ✎
                                                </span>
                                              ) : null}
                                            </button>
                                            <div className="history-actions">
                                              <button
                                                type="button"
                                                className="history-icon-button"
                                                onClick={() => void app.startNewChatInProject(project.id, library.id)}
                                                aria-label={`New chat in ${project.name}`}
                                                title="New chat in project"
                                              >
                                                +
                                              </button>
                                              <AnchoredMenu
                                                label={`More actions for ${project.name}`}
                                                open={app.openContainerMenuID === project.id}
                                                onToggle={() => app.toggleContainerMenu(project.id)}
                                                onClose={app.closeContainerMenu}
                                              >
                                                <button onClick={() => void app.startNewChatInProject(project.id, library.id)}>New Chat</button>
                                                <button onClick={() => app.startEditingContainer(project.id, project.name)}>Rename</button>
                                                <button onClick={() => app.openProjectNotes(project)}>Notes…</button>
                                                <button onClick={() => {
                                                  app.closeContainerMenu();
                                                  app.openProjectUsageReport(project.id);
                                                }}>Usage report</button>
                                                {app.confirmDeleteContainerID === project.id ? (
                                                  <button className="menu-danger" disabled={app.containerBusy} onClick={() => void app.confirmDeleteContainer(project.id)}>
                                                    Delete project and its chats?
                                                  </button>
                                                ) : (
                                                  <button className="menu-danger" onClick={() => app.armContainerDelete(project.id)}>Delete…</button>
                                                )}
                                              </AnchoredMenu>
                                            </div>
                                          </div>
                                        )}
                                        {projectOpen && app.editingContainerID !== project.id ? (
                                          <div className="project-conversations">
                                            {asArray(app.projectConversations[project.id]).length ? (
                                              asArray(app.projectConversations[project.id]).map(app.renderConversationRow)
                                            ) : (
                                              <div className="history-empty">No chats in this project yet.</div>
                                            )}
                                          </div>
                                        ) : null}
                                      </div>
                                    );
                                  })}
                                  {!asArray(library.projects).length && app.creatingProjectLibraryID !== library.id ? (
                                    <button type="button" className="project-add" onClick={() => app.startCreatingProject(library)}>
                                      + Project
                                    </button>
                                  ) : null}
                                </div>
                              ) : null}
                            </div>
                          );
                        }) : (!app.creatingLibrary ? (
                          <div className="history-empty">No libraries yet.</div>
                        ) : null)}
                      </div>
                    ) : null}
                  </div>
                </>
              )}
            </div>
          </div>

          <button className="settings-button" onClick={() => void app.openSettings()}>
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
              <circle cx="12" cy="12" r="3" />
              <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1 0 2.83 2 2 0 0 1-2.83 0l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-2 2 2 2 0 0 1-2-2v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83 0 2 2 0 0 1 0-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1-2-2 2 2 0 0 1 2-2h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 0-2.83 2 2 0 0 1 2.83 0l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 2-2 2 2 0 0 1 2 2v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 0 2 2 0 0 1 0 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 2 2 2 2 0 0 1-2 2h-.09a1.65 1.65 0 0 0-1.51 1z" />
            </svg>
            Settings
          </button>
        </aside>
      )}
      {app.view !== 'app' || app.editorOpen ? null : (
        <div
          className="sidebar-resizer"
          role="separator"
          aria-orientation="vertical"
          aria-label="Resize sidebar"
          onMouseDown={(event) => {
            event.preventDefault();
            app.setResizingSidebar(true);
          }}
        />
      )}

      <section className="workspace">
        <div className="floating-cards">
          {app.editorOpening && app.view === 'app' ? (
            <div className="editor-preparation-status" role="status">
              <span>{app.videoEditorOpening ? 'Preparing video for editing…' : app.audioEditorOpening ? 'Preparing audio for editing…' : app.documentCompareOpening ? 'Preparing comparison…' : 'Preparing image for editing…'}</span>
              {app.videoEditorOpening ? (
                <button type="button" onClick={() => {
                  app.cancelVideoPreparation();
                }}>Cancel</button>
              ) : null}
            </div>
          ) : null}
          {app.startupError ? (
            <div className="startup-error">
              <strong>Atelier started with a local data warning.</strong>
              <span>{app.startupError}</span>
              <button type="button" onClick={() => app.setStartupError('')}>Dismiss</button>
            </div>
          ) : null}
          {app.updateAvailable ? (
            <div className="update-banner">
              <div className="update-banner-content">
                <strong>Atelier {app.updateAvailable.latest} is available.</strong>
                {app.updateAvailable.notes ? <span>{app.updateAvailable.notes}</span> : null}
                {app.updateQueued ? (
                  <span className="update-queued">Installing when the current conversation finishes…</span>
                ) : null}
                {app.updateError ? <span className="update-error">{app.updateError}</span> : null}
              </div>
              <div className="update-banner-actions">
                <button onClick={app.dismissUpdate} disabled={app.updateBusy}>Later</button>
                <button className="primary" onClick={app.requestUpdateInstall} disabled={app.updateBusy}>
                  {app.updateBusy ? 'Installing…' : 'Install & Relaunch'}
                </button>
              </div>
            </div>
          ) : null}
          <ToastStack />
          {app.toolPermissions.length ? (
            <div className="tool-permission-panel">
              {app.toolPermissions.map((permission) => (
                <div className="tool-permission-card" key={permission.id}>
                  <div className="tool-permission-content">
                    <strong>{toolPermissionTitle(permission)}</strong>
                    <span>{toolPermissionSummary(permission)}</span>
                    {permission.cwd ? <small>in {shortenHomePath(permission.cwd)}</small> : null}
                    {hasToolPermissionDetails(permission) ? (
                      <details className="tool-permission-details">
                        <summary>Details</summary>
                        {permission.command?.length ? (
                          <div>
                            <small>Command</small>
                            <code>{permission.command.join(' ')}</code>
                          </div>
                        ) : null}
                        {permission.summary && permission.summary !== toolPermissionSummary(permission) ? (
                          <div>
                            <small>Summary</small>
                            <pre>{permission.summary}</pre>
                          </div>
                        ) : null}
                        {permission.path ? (
                          <div>
                            <small>Path</small>
                            <code>{shortenHomePath(permission.path)}</code>
                          </div>
                        ) : null}
                        {permission.contentPreview ? (
                          <div>
                            <small>Preview</small>
                            <pre>{permission.contentPreview}</pre>
                          </div>
                        ) : null}
                      </details>
                    ) : null}
                  </div>
                  <div className="tool-permission-actions">
                    <button onClick={() => app.resolveToolPermission(permission.id, false)}>Deny</button>
                    <button className="primary" onClick={() => app.resolveToolPermission(permission.id, true)}>Allow</button>
                  </div>
                </div>
              ))}
            </div>
          ) : null}
        </div>
        {app.editorHandle ? (
          <div className="editor-slot" hidden={app.view !== 'app'}>
            <ImageEditor
              handle={app.editorHandle}
              defaultProvider={app.inpaintProvider}
              falDefaultModel={app.falInpaintModel}
              replicateDefaultModel={app.replicateInpaintModel}
              falHasKey={app.falHasKey}
              replicateHasKey={app.replicateHasKey}
              airplaneMode={app.airplaneMode}
              suspended={app.view !== 'app'}
              onClose={app.closeImageEditor}
              onOpenParent={(parentConversationID) => {
                app.closeImageEditor();
                void app.openConversationSummary(main.ConversationSummary.createFrom({
                  id: parentConversationID,
                  kind: 'chat',
                  title: '',
                }));
              }}
              onSessionCreated={() => {
                void app.refreshConversations();
              }}
            />
          </div>
        ) : videoEditorHandle ? (
          <div className="editor-slot" hidden={app.view !== 'app'}>
            <VideoEditor
              key={videoEditorHandle.sessionID || videoEditorHandle.source.artifactId}
              handle={videoEditorHandle}
              suspended={app.view !== 'app'}
              onClose={() => {void app.confirmLeaveEditor().then((discard) => {if (discard) app.closeImageEditor();});}}
              onOpenParent={(conversationID) => {
                void app.confirmLeaveEditor().then((discard) => {
                  if (!discard) return;
                  app.closeImageEditor();
                  void app.openConversationSummary(main.ConversationSummary.createFrom({id: conversationID, kind: 'chat', title: ''}));
                });
              }}
              onSessionCreated={() => {void app.refreshConversations();}}
              onResultAdded={() => app.refreshAfterConversationWrite(videoEditorHandle.source.conversationId)}
              onDirtyChange={(dirty) => {app.videoEditorDirty.current = dirty;}}
              onOpenSettings={() => {void app.openSettings();}}
            />
          </div>
        ) : audioEditorHandle ? (
          <div className="editor-slot" hidden={app.view !== 'app'}>
            <AudioEditor
              key={audioEditorHandle.sessionID || audioEditorHandle.source.artifactId}
              handle={audioEditorHandle}
              suspended={app.view !== 'app'}
              onClose={() => {void app.confirmLeaveEditor().then((discard) => {if (discard) app.closeImageEditor();});}}
              onOpenParent={(conversationID) => {
                void app.confirmLeaveEditor().then((discard) => {
                  if (!discard) return;
                  app.closeImageEditor();
                  void app.openConversationSummary(main.ConversationSummary.createFrom({id: conversationID, kind: 'chat', title: ''}));
                });
              }}
              onSessionCreated={() => {void app.refreshConversations();}}
              onResultAdded={() => app.refreshAfterConversationWrite(audioEditorHandle.source.conversationId)}
              onDirtyChange={(dirty) => {app.audioEditorDirty.current = dirty;}}
            />
          </div>
        ) : subtitleEditorHandle ? (
          <div className="editor-slot" hidden={app.view !== 'app'}>
            <SubtitleEditor
              key={subtitleEditorHandle.source.artifactId}
              handle={subtitleEditorHandle}
              // The linked-media picker widens to the library exactly when the
              // edited conversation belongs to a project — the scope resolved
              // from its own record at open time, so an independent
              // conversation lists only its own assets.
              libraryID={subtitleEditorHandle.libraryID || undefined}
              suspended={app.view !== 'app'}
              onClose={() => {void app.confirmLeaveEditor().then((discard) => {if (discard) app.closeImageEditor();});}}
              onOpenParent={(conversationID) => {
                void app.confirmLeaveEditor().then((discard) => {
                  if (!discard) return;
                  app.closeImageEditor();
                  void app.openConversationSummary(main.ConversationSummary.createFrom({id: conversationID, kind: 'chat', title: ''}));
                });
              }}
              onDirtyChange={(dirty) => {app.subtitleEditorDirty.current = dirty;}}
              onSavedToConversation={() => app.refreshAfterConversationWrite(subtitleEditorHandle.source.conversationId)}
            />
          </div>
        ) : markdownEditorHandle ? (
          <div className="editor-slot" hidden={app.view !== 'app'}>
            <MarkdownEditor
              key={markdownEditorHandle.source.artifactId}
              handle={markdownEditorHandle}
              suspended={app.view !== 'app'}
              onClose={() => {void app.confirmLeaveEditor().then((discard) => {if (discard) app.closeImageEditor();});}}
              onOpenParent={(conversationID) => {
                void app.confirmLeaveEditor().then((discard) => {
                  if (!discard) return;
                  app.closeImageEditor();
                  void app.openConversationSummary(main.ConversationSummary.createFrom({id: conversationID, kind: 'chat', title: ''}));
                });
              }}
              onDirtyChange={(dirty) => {app.markdownEditorDirty.current = dirty;}}
              onSavedToConversation={() => app.refreshAfterConversationWrite(markdownEditorHandle.source.conversationId)}
            />
          </div>
        ) : compositionEditorHandle ? (
          <div className="editor-slot" hidden={app.view !== 'app'}>
            <CompositionEditor
              key={compositionEditorHandle.source.artifactId || 'composition-draft'}
              handle={compositionEditorHandle}
              // The media picker widens to the library exactly when the
              // composition's conversation belongs to a project — the scope
              // resolved from its own record at open time.
              libraryID={compositionEditorHandle.libraryID || undefined}
              suspended={app.view !== 'app'}
              onClose={() => {void app.confirmLeaveEditor().then((discard) => {if (discard) app.closeImageEditor();});}}
              onOpenParent={(conversationID) => {
                void app.confirmLeaveEditor().then((discard) => {
                  if (!discard) return;
                  app.closeImageEditor();
                  void app.openConversationSummary(main.ConversationSummary.createFrom({id: conversationID, kind: 'chat', title: ''}));
                });
              }}
              onDirtyChange={(dirty) => {app.compositionEditorDirty.current = dirty;}}
              onSavedToConversation={() => app.refreshAfterConversationWrite(compositionEditorHandle.source.conversationId)}
            />
          </div>
        ) : comicEditorHandle ? (
          <div className="editor-slot" hidden={app.view !== 'app'}>
            <ComicEditor
              key={comicEditorHandle.source.artifactId || 'comic-draft'}
              handle={comicEditorHandle}
              // The media picker widens to the library exactly when the
              // comic's conversation belongs to a project — the scope
              // resolved from its own record at open time.
              libraryID={comicEditorHandle.libraryID || undefined}
              suspended={app.view !== 'app'}
              onClose={() => {void app.confirmLeaveEditor().then((discard) => {if (discard) app.closeImageEditor();});}}
              onOpenParent={(conversationID) => {
                void app.confirmLeaveEditor().then((discard) => {
                  if (!discard) return;
                  app.closeImageEditor();
                  void app.openConversationSummary(main.ConversationSummary.createFrom({id: conversationID, kind: 'chat', title: ''}));
                });
              }}
              onDirtyChange={(dirty) => {app.comicEditorDirty.current = dirty;}}
              onSavedToConversation={() => app.refreshAfterConversationWrite(comicEditorHandle.source.conversationId)}
            />
          </div>
        ) : app.documentCompareHandle ? (
          <div className="editor-slot" hidden={app.view !== 'app'}>
            <DocumentCompare
              key={`${app.documentCompareHandle.source.before.artifactId}:${app.documentCompareHandle.source.after.artifactId}`}
              handle={app.documentCompareHandle}
              suspended={app.view !== 'app'}
              // Read-only: nothing to confirm on the way out.
              onClose={app.closeImageEditor}
              onOpenParent={(conversationID) => {
                app.closeImageEditor();
                void app.openConversationSummary(main.ConversationSummary.createFrom({id: conversationID, kind: 'chat', title: ''}));
              }}
              onOpenDocument={(conversationID, artifactID) => void app.openMarkdownEditor(conversationID, artifactID)}
            />
          </div>
        ) : null}
        {app.view === 'settings' ? (
          <>
            <div className="toolbar">
              <button className="back-button" onClick={() => app.setView('app')}>← Back</button>
              <div className="model-count">Saved to ~/.atelier/config.json</div>
            </div>
            <div className="settings-screen">
              <div className="settings-header">
                <h2>Settings</h2>
                <p>Providers, model defaults, local tools, and general preferences.</p>
              </div>

              <div className="settings-tabs" role="tablist">
                <button
                  type="button"
                  role="tab"
                  aria-selected={app.settingsTab === 'providers'}
                  className={app.settingsTab === 'providers' ? 'active' : ''}
                  onClick={() => app.setSettingsTab('providers')}
                >Providers</button>
                <button
                  type="button"
                  role="tab"
                  aria-selected={app.settingsTab === 'models'}
                  className={app.settingsTab === 'models' ? 'active' : ''}
                  onClick={() => app.setSettingsTab('models')}
                >Models</button>
                <button
                  type="button"
                  role="tab"
                  aria-selected={app.settingsTab === 'tools'}
                  className={app.settingsTab === 'tools' ? 'active' : ''}
                  onClick={() => app.setSettingsTab('tools')}
                >Local Tools</button>
                <button
                  type="button"
                  role="tab"
                  aria-selected={app.settingsTab === 'general'}
                  className={app.settingsTab === 'general' ? 'active' : ''}
                  onClick={() => app.setSettingsTab('general')}
                >General</button>
              </div>

              {app.settingsTab === 'providers' ? (
              <>
              <section className="settings-section">
                <h3>
                  Airplane Mode{' '}
                  <InfoHint
                    label="About airplane mode"
                    text="While on, Atelier itself never contacts a cloud service: chat runs on local Ollama and OpenAI-compatible endpoints (loopback-verified), cloud image/video/audio generation is unavailable, transcription uses the local whisper CLI, the image editor's inpaint is disabled, and update checks stand down. run_command stays available — airplane mode is not a sandbox."
                  />
                </h3>
                <div className="settings-rows">
                  <label className="airplane-toggle">
                    <input
                      type="checkbox"
                      checked={app.airplaneMode}
                      onChange={(event) => app.setAirplaneMode(event.target.checked)}
                      aria-label="Airplane mode — block all cloud providers"
                    />
                    <span>Block all cloud providers — local models and tools only</span>
                  </label>
                </div>
              </section>

              <div className="settings-group-label">Local servers</div>

              <section className="settings-section">
                <h3>Ollama</h3>
                <div className="connection">
                  <label htmlFor="base-url">Ollama endpoint</label>
                  <div className="endpoint-row">
                    <input id="base-url" value={app.baseURL} onChange={(event) => app.setBaseURL(event.target.value)} />
                    <button
                      type="button"
                      className={`icon-btn${app.refreshing ? ' spinning' : ''}`}
                      onClick={() => app.refreshOllama()}
                      disabled={app.refreshing}
                      aria-label="Refresh models"
                      title="Refresh models"
                    >
                      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                        <path d="M21 12a9 9 0 0 1-9 9 9 9 0 0 1-6.7-3" />
                        <path d="M3 12a9 9 0 0 1 9-9 9 9 0 0 1 6.7 3" />
                        <polyline points="21 4 21 9 16 9" />
                        <polyline points="3 20 3 15 8 15" />
                      </svg>
                    </button>
                  </div>
                  <div className={app.status?.online ? 'status online' : 'status offline'}>
                    <span />
                    {app.status?.online
                      ? `Online${app.status.version ? ` ${app.status.version}` : ''} — ${asArray(app.models).length} local models available`
                      : app.status ? 'Offline' : 'Not checked'}
                  </div>
                  {/* Ollama's own reachability reason: CheckOllama's error
                      when the endpoint is down, or the boot fallback's
                      refresh failure. Nothing else writes the field — config,
                      window-state, and key-write failures toast instead. */}
                  {app.status?.error ? <div className="status-error">{app.status.error}</div> : null}
                  <div className="field-label-row">
                    <label htmlFor="ollama-num-ctx">Context Window (num_ctx)</label>
                    <InfoHint
                      label="Context window guidance"
                      text="Sent on every call. Planning prompts carry the tool catalog (~6K tokens) before evidence — 16K or more is recommended for tool-heavy turns."
                    />
                  </div>
                  <select
                    id="ollama-num-ctx"
                    value={app.ollamaNumCtx}
                    onChange={(event) => app.setOllamaNumCtx(Number(event.target.value) || defaultOllamaNumCtx)}
                  >
                    {(numCtxOptions.includes(app.ollamaNumCtx) ? numCtxOptions : [...numCtxOptions, app.ollamaNumCtx].sort((a, b) => a - b)).map((value) => (
                      <option key={value} value={value}>{numCtxLabel(value)}</option>
                    ))}
                  </select>
                </div>
              </section>

              <section className="settings-section">
                <h3>OpenAI-compatible (local)</h3>
                <div className="connection">
                  <div className="field-label-row">
                    <label htmlFor="openai-image-endpoint">Local image server endpoint</label>
                    <InfoHint
                      label="Local image server endpoint"
                      text="Any server speaking OpenAI's /v1/images/generations (LocalAI, a diffusers shim, ...). Used when Image Provider is set to OpenAI-compatible."
                    />
                  </div>
                  <div className="endpoint-row">
                    <input
                      id="openai-image-endpoint"
                      value={app.openaiCompatibleBaseURL}
                      onChange={(event) => app.setOpenaiCompatibleBaseURL(event.target.value)}
                    />
                    <button onClick={() => app.refreshOpenAICompatibleModels()}>Refresh</button>
                  </div>
                  <div className={app.openaiCompatibleStatus === 'ok' ? 'status online' : 'status offline'}>
                    <span />
                    {app.openaiCompatibleStatus === 'ok'
                      ? `Online — ${app.openaiCompatibleModels.length} model${app.openaiCompatibleModels.length === 1 ? '' : 's'} listed`
                      : app.openaiCompatibleStatus === 'error'
                        ? 'Server unreachable or no /v1/models — enter model ids manually in Models.'
                        : 'Not checked.'}
                  </div>
                </div>
                <div className="connection">
                  <label htmlFor="openai-image-key">API Key (optional)</label>
                  <div className="endpoint-row">
                    <input
                      id="openai-image-key"
                      type="password"
                      placeholder={app.openaiCompatibleHasKey ? 'Key saved — enter a new key to replace it' : 'Leave empty for a local server without auth'}
                      value={app.openaiCompatibleKeyInput}
                      onChange={(event) => app.setOpenaiCompatibleKeyInput(event.target.value)}
                    />
                    <button type="button" className="icon-btn" onClick={app.saveOpenAICompatibleKey} disabled={!app.openaiCompatibleKeyInput} aria-label="Save key" title="Save key">
                      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                        <path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z" />
                        <polyline points="17 21 17 13 7 13 7 21" />
                        <polyline points="7 3 7 8 15 8" />
                      </svg>
                    </button>
                    {app.openaiCompatibleHasKey ? (
                      <button type="button" className="icon-btn" onClick={app.clearOpenAICompatibleKey} aria-label="Clear key" title="Clear key">
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                          <polyline points="3 6 5 6 21 6" />
                          <path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" />
                        </svg>
                      </button>
                    ) : <span className="icon-btn-placeholder" aria-hidden="true" />}
                  </div>
                  <div className={app.openaiCompatibleHasKey ? 'status online' : 'status offline'}>
                    <span />
                    {app.openaiCompatibleHasKey ? 'Bearer key saved' : 'No key — requests go unauthenticated.'}
                  </div>
                </div>
              </section>

              <div className="settings-group-label">Cloud services</div>

              <section className={app.airplaneMode ? 'settings-section airplane-blocked' : 'settings-section'}>
                <h3>
                  OpenRouter{' '}
                  {app.airplaneMode ? (
                    <AirplaneHint label="OpenRouter unavailable" text="Unavailable while airplane mode is on — turn it off above to configure." />
                  ) : null}
                </h3>
                <div className="connection">
                  <label htmlFor="openrouter-key">API Key</label>
                  <div className="endpoint-row">
                    <input
                      id="openrouter-key"
                      type="password"
                      placeholder={app.openRouterHasKey ? 'Key saved — enter a new key to replace it' : 'sk-or-...'}
                      value={app.openRouterAPIKeyInput}
                      onChange={(event) => app.setOpenRouterAPIKeyInput(event.target.value)}
                    />
                    <button type="button" className="icon-btn" onClick={app.saveOpenRouterKey} disabled={!app.openRouterAPIKeyInput} aria-label="Save key" title="Save key">
                      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                        <path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z" />
                        <polyline points="17 21 17 13 7 13 7 21" />
                        <polyline points="7 3 7 8 15 8" />
                      </svg>
                    </button>
                    <button type="button" className={`icon-btn${app.openRouterStatus === 'connected' ? ' spinning' : ''}`} onClick={app.checkOpenRouterConnection} disabled={!app.openRouterHasKey} aria-label="Check connection" title="Check connection">
                      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                        <path d="M21 12a9 9 0 0 1-9 9 9 9 0 0 1-6.7-3" />
                        <path d="M3 12a9 9 0 0 1 9-9 9 9 0 0 1 6.7 3" />
                        <polyline points="21 4 21 9 16 9" />
                        <polyline points="3 20 3 15 8 15" />
                      </svg>
                    </button>
                    {app.openRouterHasKey ? (
                      <button type="button" className="icon-btn" onClick={app.clearOpenRouterKey} aria-label="Clear key" title="Clear key">
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                          <polyline points="3 6 5 6 21 6" />
                          <path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" />
                        </svg>
                      </button>
                    ) : <span className="icon-btn-placeholder" aria-hidden="true" />}
                  </div>
                  <div className={app.openRouterStatus === 'connected' ? 'status online' : 'status offline'}>
                    <span />
                    {app.openRouterStatus === 'connected'
                      ? 'Connected'
                      : app.openRouterStatus === 'error'
                        ? `OpenRouter: ${app.openRouterError}`
                        : app.openRouterHasKey
                          ? 'API key saved — not checked'
                          : 'No key saved.'}
                  </div>
                </div>
              </section>

              <section className={app.airplaneMode ? 'settings-section airplane-blocked' : 'settings-section'}>
                <h3>
                  fal.ai{' '}
                  {app.airplaneMode ? (
                    <AirplaneHint label="fal.ai unavailable" text="Unavailable while airplane mode is on — turn it off above to configure." />
                  ) : null}
                </h3>
                <div className="connection">
                  <label htmlFor="fal-key">API Key</label>
                  <div className="endpoint-row">
                    <input
                      id="fal-key"
                      type="password"
                      placeholder={app.falHasKey ? 'Key saved — enter a new key to replace it' : 'fal-...'}
                      value={app.falAPIKeyInput}
                      onChange={(event) => app.setFalAPIKeyInput(event.target.value)}
                    />
                    <button type="button" className="icon-btn" onClick={app.saveFalKey} disabled={!app.falAPIKeyInput} aria-label="Save key" title="Save key">
                      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                        <path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z" />
                        <polyline points="17 21 17 13 7 13 7 21" />
                        <polyline points="7 3 7 8 15 8" />
                      </svg>
                    </button>
                    <button type="button" className={`icon-btn${app.falStatus === 'connected' ? ' spinning' : ''}`} onClick={app.checkFalConnection} disabled={!app.falHasKey} aria-label="Check connection" title="Check connection">
                      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                        <path d="M21 12a9 9 0 0 1-9 9 9 9 0 0 1-6.7-3" />
                        <path d="M3 12a9 9 0 0 1 9-9 9 9 0 0 1 6.7 3" />
                        <polyline points="21 4 21 9 16 9" />
                        <polyline points="3 20 3 15 8 15" />
                      </svg>
                    </button>
                    {app.falHasKey ? (
                      <button type="button" className="icon-btn" onClick={app.clearFalKey} aria-label="Clear key" title="Clear key">
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                          <polyline points="3 6 5 6 21 6" />
                          <path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" />
                        </svg>
                      </button>
                    ) : <span className="icon-btn-placeholder" aria-hidden="true" />}
                  </div>
                  <div className={app.falStatus === 'connected' ? 'status online' : 'status offline'}>
                    <span />
                    {app.falStatus === 'connected'
                      ? 'Connected'
                      : app.falStatus === 'error'
                        ? `fal.ai: ${app.falError}`
                        : app.falHasKey
                          ? 'API key saved — not checked'
                          : 'No key saved.'}
                  </div>
                </div>
              </section>

              <section className={app.airplaneMode ? 'settings-section airplane-blocked' : 'settings-section'}>
                <h3>
                  Replicate{' '}
                  {app.airplaneMode ? (
                    <AirplaneHint label="Replicate unavailable" text="Unavailable while airplane mode is on — turn it off above to configure." />
                  ) : null}
                </h3>
                <div className="connection">
                  <label htmlFor="replicate-key">API Token</label>
                  <div className="endpoint-row">
                    <input
                      id="replicate-key"
                      type="password"
                      placeholder={app.replicateHasKey ? 'Token saved — enter a new token to replace it' : 'r8_...'}
                      value={app.replicateAPIKeyInput}
                      onChange={(event) => app.setReplicateAPIKeyInput(event.target.value)}
                    />
                    <button type="button" className="icon-btn" onClick={app.saveReplicateKey} disabled={!app.replicateAPIKeyInput} aria-label="Save token" title="Save token">
                      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                        <path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z" />
                        <polyline points="17 21 17 13 7 13 7 21" />
                        <polyline points="7 3 7 8 15 8" />
                      </svg>
                    </button>
                    <button type="button" className={`icon-btn${app.replicateStatus === 'connected' ? ' spinning' : ''}`} onClick={app.checkReplicateConnection} disabled={!app.replicateHasKey} aria-label="Check connection" title="Check connection">
                      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                        <path d="M21 12a9 9 0 0 1-9 9 9 9 0 0 1-6.7-3" />
                        <path d="M3 12a9 9 0 0 1 9-9 9 9 0 0 1 6.7 3" />
                        <polyline points="21 4 21 9 16 9" />
                        <polyline points="3 20 3 15 8 15" />
                      </svg>
                    </button>
                    {app.replicateHasKey ? (
                      <button type="button" className="icon-btn" onClick={app.clearReplicateKey} aria-label="Clear token" title="Clear token">
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                          <polyline points="3 6 5 6 21 6" />
                          <path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" />
                        </svg>
                      </button>
                    ) : <span className="icon-btn-placeholder" aria-hidden="true" />}
                  </div>
                  <div className={app.replicateStatus === 'connected' ? 'status online' : 'status offline'}>
                    <span />
                    {app.replicateStatus === 'connected'
                      ? 'Connected'
                      : app.replicateStatus === 'error'
                        ? `Replicate: ${app.replicateError}`
                        : app.replicateHasKey
                          ? 'API token saved — not checked'
                          : 'No token saved.'}
                  </div>
                </div>
              </section>

              </>
              ) : null}

              {app.settingsTab === 'tools' ? (
              <>
              <div className="tools-tab-intro">
                <span>Binaries installed on this machine — Atelier detects them on the PATH automatically, so most of this stays untouched. Local tools keep working in airplane mode.</span>
                <button type="button" onClick={app.redetectLocalTools} disabled={app.localToolsDetecting}>
                  {app.localToolsDetecting ? 'Detecting…' : 'Re-detect'}
                </button>
              </div>

              <section className="settings-section">
                <h3>
                  Video &amp; Audio Tools (FFmpeg){' '}
                  <InfoHint
                    label="Local video and audio tools"
                    text="ffmpeg powers the local video and audio tools in chat: screenshot or storyboard a clip, split, join (with crossfades), extract, replace, or remove audio, convert formats, change speed or shape, reverse playback, fade in or out, and burn subtitles. Detected automatically on PATH, or set an explicit binary below. A missing ffprobe only degrades some paths — joins always re-encode and audio extraction falls back to MP3."
                  />
                </h3>
                <div className="settings-rows">
                  <div className="two-column">
                    <div className="field">
                      <div className="field-label-row">
                        <label htmlFor="ffmpeg-binary">FFmpeg Binary</label>
                        {app.ffmpegStatus?.detail ? (
                          <InfoHint label="FFmpeg binary detection" text={app.ffmpegStatus.detail} />
                        ) : null}
                      </div>
                      <input
                        id="ffmpeg-binary"
                        value={app.ffmpegBinary}
                        onChange={(event) => app.setFfmpegBinary(event.target.value)}
                        placeholder={app.ffmpegStatus?.path || 'auto-detect on PATH: ffmpeg'}
                      />
                    </div>

                    <div className="field">
                      <div className="field-label-row">
                        <label htmlFor="ffprobe-binary">FFprobe Binary</label>
                        {app.ffprobeStatus?.detail ? (
                          <InfoHint label="FFprobe binary detection" text={app.ffprobeStatus.detail} />
                        ) : null}
                      </div>
                      <input
                        id="ffprobe-binary"
                        value={app.ffprobeBinary}
                        onChange={(event) => app.setFfprobeBinary(event.target.value)}
                        placeholder={app.ffprobeStatus?.path || 'auto-detect on PATH: ffprobe'}
                      />
                    </div>
                  </div>
                  {!app.ffmpegStatus?.available ? (
                    <span className="hint">{app.ffmpegStatus?.detail ?? 'Detecting local tools…'}</span>
                  ) : null}
                </div>
              </section>

              <section className="settings-section">
                <h3>
                  Image Tools (sips + ImageMagick){' '}
                  <InfoHint
                    label="Local image tools"
                    text="Two local backends power the image-editing tools in chat. macOS's built-in sips converts formats (including HEIC photos), crops to an aspect, resizes, rotates, flips, and reports image facts — no install needed. ImageMagick adds watermarks and logo overlays, collages/grids, color adjustments (brightness, contrast, saturation, grayscale, sepia), and metadata stripping with webp output: `brew install imagemagick`. Both are detected automatically on PATH, or set explicit binaries below."
                  />
                </h3>
                <div className="settings-rows">
                  <div className="two-column">
                    <div className="field">
                      <div className="field-label-row">
                        <label htmlFor="sips-binary">sips Binary</label>
                        {app.sipsStatus?.detail ? (
                          <InfoHint label="sips binary detection" text={app.sipsStatus.detail} />
                        ) : null}
                      </div>
                      <input
                        id="sips-binary"
                        value={app.sipsBinary}
                        onChange={(event) => app.setSipsBinary(event.target.value)}
                        placeholder={app.sipsStatus?.path || 'auto-detect on PATH: sips'}
                      />
                    </div>

                    <div className="field">
                      <div className="field-label-row">
                        <label htmlFor="magick-binary">ImageMagick Binary</label>
                        {app.magickStatus?.detail ? (
                          <InfoHint label="ImageMagick binary detection" text={app.magickStatus.detail} />
                        ) : null}
                      </div>
                      <input
                        id="magick-binary"
                        value={app.magickBinary}
                        onChange={(event) => app.setMagickBinary(event.target.value)}
                        placeholder={app.magickStatus?.path || 'auto-detect on PATH: magick / convert'}
                      />
                    </div>
                  </div>
                  {!app.magickStatus?.available ? (
                    <span className="hint">{app.magickStatus?.detail ?? 'Detecting local tools…'}</span>
                  ) : null}
                </div>
              </section>

              <section className="settings-section">
                <h3>
                  Speech Tools (Whisper){' '}
                  <InfoHint
                    label="Local speech tools"
                    text="Whisper powers local transcription in chat — plain transcripts, timestamped VTT/SRT subtitles, and English translation of foreign-language speech. It runs when Transcription Provider is 'Whisper (local)' under Models. The binary is detected automatically on PATH ('whisper', then 'whisper-cli'); set an explicit binary, the model, or the optional VAD model below. The VAD model is whisper.cpp only: when set, silent stretches are skipped instead of transcribed as hallucinated text."
                  />
                </h3>
                <div className="settings-rows">
                  <div className="two-column">
                    <div className="field">
                      <div className="field-label-row">
                        <label htmlFor="whisper-binary">Whisper Binary</label>
                        {app.whisperStatus?.detail ? (
                          <InfoHint label="Whisper binary detection" text={app.whisperStatus.detail} />
                        ) : null}
                      </div>
                      <input
                        id="whisper-binary"
                        value={app.whisperBinary}
                        onChange={(event) => app.setWhisperBinary(event.target.value)}
                        placeholder={app.whisperStatus?.path || 'auto-detect on PATH: whisper, then whisper-cli'}
                      />
                    </div>

                    <div className="field">
                      <div className="field-label-row">
                        <label htmlFor="whisper-model">Whisper Model</label>
                        <InfoHint
                          label="Whisper model"
                          text={app.whisperStatus?.flavor === 'whisper-cpp'
                            ? 'A ggml model file for whisper.cpp (a leading ~/ is expanded). Empty resolves: $WHISPER_MODEL (if set when Atelier launched) → ~/.whisper-base.en.bin → ~/.cache/whisper.cpp/ggml-base.en.bin → ~/models/ggml-base.en.bin.'
                            : 'An openai-whisper model size (tiny, base, small, medium, large-v3, ...), downloaded on first use. Empty uses the CLI\'s own default, small. With whisper.cpp this becomes a ggml model file path instead.'}
                        />
                      </div>
                      <input
                        id="whisper-model"
                        value={app.whisperModel}
                        onChange={(event) => app.setWhisperModel(event.target.value)}
                        placeholder={app.whisperStatus?.flavor === 'whisper-cpp' ? '/path/to/ggml-base.en.bin' : 'small (whisper default)'}
                      />
                    </div>

                    <div className="field">
                      <div className="field-label-row">
                        <label htmlFor="whisper-vad-model">Whisper VAD Model</label>
                        <InfoHint
                          label="Optional voice-activity detection"
                          text="Path to a whisper.cpp VAD model (e.g. ~/.cache/whisper.cpp/ggml-silero-v6.2.0.bin). When set, local transcription runs with --vad so silent stretches are skipped instead of transcribed as hallucinated text. whisper.cpp only — the openai-whisper CLI has no VAD support and transcriptions carry a notice instead."
                        />
                      </div>
                      <input
                        id="whisper-vad-model"
                        value={app.whisperVADModel}
                        onChange={(event) => app.setWhisperVADModel(event.target.value)}
                        placeholder="optional: ~/.cache/whisper.cpp/ggml-silero-v6.2.0.bin"
                      />
                    </div>

                    <div className="field">
                      <div className="field-label-row">
                        <label htmlFor="whisper-chunk-minutes">Whisper Chunk Size (minutes)</label>
                        <InfoHint
                          label="Long-clip chunking"
                          text="For whisper.cpp: clips longer than this are transcoded to 16 kHz mono, cut into equal chunks with one ffmpeg pass, transcribed chunk by chunk, and stitched back together with offset timestamps — whisper.cpp's VAD misbehaves on long inputs (it can hallucinate one repeated line across the quiet stretches). Each chunk runs under its own timeout; a failed chunk is skipped with a notice. Empty or 0 disables chunking (single pass). whisper.cpp only; needs ffmpeg."
                        />
                      </div>
                      <input
                        id="whisper-chunk-minutes"
                        inputMode="numeric"
                        value={app.whisperChunkMinutes}
                        onChange={(event) => app.setWhisperChunkMinutes(event.target.value)}
                        placeholder="optional: e.g. 5"
                      />
                    </div>
                  </div>
                  {!app.whisperStatus?.available ? (
                    <span className="hint">{app.whisperStatus?.detail ?? 'Detecting local tools…'}</span>
                  ) : null}
                </div>
              </section>
              </>
              ) : null}

              {app.settingsTab === 'general' ? (
              <>
              <section className="settings-section">
                <h3>Storage</h3>
                <div className="storage-list">
                  <div>
                    <span>Root</span>
                    <code>{shortenHomePath(app.storageConfig?.root ?? '~/.atelier')}</code>
                  </div>
                  <div>
                    <span>History</span>
                    <code>{shortenHomePath(app.storageConfig?.history ?? '~/.atelier/history')}</code>
                  </div>
                  <div>
                    <span>Default workspace</span>
                    <div className="workspace-picker">
                      <code>{shortenHomePath(app.toolConfig?.filesystem?.root ?? '~/Documents')}</code>
                      <button onClick={app.chooseToolWorkspace}>Choose</button>
                    </div>
                    <div className="storage-hint">
                      Used when a new conversation doesn't pick its own folder. Each conversation's workspace is locked at creation.
                    </div>
                  </div>
                </div>
                <div className="storage-actions">
                  <button className="danger" onClick={app.purgeArchivedConversations} disabled={app.purgeBusy}>
                    {app.purgeBusy ? 'Deleting...' : app.confirmPurgeArchived ? 'Confirm Delete' : 'Delete Archived Conversations'}
                  </button>
                  {app.confirmPurgeArchived && !app.purgeBusy ? (
                    <button onClick={() => app.setConfirmPurgeArchived(false)}>Cancel</button>
                  ) : null}
                </div>
                {app.confirmPurgeArchived ? (
                  <div className="storage-confirmation">
                    This permanently deletes archived conversations and local assets from ~/.atelier/history.
                  </div>
                ) : null}
              </section>
              <section className="settings-section">
                <h3>Updates</h3>
                <div className="storage-actions">
                  <button onClick={app.checkForUpdates} disabled={app.updateCheckBusy}>
                    {app.updateCheckBusy ? 'Checking...' : 'Check for Updates'}
                  </button>
                </div>
                <div className="storage-hint">
                  Atelier checks for updates automatically once a day and installs only when you choose to.
                </div>
              </section>
              <section className="settings-section">
                <h3>Media generation</h3>
                <div className="field-label-row">
                  <label htmlFor="media-timeout">Generation timeout</label>
                  <InfoHint
                    label="What the timeout covers"
                    text="Bounds each fal.ai or Replicate generation (image, video, audio, upscale, and the video transforms) from submit to delivered file, so an unavailable provider can't hold a turn open indefinitely. On timeout, a job that finished anyway is still delivered; one still running is cancelled so it stops billing. Local tools (ffmpeg, whisper) are unaffected."
                  />
                </div>
                <div className="endpoint-row">
                  <input
                    id="media-timeout"
                    type="number"
                    min={1}
                    max={240}
                    value={app.mediaTimeoutMinutes}
                    onChange={(event) => app.setMediaTimeoutMinutes(Math.min(240, Math.max(1, Math.round(Number(event.target.value) || Math.round(defaultMediaTimeoutSeconds / 60)))))}
                  />
                  <span className="hint">minutes</span>
                </div>
              </section>
              </>
              ) : null}

              {app.settingsTab === 'models' ? (
              <>
              <ModelSelectionPanel
                variant="settings"
                value={app.globalModelSelection}
                onChange={app.patchSettingsModelSelection}
                catalogs={app.modelSelectionCatalogs}
                durationOptions={{video: app.videoDurationOptions, image: app.videoDurationImageOptions, keyframe: app.videoDurationKeyframeOptions, extend: app.videoDurationExtendOptions, sound: app.soundDurationOptions}}
                airplaneMode={app.airplaneMode}
                onOpenLocalTools={() => app.setSettingsTab('tools')}
              />
              </>
              ) : null}

              {app.settingsTab === 'general' ? (
              <section className="settings-section">
                <h3>System</h3>
                <div className="field">
                  <textarea id="system" aria-label="System" value={app.system} onChange={(event) => app.setSystem(event.target.value)} />
                </div>
              </section>
              ) : null}
            </div>
          </>
        ) : app.view === 'conversation-models' ? (
          <>
            <div className="toolbar">
              <div className="toolbar-left">
                <button className="back-button" onClick={app.closeConversationModels}>← Back</button>
                <div className="model-count">
                  {app.convModelsKeys.size
                    ? `${app.convModelsKeys.size} override${app.convModelsKeys.size === 1 ? '' : 's'} ${app.activeConversationID ? 'for this conversation' : 'for your next conversation'}`
                    : 'Following the Settings → Models defaults'}
                </div>
              </div>
              {app.convModelsKeys.size ? (
                <div className="toolbar-right">
                  <button className="back-button" onClick={app.resetAllConversationModels}>Reset all</button>
                </div>
              ) : null}
            </div>
            <div className="settings-screen conversation-models-screen">
              <div className="settings-header">
                <h2>{app.activeConversationID ? 'Models for this conversation' : 'Models for your next conversation'}</h2>
                {app.activeConversationID ? (
                  <p>
                    Changes here apply only to “{app.activeConversation?.title || 'this conversation'}”. Everything you
                    leave alone keeps following the Settings → Models defaults, and later Settings changes reach those
                    fields too.
                  </p>
                ) : (
                  <p>
                    Changes here apply to the conversation you start next — from its very first message. Everything you
                    leave alone keeps following the Settings → Models defaults.
                  </p>
                )}
              </div>
              <ModelSelectionPanel
                variant="conversation"
                value={app.convModelsDraft ?? app.conversationModelSelection}
                onChange={app.patchConversationModels}
                catalogs={app.modelSelectionCatalogs}
                durationOptions={(() => {
                  const withCurrent = (options: string[], current: string) => (options.includes(current) ? options : [current, ...options]);
                  const shown = app.convModelsDraft ?? app.conversationModelSelection;
                  return {
                    video: withCurrent(app.convDurationOptions.video, shown.videoDuration),
                    image: app.convDurationOptions.image,
                    keyframe: app.convDurationOptions.keyframe,
                    extend: app.convDurationOptions.extend,
                    sound: withCurrent(app.convDurationOptions.sound, shown.soundDuration),
                  };
                })()}
                overriddenKeys={app.convModelsKeys}
                onResetField={app.resetConversationModelField}
                airplaneMode={app.airplaneMode}
              />
            </div>
          </>
        ) : app.view === 'usage' ? (
          <UsageReportView
            report={app.usageReport}
            loading={app.usageLoading}
            error={app.usageError}
            onBack={() => app.setView('app')}
            onOpenChild={(child) => {
              // A library report's children are projects, a project report's
              // are conversations — the child scope follows the parent's.
              if (app.usageReport?.scope === 'library') {
                app.openProjectUsageReport(child.id);
              } else {
                app.openConversationUsageReport(child.id);
              }
            }}
          />
        ) : !app.editorOpen ? (
          <>
            <div className="toolbar">
              <div className="toolbar-left">
                {app.composerProjectNames ? (
                  <div className="project-context" title="This chat is scoped to a library project">
                    {app.composerProjectNames.libraryName}{app.composerProjectNames.projectName ? ` › ${app.composerProjectNames.projectName}` : ''}
                  </div>
                ) : null}
              </div>
              <div className="toolbar-right">
                {/* Always visible: a one-click toggle for airplane mode (the
                    Settings → Providers checkbox remains the explanatory home).
                    Amber while the mode is on, neutral gray when off — the
                    tooltip names the current state and the action. The flip
                    persists through the debounced settings save. */}
                <button
                  type="button"
                  className={`assets-toggle airplane-indicator${app.airplaneMode ? ' on' : ''}`}
                  onClick={() => app.setAirplaneMode((current) => !current)}
                  aria-pressed={app.airplaneMode}
                  aria-label={app.airplaneMode ? 'Airplane mode is on — click to turn it off' : 'Airplane mode is off — click to turn it on'}
                  title={app.airplaneMode
                    ? 'Airplane mode is on — Atelier contacts no cloud provider. Click to turn it off.'
                    : 'Airplane mode is off — cloud providers are available. Click to turn it on.'}
                >
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                    <path d="M17.8 19.2 16 11l3.5-3.5C21 6 21.5 4 21 3c-1-.5-3 0-4.5 1.5L13 8 4.8 6.2c-.5-.1-.9.1-1.1.5l-.3.5c-.2.5-.1 1 .3 1.3L9 12l-2 3H4l-1 1 3 2 2 3 1-1v-3l3-2 3.5 5.3c.3.4.8.5 1.3.3l.5-.2c.4-.3.6-.7.5-1.2z" />
                  </svg>
                </button>
                {/* The persisted usage report for the active conversation —
                    the aggregate over every turn on disk, not just the ones
                    loaded in this session. */}
                {app.activeConversationID ? (
                  <button
                    type="button"
                    className="assets-toggle usage-report-toggle"
                    onClick={() => app.openConversationUsageReport(app.activeConversationID)}
                    aria-label="Usage report for this conversation"
                    title="Usage report — cost and tool calls across this conversation"
                  >
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                      <path d="M3 3v18h18" />
                      <path d="M7 15v-4" />
                      <path d="M12 15V7" />
                      <path d="M17 15v-6" />
                    </svg>
                  </button>
                ) : null}
                <ConversationUsage usage={app.modelUsage} media={app.mediaUsage} />
                <button
                  type="button"
                  className={`assets-toggle conversation-models-toggle${app.uiOverridesActive ? ' has-overrides' : ''}`}
                  onClick={app.openConversationModels}
                  aria-label={app.activeConversationID ? 'Model overrides for this conversation' : 'Model overrides for your next conversation'}
                  title={
                    app.activeConversationID
                      ? app.uiOverridesActive
                        ? 'This conversation overrides some model selections — click to change'
                        : 'Override model selections for this conversation'
                      : app.uiOverridesActive
                        ? 'Your next conversation will override some model selections — click to change'
                        : 'Override model selections for the conversation you start next'
                  }
                >
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                      <path d="M12 2v4" />
                      <path d="m15.9 7.5 3.5-2" />
                      <path d="M15.9 16.5l3.5 2" />
                      <path d="M12 18v4" />
                      <path d="m8.1 16.5-3.5 2" />
                      <path d="m8.1 7.5-3.5-2" />
                      <circle cx="12" cy="12" r="3" />
                    </svg>
                  </button>
                  <button
                    type="button"
                    className={`assets-toggle${app.assetsPanelOpen ? ' active' : ''}`}
                    onClick={() => app.setAssetsPanelOpen((open) => !open)}
                    aria-label={app.assetsPanelOpen ? 'Hide assets panel' : 'Show assets panel'}
                    aria-pressed={app.assetsPanelOpen}
                    title={app.assetsPanelOpen ? 'Hide assets' : 'Show assets'}
                >
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                    <path d="m22 11-1.296-1.296a2.4 2.4 0 0 0-3.408 0L11 16" />
                    <path d="M4 8a2 2 0 0 0-2 2v10a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2" />
                    <circle cx="13" cy="7" r="1" fill="currentColor" />
                    <rect x="8" y="2" width="14" height="14" rx="2" />
                  </svg>
                </button>
              </div>
            </div>

            <div className="chat-panel">
              <div
                className="transcript"
                ref={app.transcriptRef}
                onScroll={(event) => {
                  app.shouldFollowTranscriptRef.current = isNearScrollBottom(event.currentTarget);
                }}
              >
                {app.visibleHarnessRun ? <HarnessRunPanel run={app.visibleHarnessRun} /> : null}
                {asArray(app.chat).length === 0 ? (
                  <div className="empty-state">
                    <h2>{app.emptyPrompt?.heading ?? 'What are we making today?'}</h2>
                    <p>{app.emptyPrompt?.sub ?? 'Describe an image, video, or sound to begin.'}</p>
                  </div>
                ) : asArray(app.chat).map((entry) => {
                  const thinkingCollapsed = Boolean(entry.thinking && (app.collapsedThinkingIDs[entry.id] ?? !entry.streaming));
                  return (
                    <article key={entry.id} id={`msg-${entry.id}`} className={`message ${entry.role}`}>
                      <div className="message-meta">
                        {entry.role}{entry.streaming ? ' streaming' : ''}
                        {entry.provider ? <span className="turn-provider-badge">{entry.provider}</span> : null}
                      </div>
                      {entry.images?.length ? (
                        entry.role === 'assistant' ? (
                          <div className="chat-image-results">
                            {entry.images.map((image, index) => (
                              <figure key={`${entry.id}-image-${index}`} className="chat-image-card">
                                <div className="chat-media-stage">
                                  <button
                                    className="chat-image-preview"
                                    type="button"
                                    aria-label={`Open generated image ${index + 1}`}
                                    onClick={() => {
                                      app.setPreviewImage(image);
                                      app.setPreviewEditContext(app.editArtifactIDFromURL(image) && app.activeConversationID
                                        ? {conversationID: app.activeConversationID, artifactID: app.editArtifactIDFromURL(image)!}
                                        : null);
                                    }}
                                  >
                                    <img src={image} alt="Generated result" />
                                  </button>
                                  <InlineMediaActions
                                    downloadTitle="Download image"
                                    onDownload={() => app.saveGeneratedImage(image, index)}
                                    editTitle="Edit image with AI"
                                    editDisabled={app.editorOpening}
                                    onEdit={app.activeConversationID && app.editArtifactIDFromURL(image)
                                      ? () => void app.openImageEditor(app.activeConversationID, app.editArtifactIDFromURL(image)!)
                                      : null}
                                  />
                                </div>
                              </figure>
                            ))}
                          </div>
                        ) : (
                          <div className="thumb-row">
                            {entry.images.map((image, index) => (
                              <button
                                key={`${entry.id}-image-${index}`}
                                className="thumb-button"
                                type="button"
                                aria-label={`Open attached image ${index + 1}`}
                                onClick={() => {
                                  app.setPreviewImage(image);
                                  app.setPreviewEditContext(app.editArtifactIDFromURL(image) && app.activeConversationID
                                    ? {conversationID: app.activeConversationID, artifactID: app.editArtifactIDFromURL(image)!}
                                    : null);
                                }}
                              >
                                <img src={image} alt="" />
                              </button>
                            ))}
                          </div>
                        )
                      ) : null}
                      {entry.role === 'user' && entry.audios?.length ? (
                        <div className="chat-user-audios">
                          {entry.audios.map((audio, index) => (
                            <div key={`${entry.id}-audio-${index}`}>
                              <AudioPlayer src={audio} />
                              {/* The Save* bindings read artifact paths, not data:
                                  URLs, so a just-attached clip (the live transcript's
                                  inline form) hides download until the persisted
                                  artifact URL replaces it. */}
                              <AudioCardActions
                                onDownload={audio.startsWith('/atelier-artifact/') ? () => app.saveGeneratedAudio(audio, index) : null}
                                editDisabled={app.editorOpening}
                                onEdit={app.activeConversationID && app.audioArtifactIDFromURL(audio)
                                  ? () => void app.openAudioEditor(app.activeConversationID, app.audioArtifactIDFromURL(audio)!)
                                  : null}
                              />
                            </div>
                          ))}
                        </div>
                      ) : null}
                      {entry.role === 'user' && entry.videos?.length ? (
                        <div className="chat-user-videos">
                          {entry.videos.map((video, index) => (
                            <figure key={`${entry.id}-video-${index}`} className="chat-video-card">
                              <div className="chat-media-stage">
                                <VideoPlayer src={video} />
                                {/* The video editor's trim stage plays the clip, so a
                                    container WebKit cannot decode gets download only
                                    (VideoPlayer already renders it as a still). */}
                                <InlineMediaActions
                                  downloadTitle="Download video"
                                  onDownload={app.videoArtifactIDFromURL(video) ? () => app.saveGeneratedVideo(video, index) : null}
                                  editTitle="Edit video"
                                  editDisabled={app.editorOpening}
                                  onEdit={app.activeConversationID && app.videoArtifactIDFromURL(video) && !videoThumbnailOnlySrc(video)
                                    ? () => void app.openVideoEditor(app.activeConversationID, app.videoArtifactIDFromURL(video)!)
                                    : null}
                                />
                              </div>
                            </figure>
                          ))}
                        </div>
                      ) : null}
                      {entry.role === 'user' && entry.subtitles?.length ? (
                        <div className="chat-transcript-results">
                          {entry.subtitles.map((subtitle, index) => (
                            <div key={`${entry.id}-subtitle-${index}`} className="chat-transcript-card">
                              <span className="chat-transcript-name">{transcriptChipLabel(subtitle)}</span>
                              <div className="chat-transcript-actions">
                                <button
                                  type="button"
                                  disabled={app.editorOpening || !app.activeConversationID || !app.transcriptArtifactIDFromURL(subtitle)}
                                  title={app.transcriptArtifactIDFromURL(subtitle) ? 'Open in the subtitle editor' : 'Persisted subtitle required'}
                                  onClick={() => {
                                    const artifactID = app.transcriptArtifactIDFromURL(subtitle);
                                    if (artifactID && app.activeConversationID) {
                                      void app.openSubtitleEditor(app.activeConversationID, artifactID);
                                    }
                                  }}
                                >
                                  Edit
                                </button>
                                {/* The Save binding reads artifact paths, not data:
                                    URLs — a just-attached file (the live entry's inline
                                    form) hides download until the persisted artifact
                                    URL replaces it, the same rule the user-audio
                                    block applies. */}
                                <button
                                  type="button"
                                  onClick={() => app.saveGeneratedTranscript(subtitle, index)}
                                  disabled={!subtitle.startsWith('/atelier-artifact/')}
                                >
                                  Download subtitle
                                </button>
                              </div>
                            </div>
                          ))}
                        </div>
                      ) : null}
                      {entry.role === 'assistant' && entry.videos?.length ? (
                        <div className="chat-video-results">
                          {entry.videos.map((video, index) => (
                            <figure key={`${entry.id}-video-${index}`} className="chat-video-card">
                              <div className="chat-media-stage">
                                <VideoPlayer src={video} />
                                <InlineMediaActions
                                  downloadTitle="Download video"
                                  onDownload={() => app.saveGeneratedVideo(video, index)}
                                  editTitle="Edit video"
                                  editDisabled={app.editorOpening}
                                  onEdit={app.activeConversationID && app.videoArtifactIDFromURL(video) && !videoThumbnailOnlySrc(video)
                                    ? () => void app.openVideoEditor(app.activeConversationID, app.videoArtifactIDFromURL(video)!)
                                    : null}
                                />
                              </div>
                            </figure>
                          ))}
                        </div>
                      ) : null}
                      {entry.role === 'assistant' && entry.audios?.length ? (
                        <div className="chat-audio-results">
                          {entry.audios.map((audio, index) => (
                            <figure key={`${entry.id}-audio-${index}`} className="chat-audio-card">
                              <AudioPlayer src={audio} />
                              <AudioCardActions
                                onDownload={() => app.saveGeneratedAudio(audio, index)}
                                editDisabled={app.editorOpening}
                                onEdit={app.activeConversationID && app.audioArtifactIDFromURL(audio)
                                  ? () => void app.openAudioEditor(app.activeConversationID, app.audioArtifactIDFromURL(audio)!)
                                  : null}
                              />
                            </figure>
                          ))}
                        </div>
                      ) : null}
                      {entry.role === 'assistant' && entry.transcripts?.length ? (
                        <div className="chat-transcript-results">
                          {entry.transcripts.map((transcript, index) => (
                            <div key={`${entry.id}-transcript-${index}`} className="chat-transcript-card">
                              <span className="chat-transcript-name">{transcriptChipLabel(transcript)}</span>
                              <div className="chat-transcript-actions">
                                {/* Only .vtt/.srt open in the subtitle editor; a
                                    plain .txt transcript has nothing to edit. */}
                                {app.transcriptArtifactIDFromURL(transcript) && app.activeConversationID ? (
                                  <button
                                    type="button"
                                    disabled={app.editorOpening}
                                    onClick={() => void app.openSubtitleEditor(app.activeConversationID, app.transcriptArtifactIDFromURL(transcript)!)}
                                  >
                                    Edit
                                  </button>
                                ) : null}
                                <button type="button" onClick={() => app.saveGeneratedTranscript(transcript, index)}>{transcript.toLowerCase().endsWith('.srt') ? 'Download subtitles' : 'Download transcript'}</button>
                              </div>
                            </div>
                          ))}
                        </div>
                      ) : null}
                      {entry.markdowns?.length ? (
                        <div className="chat-transcript-results">
                          {entry.markdowns.map((markdown, index) => (
                            <div key={`${entry.id}-markdown-${index}`} className="chat-transcript-card">
                              <span className="chat-transcript-name">📝 {markdown.split('/').pop() || 'Document (.md)'}</span>
                              <div className="chat-transcript-actions">
                                <button
                                  type="button"
                                  disabled={app.editorOpening || !app.activeConversationID || !app.markdownArtifactIDFromURL(markdown)}
                                  title={app.markdownArtifactIDFromURL(markdown) ? (markdown.toLowerCase().endsWith('.fountain') ? 'Open in the screenplay editor' : 'Open in the markdown editor') : 'Persisted document required'}
                                  onClick={() => {
                                    const artifactID = app.markdownArtifactIDFromURL(markdown);
                                    if (artifactID && app.activeConversationID) {
                                      void app.openMarkdownEditor(app.activeConversationID, artifactID);
                                    }
                                  }}
                                >
                                  Edit
                                </button>
                                <button
                                  type="button"
                                  onClick={() => app.saveGeneratedMarkdown(markdown, index)}
                                  disabled={!markdown.startsWith('/atelier-artifact/')}
                                >
                                  Download document
                                </button>
                              </div>
                            </div>
                          ))}
                        </div>
                      ) : null}
                      {entry.compositions?.length ? (
                        <div className="chat-transcript-results">
                          {entry.compositions.map((composition, index) => (
                            <div key={`${entry.id}-composition-${index}`} className="chat-transcript-card">
                              <span className="chat-transcript-name">🎬 Composition · {(composition.split('/').pop() || 'project.json').replace(/\.json$/i, '').slice(0, 16)}</span>
                              <div className="chat-transcript-actions">
                                <button
                                  type="button"
                                  disabled={app.editorOpening || !app.activeConversationID || !app.compositionArtifactIDFromURL(composition)}
                                  title={app.compositionArtifactIDFromURL(composition) ? 'Open this revision in the video composer' : 'Persisted composition required'}
                                  onClick={() => {
                                    const artifactID = app.compositionArtifactIDFromURL(composition);
                                    if (artifactID && app.activeConversationID) {
                                      void app.openCompositionEditor(app.activeConversationID, artifactID);
                                    }
                                  }}
                                >
                                  Edit
                                </button>
                                <button
                                  type="button"
                                  onClick={() => app.saveGeneratedComposition(composition, index)}
                                  disabled={!composition.startsWith('/atelier-artifact/')}
                                >
                                  Download project
                                </button>
                              </div>
                            </div>
                          ))}
                        </div>
                      ) : null}
                      {entry.comics?.length ? (
                        <div className="chat-transcript-results">
                          {entry.comics.map((comic, index) => (
                            <div key={`${entry.id}-comic-${index}`} className="chat-transcript-card">
                              <span className="chat-transcript-name">📚 Comic · {(comic.split('/').pop() || 'comic.json').replace(/\.json$/i, '').slice(0, 16)}</span>
                              <div className="chat-transcript-actions">
                                <button
                                  type="button"
                                  disabled={app.editorOpening || !app.activeConversationID || !app.comicArtifactIDFromURL(comic)}
                                  title={app.comicArtifactIDFromURL(comic) ? 'Open this revision in the comic editor' : 'Persisted comic required'}
                                  onClick={() => {
                                    const artifactID = app.comicArtifactIDFromURL(comic);
                                    if (artifactID && app.activeConversationID) {
                                      void app.openComicEditor(app.activeConversationID, artifactID);
                                    }
                                  }}
                                >
                                  Edit
                                </button>
                                <button
                                  type="button"
                                  onClick={() => app.saveGeneratedComic(comic, index)}
                                  disabled={!comic.startsWith('/atelier-artifact/')}
                                >
                                  Download project
                                </button>
                              </div>
                            </div>
                          ))}
                        </div>
                      ) : null}
                      {entry.thinking ? (
                        <div className="thinking-panel">
                          <button
                            className="thinking-toggle"
                            type="button"
                            aria-expanded={!thinkingCollapsed}
                            onClick={() => app.toggleThinkingCollapsed(entry.id)}
                          >
                            {thinkingCollapsed ? 'Show thinking' : 'Hide thinking'}
                          </button>
                          {thinkingCollapsed ? null : (
                            <div className="thinking markdown-body">
                              <ReactMarkdown remarkPlugins={[remarkGfm]}>
                                {entry.thinking}
                              </ReactMarkdown>
                            </div>
                          )}
                        </div>
                      ) : null}
                      {/* Live activity line while the turn works but no response
                          text has arrived yet — names the harness phase instead
                          of a static "..." (see StreamingActivity). */}
                      {entry.role === 'assistant' && entry.streaming && !entry.content ? (
                        <StreamingActivity run={entry.harnessRun} />
                      ) : null}
                      {entry.role === 'assistant' || entry.role === 'system' ? (
                        <div className={`markdown-body${entry.streaming && entry.content ? ' streaming-live' : ''}`}>
                          <ReactMarkdown remarkPlugins={[remarkGfm]}>
                            {entry.content}
                          </ReactMarkdown>
                        </div>
                      ) : (
                        <p>{entry.content || (entry.streaming ? '...' : '')}</p>
                      )}
                      {(entry.role === 'user' || entry.role === 'assistant') && entry.content ? (
                        <button
                          className="message-copy-button"
                          type="button"
                          aria-label={entry.role === 'user' ? 'Copy prompt' : 'Copy agent response'}
                          title={app.copiedMessageID === entry.id ? 'Copied' : entry.role === 'user' ? 'Copy prompt' : 'Copy response'}
                          onClick={() => app.copyMessage(entry)}
                        >
                          {app.copiedMessageID === entry.id ? '✓' : '⧉'}
                        </button>
                      ) : null}
                      {entry.error ? (
                        <div className="message-actions">
                          <button
                            className="message-retry-button"
                            type="button"
                            disabled={Boolean(app.activeStream)}
                            aria-label="Retry last turn"
                            title="Retry last turn"
                            onClick={() => app.retryFailedTurn(entry.id)}
                          >
                            ↻ Retry
                          </button>
                        </div>
                      ) : null}
                      {entry.role === 'assistant' && entry.harnessRun ? <TurnUsage run={entry.harnessRun} /> : null}
                      {entry.error ? <div className="error">{entry.error}</div> : null}
                    </article>
                  );
                })}
              </div>

              <div
                ref={app.composerRef}
                className={`composer${app.composerDragging ? ' composer--dragging' : ''}`}
                onDragEnter={app.handleComposerDragEnter}
                onDragOver={app.handleComposerDragOver}
                onDragLeave={app.handleComposerDragLeave}
                onDrop={app.handleComposerDrop}
              >
                {app.composerDragging ? (
                  <div className="composer-drop-overlay">Drop media to attach</div>
                ) : null}
                {asArray(app.stagingAttachments).length ? (
                  <div className="composer-staging">
                    {asArray(app.stagingAttachments).map((item) => (
                      <span key={item.key} className="composer-staging-row">
                        <span aria-hidden="true">{item.kind === 'audio' ? '♪' : '▶'}</span>
                        Staging {item.name}… {Math.round(item.fraction * 100)}%
                      </span>
                    ))}
                  </div>
                ) : null}
                {asArray(app.attachments).length ? (
                  <div className="attachment-strip">
                    {asArray(app.attachments).map((item) => (
                      <button
                        key={item.name}
                        onClick={() => {
                          // A staged upload that never rides a turn would
                          // otherwise live out the sweep window on disk.
                          if (item.stagedPath) {
                            void DiscardMediaUpload(item.stagedPath).catch(() => {});
                          }
                          app.setAttachments((items) => items.filter((next) => next.name !== item.name));
                        }}
                      >
                        {item.kind === 'audio' ? (
                          <span className="attachment-audio-chip" aria-hidden="true">
                            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                              <path d="M3 18v-6a9 9 0 0 1 18 0v6" />
                              <path d="M21 19a2 2 0 0 1-2 2h-1a2 2 0 0 1-2-2v-3a2 2 0 0 1 2-2h3zM3 19a2 2 0 0 0 2 2h1a2 2 0 0 0 2-2v-3a2 2 0 0 0-2-2H3z" />
                            </svg>
                          </span>
                        ) : item.kind === 'video' ? (
                          <span className="attachment-video-chip" aria-hidden="true">
                            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                              <path d="m22 8-6 4 6 4V8Z" />
                              <rect width="14" height="12" x="2" y="6" rx="2" ry="2" />
                            </svg>
                          </span>
                        ) : item.kind === 'subtitle' ? (
                          <span className="attachment-subtitle-chip" aria-hidden="true">
                            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                              <rect width="18" height="14" x="3" y="5" rx="2" ry="2" />
                              <path d="M7 15h4" />
                              <path d="M13 15h4" />
                              <path d="M7 11h2" />
                              <path d="M11 11h6" />
                            </svg>
                          </span>
                        ) : item.kind === 'markdown' ? (
                          <span className="attachment-subtitle-chip" aria-hidden="true">
                            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                              <path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8Z" />
                              <path d="M14 2v6h6" />
                              <path d="M8 13h8" />
                              <path d="M8 17h5" />
                            </svg>
                          </span>
                        ) : (
                          <img src={item.src} alt="" />
                        )}
                        <span>{item.name}</span>
                      </button>
                    ))}
                  </div>
                ) : null}
                {app.mentionOpen && app.mentionMatchesState.length ? (
                  <ul className="mention-list" role="listbox" ref={app.mentionListRef}>
                    {app.mentionMatchesState.map((item, index) => (
                      <li key={item.name} role="option" aria-selected={index === app.mentionIndex}>
                        <button
                          type="button"
                          className={index === app.mentionIndex ? 'mention-item active' : 'mention-item'}
                          onMouseDown={(event) => {
                            // mousedown (not click) fires before the textarea
                            // loses focus, so the accept caret-restore works.
                            event.preventDefault();
                            app.acceptMention(index);
                          }}
                          onMouseEnter={() => app.setMentionIndex(index)}
                        >
                          {item.kind === 'image' ? (
                            <img src={item.src} alt="" className="mention-thumb" />
                          ) : (
                            <span className="mention-icon" aria-hidden="true">
                              {item.kind === 'audio' ? '♪' : item.kind === 'subtitle' ? '💬' : item.kind === 'markdown' ? '📝' : '▶'}
                            </span>
                          )}
                          <span className="mention-name">{item.name}</span>
                          {item.hint ? <span className="mention-hint">{item.hint}</span> : null}
                        </button>
                      </li>
                    ))}
                  </ul>
                ) : null}
                <div className="composer-input">
                  {/* Mention chip mirror: renders the same prompt text behind
                      the textarea with resolved @-mentions styled as chips.
                      The mirror paints the input's background and its own
                      text is transparent; the real input stays the native
                      textarea above it, so caret/IME/selection are untouched
                      and only the chip decorations show through. Its
                      typographic and box properties must match
                      .composer-input textarea exactly or chips drift out of
                      alignment. */}
                  <div ref={app.chatPromptMirrorRef} className="composer-input-mirror" aria-hidden="true">
                    {app.renderMentionOverlay(app.prompt)}
                  </div>
                  <textarea
                    ref={app.chatPromptRef}
                    value={app.prompt}
                    onChange={app.handleChatPromptChange}
                    onKeyDown={app.handleChatPromptKeyDown}
                    onPaste={app.handleChatPromptPaste}
                    onScroll={app.handleChatPromptScroll}
                    placeholder="Prompt Atelier..."
                  />
                </div>
                <div className="composer-actions">
                  <div className="composer-actions-left">
                    <button
                      type="button"
                      className="composer-workspace-chip"
                      // The workspace is immutable once a conversation exists.
                      // For a new chat, clicking opens the folder picker to
                      // choose the per-conversation root before the first send.
                      disabled={Boolean(app.activeConversationID) || Boolean(app.activeStream)}
                      onClick={app.chooseDraftWorkspace}
                      aria-label={app.activeConversationID
                        ? `Workspace locked to ${app.displayedWorkspace}`
                        : 'Choose workspace for this conversation'}
                      title={app.activeConversationID
                        ? `Workspace locked to ${app.displayedWorkspace}`
                        : 'Choose workspace for this conversation'}
                    >
                      <svg className="workspace-chip-icon" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                        <path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z" />
                      </svg>
                      <code>{shortenHomePath(app.displayedWorkspace)}</code>
                    </button>
                    {app.composerProjectNames ? (
                      <span
                        className="composer-project-chip"
                        title={`Filed under ${app.composerProjectNames.libraryName}${app.composerProjectNames.projectName ? ` › ${app.composerProjectNames.projectName}` : ''} — the assets panel and @-mentions scope to the library`}
                      >
                        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                          <path d="M4 20h16a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13c0 1.1.9 2 2 2Z" />
                        </svg>
                        <code>{app.composerProjectNames.projectName || app.composerProjectNames.libraryName}</code>
                      </span>
                    ) : null}
                    <label className="file-button" aria-label="Attach file" title="Attach file">
                      {/* Inline icon keeps the composer row compact — the text
                          label was pushing the submit row onto a new line at
                          narrow widths. The title/aria-label preserve meaning.
                          A paperclip reads as generic attach (image or audio)
                          rather than implying image-only. */}
                      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                        <path d="M21.44 11.05l-9.19 9.19a6 6 0 0 1-8.49-8.49l9.19-9.19a4 4 0 0 1 5.66 5.66l-9.2 9.19a2 2 0 0 1-2.83-2.83l8.49-8.48" />
                      </svg>
                      <input type="file" accept="image/*,audio/*,video/*,.mkv,.flv,.avi,.ts,.mpg,.mpeg,.vtt,.srt,application/x-subrip,text/vtt,.md,.markdown,.fountain,text/markdown" multiple onChange={(event) => app.addFiles(event.target.files)} />
                    </label>
                  </div>
                  <div className="composer-submit-row">
                    <div className="composer-model-switch">
                      <label className="model-inline" htmlFor="primary-provider">
                        <span>Provider{app.conversationPrimaryOverride ? ' · this chat' : ''}</span>
                        <div className="model-inline-control">
                          <select
                            id="primary-provider"
                            aria-label="Provider for next message"
                            value={app.composerProvider}
                            onChange={(event) => app.selectComposerProvider(event.target.value as ChatProviderID)}
                          >
                            <option value="ollama">Ollama</option>
                            <option value="openrouter" disabled={app.airplaneMode}>OpenRouter</option>
                            <option value="openai-compatible">OpenAI-compatible (local)</option>
                          </select>
                        </div>
                      </label>
                      <label className="model-inline" htmlFor="primary-model">
                        <span>Model</span>
                        <div className="model-inline-control">
                          <ModelCombobox
                            id="primary-model"
                            ariaLabel="Model for next message"
                            placeholder="Type to filter models..."
                            value={app.composerModel}
                            onChange={app.selectComposerModel}
                            options={app.composerModelOptions}
                            allowCustom={app.composerProvider === 'openai-compatible'}
                          />
                          {app.composerProvider === 'ollama' || app.composerProvider === 'openrouter' ? (
                            <ModelCapabilityLink
                              id="primary-model"
                              modelName={app.composerModel}
                              models={app.composerProvider === 'ollama' ? app.models : undefined}
                              openRouterModels={app.composerProvider === 'openrouter' ? app.openRouterModels : undefined}
                              openID={app.openCapabilityID}
                              setOpenID={app.setOpenCapabilityID}
                              variant="icon"
                            />
                          ) : null}
                        </div>
                      </label>
                    </div>
                    {app.activeStream ? (
                      <button className="danger" onClick={app.stopChat} aria-label="Stop generating" title="Stop">
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor" stroke="none" aria-hidden="true">
                          <rect x="5" y="5" width="14" height="14" rx="2" />
                        </svg>
                      </button>
                    ) : (
                      <button className="primary" onClick={app.submitChat} disabled={!app.prompt.trim() || !app.composerModel || !app.composerModelIsValid} aria-label="Send message" title="Send">
                        <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                          <path d="M3.714 3.048a.498.498 0 0 0-.683.627l2.843 7.627a2 2 0 0 1 0 1.396l-2.842 7.627a.498.498 0 0 0 .682.627l18-8.5a.5.5 0 0 0 0-.904z" />
                          <path d="M6 12h16" />
                        </svg>
                      </button>
                    )}
                  </div>
                </div>
              </div>
            </div>
          </>
        ) : null}
      </section>
      {app.view !== 'app' || app.editorOpen || !app.assetsPanelOpen ? null : (
        <div
          className="assets-resizer"
          role="separator"
          aria-orientation="vertical"
          aria-label="Resize assets panel"
          onMouseDown={(event) => {
            event.preventDefault();
            app.setResizingAssets(true);
          }}
        />
      )}
      {app.view !== 'app' || app.editorOpen || !app.assetsPanelOpen ? null : (
        <aside className="assets-panel" aria-label={app.composerLibraryID ? 'Library assets' : 'Conversation assets'}>
          <div className="assets-panel-header">
            <span className="assets-panel-title">
              {app.composerLibraryID && app.composerProjectNames ? `Assets · ${app.composerProjectNames.libraryName}` : 'Assets'}
            </span>
            {app.activeConversationID ? (
              <button
                type="button"
                className="assets-panel-new-composition"
                disabled={app.editorOpening}
                aria-label="New comic"
                title="Open the comic editor on an unsaved draft for this conversation — add art from the picker inside it"
                onClick={() => void app.openNewComic(app.activeConversationID, [])}
              >
                {/* A comic page: a framed grid with a plus. */}
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                  <rect x="3" y="4" width="18" height="16" rx="1.5" strokeWidth="2" />
                  <path d="M3 12h18M12 4v8" strokeWidth="2" />
                  <path d="M19 15.5v5" strokeWidth="2" />
                  <path d="M16.5 18h5" strokeWidth="2" />
                </svg>
              </button>
            ) : null}
            {app.activeConversationID ? (
              <button
                type="button"
                className="assets-panel-new-composition"
                disabled={app.editorOpening}
                aria-label="New composition"
                title="Open the video composer on an unsaved draft for this conversation — add clips from the picker inside it"
                onClick={() => void app.openNewComposition(app.activeConversationID, [])}
              >
                {/* Staggered clips on tracks + a plus — deliberately
                    media-neutral: the composer hosts audio-only mixes too. */}
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                  <path d="M3 5.5h9" strokeWidth="3" />
                  <path d="M7 12h7" strokeWidth="3" />
                  <path d="M3 18.5h7" strokeWidth="3" />
                  <path d="M18.5 15v6" strokeWidth="2" />
                  <path d="M15.5 18h6" strokeWidth="2" />
                </svg>
              </button>
            ) : null}
            {app.activeConversationID ? (
              <button
                type="button"
                className="assets-panel-new-composition"
                disabled={app.editorOpening}
                aria-label="New audio mix"
                title="Open the composer on an audio-only draft — arrange music, voice, and sound on the audio lanes; a video added later turns it into a video sequence"
                onClick={() => void app.openNewComposition(app.activeConversationID, [], true)}
              >
                {/* A note head + stem with an equalizer bar and a plus — the
                    audio-first sibling of the composition glyph. */}
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                  <circle cx="7.5" cy="16.5" r="2.5" strokeWidth="2" />
                  <path d="M10 16.5V4.5l6 1.5" strokeWidth="2" />
                  <path d="M15 12v2M18 10v4" strokeWidth="2" />
                  <path d="M19.5 17v4M17.5 19h4" strokeWidth="2" />
                </svg>
              </button>
            ) : null}
            <button
              type="button"
              className="assets-panel-close"
              onClick={() => app.setAssetsPanelOpen(false)}
              aria-label="Close assets panel"
              title="Close assets panel"
            >
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round">
                <path d="M18 6 6 18M6 6l12 12" />
              </svg>
            </button>
          </div>
          <div className="assets-list">
            {app.panelAssets.length === 0 ? (
              <p className="assets-empty">
                {app.composerLibraryID
                  ? 'No library assets yet. Uploads and generated media from every chat in this library show up here.'
                  : 'No assets yet. Images, audio, video, and subtitles — attached, generated, or converted — show up here as the conversation produces them.'}
              </p>
            ) : (
              app.panelAssets.map((asset) => (
                <figure key={`${asset.id}-${asset.originTurnId}`} className={`asset-card asset-${asset.kind}`}>
                  <div className="asset-preview">
                    {asset.kind === 'image' && asset.url ? (
                      <button
                        type="button"
                        className="asset-image-button"
                        onClick={() => {
                          app.setPreviewImage(asset.url || '');
                          app.setPreviewEditContext(app.editArtifactIDFromURL(asset.url || '') && asset.conversationId
                            ? {conversationID: asset.conversationId, artifactID: app.editArtifactIDFromURL(asset.url || '') ?? asset.id}
                            : null);
                        }}
                        aria-label="Preview image asset"
                        title="Preview image asset"
                      >
                        <img src={asset.url} alt="" loading="lazy" />
                      </button>
                    ) : asset.kind === 'audio' && asset.url ? (
                      <AudioPlayer src={asset.url} />
                    ) : asset.kind === 'video' && asset.url ? (
                      <LazyPanelVideo src={asset.url} />
                    ) : asset.kind === 'transcript' && asset.url ? (
                      <span className="asset-transcript-chip">
                        {transcriptChipLabel(asset.url)}
                      </span>
                    ) : asset.kind === 'markdown' && asset.url ? (
                      <span className="asset-transcript-chip">{asset.url.toLowerCase().endsWith('.fountain') ? '📝 Screenplay (.fountain)' : '📝 Markdown (.md)'}</span>
                    ) : asset.kind === 'composition' && asset.url ? (
                      <span className="asset-transcript-chip">🎬 Composition project</span>
                    ) : asset.kind === 'comic' && asset.url ? (
                      <span className="asset-transcript-chip">📚 Comic book</span>
                    ) : (
                      <span className="asset-missing">Artifact file is missing on disk</span>
                    )}
                    {/* Sibling of the media (never nested inside the image
                        zoom button — a button cannot contain a button) so the
                        hover reveal overlays every preview variant without
                        stealing the zoom/play gestures. */}
                    {/* Compositions and comics are not @-mentionable (v1):
                        a mention has no slot and the model cannot read the
                        project, so the affordance would be a dead end. */}
                    {asset.kind !== 'composition' && asset.kind !== 'comic' ? (
                      <button
                        type="button"
                        className="asset-reference-button"
                        // Suppress the mousedown default so the click can't
                        // steal focus from the composer: engines that focus
                        // buttons on mousedown (Chromium) would blur the
                        // textarea before onClick fires, defeating the
                        // caret-position insert in insertAssetReference.
                        // Keyboard focus (Tab) is unaffected.
                        onMouseDown={(event) => event.preventDefault()}
                        onClick={() => app.insertAssetReference(asset)}
                        aria-label="Reference this asset in the composer"
                        title="Reference this asset in the composer"
                      >
                        <span aria-hidden="true">@</span>
                      </button>
                    ) : null}
                    {/* Same sibling rule as the reference button; hidden when
                        the artifact is missing on disk — there is nothing to
                        save. */}
                    {asset.url ? (
                      <button
                        type="button"
                        className="asset-download-button"
                        onClick={() => app.downloadAsset(asset)}
                        aria-label="Download asset"
                        title="Download asset"
                      >
                        <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                          <path d="M12 4v11" />
                          <path d="m7 10 5 5 5-5" />
                          <path d="M5 20h14" />
                        </svg>
                      </button>
                    ) : null}
                    {asset.kind === 'image' && asset.url && asset.conversationId ? (
                      <button
                        type="button"
                        className="asset-edit-button"
                        disabled={app.editorOpening}
                        onClick={() => void app.openImageEditor(asset.conversationId, asset.id)}
                        aria-label="Edit image with AI"
                        title="Edit image with AI"
                      >
                        <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                          <path d="M12 20h9" />
                          <path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z" />
                        </svg>
                      </button>
                    ) : null}
                    {/* The video editor's trim stage plays the clip, so a container
                        WebKit cannot decode gets no Edit door here either. */}
                    {asset.kind === 'video' && asset.url && asset.conversationId && !videoThumbnailOnlySrc(asset.url) ? (
                      <button type="button" className="asset-edit-button" disabled={app.editorOpening}
                        onClick={() => void app.openVideoEditor(asset.conversationId, asset.id)}
                        aria-label="Edit video" title="Edit video">
                        <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                          <path d="M12 20h9" />
                          <path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z" />
                        </svg>
                      </button>
                    ) : null}
                    {asset.kind === 'audio' && asset.url && asset.conversationId ? (
                      <button type="button" className="asset-edit-button" disabled={app.editorOpening}
                        onClick={() => void app.openAudioEditor(asset.conversationId, asset.id)}
                        aria-label="Edit audio" title="Edit audio">
                        <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                          <path d="M12 20h9" />
                          <path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z" />
                        </svg>
                      </button>
                    ) : null}
                    {asset.kind === 'transcript' && asset.url && asset.conversationId ? (
                      <button type="button" className="asset-edit-button" disabled={app.editorOpening}
                        onClick={() => void app.openSubtitleEditor(asset.conversationId, asset.id)}
                        aria-label="Edit subtitles" title="Edit subtitles">
                        <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                          <path d="M12 20h9" />
                          <path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z" />
                        </svg>
                      </button>
                    ) : null}
                    {asset.kind === 'markdown' && asset.url && asset.conversationId ? (
                      <button type="button" className="asset-edit-button" disabled={app.editorOpening}
                        onClick={() => void app.openMarkdownEditor(asset.conversationId, asset.id)}
                        aria-label="Edit document" title="Edit document">
                        <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                          <path d="M12 20h9" />
                          <path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z" />
                        </svg>
                      </button>
                    ) : null}
                    {asset.kind === 'comic' && asset.url && asset.conversationId ? (
                      <button type="button" className="asset-edit-button" disabled={app.editorOpening}
                        onClick={() => void app.openComicEditor(asset.conversationId, asset.id)}
                        aria-label="Open comic" title="Open this comic revision in the comic editor">
                        <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                          <path d="M12 20h9" />
                          <path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z" />
                        </svg>
                      </button>
                    ) : null}
                    {asset.kind === 'composition' && asset.url && asset.conversationId ? (
                      <button type="button" className="asset-edit-button" disabled={app.editorOpening}
                        onClick={() => void app.openCompositionEditor(asset.conversationId, asset.id)}
                        aria-label="Open composition" title="Open this composition revision in the video composer">
                        <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                          <path d="M12 20h9" />
                          <path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z" />
                        </svg>
                      </button>
                    ) : null}
                    {/* Navigation needs no artifact on disk — unlike download,
                        a missing file still has a conversation to jump to. */}
                    {asset.conversationId ? (
                      <button
                        type="button"
                        className="asset-conversation-button"
                        onClick={() => app.openAssetConversation(asset)}
                        aria-label={
                          app.composerLibraryID && asset.conversationId !== app.activeConversationID
                            ? `Open ${asset.conversationTitle || 'conversation'}`
                            : 'Jump to this asset in the conversation'
                        }
                        title={
                          app.composerLibraryID && asset.conversationId !== app.activeConversationID
                            ? `Open in ${asset.conversationTitle || 'conversation'}`
                            : 'Jump to the turn that produced this asset'
                        }
                      >
                        <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                          <path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z" />
                        </svg>
                      </button>
                    ) : null}
                  </div>
                  <figcaption>
                    <span className="asset-kind">{asset.kind}</span>
                    <span className="asset-meta">
                      {asset.role === 'user' ? 'attached' : 'generated'} · {app.composerLibraryID ? (asset.conversationTitle || 'chat') : assetTurnLabel(asset.originTurnId)}
                    </span>
                  </figcaption>
                </figure>
              ))
            )}
          </div>
        </aside>
      )}
      {app.discardEditorDraft ? (
        <div className="move-dialog-overlay" role="presentation" onClick={() => app.resolveEditorNavigation(false)}>
          <div className="move-dialog" role="dialog" aria-modal="true" aria-labelledby="discard-video-title"
            onClick={(event) => event.stopPropagation()} onKeyDown={(event) => {
              if (event.key === 'Escape') {event.stopPropagation(); app.resolveEditorNavigation(false);}
            }}>
            <strong id="discard-video-title">Discard unsaved edits?</strong>
            <p>{app.discardEditorDraft === 'subtitle' ? 'Your unexported changes will be lost.' : app.discardEditorDraft === 'markdown' ? 'Your unsaved changes will be lost. Saved versions remain in the conversation.' : app.discardEditorDraft === 'composition' ? 'Your unsaved timeline changes will be lost. Saved revisions remain in the conversation.' : app.discardEditorDraft === 'comic' ? 'Your unsaved comic changes will be lost. Saved revisions remain in the conversation.' : 'Your unrendered changes will be lost. Saved edits remain available.'}</p>
            <button type="button" autoFocus onClick={() => app.resolveEditorNavigation(false)}>Keep editing</button>
            <button type="button" onClick={() => app.resolveEditorNavigation(true)}>Discard changes</button>
          </div>
        </div>
      ) : null}
      {app.previewImage ? (
        <div className="image-preview-overlay" role="presentation" onClick={() => app.setPreviewImage('')}>
          <div
            className="image-preview-dialog"
            role="dialog"
            aria-modal="true"
            aria-label="Attached image preview"
            onClick={(event) => event.stopPropagation()}
          >
            <button className="image-preview-close" type="button" aria-label="Close image preview" onClick={() => app.setPreviewImage('')}>
              ×
            </button>
            <button className="image-preview-download" type="button" aria-label="Download image" title="Download" onClick={() => app.saveGeneratedImage(app.previewImage, 0)}>
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                <path d="M12 4v11" />
                <path d="m7 10 5 5 5-5" />
                <path d="M5 20h14" />
              </svg>
            </button>
            {previewEditContext ? (
              <button
                className="image-preview-edit"
                type="button"
                disabled={app.editorOpening}
                aria-label="Edit image with AI"
                title="Edit image with AI"
                onClick={() => {
                  const context = previewEditContext;
                  app.setPreviewImage('');
                  app.setPreviewEditContext(null);
                  void app.openImageEditor(context.conversationID, context.artifactID);
                }}
              >
                <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                  <path d="M12 20h9" />
                  <path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z" />
                </svg>
              </button>
            ) : null}
            <img src={app.previewImage} alt="Attached preview" />
          </div>
        </div>
      ) : null}
      {app.notesDialogProject ? (
        <ProjectNotesDialog
          project={app.notesDialogProject}
          value={app.notesDraft}
          saving={app.notesSaving}
          onChange={app.setNotesDraft}
          onSave={() => void app.saveProjectNotes()}
          onClose={app.closeProjectNotes}
        />
      ) : null}
      {moveDialogConversation ? (
        <MoveConversationDialog
          conversation={moveDialogConversation}
          groups={app.moveTargetGroups}
          onMove={(projectID) => {
            // Close first so the picker never lingers over the moved row's
            // reveal; moveConversation owns refresh + error reporting.
            const conversation = moveDialogConversation;
            app.setMoveDialogConversation(null);
            void app.moveConversation(conversation, projectID);
          }}
          onClose={() => app.setMoveDialogConversation(null)}
        />
      ) : null}
    </main>
  );
}

export default App;