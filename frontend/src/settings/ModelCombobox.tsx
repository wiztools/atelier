// ModelCombobox is the typeahead combobox every model picker renders: a
// filter-as-you-type input over {value,label} options with an optional
// allowCustom mode for providers whose catalog is a discovery aid rather
// than a closed list.
import {useEffect, useRef, useState} from 'react';

function ModelCombobox({
  id,
  value,
  onChange,
  options,
  placeholder,
  ariaLabel,
  allowCustom = false,
}: {
  id: string;
  value: string;
  onChange: (value: string) => void;
  options: {value: string; label: string}[];
  placeholder?: string;
  ariaLabel?: string;
  // When true, text typed that matches no option is committed verbatim (on
  // Enter or click-away) instead of being discarded. Used for fal, whose
  // catalog is a discovery aid but where an arbitrary endpoint id is still
  // valid input.
  allowCustom?: boolean;
}) {
  // `filter` is null whenever the user isn't actively typing; in that state
  // the input shows the committed `value` prop DIRECTLY, so a provider switch
  // (which changes `value`) is reflected immediately with no sync-effect in
  // between. While focused/typing, `filter` holds the local search text.
  const [filter, setFilter] = useState<string | null>(null);
  const [open, setOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement | null>(null);

  // commitCustom persists free-typed text (only when allowCustom). It reads the
  // latest filter via a ref so the document-level listeners below don't need to
  // re-subscribe on every keystroke.
  const filterRef = useRef<string | null>(null);
  filterRef.current = filter;
  const commitCustom = () => {
    const typed = (filterRef.current ?? '').trim();
    if (allowCustom && typed && typed !== value) {
      onChange(typed);
    }
  };

  const close = (commit = false) => {
    if (commit) {
      commitCustom();
    }
    setFilter(null);
    setOpen(false);
  };

  useEffect(() => {
    if (!open) {
      return;
    }
    const onPointerDown = (event: MouseEvent) => {
      if (containerRef.current && event.target instanceof Node && containerRef.current.contains(event.target)) {
        return;
      }
      close(true);
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        close();
      }
    };
    document.addEventListener('mousedown', onPointerDown);
    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('mousedown', onPointerDown);
      document.removeEventListener('keydown', onKeyDown);
    };
  }, [open]);

  const normalizedFilter = (filter ?? '').trim().toLowerCase();
  const filtered = normalizedFilter
    ? options.filter((option) => option.label.toLowerCase().includes(normalizedFilter) || option.value.toLowerCase().includes(normalizedFilter))
    : options;

  const selectOption = (option: {value: string; label: string}) => {
    onChange(option.value);
    close();
  };

  const displayValue = filter !== null ? filter : value;

  return (
    <div className="model-combobox" ref={containerRef}>
      <input
        id={id}
        type="text"
        autoComplete="off"
        aria-label={ariaLabel}
        aria-expanded={open}
        role="combobox"
        placeholder={placeholder}
        value={displayValue}
        onFocus={() => {
          // Start a fresh filter over the full list; closing without a pick
          // restores the committed value.
          setFilter('');
          setOpen(true);
        }}
        onChange={(event) => {
          // Typing only updates the local filter text; the parent `model` is
          // committed solely via selectOption. Pushing every keystroke up to
          // the parent would churn the option list and trigger its
          // "snap to a valid model" effect, reverting the input mid-type.
          setFilter(event.target.value);
          setOpen(true);
        }}
        onKeyDown={(event) => {
          if (event.key !== 'Enter' || !open) {
            return;
          }
          event.preventDefault();
          if (filtered.length) {
            selectOption(filtered[0]);
          } else if (allowCustom) {
            close(true);
          }
        }}
      />
      {open && filtered.length ? (
        <ul className="model-combobox-list" role="listbox">
          {filtered.map((option) => (
            <li key={option.value}>
              <button
                type="button"
                className={option.value === value ? 'active' : undefined}
                role="option"
                aria-selected={option.value === value}
                onMouseDown={(event) => event.preventDefault()}
                onClick={() => selectOption(option)}
              >
                {option.label}
              </button>
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}

export {ModelCombobox};
