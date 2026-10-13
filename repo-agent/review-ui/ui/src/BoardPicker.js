import React, { useEffect, useRef, useState } from 'react';

// BoardPicker: the Work tab's board, a dropdown in place of a tab per
// board. Its button and every entry carry the board's need-you count as a
// red badge, so which board wants you shows without opening it. Type to
// filter, ↑/↓ and Enter to pick, Escape or a click outside to close.

function Badge({ n }) {
  if (!n) return null;
  return (
    <span style={{
      marginLeft: '6px', backgroundColor: '#d73a49', color: 'white',
      borderRadius: '9px', padding: '0 6px', fontSize: 'x-small',
    }}>{n}</span>
  );
}

export default function BoardPicker({ boards, active, onPick }) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const [cursor, setCursor] = useState(0);
  const ref = useRef(null);

  const entries = boards.map(b => ({ key: b.name, label: b.name, n: b.needsHuman || 0, title: b.repoURL }));
  const q = query.trim().toLowerCase();
  const shown = entries.filter(e => !q || e.label.toLowerCase().includes(q) || (e.title || '').toLowerCase().includes(q));
  const current = entries.find(e => e.key === active);

  useEffect(() => {
    if (!open) return undefined;
    const onDown = (e) => { if (ref.current && !ref.current.contains(e.target)) setOpen(false); };
    document.addEventListener('mousedown', onDown);
    return () => document.removeEventListener('mousedown', onDown);
  }, [open]);

  const pick = (key) => { setOpen(false); setQuery(''); onPick(key); };
  const onKey = (e) => {
    if (e.key === 'Escape') { e.preventDefault(); setOpen(false); }
    else if (e.key === 'ArrowDown') { e.preventDefault(); setCursor(c => Math.min(shown.length - 1, c + 1)); }
    else if (e.key === 'ArrowUp') { e.preventDefault(); setCursor(c => Math.max(0, c - 1)); }
    else if (e.key === 'Enter' && shown[cursor]) { e.preventDefault(); pick(shown[cursor].key); }
  };

  return (
    <div ref={ref} style={{ position: 'relative' }}>
      <button type="button" className="btn" aria-haspopup="listbox" aria-expanded={open} aria-label="Board"
        title={current ? current.title : 'Pick a board'}
        onClick={() => { setOpen(o => !o); setCursor(0); }}
        style={{ display: 'flex', alignItems: 'center', gap: '4px', padding: '4px 10px', fontWeight: 600 }}>
        ▣ {current ? current.label : 'Pick a board'}<Badge n={current && current.n} /> <span style={{ marginLeft: '4px' }}>▾</span>
      </button>
      {open && (
        <div role="listbox" aria-label="Boards" style={{
          position: 'absolute', left: 0, top: 'calc(100% + 4px)', zIndex: 960, minWidth: '240px',
          backgroundColor: 'var(--bg-card)', border: '1px solid var(--border-color)', borderRadius: '8px',
          boxShadow: '0 8px 24px rgba(0,0,0,0.25)', padding: '6px', textAlign: 'left',
        }}>
          <input type="search" autoFocus aria-label="Filter boards" placeholder="🔍 filter" value={query}
            onChange={e => { setQuery(e.target.value); setCursor(0); }} onKeyDown={onKey}
            style={{ width: '100%', boxSizing: 'border-box', marginBottom: '4px', fontSize: 'small' }} />
          {shown.map((e, i) => (
            <button key={e.key} type="button" role="option" aria-selected={e.key === active} title={e.title}
              onMouseEnter={() => setCursor(i)} onClick={() => pick(e.key)}
              style={{
                display: 'flex', alignItems: 'center', width: '100%', textAlign: 'left', padding: '6px 8px',
                border: 'none', borderRadius: '6px', cursor: 'pointer', font: 'inherit', fontSize: '14px',
                background: i === cursor ? 'var(--bg-secondary)' : 'none', color: 'var(--text-primary)',
              }}>
              <span style={{ width: '16px' }}>{e.key === active ? '✓' : ''}</span>
              <span style={{ flex: 1 }}>{e.label}</span>
              <Badge n={e.n} />
            </button>
          ))}
          {!shown.length && <div style={{ padding: '6px 8px', color: 'var(--text-secondary)', fontSize: 'small' }}>No board matches.</div>}
        </div>
      )}
    </div>
  );
}
