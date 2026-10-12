import React, { useState, useEffect, useCallback, useMemo } from 'react';
import Chip from './Chip';
import SessionSlideOver from './SessionSlideOver';
import SandboxCard from './SandboxCard';

// OverseerWork: the Overseer page's work view
// (docs/design/overseer-work-view.md). One row per issue (with the PR its
// fix opened), per PR, or per agent without either, from
// GET /api/overseers/<name>/work; a chip per run, which opens the board's
// session slide-over, and the row's sandboxes, which open the board's
// sandbox card, both on the overseer's routes (apiBase).

// SECTIONS are the feed's sections, in the page's order.
export const SECTIONS = [
  { key: 'attention', label: 'Needs attention' },
  { key: 'working', label: 'Working' },
  { key: 'queued', label: 'Queued' },
  { key: 'idle', label: 'Idle', collapsed: true },
  { key: 'agents', label: 'Agents' },
];

// STATUS is how a chip shows a run's status.
const STATUS = {
  running: { glyph: '●', color: '#b08800' },
  ready: { glyph: '◐', color: '#0366d6' },
  done: { glyph: '✓', color: '#22863a' },
  failed: { glyph: '✗', color: '#d73a49' },
  incoming: { glyph: '⧗', color: '#6a737d' },
  processing: { glyph: '⧗', color: '#b08800' },
};

const POD = {
  running: '#22863a', starting: '#b08800', paused: '#6a737d', evicted: '#d73a49', failed: '#d73a49',
};

const tint = (color) => `color-mix(in srgb, ${color} 12%, transparent)`;

export function ageOf(ts, now = Date.now()) {
  if (!ts) return '';
  const t = new Date(ts).getTime();
  if (Number.isNaN(t)) return '';
  const s = Math.max(0, Math.round((now - t) / 1000));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.round(s / 60)}m`;
  if (s < 86400) return `${Math.round(s / 3600)}h`;
  return `${Math.round(s / 86400)}d`;
}

// matchesWork is whether a row passes the filters: issues, PRs or both;
// running, paused, failed or all; and a search over its number, title,
// sandboxes and recipes.
export function matchesWork(item, { type = 'both', state = 'all', q = '' } = {}) {
  if (type === 'issue' && item.kind !== 'issue') return false;
  if (type === 'pr' && item.kind !== 'pr' && !item.pr) return false;
  const sandboxes = item.sandboxes || [];
  const sessions = item.sessions || [];
  if (state === 'running' && !sandboxes.some(s => s.pod === 'running') && !sessions.some(s => s.status === 'running')) return false;
  if (state === 'paused' && (!sandboxes.length || !sandboxes.every(s => s.pod === 'paused'))) return false;
  if (state === 'failed' && item.section !== 'attention') return false;
  const needle = q.trim().toLowerCase();
  if (!needle) return true;
  const hay = [
    item.key, String(item.number || ''), String(item.pr || ''), item.title || '', item.reason || '',
    ...sandboxes.map(s => s.name), ...sessions.map(s => s.recipe), ...(item.queued || []).map(q2 => q2.recipe),
  ].join(' ').toLowerCase();
  return hay.includes(needle);
}

function RowTitle({ item }) {
  if (item.kind === 'agent') {
    const sb = (item.sandboxes || [])[0];
    return <span style={{ fontWeight: 600 }}>⚙ {item.title || (sb && sb.name) || item.key}</span>;
  }
  const link = (href, text) => (href
    ? <a href={href} target="_blank" rel="noopener noreferrer" style={{ color: 'var(--text-link)', fontWeight: 600 }}>{text}</a>
    : <span style={{ fontWeight: 600 }}>{text}</span>);
  return (
    <span>
      {item.kind === 'pr' ? link(item.htmlUrl, `PR #${item.number}`) : link(item.htmlUrl, `#${item.number}`)}
      {item.pr ? <> <span style={{ color: 'var(--text-secondary)' }}>→</span> {link(item.prUrl, `PR #${item.pr}`)}</> : null}
      {item.workflow && <span style={{ marginLeft: '6px' }}><Chip text="workflow" color="#6f42c1" bg={tint('#6f42c1')} /></span>}
      {item.title && <span style={{ marginLeft: '8px' }}>{item.title}</span>}
    </span>
  );
}

