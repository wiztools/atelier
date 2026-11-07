// Composer attachment reading: turns a picked, dropped, or pasted File into
// an Attachment — a data URL for small files, or, for audio/video past
// mediaStageThresholdBytes, a chunked upload staged to disk through the
// media_upload.go bindings. readFileAsAttachment is the one entry point.

import {AppendMediaUploadChunk, BeginMediaUpload, CompleteMediaUpload, DiscardMediaUpload} from '../wailsjs/go/main/App';
import {
  bytesToBase64,
  imagePayloadForOllama,
  markdownMimeForFile,
  mediaKindForFile,
  mediaMimeForFile,
  subtitleMimeForFile,
} from './mediaIntake';
import type {Attachment} from './types';

// Attachments above mediaStageThresholdBytes are staged to disk in chunks
// (stageMediaFileAsAttachment) instead of carried as data URLs. 24MiB keeps
// the data-URL path for everything it is proven to serve (larger attachments
// persisted fine as ~32MB of base64 in the request JSON) and moves anything
// bigger off the WebView/bridge/JSON pipeline that cannot carry it.
const mediaStageThresholdBytes = 24 * 1024 * 1024;
// Raw bytes per staging chunk — ~11MB of base64 per IPC call: small enough
// that no single bridge message or JSON allocation gets large, large enough
// that a 700MB clip is ~90 calls instead of thousands.
const mediaUploadChunkBytes = 8 * 1024 * 1024;

async function readImageFile(file: File, nameOverride?: string): Promise<Attachment> {
  const dataURL = await new Promise<string>((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.onerror = () => reject(reader.error);
    reader.readAsDataURL(file);
  });

  return {
    name: nameOverride ?? file.name,
    src: dataURL,
    payload: imagePayloadForOllama(dataURL),
    kind: 'image',
  };
}

// readAudioFile mirrors readImageFile but keeps the full data URL as the
// payload — unlike Ollama-bound images, the OpenRouter input_audio part needs
// the data:audio/<fmt>;base64,... wrapper so openRouterInputAudio can split off
// the format. Audio input is OpenRouter-only; the harness rejects an audio turn
// on any other provider before it runs.
async function readAudioFile(file: File, nameOverride?: string): Promise<Attachment> {
  const dataURL = await mediaFileDataURL(file, 'audio');

  return {
    name: nameOverride ?? file.name,
    src: dataURL,
    payload: dataURL,
    kind: 'audio',
  };
}

// readVideoFile mirrors readAudioFile: it keeps the full data URL as the
// payload, since video input is tool-only and the backend resolves
// AttachedVideo from the data URL the frontend sends (decodeVideoPayload /
// readVideoArtifactAsDataURL).
async function readVideoFile(file: File, nameOverride?: string): Promise<Attachment> {
  const dataURL = await mediaFileDataURL(file, 'video');

  return {
    name: nameOverride ?? file.name,
    src: dataURL,
    payload: dataURL,
    kind: 'video',
  };
}

// mediaFileDataURL builds an audio/video attachment's data URL. FileReader
// labels a container the OS left unlabeled (.mkv) application/octet-stream,
// which the Go decoders and the OpenRouter audio part read as an unknown
// format — so the extension's mime stands in when the OS offered no type
// (mediaIntake.ts).
async function mediaFileDataURL(file: File, kind: 'audio' | 'video'): Promise<string> {
  if (file.type) {
    return await new Promise<string>((resolve, reject) => {
      const reader = new FileReader();
      reader.onload = () => resolve(String(reader.result));
      reader.onerror = () => reject(reader.error);
      reader.readAsDataURL(file);
    });
  }
  const bytes = await file.arrayBuffer();
  return `data:${mediaMimeForFile(file, kind)};base64,${bytesToBase64(bytes)}`;
}

// readSubtitleFile keeps the full data URL as the payload, mirroring
// audio/video: subtitle attachments are tool-only (convert_subtitles reads the
// persisted artifact), never model input, and the backend decodes the data URL
// exactly like the other media kinds.
async function readSubtitleFile(file: File, nameOverride?: string): Promise<Attachment> {
  const mime = subtitleMimeForFile(file) ?? 'text/vtt';
  const bytes = await file.arrayBuffer();
  const dataURL = `data:${mime};base64,${bytesToBase64(bytes)}`;
  return {
    name: nameOverride ?? file.name,
    src: dataURL,
    payload: dataURL,
    kind: 'subtitle',
  };
}

