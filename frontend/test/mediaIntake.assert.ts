import {composerHasMediaDrag, mediaKindForFile, mediaMimeForFile} from '../src/mediaIntake';

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

// The conv_d3e32280 failure: an .mkv attachment carries an EMPTY type on
// macOS (the system-preferred org.matroska.mkv UTI declares no MIME tag), so
// the kind must come from the extension.
assert(mediaKindForFile({name: 'song video.mkv', type: ''}) === 'video', 'mkv with empty type classifies as video');
assert(mediaKindForFile({name: 'clip.ts', type: ''}) === 'video', 'ts with empty type classifies as video');
assert(mediaKindForFile({name: 'recording.mp3', type: ''}) === 'audio', 'mp3 with empty type classifies as audio');

// A type the OS did offer is honored when the extension says nothing...
assert(mediaKindForFile({name: 'clip', type: 'video/x-matroska'}) === 'video', 'type decides without an extension');
assert(mediaKindForFile({name: 'voice', type: 'audio/wav'}) === 'audio', 'type decides for extensionless audio');
assert(mediaKindForFile({name: 'photo.png', type: 'image/png'}) === null, 'images stay null');
assert(mediaKindForFile({name: 'notes.txt', type: ''}) === null, 'unknown extensions stay null');

// ...but a known container extension wins over a mislabeled type: macOS maps
// .wma to video/x-ms-wma, and the backend sniffs the real bytes either way.
assert(mediaKindForFile({name: 'song.wma', type: 'video/x-ms-wma'}) === 'audio', 'container extension beats a mislabeled type');
assert(mediaKindForFile({name: 'clip.mkv', type: 'video/x-matroska'}) === 'video', 'mkv with a real type still video');

// Wire mimes: the OS type passes through; an unlabeled container gets its
// honest mime so the data URL does not land as application/octet-stream.
assert(mediaMimeForFile({name: 'clip.mp4', type: 'video/mp4'}, 'video') === 'video/mp4', 'offered type passes through');
assert(mediaMimeForFile({name: 'song video.mkv', type: ''}, 'video') === 'video/x-matroska', 'mkv gets the matroska mime');
assert(mediaMimeForFile({name: 'clip', type: ''}, 'video') === 'video/mp4', 'video falls back to the house mime');
assert(mediaMimeForFile({name: 'voice', type: ''}, 'audio') === 'audio/mpeg', 'audio falls back to the house mime');

// Hidden files and dotless names carry no usable extension.
assert(mediaKindForFile({name: '.mkv', type: ''}) === null, 'a dotfile name is not an extension');
assert(mediaKindForFile({name: 'noext', type: ''}) === null, 'extensionless with no type stays null');

// The drag overlay counts any file item: dragover exposes no filename, so an
// .mkv (empty item.type on macOS) is indistinguishable from an unknown file
// there — the overlay is a hint, handleComposerDrop filters precisely.
assert(composerHasMediaDrag([{kind: 'file', type: ''}]), 'an empty-type file item shows the overlay (the mkv case)');
assert(composerHasMediaDrag([{kind: 'file', type: 'video/mp4'}]), 'a typed file item shows the overlay');
assert(composerHasMediaDrag([{kind: 'string', type: 'text/plain'}, {kind: 'file', type: ''}]), 'a file item beside a text item shows the overlay');
assert(!composerHasMediaDrag([{kind: 'string', type: 'text/plain'}]), 'a text-only drag shows nothing');
assert(!composerHasMediaDrag([]), 'no items show nothing');

// WebKit's protected mode can expose NO file items during dragover (they are
// only reliable at the drop event), so the guaranteed "Files" entry in
// dataTransfer.types carries the detection on its own.
assert(composerHasMediaDrag([], ['Files']), 'the types-only Files entry shows the overlay (the WebKit dragover case)');
assert(composerHasMediaDrag([{kind: 'file', type: ''}], ['Files', 'text/plain']), 'Files in types beside items still shows the overlay');
assert(!composerHasMediaDrag([{kind: 'string', type: 'text/plain'}], ['text/plain', 'text/html']), 'a text-only drag with no Files type shows nothing');
