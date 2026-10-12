import React from 'react';

// Chip: a small rounded label, the board's and the Overseer page's.
export default function Chip({ text, color, bg, title, children }) {
  if (!text) return null;
  return (
    <span title={title} style={{
      color, backgroundColor: bg,
      padding: '2px 8px', borderRadius: '10px',
      fontSize: 'small', whiteSpace: 'nowrap',
    }}>{text}{children}</span>
  );
}