// readMarkdownFile keeps the full data URL as the payload, mirroring
// subtitles: markdown attachments are tool-only (the markdown editor reads the
// persisted artifact), never model input.
async function readMarkdownFile(file: File, nameOverride?: string): Promise<Attachment> {
  const mime = markdownMimeForFile(file) ?? 'text/markdown';
  const bytes = await file.arrayBuffer();
  const dataURL = `data:${mime};base64,${bytesToBase64(bytes)}`;
  return {
    name: nameOverride ?? file.name,
    src: dataURL,
    payload: dataURL,
    kind: 'markdown',
  };
}

// readFileAsAttachment dispatches to the right reader. Audio files route to
// readAudioFile, video files to readVideoFile (kind decided by
// mediaKindForFile — extension first, since real containers like .mkv carry no
// MIME on macOS), subtitle files (.vtt/.srt — by extension first, the OS type
// is unreliable) to readSubtitleFile, markdown documents (.md/.markdown) to
// readMarkdownFile; everything else (images today, and an unknown type the OS
// picker let through) routes to readImageFile, which produced the historical
// behavior.
//
// Audio/video past mediaStageThresholdBytes never become data URLs at all: the
// base64 payload would have to cross the Wails bridge inside the StreamChat
// JSON — a 700MB clip becomes a ~933MB string that the WebView cannot
// stringify, the bridge cannot carry, and a <video> element cannot load. They
// are staged to disk in chunks instead (media_upload.go) and carry the staged
// artifact URL, which renders, plays, and persists like any other artifact.
async function readFileAsAttachment(file: File, nameOverride?: string, onProgress?: (fraction: number) => void): Promise<Attachment> {
  if (subtitleMimeForFile(file)) {
    return readSubtitleFile(file, nameOverride);
  }
  if (markdownMimeForFile(file)) {
    return readMarkdownFile(file, nameOverride);
  }
  const kind = mediaKindForFile(file);
  if (kind) {
    if (file.size > mediaStageThresholdBytes) {
      return stageMediaFileAsAttachment(file, kind, nameOverride, onProgress);
    }
    return kind === 'audio' ? readAudioFile(file, nameOverride) : readVideoFile(file, nameOverride);
  }
  return readImageFile(file, nameOverride);
}

// stageMediaFileAsAttachment streams a large media file to the backend in
// base64 chunks (mediaUploadChunkBytes each, so no single IPC call grows past
// a few MB) and attaches the completed /atelier-artifact/ URL. The upload ID
// sequencing is strict — begin, appends, complete — and a thrown error aborts
// the attachment: the composer keeps the rest of the drop and the uploads
// sweep eventually reclaims the partial file.
async function stageMediaFileAsAttachment(file: File, kind: 'audio' | 'video', nameOverride?: string, onProgress?: (fraction: number) => void): Promise<Attachment> {
  const uploadID = await BeginMediaUpload(nameOverride ?? file.name, kind);
  try {
    for (let offset = 0; offset < file.size; offset += mediaUploadChunkBytes) {
      const slice = file.slice(offset, Math.min(offset + mediaUploadChunkBytes, file.size));
      const chunk = bytesToBase64(await slice.arrayBuffer());
      await AppendMediaUploadChunk(uploadID, chunk);
      onProgress?.(Math.min((offset + mediaUploadChunkBytes) / file.size, 1));
    }
    const staged = await CompleteMediaUpload(uploadID);
    return {
      name: nameOverride ?? file.name,
      src: staged.url,
      payload: staged.url,
      kind,
      stagedPath: staged.path,
    };
  } catch (error) {
    // A failed staging (disk error, vanished file) discards the partial file
    // right away — DiscardMediaUpload also resolves in-flight upload IDs —
    // and the caller surfaces the error while the drop continues with the
    // remaining files.
    await DiscardMediaUpload(uploadID).catch(() => {});
    throw error;
  }
}

export {mediaStageThresholdBytes, readFileAsAttachment};
