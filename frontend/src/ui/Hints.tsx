// Hover hints: the circled-"i" InfoHint and its airplane-mode sibling,
// AirplaneHint — CSS-anchored popovers that also open on keyboard focus.

// InfoHint is a small circled "i" whose informational text pops open on hover
// — and on keyboard focus, so hover-only doesn't hide it from keyboard users.
// The popover is CSS-anchored to the icon (no portal): it's narrow, opens
// rightward/downward from a label row, and closes on pointer-leave, so it
// never fights the scroll/resize detachment the anchored menus handle.
function InfoHint(props: {label: string; text: string}) {
  return (
    <span className="info-hint" tabIndex={0} role="note" aria-label={props.label}>
      <span className="info-hint-icon" aria-hidden="true">i</span>
      <span className="info-hint-pop" role="tooltip">{props.text}</span>
    </span>
  );
}

// AirplaneHint is the airplane-mode sibling of InfoHint: the same hover
// mechanics, an airplane glyph instead of the "i", carrying the standing
// unavailability message so a disabled cloud section stays scannable — the
// copy rides the hover instead of standing on the screen.
function AirplaneHint(props: {label: string; text: string}) {
  return (
    <span className="info-hint airplane-hint" tabIndex={0} role="note" aria-label={props.label}>
      <span className="info-hint-icon" aria-hidden="true">
        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
          <path d="M17.8 19.2 16 11l3.5-3.5C21 6 21.5 4 21 3c-1-.5-3 0-4.5 1.5L13 8 4.8 6.2c-.5-.1-.9.1-1.1.5l-.3.5c-.2.5-.1 1 .3 1.3L9 12l-2 3H4l-1 1 3 2 2 3 1-1v-3l3-2 3.5 5.3c.3.4.8.5 1.3.3l.5-.2c.4-.3.6-.7.5-1.2z" />
        </svg>
      </span>
      <span className="info-hint-pop" role="tooltip">{props.text}</span>
    </span>
  );
}

export {InfoHint, AirplaneHint};
