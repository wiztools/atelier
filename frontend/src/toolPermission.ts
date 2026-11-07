// The tool-permission prompt's view model: the dialog title, the one-line
// summary (a command's first words or a home-shortened path), and whether
// the details disclosure has anything more to show.

import type {ToolPermissionEvent} from './types';
import {shortenHomePath} from './utils';

function toolPermissionTitle(permission: ToolPermissionEvent): string {
  if (permission.toolName === 'run_command') {
    return 'Run command?';
  }
  if (permission.toolName === 'write_file') {
    return 'Write file?';
  }
  return 'Allow tool action?';
}

function toolPermissionSummary(permission: ToolPermissionEvent): string {
  if (permission.toolName === 'run_command' && permission.command?.length) {
    return permission.command.slice(0, 2).join(' ');
  }
  if (permission.path) {
    return shortenHomePath(permission.path);
  }
  return permission.summary;
}

function hasToolPermissionDetails(permission: ToolPermissionEvent): boolean {
  return Boolean(
    permission.command?.length ||
    permission.path ||
    permission.contentPreview ||
    (permission.summary && permission.summary !== toolPermissionSummary(permission)),
  );
}

export {toolPermissionTitle, toolPermissionSummary, hasToolPermissionDetails};
