// EditorHeader is the editor shell's top bar: the close button, the
// breadcrumb (conversation title plus session state — linked parent, missing
// parent, or unsaved draft), and the busy label. Shared by the image and
// video editors so the next editor composes the same chrome instead of
// re-deriving it.

export function EditorHeader(props: {
  title: string;
  sessionID?: string;
  parentAvailable?: boolean;
  parentConversationID?: string;
  busyLabel?: string;
  onClose: () => void;
  onOpenParent: (conversationID: string) => void;
}) {
  return (
    <header className="editor-header">
      <button type="button" className="editor-back" onClick={props.onClose} aria-label="Close editor">
        ← Close
      </button>
      <div className="editor-breadcrumb">
        <span className="editor-crumb-title">
          {props.title}
        </span>
        {props.sessionID ? (
          props.parentAvailable ? (
            <button type="button" className="editor-crumb-link" onClick={() => props.onOpenParent(props.parentConversationID ?? '')}>
              Open original chat
            </button>
          ) : (
            <span className="editor-crumb-missing">original unavailable</span>
          )
        ) : (
          <span className="editor-crumb-draft">unsaved draft</span>
        )}
      </div>
      <div className="editor-header-status">
        {props.busyLabel ? <span className="editor-busy">{props.busyLabel}</span> : null}
      </div>
    </header>
  );
}
