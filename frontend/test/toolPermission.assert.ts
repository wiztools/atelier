import type {ToolPermissionEvent} from '../src/types';
import {hasToolPermissionDetails, toolPermissionSummary, toolPermissionTitle} from '../src/toolPermission';

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function same(actual: unknown, expected: unknown, message: string): void {
  const got = JSON.stringify(actual);
  const want = JSON.stringify(expected);
  if (got !== want) throw new Error(`${message}: expected ${want}, got ${got}`);
}

// permission builds an atelier:tool-permission payload around the fields a
// case cares about.
function permission(fields: Partial<ToolPermissionEvent> & {toolName: string}): ToolPermissionEvent {
  return {id: 'perm_1', action: 'execute', summary: 'tool summary', ...fields};
}

// The dialog title names the two gated actions and falls back to a generic ask.
same(toolPermissionTitle(permission({toolName: 'run_command'})), 'Run command?', 'run_command title');
same(toolPermissionTitle(permission({toolName: 'write_file'})), 'Write file?', 'write_file title');
same(toolPermissionTitle(permission({toolName: 'generate_image'})), 'Allow tool action?', 'other tools get the generic title');

// The one-line summary: a command's first two words, else the target path
// shortened to ~, else the backend's own summary.
same(toolPermissionSummary(permission({toolName: 'run_command', command: ['git', 'status', '--short']})), 'git status', 'a command summarizes as its first two words');
same(toolPermissionSummary(permission({toolName: 'write_file', path: '/Users/alice/project/notes.md'})), '~/project/notes.md', 'a path summarizes home-shortened');
same(toolPermissionSummary(permission({toolName: 'write_file', path: '/tmp/out.txt'})), '/tmp/out.txt', 'paths outside a home stay as they are');
same(toolPermissionSummary(permission({toolName: 'run_command', command: [], summary: 'run nothing'})), 'run nothing', 'an empty command falls back to the summary');
same(toolPermissionSummary(permission({toolName: 'generate_image', summary: 'render a cat'})), 'render a cat', 'no command or path: the summary itself');

// The details disclosure appears only when it would show more than the summary.
assert(!hasToolPermissionDetails(permission({toolName: 'generate_image', summary: 'render a cat'})), 'a bare summary has nothing more to disclose');
assert(hasToolPermissionDetails(permission({toolName: 'run_command', command: ['ls']})), 'a command is a detail');
assert(hasToolPermissionDetails(permission({toolName: 'write_file', path: '/tmp/out.txt'})), 'a path is a detail');
assert(hasToolPermissionDetails(permission({toolName: 'write_file', contentPreview: 'hello'})), 'a content preview is a detail');
