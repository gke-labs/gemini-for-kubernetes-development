import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { ResearchConversation, GrowingTextarea, repoFromURL } from './Research';
import SandboxCard from './SandboxCard';
import { EngineIcon, ageOf } from './WorkBits';

// Sessions: every agent conversation in the member's namespace, whatever
// started it — a Work row's button, the box on this page, the factory CLI
// (docs/design/sessions-view.md). Work starts from GitHub; this starts
// from the agents, sorted by what each is doing.
//
// A split page: the list is the whole width until a session opens, then
// narrows to a column and the session fills the rest. The column can be
// dragged wider or collapsed to a strip of state dots.
//
// One flat list, no sections: a row's dot says its state by colour, and
// the rows come in the order the colours do.

// STATE is a row's dot: its colour, its hover, and where the row sorts.
// Red needs you (a failure does too), blue working, green idle with its
// engine up, grey paused or done.
const RED = '#d73a49';
const BLUE = '#0969da';
const GREEN = '#22863a';
const GREY = '#8b949e';
export const STATE = {
  'needs-you': { color: RED, label: 'Needs you', order: 0 },
  failed: { color: RED, label: 'Failed', order: 0 },
  working: { color: BLUE, label: 'Working', order: 1 },
  idle: { color: GREEN, label: 'Idle · live', order: 2 },
  paused: { color: GREY, label: 'Paused', order: 3 },
  done: { color: GREY, label: 'Done', order: 3 },
};
// LEGEND is the filter bar's key to the colours.
const LEGEND = [[RED, 'needs you'], [BLUE, 'working'], [GREEN, 'idle'], [GREY, 'paused · done']];

// Rows at rest (paused, done, failed) older than this are not listed.
const RECENT_DAYS = 30;
const POLL_MS = 10000;
const LIST_MIN = 240;
const LIST_DEFAULT = 340;

// rowKey names a row across polls: a research conversation by its id, so
// a requested row and the sandbox that serves it are one selection.
export function rowKey(row) {
  return row.research ? `r:${row.research}` : `${row.sandbox}/${row.task}`;
}

