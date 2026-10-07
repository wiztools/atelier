import {
  VideoReframeParams,
  VideoReframeRect,
  changeVideoReframeAspect,
  evaluateVideoReframe,
  fitVideoReframeCrop,
  validateVideoReframe,
} from '../src/editor/videoReframe';

type FitFixture = {
  name: string;
  sourceWidth: number;
  sourceHeight: number;
  aspectRatio: VideoReframeParams['aspectRatio'];
  expected: VideoReframeRect;
};

type TimelineFixture = {
  name: string;
  params: VideoReframeParams;
  samples: {timeSeconds: number; expected: VideoReframeRect}[];
};

type RatioChangeFixture = {
  name: string;
  params: VideoReframeParams;
  aspectRatio: VideoReframeParams['aspectRatio'];
  expected: VideoReframeParams;
};

export type VideoReframeFixture = {
  fit: FitFixture[];
  timelines: TimelineFixture[];
  ratioChanges: RatioChangeFixture[];
};

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function equal(actual: unknown, expected: unknown, message: string): void {
  const actualJson = JSON.stringify(actual);
  const expectedJson = JSON.stringify(expected);
  assert(actualJson === expectedJson, `${message}\nexpected ${expectedJson}\nactual   ${actualJson}`);
}

function throws(fn: () => void, message: string): void {
  let threw = false;
  try {
    fn();
  } catch (_) {
    threw = true;
  }
  assert(threw, message);
}

function validBase(): VideoReframeParams {
  return {
    version: 1,
    source: {width: 1920, height: 1080, durationSeconds: 12},
    aspectRatio: '9:16',
    output: {width: 1080, height: 1920},
    markers: [
      {timeSeconds: 0, x: 0, y: 12, width: 594, height: 1056, interpolationToNext: 'linear'},
      {timeSeconds: 4, x: 660, y: 12, width: 594, height: 1056, interpolationToNext: 'smooth'},
    ],
  };
}

function mutated(copy: VideoReframeParams, edit: (params: VideoReframeParams) => void): VideoReframeParams {
  edit(copy);
  return copy;
}

export function runVideoReframeAssertions(fixture: VideoReframeFixture): void {
  assert(Array.isArray(fixture.fit), 'fixture.fit must be an array');
  assert(Array.isArray(fixture.timelines), 'fixture.timelines must be an array');
  assert(Array.isArray(fixture.ratioChanges), 'fixture.ratioChanges must be an array');

  for (const item of fixture.fit) {
    equal(
      fitVideoReframeCrop(item.sourceWidth, item.sourceHeight, item.aspectRatio),
      item.expected,
      `fit fixture failed: ${item.name}`,
    );
  }

  for (const item of fixture.timelines) {
    const valid = validateVideoReframe(item.params);
    for (const sample of item.samples) {
      equal(evaluateVideoReframe(valid, sample.timeSeconds), sample.expected, `timeline fixture failed: ${item.name}`);
    }
  }

  for (const item of fixture.ratioChanges) {
    equal(changeVideoReframeAspect(item.params, item.aspectRatio), item.expected, `ratio change fixture failed: ${item.name}`);
  }

  const unsorted = validBase();
  unsorted.markers = [unsorted.markers[1], unsorted.markers[0]];
  const validated = validateVideoReframe(unsorted);
  assert(validated.markers[0].timeSeconds === 0 && validated.markers[1].timeSeconds === 4, 'validate sorts marker clones by time');
  assert(unsorted.markers[0].timeSeconds === 4, 'validate does not mutate marker order');
  validated.markers[0].x = 123;
  assert(unsorted.markers[1].x === 0, 'validate returns detached marker clones');

  equal(evaluateVideoReframe(validBase(), -1), {x: 0, y: 12, width: 594, height: 1056}, 'evaluate clamps negative time');
  equal(evaluateVideoReframe(validBase(), 99), {x: 660, y: 12, width: 594, height: 1056}, 'evaluate clamps after duration');
  throws(() => evaluateVideoReframe(validBase(), Number.NaN), 'evaluate rejects nan preview time');
  throws(() => evaluateVideoReframe(validBase(), Number.POSITIVE_INFINITY), 'evaluate rejects infinite preview time');

  const hold = mutated(validBase(), (params) => {
    params.markers[0].interpolationToNext = 'hold';
  });
  equal(evaluateVideoReframe(hold, 2), {x: 0, y: 12, width: 594, height: 1056}, 'hold keeps the earlier crop');

  throws(() => validateVideoReframe(mutated(validBase(), (params) => { params.source.width = Number.NaN; })), 'rejects nan source dimensions');
  throws(() => validateVideoReframe(mutated(validBase(), (params) => { params.source.durationSeconds = Number.POSITIVE_INFINITY; })), 'rejects infinite duration');
  throws(() => validateVideoReframe(mutated(validBase(), (params) => { params.markers[0].x = 1.5; })), 'rejects non-integer positions');
  throws(() => validateVideoReframe(mutated(validBase(), (params) => { params.markers[0].x = -1; })), 'rejects out-of-bounds crops');
  throws(() => validateVideoReframe(mutated(validBase(), (params) => { params.markers[0].timeSeconds = 1; })), 'rejects timelines missing zero');
  throws(() => validateVideoReframe(mutated(validBase(), (params) => { params.markers[1].timeSeconds = 0; })), 'rejects duplicate marker times');
  throws(() => validateVideoReframe(mutated(validBase(), (params) => { params.markers[1].width = 596; })), 'rejects variable crop size');
  throws(() => validateVideoReframe(mutated(validBase(), (params) => { params.markers[0].interpolationToNext = 'snap' as 'linear'; })), 'rejects bad interpolation mode');
  throws(() => validateVideoReframe(mutated(validBase(), (params) => { params.markers = []; })), 'rejects empty markers');
  throws(() => validateVideoReframe(mutated(validBase(), (params) => {
    params.markers = Array.from({length: 101}, (_, index) => ({
      timeSeconds: index === 0 ? 0 : index / 10,
      x: 0,
      y: 12,
      width: 594,
      height: 1056,
      interpolationToNext: 'linear',
    }));
  })), 'rejects too many markers');
  throws(() => fitVideoReframeCrop(2, 2, '9:16'), 'rejects sources too small for a fixed even crop');

  console.log('videoReframe assertions passed');
}
