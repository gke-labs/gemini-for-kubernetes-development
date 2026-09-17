import React, { useState, useEffect, useCallback } from 'react';

// Work: the RepoBoard work queue (docs/design/repoboard.md §8).
// One high-density table per board: rows are issues/PRs merged with agent
// state, sorted by attention; one action button per row. The feed is
// live-computed by the backend — this component is a renderer plus thin
// click handlers.

const ATTENTION_STYLE = {
  'needs-you': { label: 'Needs you', color: '#d73a49', bg: 'rgba(215,58,73,0.12)' },
  'working':   { label: 'Agent working', color: '#b08800', bg: 'rgba(176,136,0,0.12)' },
  'waiting':   { label: 'Waiting', color: '#6a737d', bg: 'rgba(106,115,125,0.12)' },
};

const STAGE_LABEL = {
  'open': 'Open',
  'queued': 'Queued',
  'awaiting-go': 'Awaiting your go',
  'fixing': 'Fixing…',
  'fix-done': 'Fix done',
  'fix-failed': 'Fix failed',
  'pr-open': 'PR open',
  'reviewing': 'Reviewing…',
  'review-queued': 'Review queued',
  'review-requested': 'Review requested',
  'needs-reviewer': 'Needs a reviewer',
  'review-starting': 'Starting…',
  'fix-starting': 'Starting…',
  'review-failed': 'Review failed',
  'review-ready': 'Review ready',
  'review-pending': 'Pending on GitHub',
  'review-submitted': 'Review submitted',
  'triage-ready': 'Triage ready',
  'triaged': 'Triaged',
  'triaging': 'Triaging…',
  'untriaged': 'Untriaged',
};

// Action-first grouping (tabs). UP NEXT pins needs-you rows across groups.
const GROUPS = [
  { key: 'review', label: 'Review', hint: 'Incoming PRs to review' },
  { key: 'issues', label: 'Issues', hint: 'All open issues — yours, unclaimed, and filed by you; actions follow each row' },
  { key: 'mine-pr', label: 'My PRs', hint: 'PRs you authored — monitor and refine' },
];
const UP_NEXT_CAP = 5;

function groupOf(item) {
  return item.group || (item.type === 'issue' ? 'issues' : 'review');
}

// Per-group accent (CSS vars so dark mode derives automatically).
const GROUP_ACCENT = {
  review: 'var(--group-review)',
  issues: 'var(--group-fix)',
  'mine-pr': 'var(--group-mine-pr)',
};

function accentOf(item) {
  return GROUP_ACCENT[groupOf(item)] || 'var(--text-secondary)';
}

function tintOf(item) {
  return `color-mix(in srgb, ${accentOf(item)} 12%, transparent)`;
}

function ageOf(ts) {
  if (!ts) return '';
  const mins = Math.floor((Date.now() - new Date(ts).getTime()) / 60000);
  if (mins < 60) return `${mins}m`;
  if (mins < 60 * 24) return `${Math.floor(mins / 60)}h`;
  return `${Math.floor(mins / (60 * 24))}d`;
}

function Chip({ text, color, bg, title }) {
  if (!text) return null;
  return (
    <span title={title} style={{
      color, backgroundColor: bg,
      padding: '2px 8px', borderRadius: '10px',
      fontSize: 'small', whiteSpace: 'nowrap',
    }}>{text}</span>
  );
}

