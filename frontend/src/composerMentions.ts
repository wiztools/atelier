// The composer's @-mention model: open-token detection at the caret, the
// segmenter that resolves mention tokens against this turn's attachments and
// the conversation's assets, the mention menu's candidate filter, and the
// asset labels the menu and the assets panel show.

import type {main} from '../wailsjs/go/models';
import type {Attachment} from './types';

// MentionMatch describes an open @-token at the caret: `at` is the index of the
// '@', `query` is the text typed after it (no whitespace allowed mid-token).
type MentionMatch = { at: number; query: string };

// detectMentionAt finds an open @-mention token ending at the caret. A token is
// valid only when '@' sits at the start of text or right after whitespace (so
// "foo@bar" is not a mention), and contains no whitespace after the '@'. A
// leading double quote is allowed: accepted multi-word mentions insert as
// @"quoted name" tokens, and placing the caret back inside the opening word
// re-opens completions — the quote stays part of the query so acceptMention's
// replacement consumes it (mentionMatches strips it for matching). Returns
// null when no token is active. Pure/module-level for unit testing.
function detectMentionAt(text: string, caret: number): MentionMatch | null {
  if (caret <= 0) {
    return null;
  }
  const slice = text.slice(0, caret);
  const at = slice.lastIndexOf('@');
  if (at < 0) {
    return null;
  }
  // The '@' must start a token: begin-of-text or preceded by whitespace.
  if (at > 0 && !/\s/.test(text[at - 1])) {
    return null;
  }
  const query = slice.slice(at + 1);
  // The token ends at the first whitespace; if any is present, the mention is
  // no longer open at the caret.
  if (/\s/.test(query)) {
    return null;
  }
  return { at, query };
}

// ParsedMention is one resolved @-reference read out of composer text: the
// verbatim source token (quotes included) plus what it binds to. assetID is
// set only for asset mentions (their hex labels are self-describing — the
// identity rides in the text itself); attachment mentions bind to this turn's
// attached file by name and carry none, since the bytes ride the request's
// payload arrays regardless of the text.
type ParsedMention = {
  source: string;
  label: string;
  assetID?: string;
};

// ComposerSegment splits composer text into plain runs and resolved mentions
// — the shared view model for the chip overlay and the send-time
// referencedAssetIds scan.
type ComposerSegment = { text: string; mention?: ParsedMention };

// parseComposerMentions splits composer text into plain runs and mention
// tokens, resolving each against pool (this turn's attachments first, then the
// same asset list the mention menu offers). A token is '@' at text start or
// after whitespace, either bare (`@img_ab12…`, whitespace-delimited) or quoted
// (`@"sunset beach.png"`, the only form multi-word names are inserted in).
// Tokens that resolve against nothing — prose like "ping @john", a half-edited
// name — stay plain, so the chip decoration only ever marks references that
// will actually carry media. Un-closed quotes are plain text to the end. Pure/
// module-level for unit testing.
function parseComposerMentions(text: string, pool: { label: string; assetID?: string }[]): ComposerSegment[] {
  const byLabel = new Map<string, { label: string; assetID?: string }>();
  for (const item of pool) {
    const key = item.label.toLowerCase();
    // First wins: attachments outrank a same-named asset, mirroring the menu's
    // candidate order.
    if (!byLabel.has(key)) {
      byLabel.set(key, item);
    }
  }
  const segments: ComposerSegment[] = [];
  let plain = '';
  const flushPlain = () => {
    if (plain) {
      segments.push({ text: plain });
      plain = '';
    }
  };
  let i = 0;
  while (i < text.length) {
    if (text[i] === '@' && (i === 0 || /\s/.test(text[i - 1]))) {
      let source = '';
      let label = '';
      let end = i + 1;
      if (text[i + 1] === '"') {
        const close = text.indexOf('"', i + 2);
        if (close < 0) {
          // Un-closed quote: not a mention; the rest cannot contain a fresh
          // token (any '@' inside sits mid-quote), so take it all as plain.
          plain += text.slice(i);
          break;
        }
        source = text.slice(i, close + 1);
        label = text.slice(i + 2, close);
        end = close + 1;
      } else {
        let j = i + 1;
        while (j < text.length && !/\s/.test(text[j])) {
          j += 1;
        }
        source = text.slice(i, j);
        label = text.slice(i + 1, j);
        end = j;
      }
      const bound = label ? byLabel.get(label.toLowerCase()) : undefined;
      if (bound) {
        flushPlain();
        segments.push({ text: source, mention: { source, label: bound.label, ...(bound.assetID ? { assetID: bound.assetID } : {}) } });
      } else {
        plain += source;
      }
      i = end;
      continue;
    }
    plain += text[i];
    i += 1;
  }
  flushPlain();
  return segments;
}

// assetTurnLabel renders an origin turn ID (turn_000003) as a human label
// ("turn 3") for the assets panel; anything unparsable passes through as-is.
function assetTurnLabel(turnID: string): string {
  const n = Number(turnID.replace(/^turn_/, ''));
  return Number.isFinite(n) && n > 0 ? `turn ${n}` : turnID;
}

// MentionCandidate extends an in-composer attachment with the menu's provenance
// label. Candidates carry no binding state: mention resolution happens at
// render/send time from the text itself (parseComposerMentions against the
// binding pool), so accepting one only inserts text.
type MentionCandidate = Attachment & {
  hint?: string;
};

// assetMentionLabel derives the @-token for a conversation asset from its ID —
// kind prefix plus enough hex to be unique in practice, and token-safe (no
// spaces) so the open-token detection and the send-time text scan agree. The
// label is a prefix of the asset ID, so it is self-describing: resolving a
// mention is a lookup of the token against the asset list, never a recorded
// side-channel that the text could drift from.
function assetMentionLabel(asset: main.ConversationAsset): string {
  return asset.id.slice(0, 12);
}

// mentionMatches returns the candidates whose name contains the query as a
// case-insensitive substring, preserving order. An empty query lists every
// candidate (the menu is most useful right after typing '@'). A leading double
// quote (re-opening a @"quoted name" token) is stripped before matching.
function mentionMatches(query: string, items: MentionCandidate[]): MentionCandidate[] {
  const q = query.trim().toLowerCase().replace(/^"/, '');
  if (!q) {
    return items;
  }
  return items.filter((item) => item.name.toLowerCase().includes(q));
}

export type {MentionMatch, ParsedMention, ComposerSegment, MentionCandidate};
export {detectMentionAt, parseComposerMentions, assetTurnLabel, assetMentionLabel, mentionMatches};
