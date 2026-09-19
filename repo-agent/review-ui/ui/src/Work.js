import React, { useState, useEffect, useCallback, useRef } from 'react';

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

// Status is the human inbox: it shows text only when a person's move (or
// wait) matters. Machine motion lives in the Agent column; resting rows
// are blank.
const STAGE_LABEL = {
  'fix-done': 'Fix done',
  'fix-failed': 'Fix failed',
  'pr-open': 'PR open',
  'review-requested': 'Review requested',
  'review-failed': 'Review failed',
  'review-pending': 'Pending on GitHub',
  'review-submitted': 'Reviewed ✓',
  'triage-ready': 'Triage ready',
  'triaged': 'Triaged ✓',
  'plan-ready': 'Plan ready',
  'plan-failed': 'Plan failed',
};

// Agent-column wording for stages where the machine has the row (covers
// the mailbox window before any sandbox exists).
const AGENT_STAGE = {
  'fix-starting': 'starting',
  'review-starting': 'starting',
  'fixing': 'fixing',
  'reviewing': 'reviewing',
  'triaging': 'triaging',
  'planning': 'planning',
  'queued': 'queued',
};

// Action-first grouping (tabs). Up Next is the default tab: every
// needs-you row across groups, uncapped — the inbox. Group tabs are the
// complete per-group views.
// Stage-specific status colors: states that read similar must not look
// identical. Requested = red (someone waits on you, via attention);
// pending = purple (your saved draft awaits your finalize); reviewed =
// green (settled, done).
const STAGE_STYLE = {
  'review-pending': { color: '#8250df', bg: 'rgba(130,80,223,0.14)' },
  'review-submitted': { color: '#22863a', bg: 'rgba(34,134,58,0.14)' },
  'triaged': { color: '#22863a', bg: 'rgba(34,134,58,0.14)' },
};

// The cross-board inbox: a synthetic board whose only view is Up Next —
// "what do I owe right now" is a question about you, not a repo.
const ALL_BOARDS = '__all__';

const UP_NEXT = 'up-next';
const GROUPS = [
  { key: UP_NEXT, label: 'Up Next', hint: 'Everything that needs you, across all groups' },
  { key: 'review', label: 'Review', hint: 'Incoming PRs to review' },
  { key: 'issues', label: 'Issues', hint: 'All open issues — yours, unclaimed, and filed by you; actions follow each row' },
  { key: 'mine-pr', label: 'My PRs', hint: 'PRs you authored — monitor and refine' },
];

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

