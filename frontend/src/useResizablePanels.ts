import {useEffect, useState} from 'react';
import {
  maxSidebarWidth,
  minAssetsPanelWidth,
  maxAssetsPanelWidth,
  loadSidebarWidth,
  clampSidebarWidth,
  loadAssetsPanelWidth,
  clampAssetsPanelWidth,
} from './uiLayout';

// useResizablePanels owns the two draggable panel widths, the sidebar's and the
// assets panel's: each width (restored from and persisted to localStorage), the
// in-drag flag its divider sets, and the window listeners that follow a drag.
// shellRef anchors the drag math to the app shell's edges; dragging the assets
// panel below its minimum width snaps it closed through setAssetsPanelOpen.
function useResizablePanels({shellRef, setAssetsPanelOpen}: {
  shellRef: {current: HTMLElement | null};
  setAssetsPanelOpen: (open: boolean) => void;
}) {
  const [assetsWidth, setAssetsWidth] = useState(loadAssetsPanelWidth);
  const [resizingAssets, setResizingAssets] = useState(false);

  const [sidebarWidth, setSidebarWidth] = useState(loadSidebarWidth);
  const [resizingSidebar, setResizingSidebar] = useState(false);

  useEffect(() => {
    if (!resizingSidebar) {
      return;
    }
    const onMouseMove = (event: MouseEvent) => {
      const left = shellRef.current?.getBoundingClientRect().left ?? 0;
      const max = Math.min(maxSidebarWidth, window.innerWidth - 420);
      setSidebarWidth(clampSidebarWidth(event.clientX - left, max));
    };
    const onMouseUp = () => setResizingSidebar(false);
    window.addEventListener('mousemove', onMouseMove);
    window.addEventListener('mouseup', onMouseUp);
    return () => {
      window.removeEventListener('mousemove', onMouseMove);
      window.removeEventListener('mouseup', onMouseUp);
    };
  }, [resizingSidebar]);

  useEffect(() => {
    if (!resizingAssets) {
      return;
    }
    const onMouseMove = (event: MouseEvent) => {
      const right = shellRef.current?.getBoundingClientRect().right ?? window.innerWidth;
      const width = right - event.clientX;
      // Snap to close: dragging below the minimum renderable width collapses
      // the panel (the toolbar toggle follows) instead of clamping to an
      // unusable sliver. The drag session stays alive across the snap — the
      // listeners ride on window, so they survive the panel unmounting — and
      // dragging back past the threshold reopens it; the close only sticks
      // when the pointer is released while collapsed. The stored width never
      // drops below the minimum, so reopening restores the last good width.
      if (width < minAssetsPanelWidth) {
        setAssetsPanelOpen(false);
        return;
      }
      setAssetsPanelOpen(true);
      const max = Math.min(maxAssetsPanelWidth, window.innerWidth - 420);
      setAssetsWidth(clampAssetsPanelWidth(width, max));
    };
    const onMouseUp = () => setResizingAssets(false);
    window.addEventListener('mousemove', onMouseMove);
    window.addEventListener('mouseup', onMouseUp);
    return () => {
      window.removeEventListener('mousemove', onMouseMove);
      window.removeEventListener('mouseup', onMouseUp);
    };
  }, [resizingAssets]);

  useEffect(() => {
    window.localStorage.setItem('atelier.sidebarWidth', String(sidebarWidth));
  }, [sidebarWidth]);

  useEffect(() => {
    window.localStorage.setItem('atelier.assetsPanelWidth', String(assetsWidth));
  }, [assetsWidth]);

  return {
    assetsWidth,
    resizingAssets,
    resizingSidebar,
    setResizingAssets,
    setResizingSidebar,
    sidebarWidth,
  };
}

export {useResizablePanels};
