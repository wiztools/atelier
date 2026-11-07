import {useEffect, useMemo, useRef, useState} from 'react';
import {CancelStream, ResolveToolPermission} from '../../wailsjs/go/main/App';
import {main} from '../../wailsjs/go/models';
import {EventsOn} from '../../wailsjs/runtime/runtime';
import {notifyError} from '../notifications';
import {
  ConversationKind,
  ChatEntry,
  ChatChunk,
  ChatStreamDraft,
  ToolPermissionEvent,
  InFlightConversation,
  HarnessRunView,
  HarnessRunEventView,
} from '../types';
import {parseHarnessRun, summarizeModelUsage, summarizeMediaUsage} from '../usage/usageModel';
import {copyTextWithTextarea} from '../dom';

// useChatStream owns the live transcript and the streaming machinery behind
// it: the active conversation's chat entries, which requests are in flight per
// conversation, the per-request draft buffers chunks accumulate into, the
// harness-run snapshots, the pending tool-permission prompts, and the backend
// listeners that feed them — plus stopping a stream, copying a message, the
// thinking toggles, and the conversation's usage folds. Sending, retrying, and
// hydrating a conversation stay in App: they join the composer, the
// conversation list, the editors, and the model selection.
function useChatStream({setActiveConversationID, setAssetsRefreshTick, copyResetRef, refreshConversationsOnChunk, bumpLibrariesOnChunk}: {
  setActiveConversationID: React.Dispatch<React.SetStateAction<string>>;
  setAssetsRefreshTick: React.Dispatch<React.SetStateAction<number>>;
  copyResetRef: {current: number | null};
  refreshConversationsOnChunk: () => Promise<main.ConversationSummary[]>;
  bumpLibrariesOnChunk: () => void;
}) {
  const [chat, setChat] = useState<ChatEntry[]>([]);
  const chatIsEmpty = chat.length === 0;

  const [collapsedThinkingIDs, setCollapsedThinkingIDs] = useState<Record<string, boolean>>({});
  const [copiedMessageID, setCopiedMessageID] = useState('');

  const [activeStream, setActiveStream] = useState<string | null>(null);
  const [inFlightConversations, setInFlightConversations] = useState<Record<string, InFlightConversation>>({});
  const [toolPermissions, setToolPermissions] = useState<ToolPermissionEvent[]>([]);

  // Updates wait out any running conversation; both the banner's queue and
  // the backend's InstallUpdate gate read this shape of "busy".
  const anyConversationInFlight = activeStream !== null || Object.keys(inFlightConversations).length > 0;

  const transcriptRef = useRef<HTMLDivElement | null>(null);
  const shouldFollowTranscriptRef = useRef(true);
  const visibleStreamRef = useRef<string | null>(null);

  const inFlightConversationsRef = useRef<Record<string, InFlightConversation>>({});
  const requestConversationRef = useRef<Record<string, {conversationID: string; kind: ConversationKind}>>({});
  const chatStreamDraftsRef = useRef<Record<string, ChatStreamDraft>>({});
  // Live harness runs keyed by requestID, mirrored from atelier:harness-run
  // events. The chat:chunk effect pattern: refs only, so the empty-deps event
  // listener never closes over stale state. Used to re-attach the live run
  // when the user switches conversations mid-stream.
  const harnessRunDraftsRef = useRef<Record<string, HarnessRunView>>({});

  const assistantEntryID = activeStream ? `assistant-${activeStream}` : '';

  const latestHarnessRun = [...chat].reverse().find((entry) => entry.role === 'assistant' && entry.harnessRun)?.harnessRun;
  // Live turns receive real run snapshots via atelier:harness-run, so the
  // latest entry's run is always current — streamed or persisted.
  const visibleHarnessRun = latestHarnessRun ?? null;
  const modelUsage = useMemo(() => summarizeModelUsage(chat), [chat]);
  const mediaUsage = useMemo(() => summarizeMediaUsage(chat), [chat]);

  function markConversationInFlight(conversationID: string, requestID: string, kind: ConversationKind) {
    requestConversationRef.current[requestID] = {conversationID, kind};
    const next = {
      ...inFlightConversationsRef.current,
      [conversationID]: {requestID, kind},
    };
    inFlightConversationsRef.current = next;
    setInFlightConversations(next);
  }

  function clearConversationInFlight(requestID: string) {
    const tracked = requestConversationRef.current[requestID];
    if (!tracked) {
      return;
    }
    delete requestConversationRef.current[requestID];
    const next = {...inFlightConversationsRef.current};
    if (next[tracked.conversationID]?.requestID === requestID) {
      delete next[tracked.conversationID];
    }
    inFlightConversationsRef.current = next;
    setInFlightConversations(next);
  }

  useEffect(() => {
    const onChunk = (chunk: ChatChunk) => {
      const isVisibleStream = visibleStreamRef.current === chunk.requestID;
      if (chunk.conversationId) {
        markConversationInFlight(chunk.conversationId, chunk.requestID, 'chat');
      }
      const draft = chatStreamDraftsRef.current[chunk.requestID] ?? {content: '', thinking: '', images: [], videos: [], audios: [], transcripts: [], markdowns: [], streaming: true};
      chatStreamDraftsRef.current[chunk.requestID] = {
        content: `${draft.content}${chunk.content ?? ''}`,
        thinking: `${draft.thinking}${chunk.thinking ?? ''}`,
        images: chunk.images?.length ? chunk.images : draft.images,
        videos: chunk.videos?.length ? chunk.videos : draft.videos,
        audios: chunk.audios?.length ? chunk.audios : draft.audios,
        transcripts: chunk.transcripts?.length ? chunk.transcripts : draft.transcripts,
        markdowns: chunk.markdowns?.length ? chunk.markdowns : draft.markdowns,
        streaming: !chunk.done && !chunk.error,
        error: chunk.error ?? draft.error,
        provider: chunk.provider ?? draft.provider,
      };
      setChat((entries) =>
        entries.map((entry) => {
          if (entry.id !== `assistant-${chunk.requestID}`) {
            return entry;
          }
          const nextDraft = chatStreamDraftsRef.current[chunk.requestID];
          return {
            ...entry,
            content: nextDraft.content,
            thinking: nextDraft.thinking,
            images: nextDraft.images,
            videos: nextDraft.videos,
            audios: nextDraft.audios,
            transcripts: nextDraft.transcripts,
            markdowns: nextDraft.markdowns,
            streaming: nextDraft.streaming,
            error: nextDraft.error,
            provider: nextDraft.provider ?? entry.provider,
          };
        }),
      );
      if (chunk.done || chunk.error) {
        clearConversationInFlight(chunk.requestID);
        setActiveStream((current) => current === chunk.requestID ? null : current);
        if (isVisibleStream) {
          visibleStreamRef.current = null;
        }
        // The finished turn's artifacts are now in history — re-derive the
        // asset list (and the library tree, whose project listings may have
        // gained a conversation) so an open panel shows them. The bump closure
        // only touches a stable useState setter, so this empty-deps listener
        // never goes stale — the same reason refreshConversations is safe here.
        setAssetsRefreshTick((tick) => tick + 1);
        bumpLibrariesOnChunk();
      }
      if (chunk.conversationId && isVisibleStream) {
        setActiveConversationID(chunk.conversationId);
      }
      if (chunk.conversationId || chunk.done || chunk.error) {
        void refreshConversationsOnChunk();
      }
    };
    // Unsubscribe with the canceller EventsOn returns: EventsOff(name) drops
    // EVERY listener for the event, so an editor closing its own subscription
    // would take this one down too.
    return EventsOn('chat:chunk', onChunk);
  }, []);

  // Live harness telemetry: every step transition arrives as a full run
  // snapshot (the same shape history persists), so the panel renders real
  // in-flight state. Mirrors the chat:chunk effect — refs + setState only.
  useEffect(() => {
    const onHarnessRun = (event: HarnessRunEventView) => {
      if (!event?.requestId) {
        return;
      }
      const run = parseHarnessRun(event.run);
      if (!run) {
        return;
      }
      harnessRunDraftsRef.current[event.requestId] = run;
      const entryID = `assistant-${event.requestId}`;
      setChat((current) => current.map((entry) => entry.id === entryID ? {...entry, harnessRun: run} : entry));
    };
    return EventsOn('atelier:harness-run', onHarnessRun);
  }, []);

  useEffect(() => {
    const onToolPermission = (event: ToolPermissionEvent) => {
      setToolPermissions((current) => current.some((item) => item.id === event.id) ? current : [...current, event]);
    };
    return EventsOn('atelier:tool-permission', onToolPermission);
  }, []);

  async function resolveToolPermission(permissionID: string, approved: boolean) {
    setToolPermissions((current) => current.filter((item) => item.id !== permissionID));
    try {
      await ResolveToolPermission(permissionID, approved);
    } catch (error) {
      notifyError("Couldn't send the permission decision", error);
    }
  }

  useEffect(() => {
    const transcript = transcriptRef.current;
    if (!transcript || !shouldFollowTranscriptRef.current) {
      return;
    }
    transcript.scrollTo({top: transcript.scrollHeight, behavior: 'smooth'});
  }, [chat]);

  async function stopChat() {
    if (activeStream) {
      await CancelStream(activeStream);
      chatStreamDraftsRef.current[activeStream] = {
        ...(chatStreamDraftsRef.current[activeStream] ?? {content: '', thinking: '', images: [], videos: [], audios: []}),
        streaming: false,
        error: 'Stopped',
      };
      setActiveStream(null);
      visibleStreamRef.current = null;
      setChat((entries) =>
        entries.map((entry) =>
          entry.id === assistantEntryID ? {...entry, streaming: false, error: 'Stopped'} : entry,
        ),
      );
    }
  }

  function toggleThinkingCollapsed(entryID: string) {
    setCollapsedThinkingIDs((current) => ({
      ...current,
      [entryID]: !current[entryID],
    }));
  }

  async function copyMessage(entry: ChatEntry) {
    if (!entry.content) {
      return;
    }
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(entry.content);
      } else {
        copyTextWithTextarea(entry.content);
      }
      setCopiedMessageID(entry.id);
      if (copyResetRef.current) {
        window.clearTimeout(copyResetRef.current);
      }
      copyResetRef.current = window.setTimeout(() => setCopiedMessageID(''), 1600);
    } catch (error) {
      notifyError("Couldn't copy to the clipboard", error);
    }
  }

  return {
    activeStream,
    anyConversationInFlight,
    chat,
    chatIsEmpty,
    chatStreamDraftsRef,
    collapsedThinkingIDs,
    copiedMessageID,
    copyMessage,
    harnessRunDraftsRef,
    inFlightConversations,
    inFlightConversationsRef,
    markConversationInFlight,
    mediaUsage,
    modelUsage,
    resolveToolPermission,
    setActiveStream,
    setChat,
    setCollapsedThinkingIDs,
    shouldFollowTranscriptRef,
    stopChat,
    toggleThinkingCollapsed,
    toolPermissions,
    transcriptRef,
    visibleHarnessRun,
    visibleStreamRef,
  };
}

export {useChatStream};
