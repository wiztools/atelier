import {useEffect, useState} from 'react';
import {main} from '../../wailsjs/go/models';
import {MoveTargetGroup} from '../types';

// MoveConversationDialog is the ⋮ menu's "Move to…" picker: a modal listing
// every library's projects under a filter, plus a return-to-standalone
// option for chats already inside a project. It replaces the menu's inline
// flat list, which grew one button per project and buried Rename/Archive.
// One click performs the move and closes — the dialog is a picker, not a
// confirmation, and a wrong pick is undone by moving back. The chat's
// current home is omitted rather than disabled: choosing it again would be
// a no-op either way.
function MoveConversationDialog(props: {
  conversation: main.ConversationSummary;
  groups: MoveTargetGroup[];
  onMove: (projectID: string) => void;
  onClose: () => void;
}) {
  const [filter, setFilter] = useState('');
  const normalized = filter.trim().toLowerCase();

  // Escape mirrors the anchored menus' close key; the overlay press below
  // closes too, and the dialog body stops that click's propagation.
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        props.onClose();
      }
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [props.onClose]);

  const match = (libraryName: string, projectName: string) =>
    !normalized
    || libraryName.toLowerCase().includes(normalized)
    || projectName.toLowerCase().includes(normalized);
  const showStandalone = Boolean(props.conversation.projectId)
    && (!normalized || 'standalone'.includes(normalized) || 'chats'.includes(normalized));
  const visibleGroups = props.groups
    .map((group) => ({
      ...group,
      projects: group.projects.filter((project) =>
        project.projectID !== props.conversation.projectId
        && match(group.libraryName, project.name)),
    }))
    .filter((group) => group.projects.length > 0);
  const hasTargets = showStandalone || visibleGroups.length > 0;

  return (
    <div className="move-dialog-overlay" role="presentation" onClick={props.onClose}>
      <div
        className="move-dialog"
        role="dialog"
        aria-modal="true"
        aria-label={`Move ${props.conversation.title}`}
        onClick={(event) => event.stopPropagation()}
      >
        <div className="move-dialog-head">
          <span className="move-dialog-title">Move chat</span>
          <span className="move-dialog-subtitle" title={props.conversation.title}>{props.conversation.title}</span>
        </div>
        <input
          className="move-dialog-filter"
          value={filter}
          onChange={(event) => setFilter(event.target.value)}
          placeholder="Filter projects…"
          aria-label="Filter projects"
          autoFocus
        />
        <div className="move-dialog-list">
          {showStandalone ? (
            <button type="button" className="move-dialog-move" onClick={() => props.onMove('')}>
              <span>Standalone</span>
              <small>Chats section</small>
            </button>
          ) : null}
          {visibleGroups.map((group) => (
            <div key={group.libraryID} className="move-dialog-group">
              <div className="move-dialog-group-label">{group.libraryName}</div>
              {group.projects.map((project) => (
                <button key={project.projectID} type="button" className="move-dialog-move" onClick={() => props.onMove(project.projectID)}>
                  <span>{project.name}</span>
                </button>
              ))}
            </div>
          ))}
          {!hasTargets ? <div className="move-dialog-empty">No matching projects.</div> : null}
        </div>
      </div>
    </div>
  );
}

export {MoveConversationDialog};
