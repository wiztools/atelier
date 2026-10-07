export type VideoReframeAspectRatio = '9:16' | '1:1' | '16:9';
export type VideoReframeInterpolation = 'linear' | 'smooth' | 'hold';

export type VideoReframeSource = {
  width: number;
  height: number;
  durationSeconds: number;
};

export type VideoReframeRect = {
  x: number;
  y: number;
  width: number;
  height: number;
};

export type VideoReframeMarker = VideoReframeRect & {
  timeSeconds: number;
  interpolationToNext: VideoReframeInterpolation;
};

export type VideoReframeParams = {
  version: 1;
  source: VideoReframeSource;
  aspectRatio: VideoReframeAspectRatio;
  output: {
    width: number;
    height: number;
  };
  markers: VideoReframeMarker[];
};

const minDimension = 2;
const maxDimension = 32768;
const maxMarkers = 100;

const aspectPairs: Record<VideoReframeAspectRatio, {width: number; height: number}> = {
  '9:16': {width: 9, height: 16},
  '1:1': {width: 1, height: 1},
  '16:9': {width: 16, height: 9},
};

function isRecord(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object';
}

function fail(message: string): never {
  throw new Error(`Invalid video reframe params: ${message}`);
}

function integer(value: unknown, name: string): number {
  if (typeof value !== 'number' || !Number.isFinite(value) || Math.floor(value) !== value) {
    fail(`${name} must be an integer`);
  }
  return value;
}

function boundedDimension(value: unknown, name: string): number {
  const dimension = integer(value, name);
  if (dimension < minDimension || dimension > maxDimension) {
    fail(`${name} must be between ${minDimension} and ${maxDimension}`);
  }
  return dimension;
}

function evenDimension(value: unknown, name: string): number {
  const dimension = boundedDimension(value, name);
  if (dimension % 2 !== 0) fail(`${name} must be even`);
  return dimension;
}

function finiteNumber(value: unknown, name: string): number {
  if (typeof value !== 'number' || !Number.isFinite(value)) {
    fail(`${name} must be finite`);
  }
  return value;
}

function parseAspectRatio(value: unknown): VideoReframeAspectRatio {
  if (value !== '9:16' && value !== '1:1' && value !== '16:9') {
    fail('aspectRatio is not supported');
  }
  return value;
}

function parseInterpolation(value: unknown): VideoReframeInterpolation {
  if (value !== 'linear' && value !== 'smooth' && value !== 'hold') {
    fail('interpolationToNext is not supported');
  }
  return value;
}

function ratioMatches(width: number, height: number, aspectRatio: VideoReframeAspectRatio): boolean {
  const ratio = aspectPairs[aspectRatio];
  return width * ratio.height === height * ratio.width;
}

function clamp(value: number, min: number, max: number): number {
  if (max < min) return min;
  return Math.min(max, Math.max(min, value));
}

function roundPixel(value: number): number {
  return Math.floor(value + 0.5);
}

