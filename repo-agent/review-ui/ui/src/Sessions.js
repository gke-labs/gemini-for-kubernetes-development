import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { ResearchConversation, GrowingTextarea, repoFromURL } from './Research';
import SandboxCard from './SandboxCard';
import { EngineIcon, ageOf, recipeSource } from './WorkBits';
import { fetchWorkFeed } from './Work';

// Sessions: every agent conversation in the member's namespace, whatever
// started it — a Work row's button, the box on this page, the factory CLI
// (docs/design/sessions-view.md). Work starts from GitHub; this starts
// from the agents, sorted by what each is doing.
//
// A split page: the list is a column on the left, its first row + New,
// and the right is what is open — a session, or the box to start one,
// which is where the page opens. The column can be dragged wider or
// collapsed to a strip of state dots.
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
// a requested row and the sandbox that serves it are one selection; an
// overseer's by its overseer too, since its sandbox may share a name with
// one of the member's.
export function rowKey(row) {
  if (row.research) return `r:${row.research}`;
  return row.overseer ? `o:${row.overseer}/${row.sandbox}/${row.task}` : `${row.sandbox}/${row.task}`;
}

// sessionsRoute reads #/sessions[/<sandbox>/<task> | /research/<id> |
// /overseers/<name>/<sandbox>/<task>].
export function sessionsRoute(hash = window.location.hash) {
  const o = hash.match(/^#\/sessions\/overseers\/([A-Za-z0-9._-]+)\/([a-z0-9][-a-z0-9.]{0,252})\/([A-Za-z0-9_][A-Za-z0-9._-]{0,127})$/);
  if (o) return { key: `o:${o[1]}/${o[2]}/${o[3]}` };
  const m = hash.match(/^#\/sessions(?:\/([a-z0-9][-a-z0-9.]{0,252})\/([A-Za-z0-9_][A-Za-z0-9._-]{0,127}))?$/);
  if (!m) return null;
  if (!m[1]) return { key: '' };
  return { key: m[1] === 'research' ? `r:${m[2]}` : `${m[1]}/${m[2]}` };
}

function hashOf(key) {
  if (!key) return '#/sessions';
  if (key.startsWith('o:')) return `#/sessions/overseers/${key.slice(2)}`;
  return key.startsWith('r:') ? `#/sessions/research/${key.slice(2)}` : `#/sessions/${key}`;
}

// apiBaseOf is where a row's session and sandbox routes are: an
// overseer's under /api/overseers/<name>, the member's under /api.
export function apiBaseOf(row) {
  return row && row.overseer ? `/api/overseers/${row.overseer}` : '/api';
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

// visibleRows are the rows the search leaves (it matches the title, the
// repository, the sandbox, the recipe, the item and the overseer), less
// the overseers' unless withOverseers.
export function visibleRows(rows, { query, withOverseers = true }) {
  const q = (query || '').trim().toLowerCase();
  return rows.filter(r => {
    if (r.overseer && !withOverseers) return false;
    if (!q) return true;
    return [rowTitle(r), r.repo, r.sandbox, r.recipe, itemText(r), r.title, r.overseer].some(s => (s || '').toLowerCase().includes(q));
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
// state, engine, recipe, number, repository, age, all in one small size.
// The sandbox, what the agent is doing and the item's title are its
// hover; the item's link is in the open session's strip.
function SessionListRow({ row, selected, onOpen }) {
  const title = rowTitle(row);
  const hover = [title, row.recipe !== 'research' ? row.title : '', rowDoing(row), row.overseer ? `${row.overseer}'s ${row.sandbox}` : row.sandbox].filter(Boolean).join(' · ');
  return (
    <button type="button" title={hover} onClick={() => onOpen(row)} aria-current={selected || undefined}
      style={{
        display: 'flex', alignItems: 'center', gap: '6px', width: '100%', textAlign: 'left', whiteSpace: 'nowrap',
        padding: '5px 8px', border: 'none', cursor: 'pointer', font: 'inherit', fontSize: 'x-small',
        borderLeft: selected ? '3px solid var(--link-color, #0969da)' : '3px solid transparent',
        background: selected ? 'var(--bg-secondary)' : 'none', color: 'var(--text-primary)',
      }}>
      <StateMark state={row.state} />
      <EngineIcon engine={row.engine} size={16} />
      <span style={{ flex: '0 0 auto', maxWidth: '60%', overflow: 'hidden', textOverflow: 'ellipsis', fontWeight: selected ? 600 : 400 }}>{title}</span>
      {row.item && <span>#{row.number}</span>}
      {row.repo && (
        <span style={{ ...muted, padding: '0 6px', borderRadius: '10px', border: '1px solid var(--border-color)', overflow: 'hidden', textOverflow: 'ellipsis', flex: '0 1 auto', minWidth: '40px' }}>
          {shortRepo(row.repo)}</span>
      )}
      {row.overseer && <span title={`Overseer ${row.overseer}`} style={{ ...muted, flex: '0 0 auto' }}>🤖 {row.overseer}</span>}
      <span style={muted}>{ageOf(row.updatedAt)}</span>
    </button>
  );
}

// parseItemRef finds an issue or PR named in what was typed: a GitHub
// URL, owner/repo#N, alias#N (a board's or repository's name) or #N (the
// repository picked). No LLM: what is not one of these is a question.
export function parseItemRef(text) {
  const t = text || '';
  let m = t.match(/https?:\/\/github\.com\/([\w.-]+)\/([\w.-]+)\/(?:issues|pull)\/(\d+)/);
  if (m) return { owner: m[1], repo: m[2], number: Number(m[3]), text: m[0] };
  m = t.match(/(?:^|[\s(])([\w.-]+)\/([\w.-]+)#(\d+)\b/);
  if (m) return { owner: m[1], repo: m[2], number: Number(m[3]), text: `${m[1]}/${m[2]}#${m[3]}` };
  m = t.match(/(?:^|[\s(])([\w.-]+)#(\d+)\b/);
  if (m) return { alias: m[1], number: Number(m[2]), text: `${m[1]}#${m[2]}` };
  m = t.match(/(?:^|[\s(])#(\d+)\b/);
  if (m) return { number: Number(m[1]), text: `#${m[1]}` };
  return null;
}

// refBoard is the board a reference is on: the one whose repository it
// names, the one named like its alias (board or repository name), or,
// for a bare #N, current. null when none is.
export function refBoard(ref, boards, current) {
  const repoOf = (b) => (b.repoURL || '').replace(/\.git$/, '').replace(/^https?:\/\/github\.com\//, '').toLowerCase();
  if (ref.owner) return boards.find(b => repoOf(b) === `${ref.owner}/${ref.repo}`.toLowerCase()) || null;
  if (ref.alias) {
    const a = ref.alias.toLowerCase();
    return boards.find(b => b.name.toLowerCase() === a) || boards.find(b => repoOf(b).split('/')[1] === a) || null;
  }
  return current || null;
}

// itemSuggestions are what an issue or PR row offers to start, by Work's
// row rules: one entry per recipe group; a group running, starting or
// with a draft to judge offers its session instead of another run;
// Review on the viewer's own PR only with self-review on, and not over
// a pending review. Recipes that need typed input stay on Work.
export function itemSuggestions(item, { selfReview = false } = {}) {
  const groupOf = (x) => x.session || x.recipe || x.name;
  const runs = [];
  for (const run of item.sessions || []) if (!runs.some(r => groupOf(r) === groupOf(run))) runs.push(run);
  const groups = [];
  for (const rec of item.recipes || []) {
    if (rec.name === 'review' && item.mine && item.type !== 'issue' && !selfReview) continue;
    const g = groupOf(rec);
    const known = groups.find(x => x.group === g);
    if (known) known.recipes.push(rec); else groups.push({ group: g, recipes: [rec] });
  }
  const out = [];
  for (const { group: g, recipes } of groups) {
    const launching = Object.entries(item.launching || {}).find(([name]) => recipes.some(r => r.name === name));
    const run = runs.find(r => groupOf(r) === g);
    const status = (launching && launching[1]) || (run && run.status) || '';
    const lead = recipes.find(r => r.name === g) || recipes[0];
    if (status === 'starting' || status === 'queued') {
      out.push({ kind: 'busy', label: `${lead.label} ${status}…`, group: g });
      continue;
    }
    if ((status === 'running' || status === 'ready') && run && run.task) {
      out.push({ kind: 'open', label: `Open ${run.label || lead.label} session`, group: g, sandbox: run.sandbox, task: run.task, status });
      continue;
    }
    const again = status === 'done' || status === 'failed';
    for (const rec of recipes) {
      if ((rec.inputs || []).length) continue;
      if (rec.name === 'review' && item.reviewPending) continue;
      out.push({ kind: 'launch', label: `${rec.label}${again && rec === lead ? ' again' : ''}`, recipe: rec.name, group: g });
    }
  }
  return out;
}

// pickSuggestion is the entry a word typed beside the reference points
// at ("review kcc#1" → Review), else the first one that can be acted on.
export function pickSuggestion(suggestions, text) {
  const words = (text || '').toLowerCase().split(/[^a-z]+/).filter(w => w.length > 2);
  const i = suggestions.findIndex(sg => sg.kind !== 'busy' && words.some(w => sg.label.toLowerCase().includes(w) || (sg.recipe || '').includes(w)));
  if (i >= 0) return i;
  return Math.max(0, suggestions.findIndex(sg => sg.kind !== 'busy'));
}

// SESSION_ENGINES are the engines a session can run on: the session
// daemon has no claude engine yet.
export const SESSION_ENGINES = ['gemini', 'antigravity'];

// sessionEngine is the engine a session on a board with engine runs on by
// default: a claude board's runs on gemini, as research always has.
export function sessionEngine(engine) {
  return SESSION_ENGINES.includes(engine) ? engine : 'gemini';
}

// FEED_FRESH is how long a board's work feed read for a reference is
// reused: the launcher reads it per keystroke pause, not per keystroke.
const FEED_FRESH = 60000;

// StartBox starts a session on one of the member's boards' repositories:
// research's box, with the repository and the engine picked here. An
// issue or PR named in it (#N, kcc#N, owner/repo#N, a URL) turns it into
// a launcher: that row's recipes from the board's feed, Enter starting
// the highlighted one through the row's own route, as a click on Work.
export function StartBox({ boards, onStarted, onOpen, onLaunched, autoFocus }) {
  const [boardName, setBoardName] = useStored('sessions.lastBoard', '');
  const [topic, setTopic] = useState('');
  const [prompts, setPrompts] = useState({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const boxRef = useRef(null);
  const board = boards.find(b => b.name === boardName) || boards[0];
  // The engine the session runs on: the board's until one is picked, and
  // the board's again when the repository changes.
  const [pickedEngine, setPickedEngine] = useState('');
  const engine = pickedEngine || sessionEngine(board && board.engine);
  const repo = board ? repoFromURL(board.repoURL) : '';

  // The issue or PR named in the box, the board it is on, and its row.
  const ref = useMemo(() => parseItemRef(topic), [topic]);
  const target = ref ? refBoard(ref, boards, board) : null;
  const [lookup, setLookup] = useState(null); // { key, status: loading|ok|none|error, item, error }
  const feeds = useRef(new Map());
  const [highlight, setHighlight] = useState(0);
  const refKey = ref && target ? `${target.name}#${ref.number}` : '';
  // The repository chip follows the reference.
  useEffect(() => {
    if (target && board && target.name !== board.name) { setBoardName(target.name); setPickedEngine(''); }
  }, [target, board, setBoardName]);
  useEffect(() => {
    if (!refKey) { setLookup(null); return undefined; }
    const [boardOf, n] = [target.name, ref.number];
    const find = (rows) => (rows || []).find(i => i.number === n) || null;
    const cached = feeds.current.get(boardOf);
    if (cached && Date.now() - cached.at < FEED_FRESH) {
      const item = find(cached.rows);
      setLookup({ key: refKey, status: item ? 'ok' : 'none', item });
      return undefined;
    }
    setLookup({ key: refKey, status: 'loading' });
    let live = true;
    const t = setTimeout(() => {
      fetchWorkFeed(boardOf)
        .then(rows => {
          feeds.current.set(boardOf, { at: Date.now(), rows });
          const item = find(rows);
          if (live) setLookup({ key: refKey, status: item ? 'ok' : 'none', item });
        })
        .catch(err => { if (live) setLookup({ key: refKey, status: 'error', error: err.message }); });
    }, 250);
    return () => { live = false; clearTimeout(t); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [refKey]);
  const selfReview = (() => { try { return localStorage.getItem('repoboard.selfReview') === 'true'; } catch (e) { return false; } })();
  const item = lookup && lookup.key === refKey && lookup.status === 'ok' ? lookup.item : null;
  const options = ref ? [...(item ? itemSuggestions(item, { selfReview }) : []), { kind: 'ask', label: `Ask “${topic.trim()}” as a question` }] : [];
  const optionsKey = options.map(o => o.label).join('|');
  useEffect(() => {
    if (!options.length) return;
    const i = pickSuggestion(options.slice(0, -1), topic);
    setHighlight(options[i] && options[i].kind !== 'busy' && options[i].kind !== 'ask' && item ? i : options.length - 1);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [optionsKey]);

  // act does what the highlighted entry says: open the session, start the
  // recipe on the row, or ask the text as a question.
  const act = (opt) => {
    if (!opt || opt.kind === 'busy' || busy) return;
    if (opt.kind === 'ask') { start(); return; }
    if (opt.kind === 'open') { setTopic(''); onOpen(rowKey({ sandbox: opt.sandbox, task: opt.task })); return; }
    setBusy(true);
    setError('');
    fetch(`/api/board/${target.name}/${item.type === 'issue' ? 'issues' : 'prs'}/${item.number}/recipes/${opt.recipe}`, { method: 'POST' })
      .then(async res => {
        const body = await res.json().catch(() => ({}));
        if (!res.ok) { setError(body.error || `could not start ${opt.label}: HTTP ${res.status}`); return; }
        setTopic('');
        feeds.current.delete(target.name);
        onLaunched({ repoURL: target.repoURL, number: item.number, group: opt.group, recipe: opt.recipe, label: opt.label });
      })
      .catch(err => setError(`could not start ${opt.label}: ${err}`))
      .finally(() => setBusy(false));
  };

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

  // Focused once there is a box to focus: the boards arrive after the mount.
  const hasBoards = boards.length > 0;
  useEffect(() => { if (autoFocus && hasBoards && boxRef.current) boxRef.current.focus(); }, [autoFocus, hasBoards]);

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
      body: JSON.stringify({ kind: 'topic', topic: topic.trim(), engine }),
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
        onKeyDown={e => {
          if (options.length && (e.key === 'ArrowDown' || e.key === 'ArrowUp')) {
            e.preventDefault();
            setHighlight(h => Math.max(0, Math.min(options.length - 1, h + (e.key === 'ArrowDown' ? 1 : -1))));
            return;
          }
          if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); if (options.length) act(options[highlight]); else start(); }
        }}
        style={{ width: '100%', border: 'none', outline: 'none', resize: 'none', background: 'transparent', color: 'var(--text-primary)', font: 'inherit', boxSizing: 'border-box' }} />
      <div style={{ display: 'flex', alignItems: 'center', gap: '10px', marginTop: '6px', flexWrap: 'wrap' }}>
        <select aria-label="Repository" value={board ? board.name : ''} onChange={e => { setBoardName(e.target.value); setPickedEngine(''); }}
          title={board ? board.repoURL : ''}
          style={{ fontSize: 'x-small', borderRadius: '12px', padding: '2px 6px', border: '1px solid var(--border-color)', background: 'var(--bg-card)', color: 'var(--text-primary)' }}>
          {boards.map(b => <option key={b.name} value={b.name}>▣ {repoFromURL(b.repoURL) || b.name}</option>)}
        </select>
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: '2px' }}>
          <EngineIcon engine={engine} size={14} />
          <select aria-label="Engine" value={engine} onChange={e => setPickedEngine(e.target.value)}
            title={board && board.engine === 'claude' ? 'Sessions run on gemini or antigravity: claude is not available for them yet' : 'The agent the session runs on'}
            style={{ fontSize: 'x-small', borderRadius: '12px', padding: '2px 6px', border: '1px solid var(--border-color)', background: 'var(--bg-card)', color: 'var(--text-primary)' }}>
            {SESSION_ENGINES.map(e => <option key={e} value={e}>{e}</option>)}
          </select>
        </span>
        <button type="button" style={{ ...link, opacity: prompts.onboard ? 1 : 0.5 }} disabled={!prompts.onboard || busy} onClick={() => fill('onboard')}
          title="Puts the overview prompt in the box — read it, edit it, send it">overview</button>
        <button type="button" style={{ ...link, opacity: prompts.activity ? 1 : 0.5 }} disabled={!prompts.activity || busy} onClick={() => fill('activity')}
          title="Puts the recent-changes prompt in the box. The window is the first line: edit it">recent changes</button>
        <span style={{ flex: 1 }} />
        <span style={{ ...muted, ...small }}>⏎ start · ⇧⏎ newline</span>
        <button className="btn btn-sm" disabled={!topic.trim() || busy} onClick={start}>{busy ? 'Requesting…' : 'Start'}</button>
      </div>
      {ref && (
        <div role="listbox" aria-label="Item launcher" style={{ marginTop: '8px', borderTop: '1px solid var(--border-color)', paddingTop: '6px', fontSize: 'x-small' }}>
          <div style={{ ...muted, padding: '2px 4px' }}>
            {!target ? `No board for “${ref.alias || `${ref.owner}/${ref.repo}`}”: add it on Work.`
              : !lookup || lookup.key !== refKey || lookup.status === 'loading' ? `Looking up #${ref.number} on ${target.name}…`
                : lookup.status === 'error' ? `Could not read ${target.name}: ${lookup.error}`
                  : lookup.status === 'none' ? `#${ref.number} is not on ${target.name}'s board (open issues and PRs it follows).`
                    : <span><strong style={{ color: 'var(--text-primary)' }}>{item.type === 'issue' ? 'Issue' : 'PR'} #{item.number}</strong> · {item.title}{item.author ? ` · ${item.author}` : ''}</span>}
          </div>
          {options.map((o, i) => (
            <button key={o.label} type="button" role="option" aria-selected={i === highlight} disabled={o.kind === 'busy'}
              onMouseEnter={() => setHighlight(i)} onClick={() => act(o)}
              style={{
                display: 'flex', width: '100%', textAlign: 'left', gap: '6px', padding: '4px 6px', border: 'none', borderRadius: '6px',
                font: 'inherit', cursor: o.kind === 'busy' ? 'default' : 'pointer', color: o.kind === 'busy' ? 'var(--text-secondary)' : 'var(--text-primary)',
                background: i === highlight ? 'var(--bg-card)' : 'none', fontWeight: o.kind === 'ask' ? 400 : 600,
                borderTop: o.kind === 'ask' && i > 0 ? '1px solid var(--border-color)' : 'none', marginTop: o.kind === 'ask' && i > 0 ? '4px' : 0,
              }}>
              <span style={{ width: '10px' }}>{i === highlight ? '▸' : ''}</span>
              <span style={{ flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{o.label}{o.kind === 'open' ? ' ↗' : ''}</span>
              {i === highlight && o.kind !== 'busy' && <span style={muted}>⏎</span>}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

// SessionStrip heads the open session: its runs, its sandbox and the
// sandbox's other sessions, since Pause and End act on all of them.
function SessionStrip({ row, rows, onOpen, onCard }) {
  const others = rows.filter(r => r.sandbox === row.sandbox && (r.overseer || '') === (row.overseer || '') && rowKey(r) !== rowKey(row));
  const runs = [...(row.runs || [])].reverse();
  return (
    <div style={{ padding: '8px 14px 0', fontSize: 'small', textAlign: 'left', display: 'flex', flexDirection: 'column', gap: '4px' }}>
      {runs.length > 0 && (
        <div style={small}>
          <span style={muted}>runs: </span>
          {runs.map((run, i) => (
            <span key={run.run || i}>
              {i > 0 && <span style={muted}> → </span>}
              <span title={`${run.recipe} · ${run.status}${run.startedAt ? ' · started ' + ageOf(run.startedAt) + ' ago' : ''}${recipeSource(run) ? '\n' + recipeSource(run) : ''}`}>
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

// useSessions polls /api/sessions while active (the view shown), and
// reads it again the moment it is shown; withOverseers false leaves the
// overseers' out of the read too, not only the list.
function useSessions(active, withOverseers) {
  const [rows, setRows] = useState(null);
  const [error, setError] = useState('');
  const load = useCallback(() => {
    fetch(withOverseers ? '/api/sessions' : '/api/sessions?overseers=false')
      .then(res => (res.ok ? res.json() : res.json().then(b => Promise.reject(b.error || res.statusText), () => Promise.reject(res.statusText))))
      .then(data => { setRows(data.sessions || []); setError(''); })
      .catch(err => setError(`could not load sessions: ${err}`));
  }, [withOverseers]);
  useEffect(() => {
    if (!active) return undefined;
    load();
    const t = setInterval(() => { if (!document.hidden) load(); }, POLL_MS);
    return () => clearInterval(t);
  }, [load, active]);
  return { rows, error, load };
}

// active is whether Sessions is the view shown: hidden behind Work it
// stays mounted, keeping the open session and the list, but polls
// nothing and leaves the keyboard alone.
export default function Sessions({ namespace, active = true }) {
  // Whether the overseers' sessions are listed: on unless switched off.
  const [withOverseers, setWithOverseers] = useStored('sessions.overseers', true);
  const { rows, error, load } = useSessions(active, withOverseers);
  const [boards, setBoards] = useState([]);
  const [selected, setSelected] = useState(() => (sessionsRoute() || { key: '' }).key);
  // pending is the row a Start just asked for, until the feed lists it.
  const [pending, setPending] = useState(null);
  const [query, setQuery] = useState('');
  const [listWidth, setListWidth] = useStored('sessions.listWidth', LIST_DEFAULT);
  const [collapsed, setCollapsed] = useStored('sessions.collapsed', false);
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

  // select opens a row by its key; '' is + New, the start box.
  const select = useCallback((key) => {
    setSelected(key);
    window.history.replaceState(null, '', hashOf(key));
  }, []);
  const open = all.find(r => rowKey(r) === selected) || null;

  const shown = visibleRows(all, { query, withOverseers });
  const flat = orderedRows(shown);
  const needs = all.filter(r => r.state === 'needs-you').length;

  // ↑/↓ walk the list, + New above its first row; Escape goes back to
  // + New. Not while typing.
  useEffect(() => {
    const onKey = (e) => {
      const tag = (e.target && e.target.tagName) || '';
      if (!active || e.defaultPrevented || ['INPUT', 'TEXTAREA', 'SELECT'].includes(tag) || (e.target && e.target.isContentEditable)) return;
      if (e.key === 'Escape' && open) { select(''); return; }
      if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
      if (!flat.length) return;
      e.preventDefault();
      const i = flat.findIndex(r => rowKey(r) === selected);
      const next = e.key === 'ArrowDown' ? Math.min(flat.length - 1, i + 1) : i - 1;
      select(next < 0 ? '' : rowKey(flat[next]));
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [flat, selected, open, select, active]);

  // Shown again: the URL says which session is open, as it did before.
  useEffect(() => {
    if (active) window.history.replaceState(null, '', hashOf(selected));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active]);

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

  // launched is a recipe the box just started on an issue or PR. Its run
  // has no session to open until factory records it, so the box says it
  // is starting, and the row opens itself when the feed brings it.
  const [launched, setLaunched] = useState(null);
  const onLaunched = (l) => {
    setLaunched({ ...l, since: Date.now() });
    setTimeout(load, 3000);
  };
  useEffect(() => {
    // Only while + New is still open: a session opened meanwhile stays.
    if (!launched || !rows || open) return;
    const repo = (launched.repoURL || '').replace(/\.git$/, '').replace(/^https?:\/\/github\.com\//, '').toLowerCase();
    const row = rows.find(r => !r.overseer && (r.repo || '').toLowerCase() === repo && r.number === launched.number && r.task &&
      (r.recipe === launched.recipe || (r.session || r.recipe) === launched.group) &&
      (r.runs || []).some(run => run.startedAt && new Date(run.startedAt).getTime() >= launched.since - 60000));
    if (row) { setLaunched(null); select(rowKey(row)); }
  }, [rows, launched, open, select]);

  const rowStyle = (on) => ({
    display: 'flex', alignItems: 'center', gap: '6px', width: '100%', textAlign: 'left', whiteSpace: 'nowrap',
    padding: '5px 8px', border: 'none', cursor: 'pointer', font: 'inherit',
    borderLeft: on ? '3px solid var(--link-color, #0969da)' : '3px solid transparent',
    background: on ? 'var(--bg-secondary)' : 'none', color: 'var(--text-primary)',
  });
  const list = (
    <div>
      <button type="button" style={{ ...rowStyle(!open), fontSize: 'x-small', fontWeight: 600 }} onClick={() => select('')} aria-current={!open || undefined}>
        <span style={{ width: '14px', textAlign: 'center' }}>+</span> New session</button>
      {error && <div className="warning-banner" style={{ margin: '6px 0' }}>{error}</div>}
      {rows === null ? (
        <div style={{ ...muted, fontStyle: 'italic', padding: '6px 8px' }}>loading…</div>
      ) : !flat.length ? (
        <div style={{ ...muted, fontStyle: 'italic', padding: '6px 8px' }}>
          {all.length ? 'No session matches.' : 'No sessions yet. Start one, or from a Work row.'}
        </div>
      ) : flat.map(r => (
        <SessionListRow key={rowKey(r)} row={r} selected={rowKey(r) === selected} onOpen={x => select(rowKey(x))} />
      ))}
    </div>
  );

  const strip = (
    <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: '4px', padding: '8px 0', width: '28px', borderRight: '1px solid var(--border-color)', overflowY: 'auto' }}>
      <button type="button" title="Show the list" aria-label="Show the list" onClick={() => setCollapsed(false)}
        style={{ border: 'none', background: 'none', cursor: 'pointer', color: 'var(--text-secondary)' }}>»</button>
      <button type="button" title="New session" aria-label="New session" onClick={() => select('')}
        style={{ border: 'none', background: !open ? 'var(--bg-secondary)' : 'none', cursor: 'pointer', padding: '2px 4px', borderRadius: '4px', color: 'var(--text-primary)' }}>+</button>
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
            <div style={{ display: 'flex', gap: '6px', alignItems: 'center', marginBottom: '6px' }}>
              <input type="search" aria-label="Search sessions" placeholder="🔍 search" value={query} onChange={e => setQuery(e.target.value)}
                style={{ flex: 1, minWidth: 0, fontSize: 'x-small' }} />
              <button type="button" aria-pressed={withOverseers} onClick={() => setWithOverseers(!withOverseers)}
                title={withOverseers ? "Overseers' sessions are listed: click to hide them" : "Overseers' sessions are hidden: click to list them"}
                style={{ border: '1px solid var(--border-color)', borderRadius: '10px', padding: '1px 6px', cursor: 'pointer', fontSize: 'x-small',
                  background: withOverseers ? 'var(--bg-secondary)' : 'none', color: 'var(--text-primary)', opacity: withOverseers ? 1 : 0.55 }}>🤖 Overseers</button>
              <button type="button" title="Collapse the list" aria-label="Collapse the list" onClick={() => setCollapsed(true)}
                style={{ border: 'none', background: 'none', cursor: 'pointer', color: 'var(--text-secondary)' }}>«</button>
            </div>
            {list}
            <div aria-label="Legend" style={{ display: 'flex', flexWrap: 'wrap', gap: '8px', padding: '10px 8px 0', ...small }}>
              {LEGEND.map(([color, label]) => <span key={label}><span style={{ color }}>●</span> <span style={muted}>{label}</span></span>)}
              {needs > 0 && <span style={{ marginLeft: 'auto', color: RED }}>{needs} need you</span>}
            </div>
          </div>
          <div role="separator" aria-orientation="vertical" aria-label="Resize the list" onMouseDown={onDrag}
            style={{ width: '5px', cursor: 'col-resize', flex: '0 0 auto', borderLeft: '1px solid var(--border-color)' }} />
        </>
      )}
      <div style={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column' }}>
        {!open ? (
          <div style={{ padding: '14px' }}>
            {launched && (
              <div role="status" style={{ ...small, ...muted, marginBottom: '8px' }}>
                {launched.label} is starting on #{launched.number}: it opens here when its session is recorded.
              </div>
            )}
            <StartBox boards={boards} onStarted={onStarted} onOpen={select} onLaunched={onLaunched} autoFocus />
          </div>
        ) : (
          <>
            <SessionStrip row={open} rows={all} onOpen={x => select(rowKey(x))} onCard={(name) => setCard({ name, apiBase: apiBaseOf(open) })} />
            <ResearchConversation
              key={open.task ? `${apiBaseOf(open)}/${open.sandbox}/${open.task}` : rowKey(open)}
              task={open.task ? { sandbox: open.sandbox, task: open.task } : null}
              sessionId={open.research || undefined}
              pending={!!open.requested}
              title={open.recipe === 'research' || open.requested ? (open.title || '') : `${rowTitle(open)} · ${open.sandbox}`}
              apiBase={apiBaseOf(open)}
              fill
              onDismiss={() => select('')}
              onDeleted={() => { select(''); load(); }}
              onRenamed={load}
            />
          </>
        )}
      </div>
      {card && <SandboxCard name={card.name} namespace={namespace} apiBase={card.apiBase} onClose={() => setCard(null)} />}
    </div>
  );
}
