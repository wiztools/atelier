// Media attachment intake: classifying a dropped/pasted/picked file as audio,
// video, subtitle, or document attachment. The OS MIME
// type is unreliable for real containers — macOS's preferred UTI for .mkv
// (org.matroska.mkv) declares no MIME tag, so WebKit hands the composer an
// EMPTY file.type; a type-only check routed an attached MKV to the image
// reader, the backend silently dropped the non-image payload, and the turn
// shipped text-only ("Please provide the file" — conv_d3e32280). The extension
// decides when it names a container, mirroring the staged-upload allowlists in
// Go's media_upload.go; the type decides for anonymous pasted bytes with no
// usable name. Nothing here validates bytes: the backend sniffs the content at
// staging/persistence, so a mislabeled file fails loudly there instead of
// vanishing.

const videoExtensions = new Set([
  '.mp4', '.m4v', '.mov', '.webm', '.mkv', '.avi', '.ts', '.3gp', '.mpg', '.mpeg', '.flv',
]);

const audioExtensions = new Set([
  '.mp3', '.wav', '.ogg', '.opus', '.flac', '.m4a', '.aac', '.wma', '.aif', '.aiff',
]);

// The mime for containers the OS leaves unlabeled — a small (<24MB) attachment
// becomes an honest data:video/x-matroska URL instead of
// application/octet-stream, which decodeVideoPayload/decodeAudioPayload and
// the OpenRouter audio part read as an unknown format. Everything else falls
// back to the kind's house mime. Mirrors mediaTypeForExtension (app.go).
const extensionMimes: Record<string, string> = {
  '.mkv': 'video/x-matroska',
  '.avi': 'video/x-msvideo',
  '.ts': 'video/mp2t',
  '.mpg': 'video/mpeg',
  '.mpeg': 'video/mpeg',
  '.flv': 'video/x-flv',
};

// Video containers WKWebView's <video> cannot decode — it has no demuxer for
// them, so a player element would sit black under a dead control bar. The UI
// renders these as their poster thumbnail instead (VideoPlayer's still branch);
// every ffmpeg-backed tool reads them fine, so they remain full attachments.
// MKV is deliberately absent: it predates this rule and rides the player today.
const thumbnailOnlyVideoExtensions = new Set(['.flv', '.avi', '.ts', '.mpg', '.mpeg']);

const thumbnailOnlyVideoMimes = new Set(['video/x-flv', 'video/x-msvideo', 'video/mp2t', 'video/mpeg']);

