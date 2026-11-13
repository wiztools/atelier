// fal/Replicate model-catalog helpers: the option-list builder that
// disambiguates colliding display names, the speech-picker's voice-design
// badge, and the rate-limit ride-out wrapper the catalog loads run through.
import {main} from '../../wailsjs/go/models';
import {asArray} from '../utils';

// falCatalogWithRetry rides out fal's /v1/models rate limit. The settings
// screen fires the whole catalog plus every per-category walk on load — a
// burst of ~30 paginated requests that fal answers with 429s once its window
// is exhausted — and the audio listers sit at the END of the sequential chain,
// so their swallowed failures left those pickers with zero options and, where
// no static fallback exists, a combobox that renders no dropdown at all. The
// waits grow per attempt (the window refills on the order of seconds to a
// minute) and stop at the first non-empty load; a lister that legitimately
// returns nothing keeps its empty result, and a persistent failure still
// throws so each caller's catch keeps its own degrade semantics (clear vs
// keep-last-good).
async function falCatalogWithRetry<T>(load: () => Promise<T[]>, attempts = 3): Promise<T[]> {
  const waits = [2000, 5000];
  let lastError: unknown = new Error('catalog load failed');
  for (let attempt = 1; attempt <= attempts; attempt++) {
    try {
      const value = asArray(await load());
      if (value.length > 0 || attempt === attempts) {
        return value;
      }
    } catch (error) {
      lastError = error;
    }
    if (attempt < attempts) {
      await new Promise((resolve) => setTimeout(resolve, waits[Math.min(attempt - 1, waits.length - 1)]));
    }
  }
  throw lastError;
}

// falModelOptionList builds sorted combobox options for a fal model catalog.
// fal's catalog reuses one display name across endpoint variants — e.g.
// fal-ai/speech-to-text, .../turbo, .../stream, and .../turbo/stream all carry
// the display name "Speech-to-Text", making them indistinguishable in a
// dropdown. To disambiguate without guessing fal's org/name split (ambiguous
// from the id alone), we only annotate when a display name is shared by more
// than one endpoint: each colliding entry gets its distinguishing id tail
// (everything after the shared prefix) appended. Non-colliding entries keep
// their clean display name.
// falModelOptionList turns a provider catalog (fal's or Replicate's — both
// carry {id, displayName}) into combobox options, qualifying display names
// that collide across owners.
// speechModelOptionsWithVoiceDesignBadge marks the MiniMax speech endpoints in
// the Speech Model picker — the one speech family whose voice input accepts
// the reusable ids generate_voice designs — so the voice-design pairing is
// visible (and searchable via the label) in the long speech list without
// consulting the model docs.
function speechModelOptionsWithVoiceDesignBadge(options: {value: string; label: string}[]): {value: string; label: string}[] {
  return options.map((option) => {
    const id = option.value.toLowerCase();
    if (id.includes('minimax') && id.includes('speech')) {
      return {...option, label: `${option.label} — accepts voice-design ids`};
    }
    return option;
  });
}

function falModelOptionList(models: {id?: string; displayName?: string}[] | null | undefined): {value: string; label: string}[] {
  const items = asArray(models).map((item) => ({id: item.id || '', base: item.displayName || item.id || ''}));
  // Group by display name to find collisions.
  const counts: Record<string, number> = {};
  for (const item of items) {
    const key = item.base.toLowerCase();
    counts[key] = (counts[key] ?? 0) + 1;
  }
  return items
    .map((item) => {
      let label = item.base;
      if ((counts[item.base.toLowerCase()] ?? 0) > 1) {
        // Find the longest id prefix shared by every colliding endpoint, then
        // append whatever follows it as the distinguishing tail.
        const colliders = items.filter((other) => other.base.toLowerCase() === item.base.toLowerCase()).map((other) => other.id);
        const tail = idTailAfterSharedPrefix(item.id, colliders);
        if (tail) {
          label = `${item.base} (${tail})`;
        }
      }
      return {value: item.id, label};
    })
    .sort((a, b) => a.label.localeCompare(b.label));
}

// idTailAfterSharedPrefix returns the portion of id that follows the longest
// slash-delimited prefix shared by all the given ids. For the speech-to-text
// collision (fal-ai/speech-to-text, .../turbo, .../stream, .../turbo/stream)
// the shared prefix is "fal-ai/speech-to-text", so the tails are "", "turbo",
// "stream", "turbo/stream" — and an empty tail collapses back to the base name.
function idTailAfterSharedPrefix(id: string, ids: string[]): string {
  if (ids.length === 0) {
    return '';
  }
  const splitIds = ids.map((other) => other.split('/').filter(Boolean));
  const thisSegments = id.split('/').filter(Boolean);
  let shared = 0;
  const minLen = Math.min(...splitIds.map((segments) => segments.length));
  for (let i = 0; i < minLen; i++) {
    if (splitIds.every((segments) => segments[i] === thisSegments[i])) {
      shared = i + 1;
    } else {
      break;
    }
  }
  return thisSegments.slice(shared).join('/');
}

export {
  falCatalogWithRetry,
  speechModelOptionsWithVoiceDesignBadge,
  falModelOptionList,
  idTailAfterSharedPrefix,
};
