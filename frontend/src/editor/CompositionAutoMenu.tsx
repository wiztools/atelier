import {useEffect, useLayoutEffect, useRef, useState} from 'react';
import {createPortal} from 'react-dom';

// CompositionAutoMenu is the composer's auto-update control: a gear trigger
// beside Render preview plus its one-item dropdown. It replaces a labeled
// checkbox on the render-controls row — a setting that changes rarely was
// spending a whole label's width there — so the gear carries the state
// instead: it picks up the accent color while auto-update is on, and the
// menu item names the action ("Enable auto-update" / "Disable auto-update")
// rather than the state.
//
// The menu portals to document.body and fixed-positions at the trigger's
// viewport rect — the same reasoning as EditKebabMenu (editor/EditKebabMenu.tsx):
// the editor body scrolls, so an absolutely-positioned menu is clipped to it;
// placement and dismissal mirror the kebab too (flip above when the window
// edge leaves no room below; close on outside press, Escape, scroll, resize).
// The item closes the menu from its own click handler before toggling,
// because a capture-phase auto-close would unmount the portal before the
// handler ran (the EditKebabMenu rule).
export function CompositionAutoMenu(props: {
  enabled: boolean;
  onToggle: (enabled: boolean) => void;
}) {
  const [open, setOpen] = useState(false);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const menuRef = useRef<HTMLDivElement | null>(null);
  // Viewport coordinates for the open menu; null during the hidden
  // pre-measure render.
  const [placement, setPlacement] = useState<{top?: number; bottom?: number; left: number} | null>(null);

  useLayoutEffect(() => {
    if (!open || !triggerRef.current || !menuRef.current) {
      setPlacement(null);
      return;
    }
    const rect = triggerRef.current.getBoundingClientRect();
    const height = menuRef.current.offsetHeight;
    const fitsBelow = window.innerHeight - rect.bottom >= height + 8;
    const openDownward = fitsBelow || rect.bottom <= window.innerHeight / 2;
    setPlacement({
      left: Math.round(rect.left),
      ...(openDownward
        ? {top: Math.round(rect.bottom) + 4}
        : {bottom: Math.round(window.innerHeight - rect.top) + 4}),
    });
  }, [open]);

  useEffect(() => {
    if (!open) {
      return;
    }
    const onPointerDown = (event: MouseEvent) => {
      const target = event.target instanceof Node ? event.target : null;
      if ((triggerRef.current && triggerRef.current.contains(target))
        || (menuRef.current && menuRef.current.contains(target))) {
        return;
      }
      setOpen(false);
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setOpen(false);
      }
    };
    // Scroll (captured, so nested panes count) or resize detaches a fixed
    // menu from its anchor — closing is the only correct response. Scrolling
    // INSIDE the menu itself is exempt.
    const onReflow = (event: Event) => {
      if (event.target instanceof Node && menuRef.current?.contains(event.target)) {
        return;
      }
      setOpen(false);
    };
    document.addEventListener('mousedown', onPointerDown);
    document.addEventListener('keydown', onKeyDown);
    window.addEventListener('resize', onReflow);
    window.addEventListener('scroll', onReflow, true);
    return () => {
      document.removeEventListener('mousedown', onPointerDown);
      document.removeEventListener('keydown', onKeyDown);
      window.removeEventListener('resize', onReflow);
      window.removeEventListener('scroll', onReflow, true);
    };
  }, [open]);

  return (
    <>
      <button
        type="button"
        className={`composition-auto-gear${props.enabled ? ' on' : ''}`}
        aria-label="Auto-update options"
        aria-haspopup="menu"
        aria-expanded={open}
        title={`Auto-update is ${props.enabled ? 'on' : 'off'} — re-render the preview on its own about a second after each change settles — never mid-drag; a newer edit restarts a render still running. Each preview re-encodes the whole timeline (or the From/To range) at full output size, so a long sequence keeps ffmpeg busy while you edit.`}
        onClick={() => setOpen((current) => !current)}
        ref={triggerRef}
      >
        ⚙&#xFE0E;
      </button>
      {open ? createPortal(
        <div ref={menuRef} className="history-menu anchored composition-auto-menu" role="menu" style={placement ? {...placement} : {visibility: 'hidden'}}>
          <button
            type="button"
            role="menuitemcheckbox"
            aria-checked={props.enabled}
            onClick={() => {
              // Close first, then act — both run to completion inside this
              // handler; scheduling the close before the toggle keeps the
              // flush (and the portal unmount) from racing the action.
              setOpen(false);
              props.onToggle(!props.enabled);
            }}
          >
            {props.enabled ? 'Disable auto-update' : 'Enable auto-update'}
          </button>
        </div>,
        document.body,
      ) : null}
    </>
  );
}
