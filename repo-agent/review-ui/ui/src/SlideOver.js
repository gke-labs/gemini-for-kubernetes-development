import React, { useEffect, useState } from 'react';

// SlideOver is the right-hand sheet the sandbox card opens in, as a
// component the rest of the app can reach for.
//
// The point of it over a full-page swap is that the page you came from
// stays on screen behind it. A research conversation is something you
// open, read, and come back from — often to open the next one — and a
// list that disappears the moment you pick from it makes that a
// navigation rather than a glance.
//
// The children are laid out as a flex column with a definite height, so
// a child that wants to own its own scrolling (a transcript pinned to
// the bottom, a terminal) can do that by filling the space instead of
// growing the sheet.
export function SlideOver({
  onClose,
  label,
  defaultWidth = '70%',
  expandedWidth = '95%',
  children,
}) {
  const [expanded, setExpanded] = useState(false);

  useEffect(() => {
    const onKey = (e) => {
      if (e.key !== 'Escape') return;
      // A child that already meant something by Escape wins. Renaming a
      // conversation is cancelled with it, and closing the whole sheet
      // out from under someone who only wanted to abandon an edit is
      // the kind of thing that loses a half-typed prompt.
      if (e.defaultPrevented) return;
      onClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  return (
    <>
      <div onClick={onClose} aria-hidden="true" style={{
        position: 'fixed', inset: 0, backgroundColor: 'rgba(0,0,0,0.35)', zIndex: 900,
      }} />
      <div role="dialog" aria-modal="true" aria-label={label} style={{
        position: 'fixed', top: 0, right: 0, height: '100%',
        width: expanded ? expandedWidth : defaultWidth,
        backgroundColor: 'var(--bg-card)', borderLeft: '1px solid var(--border-color)',
        boxShadow: '-6px 0 24px rgba(0,0,0,0.25)', zIndex: 901,
        display: 'flex', flexDirection: 'column', textAlign: 'left',
      }}>
        <div style={{
          display: 'flex', justifyContent: 'flex-end', alignItems: 'center',
          gap: '6px', padding: '8px 14px 0', flex: '0 0 auto',
        }}>
          <button className="btn btn-sm" aria-label={expanded ? 'Shrink the panel' : 'Widen the panel'}
            onClick={() => setExpanded(v => !v)}
            title={expanded ? 'Shrink the panel' : 'Widen the panel'}>
            {expanded ? '⇥' : '⛶'}
          </button>
          <button className="btn btn-sm" aria-label="Close" onClick={onClose} title="Close (Esc)">✕</button>
          <kbd style={{
            fontSize: 'x-small', color: 'var(--text-secondary)',
            border: '1px solid var(--border-color)', borderRadius: '3px', padding: '0 4px',
          }}>esc</kbd>
        </div>
        <div style={{ flex: '1 1 auto', minHeight: 0, display: 'flex', flexDirection: 'column' }}>
          {children}
        </div>
      </div>
    </>
  );
}

export default SlideOver;
