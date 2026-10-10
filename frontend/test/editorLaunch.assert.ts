import {detectEditorLaunchIntent} from '../src/editorLaunch';

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function kinds(...items: Array<'image' | 'audio' | 'video'>): Array<'image' | 'audio' | 'video'> {
  return items;
}

// The two canonical phrasings.
assert(detectEditorLaunchIntent('Open this image in image editor', kinds('image')) === 'image', 'canonical image phrasing');
assert(detectEditorLaunchIntent('Open in video editor', kinds('video')) === 'video', 'canonical video phrasing');

// Punctuation, case, and articles.
assert(detectEditorLaunchIntent('open this image in the image editor.', kinds('image')) === 'image', 'punctuation and articles');
assert(detectEditorLaunchIntent('Please open that clip in the clip editor', kinds('video')) === 'video', 'please prefix');
assert(detectEditorLaunchIntent('Open the editor', kinds('video')) === 'video', 'no media word resolves from the single attachment');
assert(detectEditorLaunchIntent('Open up this photo in the editor', kinds('image')) === 'image', 'media word anywhere in the message');

// The word before "editor" wins over other media words.
assert(detectEditorLaunchIntent('open the video in the image editor', kinds('image', 'video')) === 'image', 'editor noun names the kind');

// Mismatch between the named editor and the attachment falls through.
assert(detectEditorLaunchIntent('Open this image in the video editor', kinds('image')) === null, 'named editor without a matching attachment');

// Ambiguity falls through.
assert(detectEditorLaunchIntent('Open in the editor', kinds('image', 'video')) === null, 'two media attachments without a kind word');
assert(detectEditorLaunchIntent('Open this image in image editor', kinds('image', 'image')) === null, 'two candidates for the named kind');
assert(detectEditorLaunchIntent('Open in the editor', kinds('audio')) === null, 'audio alone is not an editor asset');

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
