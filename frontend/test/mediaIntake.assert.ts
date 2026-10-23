import {composerHasMediaDrag, dropAcceptsFile, markdownMimeForFile, mediaKindForFile, mediaMimeForFile, subtitleMimeForFile, videoThumbnailOnlySrc} from '../src/mediaIntake';

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

// The conv_d3e32280 failure: an .mkv attachment carries an EMPTY type on
// macOS (the system-preferred org.matroska.mkv UTI declares no MIME tag), so
// the kind must come from the extension.
assert(mediaKindForFile({name: 'song video.mkv', type: ''}) === 'video', 'mkv with empty type classifies as video');
assert(mediaKindForFile({name: 'clip.ts', type: ''}) === 'video', 'ts with empty type classifies as video');
assert(mediaKindForFile({name: 'recording.mp3', type: ''}) === 'audio', 'mp3 with empty type classifies as audio');
// The FLV report: the extension was not admitted, so classification rode the
// OS mime alone and an unlabeled FLV would fall out of the drop entirely.
assert(mediaKindForFile({name: 'movie.flv', type: ''}) === 'video', 'flv with empty type classifies as video');
assert(mediaKindForFile({name: 'clip.avi', type: ''}) === 'video', 'avi with empty type classifies as video');

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
assert(mediaMimeForFile({name: 'movie.flv', type: ''}, 'video') === 'video/x-flv', 'flv gets the flash-video mime');
assert(mediaMimeForFile({name: 'clip.avi', type: ''}, 'video') === 'video/x-msvideo', 'avi gets the msvideo mime');
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

// The drop's admission test mirrors the picker's accept list. The regression
// this pins: the container routing refactor dropped the image branch, so a
// dropped PNG passed no predicate and the handler swallowed it silently —
// while the overlay above still showed a drop zone.
assert(dropAcceptsFile({name: 'photo.png', type: 'image/png'}), 'an image rides its MIME type');
assert(dropAcceptsFile({name: 'clip.mkv', type: ''}), 'an empty-type mkv rides its extension');
assert(dropAcceptsFile({name: 'voice memo.mp3', type: 'audio/mpeg'}), 'an audio attachment is accepted');
assert(dropAcceptsFile({name: 'captions.srt', type: ''}), 'a subtitle rides its extension');
assert(dropAcceptsFile({name: 'notes.md', type: ''}), 'a markdown document rides its extension');
assert(!dropAcceptsFile({name: 'page.pdf', type: 'application/pdf'}), 'a pdf is not an attachment');
assert(!dropAcceptsFile({name: 'mystery', type: ''}), 'an extensionless unknown file is not an attachment');

// The subtitle/markdown mimes the admission test and the readers share.
assert(subtitleMimeForFile({name: 'captions.srt', type: ''}) === 'application/x-subrip', 'srt extension decides the subtitle mime');
assert(subtitleMimeForFile({name: 'subtitles', type: 'text/vtt'}) === 'text/vtt', 'a typed vtt passes through');
assert(markdownMimeForFile({name: 'script.fountain', type: ''}) === 'text/x-fountain', 'fountain keeps its dialect mime');
assert(markdownMimeForFile({name: 'notes', type: 'text/markdown'}) === 'text/markdown', 'a typed markdown passes through');

// Containers WKWebView cannot decode render as poster stills, not players.
// Artifact URLs decide by extension, the live transcript's data URLs by the
// mime in the header.
assert(videoThumbnailOnlySrc('/atelier-artifact/x/vid_a1.flv'), 'an flv artifact renders as a still');
assert(videoThumbnailOnlySrc('/atelier-artifact/x/vid_b2.avi'), 'an avi artifact renders as a still');
assert(videoThumbnailOnlySrc('/atelier-artifact/x/vid_c3.ts'), 'a ts artifact renders as a still');
assert(videoThumbnailOnlySrc('/atelier-artifact/x/vid_d4.mpg'), 'an mpg artifact renders as a still');
assert(videoThumbnailOnlySrc('data:video/x-flv;base64,AAAA'), 'a flv data URL renders as a still');
assert(videoThumbnailOnlySrc('data:video/x-msvideo;base64,AAAA'), 'an avi data URL renders as a still');
assert(!videoThumbnailOnlySrc('/atelier-artifact/x/vid_e5.mp4'), 'an mp4 artifact plays');
assert(!videoThumbnailOnlySrc('/atelier-artifact/x/vid_f6.webm'), 'a webm artifact plays');
assert(!videoThumbnailOnlySrc('/atelier-artifact/x/vid_g7.mkv'), 'mkv keeps its player (predates the rule)');
assert(!videoThumbnailOnlySrc('data:video/mp4;base64,AAAA'), 'an mp4 data URL plays');
assert(!videoThumbnailOnlySrc('data:image/png;base64,AAAA'), 'a non-video data URL is not a still');
assert(!videoThumbnailOnlySrc('/atelier-artifact/x/vid_h8'), 'an extensionless URL is not a still');
