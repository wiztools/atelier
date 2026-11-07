import {useEffect, useLayoutEffect, useRef, useState} from 'react';
import {createPortal} from 'react-dom';

// AnchoredMenu is the sidebar's ⋮ menu: a trigger button plus its dropdown,
// portaled to document.body and fixed-positioned at the trigger's viewport
// rect. The portal is the point — the sidebar's lists scroll (overflow-y:
// auto), so an absolutely-positioned menu inside them is clipped to the
// scroll pane and demands scrolling to read. Fixed viewport coordinates also
// let the menu flip ABOVE the trigger when the window's bottom edge leaves no
// room below. It closes on outside press, Escape, scroll, or resize —
// anything that detaches it from its anchor.
function AnchoredMenu(props: {
  label: string;
  open: boolean;
  onToggle: () => void;
  onClose: () => void;
  children: React.ReactNode;
}) {
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const menuRef = useRef<HTMLDivElement | null>(null);
  // Viewport coordinates for the open menu; null during the hidden
  // pre-measure render.
  const [placement, setPlacement] = useState<{top?: number; bottom?: number; right: number} | null>(null);

  // Place the freshly rendered menu below the trigger, or above it when the
  // window can't fit it below. The menu paints hidden (visibility, not
  // display, so it still has layout to measure) and useLayoutEffect lands
  // the coordinates before anything becomes visible. children is a
  // dependency on purpose — content changes the height (Copy ID → ✓ Copied,
  // Export… → the sized plan) and the placement must follow.
  useLayoutEffect(() => {
    if (!props.open || !triggerRef.current || !menuRef.current) {
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
  }, [props.open, props.children]);

  useEffect(() => {
    if (!props.open) {
      return;
    }
    const onPointerDown = (event: MouseEvent) => {
      const target = event.target instanceof Node ? event.target : null;
      if ((triggerRef.current && triggerRef.current.contains(target))
        || (menuRef.current && menuRef.current.contains(target))) {
        return;
      }
      props.onClose();
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        props.onClose();
      }
    };
    // Scroll (captured, so nested panes count) or resize detaches a fixed
    // menu from its anchor — closing is the only correct response. Scrolling
    // INSIDE the menu itself is exempt.
    const onReflow = (event: Event) => {
      if (event.target instanceof Node && menuRef.current?.contains(event.target)) {
        return;
      }
      props.onClose();
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
  }, [props.open, props.onClose]);

  return (
    <>
      <button
        type="button"
        className="history-icon-button"
        aria-label={props.label}
        title="More"
        onClick={props.onToggle}
        ref={triggerRef}
      >
        ⋮
      </button>
      {props.open ? createPortal(
        <div
          ref={menuRef}
          className="history-menu anchored"
          style={placement ? {...placement} : {visibility: 'hidden'}}
        >
          {props.children}
        </div>,
        document.body,
      ) : null}
    </>
  );
}

export {AnchoredMenu};
