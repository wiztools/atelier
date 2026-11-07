import {useEffect, useMemo, useState} from 'react';
import {ListConversationAssets, ListLibraryAssets} from '../../wailsjs/go/main/App';
import {main} from '../../wailsjs/go/models';
import {asArray} from '../utils';

// useAssets owns the assets panel's contents: whether it is open, the active
// conversation's derived media assets, and — when the conversation is
// project-scoped — every asset in its library, plus the panelAssets merge the
// panel renders and the fetch that re-runs when a turn finishes (the index
// derives from persisted history, so it lags until the turn is saved). The
// active conversation id, the composer's library scope, and the turn-finished
// tick arrive from App, the composer's scope, and useChatStream.
function useAssets({activeConversationID, composerLibraryID, assetsRefreshTick}: {
  activeConversationID: string;
  composerLibraryID: string;
  assetsRefreshTick: number;
}) {
  // Assets panel: closed by default; lists the active conversation's derived
  // media assets (ListConversationAssets) — or, when the conversation is
  // project-scoped, every asset in its library (ListLibraryAssets).
  // assetsRefreshTick re-runs the fetch when a turn finishes — the index
  // derives from persisted history, so it lags until the turn is saved.
  const [assetsPanelOpen, setAssetsPanelOpen] = useState(false);
  const [conversationAssets, setConversationAssets] = useState<main.ConversationAsset[]>([]);
  const [libraryAssets, setLibraryAssets] = useState<main.ConversationAsset[]>([]);

  useEffect(() => {
    if (!activeConversationID) {
      setConversationAssets([]);
      return;
    }
    let cancelled = false;
    ListConversationAssets(activeConversationID)
      .then((assets) => {
        if (!cancelled) setConversationAssets(asArray(assets));
      })
      .catch(() => {
        if (!cancelled) setConversationAssets([]);
      });
    return () => {
      cancelled = true;
    };
  }, [activeConversationID, assetsRefreshTick]);

  // Library-scoped assets refresh like the conversation's do: the fold derives
  // from persisted history, so a finished turn's artifacts need a re-fetch.
  useEffect(() => {
    if (!composerLibraryID) {
      setLibraryAssets([]);
      return;
    }
    let cancelled = false;
    ListLibraryAssets(composerLibraryID)
      .then((assets) => {
        if (!cancelled) setLibraryAssets(asArray(assets));
      })
      .catch(() => {
        if (!cancelled) setLibraryAssets([]);
      });
    return () => {
      cancelled = true;
    };
  }, [composerLibraryID, assetsRefreshTick]);

  // Panel view of the asset list: library-scoped when the composer sits in a
  // project context (every asset across the library's conversations), else the
  // active conversation's own. Deduped by ID (a @-mention re-reference adds
  // another entry for the same artifact — keep the newest turn's). The
  // conversation listing arrives oldest-first (reversed to newest-first); the
  // library fold already arrives newest-first.
  const panelAssets = useMemo(() => {
    const source = composerLibraryID ? libraryAssets : conversationAssets;
    const byID = new Map<string, main.ConversationAsset>();
    for (const asset of source) {
      byID.set(asset.id, asset);
    }
    const list = [...byID.values()];
    return composerLibraryID ? list : list.reverse();
  }, [composerLibraryID, libraryAssets, conversationAssets]);

  return {
    assetsPanelOpen,
    libraryAssets,
    panelAssets,
    setAssetsPanelOpen,
  };
}

export {useAssets};