// sessionsRoute reads #/sessions[/<sandbox>/<task> | /research/<id>].
export function sessionsRoute(hash = window.location.hash) {
  const m = hash.match(/^#\/sessions(?:\/([a-z0-9][-a-z0-9.]{0,252})\/([A-Za-z0-9_][A-Za-z0-9._-]{0,127}))?$/);
  if (!m) return null;
  if (!m[1]) return { key: '' };
  return { key: m[1] === 'research' ? `r:${m[2]}` : `${m[1]}/${m[2]}` };
}

function hashOf(key) {
  if (!key) return '#/sessions';
  return key.startsWith('r:') ? `#/sessions/research/${key.slice(2)}` : `#/sessions/${key}`;
}

// rowTitle is what a row is called: a research conversation's name, else
// its recipe's label.
export function rowTitle(row) {
  if (row.recipe === 'research' || row.requested) return row.title || 'untitled research';
  return row.label || row.recipe || row.task;
}

// rowDoing is what the row's agent is doing now, in a few words.
export function rowDoing(row) {
  const run = (row.runs && row.runs[0]) || {};
  if (row.requested) return 'starting sandbox…';
  if (row.waiting) return '⚠ asking permission';
  if (row.retrying) return '⟳ retrying';
  if (row.unreachable) return '⚠ daemon unreachable';
  if (run.status === 'ready') return '📝 draft ready';
  if (row.busy) return 'working';
  if (run.status === 'running') return row.pod === 'running' ? 'running' : `${row.pod}…`;
  if (row.state === 'failed') return row.pod === 'evicted' ? 'evicted' : 'failed';
  if (row.pod === 'paused') return 'paused';
  if (row.live) return 'idle';
  return run.status || 'done';
}

function itemText(row) {
  if (!row.item) return '';
  return row.item === 'pr' ? `PR #${row.number}` : `#${row.number}`;
}

function shortRepo(repo) {
  return (repo || '').split('/').pop();
}

// visibleRows are the rows the search leaves: it matches the title, the
// repository, the sandbox, the recipe and the item.
export function visibleRows(rows, { query }) {
  const q = (query || '').trim().toLowerCase();
  return rows.filter(r => {
    if (!q) return true;
    return [rowTitle(r), r.repo, r.sandbox, r.recipe, itemText(r), r.title].some(s => (s || '').toLowerCase().includes(q));
  });
}

// stateOrder sorts rows by their dot's colour, then newest change first.
function stateOrder(a, b) {
  const oa = (STATE[a.state] || STATE.done).order;
  const ob = (STATE[b.state] || STATE.done).order;
  return (oa - ob) || (b.updatedAt || '').localeCompare(a.updatedAt || '');
}

// orderedRows are the rows to list, in order: all but the ones at rest
// for longer than RECENT_DAYS.
export function orderedRows(rows, now = Date.now()) {
  const cutoff = now - RECENT_DAYS * 24 * 3600 * 1000;
  const atRest = (r) => ['paused', 'done', 'failed'].includes(r.state);
  return rows
    .filter(r => !atRest(r) || !r.updatedAt || new Date(r.updatedAt).getTime() >= cutoff)
    .sort(stateOrder);
}

function useStored(key, initial) {
  const [value, setValue] = useState(() => {
    try {
      const v = localStorage.getItem(key);
      return v === null ? initial : JSON.parse(v);
    } catch { return initial; }
  });
  useEffect(() => { try { localStorage.setItem(key, JSON.stringify(value)); } catch { /* full or blocked */ } }, [key, value]);
  return [value, setValue];
}

function StateMark({ state }) {
  const s = STATE[state] || STATE.done;
  return <span title={s.label} style={{ color: s.color, width: '14px', display: 'inline-block', textAlign: 'center', flex: '0 0 auto', fontSize: '14px', lineHeight: 1 }}>●</span>;
}

const muted = { color: 'var(--text-secondary)' };
const small = { fontSize: 'x-small' };

// SessionListRow is a row, one continuous line wherever it is shown:
// state, engine, recipe, item, repository, age. The sandbox, what the
// agent is doing and the item's title are its hover.
function SessionListRow({ row, selected, onOpen }) {
  const title = rowTitle(row);
  const hover = [title, row.recipe !== 'research' ? row.title : '', rowDoing(row), row.sandbox].filter(Boolean).join(' · ');
  return (
    <button type="button" title={hover} onClick={() => onOpen(row)} aria-current={selected || undefined}
      style={{
        display: 'flex', alignItems: 'center', gap: '6px', width: '100%', textAlign: 'left', whiteSpace: 'nowrap',
        padding: '5px 8px', border: 'none', cursor: 'pointer', font: 'inherit',
        borderLeft: selected ? '3px solid var(--link-color, #0969da)' : '3px solid transparent',
        background: selected ? 'var(--bg-secondary)' : 'none', color: 'var(--text-primary)',
      }}>
      <StateMark state={row.state} />
      <EngineIcon engine={row.engine} size={16} />
      <span style={{ flex: '0 0 auto', maxWidth: '60%', overflow: 'hidden', textOverflow: 'ellipsis', fontWeight: selected ? 600 : 400 }}>{title}</span>
      {row.item && (
        <a href={row.htmlURL} target="_blank" rel="noopener noreferrer" onClick={e => e.stopPropagation()} style={small}>{itemText(row)} ↗</a>
      )}
      {row.repo && (
        <span style={{ ...small, ...muted, padding: '0 6px', borderRadius: '10px', border: '1px solid var(--border-color)', overflow: 'hidden', textOverflow: 'ellipsis', flex: '0 1 auto', minWidth: '40px' }}>
          {shortRepo(row.repo)}</span>
      )}
      <span style={{ ...small, ...muted }}>{ageOf(row.updatedAt)}</span>
    </button>
  );
}

// StartBox starts a session on one of the member's boards' repositories:
// research's box, with the repository picked here.
export function StartBox({ boards, onStarted, autoFocus }) {
  const [boardName, setBoardName] = useStored('sessions.lastBoard', '');
  const [topic, setTopic] = useState('');
  const [prompts, setPrompts] = useState({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const boxRef = useRef(null);
  const board = boards.find(b => b.name === boardName) || boards[0];
  const repo = board ? repoFromURL(board.repoURL) : '';

  useEffect(() => {
    if (!board) return undefined;
    let live = true;
    setPrompts({});
    fetch(`/api/board/${board.name}/research/prompts`)
      .then(res => (res.ok ? res.json() : Promise.reject(res.statusText)))
      .then(data => { if (live) setPrompts(data.prompts || {}); })
      .catch(() => {});
    return () => { live = false; };
  }, [board]);

  useEffect(() => { if (autoFocus && boxRef.current) boxRef.current.focus(); }, [autoFocus]);

  const fill = (kind) => {
    if (!prompts[kind]) return;
    setTopic(prompts[kind]);
    if (boxRef.current) boxRef.current.focus();
  };

  const start = () => {
    if (!board || !topic.trim() || busy) return;
    setBusy(true);
    setError('');
    fetch(`/api/board/${board.name}/research`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ kind: 'topic', topic: topic.trim() }),
    })
      .then(async res => {
        const body = await res.json().catch(() => ({}));
        if (!res.ok) { setError(body.error || `could not start a session: HTTP ${res.status}`); return; }
        setTopic('');
        onStarted({ research: body.sessionId, title: body.title || '', repo: board.repoURL });
      })
      .catch(err => setError(`could not start a session: ${err}`))
      .finally(() => setBusy(false));
  };

  if (!boards.length) {
    return <div style={{ ...muted, padding: '8px 2px' }}>No boards yet: add a repository on Work to start sessions on it.</div>;
  }
  const link = { border: 'none', background: 'none', padding: 0, font: 'inherit', fontSize: 'x-small', color: 'var(--link-color, #0969da)', cursor: 'pointer' };
  return (
    <div style={{ border: '1px solid var(--border-color)', borderRadius: '10px', background: 'var(--bg-secondary)', padding: '10px 12px' }}>
      {error && <div className="warning-banner" style={{ marginBottom: '6px', cursor: 'pointer' }} onClick={() => setError('')}>{error}</div>}
      <GrowingTextarea value={topic} onChange={e => setTopic(e.target.value)} minRows={2} inputRef={boxRef}
        aria-label="Start a session"
        placeholder={`Ask anything about ${repo || 'this repository'} — a question, a subsystem, or 'compare with …'`}
        onKeyDown={e => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); start(); } }}
        style={{ width: '100%', border: 'none', outline: 'none', resize: 'none', background: 'transparent', color: 'var(--text-primary)', font: 'inherit', boxSizing: 'border-box' }} />
      <div style={{ display: 'flex', alignItems: 'center', gap: '10px', marginTop: '6px', flexWrap: 'wrap' }}>
        <select aria-label="Repository" value={board ? board.name : ''} onChange={e => setBoardName(e.target.value)}
          title={board ? board.repoURL : ''}
          style={{ fontSize: 'x-small', borderRadius: '12px', padding: '2px 6px', border: '1px solid var(--border-color)', background: 'var(--bg-card)', color: 'var(--text-primary)' }}>
          {boards.map(b => <option key={b.name} value={b.name}>▣ {repoFromURL(b.repoURL) || b.name}</option>)}
        </select>
        <button type="button" style={{ ...link, opacity: prompts.onboard ? 1 : 0.5 }} disabled={!prompts.onboard || busy} onClick={() => fill('onboard')}
          title="Puts the overview prompt in the box — read it, edit it, send it">overview</button>
        <button type="button" style={{ ...link, opacity: prompts.activity ? 1 : 0.5 }} disabled={!prompts.activity || busy} onClick={() => fill('activity')}
          title="Puts the recent-changes prompt in the box. The window is the first line: edit it">recent changes</button>
        <span style={{ flex: 1 }} />
        <span style={{ ...muted, ...small }}>⏎ start · ⇧⏎ newline</span>
        <button className="btn btn-sm" disabled={!topic.trim() || busy} onClick={start}>{busy ? 'Requesting…' : 'Start'}</button>
      </div>
    </div>
  );
}

