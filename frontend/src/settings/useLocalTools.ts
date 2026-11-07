import {useEffect, useMemo, useState} from 'react';
import {DetectLocalTools} from '../../wailsjs/go/main/App';
import {main} from '../../wailsjs/go/models';

// useLocalTools owns the Local Tools detection report: the binaries Go found
// on PATH or under the Settings overrides, the Re-detect nonce and its busy
// flag, and each binary's status for the Settings rows. A changed override
// re-probes after a short debounce, so the statuses track an unsaved edit; the
// startup probe rides loadConfig, which writes the report through its setter.
function useLocalTools({configLoaded, whisperBinary, ffmpegBinary, ffprobeBinary, sipsBinary, magickBinary}: {
  configLoaded: boolean;
  whisperBinary: string;
  ffmpegBinary: string;
  ffprobeBinary: string;
  sipsBinary: string;
  magickBinary: string;
}) {
  const [localToolsReport, setLocalToolsReport] = useState<main.LocalToolsReport | null>(null);
  // The Local Tools tab's Re-detect button bumps the nonce so detection
  // re-runs without a binary edit — a `brew install` made while Settings is
  // open would otherwise never flip the status until the next launch.
  const [localToolsDetectNonce, setLocalToolsDetectNonce] = useState(0);
  const [localToolsDetecting, setLocalToolsDetecting] = useState(false);

  // Re-probe local CLI tools when a binary override changes — or the Local
  // Tools tab's Re-detect button bumps the nonce — so the transcription
  // dropdown and the tool statuses reflect an unsaved override or a fresh
  // install without waiting for a save (an empty override means PATH
  // auto-detection — the Go side treats it that way too).
  useEffect(() => {
    if (!configLoaded) {
      return;
    }
    const timer = window.setTimeout(() => {
      DetectLocalTools(new main.LocalToolOverrides({binaries: {whisper: whisperBinary, ffmpeg: ffmpegBinary, ffprobe: ffprobeBinary, sips: sipsBinary, imagemagick: magickBinary}}))
        .then((report) => setLocalToolsReport(report))
        .catch(() => setLocalToolsReport(null))
        .finally(() => setLocalToolsDetecting(false));
    }, 300);
    return () => window.clearTimeout(timer);
  }, [configLoaded, whisperBinary, ffmpegBinary, ffprobeBinary, sipsBinary, magickBinary, localToolsDetectNonce]);

  const redetectLocalTools = () => {
    setLocalToolsDetecting(true);
    setLocalToolsDetectNonce((nonce) => nonce + 1);
  };

  const whisperStatus = useMemo(
    () => localToolsReport?.binaries?.find((entry) => entry.key === 'whisper') ?? null,
    [localToolsReport],
  );

  const ffmpegStatus = useMemo(
    () => localToolsReport?.binaries?.find((entry) => entry.key === 'ffmpeg') ?? null,
    [localToolsReport],
  );
  const ffprobeStatus = useMemo(
    () => localToolsReport?.binaries?.find((entry) => entry.key === 'ffprobe') ?? null,
    [localToolsReport],
  );
  const sipsStatus = useMemo(
    () => localToolsReport?.binaries?.find((entry) => entry.key === 'sips') ?? null,
    [localToolsReport],
  );
  const magickStatus = useMemo(
    () => localToolsReport?.binaries?.find((entry) => entry.key === 'imagemagick') ?? null,
    [localToolsReport],
  );

  return {
    ffmpegStatus,
    ffprobeStatus,
    localToolsDetecting,
    magickStatus,
    redetectLocalTools,
    setLocalToolsReport,
    sipsStatus,
    whisperStatus,
  };
}

export {useLocalTools};
