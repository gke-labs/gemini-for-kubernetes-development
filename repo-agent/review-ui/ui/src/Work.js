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
  'review-ready': 'Review ready',
  'review-submitted': 'Review submitted',
  'triage-ready': 'Triage ready',
};

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

function WorkRow({ item, boardName, onAction, namespace }) {
  const attention = ATTENTION_STYLE[item.attention];
  const stage = STAGE_LABEL[item.stage] || item.stage;

  // One primary action per row/stage.
  let action = null;
  if (item.type === 'issue') {
    if (['open', 'awaiting-go', 'queued'].includes(item.stage) && !item.sandbox) {
      action = { label: 'Fix', path: `issues/${item.number}/fix` };
    } else if (['fix-failed', 'fix-done', 'pr-open'].includes(item.stage)) {
      action = { label: 'Fix again', path: `issues/${item.number}/rerun` };
    }
  } else {
    if (['open', 'review-queued'].includes(item.stage) && !item.sandbox) {
      action = { label: 'Review', path: `prs/${item.number}/review` };
    } else if (item.stage === 'review-ready') {
      action = { label: 'Re-review', path: `prs/${item.number}/rerun` };
    }
  }

  return (
    <tr style={{ borderBottom: '1px solid var(--border-color)' }}>
      <td style={{ padding: '6px 8px', whiteSpace: 'nowrap' }} title={item.type === 'issue' ? 'Issue' : 'Pull request'}>
        {item.type === 'issue' ? '◉' : '⇄'} #{item.number}
      </td>
      <td style={{ padding: '6px 8px', maxWidth: '480px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
        <a href={item.htmlURL} target="_blank" rel="noopener noreferrer" title={item.title}>{item.title}</a>
        {item.prURL && item.type === 'issue' && (
          <a href={item.prURL} target="_blank" rel="noopener noreferrer" style={{ marginLeft: '8px', fontSize: 'small' }}>PR ↗</a>
        )}
        {item.draft && (
          <details style={{ marginTop: '4px' }}>
            <summary style={{ cursor: 'pointer', fontSize: 'small', color: 'var(--text-secondary)' }}>
              {item.type === 'issue' ? 'Triage suggestions' : 'Review draft'}
            </summary>
            <pre style={{
              whiteSpace: 'pre-wrap', fontSize: 'small', margin: '4px 0 0 0',
              padding: '8px', backgroundColor: 'var(--bg-secondary)',
              borderRadius: '4px', maxHeight: '260px', overflowY: 'auto',
            }}>{item.draft}</pre>
          </details>
        )}
      </td>
      <td style={{ padding: '6px 8px' }}>{stage}</td>
      <td style={{ padding: '6px 8px' }}>
        {attention && <Chip text={attention.label} color={attention.color} bg={attention.bg} />}
      </td>
      <td style={{ padding: '6px 8px', fontSize: 'small' }}>{item.claimedBy}</td>
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
      <td style={{ padding: '6px 8px', fontSize: 'small', color: 'var(--text-secondary)' }}>{ageOf(item.updatedAt)}</td>
      <td style={{ padding: '6px 8px', textAlign: 'right' }}>
        {action && (
          <button className="btn btn-sm" onClick={() => onAction(action.path, action.label)}>{action.label}</button>
        )}
      </td>
    </tr>
  );
}

function Work({ onBack, namespace }) {
  const [boards, setBoards] = useState([]);
  const [activeBoard, setActiveBoard] = useState('');
  const [work, setWork] = useState([]);
  const [loading, setLoading] = useState(false);
  const [addURL, setAddURL] = useState('');
  const [error, setError] = useState('');
  const [autoFix, setAutoFix] = useState(false);

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
      .then(res => res.ok ? res.json() : { autoFix: false })
      .then(data => setAutoFix(!!data.autoFix))
      .catch(() => setAutoFix(false));
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

  const handleAutoFixToggle = (enabled) => {
    setAutoFix(enabled);
    fetch(`/api/board/${activeBoard}/settings`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ autoFix: enabled }),
    })
      .then(res => { if (!res.ok) { setAutoFix(!enabled); res.text().then(t => setError(`Settings save failed: ${t}`)); } })
      .catch(err => { setAutoFix(!enabled); setError(`Settings save failed: ${err}`); });
  };

  const board = boards.find(b => b.name === activeBoard);

  return (
    <div style={{ padding: '10px 20px' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '10px', marginBottom: '10px' }}>
        <button className="btn" onClick={onBack}>← Back</button>
        <h2 style={{ margin: 0 }}>Work</h2>
        <nav className="repo-tabs" style={{ margin: 0 }}>
          {boards.map(b => (
            <button
              key={b.name}
              className={`tab-btn ${activeBoard === b.name ? 'active' : ''}`}
              onClick={() => { setActiveBoard(b.name); setWork([]); }}
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
        <div style={{ marginLeft: 'auto', display: 'flex', gap: '6px', alignItems: 'center' }}>
          <input
            type="text"
            placeholder="https://github.com/org/repo"
            value={addURL}
            onChange={e => setAddURL(e.target.value)}
            onKeyDown={e => e.key === 'Enter' && handleAddBoard()}
            style={{ padding: '6px', borderRadius: '4px', border: '1px solid var(--border-color)', width: '260px' }}
          />
          <button className="btn" onClick={handleAddBoard} disabled={loading}>Add board</button>
          {activeBoard && <button className="btn btn-delete" onClick={handleDeleteBoard} title="Remove board">✕</button>}
        </div>
      </div>

      {error && (
        <div className="warning-banner" style={{ cursor: 'pointer' }} onClick={() => setError('')} title="Dismiss">
          {error}
        </div>
      )}

      {board && (
        <div style={{ fontSize: 'small', color: 'var(--text-secondary)', marginBottom: '8px' }}>
          <a href={board.repoURL} target="_blank" rel="noopener noreferrer">{board.repoURL}</a>
          {' — '}{board.active} active, {board.needsHuman} need you
          <label style={{ marginLeft: '16px', cursor: 'pointer' }} title="With the board's auto-fix intake enabled, issues assigned to you (with the trigger label) start fixing automatically as you. Your consent, your identity, draft PRs only.">
            <input type="checkbox" checked={autoFix} onChange={e => handleAutoFixToggle(e.target.checked)} style={{ marginRight: '4px' }} />
            Auto-fix issues assigned to me
          </label>
        </div>
      )}

      {!boards.length ? (
        <p>No boards yet. Paste a repository URL above to create one.</p>
      ) : (
        <table style={{ width: '100%', borderCollapse: 'collapse' }}>
          <thead>
            <tr style={{ textAlign: 'left', borderBottom: '2px solid var(--border-color)', fontSize: 'small', color: 'var(--text-secondary)' }}>
              <th style={{ padding: '6px 8px' }}>#</th>
              <th style={{ padding: '6px 8px' }}>Title</th>
              <th style={{ padding: '6px 8px' }}>Stage</th>
              <th style={{ padding: '6px 8px' }}>Attention</th>
              <th style={{ padding: '6px 8px' }}>Claimed by</th>
              <th style={{ padding: '6px 8px' }}>Sandbox</th>
              <th style={{ padding: '6px 8px' }}>Age</th>
              <th style={{ padding: '6px 8px' }}></th>
            </tr>
          </thead>
          <tbody>
            {work.map(item => (
              <WorkRow key={`${item.type}-${item.number}`} item={item} boardName={activeBoard} onAction={handleAction} namespace={namespace} />
            ))}
            {!work.length && (
              <tr><td colSpan="8" style={{ padding: '16px 8px', color: 'var(--text-secondary)' }}>
                Nothing here — issues assigned to you, your PRs, and trigger-labeled items will appear as rows.
              </td></tr>
            )}
          </tbody>
        </table>
      )}
    </div>
  );
}

export default Work;
