import {useEffect} from 'react';
import {main} from '../../wailsjs/go/models';
import {projectNotesMaxLength} from '../modelDefaults';

// ProjectNotesDialog edits a project's standing instruction block — the note
// the harness injects into its planner prompt for every conversation in the
// project, shaping generation calls (aspect ratios, styles) until the user's
// message says otherwise. Modal chrome mirrors MoveConversationDialog: Escape
// and an overlay press close, the body stops that press's propagation, and
// Cmd/Ctrl+Enter saves from the textarea.
function ProjectNotesDialog(props: {
  project: main.ProjectSummary;
  value: string;
  saving: boolean;
  onChange: (value: string) => void;
  onSave: () => void;
  onClose: () => void;
}) {
  const remaining = projectNotesMaxLength - props.value.length;

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        props.onClose();
      }
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [props.onClose]);

  return (
    <div className="move-dialog-overlay" role="presentation" onClick={props.onClose}>
      <div
        className="project-notes-dialog"
        role="dialog"
        aria-modal="true"
        aria-label={`Notes for ${props.project.name}`}
        onClick={(event) => event.stopPropagation()}
      >
        <div className="move-dialog-head">
          <span className="move-dialog-title">Project notes</span>
          <span className="move-dialog-subtitle" title={props.project.name}>{props.project.name}</span>
        </div>
        <p className="project-notes-hint">
          Standing guidance the workshop applies to every chat in this project — e.g. “render all
          video requests at 9:16 in pixar style 3D animation”. An explicit instruction in a message
          still wins.
        </p>
        <textarea
          className="project-notes-input"
          value={props.value}
          maxLength={projectNotesMaxLength}
          onChange={(event) => props.onChange(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === 'Enter' && (event.metaKey || event.ctrlKey)) {
              event.preventDefault();
              props.onSave();
            }
          }}
          placeholder="Project instructions…"
          aria-label="Project instructions"
          autoFocus
        />
        <div className="project-notes-actions">
          <span className="project-notes-count">{remaining} characters left</span>
          <div className="project-notes-buttons">
            <button type="button" onClick={props.onClose} disabled={props.saving}>Cancel</button>
            <button type="button" className="project-notes-save" onClick={props.onSave} disabled={props.saving}>
              {props.saving ? 'Saving…' : 'Save'}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}

export {ProjectNotesDialog};
