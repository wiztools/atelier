import {detectEditorLaunchIntent} from '../src/editorLaunch';

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function kinds(...items: Array<'image' | 'audio' | 'video' | 'subtitle' | 'markdown'>): Array<'image' | 'audio' | 'video' | 'subtitle' | 'markdown'> {
  return items;
}

// The canonical phrasings.
assert(detectEditorLaunchIntent('Open this image in image editor', kinds('image')) === 'image', 'canonical image phrasing');
assert(detectEditorLaunchIntent('Open in video editor', kinds('video')) === 'video', 'canonical video phrasing');
assert(detectEditorLaunchIntent('Open in audio editor', kinds('audio')) === 'audio', 'canonical audio phrasing');

// Subtitle files are an editor subject now: the kind resolves from the
// subtitle vocabulary or a lone .srt attachment, like the other kinds.
assert(detectEditorLaunchIntent('Open this in the subtitle editor', kinds('subtitle')) === 'subtitle', 'subtitle editor phrasing');
assert(detectEditorLaunchIntent('Open the subtitles in the editor', kinds('subtitle')) === 'subtitle', 'subtitle media word resolves');
assert(detectEditorLaunchIntent('Open this srt in the editor', kinds('subtitle')) === 'subtitle', 'extension word resolves');
assert(detectEditorLaunchIntent('Open the vtt editor for me', kinds('subtitle')) === 'subtitle', 'vtt word and trailing politeness');
assert(detectEditorLaunchIntent('Open in the editor', kinds('subtitle')) === 'subtitle', 'lone subtitle attachment resolves');
assert(detectEditorLaunchIntent('Open in the editor', kinds('subtitle', 'image')) === null, 'subtitle plus image without a kind word abstains');

// Punctuation, case, and articles.
assert(detectEditorLaunchIntent('open this image in the image editor.', kinds('image')) === 'image', 'punctuation and articles');
assert(detectEditorLaunchIntent('Please open that clip in the clip editor', kinds('video')) === 'video', 'please prefix');
assert(detectEditorLaunchIntent('Open the editor', kinds('video')) === 'video', 'no media word resolves from the single attachment');
assert(detectEditorLaunchIntent('Open up this photo in the editor', kinds('image')) === 'image', 'media word anywhere in the message');
assert(detectEditorLaunchIntent('Open this recording in the editor', kinds('audio')) === 'audio', 'audio media word resolves');
assert(detectEditorLaunchIntent('Open the sound editor for me', kinds('audio')) === 'audio', 'trailing politeness binds for audio');

// The word before "editor" wins over other media words.
assert(detectEditorLaunchIntent('open the video in the image editor', kinds('image', 'video')) === 'image', 'editor noun names the kind');

// Mismatch between the named editor and the attachment falls through.
assert(detectEditorLaunchIntent('Open this image in the video editor', kinds('image')) === null, 'named editor without a matching attachment');
assert(detectEditorLaunchIntent('Open this image in the audio editor', kinds('image')) === null, 'audio editor named without audio');

// Ambiguity falls through.
assert(detectEditorLaunchIntent('Open in the editor', kinds('image', 'video')) === null, 'two media attachments without a kind word');
assert(detectEditorLaunchIntent('Open this image in image editor', kinds('image', 'image')) === null, 'two candidates for the named kind');
assert(detectEditorLaunchIntent('Open in the editor', kinds('audio', 'image')) === null, 'audio plus image without a kind word abstains');
assert(detectEditorLaunchIntent('Open in the editor', kinds('audio')) === 'audio', 'audio alone opens the audio editor');

// Nothing attached is a normal chat turn.
assert(detectEditorLaunchIntent('Open this image in image editor', kinds()) === null, 'no attachment');
assert(detectEditorLaunchIntent('Open in video editor', kinds('image')) === null, 'image attached for the video editor');

// Not an "open" command, or not naming an editor.
assert(detectEditorLaunchIntent('opened this image in the editor yesterday', kinds('image')) === null, 'past tense is not a command');
assert(detectEditorLaunchIntent('How do I open this image in the image editor', kinds('image')) === null, 'questions stay chat');
assert(detectEditorLaunchIntent('Open this image in Photoshop', kinds('image')) === null, 'no editor named');
assert(detectEditorLaunchIntent('Editing this image in the image editor now', kinds('image')) === null, 'statements stay chat');
assert(detectEditorLaunchIntent('', kinds('image')) === null, 'empty text');

// Trailing politeness binds; trailing content does not.
assert(detectEditorLaunchIntent('Open the image editor for me', kinds('image')) === 'image', 'trailing politeness binds');
assert(detectEditorLaunchIntent('open this image in the image editor please', kinds('image')) === 'image', 'trailing please binds');
assert(detectEditorLaunchIntent('Open the image editor for my client', kinds('image')) === null, 'trailing content abstains');
assert(detectEditorLaunchIntent('Open the video editor now', kinds('video')) === null, 'unlisted trailing word abstains');

// Compound requests: the grammar binds every token or abstains — a second
// intent is detected as unbindable content, never semantically.
assert(detectEditorLaunchIntent('Open the editor and stuff', kinds('image')) === null, 'loose tail abstains');
assert(detectEditorLaunchIntent('Open this clip in the video editor and extract its audio', kinds('video')) === null, 'second intent abstains');
assert(detectEditorLaunchIntent('Open the image and the video editors', kinds('image', 'video')) === null, 'two editors in one message abstains');
assert(detectEditorLaunchIntent('Open the editor, then make it black and white', kinds('image')) === null, 'follow-up instruction abstains');

// Markdown documents are the fifth editor subject: the document vocabulary or
// a lone .md attachment resolves, like subtitles.
assert(detectEditorLaunchIntent('Open this in the markdown editor', kinds('markdown')) === 'markdown', 'markdown editor phrasing');
assert(detectEditorLaunchIntent('Open the document in the editor', kinds('markdown')) === 'markdown', 'document media word resolves');
assert(detectEditorLaunchIntent('Open the markdown editor for me', kinds('markdown')) === 'markdown', 'markdown word and trailing politeness');
assert(detectEditorLaunchIntent('Open in the editor', kinds('markdown')) === 'markdown', 'lone markdown attachment resolves');
assert(detectEditorLaunchIntent('Open in the editor', kinds('markdown', 'image')) === null, 'markdown plus image without a kind word abstains');
assert(detectEditorLaunchIntent('Open this md in the editor', kinds('markdown')) === 'markdown', 'extension word resolves');

// The screenplay vocabulary rides the markdown editor (Fountain is its dialect).
assert(detectEditorLaunchIntent('Open the screenplay editor', kinds('markdown')) === 'markdown', 'screenplay editor phrasing');
assert(detectEditorLaunchIntent('Open this script in the editor', kinds('markdown')) === 'markdown', 'script media word resolves');
assert(detectEditorLaunchIntent('Open the fountain editor for me', kinds('markdown')) === 'markdown', 'fountain word and trailing politeness');