function markerRect(marker: VideoReframeMarker): VideoReframeRect {
  return {x: marker.x, y: marker.y, width: marker.width, height: marker.height};
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

function validateRectInSource(rect: VideoReframeRect, source: VideoReframeSource, aspectRatio: VideoReframeAspectRatio, label: string): void {
  if (rect.width % 2 !== 0 || rect.height % 2 !== 0) fail(`${label} crop dimensions must be even`);
  if (!ratioMatches(rect.width, rect.height, aspectRatio)) fail(`${label} crop dimensions must match ${aspectRatio}`);
  if (rect.x < 0 || rect.y < 0 || rect.x + rect.width > source.width || rect.y + rect.height > source.height) {
    fail(`${label} crop must stay inside the source`);
  }
}

function normalizedMarkers(markers: unknown, source: VideoReframeSource, aspectRatio: VideoReframeAspectRatio): VideoReframeMarker[] {
  if (!Array.isArray(markers)) fail('markers must be an array');
  if (markers.length < 1 || markers.length > maxMarkers) fail(`markers must contain 1 to ${maxMarkers} entries`);

  const parsed = markers.map((value, index) => {
    if (!isRecord(value)) fail(`markers[${index}] must be an object`);
    return {
      timeSeconds: finiteNumber(value.timeSeconds, `markers[${index}].timeSeconds`),
      x: integer(value.x, `markers[${index}].x`),
      y: integer(value.y, `markers[${index}].y`),
      width: evenDimension(value.width, `markers[${index}].width`),
      height: evenDimension(value.height, `markers[${index}].height`),
      interpolationToNext: parseInterpolation(value.interpolationToNext),
    };
  }).sort((a, b) => a.timeSeconds - b.timeSeconds);

  if (parsed[0].timeSeconds !== 0) fail('first marker must start at 0 seconds');

  const fixedWidth = parsed[0].width;
  const fixedHeight = parsed[0].height;
  parsed.forEach((marker, index) => {
    if (marker.timeSeconds < 0 || marker.timeSeconds > source.durationSeconds) {
      fail(`markers[${index}].timeSeconds must be inside the source duration`);
    }
    if (index > 0 && marker.timeSeconds === parsed[index - 1].timeSeconds) {
      fail('marker times must be unique');
    }
    if (marker.width !== fixedWidth || marker.height !== fixedHeight) {
      fail('all markers must use the same crop size');
    }
    validateRectInSource(markerRect(marker), source, aspectRatio, `markers[${index}]`);
  });

  return parsed.map(cloneMarker);
}

export function fitVideoReframeCrop(sourceWidth: number, sourceHeight: number, aspectRatio: VideoReframeAspectRatio): VideoReframeRect {
  const ratio = aspectPairs[parseAspectRatio(aspectRatio)];
  const width = boundedDimension(sourceWidth, 'sourceWidth');
  const height = boundedDimension(sourceHeight, 'sourceHeight');
  const multiplier = 2 * Math.floor(Math.min(width / ratio.width, height / ratio.height) / 2);
  if (multiplier < 2) fail('source is too small for the requested aspect ratio');
  const cropWidth = ratio.width * multiplier;
  const cropHeight = ratio.height * multiplier;
  return {
    x: roundPixel((width - cropWidth) / 2),
    y: roundPixel((height - cropHeight) / 2),
    width: cropWidth,
    height: cropHeight,
  };
}

export function validateVideoReframe(params: unknown): VideoReframeParams {
  if (!isRecord(params)) fail('params must be an object');
  if (params.version !== 1) fail('version must be 1');

  if (!isRecord(params.source)) fail('source must be an object');
  const source = {
    width: boundedDimension(params.source.width, 'source.width'),
    height: boundedDimension(params.source.height, 'source.height'),
    durationSeconds: finiteNumber(params.source.durationSeconds, 'source.durationSeconds'),
  };
  if (source.durationSeconds <= 0) fail('source.durationSeconds must be positive');

  const aspectRatio = parseAspectRatio(params.aspectRatio);

  if (!isRecord(params.output)) fail('output must be an object');
  const output = {
    width: evenDimension(params.output.width, 'output.width'),
    height: evenDimension(params.output.height, 'output.height'),
  };
  if (!ratioMatches(output.width, output.height, aspectRatio)) fail('output dimensions must match aspectRatio');

  const markers = normalizedMarkers(params.markers, source, aspectRatio);

  return {version: 1, source, aspectRatio, output, markers};
}

export function evaluateVideoReframe(params: VideoReframeParams, timeSeconds: number): VideoReframeRect {
  const valid = validateVideoReframe(params);
  const time = clamp(finiteNumber(timeSeconds, 'preview time'), 0, valid.source.durationSeconds);
  let markerIndex = 0;
  for (let i = 1; i < valid.markers.length && valid.markers[i].timeSeconds <= time; i++) {
    markerIndex = i;
  }

  const current = valid.markers[markerIndex];
  const next = valid.markers[markerIndex + 1];
  if (!next) return markerRect(current);

  const span = next.timeSeconds - current.timeSeconds;
  const rawU = span > 0 ? (time - current.timeSeconds) / span : 0;
  const u = current.interpolationToNext === 'hold'
    ? 0
    : current.interpolationToNext === 'smooth'
      ? rawU * rawU * (3 - 2 * rawU)
      : rawU;

  const x = clamp(roundPixel(current.x + (next.x - current.x) * u), 0, valid.source.width - current.width);
  const y = clamp(roundPixel(current.y + (next.y - current.y) * u), 0, valid.source.height - current.height);
  return {x, y, width: current.width, height: current.height};
}

export function changeVideoReframeAspect(params: VideoReframeParams, aspectRatio: VideoReframeAspectRatio): VideoReframeParams {
  const valid = validateVideoReframe(params);
  const nextAspectRatio = parseAspectRatio(aspectRatio);
  const crop = fitVideoReframeCrop(valid.source.width, valid.source.height, nextAspectRatio);

  const markers = valid.markers.map((marker) => {
    const centerX = marker.x + marker.width / 2;
    const centerY = marker.y + marker.height / 2;
    return {
      timeSeconds: marker.timeSeconds,
      x: clamp(roundPixel(centerX - crop.width / 2), 0, valid.source.width - crop.width),
      y: clamp(roundPixel(centerY - crop.height / 2), 0, valid.source.height - crop.height),
      width: crop.width,
      height: crop.height,
      interpolationToNext: marker.interpolationToNext,
    };
  });

  return {
    version: 1,
    source: {...valid.source},
    aspectRatio: nextAspectRatio,
    output: {width: crop.width, height: crop.height},
    markers,
  };
}
