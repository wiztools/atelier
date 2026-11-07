import {main} from '../../wailsjs/go/models';
import {ImageEditor} from '../editor/ImageEditor';
import {VideoEditor} from '../editor/VideoEditor';
import {AudioEditor} from '../editor/AudioEditor';
import {SubtitleEditor} from '../editor/SubtitleEditor';
import {MarkdownEditor} from '../editor/MarkdownEditor';
import {CompositionEditor} from '../editor/CompositionEditor';
import {ComicEditor} from '../editor/ComicEditor';
import {DocumentCompare} from '../editor/DocumentCompare';
import {AppModel} from '../app/useAppModel';

// Values the JSX null-checks and then reads in a callback stay const locals:
// TypeScript narrows a const through a closure, never a property access.
export function EditorSlot({app}: {app: AppModel}) {
  const {
    videoEditorHandle,
    audioEditorHandle,
    subtitleEditorHandle,
    markdownEditorHandle,
    compositionEditorHandle,
    comicEditorHandle,
  } = app;
  return (
    <>
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
    </>
  );
}
