import {useEffect, useRef, useState} from 'react';
import {RandomEmptyStatePrompt} from '../../wailsjs/go/main/App';
import {main} from '../../wailsjs/go/models';
import {
  composerHasMediaDrag,
  dropAcceptsFile,
  mediaKindForFile,
  mediaExtensionForType,
  uniqueAttachmentName,
} from '../mediaIntake';
import {notifyError} from '../notifications';
import {Attachment, StagingAttachment, ComposerDraft} from '../types';
import {formatError} from '../utils';
import {MentionCandidate, assetMentionLabel, detectMentionAt, mentionMatches} from '../composerMentions';
import {mediaStageThresholdBytes, readFileAsAttachment} from '../attachments';

// useComposer owns the chat composer: the prompt and its attachments (staged
// uploads included), the @-mention menu, drag-and-drop and paste intake, the
// empty-screen greeting, and the per-conversation drafts a conversation switch
// stashes and restores. It sends nothing itself: Enter calls App's submitChat,
// mention candidates come from App's asset pool, and the active conversation
// arrives as parameters.
function useComposer({chatIsEmpty, activeConversationID, activeConversationIDRef, mentionCandidates, submitChat}: {
  chatIsEmpty: boolean;
  activeConversationID: string;
  activeConversationIDRef: {current: string};
  mentionCandidates: () => MentionCandidate[];
  submitChat: () => Promise<void>;
}) {
  const [prompt, setPrompt] = useState('');
  const [attachments, setAttachments] = useState<Attachment[]>([]);
  // In-progress staged uploads (large dropped/picked media): the composer
  // shows a per-file progress row while the chunks stream to the backend,
  // which for a multi-hundred-MB clip takes seconds — long enough to need
  // visible feedback before the attachment chip exists.
  const [stagingAttachments, setStagingAttachments] = useState<StagingAttachment[]>([]);
  const [composerDragging, setComposerDragging] = useState(false);
  const composerDragDepth = useRef(0);
  // @-mention autocomplete state. mentionOpen/mentionIndex drive renders;
  // mentionStateRef holds the active {@-position, query} for the keydown and
  // accept handlers to read synchronously (avoiding stale-closure reads of
  // prompt/selection during rapid typing).
  const [mentionOpen, setMentionOpen] = useState(false);
  const [mentionIndex, setMentionIndex] = useState(0);
  const mentionStateRef = useRef<{ at: number; query: string } | null>(null);
  const mentionListRef = useRef<HTMLUListElement | null>(null);
  const composerRef = useRef<HTMLDivElement | null>(null);
  const [emptyPrompt, setEmptyPrompt] = useState<main.EmptyStatePrompt | null>(null);

  // Re-roll the empty-screen greeting whenever the transcript becomes empty or
  // the active conversation changes, so a fresh prompt shows each time.
  useEffect(() => {
    if (!chatIsEmpty) return;
    let cancelled = false;
    RandomEmptyStatePrompt()
      .then((prompt) => {
        if (!cancelled) setEmptyPrompt(prompt);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [chatIsEmpty, activeConversationID]);

  const chatPromptRef = useRef<HTMLTextAreaElement | null>(null);
  // The chip mirror behind the composer textarea (see .composer-input-mirror).
  const chatPromptMirrorRef = useRef<HTMLDivElement | null>(null);
  // Per-conversation composer drafts, keyed by conversationID ('' = new chat).
  // Mirror refs keep the latest prompt/attachments/activeConversationID so the
  // Cmd+N keydown listener (whose closure goes stale between activeStream
  // changes) reads current values rather than captured ones.
  const composerDraftsRef = useRef<Record<string, ComposerDraft>>({});
  const promptRef = useRef('');
  const attachmentsRef = useRef<Attachment[]>([]);

  // Close the @-mention menu on outside click or document-level Escape. The
  // textarea's own Escape handling (above) closes it too; this covers clicks
  // elsewhere in the window. Clicks inside the composer are ignored so picking
  // an item doesn't immediately dismiss via the same gesture.
  useEffect(() => {
    if (!mentionOpen) {
      return;
    }
    const onPointerDown = (event: MouseEvent) => {
      if (composerRef.current && event.target instanceof Node && composerRef.current.contains(event.target)) {
        return;
      }
      closeMention();
    };
    document.addEventListener('mousedown', onPointerDown);
    return () => document.removeEventListener('mousedown', onPointerDown);
  }, [mentionOpen]);

  // Keep the mirror refs in sync so navigation handlers (and the stale Cmd+N
  // keydown closure) always read the latest composer/active-conversation state.
  useEffect(() => {
    promptRef.current = prompt;
  }, [prompt]);
  useEffect(() => {
    attachmentsRef.current = attachments;
  }, [attachments]);

  // Snapshot the current composer (prompt + attachments) under the conversation
  // being left, so it can be restored when returning. Empty drafts are deleted
  // to keep the store bounded and avoid resurrecting blank composes.
  function stashCurrentDraft() {
    const id = activeConversationIDRef.current;
    const next: ComposerDraft = {
      prompt: promptRef.current,
      attachments: attachmentsRef.current,
    };
    if (!next.prompt.trim() && next.attachments.length === 0) {
      delete composerDraftsRef.current[id];
    } else {
      composerDraftsRef.current[id] = next;
    }
  }

  // Load the composer from the store for the conversation being entered, or
  // clear it if no draft exists for that key.
  function restoreDraftFor(conversationID: string) {
    const draft = composerDraftsRef.current[conversationID];
    setPrompt(draft?.prompt ?? '');
    setAttachments(draft?.attachments ?? []);
  }

  // Latest filtered mention candidates, mirrored into a ref so the keydown and
  // accept handlers read the same list that the rendered menu shows without
  // racing against React state updates during rapid typing.
  const [mentionMatchesState, setMentionMatchesState] = useState<MentionCandidate[]>([]);
  const mentionMatchesRef = useRef<MentionCandidate[]>([]);
  mentionMatchesRef.current = mentionMatchesState;

  // The popup's overflow scrolls only for the mouse; arrow-key moves change
  // mentionIndex without repositioning it, so keyboard navigation runs blind
  // past the first viewport of items. Keep the highlighted row visible.
  // block:'nearest' is a no-op when the row is already on screen, so manual
  // scrolling and hover-driven index changes are never fought. The matches are
  // a dependency because re-filtering on a narrower query can shrink the list
  // while the index resets to 0, leaving a stale scroll offset.
  useEffect(() => {
    if (!mentionOpen) return;
    mentionListRef.current?.children[mentionIndex]?.scrollIntoView({block: 'nearest'});
  }, [mentionIndex, mentionOpen, mentionMatchesState]);

  function handleChatPromptChange(event: React.ChangeEvent<HTMLTextAreaElement>) {
    const value = event.target.value;
    const caret = event.target.selectionStart ?? value.length;
    setPrompt(value);
    const match = detectMentionAt(value, caret);
    mentionStateRef.current = match;
    if (match) {
      const matches = mentionMatches(match.query, mentionCandidates());
      setMentionMatchesState(matches);
      setMentionIndex(0);
      setMentionOpen(matches.length > 0);
    } else {
      setMentionOpen(false);
    }
  }

  // Keep the chip mirror scrolled with the textarea: the mirror lays out the
  // same text, so its scroll offsets track the real input's exactly.
  function handleChatPromptScroll(event: React.UIEvent<HTMLTextAreaElement>) {
    const mirror = chatPromptMirrorRef.current;
    if (mirror) {
      mirror.scrollTop = event.currentTarget.scrollTop;
      mirror.scrollLeft = event.currentTarget.scrollLeft;
    }
  }

  function closeMention() {
    setMentionOpen(false);
    mentionStateRef.current = null;
  }

  // acceptMention replaces the open @-token with the accepted reference
  // (trailing space) and places the caret after it. Multi-word names insert in
  // the quoted form @"sunset beach.png": without delimiters the model cannot
  // tell where the reference ends and the instruction begins. Called from
  // Enter/Tab/Click. No-op if the menu is closed or the index is out of range.
  // No binding is recorded here — resolution happens from the text at render
  // and send time (see mentionBindingPool).
  function acceptMention(index: number) {
    const matches = mentionMatchesRef.current;
    const match = mentionStateRef.current;
    if (!match || index < 0 || index >= matches.length) {
      closeMention();
      return;
    }
    const chosen = matches[index];
    const token = /\s/.test(chosen.name) ? `@"${chosen.name}" ` : `@${chosen.name} `;
    const before = prompt.slice(0, match.at);
    const afterCaret = prompt.slice(match.at + 1 + match.query.length);
    setPrompt(`${before}${token}${afterCaret}`);
    closeMention();
    // Restore focus and caret after React re-renders the new value.
    const caret = (before + token).length;
    requestAnimationFrame(() => {
      const el = chatPromptRef.current;
      if (el) {
        el.focus();
        el.setSelectionRange(caret, caret);
      }
    });
  }

  // insertAssetReference is the panel-side counterpart of acceptMention: it
  // puts the asset's @-mention token into the prompt for the user who picked
  // the asset visually in the panel instead of typing toward it. When the
  // composer holds the caret the token inserts AT the caret (replacing the
  // selection, or a still-open @-token the caret sits in — the user started a
  // mention, then browsed); only with focus elsewhere is it appended at the
  // end. Same resolution path as typed mentions — nothing is recorded here,
  // the composer text stays the single source of truth. Hex labels are
  // token-safe, so no quoted form; an alias (asset_aliases.go) is preferred
  // when set — the name the user knows — and takes the quoted @"…" form when
  // it carries whitespace, the same shape acceptMention inserts.
  function insertAssetReference(asset: main.ConversationAsset) {
    const label = asset.alias || assetMentionLabel(asset);
    const token = /\s/.test(label) ? `@"${label}" ` : `@${label} `;
    closeMention();
    const el = chatPromptRef.current;
    if (el && document.activeElement === el) {
      const selStart = el.selectionStart ?? prompt.length;
      const selEnd = el.selectionEnd ?? selStart;
      // A collapsed caret ending an open @-token consumes that token (the
      // acceptMention replacement); a real selection is replaced as-is.
      const open = selStart === selEnd ? detectMentionAt(prompt, selEnd) : null;
      const start = open ? open.at : selStart;
      const before = prompt.slice(0, start);
      const next = `${before}${token}${prompt.slice(selEnd)}`;
      setPrompt(next);
      const caret = (before + token).length;
      requestAnimationFrame(() => {
        el.focus();
        el.setSelectionRange(caret, caret);
      });
      return;
    }
    const open = detectMentionAt(prompt, prompt.length);
    const base = (open ? prompt.slice(0, open.at) : prompt).replace(/\s+$/, '');
    const next = base ? `${base} ${token}` : token;
    setPrompt(next);
    // Focus the composer with the caret after the inserted token, the same
    // restore acceptMention does.
    requestAnimationFrame(() => {
      const el = chatPromptRef.current;
      if (el) {
        el.focus();
        el.setSelectionRange(next.length, next.length);
      }
    });
  }

  function handleChatPromptKeyDown(event: React.KeyboardEvent<HTMLTextAreaElement>) {
    if (mentionOpen) {
      const matches = mentionMatchesRef.current;
      if (event.key === 'ArrowDown') {
        event.preventDefault();
        setMentionIndex((index) => (matches.length ? (index + 1) % matches.length : 0));
        return;
      }
      if (event.key === 'ArrowUp') {
        event.preventDefault();
        setMentionIndex((index) => (matches.length ? (index - 1 + matches.length) % matches.length : 0));
        return;
      }
      if (event.key === 'Enter' && !event.shiftKey) {
        event.preventDefault();
        acceptMention(mentionIndex);
        return;
      }
      if (event.key === 'Tab' && !event.shiftKey) {
        event.preventDefault();
        acceptMention(mentionIndex);
        return;
      }
      if (event.key === 'Escape') {
        event.preventDefault();
        closeMention();
        return;
      }
    } else if (event.key === 'Enter' && !event.shiftKey) {
      event.preventDefault();
      void submitChat();
    }
  }

  // attachFiles routes files through readFileAsAttachment, tracking staging
  // progress for the large media that stage to disk. nameFor supplies the
  // synthetic name for files without one (pasted bytes, anonymous drops); the
  // staging rows are keyed per call so parallel drops don't cross-talk.
  async function attachFiles(files: File[], nameFor?: (file: File, index: number) => string) {
    const stagingKeys = files.map((file, index): {key: string; kind: 'audio' | 'video'} | null => {
      const kind = mediaKindForFile(file);
      if (!kind || file.size <= mediaStageThresholdBytes) {
        return null;
      }
      return {key: `staging-${Date.now()}-${index}`, kind};
    });
    stagingKeys.forEach((entry, index) => {
      if (entry) {
        setStagingAttachments((items) => [...items, {key: entry.key, name: files[index].name, kind: entry.kind, fraction: 0}]);
      }
    });
    try {
      const next = await Promise.all(
        files.map((file, index) => {
          const entry = stagingKeys[index];
          const onProgress = entry
            ? (fraction: number) =>
                setStagingAttachments((items) => items.map((item) => (item.key === entry.key ? {...item, fraction} : item)))
            : undefined;
          return readFileAsAttachment(file, nameFor?.(file, index), onProgress)
            // Name the file that failed: a multi-file drop otherwise reports a
            // backend message with no hint which attachment it belongs to.
            .catch((error) => {
              throw new Error(`${file.name}: ${formatError(error)}`);
            })
            .finally(() => {
              if (entry) {
                setStagingAttachments((items) => items.filter((item) => item.key !== entry.key));
              }
            });
        }),
      );
      appendAttachments(next);
    } catch (error) {
      stagingKeys.forEach((entry) => {
        if (entry) {
          setStagingAttachments((items) => items.filter((item) => item.key !== entry.key));
        }
      });
      // A failed intake (a rejected staging sniff, a disk error mid-chunks)
      // must never be silent: it reads as a swallowed drop — the staging row
      // ran to 100% and vanished — so the reason toasts.
      notifyError("Couldn't attach", error);
      console.error('attaching files failed', error);
    }
  }

  async function addFiles(files: FileList | null) {
    if (!files) {
      return;
    }
    await attachFiles(Array.from(files));
  }

  // appendAttachments merges new attachments into the list, renaming any whose
  // name collides with an existing (or another incoming) attachment so every
  // name is unique. Names are the key @-mentions resolve against and back the
  // React key for attachment chips, so uniqueness must hold.
  function appendAttachments(incoming: Attachment[]) {
    setAttachments((items) => {
      const used = items.map((item) => item.name);
      const renamed = incoming.map((item) => {
        const name = uniqueAttachmentName(item.name, used);
        used.push(name);
        return name === item.name ? item : { ...item, name };
      });
      return [...items, ...renamed];
    });
  }

  async function handleChatPromptPaste(event: React.ClipboardEvent<HTMLTextAreaElement>) {
    const mediaFiles = Array.from(event.clipboardData?.items ?? [])
      .filter((item) => item.kind === 'file' && (item.type.startsWith('image/') || item.type.startsWith('audio/') || item.type.startsWith('video/')))
      .map((item) => item.getAsFile())
      .filter((file): file is File => file !== null);
    if (!mediaFiles.length) {
      return;
    }
    // Keep the pasted bytes out of the text field and route them to attachments.
    event.preventDefault();
    const stamp = Date.now();
    await attachFiles(mediaFiles, (file, index) => {
      const extension = file.name.includes('.') ? '' : mediaExtensionForType(file.type);
      return `pasted-${stamp}-${index + 1}${file.name ? `-${file.name}` : extension}`;
    });
  }

  function handleComposerDragEnter(event: React.DragEvent<HTMLDivElement>) {
    // Any file drag counts (composerHasMediaDrag in mediaIntake.ts): WebKit
    // offers no filename during dragover and may omit the file items entirely
    // outside the drop event, so the overlay rides the guaranteed "Files"
    // types entry — a type-only or items-only check hid the drop overlay from
    // an .mkv drop the handler would accept.
    if (!composerHasMediaDrag(Array.from(event.dataTransfer?.items ?? []), Array.from(event.dataTransfer?.types ?? []))) {
      return;
    }
    event.preventDefault();
    composerDragDepth.current += 1;
    setComposerDragging(true);
  }

  function handleComposerDragLeave(event: React.DragEvent<HTMLDivElement>) {
    if (composerDragDepth.current === 0) {
      return;
    }
    composerDragDepth.current -= 1;
    if (composerDragDepth.current === 0) {
      setComposerDragging(false);
    }
  }

  async function handleComposerDrop(event: React.DragEvent<HTMLDivElement>) {
    composerDragDepth.current = 0;
    setComposerDragging(false);
    const droppedFiles = Array.from(event.dataTransfer?.files ?? []);
    if (!droppedFiles.length) {
      return;
    }
    // Keep the browser from navigating to the dropped file before anything
    // else — even when no file routes to attachments, navigation must not win.
    event.preventDefault();
    // Subtitle and markdown files are matched by extension too: an .srt or .md
    // dropped from Finder often reports an empty MIME type, which the type-only
    // drag preview above misses — the drop itself should still accept what the
    // picker accepts (dropAcceptsFile in mediaIntake.ts; the .mkv empty-type
    // case is conv_d3e32280).
    const mediaFiles = droppedFiles.filter(dropAcceptsFile);
    if (!mediaFiles.length) {
      return;
    }
    const stamp = Date.now();
    await attachFiles(
      mediaFiles,
      (file, index) => file.name || `dropped-${stamp}-${index + 1}${file.name.includes('.') ? '' : mediaExtensionForType(file.type)}`,
    );
  }

  return {
    acceptMention,
    addFiles,
    attachments,
    attachmentsRef,
    chatPromptMirrorRef,
    chatPromptRef,
    composerDraftsRef,
    composerDragging,
    composerRef,
    emptyPrompt,
    handleChatPromptChange,
    handleChatPromptKeyDown,
    handleChatPromptPaste,
    handleChatPromptScroll,
    handleComposerDragEnter,
    handleComposerDragLeave,
    handleComposerDrop,
    insertAssetReference,
    mentionIndex,
    mentionListRef,
    mentionMatchesState,
    mentionOpen,
    prompt,
    restoreDraftFor,
    setAttachments,
    setMentionIndex,
    setPrompt,
    stagingAttachments,
    stashCurrentDraft,
  };
}

export {useComposer};
