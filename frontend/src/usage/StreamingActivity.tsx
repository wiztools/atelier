import {useEffect, useState} from 'react';
import {HarnessRunView, HarnessStepView} from '../types';
import {asArray} from '../utils';
import {formatToolName, formatDuration} from './usageModel';

// Live streaming feedback inside the chat area: the sidebar's spinner is easy
// to miss, so the streaming assistant bubble names what the harness is doing
// right now — the latest in-flight step of the live run snapshot — with a
// spinner and a ticking elapsed clock. Snapshots only arrive on step
// transitions, so without the local timer a two-minute video generation would
// read as frozen. Mounts only while a turn streams with no response text yet;
// once content flows the blinking caret takes over.
function StreamingActivity({run}: {run?: HarnessRunView}) {
  const [, setTick] = useState(0);
  useEffect(() => {
    const timer = window.setInterval(() => setTick((tick) => tick + 1), 1000);
    return () => window.clearInterval(timer);
  }, []);
  const active = [...asArray(run?.steps)].reverse().find((step) => step.status === 'running');
  const elapsed = active?.startedAt ? Math.max(0, Date.now() - new Date(active.startedAt).getTime()) : 0;
  return (
    <div className="stream-activity">
      <span className="stream-activity-spinner" aria-hidden="true" />
      <span className="stream-activity-label">{active ? streamActivityLabel(active) : 'thinking'}</span>
      {elapsed > 0 ? <span className="stream-activity-elapsed">{formatDuration(elapsed)}</span> : null}
    </div>
  );
}

// One line naming the phase a streaming turn is in. A tool round reads the
// step summary — which names the planned tools ("running generate_video",
// harness.go) — because the per-tool activities only attach once the whole
// batch completes, making the summary the only live signal of what runs.
function streamActivityLabel(step: HarnessStepView): string {
  switch (step.kind) {
    case 'triage':
      return 'routing request';
    case 'skill':
      return 'matching skills';
    case 'planning':
      return 'planning tool calls';
    case 'tool_call': {
      const summary = step.summary ?? '';
      if (summary.startsWith('running ')) {
        const names = summary.slice('running '.length)
          .split(',')
          .map((name) => formatToolName(name.trim()))
          .join(', ');
        return `running ${names}`;
      }
      return 'running tools';
    }
    case 'document':
      return 'writing document';
    case 'model_call':
      return 'generating response';
    case 'streaming':
      return 'streaming response';
    default:
      return 'working';
  }
}

export {StreamingActivity, streamActivityLabel};
