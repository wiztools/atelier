import {useMemo, useState} from 'react';
import {main} from '../../wailsjs/go/models';
import {linkedMediaProvenance} from './subtitleModel';

// The in-workspace media picker, generalized from the subtitle editor's
// linked-media dialog: the caller folds the dual-scope candidates
// (conversation assets widened to the library when the conversation belongs
// to a project), rows carry the same provenance labels, a filter box narrows
// by conversation or id, newest first. Where the subtitle picker selects ONE
// playback source, this one is multi-add: every row is an "Add" action, and a
// row already in the composition says how many times while staying
// re-addable — a second copy of a clip is a legitimate edit.
export type MediaPickerAsset = main.ConversationAsset;

export function MediaPicker(props: {
  assets: MediaPickerAsset[];
  editedConversationID: string;
  // How many times each asset id already appears in the composition.
  refCounts: Record<string, number>;
  // Asset ids currently being resolved and measured (their rows show busy).
  busyIDs: string[];
  error?: string;
  onAdd: (asset: MediaPickerAsset) => void;
  onClose: () => void;
}) {
  const [query, setQuery] = useState('');

  const candidates = useMemo(() => {
    const needle = query.trim().toLowerCase();
    const seen = new Set<string>();
    return props.assets
      .filter((asset) => (asset.kind === 'video' || asset.kind === 'audio') && asset.url && !seen.has(asset.id) && seen.add(asset.id))
      .filter((asset) => !needle || (asset.conversationTitle || '').toLowerCase().includes(needle) || asset.id.toLowerCase().includes(needle))
      .sort((a, b) => ((a.createdAt ?? '') === (b.createdAt ?? '') ? (a.id < b.id ? 1 : -1) : (a.createdAt ?? '') < (b.createdAt ?? '') ? 1 : -1));
  }, [props.assets, query]);

  return (
    <div className="composition-media-picker" role="dialog" aria-label="Add media to the composition">
      <div className="composition-media-picker-head">
        <input
          type="text"
          value={query}
          autoFocus
          placeholder="Filter by conversation or id…"
          onChange={(event) => setQuery(event.target.value)}
        />
        <button type="button" onClick={props.onClose} title="Close the picker">Done</button>
      </div>
      {props.error ? <div className="editor-error">{props.error}</div> : null}
      <ul>
        {candidates.map((asset) => {
          const count = props.refCounts[asset.id] ?? 0;
          const busy = props.busyIDs.includes(asset.id);
          return (
            <li key={`${asset.id}-${asset.conversationId}`}>
              <button
                type="button"
                className={count > 0 ? 'added' : ''}
                disabled={busy}
                title={asset.kind === 'video' ? 'Add this clip to the end of the video lane' : 'Add this recording to the last audio lane'}
                onClick={() => props.onAdd(asset)}
              >
                <span className="composition-picker-name">{asset.id.slice(0, 16)} · {asset.kind}</span>
                <span className="composition-picker-provenance">
                  {asset.role === 'user' ? 'attached' : 'generated'} · {linkedMediaProvenance(asset, props.editedConversationID)}
                </span>
                <span className="composition-picker-state">{busy ? 'measuring…' : count > 0 ? `in composition ×${count} · add again` : '+ add'}</span>
              </button>
            </li>
          );
        })}
        {candidates.length === 0 ? <li><span className="composition-picker-empty">No video or audio assets found.</span></li> : null}
      </ul>
    </div>
  );
}
