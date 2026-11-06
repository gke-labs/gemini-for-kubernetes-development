import React from 'react';

const GiB = 1024 ** 3;

// diskLevel is how full a disk is, for its colour: ok below 75%, low from
// 75%, full from 90% (a task may fail for space).
export function diskLevel(disk) {
  if (!disk || !disk.size) return null;
  const pct = (disk.used / disk.size) * 100;
  return pct >= 90 ? 'full' : pct >= 75 ? 'low' : 'ok';
}

const levelColor = { ok: '#22863a', low: '#b08800', full: '#d73a49' };

// DiskBar is a sandbox's workspace disk: used of size and free, as a bar
// coloured by diskLevel. Measured now (live) when the sandbox is up, else
// as the overseer last recorded it, with when. Nothing when not known.
export default function DiskBar({ disk }) {
  const level = diskLevel(disk);
  if (!level) return null;
  const pct = Math.min(100, Math.round((disk.used / disk.size) * 100));
  const gb = b => (b / GiB).toFixed(1);
  const when = disk.live ? '' : disk.at ? `as of ${new Date(disk.at).toLocaleString()}` : 'last recorded';
  return (
    <div data-testid="disk-bar" data-level={level} style={{ marginTop: '8px', fontSize: 'small' }}
      title={`Workspace disk: ${gb(disk.used)} GB used of ${gb(disk.size)} GB, ${gb(disk.free)} GB free${when ? ` (${when})` : ''}`}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: '6px', color: 'var(--text-secondary)' }}>
        <span>Disk</span>
        <span style={{ color: level === 'ok' ? 'var(--text-primary)' : levelColor[level], fontWeight: level === 'ok' ? 400 : 600 }}>
          {gb(disk.used)} / {gb(disk.size)} GB ({pct}%)
        </span>
        <span>· {gb(disk.free)} GB free</span>
        {when && <span style={{ fontStyle: 'italic' }}>· {when}</span>}
      </div>
      <div style={{ height: '6px', borderRadius: '3px', background: 'var(--border-color)', marginTop: '3px', overflow: 'hidden' }}>
        <div style={{ width: `${pct}%`, height: '100%', background: levelColor[level] }} />
      </div>
    </div>
  );
}
