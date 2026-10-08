export function terminalVideoEditStatus(status: string): boolean {
  return ['completed', 'failed', 'cancelled'].includes(status);
}

// Reconcile persisted snapshots with live events. A delayed queued/running
// snapshot must never replace a terminal operation received during an await.
export function mergeVideoEditOperations<T extends {id: string; status: string}>(base: T[], updates: T[]): T[] {
  const merged = [...base];
  for (const update of updates) {
    const index = merged.findIndex((op) => op.id === update.id);
    if (index < 0) merged.push(update);
    else if (!terminalVideoEditStatus(merged[index].status) || terminalVideoEditStatus(update.status)) merged[index] = update;
  }
  return merged;
}
