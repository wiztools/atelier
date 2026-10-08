import {
  VideoReframeAspectRatio,
  VideoReframeInterpolation,
  VideoReframeMarker,
  VideoReframeParams,
  VideoReframeRect,
  changeVideoReframeAspect,
  evaluateVideoReframe,
  fitVideoReframeCrop,
  validateVideoReframe,
} from './videoReframe';

export type VideoOutputPreset = 'source' | '720p' | '1080p';

export const videoReframeAspects: VideoReframeAspectRatio[] = ['9:16', '1:1', '16:9'];
export const maxVideoReframeMarkers = 100;

const timePrecision = 1000;
const markerTimeEpsilon = 1 / timePrecision;

function clamp(value: number, min: number, max: number): number {
  if (max < min) return min;
  return Math.min(max, Math.max(min, value));
}

function roundTime(value: number): number {
  return Math.round(value * timePrecision) / timePrecision;
}

function roundEven(value: number): number {
  const rounded = Math.max(2, Math.round(value / 2) * 2);
  return rounded % 2 === 0 ? rounded : rounded - 1;
}

function aspectPair(aspectRatio: VideoReframeAspectRatio): {width: number; height: number} {
  if (aspectRatio === '1:1') return {width: 1, height: 1};
  if (aspectRatio === '16:9') return {width: 16, height: 9};
  return {width: 9, height: 16};
}

function cloneMarker(marker: VideoReframeMarker): VideoReframeMarker {
  return {
    timeSeconds: marker.timeSeconds,
    x: marker.x,
    y: marker.y,
    width: marker.width,
    height: marker.height,
    interpolationToNext: marker.interpolationToNext,
  };
}

export function defaultVideoReframeParams(source: {width: number; height: number; durationSeconds: number}, aspectRatio: VideoReframeAspectRatio = '9:16'): VideoReframeParams {
  const crop = fitVideoReframeCrop(source.width, source.height, aspectRatio);
  return {
    version: 1,
    source: {width: source.width, height: source.height, durationSeconds: source.durationSeconds},
    aspectRatio,
    output: {width: crop.width, height: crop.height},
    markers: [{...crop, timeSeconds: 0, interpolationToNext: 'smooth'}],
  };
}

export function clampVideoReframeRect(rect: VideoReframeRect, source: {width: number; height: number}): VideoReframeRect {
  return {
    x: clamp(Math.round(rect.x), 0, Math.max(0, source.width - rect.width)),
    y: clamp(Math.round(rect.y), 0, Math.max(0, source.height - rect.height)),
    width: rect.width,
    height: rect.height,
  };
}

export function outputForVideoPreset(params: VideoReframeParams, preset: VideoOutputPreset): {width: number; height: number; notices: string[]} {
  const valid = validateVideoReframe(params);
  const crop = valid.markers[0];
  if (preset === 'source') return {width: crop.width, height: crop.height, notices: []};

  const pair = aspectPair(valid.aspectRatio);
  const shortEdge = preset === '1080p' ? 1080 : 720;
  const scale = shortEdge / Math.min(pair.width, pair.height);
  const width = roundEven(pair.width * scale);
  const height = roundEven(pair.height * scale);
  const notices = width > crop.width || height > crop.height
    ? [`${preset} renders above the fixed crop size; ffmpeg will upscale the framed pixels.`]
    : [];
  return {width, height, notices};
}

export function setVideoOutputPreset(params: VideoReframeParams, preset: VideoOutputPreset): VideoReframeParams {
  const valid = validateVideoReframe(params);
  const output = outputForVideoPreset(valid, preset);
  return {...valid, output: {width: output.width, height: output.height}};
}

export function changeVideoAspectWithOutput(params: VideoReframeParams, aspectRatio: VideoReframeAspectRatio, preset: VideoOutputPreset): VideoReframeParams {
  return setVideoOutputPreset(changeVideoReframeAspect(params, aspectRatio), preset);
}

export function markerIndexAtTime(params: VideoReframeParams, timeSeconds: number): number {
  const valid = validateVideoReframe(params);
  const time = clamp(roundTime(clamp(timeSeconds, 0, valid.source.durationSeconds)), 0, valid.source.durationSeconds);
  const exact = valid.markers.findIndex((marker) => Math.abs(marker.timeSeconds - time) <= markerTimeEpsilon);
  if (exact >= 0) return exact;
  let nearest = 0;
  let distance = Number.POSITIVE_INFINITY;
  valid.markers.forEach((marker, index) => {
    const nextDistance = Math.abs(marker.timeSeconds - time);
    if (nextDistance < distance) {
      nearest = index;
      distance = nextDistance;
    }
  });
  return nearest;
}