function RunChips({ item, onOpenSession, onOpenSandbox }) {
  return (
    <span style={{ display: 'inline-flex', flexWrap: 'wrap', gap: '4px' }}>
      {(item.sessions || []).map(s => {
        const st = STATUS[s.status] || STATUS.done;
        const title = `${s.recipe}: ${s.status}${s.state ? ` (${s.state})` : ''}${s.engine ? ` · ${s.engine}` : ''} · ${s.sandbox}`
          + (s.task ? ' — open the session' : ' — no session to open (a classic task): open the sandbox');
        return (
          <button key={`${s.sandbox}/${s.run || s.recipe}`} type="button" title={title}
            onClick={() => (s.task ? onOpenSession({ sandbox: s.sandbox, task: s.task }) : onOpenSandbox(s.sandbox))}
            style={{ border: 'none', background: 'none', padding: 0, cursor: 'pointer' }}>
            <Chip text={`${s.recipe} ${st.glyph}`} color={st.color} bg={tint(st.color)} />
          </button>
        );
      })}
      {(item.queued || []).map(q => {
        const st = STATUS[q.state] || STATUS.incoming;
        return (
          <Chip key={q.fileName} text={`${q.recipe || q.type} ${st.glyph}${q.rank ? ` #${q.rank}` : ''}`}
            color={st.color} bg={tint(st.color)}
            title={`${q.type}: ${q.state}${q.priority ? ` · ${q.priority}` : ''}${q.assignee ? ` · ${q.assignee}` : ''}`} />
        );
      })}
    </span>
  );
}

export function WorkRow({ item, onOpenSession, onOpenSandbox, now }) {
  const users = [...new Set((item.sandboxes || []).map(s => s.user).filter(Boolean))];
  return (
    <div data-testid={`row-${item.key}`} style={{ padding: '8px 4px', borderTop: '1px solid var(--border-color)', textAlign: 'left' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '10px', flexWrap: 'wrap' }}>
        <RowTitle item={item} />
        <span style={{ marginLeft: 'auto' }}>
          <RunChips item={item} onOpenSession={onOpenSession} onOpenSandbox={onOpenSandbox} />
        </span>
      </div>
      <div style={{ display: 'flex', alignItems: 'center', gap: '10px', marginTop: '4px', fontSize: 'small', color: 'var(--text-secondary)', flexWrap: 'wrap' }}>
        {users.map(u => <span key={u}>@{u}</span>)}
        {item.reason && <span style={{ color: '#d73a49' }}>{item.reason}</span>}
        {item.updatedAt && <span title={item.updatedAt}>{ageOf(item.updatedAt, now)}</span>}
        <span style={{ marginLeft: 'auto', display: 'inline-flex', gap: '6px', flexWrap: 'wrap' }}>
          {(item.sandboxes || []).map(s => (
            <button key={s.name} type="button" className="btn btn-sm" onClick={() => onOpenSandbox(s.name)}
              title={`${s.name}: ${s.pod}${s.taskType ? ` · last ${s.taskType} ${s.taskState || ''}` : ''} — open the sandbox`}
              style={{ fontSize: 'x-small', display: 'inline-flex', alignItems: 'center', gap: '4px' }}>
              <span style={{ color: POD[s.pod] || 'var(--text-secondary)' }}>▣</span>{s.name}
            </button>
          ))}
        </span>
      </div>
    </div>
  );
}

const OverseerWork = ({ overseer }) => {
  const [work, setWork] = useState(null);
  const [error, setError] = useState('');
  const [type, setType] = useState('both');
  const [state, setState] = useState('all');
  const [q, setQ] = useState('');
  const [open, setOpen] = useState({});
  const [session, setSession] = useState(null);
  const [cardSandbox, setCardSandbox] = useState(null);
  const apiBase = `/api/overseers/${encodeURIComponent(overseer)}`;

  const load = useCallback(() => {
    fetch(`${apiBase}/work`)
      .then(res => (res.ok ? res.json() : res.text().then(t => Promise.reject(new Error(t || `HTTP ${res.status}`)))))
      .then(data => { setWork(data); setError(''); })
      .catch(err => setError(`${err.message || err}`));
  }, [apiBase]);
  useEffect(() => {
    setWork(null);
    load();
    const t = setInterval(load, 15000);
    return () => clearInterval(t);
  }, [load]);

  const items = useMemo(() => ((work && work.items) || []).filter(it => matchesWork(it, { type, state, q })), [work, type, state, q]);
  const now = Date.now();
  const toggle = (key, def) => setOpen(o => ({ ...o, [key]: !(key in o ? o[key] : def) }));

  return (
    <div style={{ textAlign: 'left' }}>
      <div style={{ display: 'flex', gap: '8px', alignItems: 'center', flexWrap: 'wrap', marginBottom: '10px' }}>
        {[['both', 'Both'], ['issue', 'Issues'], ['pr', 'PRs']].map(([k, label]) => (
          <button key={k} type="button" className={`btn btn-sm ${type === k ? 'btn-primary' : 'btn-secondary'}`} onClick={() => setType(k)}>{label}</button>
        ))}
        <span style={{ width: '10px' }} />
        {[['all', 'All'], ['running', 'Running'], ['paused', 'Paused'], ['failed', 'Failed']].map(([k, label]) => (
          <button key={k} type="button" className={`btn btn-sm ${state === k ? 'btn-primary' : 'btn-secondary'}`} onClick={() => setState(k)}>{label}</button>
        ))}
        <input type="search" placeholder="Search number, title, sandbox, recipe…" value={q} onChange={e => setQ(e.target.value)}
          aria-label="Search work" style={{ marginLeft: 'auto', minWidth: '260px', padding: '4px 8px' }} />
        <button type="button" className="btn btn-sm" onClick={load} title="Refresh">↻</button>
      </div>
      {error && <div style={{ color: 'var(--danger, #d33)', fontSize: 'small', marginBottom: '8px' }}>Work feed: {error}</div>}
      {work && work.queueError && (
        <div style={{ color: 'var(--text-secondary)', fontSize: 'small', marginBottom: '8px' }}>Queue not shown: {work.queueError}</div>
      )}
      {!work && !error && <div style={{ color: 'var(--text-secondary)', fontStyle: 'italic' }}>Loading…</div>}
      {work && SECTIONS.map(sec => {
        const rows = items.filter(it => it.section === sec.key);
        if (!rows.length) return null;
        const isOpen = sec.key in open ? open[sec.key] : !sec.collapsed;
        return (
          <section key={sec.key} style={{ marginBottom: '14px' }} aria-label={sec.label}>
            <button type="button" onClick={() => toggle(sec.key, !sec.collapsed)}
              style={{ border: 'none', background: 'var(--bg-secondary)', width: '100%', textAlign: 'left', padding: '6px 8px', borderRadius: '4px', cursor: 'pointer', fontWeight: 700, letterSpacing: '0.04em', color: 'var(--text-primary)' }}>
              {isOpen ? '▾' : '▸'} {sec.label.toUpperCase()} ({rows.length})
            </button>
            {isOpen && rows.map(it => (
              <WorkRow key={it.key} item={it} now={now} onOpenSession={setSession} onOpenSandbox={setCardSandbox} />
            ))}
          </section>
        );
      })}
      {work && !items.length && <div style={{ color: 'var(--text-secondary)' }}>No work matches.</div>}
      {session && <SessionSlideOver session={session} onClose={() => setSession(null)} apiBase={apiBase} />}
      {cardSandbox && (
        <SandboxCard name={cardSandbox} namespace={`overseer-${overseer}`} onClose={() => setCardSandbox(null)} apiBase={apiBase} />
      )}
    </div>
  );
};

export default OverseerWork;