function WorkRow({ item, boardName, onAction, namespace, groupTag, readOnly }) {
  const [showDraft, setShowDraft] = useState(false);
  const attention = ATTENTION_STYLE[item.attention];
  const stage = STAGE_LABEL[item.stage] || item.stage;
  const group = groupOf(item);

  // Row actions: kickoffs, re-runs, and the human-gated writes
  // (publish/promote/merge run under the clicker's own token — GitHub
  // enforces permissions and branch protection).
  const prNumFromURL = (u) => {
    const m = (u || '').match(/\/pull\/(\d+)/);
    return m ? m[1] : null;
  };
  // Fix and repo-write flows need push permission; on read-only boards the
  // buttons are suppressed so failure is prevented, not discovered.
  const actions = [];
  if (item.type === 'issue') {
    if (['untriaged', 'open'].includes(item.stage) && !item.draft) {
      // Triage is draft-only (discovery identity) — available even on
      // read-only boards and on issues assigned to you; runs only on this
      // click or auto-triage.
      actions.push({ label: 'Triage', path: `issues/${item.number}/triage`, title: 'Run the triage agent for this issue — suggestions appear on the board, nothing is written to GitHub' });
    }
    if (item.stage === 'triage-ready' && !readOnly) {
      actions.push({ label: 'Publish triage', path: `issues/${item.number}/publish-triage`, confirm: `Apply the suggested labels and post the triage comment on issue #${item.number} as you?`, title: 'Applies suggested labels and posts the assessment comment under your identity' });
    }
    if (readOnly) {
      // No fix pipeline without push: the agent's PR could not land.
    } else if (['open', 'untriaged', 'triage-ready', 'triaged'].includes(item.stage) && !item.sandbox) {
      actions.push({ label: 'Fix', path: `issues/${item.number}/fix` });
    } else if (item.stage === 'fix-failed') {
      // Nothing shipped, so relaunching in the same sandbox is a clean
      // retry. Successful fixes have no re-run: candidate PRs would need
      // per-run sandboxes, which the shared fix sandbox can't provide.
      actions.push({ label: 'Retry', path: `issues/${item.number}/rerun` });
    } else if (item.stage === 'pr-open') {
      // Merging happens on GitHub — the row links to the PR.
      const prNum = prNumFromURL(item.prURL);
      if (prNum) {
        actions.push({ label: 'Promote PR', path: `prs/${prNum}/promote`, title: 'Mark the draft PR ready for review' });
      }
    }
  } else if (group === 'mine-pr') {
    // Your own PR: promote drafts, merge, or send the agent back to iterate
    // on the folded issue.
    if (!readOnly && item.draftPR) {
      // Merging happens on GitHub; promote is the one repo-write left here.
      actions.push({ label: 'Promote PR', path: `prs/${item.number}/promote`, title: 'Mark the draft PR ready for review' });
    }
  } else {
    // These stages only occur when no run is active or pending, so the
    // stage itself is the guard — a leftover paused sandbox (e.g. after
    // Abandon) must not hide the Review action.
    if (['open', 'review-queued', 'review-requested', 'needs-reviewer'].includes(item.stage)) {
      actions.push({ label: 'Review', path: `prs/${item.number}/review`, title: 'Agent reviews as you and leaves a pending review on GitHub for you to finalize' });
    } else if (item.stage === 'review-pending') {
      // GitHub allows one pending review per user: finalize it there, or
      // abandon it to start over.
      actions.push({ label: 'Finalize on GitHub ↗', href: `${item.htmlURL}/files`, title: 'Your pending review is on GitHub — edit and submit it there' });
      actions.push({ label: 'Abandon review', path: `prs/${item.number}/abandon`, confirm: `Delete your pending review on PR #${item.number}?` });
    } else if (item.stage === 'review-submitted') {
      // Submitting freed your pending-review slot; a voluntary re-run is
      // always available (re-requested reviews surface as needs-you).
      actions.push({ label: 'Review again', path: `prs/${item.number}/review`, title: 'Run a fresh review as you — posts a new pending review on GitHub' });
    } else if (item.stage === 'review-failed') {
      actions.push({ label: 'Retry', path: `prs/${item.number}/review`, title: 'Relaunch the review' });
    }
  }

  return (
    <React.Fragment>
    <tr>
      <td className="work-num" style={{ padding: '6px 8px' }} title={item.type === 'issue' ? 'Issue' : 'Pull request'}>
        {groupTag && (
          <Chip text={groupTag} color={accentOf(item)} bg={tintOf(item)} title={GROUPS.find(g => g.key === group)?.hint} />
        )}
        {groupTag ? ' ' : ''}{item.type === 'issue' ? '◉' : '⇄'} #{item.number}
      </td>
      <td style={{ padding: '6px 8px', fontSize: 'small', color: 'var(--text-secondary)', whiteSpace: 'nowrap' }}>{ageOf(item.updatedAt)}</td>
      <td style={{ padding: '6px 8px', maxWidth: '480px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
        {item.assignee && (
          <span style={{ marginRight: '6px' }}>
            <Chip text={`⦿ ${item.assignee}`} color="var(--text-secondary)" bg="var(--bg-secondary)" title="GitHub assignee" />
          </span>
        )}
        <a href={item.htmlURL} target="_blank" rel="noopener noreferrer" title={item.title}>{item.title}</a>
        {item.prURL && item.type === 'issue' && (
          <a href={item.prURL} target="_blank" rel="noopener noreferrer" style={{ marginLeft: '8px', fontSize: 'small' }}>PR ↗</a>
        )}
        {(item.fixes || []).map(n => (
          <a key={n} href={item.htmlURL.replace(/\/pull\/\d+.*/, `/issues/${n}`)} target="_blank" rel="noopener noreferrer"
            style={{ marginLeft: '8px', fontSize: 'small', color: 'var(--text-secondary)' }}
            title="Issue folded into this PR — the PR is the focus now">↳ fixes #{n}</a>
        ))}
        {(item.labels || []).slice(0, 4).map(l => (
          <span key={l} style={{ marginLeft: '6px' }}>
            <Chip text={l} color="var(--text-secondary)" bg="var(--bg-secondary)" title="GitHub label" />
          </span>
        ))}
        {(item.labels || []).length > 4 && (
          <span style={{ marginLeft: '4px', fontSize: 'x-small', color: 'var(--text-muted)' }}>+{item.labels.length - 4}</span>
        )}
      </td>
      {/* GitHub facts on the left …, repo-agent state on the right. */}
      <td style={{ padding: '6px 8px' }}>
        <Chip
          text={stage}
          color={attention ? attention.color : 'var(--text-secondary)'}
          bg={attention ? attention.bg : 'var(--bg-secondary)'}
          title={attention ? attention.label : ''}
        />
      </td>
      <td style={{ padding: '6px 8px' }}>
        {item.sandbox && (
          <a href={`/sandbox/${namespace}/${item.sandbox.name}/`} target="_blank" rel="noopener noreferrer">
            <Chip
              text={item.sandbox.replicas === '0' ? 'paused' : (item.sandbox.taskState || 'active').toLowerCase()}
              color={item.sandbox.replicas === '0' ? '#6a737d' : '#22863a'}
              bg={item.sandbox.replicas === '0' ? 'rgba(106,115,125,0.12)' : 'rgba(34,134,58,0.12)'}
              title={item.sandbox.name}
            />
          </a>
        )}
      </td>
      <td style={{ padding: '6px 8px', textAlign: 'right', whiteSpace: 'nowrap' }}>
        {item.draft && (
          <button
            className="btn btn-sm"
            style={{ marginLeft: '4px' }}
            title="Show the agent's suggestions"
            onClick={() => setShowDraft(v => !v)}
          >{showDraft ? 'Hide suggestions' : 'Suggestions'}</button>
        )}
        {actions.map(a => a.href ? (
          <a
            key={a.label}
            className="btn btn-sm"
            style={{ marginLeft: '4px', textDecoration: 'none' }}
            title={a.title}
            href={a.href}
            target="_blank"
            rel="noopener noreferrer"
          >{a.label}</a>
        ) : (
          <button
            key={a.label}
            className="btn btn-sm"
            style={{ marginLeft: '4px' }}
            title={a.title}
            onClick={() => {
              if (a.confirm && !window.confirm(a.confirm)) return;
              onAction(a.path, a.label);
            }}
          >{a.label}</button>
        ))}
      </td>
    </tr>
    {showDraft && item.draft && (
      <tr>
        <td colSpan="6" style={{ padding: '0 8px 10px 8px' }}>
          <pre style={{
            whiteSpace: 'pre-wrap', fontSize: 'small', margin: 0,
            padding: '10px', backgroundColor: 'var(--bg-secondary)',
            borderRadius: '6px', maxHeight: '300px', overflowY: 'auto',
            textAlign: 'left',
          }}>{item.draft}</pre>
        </td>
      </tr>
    )}
    </React.Fragment>
  );
}

function Work({ onBack, namespace }) {
  const [boards, setBoards] = useState([]);
  const [activeBoard, setActiveBoard] = useState('');
  const [work, setWork] = useState([]);
  const [activeGroup, setActiveGroup] = useState(''); // '' = auto-pick
  const [loading, setLoading] = useState(false);
  const [addURL, setAddURL] = useState('');
  const [error, setError] = useState('');
  const [autoFix, setAutoFix] = useState(false);
  const [autoReview, setAutoReview] = useState(false);
  const [specOpen, setSpecOpen] = useState(false);
  const [spec, setSpec] = useState(null);

  const fetchBoards = useCallback(() => {
    fetch('/api/boards')
      .then(res => res.json())
      .then(data => {
        if (!Array.isArray(data)) return;
        setBoards(data);
        setActiveBoard(prev => prev || (data[0] && data[0].name) || '');
      })
      .catch(err => console.error('Failed to fetch boards', err));
  }, []);

  const fetchWork = useCallback(() => {
    if (!activeBoard) return;
    fetch(`/api/board/${activeBoard}/work`)
      .then(res => res.ok ? res.json() : Promise.reject(res.statusText))
      .then(data => { if (Array.isArray(data)) setWork(data); })
      .catch(err => console.error('Failed to fetch work feed', err));
  }, [activeBoard]);

  useEffect(() => { fetchBoards(); }, [fetchBoards]);
  useEffect(() => {
    if (!activeBoard) return;
    fetch(`/api/board/${activeBoard}/settings`)
      .then(res => res.ok ? res.json() : { autoFix: false, autoReview: false })
      .then(data => { setAutoFix(!!data.autoFix); setAutoReview(!!data.autoReview); })
      .catch(() => { setAutoFix(false); setAutoReview(false); });
  }, [activeBoard]);
  useEffect(() => {
    fetchWork();
    const interval = setInterval(() => {
      if (!document.hidden) { fetchWork(); fetchBoards(); }
    }, 20000);
    return () => clearInterval(interval);
  }, [fetchWork, fetchBoards]);

  const handleAction = (path, label) => {
    fetch(`/api/board/${activeBoard}/${path}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({}),
    })
      .then(res => {
        if (res.ok) { fetchWork(); }
        else { res.text().then(t => setError(`${label} failed: ${t}`)); }
      })
      .catch(err => setError(`${label} failed: ${err}`));
  };

  const handleAddBoard = () => {
    if (!addURL.trim()) return;
    setLoading(true);
    fetch('/api/boards', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ repoURL: addURL.trim() }),
    })
      .then(res => {
        setLoading(false);
        if (res.ok) { setAddURL(''); setError(''); fetchBoards(); }
        else { res.text().then(t => setError(`Add board failed: ${t}`)); }
      })
      .catch(err => { setLoading(false); setError(`Add board failed: ${err}`); });
  };

  const handleDeleteBoard = () => {
    if (!activeBoard) return;
    if (!window.confirm(`Remove board "${activeBoard}"? Running sandboxes are not touched.`)) return;
    fetch(`/api/board/${activeBoard}`, { method: 'DELETE' })
      .then(res => {
        if (res.ok) { setActiveBoard(''); setWork([]); fetchBoards(); }
        else { res.text().then(t => setError(`Delete failed: ${t}`)); }
      })
      .catch(err => setError(`Delete failed: ${err}`));
  };

  // The settings PUT carries the full preference set; sending one flag
  // alone would clear the other.
  const savePrefs = (nextAutoFix, nextAutoReview, revert) => {
    fetch(`/api/board/${activeBoard}/settings`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ autoFix: nextAutoFix, autoReview: nextAutoReview }),
    })
      .then(res => { if (!res.ok) { revert(); res.text().then(t => setError(`Settings save failed: ${t}`)); } })
      .catch(err => { revert(); setError(`Settings save failed: ${err}`); });
  };

  const handleAutoFixToggle = (enabled) => {
    setAutoFix(enabled);
    savePrefs(enabled, autoReview, () => setAutoFix(!enabled));
  };

  const handleAutoReviewToggle = (enabled) => {
    setAutoReview(enabled);
    savePrefs(autoFix, enabled, () => setAutoReview(!enabled));
  };

  const openSpec = () => {
    fetch(`/api/board/${activeBoard}/spec`)
      .then(res => res.ok ? res.json() : Promise.reject(res.statusText))
      .then(data => { setSpec(data); setSpecOpen(true); })
      .catch(err => setError(`Load settings failed: ${err}`));
  };

  const saveSpec = () => {
    fetch(`/api/board/${activeBoard}/spec`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(spec),
    })
      .then(res => {
        if (res.ok) { setSpecOpen(false); fetchWork(); fetchBoards(); }
        else { res.text().then(t => setError(`Save settings failed: ${t}`)); }
      })
      .catch(err => setError(`Save settings failed: ${err}`));
  };

  const board = boards.find(b => b.name === activeBoard);
  const readOnly = !!board && board.role === 'read-only';

  return (
    <div style={{ padding: '10px 20px' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '10px', marginBottom: '10px' }}>
        {onBack && <button className="btn" onClick={onBack}>← Back</button>}
        <h2 style={{ margin: 0 }}>Work</h2>
        <nav className="repo-tabs" style={{ margin: 0 }}>
          {boards.map(b => (
            <button
              key={b.name}
              className={`tab-btn ${activeBoard === b.name ? 'active' : ''}`}
              onClick={() => { setActiveBoard(b.name); setWork([]); setActiveGroup(''); }}
            >
              {b.name}
              {b.needsHuman > 0 && (
                <span style={{
                  marginLeft: '6px', backgroundColor: '#d73a49', color: 'white',
                  borderRadius: '9px', padding: '0 6px', fontSize: 'x-small',
                }}>{b.needsHuman}</span>
              )}
            </button>
          ))}
        </nav>
        <input
          type="text"
          placeholder="https://github.com/org/repo"
          title="Add a board for a repository"
          value={addURL}
          onChange={e => setAddURL(e.target.value)}
          onKeyDown={e => e.key === 'Enter' && handleAddBoard()}
          style={{ padding: '6px', borderRadius: '4px', border: '1px solid var(--border-color)', width: '230px' }}
        />
        <button
          className="btn"
          onClick={handleAddBoard}
          disabled={loading}
          title="Add board"
          style={{ padding: '4px 10px', fontWeight: 'bold' }}
        >+</button>
      </div>

      {error && (
        <div className="warning-banner" style={{ cursor: 'pointer' }} onClick={() => setError('')} title="Dismiss">
          {error}
        </div>
      )}

      {board && (
        <div style={{ display: 'flex', alignItems: 'center', fontSize: 'small', color: 'var(--text-secondary)', marginBottom: '8px' }}>
          <a href={board.repoURL} target="_blank" rel="noopener noreferrer">{board.repoURL}</a>
          {board.role && (
            <span style={{ marginLeft: '8px' }}>
              <Chip
                text={board.role}
                color={board.role === 'maintainer' ? 'var(--group-fix)' : 'var(--group-mine-issue)'}
                bg={board.role === 'maintainer' ? 'color-mix(in srgb, var(--group-fix) 12%, transparent)' : 'var(--bg-secondary)'}
                title={board.role === 'maintainer'
                  ? 'Your token has push access — fixes, promote and merge are available.'
                  : 'Your token has no push access — reviews and triage work; fixes, promote and merge are disabled.'}
              />
            </span>
          )}
          <span style={{ margin: '0 6px' }}>—</span>{board.active} active, {board.needsHuman} need you
          <button
            className="btn btn-sm"
            onClick={openSpec}
            title="Board settings (trigger label, intake, limits, policy)"
            style={{ marginLeft: 'auto' }}
          >⚙</button>
          <button
            className="btn btn-delete btn-sm"
            onClick={handleDeleteBoard}
            title="Remove this board (running sandboxes are not touched)"
            style={{ marginLeft: '6px' }}
          >Delete</button>
        </div>
      )}

      {!boards.length ? (
        <p>No boards yet. Paste a repository URL above to create one.</p>
      ) : (() => {
        const byGroup = {};
        GROUPS.forEach(g => { byGroup[g.key] = []; });
        work.forEach(item => { (byGroup[groupOf(item)] = byGroup[groupOf(item)] || []).push(item); });
        const upNext = work.filter(i => i.attention === 'needs-you');
        const shown = activeGroup
          || (GROUPS.find(g => byGroup[g.key].some(i => i.attention === 'needs-you')) || {}).key
          || (GROUPS.find(g => byGroup[g.key].length) || {}).key
          || 'review';
        const rows = byGroup[shown] || [];
        const groupLabel = { review: 'REVIEW', issues: 'ISSUE', 'mine-pr': 'MY PR' };
        const header = (
          <thead>
            <tr style={{ textAlign: 'left', borderBottom: '2px solid var(--border-color)', fontSize: 'small', color: 'var(--text-secondary)' }}>
              <th style={{ padding: '6px 8px' }}>#</th>
              <th style={{ padding: '6px 8px' }}>Age</th>
              <th style={{ padding: '6px 8px' }}>Title</th>
              <th style={{ padding: '6px 8px' }}>Status</th>
              <th style={{ padding: '6px 8px' }}>Sandbox</th>
              <th style={{ padding: '6px 8px' }}></th>
            </tr>
          </thead>
        );
        return (
          <div>
            {upNext.length > 0 ? (
              <div className="up-next-card">
                <div className="up-next-title">
                  UP NEXT — needs you ({upNext.length})
                </div>
                <table className="work-table">
                  <tbody>
                    {upNext.slice(0, UP_NEXT_CAP).map(item => (
                      <WorkRow key={`up-${item.type}-${item.number}`} item={item} boardName={activeBoard}
                        onAction={handleAction} namespace={namespace} groupTag={groupLabel[groupOf(item)]} readOnly={readOnly} />
                    ))}
                  </tbody>
                </table>
                {upNext.length > UP_NEXT_CAP && (
                  <div style={{ fontSize: 'small', color: 'var(--text-secondary)', padding: '4px 8px' }}>
                    +{upNext.length - UP_NEXT_CAP} more need you — see the tab badges below.
                  </div>
                )}
              </div>
            ) : work.length > 0 && (
              <div style={{ fontSize: 'small', color: 'var(--status-green)', margin: '0 0 12px 2px' }}>
                ✓ Nothing needs you right now
              </div>
            )}

            <nav className="group-tabs">
              {GROUPS.map(g => {
                const needs = byGroup[g.key].filter(i => i.attention === 'needs-you').length;
                return (
                  <button
                    key={g.key}
                    className={`group-tab g-${g.key} ${shown === g.key ? 'active' : ''}`}
                    title={g.hint}
                    onClick={() => setActiveGroup(g.key)}
                  >
                    {g.label}
                    {byGroup[g.key].length > 0 && (
                      <span style={{
                        marginLeft: '6px', backgroundColor: 'var(--bg-secondary)',
                        borderRadius: '9px', padding: '0 7px', fontSize: 'x-small',
                        color: 'var(--text-secondary)',
                      }}>{byGroup[g.key].length}</span>
                    )}
                    {needs > 0 && (
                      <span style={{
                        marginLeft: '4px', backgroundColor: '#d73a49', color: 'white',
                        borderRadius: '9px', padding: '0 6px', fontSize: 'x-small',
                      }}>{needs}</span>
                    )}
                  </button>
                );
              })}
            </nav>

            <div className="work-card">
              <table className="work-table">
                {header}
                <tbody>
                  {rows.map(item => (
                    <WorkRow key={`${item.type}-${item.number}`} item={item} boardName={activeBoard} onAction={handleAction} namespace={namespace} readOnly={readOnly} />
                  ))}
                  {!rows.length && (
                    <tr><td colSpan="6" style={{ padding: '16px 8px', color: 'var(--text-secondary)' }}>
                      Nothing in {(GROUPS.find(g => g.key === shown) || {}).label || 'this group'} — {(GROUPS.find(g => g.key === shown) || {}).hint || ''}.
                    </td></tr>
                  )}
                </tbody>
              </table>
            </div>
          </div>
        );
      })()}

      {specOpen && spec && (
        <div className="modal-overlay" onClick={() => setSpecOpen(false)}>
          <div className="modal-content" onClick={e => e.stopPropagation()} style={{ maxWidth: '440px', textAlign: 'left' }}>
            <h4 style={{ marginTop: 0 }}>Board settings — {activeBoard}</h4>
            <fieldset disabled={!spec.editable} style={{ border: 'none', padding: 0, margin: 0, display: 'flex', flexDirection: 'column', gap: '10px' }}>
              <label style={{ display: 'flex', flexDirection: 'column', gap: '2px', fontSize: 'small' }}>
                Trigger label <span style={{ color: 'var(--text-secondary)' }}>(labeling an issue/PR on GitHub queues it; empty disables)</span>
                <input type="text" value={spec.triggerLabel || ''} onChange={e => setSpec({ ...spec, triggerLabel: e.target.value })} />
              </label>
              <label style={{ cursor: 'pointer', fontSize: 'small' }} title="Runs the triage agent for every open unclaimed issue (token cost scales with open issues). Off: triage runs only on your per-issue Triage click.">
                <input type="checkbox" checked={!!spec.triageIssues} onChange={e => setSpec({ ...spec, triageIssues: e.target.checked })} style={{ marginRight: '6px' }} />
                Auto-triage all open issues
              </label>
              <label style={{ cursor: 'pointer', fontSize: 'small' }} title="Reviews every open PR as you; each run parks a pending review on GitHub, visible only to you. Token cost scales with open PRs.">
                <input type="checkbox" checked={!!spec.draftReviews} onChange={e => setSpec({ ...spec, draftReviews: e.target.checked })} style={{ marginRight: '6px' }} />
                Auto-review all open PRs (as you)
              </label>
              <label style={{ display: 'flex', flexDirection: 'column', gap: '2px', fontSize: 'small' }}>
                Exclude labels <span style={{ color: 'var(--text-secondary)' }}>(comma-separated; hard veto for all agent work)</span>
                <input
                  type="text"
                  value={(spec.excludeLabels || []).join(', ')}
                  onChange={e => setSpec({ ...spec, excludeLabels: e.target.value.split(',').map(v => v.trim()).filter(Boolean) })}
                />
              </label>
              <div style={{ display: 'flex', gap: '12px', fontSize: 'small' }}>
                <label style={{ display: 'flex', flexDirection: 'column', gap: '2px' }}>
                  Max active sandboxes
                  <input type="number" min="1" value={spec.maxActive || 5} style={{ width: '80px' }}
                    onChange={e => setSpec({ ...spec, maxActive: parseInt(e.target.value, 10) || 5 })} />
                </label>
                <label style={{ display: 'flex', flexDirection: 'column', gap: '2px' }}>
                  Max per member
                  <input type="number" min="1" value={spec.maxActivePerUser || 2} style={{ width: '80px' }}
                    onChange={e => setSpec({ ...spec, maxActivePerUser: parseInt(e.target.value, 10) || 2 })} />
                </label>
              </div>
              <label style={{ cursor: 'pointer', fontSize: 'small' }} title="Follow up factory-created PRs (address review comments and failures) with factory pr watch.">
                <input type="checkbox" checked={!!spec.autoIterate} onChange={e => setSpec({ ...spec, autoIterate: e.target.checked })} style={{ marginRight: '6px' }} />
                Auto-iterate on factory PRs
              </label>
              <label style={{ cursor: 'pointer', fontSize: 'small' }} title="Agent-created PRs open as drafts; you promote them.">
                <input type="checkbox" checked={!!spec.draftPR} onChange={e => setSpec({ ...spec, draftPR: e.target.checked })} style={{ marginRight: '6px' }} />
                Open agent PRs as drafts
              </label>
              <label style={{ cursor: 'pointer', fontSize: 'small' }} title="Add an AI-assistance disclosure line to agent-created PR descriptions.">
                <input type="checkbox" checked={!!spec.disclose} onChange={e => setSpec({ ...spec, disclose: e.target.checked })} style={{ marginRight: '6px' }} />
                Disclose agent assistance in PRs
              </label>
            </fieldset>
            <hr style={{ border: 'none', borderTop: '1px solid var(--border-color)', margin: '14px 0 10px 0' }} />
            <div style={{ fontSize: 'small', fontWeight: 600, marginBottom: '6px' }}>
              My automation <span style={{ fontWeight: 400, color: 'var(--text-secondary)' }}>(only affects you; saved immediately)</span>
            </div>
            <div style={{ display: 'flex', flexDirection: 'column', gap: '8px' }}>
              {(!board || board.role !== 'read-only') && (
                <label style={{ cursor: 'pointer', fontSize: 'small' }} title="With the board's auto-fix intake enabled, issues assigned to you start fixing automatically as you. Your consent, your identity, draft PRs only.">
                  <input type="checkbox" checked={autoFix} onChange={e => handleAutoFixToggle(e.target.checked)} style={{ marginRight: '6px' }} />
                  Auto-fix issues assigned to me
                </label>
              )}
              <label style={{ cursor: 'pointer', fontSize: 'small' }} title="With the board's review intake enabled, PRs that request your review run the agent as you and park a pending review on GitHub — visible only to you until you submit it.">
                <input type="checkbox" checked={autoReview} onChange={e => handleAutoReviewToggle(e.target.checked)} style={{ marginRight: '6px' }} />
                Auto-review PRs assigned to me
              </label>
            </div>
            <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '8px', marginTop: '14px' }}>
              <button className="btn" onClick={() => setSpecOpen(false)}>Cancel</button>
              {spec.editable && <button className="btn btn-submit" onClick={saveSpec}>Save board settings</button>}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

export default Work;
