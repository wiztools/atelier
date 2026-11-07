// The download/edit affordances on the chat transcript's media cards:
// hover pills over image/video viewports, a labeled row under audio.

// The hover-revealed affordance cluster on the chat transcript's inline media
// cards: the download/edit pair the maximized image preview floats, moved onto
// the asset's own viewport so the caption row of labeled buttons goes away.
// Same icon vocabulary as the assets panel's hover pills (arrow = download,
// pencil = edit); the cluster anchors to the card's top-right corner via the
// .chat-media-stage wrapper and renders as a SIBLING of the media, never a
// child — a button cannot contain a button, and a sibling's click can't fall
// through to the zoom/play gesture underneath. onEdit is omitted (not merely
// hidden) when the media has no editable artifact, so a cluster is never a
// lone disabled gap; audio without a persisted artifact (the live
// transcript's data: URL) carries download only.
function InlineMediaActions({downloadTitle, onDownload, editTitle, onEdit, editDisabled}: {
  downloadTitle: string;
  onDownload: (() => void) | null;
  editTitle?: string;
  onEdit?: (() => void) | null;
  editDisabled?: boolean;
}) {
  if (!onDownload && !onEdit) return null;
  return (
    <div className="chat-media-actions">
      {onEdit ? (
        <button
          type="button"
          className="chat-media-action"
          disabled={editDisabled}
          aria-label={editTitle}
          title={editTitle}
          onClick={onEdit}
        >
          <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <path d="M12 20h9" />
            <path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z" />
          </svg>
        </button>
      ) : null}
      {onDownload ? (
        <button
          type="button"
          className="chat-media-action"
          aria-label={downloadTitle}
          title={downloadTitle}
          onClick={onDownload}
        >
          <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <path d="M12 4v11" />
            <path d="m7 10 5 5 5-5" />
            <path d="M5 20h14" />
          </svg>
        </button>
      ) : null}
    </div>
  );
}

// The audio cards' action row: the labeled pair the image/video cards float
// as hover pills over their media's corner. An audio card's "viewport" is
// WebKit's own control bar, which packs its time and volume controls into
// exactly that corner, so audio renders an always-visible row under the
// player instead — the transcript card's idiom. Each button renders only
// when its action exists (a data: URL attachment has no persisted artifact
// to download).
function AudioCardActions({onDownload, onEdit, editDisabled}: {
  onDownload: (() => void) | null;
  onEdit?: (() => void) | null;
  editDisabled?: boolean;
}) {
  if (!onDownload && !onEdit) return null;
  return (
    <div className="chat-audio-actions">
      {onEdit ? (
        <button type="button" disabled={editDisabled} onClick={onEdit}>
          Edit audio
        </button>
      ) : null}
      {onDownload ? (
        <button type="button" onClick={onDownload}>
          Download
        </button>
      ) : null}
    </div>
  );
}

export {InlineMediaActions, AudioCardActions};
