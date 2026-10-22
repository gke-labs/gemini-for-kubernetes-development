import React, { useState, useEffect, useCallback, useMemo } from 'react';
import agentSandboxIcon from './agent-sandbox-icon.svg';
import Chip from './Chip';
import SessionSlideOver from './SessionSlideOver';
import SandboxCard from './SandboxCard';
import { SessionMark, RUN_STYLE, ageOf, recipeSource } from './WorkBits';

// OverseerWork: the Overseer page's work view
// (docs/design/overseer-work-view.md). One row per issue (with the PR its
// fix opened), per PR, or per agent without either, from
// GET /api/overseers/<name>/work, drawn as the board's Work rows are: the
// same table and columns (# · Age · Title · Agent · rail), the same chips
// (one per session group, coloured by status, the engine's mark) and the
// same sandbox icon. A chip opens the board's session slide-over, a
// sandbox its sandbox card, both on the overseer's routes (apiBase). The
// rail holds one write: Clear & retry on a row whose sandbox factory marked
// as needing a person (requires human), which the watch otherwise leaves
// alone.

// SECTIONS are the feed's sections, in the page's order.
export const SECTIONS = [
  { key: 'attention', label: 'Needs attention' },
  { key: 'working', label: 'Working' },
  { key: 'queued', label: 'Queued' },
  { key: 'idle', label: 'Idle', collapsed: true },
  { key: 'agents', label: 'Agents' },
];

// GROUPS are the board's session groups and labels for the recipes an
// overseer runs: the care recipes share the care session, one chip.
const GROUPS = {
  care: ['care', 'Care'], 'care-ci': ['care', 'Care'], 'care-comments': ['care', 'Care'],
  'care-rebase': ['care', 'Care'], 'care-iterate': ['care', 'Care'],
  fix: ['fix', 'Fix'], review: ['review', 'Review'], plan: ['plan', 'Plan'], triage: ['triage', 'Triage'],
  fanout: ['fanout', 'Fan out'], workflow: ['workflow', 'Workflow'], agent: ['agent', 'Agent'],
};

// groupOf is a recipe's session group and its chip's label.
export function groupOf(recipe) {
  const g = GROUPS[recipe];
  if (g) return { group: g[0], label: g[1] };
  const name = recipe || 'task';
  return { group: name, label: name.charAt(0).toUpperCase() + name.slice(1) };
}

// rowChips are a row's chips, as the board's: each group's newest run
// (sessions come newest first), then the queued work of a group with no
// run in flight. A queued task is queued, a processing one starting.
export function rowChips(item) {
  const chips = [];
  const byGroup = {};
  for (const s of item.sessions || []) {
    const { group, label } = groupOf(s.recipe);
    if (byGroup[group]) continue;
    byGroup[group] = { group, label, status: s.status || 'done', run: s, recipe: s.recipe };
    chips.push(byGroup[group]);
  }
  for (const q of item.queued || []) {
    const { group, label } = groupOf(q.recipe || q.type);
    const known = byGroup[group];
    if (known && ['running', 'starting', 'queued'].includes(known.status)) continue;
    const chip = { group, label, status: q.state === 'processing' ? 'starting' : 'queued', queued: q, recipe: q.recipe };
    // A queued run of a resting group is its chip now, as a click
    // starting is on the board.
    if (known) Object.assign(known, chip, { run: undefined }); else { byGroup[group] = chip; chips.push(chip); }
  }
  return chips;
}

