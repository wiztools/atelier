// Small shared utilities with no home of their own yet — generic value
// helpers (asArray), byte formatting (formatModelSize), home-directory path
// shortening (shortenHomePath), and error-message text (formatError and its
// provider-key variants) that several screens and modules read.

function asArray<T>(value: T[] | null | undefined): T[] {
  return Array.isArray(value) ? value : [];
}

function formatModelSize(size: number): string {
  if (!Number.isFinite(size) || size <= 0) {
    return '';
  }
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let value = size;
  let unitIndex = 0;
  while (value >= 1024 && unitIndex < units.length - 1) {
    value /= 1024;
    unitIndex += 1;
  }
  return `${value >= 10 || unitIndex === 0 ? value.toFixed(0) : value.toFixed(1)} ${units[unitIndex]}`;
}

// shortenHomePath renders a home-relative path in ~ form (macOS
// /Users/<name>, Linux /home/<name>; /Users/Shared is not a home).
function shortenHomePath(path: string): string {
  const match = /^\/(?:Users\/(?!Shared(?:\/|$))[^/]+|home\/[^/]+)/.exec(path);
  return match ? `~${path.slice(match[0].length)}` : path;
}

function formatError(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function formatOpenRouterError(error: unknown): string {
  const message = formatError(error);
  const lower = message.toLowerCase();
  if (lower.includes('authentication failed') || lower.includes('401') || lower.includes('unauthorized')) {
    return 'Invalid API key — check your OpenRouter key in Settings';
  }
  return message;
}

function formatFalError(error: unknown): string {
  const message = formatError(error);
  const lower = message.toLowerCase();
  if (lower.includes('authentication failed') || lower.includes('401') || lower.includes('unauthorized')) {
    return 'Invalid API key — check your fal.ai key in Settings';
  }
  return message;
}

export {
  asArray,
  formatModelSize,
  shortenHomePath,
  formatError,
  formatOpenRouterError,
  formatFalError,
};