export function upsertVideoReframeMarker(
  params: VideoReframeParams,
  timeSeconds: number,
  rect: VideoReframeRect,
  interpolationToNext?: VideoReframeInterpolation,
): VideoReframeParams {
  const valid = validateVideoReframe(params);
  const time = clamp(roundTime(clamp(timeSeconds, 0, valid.source.durationSeconds)), 0, valid.source.durationSeconds);
  const base = valid.markers[0];
  const nextRect = clampVideoReframeRect({...rect, width: base.width, height: base.height}, valid.source);
  const markers = valid.markers.map(cloneMarker);
  const existing = markers.findIndex((marker) => Math.abs(marker.timeSeconds - time) <= markerTimeEpsilon);
  if (existing >= 0) {
    markers[existing] = {
      ...markers[existing],
      ...nextRect,
      timeSeconds: markers[existing].timeSeconds === 0 ? 0 : time,
      interpolationToNext: interpolationToNext ?? markers[existing].interpolationToNext,
    };
  } else {
    if (markers.length >= maxVideoReframeMarkers) {
      throw new Error(`Video reframe supports at most ${maxVideoReframeMarkers} markers`);
    }
    markers.push({
      ...nextRect,
      timeSeconds: time,
      interpolationToNext: interpolationToNext ?? 'smooth',
    });
  }
  markers.sort((a, b) => a.timeSeconds - b.timeSeconds);
  markers[0].timeSeconds = 0;
  return validateVideoReframe({...valid, markers});
}

export function updateVideoReframeMarker(
  params: VideoReframeParams,
  markerIndex: number,
  patch: Partial<Pick<VideoReframeMarker, 'x' | 'y' | 'interpolationToNext'>>,
): VideoReframeParams {
  const valid = validateVideoReframe(params);
  if (markerIndex < 0 || markerIndex >= valid.markers.length) return valid;
  const markers = valid.markers.map(cloneMarker);
  const current = markers[markerIndex];
  const rect = clampVideoReframeRect({
    x: patch.x ?? current.x,
    y: patch.y ?? current.y,
    width: current.width,
    height: current.height,
  }, valid.source);
  markers[markerIndex] = {
    ...current,
    ...rect,
    interpolationToNext: patch.interpolationToNext ?? current.interpolationToNext,
  };
  return validateVideoReframe({...valid, markers});
}

export function moveVideoReframeMarkerTime(params: VideoReframeParams, markerIndex: number, timeSeconds: number): VideoReframeParams {
  const valid = validateVideoReframe(params);
  if (markerIndex <= 0 || markerIndex >= valid.markers.length) return valid;
  const markers = valid.markers.map(cloneMarker);
  const previous = markers[markerIndex - 1];
  const next = markers[markerIndex + 1];
  const min = previous.timeSeconds + markerTimeEpsilon;
  const max = next ? next.timeSeconds - markerTimeEpsilon : valid.source.durationSeconds;
  if (max < min) return valid;
  markers[markerIndex].timeSeconds = clamp(roundTime(clamp(timeSeconds, min, max)), min, max);
  return validateVideoReframe({...valid, markers});
}

export function deleteVideoReframeMarker(params: VideoReframeParams, markerIndex: number): VideoReframeParams {
  const valid = validateVideoReframe(params);
  if (markerIndex <= 0 || markerIndex >= valid.markers.length) return valid;
  const markers = valid.markers.filter((_, index) => index !== markerIndex).map(cloneMarker);
  return validateVideoReframe({...valid, markers});
}

export function selectedVideoRect(params: VideoReframeParams, currentTime: number, selectedMarkerIndex: number): VideoReframeRect {
  const valid = validateVideoReframe(params);
  const marker = valid.markers[selectedMarkerIndex];
  return marker ? {
    x: marker.x,
    y: marker.y,
    width: marker.width,
    height: marker.height,
  } : evaluateVideoReframe(valid, currentTime);
}

export function sameVideoParams(a: VideoReframeParams, b: VideoReframeParams): boolean {
  return JSON.stringify(validateVideoReframe(a)) === JSON.stringify(validateVideoReframe(b));
}
