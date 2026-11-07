import {MoveConversationDialog} from '../dialogs/MoveConversationDialog';
import {ProjectNotesDialog} from '../dialogs/ProjectNotesDialog';
import {AppModel} from '../app/useAppModel';

// Values the JSX null-checks and then reads in a callback stay const locals:
// TypeScript narrows a const through a closure, never a property access.
export function AppOverlays({app}: {app: AppModel}) {
  const {
    previewEditContext,
    moveDialogConversation,
  } = app;
  return (
    <>
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
    </>
  );
}