// videoThumbnailOnlySrc decides whether a video attachment's src renders as a
// poster still instead of a player. Persisted clips arrive as artifact URLs
// (the extension decides); the live transcript's just-attached clip is a data
// URL (the mime in the header decides), so both spellings are recognized.
export function videoThumbnailOnlySrc(src: string): boolean {
  if (src.startsWith('data:')) {
    const headerEnd = src.indexOf(',');
    if (headerEnd < 0) {
      return false;
    }
    return thumbnailOnlyVideoMimes.has(src.slice(5, headerEnd).split(';')[0]);
  }
  return thumbnailOnlyVideoExtensions.has(fileExtension(src.split(/[?#]/)[0]));
}

export type MediaKind = 'audio' | 'video';

function fileExtension(name: string): string {
  const dot = name.lastIndexOf('.');
  return dot > 0 ? name.slice(dot).toLowerCase() : '';
}

export function mediaKindForFile(file: {name: string; type: string}): MediaKind | null {
  const extension = fileExtension(file.name);
  if (videoExtensions.has(extension)) return 'video';
  if (audioExtensions.has(extension)) return 'audio';
  if (file.type.startsWith('video/')) return 'video';
  if (file.type.startsWith('audio/')) return 'audio';
  return null;
}

// mediaMimeForFile names the wire mime a data URL should carry: the OS type
// when it offered one, else the extension's container mime, else the kind's
// house mime (the same defaults the Go decoders land on).
export function mediaMimeForFile(file: {name: string; type: string}, kind: MediaKind): string {
  if (file.type) {
    return file.type;
  }
  return extensionMimes[fileExtension(file.name)] ?? (kind === 'audio' ? 'audio/mpeg' : 'video/mp4');
}

// composerHasMediaDrag decides whether a drag over the composer shows the
// drop overlay. During dragover WebKit's protected mode may expose NO readable
// file items (they are only reliable at the drop event), so the check rides
// the spec-guaranteed signal first: a drag carrying files always lists
// "Files" in dataTransfer.types. Any file item counts beyond that — WebKit
// exposes no filename during dragover either, so an .mkv (no MIME on macOS)
// is indistinguishable from an unknown file there. The overlay is a hint, and
// handleComposerDrop filters precisely.
export function composerHasMediaDrag(items: Array<{kind: string; type: string}>, types: string[] = []): boolean {
  if (types.includes('Files')) {
    return true;
  }
  return items.some((item) => item.kind === 'file');
}

// subtitleMimeForFile names the wire mime an attached subtitle carries: the
// extension decides (.vtt WebVTT, .srt SubRip) because OSes report these files
// inconsistently (often an empty type). The backend re-sniffs the CONTENT at
// persistence, so a mislabeled mime only affects the chip, never the artifact.
export function subtitleMimeForFile(file: {name: string; type: string}): string | null {
  const lower = file.name.toLowerCase();
  if (lower.endsWith('.vtt') || file.type === 'text/vtt') return 'text/vtt';
  if (lower.endsWith('.srt') || file.type === 'application/x-subrip') return 'application/x-subrip';
  return null;
}

// markdownMimeForFile is the document twin: extension-first (.md/.markdown —
// OSes often report an empty type), with text/markdown as the declared fallback.
// .fountain rides the screenplay dialect's wire mime so the extension survives
// persistence (unlike .markdown, which normalizes .md because the mime cannot
// distinguish the spellings). The backend maps the mime back onto the extension.
export function markdownMimeForFile(file: {name: string; type: string}): string | null {
  const lower = file.name.toLowerCase();
  if (lower.endsWith('.fountain')) return 'text/x-fountain';
  if (lower.endsWith('.md') || lower.endsWith('.markdown') || file.type === 'text/markdown') return 'text/markdown';
  return null;
}

// dropAcceptsFile is the composer drop's admission test, the drop-side twin of
// the picker's accept list: containers ride mediaKindForFile (extension first —
// an .mkv carries no MIME on macOS, and the filter that predates it turned such
// a drop into a silent no-op, conv_d3e32280), subtitles and documents ride
// their extension-first mimes, and images ride the MIME type (macOS types them
// reliably, and readFileAsAttachment's readImageFile fallthrough is the reader
// the picker's image/* accept entry feeds). The image branch was dropped when
// the container routing moved to mediaKindForFile, so dropped PNGs were
// silently swallowed while the overlay still promised a drop zone — every
// branch here is asserted in test/mediaIntake.assert.ts.
export function dropAcceptsFile(file: {name: string; type: string}): boolean {
  return (
    mediaKindForFile(file) !== null ||
    file.type.startsWith('image/') ||
    subtitleMimeForFile(file) !== null ||
    markdownMimeForFile(file) !== null
  );
}

export function imagePayloadForOllama(image: string): string {
  const match = /^data:image\/[a-z+.-]+;base64,(.*)$/i.exec(image);
  if (match) {
    return match[1];
  }
  // Not an inline data URL — e.g. a hydrated /atelier-artifact/ history URL or a
  // file path. These are display references, not valid model image payloads, so
  // drop them rather than sending a string Ollama can't base64-decode.
  return '';
}

// bytesToBase64 encodes an ArrayBuffer in 0x8000-char slices — the largest
// argument btoa's String.fromCharCode spread accepts without blowing the
// engine's argument limit.
export function bytesToBase64(buffer: ArrayBuffer): string {
  const bytes = new Uint8Array(buffer);
  const parts: string[] = [];
  for (let offset = 0; offset < bytes.length; offset += 0x8000) {
    parts.push(String.fromCharCode(...bytes.subarray(offset, offset + 0x8000)));
  }
  return btoa(parts.join(''));
}

// uniqueAttachmentName returns a name that does not collide with any existing
// name, appending " (2)", " (3)", … before the extension as needed. Attachment
// names are the key @-mentions match against, and they back the React key for
// attachment chips, so duplicates would both break @ resolution and collide in
// the chip list. Pure/module-level so it can be unit-tested in isolation.
export function uniqueAttachmentName(requested: string, existing: string[]): string {
  if (!requested) {
    requested = 'attachment';
  }
  if (!existing.includes(requested)) {
    return requested;
  }
  const taken = new Set(existing);
  const dot = requested.lastIndexOf('.');
  const stem = dot > 0 ? requested.slice(0, dot) : requested;
  const ext = dot > 0 ? requested.slice(dot) : '';
  for (let counter = 2; ; counter++) {
    const candidate = `${stem} (${counter})${ext}`;
    if (!taken.has(candidate)) {
      return candidate;
    }
  }
}

function imageExtensionForType(type: string): string {
  const subtype = type.split('/')[1]?.split(';')[0]?.trim();
  if (!subtype) {
    return '.png';
  }
  return `.${subtype === 'jpeg' ? 'jpg' : subtype}`;
}

function audioExtensionForType(type: string): string {
  const subtype = type.split('/')[1]?.split(';')[0]?.trim().toLowerCase();
  switch (subtype) {
    case 'wav':
    case 'wave':
    case 'x-wav':
      return '.wav';
    case 'ogg':
    case 'opus':
      return '.ogg';
    case 'flac':
      return '.flac';
    case 'mp4':
    case 'aac':
    case 'x-m4a':
      return '.m4a';
    default:
      return '.mp3';
  }
}

function videoExtensionForType(type: string): string {
  const subtype = type.split('/')[1]?.split(';')[0]?.trim().toLowerCase();
  switch (subtype) {
    case 'webm':
      return '.webm';
    case 'quicktime':
      return '.mov';
    default:
      return '.mp4';
  }
}

// mediaExtensionForType picks a fallback extension for a synthesized filename
// (pasted/dropped media that has no name), branching on the MIME category.
export function mediaExtensionForType(type: string): string {
  if (type.startsWith('audio/')) {
    return audioExtensionForType(type);
  }
  if (type.startsWith('video/')) {
    return videoExtensionForType(type);
  }
  return imageExtensionForType(type);
}