function WorkRow({ item, boardName, onAction, onRefresh, namespace, groupTag, onGroupTagClick, onOpenSandbox, readOnly }) {
  const [showDraft, setShowDraft] = useState(false);
  const [editingDraft, setEditingDraft] = useState(false);
  const [draftText, setDraftText] = useState('');
  const [draftErr, setDraftErr] = useState('');

  // Edits are validated server-side against the publish schema; a rejected
  // save keeps the editor open with the reason.
  const saveDraft = () => {
    fetch(`/api/board/${boardName}/issues/${item.number}/draft`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ draft: draftText }),
    }).then(async res => {
      if (res.ok) {
        setEditingDraft(false);
        setDraftErr('');
        if (onRefresh) onRefresh();
      } else {
        const t = await res.text();
        let msg = t;
        try { const j = JSON.parse(t); msg = j.details || j.error || t; } catch (e) { /* raw text */ }
        setDraftErr(msg);
      }
    }).catch(err => setDraftErr(String(err)));
  };

  const [showPlan, setShowPlan] = useState(false);
  const [feedbackText, setFeedbackText] = useState('');
  const [editingPlan, setEditingPlan] = useState(false);
  const [planText, setPlanText] = useState('');
  const [planErr, setPlanErr] = useState('');
  const planPost = (path, body, label) => {
    fetch(`/api/board/${boardName}/issues/${item.number}/${path}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body || {}),
    }).then(async res => {
      if (res.ok) {
        setPlanErr('');
        if (path === 'plan-feedback') setFeedbackText('');
        if (onRefresh) onRefresh();
      } else {
        const t = await res.text();
        let msg = t;
        try { const j = JSON.parse(t); msg = j.details || j.error || t; } catch (e) { /* raw text */ }
        setPlanErr(`${label} failed: ${msg}`);
      }
    }).catch(err => setPlanErr(`${label} failed: ${err}`));
  };
  const planPut = (path, body, label, onOk) => {
    fetch(`/api/board/${boardName}/issues/${item.number}/${path}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body || {}),
    }).then(async res => {
      if (res.ok) {
        setPlanErr('');
        if (onOk) onOk();
        if (onRefresh) onRefresh();
      } else {
        const t = await res.text();
        let msg = t;
        try { const j = JSON.parse(t); msg = j.details || j.error || t; } catch (e) { /* raw */ }
        setPlanErr(`${label} failed: ${msg}`);
      }
    }).catch(err => setPlanErr(`${label} failed: ${err}`));
  };

  const attention = ATTENTION_STYLE[item.attention];
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
    if (item.stage === 'triage-ready') {
      // Publishing writes labels — the one verb that truly needs push
      // rights. Rejecting is board-local: available to everyone.
      if (!readOnly) {
        actions.push({ label: 'Publish triage', path: `issues/${item.number}/publish-triage`, confirm: `Apply the suggested labels and post the triage comment on issue #${item.number} as you?`, title: 'Applies suggested labels and posts the assessment comment under your identity' });
      }
      actions.push({ label: 'Reject triage', path: `issues/${item.number}/triage-reject`, confirm: `Discard the triage suggestions for issue #${item.number}?`, title: 'Discards the draft and resets the row — auto-triage will not redo it; a fresh Triage click will' });
    }
    if (item.stage === 'plan-ready') {
      actions.push({ label: 'Approve & Fix', path: `issues/${item.number}/plan-approve`, confirm: `Approve this plan and launch the fix for issue #${item.number} as you?`, title: 'Approves the plan and launches the fix — the plan ships in the PR description' });
      actions.push({ label: 'Reject plan', path: `issues/${item.number}/plan-reject`, confirm: `Discard the plan for issue #${item.number}?`, title: 'Discards this plan draft' });
    }
    if (['open', 'untriaged', 'triaged'].includes(item.stage)) {
      // Fixing is fork-based (factory forks, pushes to the member's fork,
      // opens the PR against upstream) — the normal external-contributor
      // path, no push rights needed. Plan writes nothing to GitHub.
      // Stage is the guard (a resting sandbox chip must not hide verbs).
      // Triage-ready deliberately isn't here: a pending draft demands its
      // verdict (Publish | Reject) before other verbs return — otherwise
      // a Plan/Fix started on top buries the draft unpublished.
      // Plan first (agent drafts, you refine and approve — the approved
      // plan launches the fix), or Fix directly.
      actions.push({ label: 'Plan', path: `issues/${item.number}/plan`, title: 'Agent drafts an implementation plan for you to refine and approve — nothing is written to GitHub until you approve' });
      actions.push({ label: 'Fix', path: `issues/${item.number}/fix` });
    } else if (item.stage === 'plan-failed') {
      actions.push({ label: 'Retry plan', path: `issues/${item.number}/plan`, title: 'Relaunch the planner' });
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
    if (item.draftPR) {
      // Promoting your own draft PR is an author right, not a repo write.
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
      // Finalize lives on the status chip (Pending on GitHub ↗).
      actions.push({ label: 'Abandon review', path: `prs/${item.number}/abandon`, confirm: `Delete your pending review on PR #${item.number}?` });
    } else if (item.stage === 'review-submitted') {
      // Submitting freed your pending-review slot; a voluntary re-run is
      // always available (re-requested reviews surface as needs-you).
      actions.push({ label: 'Review again', path: `prs/${item.number}/review`, title: 'Run a fresh review as you — posts a new pending review on GitHub' });
    } else if (item.stage === 'review-failed') {
      actions.push({ label: 'Retry', path: `prs/${item.number}/review`, title: 'Relaunch the review' });
    }
  }

  // Clicking the status shows the thing it names.
  const statusAction = (() => {
    switch (item.stage) {
      case 'triage-ready':
        return { onClick: () => setShowDraft(v => !v), title: 'Show the triage suggestions' };
      case 'triaged':
        return { onClick: () => setShowDraft(v => !v), title: 'Triage published — show the suggestions' };
      case 'plan-ready':
        return { onClick: () => setShowPlan(v => !v), title: 'Show the plan — refine, approve, or reject' };
      case 'review-pending':
        return { href: `${item.htmlURL}/files`, title: 'Open your pending review on GitHub' };
      case 'review-requested':
        return { href: item.htmlURL, title: 'Open the PR on GitHub' };
      case 'review-submitted':
        return { href: item.htmlURL, title: 'Open the PR — your review is submitted' };
      case 'pr-open':
        return item.prURL ? { href: item.prURL, title: 'Open the PR on GitHub' } : null;
      case 'review-failed':
        if (item.error) return { onClick: () => setShowDraft(v => !v), title: 'Show why the review failed' };
        return item.sandbox ? { onClick: () => onOpenSandbox && onOpenSandbox(item.sandbox.name), noArrow: true, title: 'Open the sandbox card (tasks & logs)' } : null;
      case 'fix-failed':
      case 'fix-done':
        return item.sandbox ? { onClick: () => onOpenSandbox && onOpenSandbox(item.sandbox.name), noArrow: true, title: 'Open the sandbox card (tasks & logs)' } : null;
      default:
        return null;
    }
  })();

  const statusStyle = STAGE_STYLE[item.stage] ||
    (attention ? { color: attention.color, bg: attention.bg } : { color: 'var(--text-secondary)', bg: 'var(--bg-secondary)' });

  return (
    <React.Fragment>
    <tr>
      <td className="work-num" style={{ padding: '6px 4px 6px 8px', width: '1%' }} title={item.type === 'issue' ? 'Issue' : 'Pull request'}>
        {groupTag && (
          <span onClick={onGroupTagClick} style={onGroupTagClick ? { cursor: 'pointer' } : undefined}
            title={onGroupTagClick ? `Open the ${groupTag} board` : undefined}>
            <Chip text={groupTag} color={accentOf(item)} bg={tintOf(item)} title={onGroupTagClick ? undefined : GROUPS.find(g => g.key === group)?.hint} />
          </span>
        )}
        {groupTag ? ' ' : ''}{item.type === 'issue' ? '◉' : '⇄'} #{item.number}
      </td>
      <td style={{ padding: '6px 6px 6px 4px', fontSize: 'small', color: 'var(--text-secondary)', whiteSpace: 'nowrap', width: '1%' }}>{ageOf(item.updatedAt)}</td>
      <td style={{ padding: '6px 8px', maxWidth: '480px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
        {item.assignee && (
          <span style={{ marginRight: '6px' }}>
            <Chip
              text={`⦿ ${item.assignee}`}
              color={item.assignee.startsWith(namespace) ? 'var(--group-fix)' : 'var(--group-review)'}
              bg={`color-mix(in srgb, ${item.assignee.startsWith(namespace) ? 'var(--group-fix)' : 'var(--group-review)'} 14%, transparent)`}
              title="GitHub assignee"
            />
          </span>
        )}
        {item.author && group === 'review' && (
          <span style={{ marginRight: '6px' }}>
            <Chip
              text={`⦿ ${item.author}`}
              color={item.author.startsWith(namespace) ? 'var(--group-fix)' : 'var(--group-review)'}
              bg={`color-mix(in srgb, ${item.author.startsWith(namespace) ? 'var(--group-fix)' : 'var(--group-review)'} 14%, transparent)`}
              title="PR author"
            />
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
        {STAGE_LABEL[item.stage] && (statusAction ? (
          statusAction.href ? (
            <a href={statusAction.href} target="_blank" rel="noopener noreferrer" style={{ textDecoration: 'none' }} title={statusAction.title}>
              <Chip text={STAGE_LABEL[item.stage] + ' ↗'} color={statusStyle.color} bg={statusStyle.bg} />
            </a>
          ) : (
            <span onClick={statusAction.onClick} style={{ cursor: 'pointer' }} title={statusAction.title}>
              <Chip text={STAGE_LABEL[item.stage] + (statusAction.noArrow ? '' : (showDraft ? ' ▴' : ' ▾'))} color={statusStyle.color} bg={statusStyle.bg} />
            </span>
          )
        ) : (
          <Chip
            text={STAGE_LABEL[item.stage]}
            color={statusStyle.color}
            bg={statusStyle.bg}
            title={attention ? attention.label : ''}
          />
        ))}
        {item.triagePublished && item.stage !== 'triaged' && (
          <span onClick={() => setShowDraft(v => !v)} style={{ cursor: 'pointer', marginLeft: '6px' }}
            title="Triage published — show the suggestions">
            <Chip text="Triaged ✓" color="#22863a" bg="rgba(34,134,58,0.14)" />
          </span>
        )}
      </td>
      <td style={{ padding: '6px 8px' }}>
        {AGENT_STAGE[item.stage] ? (
          item.sandbox ? (
            <span onClick={() => onOpenSandbox && onOpenSandbox(item.sandbox.name)} style={{ cursor: 'pointer' }} title={`${item.sandbox.name} — tasks & logs`}>
              <Chip text={AGENT_STAGE[item.stage]} color="#b08800" bg="rgba(176,136,0,0.12)" />
            </span>
          ) : (
            <Chip text={AGENT_STAGE[item.stage]} color="#b08800" bg="rgba(176,136,0,0.12)" title="launching" />
          )
        ) : item.sandbox && (
          <span onClick={() => onOpenSandbox && onOpenSandbox(item.sandbox.name)} style={{ cursor: 'pointer' }} title={`${item.sandbox.name} — tasks & logs`}>
            <Chip
              text={item.sandbox.replicas === '0' ? 'paused' : (item.sandbox.taskState || 'active').toLowerCase()}
              color={item.sandbox.replicas === '0' ? '#6a737d' : '#22863a'}
              bg={item.sandbox.replicas === '0' ? 'rgba(106,115,125,0.12)' : 'rgba(34,134,58,0.12)'}
            />
          </span>
        )}
      </td>
      <td style={{ padding: '6px 8px', textAlign: 'right', whiteSpace: 'nowrap' }}>
        {item.draft && !item.triagePublished && !['triage-ready', 'triaged'].includes(item.stage) && (
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
          {editingDraft ? (
            <div>
              <textarea
                value={draftText}
                onChange={e => setDraftText(e.target.value)}
                spellCheck={false}
                style={{
                  width: '100%', boxSizing: 'border-box', fontFamily: 'monospace',
                  fontSize: 'small', padding: '10px', backgroundColor: 'var(--bg-secondary)',
                  color: 'var(--text-primary)', border: '1px solid var(--border-color, #444)',
                  borderRadius: '6px', minHeight: '160px', textAlign: 'left',
                }}
              />
              {draftErr && (
                <div style={{
                  fontSize: 'small', marginTop: '4px', padding: '6px 10px', borderRadius: '6px',
                  backgroundColor: 'color-mix(in srgb, var(--danger, #d33) 10%, transparent)',
                  textAlign: 'left', whiteSpace: 'pre-wrap',
                }}>{draftErr}</div>
              )}
              <div style={{ marginTop: '6px', textAlign: 'right' }}>
                <button className="btn btn-sm" onClick={saveDraft} title="Validate against the triage schema and save">Save</button>
                <button className="btn btn-sm" style={{ marginLeft: '4px' }}
                  onClick={() => { setEditingDraft(false); setDraftErr(''); }}>Cancel</button>
              </div>
            </div>
          ) : (
            <div>
              <pre style={{
                whiteSpace: 'pre-wrap', fontSize: 'small', margin: 0,
                padding: '10px', backgroundColor: 'var(--bg-secondary)',
                borderRadius: '6px', maxHeight: '300px', overflowY: 'auto',
                textAlign: 'left',
              }}>{item.draft}</pre>
              {item.type === 'issue' && (
                <div style={{ marginTop: '4px', textAlign: 'right' }}>
                  <button className="btn btn-sm" title="Edit the suggestion before publishing"
                    onClick={() => { setDraftText(item.draft); setEditingDraft(true); setDraftErr(''); }}>Edit</button>
                </div>
              )}
            </div>
          )}
        </td>
      </tr>
    )}
    {showPlan && item.plan && (
      <tr>
        <td colSpan="6" style={{ padding: '0 8px 10px 8px' }}>
          {editingPlan ? (
            <div>
              <textarea
                value={planText}
                onChange={e => setPlanText(e.target.value)}
                spellCheck={false}
                style={{
                  width: '100%', boxSizing: 'border-box', fontFamily: 'monospace',
                  fontSize: 'small', padding: '10px', backgroundColor: 'var(--bg-secondary)',
                  color: 'var(--text-primary)', border: '1px solid var(--border-color, #444)',
                  borderRadius: '6px', minHeight: '220px', textAlign: 'left',
                }}
              />
              {planErr && (
                <div style={{
                  fontSize: 'small', marginTop: '4px', padding: '6px 10px', borderRadius: '6px',
                  backgroundColor: 'color-mix(in srgb, var(--danger, #d33) 10%, transparent)',
                  textAlign: 'left', whiteSpace: 'pre-wrap',
                }}>{planErr}</div>
              )}
              <div style={{ marginTop: '6px', textAlign: 'right' }}>
                <button className="btn btn-sm" title="Save your edits as the plan"
                  onClick={() => planPut('plan-draft', { plan: planText }, 'Save plan', () => setEditingPlan(false))}>Save</button>
                <button className="btn btn-sm" style={{ marginLeft: '4px' }}
                  onClick={() => { setEditingPlan(false); setPlanErr(''); }}>Cancel</button>
              </div>
            </div>
          ) : (
            <div>
              <pre style={{
                whiteSpace: 'pre-wrap', fontSize: 'small', margin: 0,
                padding: '10px', backgroundColor: 'var(--bg-secondary)',
                borderRadius: '6px', maxHeight: '360px', overflowY: 'auto',
                textAlign: 'left',
              }}>{item.plan}</pre>
              {item.stage === 'plan-ready' && (
                <div style={{ marginTop: '6px' }}>
                  <textarea
                    value={feedbackText}
                    onChange={e => setFeedbackText(e.target.value)}
                    placeholder="Feedback for the agent — what should change in this plan?"
                    spellCheck={false}
                    style={{
                      width: '100%', boxSizing: 'border-box', fontFamily: 'inherit',
                      fontSize: 'small', padding: '8px', backgroundColor: 'var(--bg-secondary)',
                      color: 'var(--text-primary)', border: '1px solid var(--border-color, #444)',
                      borderRadius: '6px', minHeight: '60px', textAlign: 'left',
                    }}
                  />
                  {planErr && (
                    <div style={{
                      fontSize: 'small', marginTop: '4px', padding: '6px 10px', borderRadius: '6px',
                      backgroundColor: 'color-mix(in srgb, var(--danger, #d33) 10%, transparent)',
                      textAlign: 'left', whiteSpace: 'pre-wrap',
                    }}>{planErr}</div>
                  )}
                  <div style={{ marginTop: '6px', textAlign: 'right' }}>
                    <button className="btn btn-sm" disabled={!feedbackText.trim()}
                      title="Send feedback — the agent revises the plan"
                      onClick={() => planPost('plan-feedback', { feedback: feedbackText }, 'Refine')}>Refine</button>
                    <button className="btn btn-sm" style={{ marginLeft: '4px' }}
                      title="Edit the plan text directly"
                      onClick={() => { setPlanText(item.plan); setEditingPlan(true); setPlanErr(''); }}>Edit</button>
                  </div>
                </div>
              )}
            </div>
          )}
        </td>
      </tr>
    )}
    {showDraft && item.error && (
      <tr>
        <td colSpan="6" style={{ padding: '0 8px 10px 8px' }}>
          <div style={{
            fontSize: 'small', padding: '10px', borderRadius: '6px',
            backgroundColor: 'color-mix(in srgb, var(--danger, #d33) 10%, transparent)',
            color: 'var(--text-primary)', textAlign: 'left',
          }}>
            {item.error}
            {item.sandbox && (
              <button className="btn btn-sm" style={{ marginLeft: '8px' }}
                onClick={() => onOpenSandbox && onOpenSandbox(item.sandbox.name)}>agent logs</button>
            )}
          </div>
        </td>
      </tr>
    )}
    </React.Fragment>
  );
}

// SandboxCard: the half-screen overlay behind every sandbox chip — task
// history (newest first) with expandable log tails and Follow, plus
// wake/pause. Terminal and richer lifecycle land here later.
function SandboxCard({ name, onClose }) {
  const [card, setCard] = useState(null);
  const [err, setErr] = useState('');
  const [openTask, setOpenTask] = useState('');
  const [logText, setLogText] = useState('');
  const [follow, setFollow] = useState(false);
  const logRef = useRef(null);

  const load = useCallback(() => {
    fetch(`/api/sandbox-card/${name}`)
      .then(res => (res.ok ? res.json() : Promise.reject(res.statusText)))
      .then(setCard)
      .catch(e => setErr(`load failed: ${e}`));
  }, [name]);
  useEffect(() => { setCard(null); setOpenTask(''); setLogText(''); setFollow(false); load(); }, [load]);

  const loadLog = useCallback((task) => {
    fetch(`/api/sandbox-card/${name}/log?task=${encodeURIComponent(task)}`)
      .then(res => res.text())
      .then(text => {
        setLogText(text);
        if (logRef.current) logRef.current.scrollTop = logRef.current.scrollHeight;
      })
      .catch(e => setLogText(`log fetch failed: ${e}`));
  }, [name]);

  useEffect(() => {
    if (!follow || !openTask) return undefined;
    const t = setInterval(() => loadLog(openTask), 3000);
    return () => clearInterval(t);
  }, [follow, openTask, loadLog]);

  useEffect(() => {
    const onKey = (e) => { if (e.key === 'Escape') onClose(); };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  const lifecycle = (action) => {
    fetch(`/api/sandbox-card/${name}/lifecycle`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ action }),
    }).then(res => { if (!res.ok) res.text().then(t => setErr(`${action} failed: ${t}`)); else setTimeout(load, 1500); });
  };

  const statusColor = { running: '#b08800', succeeded: '#22863a', failed: '#d73a49', aborted: '#6a737d' };
  return (
    <>
      <div onClick={onClose} style={{ position: 'fixed', inset: 0, backgroundColor: 'rgba(0,0,0,0.35)', zIndex: 900 }} />
      <div style={{
        position: 'fixed', top: 0, right: 0, height: '100%', width: 'min(640px, 55%)',
        backgroundColor: 'var(--bg-card)', borderLeft: '1px solid var(--border-color)',
        boxShadow: '-6px 0 24px rgba(0,0,0,0.25)', zIndex: 901, overflowY: 'auto',
        padding: '14px 16px', textAlign: 'left',
      }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
          <strong style={{ fontSize: 'small', overflow: 'hidden', textOverflow: 'ellipsis' }}>{name}</strong>
          {card && (
            <Chip
              text={card.paused ? 'paused' : card.starting ? 'starting' : (card.taskState || 'active').toLowerCase()}
              color={card.paused ? '#6a737d' : '#22863a'}
              bg={card.paused ? 'rgba(106,115,125,0.12)' : 'rgba(34,134,58,0.12)'}
            />
          )}
          <span style={{ marginLeft: 'auto', display: 'flex', gap: '6px' }}>
            {card && card.paused && (
              <button className="btn btn-sm" onClick={() => lifecycle('wake')} title="Scale the sandbox back up to inspect tasks and logs">Wake</button>
            )}
            {card && !card.paused && !card.starting && (
              <button className="btn btn-sm" onClick={() => lifecycle('pause')} title="Scale the sandbox to zero (state stays on its disk)">Pause</button>
            )}
            <button className="btn btn-sm" onClick={load} title="Refresh">↻</button>
            <button className="btn btn-sm" onClick={onClose} title="Close (Esc)">✕</button>
          </span>
        </div>
        {err && <div style={{ color: 'var(--danger, #d33)', fontSize: 'small', marginTop: '8px' }}>{err}</div>}
        {!card && !err && <div style={{ color: 'var(--text-secondary)', fontStyle: 'italic', marginTop: '12px' }}>Loading…</div>}
        {card && card.paused && (
          <div style={{ color: 'var(--text-secondary)', fontSize: 'small', marginTop: '12px' }}>
            Sandbox is paused — its task history lives on the workspace disk. Wake it to browse tasks and logs.
          </div>
        )}
        {card && card.starting && (
          <div style={{ color: 'var(--text-secondary)', fontSize: 'small', marginTop: '12px' }}>
            Pod is starting — tasks will appear once it is ready.
          </div>
        )}
        {card && !card.paused && !card.starting && (
          <div style={{ marginTop: '12px' }}>
            <div style={{ fontSize: 'small', fontWeight: 700, letterSpacing: '0.04em', color: 'var(--text-secondary)', marginBottom: '6px' }}>
              TASKS ({card.tasks.length})
            </div>
            {!card.tasks.length && <div style={{ color: 'var(--text-secondary)', fontSize: 'small' }}>No tasks have run in this sandbox yet.</div>}
            {card.tasks.map(task => (
              <div key={task.name} style={{ borderTop: '1px solid var(--border-color)', padding: '6px 0' }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: '8px', cursor: 'pointer' }}
                  onClick={() => {
                    const next = openTask === task.name ? '' : task.name;
                    setOpenTask(next); setLogText(''); setFollow(false);
                    if (next) loadLog(next);
                  }}>
                  <span style={{ fontSize: 'small' }}>{openTask === task.name ? '▾' : '▸'} <strong>{task.type}</strong></span>
                  <span style={{ fontSize: 'x-small', color: 'var(--text-secondary)' }}>{task.startedAt}</span>
                  <Chip text={task.status + (task.exitCode && task.exitCode !== '0' ? ` (${task.exitCode})` : '')}
                    color={statusColor[task.status] || 'var(--text-secondary)'}
                    bg={`color-mix(in srgb, ${statusColor[task.status] || 'var(--text-secondary)'} 12%, transparent)`} />
                  <span style={{ marginLeft: 'auto', fontSize: 'x-small', color: 'var(--text-secondary)' }}>{Math.round(task.logBytes / 1024)}k log</span>
                </div>
                {openTask === task.name && (
                  <div style={{ marginTop: '6px' }}>
                    <div style={{ display: 'flex', gap: '8px', alignItems: 'center', marginBottom: '4px' }}>
                      <label style={{ fontSize: 'x-small', cursor: 'pointer' }}>
                        <input type="checkbox" checked={follow} onChange={e => setFollow(e.target.checked)} /> Follow
                      </label>
                      <button className="btn btn-sm" onClick={() => loadLog(task.name)}>↻</button>
                    </div>
                    <pre ref={logRef} style={{
                      whiteSpace: 'pre-wrap', fontSize: 'x-small', margin: 0, padding: '8px',
                      backgroundColor: 'var(--bg-secondary)', borderRadius: '6px',
                      maxHeight: '45vh', overflowY: 'auto', textAlign: 'left',
                    }}>{logText || '…'}</pre>
                  </div>
                )}
              </div>
            ))}
          </div>
        )}
      </div>
    </>
  );
}

function Work({ onBack, namespace }) {
  const [boards, setBoards] = useState([]);
  const [activeBoard, setActiveBoard] = useState('');
  const [work, setWork] = useState([]);
  const [loadingWork, setLoadingWork] = useState(false);
  const [syncing, setSyncing] = useState(false);
  // Guards against the stale-response race: a fetch launched for the
  // previous board must never overwrite the board you switched to.
  const activeBoardRef = useRef('');
  const [activeGroup, setActiveGroup] = useState(''); // '' = auto-pick
  const [loading, setLoading] = useState(false);
  const [addURL, setAddURL] = useState('');
  const [repoSuggestions, setRepoSuggestions] = useState([]);
  const [error, setError] = useState('');
  // View is fluid per-user UI state (localStorage, per board) — it never
  // touches the board spec, so flipping it can never change what runs.
  const defaultView = { issues: 'all', reviews: 'requested', labels: '' };
  const [view, setView] = useState(defaultView);
  const [cardSandbox, setCardSandbox] = useState(null);
  const [specOpen, setSpecOpen] = useState(false);
  const [spec, setSpec] = useState(null);

  const fetchBoards = useCallback(() => {
    fetch('/api/boards')
      .then(res => res.json())
      .then(data => {
        if (!Array.isArray(data)) return;
        setBoards(data);
        setActiveBoard(prev => prev || (data.length ? ALL_BOARDS : ''));
      })
      .catch(err => console.error('Failed to fetch boards', err));
  }, []);

  const fetchWork = useCallback(() => {
    if (!activeBoard) return;
    const board = activeBoard;
    setSyncing(true);
    const done = (rows) => {
      if (board !== activeBoardRef.current) return; // stale response
      if (Array.isArray(rows)) setWork(rows);
      setLoadingWork(false);
    };
    if (board === ALL_BOARDS) {
      // Aggregate every board's (server-cached) feed; rows are tagged
      // with their board so actions post to the right endpoints.
      Promise.all(boards.map(b =>
        fetch(`/api/board/${b.name}/work`)
          .then(res => (res.ok ? res.json() : []))
          .then(rows => (Array.isArray(rows) ? rows.map(i => ({ ...i, board: b.name })) : []))
          .catch(() => [])
      ))
        .then(all => done(all.flat()))
        .finally(() => { if (board === activeBoardRef.current) setSyncing(false); });
      return;
    }
    fetch(`/api/board/${board}/work`)
      .then(res => res.ok ? res.json() : Promise.reject(res.statusText))
      .then(done)
      .catch(err => console.error('Failed to fetch work feed', err))
      .finally(() => { if (board === activeBoardRef.current) setSyncing(false); });
  }, [activeBoard, boards]);

  useEffect(() => { fetchBoards(); }, [fetchBoards]);

  // Prefetch onboarding suggestions in the background so the add-repo box
  // has them ready by the first click (server caches per user).
  useEffect(() => {
    fetch('/api/repo-suggestions')
      .then(res => (res.ok ? res.json() : []))
      .then(data => setRepoSuggestions(Array.isArray(data) ? data : []))
      .catch(() => {});
  }, []);
  useEffect(() => {
    activeBoardRef.current = activeBoard;
    if (!activeBoard) return;
    // Board switch: clear the previous board's rows immediately and show
    // the loading state — never render another board's items.
    setWork([]);
    setLoadingWork(true);
    if (activeBoard === ALL_BOARDS) return; // no per-board view state
    try {
      const saved = JSON.parse(localStorage.getItem(`repoboard.view.${activeBoard}`));
      setView(saved ? { ...defaultView, ...saved } : defaultView);
    } catch (e) { setView(defaultView); }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeBoard]);

  const updateView = (patch) => {
    const next = { ...view, ...patch };
    setView(next);
    try { localStorage.setItem(`repoboard.view.${activeBoard}`, JSON.stringify(next)); } catch (e) { /* private mode */ }
  };
  useEffect(() => {
    fetchWork();
    const interval = setInterval(() => {
      if (!document.hidden) { fetchWork(); fetchBoards(); }
    }, 20000);
    return () => clearInterval(interval);
  }, [fetchWork, fetchBoards]);

  const handleAction = (path, label, boardName) => {
    fetch(`/api/board/${boardName || activeBoard}/${path}`, {
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
          {boards.length > 1 && (
            <button
              className={`tab-btn ${activeBoard === ALL_BOARDS ? 'active' : ''}`}
              title="Everything that needs you, across every board"
              onClick={() => { setActiveBoard(ALL_BOARDS); setWork([]); setActiveGroup(''); }}
            >
              All
              {boards.reduce((n, b) => n + (b.needsHuman || 0), 0) > 0 && (
                <span style={{
                  marginLeft: '6px', backgroundColor: '#d73a49', color: 'white',
                  borderRadius: '9px', padding: '0 6px', fontSize: 'x-small',
                }}>{boards.reduce((n, b) => n + (b.needsHuman || 0), 0)}</span>
              )}
            </button>
          )}
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
          list="repo-suggestions"
          placeholder="https://github.com/org/repo"
          title="Add a board for a repository — repos you work in are suggested"
          value={addURL}
          onChange={e => setAddURL(e.target.value)}
          onKeyDown={e => e.key === 'Enter' && handleAddBoard()}
          style={{ padding: '6px', borderRadius: '4px', border: '1px solid var(--border-color)', width: '230px' }}
        />
        <datalist id="repo-suggestions">
          {repoSuggestions
            .filter(sug => !boards.some(b => (b.repoURL || '').replace(/\.git$/, '') === sug.url))
            .map(sug => (
              <option key={sug.url} value={sug.url}>{sug.fullName}</option>
            ))}
        </datalist>
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
      ) : activeBoard === ALL_BOARDS ? (() => {
        // The cross-board inbox: needs-you rows from every board, oldest
        // demands first, actions inline (each row posts to its own
        // board). Just Up Next — a cross-repo triage queue would mix
        // hats, but an inbox of things waiting on YOU is one hat.
        const stageDeferred = i => (i.stage === 'review-requested' ? 1 : 0);
        const upNextAll = work
          .filter(i => i.attention === 'needs-you')
          .sort((x, y) => (stageDeferred(x) - stageDeferred(y)) || (x.updatedAt < y.updatedAt ? -1 : 1));
        return (
          <div>
            <div style={{ fontSize: 'small', fontWeight: 700, letterSpacing: '0.04em', color: '#d73a49', margin: '0 0 8px 2px', textAlign: 'left' }}>
              UP NEXT — across {boards.length} boards
            </div>
            <div className="work-card">
              <table className="work-table">
                <tbody>
                  {loadingWork && (
                    <tr><td colSpan="6" style={{ padding: '24px 8px', color: 'var(--text-secondary)', fontStyle: 'italic' }}>
                      Gathering your boards…
                    </td></tr>
                  )}
                  {!loadingWork && upNextAll.map(item => (
                    <WorkRow key={`${item.board}-${item.type}-${item.number}`} item={item} boardName={item.board}
                      onAction={(p, l) => handleAction(p, l, item.board)} onRefresh={fetchWork}
                      namespace={namespace} groupTag={item.board}
                      onGroupTagClick={() => { setActiveBoard(item.board); setWork([]); setActiveGroup(''); }}
                      onOpenSandbox={setCardSandbox}
                      readOnly={(boards.find(b => b.name === item.board) || {}).role === 'read-only'} />
                  ))}
                  {!loadingWork && !upNextAll.length && (
                    <tr><td colSpan="6" style={{ padding: '16px 8px', color: 'var(--status-green)' }}>
                      ✓ Nothing needs you anywhere.
                    </td></tr>
                  )}
                </tbody>
              </table>
            </div>
          </div>
        );
      })() : (() => {
        // The feed is the full universe; the view narrows it here, client
        // side. In-flight items (sandbox, agent motion) always surface —
        // tightening a filter must never hide running work.
        const inFlight = i => !!i.sandbox ||
          ['queued', 'fix-starting', 'review-starting', 'planning', 'triaging', 'fixing', 'reviewing'].includes(i.stage);
        const labelFilters = (view.labels || '').split(',').map(v => v.trim().toLowerCase()).filter(Boolean);
        const visible = work.filter(item => {
          if (inFlight(item)) return true;
          if (labelFilters.length && !(item.labels || []).some(l => labelFilters.includes(l.toLowerCase()))) return false;
          const g = groupOf(item);
          if (g === 'issues' && view.issues === 'mine') {
            // Assignee list is viewer-first, so a simple prefix test works.
            if (!(item.assignee || '').startsWith(namespace) && item.author !== namespace) return false;
          }
          if (g === 'review' && view.reviews === 'requested' && !item.reviewRequested) return false;
          return true;
        });
        const byGroup = {};
        GROUPS.forEach(g => { byGroup[g.key] = []; });
        visible.forEach(item => { (byGroup[groupOf(item)] = byGroup[groupOf(item)] || []).push(item); });
        byGroup[UP_NEXT] = visible.filter(i => i.attention === 'needs-you');
        // Always land on Up Next: consistent muscle memory, and its empty
        // state ("nothing needs you") is the good news, not a dead end.
        const shown = activeGroup || UP_NEXT;
        const rows = byGroup[shown] || [];
        const groupLabel = { review: 'REVIEW', issues: 'ISSUE', 'mine-pr': 'MY PR' };
        const header = (
          <thead>
            <tr style={{ textAlign: 'left', borderBottom: '2px solid var(--border-color)', fontSize: 'small', color: 'var(--text-secondary)' }}>
              <th style={{ padding: '6px 4px 6px 8px', width: '1%' }}>#</th>
              <th style={{ padding: '6px 6px 6px 4px', width: '1%' }}>Age</th>
              <th style={{ padding: '6px 8px' }}>Title</th>
              <th style={{ padding: '6px 8px' }}>Status</th>
              <th style={{ padding: '6px 8px' }}>Agent</th>
              <th style={{ padding: '6px 8px' }}></th>
            </tr>
          </thead>
        );
        return (
          <div>
            <nav className="group-tabs" style={{ display: 'flex', alignItems: 'center' }}>
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
                    {g.key !== UP_NEXT && byGroup[g.key].length > 0 && (
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
              <span style={{ marginLeft: 'auto', display: 'flex', gap: '6px', alignItems: 'center', fontSize: 'small' }}>
                {shown === 'issues' && (
                  <span title="View scope — display only, never changes what runs">
                    {['all', 'mine'].map(v => (
                      <button key={v} className="btn btn-sm"
                        style={{ marginLeft: '2px', opacity: view.issues === v ? 1 : 0.5 }}
                        onClick={() => updateView({ issues: v })}
                      >{v === 'all' ? 'All' : 'Mine'}</button>
                    ))}
                  </span>
                )}
                {shown === 'review' && (
                  <span title="View scope — display only, never changes what runs">
                    {['requested', 'all'].map(v => (
                      <button key={v} className="btn btn-sm"
                        style={{ marginLeft: '2px', opacity: view.reviews === v ? 1 : 0.5 }}
                        onClick={() => updateView({ reviews: v })}
                      >{v === 'requested' ? 'Requested' : 'All'}</button>
                    ))}
                  </span>
                )}
                {syncing && !loadingWork && (
                  <span style={{ color: 'var(--text-secondary)', fontSize: 'x-small' }} title="Refreshing from the server">↻</span>
                )}
                <input
                  type="text"
                  value={view.labels}
                  placeholder="filter labels…"
                  title="Show only items with these labels (comma-separated) — display only"
                  onChange={e => updateView({ labels: e.target.value })}
                  style={{ width: '120px', padding: '3px 6px', borderRadius: '4px', border: '1px solid var(--border-color)', fontSize: 'small' }}
                />
              </span>
            </nav>

            <div className="work-card">
              <table className="work-table">
                {header}
                <tbody>
                  {loadingWork && (
                    <tr><td colSpan="6" style={{ padding: '24px 8px', color: 'var(--text-secondary)', fontStyle: 'italic' }}>
                      Loading {activeBoard}…
                    </td></tr>
                  )}
                  {!loadingWork && rows.map(item => (
                    <WorkRow key={`${item.type}-${item.number}`} item={item} boardName={activeBoard}
                      onOpenSandbox={setCardSandbox}
                      onAction={handleAction} onRefresh={fetchWork} namespace={namespace}
                      groupTag={shown === UP_NEXT ? groupLabel[groupOf(item)] : undefined} readOnly={readOnly} />
                  ))}
                  {!loadingWork && !rows.length && (
                    shown === UP_NEXT ? (
                      <tr><td colSpan="6" style={{ padding: '16px 8px', color: 'var(--status-green)' }}>
                        ✓ Nothing needs you right now.
                      </td></tr>
                    ) : (
                      <tr><td colSpan="6" style={{ padding: '16px 8px', color: 'var(--text-secondary)' }}>
                        Nothing in {(GROUPS.find(g => g.key === shown) || {}).label || 'this group'} — {(GROUPS.find(g => g.key === shown) || {}).hint || ''}.
                      </td></tr>
                    )
                  )}
                </tbody>
              </table>
            </div>
          </div>
        );
      })()}

      {cardSandbox && (
        <SandboxCard name={cardSandbox} onClose={() => setCardSandbox(null)} />
      )}

      {specOpen && spec && (
        <div className="modal-overlay" onClick={() => setSpecOpen(false)}>
          <div className="modal-content" onClick={e => e.stopPropagation()} style={{ maxWidth: '440px', textAlign: 'left' }}>
            <h4 style={{ marginTop: 0 }}>Board settings — {activeBoard}</h4>
            <fieldset disabled={!spec.editable} style={{ border: 'none', padding: 0, margin: 0, display: 'flex', flexDirection: 'column', gap: '10px' }}>
              <label style={{ display: 'flex', flexDirection: 'column', gap: '2px', fontSize: 'small' }}>
                Board view filter — labels <span style={{ color: 'var(--text-secondary)' }}>(view only: narrows what the board shows, never what runs; the table's filter box narrows further, temporarily)</span>
                <input
                  type="text"
                  value={(spec.viewLabels || []).join(', ')}
                  onChange={e => setSpec({ ...spec, viewLabels: e.target.value.split(',').map(v => v.trim()).filter(Boolean) })}
                />
              </label>
              <hr style={{ border: 'none', borderTop: '1px solid var(--border-color)', margin: '4px 0' }} />
              <div style={{ fontSize: 'small', fontWeight: 600 }}>
                Automation <span style={{ fontWeight: 400, color: 'var(--text-secondary)' }}>(what runs without a click — everything is draft-shaped and recency-bounded)</span>
              </div>
              <div style={{ display: 'flex', gap: '12px', fontSize: 'small', flexWrap: 'wrap' }}>
                <label style={{ display: 'flex', flexDirection: 'column', gap: '2px' }} title="Triage drafts for recent issues: unclaimed = no assignees; all = assignment doesn't imply triaged.">
                  Auto-triage
                  <select value={spec.autoTriage || 'off'} onChange={e => setSpec({ ...spec, autoTriage: e.target.value })}>
                    <option value="off">off</option>
                    <option value="unclaimed">unclaimed issues</option>
                    <option value="all">all issues</option>
                  </select>
                </label>
                <label style={{ display: 'flex', flexDirection: 'column', gap: '2px' }} title="Draft-PR fixes for recent issues assigned to you, run as you.">
                  Auto-fix
                  <select value={spec.autoFix || 'off'} onChange={e => setSpec({ ...spec, autoFix: e.target.value })}>
                    <option value="off">off</option>
                    <option value="assigned">assigned to me</option>
                  </select>
                </label>
                <label style={{ display: 'flex', flexDirection: 'column', gap: '2px' }} title="Reviews run as you and park pending reviews on GitHub, visible only to you until you submit.">
                  Auto-review
                  <select value={spec.autoReview || 'off'} onChange={e => setSpec({ ...spec, autoReview: e.target.value })}>
                    <option value="off">off</option>
                    <option value="requested">requesting my review</option>
                    <option value="all">all open PRs</option>
                  </select>
                </label>
                <label style={{ display: 'flex', flexDirection: 'column', gap: '2px' }} title="Automation only touches items updated within this window — enabling auto on an old repo processes the live edge, not the archive.">
                  Recency window
                  <select value={String(spec.recencyDays || 7)} onChange={e => setSpec({ ...spec, recencyDays: parseInt(e.target.value, 10) || 7 })}>
                    <option value="1">1 day</option>
                    <option value="7">1 week</option>
                    <option value="30">1 month</option>
                  </select>
                </label>
              </div>
              <label style={{ display: 'flex', flexDirection: 'column', gap: '2px', fontSize: 'small' }}>
                Auto only for labels <span style={{ color: 'var(--text-secondary)' }}>(comma-separated; empty = everything in scope)</span>
                <input
                  type="text"
                  value={(spec.autoLabels || []).join(', ')}
                  onChange={e => setSpec({ ...spec, autoLabels: e.target.value.split(',').map(v => v.trim()).filter(Boolean) })}
                />
              </label>
              <label style={{ display: 'flex', flexDirection: 'column', gap: '2px', fontSize: 'small' }}>
                Never auto-touch labels <span style={{ color: 'var(--text-secondary)' }}>(comma-separated; absolute veto)</span>
                <input
                  type="text"
                  value={(spec.autoExcludeLabels || []).join(', ')}
                  onChange={e => setSpec({ ...spec, autoExcludeLabels: e.target.value.split(',').map(v => v.trim()).filter(Boolean) })}
                />
              </label>
              <hr style={{ border: 'none', borderTop: '1px solid var(--border-color)', margin: '4px 0' }} />
              <div style={{ display: 'flex', gap: '12px', fontSize: 'small' }}>
                <label style={{ display: 'flex', flexDirection: 'column', gap: '2px' }}>
                  Max active sandboxes
                  <input type="number" min="1" value={spec.maxActive || 5} style={{ width: '80px' }}
                    onChange={e => setSpec({ ...spec, maxActive: parseInt(e.target.value, 10) || 5 })} />
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