// SessionStrip heads the open session: its runs, its sandbox and the
// sandbox's other sessions, since Pause and End act on all of them.
function SessionStrip({ row, rows, onOpen, onCard }) {
  const others = rows.filter(r => r.sandbox === row.sandbox && rowKey(r) !== rowKey(row));
  const runs = [...(row.runs || [])].reverse();
  return (
    <div style={{ padding: '8px 14px 0', fontSize: 'small', textAlign: 'left', display: 'flex', flexDirection: 'column', gap: '4px' }}>
      {runs.length > 0 && (
        <div style={small}>
          <span style={muted}>runs: </span>
          {runs.map((run, i) => (
            <span key={run.run || i}>
              {i > 0 && <span style={muted}> → </span>}
              <span title={`${run.recipe} · ${run.status}${run.startedAt ? ' · started ' + ageOf(run.startedAt) + ' ago' : ''}`}>
                {run.label || run.recipe} <StateMark state={{ running: 'working', ready: 'needs-you', done: 'done', failed: 'failed' }[run.status] || 'done'} />
              </span>
            </span>
          ))}
        </div>
      )}
      <div style={{ ...small, display: 'flex', alignItems: 'center', gap: '8px', flexWrap: 'wrap' }}>
        <button type="button" className="btn btn-sm" onClick={() => onCard(row.sandbox)} title="The sandbox: wake, pause, terminal, logs, delete">▣ {row.sandbox}</button>
        <span style={muted}>{row.pod}</span>
        <EngineIcon engine={row.engine} />
        {row.repo && <span style={muted}>{row.repo}</span>}
        {row.item && <a href={row.htmlURL} target="_blank" rel="noopener noreferrer">{itemText(row)} ↗</a>}
        {others.length > 0 && (
          <span>
            <span style={muted}>also here: </span>
            {others.map((o, i) => (
              <span key={rowKey(o)}>
                {i > 0 && ', '}
                <button type="button" onClick={() => onOpen(o)}
                  style={{ border: 'none', background: 'none', padding: 0, font: 'inherit', color: 'var(--link-color, #0969da)', cursor: 'pointer' }}>
                  {rowTitle(o)}</button> <span style={muted}>({rowDoing(o)})</span>
              </span>
            ))}
          </span>
        )}
      </div>
    </div>
  );
}

