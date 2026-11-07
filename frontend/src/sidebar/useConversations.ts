import {useState} from 'react';
import {ListConversations, PurgeArchivedConversations, UpdateConversationTitle} from '../../wailsjs/go/main/App';
import {main} from '../../wailsjs/go/models';
import {notifications, notifyError, notifySuccess} from '../notifications';
import type {ComposerDraft} from '../types';
import {compactHistoryLimit, expandedHistoryBatchSize} from '../uiLayout';
import {asArray} from '../utils';
import {copyTextWithTextarea, isNearScrollBottom} from '../dom';

// useConversations owns the sidebar's conversation list: the listing and its
// expand-in-batches scroll, the per-row title editor, the copy-ID affordance,
// the row menu and Move-to-project dialog state, the Chats section's collapse,
// and purging archived conversations. Opening and archiving a conversation stay
// in App: they reach the transcript, the stream, the composer, and the
// libraries tree. copyResetRef is App's: the transcript's copy button shares
// the one copied-indicator timer.
function useConversations({composerDraftsRef, copyResetRef}: {
  composerDraftsRef: {current: Record<string, ComposerDraft>};
  copyResetRef: {current: number | null};
}) {
  const [copiedConversationID, setCopiedConversationID] = useState('');
  const [conversations, setConversations] = useState<main.ConversationSummary[]>([]);
  const [historyExpanded, setHistoryExpanded] = useState(false);
  const [visibleHistoryCount, setVisibleHistoryCount] = useState(compactHistoryLimit);

  // chatsOpen collapses the standalone Chats section to just its header, the
  // same affordance the Libraries section below it has.
  const [chatsOpen, setChatsOpen] = useState(true);

  const [editingTitleID, setEditingTitleID] = useState('');
  const [editingTitle, setEditingTitle] = useState('');
  const [openHistoryMenuID, setOpenHistoryMenuID] = useState('');
  // The conversation whose "Move to…" picker is open; null keeps the dialog
  // unmounted so its filter state resets on every open.
  const [moveDialogConversation, setMoveDialogConversation] = useState<main.ConversationSummary | null>(null);

  const [purgeBusy, setPurgeBusy] = useState(false);
  const [confirmPurgeArchived, setConfirmPurgeArchived] = useState(false);

  // The standalone chats list hides project-scoped conversations — they live
  // under their library's project rows instead, so they are never orphaned in
  // neither place.
  const conversationList = asArray(conversations).filter((item) => !item.projectId);
  const visibleConversations = historyExpanded
    ? conversationList.slice(0, visibleHistoryCount)
    : conversationList.slice(0, compactHistoryLimit);
  const hasMoreConversations = visibleConversations.length < conversationList.length;

  // refreshConversations runs from the chat:chunk listener and after every
  // write into a conversation, so a failing listing toasts under one key —
  // repeats count in place instead of stacking — and the next successful
  // load dismisses it.
  async function refreshConversations(): Promise<main.ConversationSummary[]> {
    try {
      const nextConversations = await ListConversations();
      setConversations(asArray(nextConversations));
      setVisibleHistoryCount((current) => historyExpanded ? Math.max(current, compactHistoryLimit) : compactHistoryLimit);
      notifications.dismissKey('conversations-load');
      return asArray(nextConversations);
    } catch (error) {
      notifyError("Couldn't load conversations", error, {key: 'conversations-load'});
      setConversations([]);
      return [];
    }
  }

  function showMoreConversations() {
    setHistoryExpanded(true);
    setVisibleHistoryCount((current) => Math.max(current, compactHistoryLimit) + expandedHistoryBatchSize);
  }

  function handleHistoryScroll(event: React.UIEvent<HTMLDivElement>) {
    if (!historyExpanded || !hasMoreConversations || !isNearScrollBottom(event.currentTarget, 96)) {
      return;
    }
    setVisibleHistoryCount((current) => Math.min(current + expandedHistoryBatchSize, conversationList.length));
  }

  function startEditingConversationTitle(conversation: main.ConversationSummary) {
    setOpenHistoryMenuID('');
    setEditingTitleID(conversation.id);
    setEditingTitle(conversation.title);
  }

  function cancelEditingConversationTitle() {
    setEditingTitleID('');
    setEditingTitle('');
  }

  async function saveConversationTitle(conversation: main.ConversationSummary) {
    const title = editingTitle.trim();
    if (!title || title === conversation.title) {
      cancelEditingConversationTitle();
      return;
    }
    // Optimistic: the sidebar shows the new title immediately; the backend
    // response reconciles the normalized form (whitespace collapsed, capped
    // length), and a failure restores the previous title.
    setConversations((items) =>
      asArray(items).map((item) => item.id === conversation.id ? main.ConversationSummary.createFrom({...item, title}) : item),
    );
    cancelEditingConversationTitle();
    try {
      const updated = await UpdateConversationTitle(conversation.id, title);
      setConversations((items) =>
        asArray(items).map((item) => item.id === updated.id ? main.ConversationSummary.createFrom({...item, ...updated}) : item),
      );
    } catch (error) {
      notifyError("Couldn't rename the conversation", error);
      setConversations((items) =>
        asArray(items).map((item) => item.id === conversation.id ? main.ConversationSummary.createFrom({...item, title: conversation.title}) : item),
      );
    }
  }

  function handleConversationTitleKeyDown(event: React.KeyboardEvent<HTMLInputElement>, conversation: main.ConversationSummary) {
    if (event.key === 'Enter') {
      event.preventDefault();
      void saveConversationTitle(conversation);
    }
    if (event.key === 'Escape') {
      event.preventDefault();
      cancelEditingConversationTitle();
    }
  }

  async function copyConversationID(conversation: main.ConversationSummary) {
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(conversation.id);
      } else {
        copyTextWithTextarea(conversation.id);
      }
      setCopiedConversationID(conversation.id);
      if (copyResetRef.current) {
        window.clearTimeout(copyResetRef.current);
      }
      copyResetRef.current = window.setTimeout(() => setCopiedConversationID(''), 1600);
    } catch (error) {
      notifyError("Couldn't copy to the clipboard", error);
    }
  }

  async function purgeArchivedConversations() {
    if (purgeBusy) {
      return;
    }
    if (!confirmPurgeArchived) {
      setConfirmPurgeArchived(true);
      return;
    }
    try {
      setPurgeBusy(true);
      const result = await PurgeArchivedConversations();
      const remaining = await refreshConversations();
      // Drop any session drafts for conversations that no longer exist, but
      // always keep the '' new-chat key.
      const liveIDs = new Set(remaining.map((item) => item.id));
      for (const id of Object.keys(composerDraftsRef.current)) {
        if (id !== '' && !liveIDs.has(id)) {
          delete composerDraftsRef.current[id];
        }
      }
      setConfirmPurgeArchived(false);
      notifySuccess(`${result.deletedConversations} archived ${result.deletedConversations === 1 ? 'conversation' : 'conversations'} and ${result.deletedAssets} ${result.deletedAssets === 1 ? 'asset' : 'assets'} deleted.`);
    } catch (error) {
      notifyError("Couldn't purge archived conversations", error);
    } finally {
      setPurgeBusy(false);
    }
  }

  return {
    cancelEditingConversationTitle,
    chatsOpen,
    confirmPurgeArchived,
    conversationList,
    conversations,
    copiedConversationID,
    copyConversationID,
    editingTitle,
    editingTitleID,
    handleConversationTitleKeyDown,
    handleHistoryScroll,
    hasMoreConversations,
    moveDialogConversation,
    openHistoryMenuID,
    purgeArchivedConversations,
    purgeBusy,
    refreshConversations,
    saveConversationTitle,
    setChatsOpen,
    setConfirmPurgeArchived,
    setConversations,
    setEditingTitle,
    setMoveDialogConversation,
    setOpenHistoryMenuID,
    showMoreConversations,
    startEditingConversationTitle,
    visibleConversations,
  };
}

export {useConversations};
