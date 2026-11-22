import React, { useState } from 'react';

// DataTree shows a parsed JSON or YAML value as a tree: objects and arrays
// fold and unfold (the first levels open), keys and value types in their
// own colours. TaskFiles uses it for a task's .json and .yaml files.

const colours = {
  key: 'var(--tree-key, #1a73e8)',
  string: 'var(--tree-string, #188038)',
  number: 'var(--tree-number, #c5221f)',
  literal: 'var(--tree-literal, #9334e6)',
  muted: 'var(--text-secondary, #888)',
};

// summary is a folded object's or array's one-line description.
export function summary(value) {
  if (Array.isArray(value)) return `[${value.length} ${value.length === 1 ? 'item' : 'items'}]`;
  const n = Object.keys(value).length;
  return `{${n} ${n === 1 ? 'key' : 'keys'}}`;
}

function Scalar({ value }) {
  if (value === null || value === undefined) return <span style={{ color: colours.literal }}>null</span>;
  if (typeof value === 'string') return <span style={{ color: colours.string, whiteSpace: 'pre-wrap' }}>"{value}"</span>;
  if (typeof value === 'number') return <span style={{ color: colours.number }}>{String(value)}</span>;
  if (typeof value === 'boolean') return <span style={{ color: colours.literal }}>{String(value)}</span>;
  if (value instanceof Date) return <span style={{ color: colours.string }}>{value.toISOString()}</span>;
  return <span>{String(value)}</span>;
}

function Node({ name, value, depth, openDepth }) {
  const branch = value !== null && typeof value === 'object' && !(value instanceof Date);
  const [open, setOpen] = useState(depth < openDepth);
  const label = name === undefined ? null : <span style={{ color: colours.key }}>{name}: </span>;
  if (!branch) {
    return <div style={{ paddingLeft: depth ? 16 : 0 }}>{label}<Scalar value={value} /></div>;
  }
  const entries = Array.isArray(value) ? value.map((v, i) => [String(i), v]) : Object.entries(value);
  return (
    <div style={{ paddingLeft: depth ? 16 : 0 }}>
      <button type="button" onClick={() => setOpen(!open)} aria-expanded={open}
        style={{ border: 0, background: 'transparent', color: 'inherit', cursor: 'pointer', padding: 0, font: 'inherit' }}>
        <span style={{ display: 'inline-block', width: 12 }}>{open ? '▾' : '▸'}</span>
        {label}
        {!open && <span style={{ color: colours.muted }}>{summary(value)}</span>}
        {open && entries.length === 0 && <span style={{ color: colours.muted }}>{Array.isArray(value) ? '[]' : '{}'}</span>}
      </button>
      {open && entries.map(([k, v]) => <Node key={k} name={k} value={v} depth={depth + 1} openDepth={openDepth} />)}
    </div>
  );
}

export default function DataTree({ value, openDepth = 2 }) {
  return (
    <div className="data-tree" style={{ fontFamily: 'monospace', fontSize: 12, lineHeight: 1.6, textAlign: 'left' }}>
      <Node value={value} depth={0} openDepth={openDepth} />
    </div>
  );
}