// useSessions polls /api/sessions.
function useSessions() {
  const [rows, setRows] = useState(null);
  const [error, setError] = useState('');
  const load = useCallback(() => {
    fetch('/api/sessions')
      .then(res => (res.ok ? res.json() : res.json().then(b => Promise.reject(b.error || res.statusText), () => Promise.reject(res.statusText))))
      .then(data => { setRows(data.sessions || []); setError(''); })
      .catch(err => setError(`could not load sessions: ${err}`));
  }, []);
  useEffect(() => {
    load();
    const t = setInterval(() => { if (!document.hidden) load(); }, POLL_MS);
    return () => clearInterval(t);
  }, [load]);
  return { rows, error, load };
}

export default function Sessions({ namespace }) {
  const { rows, error, load } = useSessions();
  const [boards, setBoards] = useState([]);
  const [selected, setSelected] = useState(() => (sessionsRoute() || { key: '' }).key);
  // pending is the row a Start just asked for, until the feed lists it.
  const [pending, setPending] = useState(null);
  const [query, setQuery] = useState('');
  const [listWidth, setListWidth] = useStored('sessions.listWidth', LIST_DEFAULT);
  const [collapsed, setCollapsed] = useStored('sessions.collapsed', false);
  const [starting, setStarting] = useState(false); // the start box in the right pane
  const [card, setCard] = useState(null);
  const splitRef = useRef(null);

  useEffect(() => {
    fetch('/api/boards').then(res => (res.ok ? res.json() : [])).then(data => setBoards(Array.isArray(data) ? data : [])).catch(() => {});
  }, []);

  const all = useMemo(() => {
    const list = rows || [];
    if (pending && !list.some(r => rowKey(r) === rowKey(pending))) return [pending, ...list];
    return list;
  }, [rows, pending]);

  const select = useCallback((key) => {
    setSelected(key);
    setStarting(false);
    window.history.replaceState(null, '', hashOf(key));
  }, []);
  const open = all.find(r => rowKey(r) === selected) || null;
  const isOpen = !!(open || starting);

  const shown = visibleRows(all, { query });
  const flat = orderedRows(shown);
  const needs = all.filter(r => r.state === 'needs-you').length;

  // ↑/↓ walk the list; Escape closes the session. Not while typing.
  useEffect(() => {
    const onKey = (e) => {
      const tag = (e.target && e.target.tagName) || '';
      if (e.defaultPrevented || ['INPUT', 'TEXTAREA', 'SELECT'].includes(tag) || (e.target && e.target.isContentEditable)) return;
      if (e.key === 'Escape' && isOpen) { select(''); return; }
      if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
      if (!flat.length) return;
      e.preventDefault();
      const i = flat.findIndex(r => rowKey(r) === selected);
      const next = e.key === 'ArrowDown' ? Math.min(flat.length - 1, i + 1) : Math.max(0, i < 0 ? 0 : i - 1);
      select(rowKey(flat[next]));
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [flat, selected, isOpen, select]);

  const onDrag = (e) => {
    e.preventDefault();
    const left = splitRef.current ? splitRef.current.getBoundingClientRect().left : 0;
    const move = (ev) => setListWidth(Math.max(LIST_MIN, Math.min(window.innerWidth * 0.6, ev.clientX - left)));
    const up = () => { window.removeEventListener('mousemove', move); window.removeEventListener('mouseup', up); };
    window.addEventListener('mousemove', move);
    window.addEventListener('mouseup', up);
  };

  const onStarted = ({ research, title, repo }) => {
    const row = { sandbox: '', research, title, recipe: 'research', label: 'Research', requested: true, state: 'working', pod: 'starting', repoURL: repo, updatedAt: new Date().toISOString() };
    setPending(row);
    select(rowKey(row));
    setTimeout(load, 3000);
  };

  const narrow = isOpen;
  const filters = (
    <div style={{ display: 'flex', gap: '8px', alignItems: 'center', flexWrap: 'wrap', ...small, margin: '8px 0' }}>
      {!narrow && (
        <span aria-label="Legend" style={{ display: 'flex', gap: '8px' }}>
          {LEGEND.map(([color, label]) => <span key={label}><span style={{ color }}>●</span> <span style={muted}>{label}</span></span>)}
        </span>
      )}
      <input type="search" aria-label="Search sessions" placeholder="🔍 search" value={query} onChange={e => setQuery(e.target.value)}
        style={{ flex: narrow ? '1 1 100px' : '0 1 200px', marginLeft: narrow ? 0 : 'auto', fontSize: 'x-small' }} />
    </div>
  );

  const list = (
    <div>
      {error && <div className="warning-banner" style={{ marginBottom: '8px' }}>{error}</div>}
      {rows === null ? (
        <div style={{ ...muted, fontStyle: 'italic', padding: '6px 2px' }}>loading…</div>
      ) : !flat.length ? (
        <div style={{ ...muted, fontStyle: 'italic', padding: '6px 2px' }}>
          {all.length ? 'No session matches.' : 'No sessions yet. Start one above, or from a Work row.'}
        </div>
      ) : flat.map(r => (
        <SessionListRow key={rowKey(r)} row={r} selected={rowKey(r) === selected} onOpen={x => select(rowKey(x))} />
      ))}
    </div>
  );

  // Nothing open: the box and the list, all the way across.
  if (!isOpen) {
    return (
      <div className="work-card" style={{ padding: '14px', textAlign: 'left', fontSize: 'small' }}>
        <StartBox boards={boards} onStarted={onStarted} />
        {filters}
        {list}
        {card && <SandboxCard name={card} namespace={namespace} onClose={() => setCard(null)} />}
      </div>
    );
  }

  const strip = (
    <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: '4px', padding: '8px 0', width: '28px', borderRight: '1px solid var(--border-color)', overflowY: 'auto' }}>
      <button type="button" title="Show the list" aria-label="Show the list" onClick={() => setCollapsed(false)}
        style={{ border: 'none', background: 'none', cursor: 'pointer', color: 'var(--text-secondary)' }}>»</button>
      {flat.map(r => (
        <button key={rowKey(r)} type="button" title={`${rowTitle(r)} · ${shortRepo(r.repo)} · ${rowDoing(r)}`} onClick={() => select(rowKey(r))}
          style={{ border: 'none', background: rowKey(r) === selected ? 'var(--bg-secondary)' : 'none', cursor: 'pointer', padding: '2px', borderRadius: '4px' }}>
          <StateMark state={r.state} />
        </button>
      ))}
    </div>
  );

  return (
    <div ref={splitRef} style={{ display: 'flex', height: 'calc(100vh - 90px)', textAlign: 'left', fontSize: 'small', borderTop: '1px solid var(--border-color)' }}>
      {collapsed ? strip : (
        <>
          <div style={{ width: `${listWidth}px`, flex: '0 0 auto', overflowY: 'auto', padding: '8px 6px' }}>
            <div style={{ display: 'flex', gap: '6px', alignItems: 'center' }}>
              <button type="button" className="btn btn-sm" onClick={() => { setStarting(true); setSelected(''); window.history.replaceState(null, '', hashOf('')); }}>+ New</button>
              <span style={{ flex: 1, ...muted, ...small }}>{needs ? `${needs} need you` : ''}</span>
              <button type="button" title="Collapse the list" aria-label="Collapse the list" onClick={() => setCollapsed(true)}
                style={{ border: 'none', background: 'none', cursor: 'pointer', color: 'var(--text-secondary)' }}>«</button>
            </div>
            {filters}
            {list}
          </div>
          <div role="separator" aria-orientation="vertical" aria-label="Resize the list" onMouseDown={onDrag}
            style={{ width: '5px', cursor: 'col-resize', flex: '0 0 auto', borderLeft: '1px solid var(--border-color)' }} />
        </>
      )}
      <div style={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column' }}>
        {starting ? (
          <div style={{ padding: '14px' }}>
            <div style={{ display: 'flex', marginBottom: '8px' }}>
              <strong style={{ flex: 1 }}>New session</strong>
              <button className="btn btn-sm" onClick={() => select('')} aria-label="Close" title="Close (Esc)">✕</button>
            </div>
            <StartBox boards={boards} onStarted={onStarted} autoFocus />
          </div>
        ) : (
          <>
            <SessionStrip row={open} rows={all} onOpen={x => select(rowKey(x))} onCard={setCard} />
            <ResearchConversation
              key={open.task ? `${open.sandbox}/${open.task}` : rowKey(open)}
              task={open.task ? { sandbox: open.sandbox, task: open.task } : null}
              sessionId={open.research || undefined}
              pending={!!open.requested}
              title={open.recipe === 'research' || open.requested ? (open.title || '') : `${rowTitle(open)} · ${open.sandbox}`}
              fill
              onDismiss={() => select('')}
              onDeleted={() => { select(''); load(); }}
              onRenamed={load}
            />
          </>
        )}
      </div>
      {card && <SandboxCard name={card} namespace={namespace} onClose={() => setCard(null)} />}
    </div>
  );
}
