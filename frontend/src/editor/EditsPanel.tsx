import {ReactNode, Ref, RefObject, useEffect, useRef, useState} from 'react';

// EditsPanel is the editor shell's bottom strip: the "Edits" footer shared by
// the image and video editors. It owns the panel's height (persisted in
// localStorage, one key across editors so the preference carries), the divider
// drag (up grows the panel; dropping near the floor snaps it shut, leaving the
// slim restore bar), keyboard resizing on the separator, and the
// scroll-contained list pane. It does NOT own the rows — the thumb element,
// the action buttons, and the compare expansion are media-specific and arrive
// as children.
//
// bodyReservePx is the vertical floor of everything above the panel for THIS
// editor (header + stage minimum + tool rows + paddings). The height clamp
// trades panel height against that floor so the footer never pushes the stage
// below its minimum; a slightly generous value only costs growth headroom,
// a tight one clips the stage. The image editor's floor is the default; the
// video editor passes its own (its timeline row is taller than the
// brush-tools row).

const minEditsPanelHeight = 120;
const collapseSnapSlack = 24;
const defaultEditsPanelHeight = 260;
const defaultBodyReservePx = 430;
const heightStorageKey = 'atelier.editorResultsHeight';

function clampEditsPanelHeight(height: number, bodyReservePx: number): number {
  const max = Math.max(minEditsPanelHeight, window.innerHeight - bodyReservePx);
  return Math.round(Math.max(minEditsPanelHeight, Math.min(height, max)));
}

function loadEditsPanelHeight(bodyReservePx: number): number {
  const stored = Number(window.localStorage.getItem(heightStorageKey));
  if (Number.isFinite(stored) && stored >= minEditsPanelHeight) {
    return clampEditsPanelHeight(stored, bodyReservePx);
  }
  return defaultEditsPanelHeight;
}

export function EditsPanel(props: {
  count: number;
  emptyText: string;
  adoptError?: ReactNode;
  scrollRef?: RefObject<HTMLDivElement | null>;
  bodyReservePx?: number;
  children: ReactNode;
}) {
  const bodyReservePx = props.bodyReservePx ?? defaultBodyReservePx;
  const [height, setHeight] = useState(() => loadEditsPanelHeight(bodyReservePx));
  const [collapsed, setCollapsed] = useState(false);
  const [resizing, setResizing] = useState(false);
  const dragRef = useRef<{startY: number; startHeight: number} | null>(null);

  // The divider sits between the canvas body and the footer, so moving the
  // mouse UP grows the panel. While the drag is live the whole document gets
  // the row-resize cursor and loses text selection — the mouse travels over
  // the stage, not just the divider.
  useEffect(() => {
    if (!resizing) {
      return;
    }
    document.body.classList.add('editor-results-resizing');
    const onMove = (event: MouseEvent) => {
      const drag = dragRef.current;
      if (!drag) {
        return;
      }
      const next = clampEditsPanelHeight(drag.startHeight + (drag.startY - event.clientY), bodyReservePx);
      if (next <= minEditsPanelHeight + collapseSnapSlack) {
        setCollapsed(true);
        setResizing(false);
        return;
      }
      setHeight(next);
    };
    const onUp = () => setResizing(false);
    window.addEventListener('mousemove', onMove);
    window.addEventListener('mouseup', onUp);
    return () => {
      document.body.classList.remove('editor-results-resizing');
      window.removeEventListener('mousemove', onMove);
      window.removeEventListener('mouseup', onUp);
    };
  }, [resizing, bodyReservePx]);

  useEffect(() => {
    window.localStorage.setItem(heightStorageKey, String(height));
  }, [height]);

  return (
    <>
      {collapsed ? (
        <button
          type="button"
          className="editor-results-restore"
          onClick={() => setCollapsed(false)}
          title="Show the edits panel"
        >
          ▲ Edits{props.count ? ` (${props.count})` : ''}
        </button>
      ) : (
        <>
          <div
            className={`editor-results-resizer${resizing ? ' active' : ''}`}
            role="separator"
            aria-orientation="horizontal"
            aria-label="Resize edits panel"
            tabIndex={0}
            onMouseDown={(event) => {
              event.preventDefault();
              dragRef.current = {startY: event.clientY, startHeight: height};
              setResizing(true);
            }}
            onDoubleClick={() => setCollapsed(true)}
            onKeyDown={(event) => {
              if (event.key === 'ArrowUp') {
                event.preventDefault();
                setCollapsed(false);
                setHeight(clampEditsPanelHeight(height + 32, bodyReservePx));
              }
              if (event.key === 'ArrowDown') {
                event.preventDefault();
                const next = clampEditsPanelHeight(height - 32, bodyReservePx);
                if (next <= minEditsPanelHeight) {
                  setCollapsed(true);
                } else {
                  setHeight(next);
                }
              }
            }}
          />
          <footer className="editor-results" style={{height}}>
            <div className="editor-results-header">
              <h4>Edits</h4>
              {props.adoptError}
            </div>
            <div className="editor-results-scroll" ref={(props.scrollRef ?? undefined) as Ref<HTMLDivElement> | undefined}>
              {props.count === 0 ? <p className="editor-results-empty">{props.emptyText}</p> : props.children}
            </div>
          </footer>
        </>
      )}
    </>
  );
}
