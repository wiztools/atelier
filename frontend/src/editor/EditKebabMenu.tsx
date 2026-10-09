import {useEffect, useLayoutEffect, useRef, useState} from 'react';
import {createPortal} from 'react-dom';

// EditKebabMenu is the per-edit ⋮ menu in an Edits panel row: the trigger
// button plus its Delete dropdown, portaled to document.body and
// fixed-positioned at the trigger's viewport rect. The portal is the point —
// the edits list scrolls (overflow-y: auto), so an absolutely-positioned menu
// inside it is clipped to the scroll pane; the same reasoning as the sidebar's
// AnchoredMenu in App.tsx, whose placement and dismissal behavior this mirrors
// (flip above when the window edge leaves no room below; close on outside
// press, Escape, scroll, resize). Unlike the sidebar variant it owns its open
// state — rows are independent, so there is no cross-menu coordination to
// lift — and it renders the single Delete item itself: the item closes the
// menu from its own click handler, because a capture-phase auto-close on the
// container unmounts the portal before the item's handler can run (a click on
// Delete would close the menu and delete nothing).
//
// Deleting an edit is a hard delete (the result leaves the disk), so the item
// is the sidebar's two-step confirm: "Delete…" only arms, and the second
// click — an explicit question naming what goes — performs it. Every close
// path disarms, so a pending confirmation never outlives its menu.
export function EditKebabMenu(props: {
  label: string;
  disabled?: boolean;
  deleteInProgress?: boolean;
  onDelete: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [armed, setArmed] = useState(false);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const menuRef = useRef<HTMLDivElement | null>(null);
  // Viewport coordinates for the open menu; null during the hidden
  // pre-measure render.
  const [placement, setPlacement] = useState<{top?: number; bottom?: number; right: number} | null>(null);

  // Every close disarms — a second-step click must never linger into a
  // freshly opened menu (the sidebar's toggleContainerMenu/closeContainerMenu
  // rule).
  function closeMenu() {
    setOpen(false);
    setArmed(false);
  }

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
      right: window.innerWidth - rect.right,
      ...(openDownward
        ? {top: Math.round(rect.bottom) + 4}
        : {bottom: Math.round(window.innerHeight - rect.top) + 4}),
    });
  }, [open, armed]);

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
      closeMenu();
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        closeMenu();
      }
    };
    // Scroll (captured, so nested panes count) or resize detaches a fixed
    // menu from its anchor — closing is the only correct response. Scrolling
    // INSIDE the menu itself is exempt.
    const onReflow = (event: Event) => {
      if (event.target instanceof Node && menuRef.current?.contains(event.target)) {
        return;
      }
      closeMenu();
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
        className="editor-op-kebab"
        aria-label={props.label}
        aria-haspopup="menu"
        aria-expanded={open}
        title="Edit options"
        disabled={props.disabled}
        onClick={() => {
          setArmed(false);
          setOpen((current) => !current);
        }}
        ref={triggerRef}
      >
        ⋮
      </button>
      {open ? createPortal(
        <div ref={menuRef} className="history-menu anchored" role="menu" style={placement ? {...placement} : {visibility: 'hidden'}}>
          {armed ? (
            <button
              type="button"
              role="menuitem"
              className="menu-danger"
              disabled={props.deleteInProgress}
              onClick={() => {
                // Close first, then act — both run to completion inside this
                // handler; scheduling the close before the delete keeps the
                // flush (and the portal unmount) from racing the action.
                closeMenu();
                props.onDelete();
              }}
            >
              {props.deleteInProgress ? 'Deleting…' : 'Delete this edit and its result?'}
            </button>
          ) : (
            <button
              type="button"
              role="menuitem"
              className="menu-danger"
              onClick={() => setArmed(true)}
            >
              Delete…
            </button>
          )}
        </div>,
        document.body,
      ) : null}
    </>
  );
}
