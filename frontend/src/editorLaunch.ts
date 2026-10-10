// Editor-launch intent detection: prompts like "Open this image in image
// editor" or "Open in video editor" are UI navigation commands, not chat —
// detecting them deterministically (no model call) lets the composer route the
// send straight to the matching editor. The detector's contract is full
// consumption, not keyword spotting: the message must be EXACTLY an
// editor-open command, with every token bound by a closed-vocabulary grammar.
// Any token the grammar cannot bind ("and crop it", "and extract its audio")
// means the message carries a second intent this path cannot execute, so it
// abstains and the AI tier (the harness's open_editor tool) handles the turn.
// Abstention is decidable without understanding what the leftover content
// means — that is what keeps this path safe to bypass the model with.

export type EditorLaunchKind = 'image' | 'video' | 'audio';
export type ComposerAttachmentKind = 'image' | 'audio' | 'video';

const openCommand = /^(?:please\s+)?open\b/;

const mediaKinds: Record<string, EditorLaunchKind> = {
  image: 'image',
  images: 'image',
  photo: 'image',
  photos: 'image',
  picture: 'image',
  pictures: 'image',
  pic: 'image',
  pics: 'image',
  video: 'video',
  videos: 'video',
  clip: 'video',
  clips: 'video',
  movie: 'video',
  movies: 'video',
  mp4: 'video',
  audio: 'audio',
  audios: 'audio',
  sound: 'audio',
  sounds: 'audio',
  recording: 'audio',
  recordings: 'audio',
  track: 'audio',
  tracks: 'audio',
  song: 'audio',
  songs: 'audio',
  voice: 'audio',
  memo: 'audio',
  memos: 'audio',
  mp3: 'audio',
  wav: 'audio',
  m4a: 'audio',
};

// The closed vocabulary. Politeness particles are admitted; content words are
// not — a content word is exactly the shape of a second intent.
const determiners = new Set(['the', 'a', 'an', 'this', 'that', 'my']);
const prepositions = new Set(['in', 'into', 'with', 'on', 'to']);
const particles = new Set(['up']);
const trailingPoliteness = new Set(['for', 'me', 'please']);

// detectEditorLaunchIntent returns the editor the prompt asks to open, or null
// when the send should stay with the AI tier. The grammar is anchored:
//
//   [please] open [up] {determiner | media word | preposition | particle}*
//   editor[s] {for | me | please}*
//
// The kind resolves from the media word directly before "editor" ("video
// editor"), else any media word in the message ("open this image in the
// editor"), else — no media word at all — the single attached asset. The
// resolved kind must match exactly one attached asset; ambiguity or a
// mismatch (the "image editor" named with only a video attached) abstains.
export function detectEditorLaunchIntent(text: string, attachmentKinds: ComposerAttachmentKind[]): EditorLaunchKind | null {
  const normalized = text.toLowerCase().replace(/[^a-z0-9\s]/g, ' ').replace(/\s+/g, ' ').trim();
  if (!normalized || !openCommand.test(normalized)) {
    return null;
  }
  const words = normalized.split(' ');
  let index = 0;
  if (words[index] === 'please') index++;
  if (words[index] !== 'open') {
    return null;
  }
  index++;
  if (words[index] === 'up') index++;
  let editorIndex = -1;
  const mediaSeen: EditorLaunchKind[] = [];
  for (; index < words.length; index++) {
    const word = words[index];
    if (word === 'editor' || word === 'editors') {
      editorIndex = index;
      index++;
      break;
    }
    const media = mediaKinds[word];
    if (media !== undefined) {
      mediaSeen.push(media);
      continue;
    }
    if (determiners.has(word) || prepositions.has(word) || particles.has(word)) {
      continue;
    }
    // Unbindable token — content this command cannot consume.
    return null;
  }
  if (editorIndex < 0) {
    return null;
  }
  for (; index < words.length; index++) {
    if (!trailingPoliteness.has(words[index])) {
      return null;
    }
  }
  let named: EditorLaunchKind | null = null;
  for (let scan = editorIndex - 1; scan >= 0; scan--) {
    const word = words[scan];
    if (determiners.has(word)) {
      continue;
    }
    named = mediaKinds[word] ?? null;
    break;
  }
  const requested = named ?? mediaSeen[0] ?? null;
  const attachedCount = (kind: EditorLaunchKind) => attachmentKinds.filter((item) => item === kind).length;
  if (requested) {
    return attachedCount(requested) === 1 ? requested : null;
  }
  const attached = attachmentKinds.filter((item) => item === 'image' || item === 'video' || item === 'audio');
  if (attached.length === 1) {
    return attached[0];
  }
  return null;
}