// chipTitle is a chip's hover: its label and status, what ran, where.
function chipTitle(c, reason) {
  const lines = [`${c.label}: ${c.status}${c.recipe && c.recipe !== c.group ? ` — ${c.recipe}` : ''}`];
  if (c.status === 'failed' && reason) lines.push(reason);
  if (recipeSource(c.run)) lines.push(recipeSource(c.run));
  if (c.queued) lines.push(`${c.queued.type}${c.queued.rank ? `, rank ${c.queued.rank}` : ''}${c.queued.priority ? `, ${c.queued.priority}` : ''}`);
  if (c.run && c.run.sandbox) lines.push(c.run.task ? `${c.run.sandbox} — open the session` : `${c.run.sandbox} — a classic task, no session: open the sandbox`);
  return lines.join('\n');
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

const userChip = (u) => (
  <span key={u} style={{ marginRight: '6px' }}>
    <Chip text={`⦿ ${u}`} color="var(--group-review)" bg="color-mix(in srgb, var(--group-review) 14%, transparent)" title="The bot the overseer ran this as" />
  </span>
);

// cooldownTitle explains a row's cooldown label: when its task may be
// queued again, and the run it waits out (a failed run waits 20 minutes,
// any other the workflow's cooldown).
export function cooldownTitle(c) {
  const at = (s) => new Date(s).toLocaleString();
  const what = c.workflow ? `workflow ${c.workflow}` : 'fix';
  const last = c.lastCompletedAt ? `last run ${(c.lastStatus || 'ended').toLowerCase()} at ${at(c.lastCompletedAt)}` : '';
  const why = c.lastStatus === 'Failed' ? 'a failed run waits 20 minutes' : `${what}'s cooldown`;
  return [`Not queued again before ${at(c.until)}`, last, why].filter(Boolean).join('; ');
}

// failureReason is a failed task's error without its exit status: what
// the child printed ("exit status 1: applying the Change …" → "applying
// the Change …").
export function failureReason(f) {
  return (f.error || '').replace(/^exit status \d+:\s*/, '');
}

// failureText is a row's failed-task label: which task and why, cut short;
// failureTitle is all of it, on hover. Whether a run's apply failed is the
// run's to say (its session's "apply failed" chip).
export function failureText(f) {
  const why = failureReason(f);
  const short = why.length > 48 ? `${why.slice(0, 47)}…` : why;
  return `${f.recipe || f.type} failed${short ? `: ${short}` : ''}`;
}

export function failureTitle(f) {
  return [`${f.recipe || f.type} task failed at ${new Date(f.at).toLocaleString()}`, failureReason(f) || f.error].filter(Boolean).join('\n');
}

// requiresHuman is the row's sandbox that needs a person, if any.
export function requiresHuman(item) {
  return (item.sandboxes || []).find(s => s.requiresHuman);
}

// byNeed puts rows that need a person first, keeping the feed's order
// otherwise.
export function byNeed(rows) {
  return [...rows.filter(r => requiresHuman(r)), ...rows.filter(r => !requiresHuman(r))];
}

export function WorkRow({ item, onOpenSession, onOpenSandbox, onRetry }) {
  const chips = rowChips(item);
  const users = [...new Set((item.sandboxes || []).map(s => s.user).filter(Boolean))];
  const podTrouble = (item.sandboxes || []).find(s => s.pod === 'evicted' || s.pod === 'failed');
  const failedChip = chips.some(c => c.status === 'failed');
  const isAgent = item.kind === 'agent';
  const needsHuman = requiresHuman(item);
  const titleText = item.title || (isAgent ? ((item.sandboxes || [])[0] || {}).name : '') || (item.kind === 'pr' ? `PR #${item.number}` : `Issue #${item.number}`);
  return (
    <tr data-testid={`row-${item.key}`}>
      <td className="work-num" style={{ padding: '6px 4px 6px 8px', width: '1%' }}
        title={isAgent ? 'An agent with no issue' : item.kind === 'issue' ? 'Issue' : 'Pull request'}>
        {isAgent ? '⚙' : `${item.kind === 'issue' ? '◉' : '⇄'} #${item.number}`}
      </td>
      <td style={{ padding: '6px 6px 6px 4px', fontSize: 'small', color: 'var(--text-secondary)', whiteSpace: 'nowrap', width: '1%' }}
        title={item.updatedAt}>{ageOf(item.updatedAt)}</td>
      <td style={{ padding: '6px 8px', maxWidth: '480px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
        {users.map(userChip)}
        {item.htmlUrl
          ? <a href={item.htmlUrl} target="_blank" rel="noopener noreferrer" title={titleText}>{titleText}</a>
          : <span title={titleText}>{titleText}</span>}
        {item.pr ? (
          <a href={item.prUrl} target="_blank" rel="noopener noreferrer"
            style={{ marginLeft: '6px', fontSize: 'small', color: 'var(--text-secondary)' }}
            title="The PR this issue's fix opened">↳ PR #{item.pr}</a>
        ) : null}
        {item.workflow && (
          <span style={{ marginLeft: '6px' }}><Chip text="workflow" color="var(--text-secondary)" bg="var(--bg-secondary)" title="A workflow works on this issue" /></span>
        )}
        {needsHuman && (
          <span style={{ marginLeft: '6px', fontWeight: 600 }}>
            <Chip text="🙋 requires human" color={RUN_STYLE.failed.color} bg={RUN_STYLE.failed.bg}
              title={`${needsHuman.requiresHuman}\n\nThe overseer won't try this again until you clear it (Clear & retry).`} />
          </span>
        )}
      </td>
      <td style={{ padding: '6px 8px' }}>
        {(item.sandboxes || []).map(sb => (
          <span key={sb.name} onClick={() => onOpenSandbox(sb.name)} style={{ cursor: 'pointer' }}>
            <img src={agentSandboxIcon} alt="sandbox"
              title={`${sb.name} (${sb.engine || 'gemini'}, ${sb.pod}) — tasks & logs`}
              style={{ height: '20px', verticalAlign: 'middle', marginRight: '6px', opacity: sb.pod === 'paused' ? 0.55 : 1 }} />
          </span>
        ))}
        {chips.map(c => {
          const style = RUN_STYLE[c.status] || RUN_STYLE.done;
          const text = c.status === 'failed' ? `${c.label} !` : c.label;
          const title = chipTitle(c, item.reason);
          if (c.run && c.run.task) {
            return (
              <button key={c.group} type="button" title={title}
                onClick={() => onOpenSession({ sandbox: c.run.sandbox, task: c.run.task })}
                style={{ border: 'none', background: 'none', padding: 0, cursor: 'pointer', marginLeft: '4px', whiteSpace: 'nowrap' }}>
                <Chip text={text} color={style.color} bg={style.bg}><SessionMark engine={c.run.engine} /></Chip>
              </button>
            );
          }
          if (c.run) {
            return (
              <button key={c.group} type="button" title={title} onClick={() => onOpenSandbox(c.run.sandbox)}
                style={{ border: 'none', background: 'none', padding: 0, cursor: 'pointer', marginLeft: '4px', whiteSpace: 'nowrap' }}>
                <Chip text={text} color={style.color} bg={style.bg} />
              </button>
            );
          }
          return (
            <span key={c.group} style={{ marginLeft: '4px', whiteSpace: 'nowrap' }} title={title}>
              <Chip text={c.queued && c.queued.rank ? `${text} #${c.queued.rank}` : text} color={style.color} bg={style.bg} />
            </span>
          );
        })}
        {item.cooldown && (
          <span style={{ marginLeft: '4px', whiteSpace: 'nowrap' }} title={cooldownTitle(item.cooldown)}>
            <Chip text={`cooldown until ${new Date(item.cooldown.until).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' })}`}
              color={RUN_STYLE.queued.color} bg={RUN_STYLE.queued.bg} />
          </span>
        )}
        {item.failure && (
          <span style={{ marginLeft: '4px', whiteSpace: 'nowrap' }} title={failureTitle(item.failure)}>
            <Chip text={failureText(item.failure)} color={RUN_STYLE.failed.color} bg={RUN_STYLE.failed.bg} />
          </span>
        )}
        {podTrouble && !failedChip && (
          <span style={{ marginLeft: '4px' }} title={item.reason}>
            <Chip text={`${podTrouble.pod} !`} color={RUN_STYLE.failed.color} bg={RUN_STYLE.failed.bg} />
          </span>
        )}
      </td>
      <td style={{ padding: '6px 8px', textAlign: 'right', whiteSpace: 'nowrap' }}>
        {needsHuman && onRetry && (
          <button type="button" className="btn btn-sm btn-secondary"
            title={`Clear the requires-human mark on ${needsHuman.name}: the overseer queues this again`}
            onClick={() => {
              if (window.confirm(`Clear the requires-human mark on ${needsHuman.name}?\n\n${needsHuman.requiresHuman}\n\nThe overseer will try this again.`)) onRetry(needsHuman.name);
            }}>Clear &amp; retry</button>
        )}
      </td>
    </tr>
  );
}

// rulesByRule are the rules summary's rules, those with tasks dropped at
// start first (most first), then the rest by name.
export function rulesByRule(summary) {
  return Object.entries((summary && summary.byRule) || {})
    .sort(([a, ra], [b, rb]) => ((rb.dropped || 0) - (ra.dropped || 0)) || a.localeCompare(b));
}

// ruleTallyText is one rule's tally: "ci run 12 · give-up 1 · dropped 2".
export function ruleTallyText(rule, t) {
  const parts = [];
  if (t.run) parts.push(`run ${t.run}`);
  if (t.giveUp) parts.push(`give-up ${t.giveUp}`);
  if (t.dropped) parts.push(`dropped ${t.dropped}`);
  return `${rule} ${parts.join(' · ') || '0'}`;
}

// decisionsText is one side of an inconsistency: its decisions, or "nothing".
export function decisionsText(decided) {
  return (decided && decided.length) ? decided.join(', ') : 'nothing';
}

// RulesBar is what the watch rules decided since the overseer started: each
// rule's runs, give-ups and tasks dropped at start (the last reason in its
// title), and the consistency check - the engine deciding again on each
// recorded decision - with the latest inconsistencies, each of which
// filters the rows to its item.
export function RulesBar({ summary, onShowItem }) {
  const [open, setOpen] = useState(false);
  if (!summary) return null;
  const inconsistent = summary.records - summary.consistent;
  const since = summary.since ? new Date(summary.since).toLocaleString() : '';
  return (
    <div className="rules-bar" style={{ fontSize: 'small', marginBottom: '8px', color: 'var(--text-secondary)' }}>
      <div style={{ display: 'flex', gap: '6px', alignItems: 'center', flexWrap: 'wrap' }}>
        <span title={`What the watch rules decided, counted since ${since}`}>
          <strong style={{ color: 'var(--text-primary)' }}>Rules</strong> since {since}
        </span>
        {rulesByRule(summary).map(([rule, t]) => (
          <span key={rule} title={t.dropped ? `${rule}: last dropped at start: ${t.lastDrop || '?'}` : rule}>
            <Chip text={ruleTallyText(rule, t)}
              color={t.dropped ? RUN_STYLE.pending.color : RUN_STYLE.queued.color}
              bg={t.dropped ? RUN_STYLE.pending.bg : RUN_STYLE.queued.bg} />
          </span>
        ))}
        <span title="Each recorded decision decided again by the engine on its recorded facts; a gate outside the rules is counted, not checked">
          consistent {summary.consistent}/{summary.records}
          {inconsistent ? <span style={{ color: RUN_STYLE.failed.color, fontWeight: 700 }}> · {inconsistent} not</span> : null}
          {summary.gated ? ` · ${summary.gated} held back by gates` : ''}
        </span>
        {(summary.recent || []).length > 0 && (
          <button type="button" className="btn btn-sm" aria-expanded={open} onClick={() => setOpen(o => !o)}>
            {open ? 'Hide inconsistencies' : `Inconsistencies (${summary.recent.length})`}
          </button>
        )}
      </div>
      {open && (
        <ul style={{ margin: '6px 0 0', paddingLeft: '18px' }}>
          {summary.recent.map((d, i) => (
            <li key={`${d.kind}-${d.number}-${d.at}-${i}`}>
              <button type="button" className="btn btn-link btn-sm" style={{ padding: 0 }} onClick={() => onShowItem && onShowItem(d.number)}>
                {d.kind === 'pr' ? 'PR' : d.kind} #{d.number}
              </button>{' '}
              rules: <code>{decisionsText(d.rules)}</code>, scanner: <code>{decisionsText(d.scanner)}</code>
              {d.at ? <span> · {new Date(d.at).toLocaleTimeString()}</span> : null}
            </li>
          ))}
        </ul>
      )}
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
  const toggle = (key, def) => setOpen(o => ({ ...o, [key]: !(key in o ? o[key] : def) }));
  const retry = (sandbox) => fetch(`${apiBase}/sandbox-card/${encodeURIComponent(sandbox)}/lifecycle`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ action: 'retry' }),
  })
    .then(res => (res.ok ? load() : res.text().then(t => Promise.reject(new Error(t || `HTTP ${res.status}`)))))
    .catch(err => setError(`Clear & retry ${sandbox}: ${err.message || err}`));

  return (
    <div style={{ textAlign: 'left' }}>
      <div style={{ display: 'flex', gap: '8px', alignItems: 'center', flexWrap: 'wrap', justifyContent: 'flex-end', marginBottom: '10px' }}>
        {[['both', 'Both'], ['issue', 'Issues'], ['pr', 'PRs']].map(([k, label]) => (
          <button key={k} type="button" className={`btn btn-sm ${type === k ? 'btn-primary' : 'btn-secondary'}`} onClick={() => setType(k)}>{label}</button>
        ))}
        <span style={{ width: '10px' }} />
        {[['all', 'All'], ['running', 'Running'], ['paused', 'Paused'], ['failed', 'Failed']].map(([k, label]) => (
          <button key={k} type="button" className={`btn btn-sm ${state === k ? 'btn-primary' : 'btn-secondary'}`} onClick={() => setState(k)}>{label}</button>
        ))}
        <input type="search" placeholder="Search number, title, sandbox, recipe…" value={q} onChange={e => setQ(e.target.value)}
          aria-label="Search work" style={{ marginLeft: '10px', minWidth: '260px', padding: '4px 8px' }} />
        <button type="button" className="btn btn-sm" onClick={load} title="Refresh">↻</button>
      </div>
      {error && <div style={{ color: 'var(--danger, #d33)', fontSize: 'small', marginBottom: '8px' }}>Work feed: {error}</div>}
      {work && work.queueError && (
        <div style={{ color: 'var(--text-secondary)', fontSize: 'small', marginBottom: '8px' }}>Queue not shown: {work.queueError}</div>
      )}
      {work && <RulesBar summary={work.rules} onShowItem={n => setQ(String(n))} />}
      <div className="work-card">
        <table className="work-table">
          <thead>
            <tr style={{ textAlign: 'left', borderBottom: '2px solid var(--border-color)', fontSize: 'small', color: 'var(--text-secondary)' }}>
              <th style={{ padding: '6px 4px 6px 8px', width: '1%' }}>#</th>
              <th style={{ padding: '6px 6px 6px 4px', width: '1%' }}>Age</th>
              <th style={{ padding: '6px 8px' }}>Title</th>
              <th style={{ padding: '6px 8px' }}>Agent</th>
              <th style={{ padding: '6px 8px' }}></th>
            </tr>
          </thead>
          <tbody>
            {!work && !error && (
              <tr><td colSpan="5" style={{ padding: '24px 8px', color: 'var(--text-secondary)', fontStyle: 'italic' }}>Loading {overseer}…</td></tr>
            )}
            {work && SECTIONS.map(sec => {
              const rows = byNeed(items.filter(it => it.section === sec.key));
              if (!rows.length) return null;
              const isOpen = sec.key in open ? open[sec.key] : !sec.collapsed;
              return (
                <React.Fragment key={sec.key}>
                  <tr className="work-section" aria-label={sec.label} onClick={() => toggle(sec.key, !sec.collapsed)} style={{ cursor: 'pointer' }}>
                    <td colSpan="5" style={{ padding: '6px 8px', fontSize: 'x-small', fontWeight: 700, letterSpacing: '0.04em', color: 'var(--text-primary)', backgroundColor: 'var(--bg-secondary)', textTransform: 'uppercase' }}>
                      {isOpen ? '▾' : '▸'} {sec.label} ({rows.length})
                    </td>
                  </tr>
                  {isOpen && rows.map(it => (
                    <WorkRow key={it.key} item={it} onOpenSession={setSession} onOpenSandbox={setCardSandbox} onRetry={retry} />
                  ))}
                </React.Fragment>
              );
            })}
            {work && !items.length && (
              <tr><td colSpan="5" style={{ padding: '16px 8px', color: 'var(--text-secondary)' }}>No work matches.</td></tr>
            )}
          </tbody>
        </table>
      </div>
      {session && <SessionSlideOver session={session} onClose={() => setSession(null)} apiBase={apiBase} />}
      {cardSandbox && (
        <SandboxCard name={cardSandbox} namespace={`overseer-${overseer}`} onClose={() => setCardSandbox(null)} apiBase={apiBase} />
      )}
    </div>
  );
};

export default OverseerWork;
