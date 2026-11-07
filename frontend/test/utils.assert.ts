import {asArray, formatError, formatFalError, formatModelSize, formatOpenRouterError, inferHomePath, shortenHomePath} from '../src/utils';

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function same(actual: unknown, expected: unknown, message: string): void {
  const got = JSON.stringify(actual);
  const want = JSON.stringify(expected);
  if (got !== want) throw new Error(`${message}: expected ${want}, got ${got}`);
}

// Bindings decode a Go nil slice as null; asArray makes every list iterable.
const list = [1, 2];
assert(asArray(list) === list, 'an array passes through untouched');
same(asArray(null), [], 'null reads as empty');
same(asArray(undefined), [], 'undefined reads as empty');

// Binary units; one decimal below 10, whole numbers from 10 up.
same(formatModelSize(0), '', 'zero has no size');
same(formatModelSize(-1), '', 'a negative size renders nothing');
same(formatModelSize(Number.NaN), '', 'NaN renders nothing');
same(formatModelSize(999), '999 B', 'bytes stay whole');
same(formatModelSize(1536), '1.5 KB', 'one decimal below 10');
same(formatModelSize(20 * 1024 * 1024), '20 MB', 'whole numbers from 10 up');
same(formatModelSize(4.7 * 1024 ** 3), '4.7 GB', 'model-sized files read in GB');
same(formatModelSize(2048 * 1024 ** 4), '2048 TB', 'TB is the largest unit');

// A home directory prefix collapses to ~.
same(shortenHomePath('/Users/alice/project/a.txt'), '~/project/a.txt', 'macOS home');
same(shortenHomePath('/home/bob/work'), '~/work', 'Linux home');
same(shortenHomePath('/Users/alice'), '~', 'the home itself');
same(shortenHomePath('/tmp/scratch'), '/tmp/scratch', 'paths outside a home stay as they are');
same(inferHomePath('/Users/alice/project'), '/Users/alice', 'the home is the user directory');
same(inferHomePath('relative/path'), '', 'a relative path has no home');

// formatError reads an Error's message and stringifies anything else; the
// provider variants turn an auth failure into the remedy.
same(formatError(new Error('boom')), 'boom', 'an Error yields its message');
same(formatError('plain failure'), 'plain failure', 'a string passes through');
same(formatError(42), '42', 'other values stringify');
same(formatOpenRouterError(new Error('openrouter authentication failed')), 'Invalid API key — check your OpenRouter key in Settings', 'an auth failure names the fix');
same(formatOpenRouterError(new Error('HTTP 401')), 'Invalid API key — check your OpenRouter key in Settings', 'a 401 is an auth failure');
same(formatOpenRouterError(new Error('rate limited')), 'rate limited', 'other errors pass through');
same(formatFalError('Unauthorized'), 'Invalid API key — check your fal.ai key in Settings', 'unauthorized, in any case, names the fal fix');
same(formatFalError(new Error('queue timeout')), 'queue timeout', 'other fal errors pass through');
