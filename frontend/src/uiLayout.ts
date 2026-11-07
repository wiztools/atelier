// UI layout constants and their persisted-size helpers: the sidebar and
// assets-panel width bounds (clamped the same way wherever a drag or a
// restored localStorage value lands outside them) and the history list's
// compact/batch display limits.

const defaultSidebarWidth = 320;
const minSidebarWidth = 240;
const maxSidebarWidth = 560;
// The assets panel snaps closed when a drag crosses minAssetsPanelWidth —
// below it the media cards no longer render properly. maxAssetsPanelWidth
// keeps room for the chat column, mirroring the sidebar clamps.
const minAssetsPanelWidth = 240;
const maxAssetsPanelWidth = 560;
const defaultAssetsPanelWidth = 300;
const compactHistoryLimit = 10;
const expandedHistoryBatchSize = 20;

function loadSidebarWidth(): number {
  const stored = Number(window.localStorage.getItem('atelier.sidebarWidth'));
  return clampSidebarWidth(Number.isFinite(stored) && stored > 0 ? stored : defaultSidebarWidth);
}

function clampSidebarWidth(width: number, max = maxSidebarWidth): number {
  return Math.round(Math.max(minSidebarWidth, Math.min(Math.max(minSidebarWidth, max), width)));
}

function loadAssetsPanelWidth(): number {
  const stored = Number(window.localStorage.getItem('atelier.assetsPanelWidth'));
  return clampAssetsPanelWidth(Number.isFinite(stored) && stored > 0 ? stored : defaultAssetsPanelWidth);
}

function clampAssetsPanelWidth(width: number, max = maxAssetsPanelWidth): number {
  return Math.round(Math.max(minAssetsPanelWidth, Math.min(Math.max(minAssetsPanelWidth, max), width)));
}

export {
  defaultSidebarWidth,
  minSidebarWidth,
  maxSidebarWidth,
  defaultAssetsPanelWidth,
  minAssetsPanelWidth,
  maxAssetsPanelWidth,
  compactHistoryLimit,
  expandedHistoryBatchSize,
  loadSidebarWidth,
  clampSidebarWidth,
  loadAssetsPanelWidth,
  clampAssetsPanelWidth,
};
