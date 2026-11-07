import {useEffect, useState} from 'react';
import {
  ListFalVideoDurations,
  ListFalSoundEffectDurations,
  ListReplicateVideoDurations,
} from '../../wailsjs/go/main/App';
import {
  defaultVideoDuration,
  defaultVideoDurationOptions,
  defaultSoundDuration,
  defaultSoundDurationOptions,
} from '../modelDefaults';

// useDurationOptions owns the duration ladders the generation pickers offer:
// for each selected model (text-to-video, image-to-video, keyframe, extend,
// and sound effects), the durations its published schema accepts, with the
// house ladder when a schema can't be loaded. A model swap re-fetches its
// ladder; the text-to-video and sound loaders also move the selected duration
// onto the new ladder when it no longer offers the old one.
function useDurationOptions({videoProvider, falVideoModel, replicateVideoModel, setVideoDuration, audioProvider, falSoundEffectsModel, setSoundDuration, falVideoImageModel, replicateVideoImageModel, falVideoKeyframeModel, falVideoExtendModel, replicateVideoExtendModel}: {
  videoProvider: string;
  falVideoModel: string;
  replicateVideoModel: string;
  setVideoDuration: React.Dispatch<React.SetStateAction<string>>;
  audioProvider: string;
  falSoundEffectsModel: string;
  setSoundDuration: React.Dispatch<React.SetStateAction<string>>;
  falVideoImageModel: string;
  replicateVideoImageModel: string;
  falVideoKeyframeModel: string;
  falVideoExtendModel: string;
  replicateVideoExtendModel: string;
}) {
  const [videoDurationOptions, setVideoDurationOptions] = useState<string[]>(defaultVideoDurationOptions);
  const [videoDurationImageOptions, setVideoDurationImageOptions] = useState<string[]>(defaultVideoDurationOptions);
  const [videoDurationKeyframeOptions, setVideoDurationKeyframeOptions] = useState<string[]>(defaultVideoDurationOptions);
  const [videoDurationExtendOptions, setVideoDurationExtendOptions] = useState<string[]>(defaultVideoDurationOptions);
  const [soundDurationOptions, setSoundDurationOptions] = useState<string[]>(defaultSoundDurationOptions);

  // Each video picker fetches the duration values its selected model accepts
  // (ListFalVideoDurations → the model's published schema enum), then reconciles
  // the picker's current value into that set — so switching models never leaves
  // a duration selected that the model would 422 on (e.g. Seedance-only "auto"
  // on an integer-only model; see conv_4feb919a). The fetch refires only on
  // model change; the value reconcile reads the latest state inside the setter.
  // An empty/error result falls back to the generic option set.
  useEffect(() => {
    let cancelled = false;
    // The fetch follows the routed backend: fal's schema cache on fal,
    // Replicate's on replicate — the same seam generate_video routes by.
    const fetchDurations = videoProvider === 'replicate'
      ? ListReplicateVideoDurations(replicateVideoModel)
      : ListFalVideoDurations(falVideoModel);
    fetchDurations
      .then((durations) => {
        if (cancelled) return;
        const opts = durations && durations.length ? durations : defaultVideoDurationOptions;
        setVideoDurationOptions(opts);
        setVideoDuration((current) => (opts.includes(current) ? current : opts[0] ?? defaultVideoDuration));
      })
      .catch(() => setVideoDurationOptions(defaultVideoDurationOptions));
    return () => {
      cancelled = true;
    };
  }, [videoProvider, falVideoModel, replicateVideoModel]);

  // Sound duration options follow the selected sound-effects model the same
  // way: ListFalSoundEffectDurations returns the model's accepted durations
  // (elevenlabs' numeric 0.5–22 range synthesizes the ladder 1..22). 'auto' is
  // always offered first — the synthesized ladder carries only numbers, and
  // auto is the send-no-duration escape hatch the backend honors everywhere.
  // An empty/error result falls back to the generic option set, like video.
  // A Replicate-pinned audio provider skips the fal lookup and rides the
  // generic ladder (the replicate resolver clamps or drops an unsupported
  // length with a notice at call time).
  useEffect(() => {
    let cancelled = false;
    if (audioProvider === 'replicate') {
      setSoundDurationOptions(defaultSoundDurationOptions);
      return;
    }
    ListFalSoundEffectDurations(falSoundEffectsModel)
      .then((durations) => {
        if (cancelled) return;
        const schemaOpts = durations && durations.length ? durations : defaultSoundDurationOptions;
        const opts = schemaOpts.includes('auto') ? schemaOpts : ['auto', ...schemaOpts];
        setSoundDurationOptions(opts);
        setSoundDuration((current) => (opts.includes(current) ? current : defaultSoundDuration));
      })
      .catch(() => setSoundDurationOptions(defaultSoundDurationOptions));
    return () => {
      cancelled = true;
    };
  }, [audioProvider, falSoundEffectsModel]);

  useEffect(() => {
    let cancelled = false;
    const fetchDurations = videoProvider === 'replicate'
      ? ListReplicateVideoDurations(replicateVideoImageModel)
      : ListFalVideoDurations(falVideoImageModel);
    fetchDurations
      .then((durations) => {
        if (cancelled) return;
        setVideoDurationImageOptions(durations && durations.length ? durations : defaultVideoDurationOptions);
      })
      .catch(() => setVideoDurationImageOptions(defaultVideoDurationOptions));
    return () => {
      cancelled = true;
    };
  }, [videoProvider, falVideoImageModel, replicateVideoImageModel]);

  useEffect(() => {
    let cancelled = false;
    ListFalVideoDurations(falVideoKeyframeModel)
      .then((durations) => {
        if (cancelled) return;
        setVideoDurationKeyframeOptions(durations && durations.length ? durations : defaultVideoDurationOptions);
      })
      .catch(() => setVideoDurationKeyframeOptions(defaultVideoDurationOptions));
    return () => {
      cancelled = true;
    };
  }, [falVideoKeyframeModel]);

  useEffect(() => {
    let cancelled = false;
    // The extend picker's durations follow the routed backend like the t2v/i2v
    // pickers above.
    const fetchDurations = videoProvider === 'replicate'
      ? ListReplicateVideoDurations(replicateVideoExtendModel)
      : ListFalVideoDurations(falVideoExtendModel);
    fetchDurations
      .then((durations) => {
        if (cancelled) return;
        setVideoDurationExtendOptions(durations && durations.length ? durations : defaultVideoDurationOptions);
      })
      .catch(() => setVideoDurationExtendOptions(defaultVideoDurationOptions));
    return () => {
      cancelled = true;
    };
  }, [videoProvider, falVideoExtendModel, replicateVideoExtendModel]);

  return {
    soundDurationOptions,
    videoDurationExtendOptions,
    videoDurationImageOptions,
    videoDurationKeyframeOptions,
    videoDurationOptions,
  };
}

export {useDurationOptions};
