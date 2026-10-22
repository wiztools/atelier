// Media attachment intake: classifying a file as audio or video. The OS MIME
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
  '.mp4', '.m4v', '.mov', '.webm', '.mkv', '.avi', '.ts', '.3gp', '.mpg', '.mpeg',
]);

const audioExtensions = new Set([
  '.mp3', '.wav', '.ogg', '.opus', '.flac', '.m4a', '.aac', '.wma', '.aif', '.aiff',
]);

// The mime for containers the OS leaves unlabeled — a small (<24MB) attachment
// becomes an honest data:video/x-matroska URL instead of
// application/octet-stream, which decodeVideoPayload/decodeAudioPayload and
// the OpenRouter audio part read as an unknown format. Everything else falls
// back to the kind's house mime.
const extensionMimes: Record<string, string> = {
  '.mkv': 'video/x-matroska',
};

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
