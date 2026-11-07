import {main} from '../../wailsjs/go/models';
import {asArray, formatModelSize} from '../utils';
import {ContainerNameInput} from '../dialogs/ContainerNameInput';
import {AnchoredMenu} from '../dialogs/AnchoredMenu';
import {AppModel} from '../app/useAppModel';

export function Sidebar({app}: {app: AppModel}) {
  return (
    <>
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
    </>
  );
}
