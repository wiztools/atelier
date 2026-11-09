import {main} from '../../wailsjs/go/models';

// DetachedDraft is the creation context a composer or comic draft captures
// when it opens with no conversation — the assets panel's ＋ actions on a
// brand-new chat, which has no id until its first send. Nothing persists
// while the draft is open; its first save hands this back as the request's
// lifecycle and the backend gives birth to the host conversation from it —
// in the "New chat"'s project, workspace, and model selection, exactly as
// pressing Enter would have. Captured at open, not read at save: the pending
// project is shared app state that other actions clear.
export type DetachedDraft = {
  projectID: string;
  // The picker's scope while no conversation exists — the project's library
  // ('' for a standalone new chat, whose picker then starts empty).
  libraryID: string;
  workspace: string;
  chatModel: string;
  chatSystem: string;
  modelOverrides: main.ConversationModelOverrides | null;
};

// detachedDraftLifecycle shapes the captured context into the save request's
// lifecycle (EditorSaveLifecycle), omitting what was never set.
export function detachedDraftLifecycle(draft: DetachedDraft): main.EditorSaveLifecycle {
  return main.EditorSaveLifecycle.createFrom({
    ...(draft.projectID ? {projectId: draft.projectID} : {}),
    ...(draft.workspace ? {workspace: draft.workspace} : {}),
    ...(draft.chatModel ? {chatModel: draft.chatModel} : {}),
    ...(draft.chatSystem ? {chatSystem: draft.chatSystem} : {}),
    ...(draft.modelOverrides ? {modelOverrides: draft.modelOverrides} : {}),
  });
}
