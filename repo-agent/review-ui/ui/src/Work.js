import React, { useState, useEffect, useCallback, useRef } from 'react';
import claudeIcon from './claude-icon.svg';
import geminiIcon from './gemini-icon.svg';
import { Terminal as XTerm } from 'xterm';
import { FitAddon } from 'xterm-addon-fit';
import 'xterm/css/xterm.css';

// Work: the RepoBoard work queue (docs/design/repoboard.md §8).
// One high-density table per board: rows are issues/PRs merged with agent
// state, sorted by attention; one action button per row. The feed is
// live-computed by the backend — this component is a renderer plus thin
// click handlers.

// The engine that launched into the row's sandbox (stamped by the
// controller) — brand icons so an A/B board pair reads at a glance.
const ENGINE_ICON = { claude: claudeIcon, gemini: geminiIcon };
function EngineIcon({ engine, title }) {
  const src = ENGINE_ICON[engine];
  if (!src) return null;
  return (
    <img src={src} alt={engine} title={title || `engine: ${engine}`}
      style={{ width: '20px', height: '20px', verticalAlign: 'middle', marginRight: '6px' }} />
  );
}

const ATTENTION_STYLE = {
  'needs-you': { label: 'Needs you', color: '#d73a49', bg: 'rgba(215,58,73,0.12)' },
  'working':   { label: 'Agent working', color: '#b08800', bg: 'rgba(176,136,0,0.12)' },
  'waiting':   { label: 'Waiting', color: '#6a737d', bg: 'rgba(106,115,125,0.12)' },
};

// Status is the human inbox: it shows text only when a person's move (or
// wait) matters. Machine motion lives in the Agent column; resting rows
// are blank.
// One action per row: when a draft awaits the member's verdict, the whole
// row collapses to a single color-coded stage button — clicking opens the
// panel, and the verdict verbs (publish/approve/reject/…) live there,
// under the content they judge. Amber = your verdict is the bottleneck;
// purple = your saved review awaits finalize.
const STAGE_BUTTON = {
  'triage-ready': { label: 'Triage ready', color: '#b08800', bg: 'rgba(176,136,0,0.16)', title: 'Triage suggestions await your verdict — open to edit, publish, or reject' },
  'plan-ready': { label: 'Plan ready', color: '#b08800', bg: 'rgba(176,136,0,0.16)', title: 'The plan awaits your verdict — open to refine, approve & fix, or reject' },
  'review-pending': { label: 'Review ready', color: '#8250df', bg: 'rgba(130,80,223,0.14)', title: 'Your draft review is saved on GitHub — open to finalize or abandon' },
};

// Agent-column wording for stages the machine owns (covers the mailbox
// window before any sandbox exists) — plus run outcomes: a failure is a
// fact about the run, not a work-state, so it lives here in red while the
// launch verbs return on the right (running the verb again IS the retry).
const AGENT_STAGE = {
  'fix-starting': 'starting',
  'review-starting': 'starting',
  'fixing': 'fixing',
  'reviewing': 'reviewing',
  'triaging': 'triaging',
  'planning': 'planning',
  'queued': 'queued',
  'iterating': 'iterating',
  'addressing': 'addressing comments',
  'investigating': 'investigating CI',
  'iterating-failed': 'iterate failed !',
  'addressing-failed': 'address failed !',
  'investigating-failed': 'investigate failed !',
  'plan-failed': 'plan failed !',
  'fix-failed': 'fix failed !',
  'review-failed': 'review failed !',
  'fix-done': 'done — no PR',
};
const AGENT_STYLE = {
  'iterating-failed': { color: 'var(--danger, #d33)', bg: 'rgba(221,51,51,0.12)' },
  'addressing-failed': { color: 'var(--danger, #d33)', bg: 'rgba(221,51,51,0.12)' },
  'investigating-failed': { color: 'var(--danger, #d33)', bg: 'rgba(221,51,51,0.12)' },
  'plan-failed': { color: 'var(--danger, #d33)', bg: 'rgba(221,51,51,0.12)' },
  'fix-failed': { color: 'var(--danger, #d33)', bg: 'rgba(221,51,51,0.12)' },
  'review-failed': { color: 'var(--danger, #d33)', bg: 'rgba(221,51,51,0.12)' },
  'fix-done': { color: '#6a737d', bg: 'rgba(106,115,125,0.12)' },
};

// Done verbs turn into green receipts: the pipeline's history stays on
// the rail (Triage ✓ → Plan ✓ → Fix ✓), and clicking a receipt shows the
// artifact it produced.
const RECEIPT_STYLE = { color: '#22863a', bg: 'rgba(34,134,58,0.14)' };

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
  const [showReview, setShowReview] = useState(false);
  const [showIterate, setShowIterate] = useState(false);
  const [iterateText, setIterateText] = useState('');
  const [editingPlan, setEditingPlan] = useState(false);
  const [planText, setPlanText] = useState('');
  const [planErr, setPlanErr] = useState('');
  // The plan file in the sandbox is the source of truth (a continued chat
  // session edits it there); opening the panel re-reads it, so chat edits
  // surface without any session-end event.
  const [freshPlan, setFreshPlan] = useState(null);
  useEffect(() => { setFreshPlan(null); }, [item.plan]);
  useEffect(() => {
    if (!showPlan || item.stage !== 'plan-ready' || !item.plan) return;
    fetch(`/api/board/${boardName}/issues/${item.number}/plan-refresh`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}',
    }).then(res => (res.ok ? res.json() : null)).then(data => {
      if (data && data.plan) setFreshPlan(data.plan);
      if (data && data.changed && onRefresh) onRefresh();
    }).catch(() => { /* the cached draft still shows */ });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [showPlan]);
  const planShown = freshPlan || item.plan;
  const planPost = (path, body, label) => {
    fetch(`/api/board/${boardName}/issues/${item.number}/${path}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body || {}),
    }).then(async res => {
      if (res.ok) {
        setPlanErr('');
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

  const group = groupOf(item);

  // One-action rail (chip + verbs share the last column). A verdict stage
  // collapses the row to its single stage button — the verdict verbs live
  // in the panel it opens, under the content they judge. A failed run is
  // an Agent-column fact, and the launch verb returns: the verb IS the
  // retry. Green receipts carry the pipeline history (Triage ✓ → Plan ✓ →
  // Fix ✓); clicking one shows the artifact it produced.
  const prNumFromURL = (u) => {
    const m = (u || '').match(/\/pull\/(\d+)/);
    return m ? m[1] : null;
  };
  const stageBtn = STAGE_BUTTON[item.stage];
  const actions = [];
  if (!stageBtn && item.type === 'issue') {
    if (['untriaged', 'open', 'triaged', 'plan-failed'].includes(item.stage)) {
      // Triage is draft-only (discovery identity) — available even on
      // read-only boards; not re-offered once done (the receipt stands).
      if (!item.draft && !item.triagePublished && ['untriaged', 'open'].includes(item.stage)) {
        actions.push({ label: 'Triage', path: `issues/${item.number}/triage`, title: 'Run the triage agent for this issue — suggestions appear on the board, nothing is written to GitHub' });
      }
      // Fixing is fork-based (no push rights needed); Plan writes nothing
      // to GitHub until approved. On plan-failed these ARE the retry.
      actions.push({ label: 'Plan', path: `issues/${item.number}/plan`, title: 'Agent drafts an implementation plan for you to refine and approve — nothing is written to GitHub until you approve' });
      actions.push({ label: 'Fix', path: `issues/${item.number}/fix` });
    } else if (item.stage === 'fix-failed' || item.stage === 'fix-done') {
      // Nothing shipped (failed, or completed without a PR): Fix relaunches
      // in the same sandbox. The Agent chip explains why it stopped.
      actions.push({ label: 'Fix', path: `issues/${item.number}/rerun`, title: 'Run the fix again in the same sandbox' });
    } else if (item.stage === 'pr-open') {
      const prNum = prNumFromURL(item.prURL);
      if (prNum) {
        actions.push({ label: 'Promote PR', path: `prs/${prNum}/promote`, title: 'Mark the draft PR ready for review' });
      }
    }
  } else if (!stageBtn && group === 'mine-pr') {
    if (item.draftPR) {
      // Promoting your own draft PR is an author right, not a repo write.
      actions.push({ label: 'Promote PR', path: `prs/${item.number}/promote`, title: 'Mark the draft PR ready for review' });
    }
    // The agent's follow-up verbs fold into one drawer — the rail keeps a
    // single owed action (Promote) plus one door. Hidden while a
    // follow-up runs; a failed one re-opens the door — the verb is the
    // retry.
    // No sandbox needed: on a hand-made PR the first verb creates one
    // (factory ensures it and checks the PR branch out).
    if (!['iterating', 'addressing', 'investigating'].includes(item.stage)) {
      actions.push({ label: showIterate ? 'Agent ▴' : 'Agent ▾', onClick: () => setShowIterate(v => !v), title: 'Send the agent to this PR — address review comments, fix CI, or iterate with an instruction. First use on a hand-made PR creates its sandbox.' });
    }
  } else if (!stageBtn) {
    // Stage is the guard — a leftover paused sandbox must not hide Review.
    // A requested review is the same verb wearing the urgency: someone is
    // waiting on you, so the button tints red instead of adding a chip.
    if (['open', 'review-requested', 'review-failed'].includes(item.stage)) {
      const requested = item.stage === 'review-requested';
      actions.push({
        label: 'Review', path: `prs/${item.number}/review`,
        tint: requested ? ATTENTION_STYLE['needs-you'] : undefined,
        title: requested
          ? 'Your review was requested — agent reviews as you and leaves a pending review on GitHub for you to finalize'
          : 'Agent reviews as you and leaves a pending review on GitHub for you to finalize',
      });
    } else if (item.stage === 'review-submitted') {
      actions.push({ label: 'Review again', path: `prs/${item.number}/review`, title: 'Run a fresh review as you — posts a new pending review on GitHub' });
    }
  }

  // Receipts: done verbs turned green. Click = view the artifact.
  const receipts = [];
  if (item.type === 'issue') {
    if (item.triagePublished || item.stage === 'triaged') {
      receipts.push({ label: 'Triage ✓', onClick: item.draft ? () => setShowDraft(v => !v) : undefined, title: 'Triage published — view the suggestions' });
    }
    if (item.planApproved && item.plan) {
      receipts.push({ label: 'Plan ✓', onClick: () => setShowPlan(v => !v), title: 'Approved plan — view it' });
    }
    if (item.stage === 'pr-open' && item.prURL) {
      receipts.push({ label: 'Fix ✓', href: item.prURL, title: 'Fix shipped — open the PR' });
    }
  } else if (item.stage === 'review-submitted') {
    receipts.push({ label: 'Review ✓', href: item.htmlURL, title: 'Your review is submitted — open the PR' });
  }

  // The stage button toggles its verdict panel.
  const stagePanelOpen = item.stage === 'triage-ready' ? showDraft
    : item.stage === 'plan-ready' ? showPlan
      : item.stage === 'review-pending' ? showReview : false;
  const toggleStagePanel = () => {
    if (item.stage === 'triage-ready') setShowDraft(v => !v);
    else if (item.stage === 'plan-ready') setShowPlan(v => !v);
    else if (item.stage === 'review-pending') setShowReview(v => !v);
  };

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
      {/* GitHub facts on the left …, repo-agent state on the right:
          Agent (machine facts, incl. run outcomes), then the one-action
          rail — receipts, the stage button, or launch verbs. */}
      <td style={{ padding: '6px 8px' }}>
        {/* The icon is the sandbox's presence on the row: click for the
            card (tasks, logs, lifecycle). Resting lifecycle (paused /
            active) is deliberately NOT a board-level chip — pause/wake
            is automatic, and every flow that needs a manual wake goes
            through the card anyway. The state lives in the tooltip. */}
        {item.sandbox && (
          <span onClick={() => onOpenSandbox && onOpenSandbox(item.sandbox.name)} style={{ cursor: 'pointer' }}>
            <EngineIcon engine={item.sandbox.engine}
              title={`${item.sandbox.name} (${item.sandbox.engine}${item.sandbox.replicas === '0' ? ', paused' : ''}) — tasks & logs`} />
          </span>
        )}
        {AGENT_STAGE[item.stage] ? (
          // A failure chip opens the error first (the "why"); the sandbox
          // card with the full logs is one more click from there.
          <span
            onClick={() => {
              if (item.error) setShowDraft(v => !v);
              else if (item.sandbox && onOpenSandbox) onOpenSandbox(item.sandbox.name);
            }}
            style={{ cursor: (item.error || item.sandbox) ? 'pointer' : 'default' }}
            title={item.error ? 'Show why the run stopped' : (item.sandbox ? `${item.sandbox.name} — tasks & logs` : 'Launching — the sandbox is not created yet')}>
            <Chip text={AGENT_STAGE[item.stage]}
              color={(AGENT_STYLE[item.stage] || { color: '#b08800' }).color}
              bg={(AGENT_STYLE[item.stage] || { bg: 'rgba(176,136,0,0.12)' }).bg} />
          </span>
        ) : null}
      </td>
      <td style={{ padding: '6px 8px', textAlign: 'right', whiteSpace: 'nowrap' }}>
        {receipts.map(r => r.href ? (
          <a key={r.label} href={r.href} target="_blank" rel="noopener noreferrer" style={{ textDecoration: 'none', marginLeft: '4px' }} title={r.title}>
            <Chip text={r.label + ' ↗'} color={RECEIPT_STYLE.color} bg={RECEIPT_STYLE.bg} />
          </a>
        ) : (
          <span key={r.label} onClick={r.onClick} style={{ cursor: r.onClick ? 'pointer' : 'default', marginLeft: '4px' }} title={r.title}>
            <Chip text={r.label} color={RECEIPT_STYLE.color} bg={RECEIPT_STYLE.bg} />
          </span>
        ))}
        {stageBtn && (
          // A real button, not a chip: the stage verdict is THE action on
          // the row, so it must look pressable — chips are for facts. The
          // tint keeps the urgency grammar (amber = your verdict blocks,
          // purple = finalize your review).
          <button className="btn btn-sm" onClick={toggleStagePanel} title={stageBtn.title}
            style={{ marginLeft: '4px', color: stageBtn.color, backgroundColor: stageBtn.bg, borderColor: stageBtn.color, fontWeight: 600 }}>
            {stageBtn.label + (stagePanelOpen ? ' ▴' : ' ▾')}
          </button>
        )}
        {item.draft && !item.triagePublished && !['triage-ready', 'triaged'].includes(item.stage) && (
          <button
            className="btn btn-sm"
            style={{ marginLeft: '4px' }}
            title="Show the agent's suggestions"
            onClick={() => setShowDraft(v => !v)}
          >{showDraft ? 'Hide suggestions' : 'Suggestions'}</button>
        )}
        {actions.map(a => a.onClick ? (
          <button key={a.label} className="btn btn-sm" style={{ marginLeft: '4px' }} title={a.title} onClick={a.onClick}>{a.label}</button>
        ) : a.href ? (
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
            style={a.tint
              ? { marginLeft: '4px', color: a.tint.color, backgroundColor: a.tint.bg, borderColor: a.tint.color, fontWeight: 600 }
              : { marginLeft: '4px' }}
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
        <td colSpan="5" style={{ padding: '0 8px 10px 8px' }}>
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
                  {item.stage === 'triage-ready' && !readOnly && (
                    <button className="btn btn-sm" style={{ marginLeft: '4px' }}
                      title="Applies suggested labels and posts the assessment comment under your identity"
                      onClick={() => {
                        if (!window.confirm(`Apply the suggested labels and post the triage comment on issue #${item.number} as you?`)) return;
                        onAction(`issues/${item.number}/publish-triage`, 'Publish triage');
                      }}>Publish</button>
                  )}
                  {item.stage === 'triage-ready' && (
                    <button className="btn btn-sm" style={{ marginLeft: '4px' }}
                      title="Discards the draft and resets the row — auto-triage will not redo it; a fresh Triage click will"
                      onClick={() => {
                        if (!window.confirm(`Discard the triage suggestions for issue #${item.number}?`)) return;
                        onAction(`issues/${item.number}/triage-reject`, 'Reject triage');
                      }}>Reject</button>
                  )}
                </div>
              )}
            </div>
          )}
        </td>
      </tr>
    )}
    {showPlan && item.plan && (
      <tr>
        <td colSpan="5" style={{ padding: '0 8px 10px 8px' }}>
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
              }}>{planShown}</pre>
              {item.stage === 'plan-ready' && (
                <div style={{ marginTop: '6px' }}>
                  {planErr && (
                    <div style={{
                      fontSize: 'small', marginBottom: '4px', padding: '6px 10px', borderRadius: '6px',
                      backgroundColor: 'color-mix(in srgb, var(--danger, #d33) 10%, transparent)',
                      textAlign: 'left', whiteSpace: 'pre-wrap',
                    }}>{planErr}</div>
                  )}
                  <div style={{ textAlign: 'right' }}>
                    {item.sandbox && (
                      <a className="btn btn-sm" href={`#/terminal/${namespace}/${item.sandbox.name}?chat=plan`}
                        target="_blank" rel="noopener noreferrer"
                        title="Continue the planning conversation — opens a terminal tab resuming the same agent session; changes to the plan file ride into Approve & Fix"
                      >Continue session ↗</a>
                    )}
                    <button className="btn btn-sm" style={{ marginLeft: '4px' }}
                      title="Edit the plan text directly"
                      onClick={() => { setPlanText(planShown); setEditingPlan(true); setPlanErr(''); }}>Edit</button>
                    <button className="btn btn-sm" style={{ marginLeft: '4px' }}
                      title="Approves the plan and launches the fix — the plan ships in the PR description"
                      onClick={() => {
                        if (!window.confirm(`Approve this plan and launch the fix for issue #${item.number} as you?`)) return;
                        onAction(`issues/${item.number}/plan-approve`, 'Approve & Fix');
                      }}>Approve & Fix</button>
                    <button className="btn btn-sm" style={{ marginLeft: '4px' }}
                      title="Discards this plan draft"
                      onClick={() => {
                        if (!window.confirm(`Discard the plan for issue #${item.number}?`)) return;
                        onAction(`issues/${item.number}/plan-reject`, 'Reject plan');
                      }}>Reject</button>
                  </div>
                </div>
              )}
            </div>
          )}
        </td>
      </tr>
    )}
    {showReview && item.stage === 'review-pending' && (
      <tr>
        <td colSpan="5" style={{ padding: '0 8px 10px 8px' }}>
          <div style={{
            fontSize: 'small', padding: '10px', borderRadius: '6px',
            backgroundColor: 'var(--bg-secondary)', textAlign: 'left',
            display: 'flex', alignItems: 'center', justifyContent: 'space-between',
          }}>
            <span>Your draft review is saved on GitHub (visible only to you) — finalize it there, or abandon it to start over.</span>
            <span style={{ whiteSpace: 'nowrap' }}>
              <a className="btn btn-sm" href={`${item.htmlURL}/files`} target="_blank" rel="noopener noreferrer"
                style={{ textDecoration: 'none' }} title="Open your pending review on GitHub">Finalize ↗</a>
              <button className="btn btn-sm" style={{ marginLeft: '4px' }}
                onClick={() => {
                  if (!window.confirm(`Delete your pending review on PR #${item.number}?`)) return;
                  onAction(`prs/${item.number}/abandon`, 'Abandon review');
                }}>Abandon</button>
            </span>
          </div>
        </td>
      </tr>
    )}
    {showIterate && group === 'mine-pr' && (
      <tr>
        <td colSpan="5" style={{ padding: '0 8px 10px 8px' }}>
          <div style={{ fontSize: 'small', padding: '10px', borderRadius: '6px', backgroundColor: 'var(--bg-secondary)', textAlign: 'left' }}>
            <div style={{ marginBottom: '8px', display: 'flex', gap: '6px', alignItems: 'center' }}>
              <span style={{ marginLeft: 'auto' }} />
              <button className="btn btn-sm"
                title="Agent addresses review feedback on this PR and pushes to the branch — continues the fix conversation"
                onClick={() => { setShowIterate(false); onAction(`prs/${item.number}/address-comments`, 'Address comments'); }}>Address review comments</button>
              <button className="btn btn-sm"
                title="Agent investigates failing checks and pushes a fix"
                onClick={() => { setShowIterate(false); onAction(`prs/${item.number}/investigate`, 'Fix CI'); }}>Fix failing CI</button>
              {item.sandbox && item.sandbox.autoIterate && (
                <span
                  onClick={() => {
                    fetch(`/api/board/${boardName}/prs/${item.number}/auto-iterate`, {
                      method: 'POST',
                      headers: { 'Content-Type': 'application/json' },
                      body: JSON.stringify({ mode: item.sandbox.autoIterate === 'on' ? 'off' : 'on' }),
                    }).then(res => { if (res.ok && onRefresh) onRefresh(); }).catch(() => {});
                  }}
                  style={{ cursor: 'pointer' }}
                  title={`Auto follow-up is ${item.sandbox.autoIterate}${item.sandbox.autoIterateOverridden ? ' (set for this PR)' : ' (board default)'} — click to turn ${item.sandbox.autoIterate === 'on' ? 'off' : 'on'} for this PR`}>
                  <Chip text={item.sandbox.autoIterate === 'on' ? 'auto ⏻' : 'auto ⏸'}
                    color={item.sandbox.autoIterate === 'on' ? '#22863a' : '#6a737d'}
                    bg={item.sandbox.autoIterate === 'on' ? 'rgba(34,134,58,0.14)' : 'rgba(106,115,125,0.12)'} />
                </span>
              )}
            </div>
            <textarea
              value={iterateText}
              onChange={e => setIterateText(e.target.value)}
              placeholder="Iterate with an instruction — what should the agent change on this PR? Leave empty to resolve conflicts and iterate."
              spellCheck={false}
              style={{
                width: '100%', boxSizing: 'border-box', fontFamily: 'inherit', fontSize: 'small',
                padding: '8px', backgroundColor: 'var(--bg-card)', color: 'var(--text-primary)',
                border: '1px solid var(--border-color, #444)', borderRadius: '6px', minHeight: '56px', textAlign: 'left',
              }}
            />
            <div style={{ marginTop: '6px', textAlign: 'right' }}>
              <button className="btn btn-sm"
                title="Run factory pr iterate in the PR's sandbox — pushes to the PR branch; empty instruction resolves conflicts and iterates"
                onClick={() => {
                  fetch(`/api/board/${boardName}/prs/${item.number}/iterate`, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ instruction: iterateText }),
                  }).then(res => {
                    if (res.ok) { setShowIterate(false); setIterateText(''); if (onRefresh) onRefresh(); }
                  }).catch(() => {});
                }}>Iterate</button>
            </div>
          </div>
        </td>
      </tr>
    )}
        {showDraft && item.error && (
      <tr>
        <td colSpan="5" style={{ padding: '0 8px 10px 8px' }}>
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
function SandboxCard({ name, namespace, onClose }) {
  const [card, setCard] = useState(null);
  const [err, setErr] = useState('');
  const [openTask, setOpenTask] = useState('');
  const [logText, setLogText] = useState('');
  const [follow, setFollow] = useState(false);
  const logRef = useRef(null);
  const [showTerminal, setShowTerminal] = useState(false);
  const [expanded, setExpanded] = useState(false);
  const [termStatus, setTermStatus] = useState('');
  const termHostRef = useRef(null);
  const termRef = useRef(null); // { term, fit, ws, closed }

  // The terminal: xterm ↔ ws ↔ pods/exec attaching `tmux new -As board`.
  // The session lives in tmux, so a dropped socket loses nothing — we
  // just reconnect into the same session (with backoff) until the pane
  // is closed.
  useEffect(() => {
    if (!showTerminal || !termHostRef.current) return undefined;
    const state = { closed: false, retry: 0, ws: null };
    termRef.current = state;
    const term = new XTerm({ fontSize: 12, cursorBlink: true, theme: { background: '#0d1117' } });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(termHostRef.current);
    fit.fit();
    state.term = term;

    // Refit when the card itself resizes (expand toggle, drag) — width
    // changes must reach tmux, not just window resizes.
    const hostObserver = new ResizeObserver(() => { try { fit.fit(); sendResize(); } catch (e) { /* detached */ } });
    hostObserver.observe(termHostRef.current);

    const sendResize = () => {
      if (state.ws && state.ws.readyState === 1) {
        state.ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }));
      }
    };
    const onWindowResize = () => { try { fit.fit(); sendResize(); } catch (e) { /* detached */ } };
    window.addEventListener('resize', onWindowResize);

    const connect = () => {
      if (state.closed) return;
      setTermStatus(state.retry ? `reconnecting (try ${state.retry})…` : 'connecting…');
      const proto = window.location.protocol === 'https:' ? 'wss' : 'ws';
      const ws = new WebSocket(`${proto}://${window.location.host}/api/sandbox-card/${name}/terminal`);
      ws.binaryType = 'arraybuffer';
      state.ws = ws;
      ws.onopen = () => { state.retry = 0; setTermStatus('connected'); fit.fit(); sendResize(); };
      ws.onmessage = (ev) => {
        term.write(typeof ev.data === 'string' ? ev.data : new Uint8Array(ev.data));
      };
      ws.onclose = () => {
        if (state.closed) return;
        state.retry += 1;
        const delay = Math.min(1000 * Math.pow(1.6, state.retry), 10000);
        setTermStatus(`disconnected — reconnecting in ${Math.round(delay / 1000)}s (session survives in tmux)`);
        setTimeout(connect, delay);
      };
    };
    term.onData((data) => {
      if (state.ws && state.ws.readyState === 1) state.ws.send(JSON.stringify({ type: 'input', data }));
    });
    connect();

    return () => {
      state.closed = true;
      hostObserver.disconnect();
      window.removeEventListener('resize', onWindowResize);
      if (state.ws) try { state.ws.close(); } catch (e) { /* gone */ }
      term.dispose();
      termRef.current = null;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [showTerminal, name]);

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
    }).then(res => {
      if (!res.ok) { res.text().then(t => setErr(`${action} failed: ${t}`)); return; }
      if (action === 'delete') { onClose(); return; } // the card's subject is gone
      setTimeout(load, 1500);
    });
  };

  const statusColor = { running: '#b08800', succeeded: '#22863a', failed: '#d73a49', aborted: '#6a737d' };
  return (
    <>
      <div onClick={onClose} style={{ position: 'fixed', inset: 0, backgroundColor: 'rgba(0,0,0,0.35)', zIndex: 900 }} />
      <div style={{
        position: 'fixed', top: 0, right: 0, height: '100%', width: expanded ? '80%' : 'min(640px, 55%)',
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
              <button className="btn btn-sm" onClick={() => setShowTerminal(v => { const next = !v; if (next) setExpanded(true); return next; })}
                title="Shell into the sandbox — the session lives in tmux and survives disconnects">
                {showTerminal ? 'Hide terminal' : 'Terminal'}
              </button>
            )}
            {card && !card.paused && !card.starting && (
              <button className="btn btn-sm" onClick={() => lifecycle('pause')} title="Scale the sandbox to zero (state stays on its disk)">Pause</button>
            )}
            {/* Delete is the wedge-recovery path, so it must stay
                available in exactly the states a wedge presents as:
                paused, starting-forever, or running. */}
            {card && (
              <button className="btn btn-sm"
                style={{ marginLeft: '8px', color: 'var(--danger, #d33)', borderColor: 'var(--danger, #d33)' }}
                title="Recover from a wedged sandbox by deleting it — destroys the checkout, plan drafts and approvals, review breadcrumbs, and chat sessions. The next agent action recreates it fresh."
                onClick={() => {
                  if (!window.confirm(`Delete sandbox ${name}?\n\nThis destroys its checkout, plan drafts/approvals, review breadcrumbs, and chat sessions. The next agent action recreates it fresh.`)) return;
                  lifecycle('delete');
                }}>Delete</button>
            )}
            <button className="btn btn-sm" onClick={() => setExpanded(v => !v)} title={expanded ? 'Shrink the card' : 'Expand the card to 80%'}>{expanded ? '⇥' : '⛶'}</button>
            <button className="btn btn-sm" onClick={load} title="Refresh">↻</button>
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: '4px' }}>
              <button className="btn btn-sm" onClick={onClose} title="Close (Esc)">✕</button>
              <kbd style={{ fontSize: 'x-small', color: 'var(--text-secondary)', border: '1px solid var(--border-color)', borderRadius: '3px', padding: '0 4px' }}>esc</kbd>
            </span>
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
        {showTerminal && (
          <div style={{ marginTop: '12px' }}>
            <div style={{ display: 'flex', alignItems: 'center', marginBottom: '4px' }}>
              <span style={{ fontSize: 'small', fontWeight: 700, letterSpacing: '0.04em', color: 'var(--text-secondary)' }}>TERMINAL</span>
              <span style={{ marginLeft: '8px', fontSize: 'x-small', color: 'var(--text-secondary)' }}>{termStatus}</span>
              <a href={`#/terminal/${namespace}/${name}`} target="_blank" rel="noopener noreferrer"
                className="btn btn-sm" style={{ marginLeft: 'auto', textDecoration: 'none' }}
                title="Pop out into its own window — same tmux session, real window management">↗ pop out</a>
            </div>
            <div ref={termHostRef} style={{ height: 'calc(100vh - 300px)', minHeight: '260px', backgroundColor: '#0d1117', borderRadius: '6px', padding: '4px' }} />
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

// ExplorePanel: the board's understanding surface — docs from the
// member's fork branch (git is the record; renders with the sandbox
// paused or gone), kickoff buttons for the three exploration kinds, and
// the deep-dive chat door.
function ExplorePanel({ boardName, onOpenSandbox }) {
  const [exp, setExp] = useState(null);
  const [topic, setTopic] = useState('');
  const [since, setSince] = useState('2 weeks');
  const [busy, setBusy] = useState('');

  const load = useCallback(() => {
    fetch(`/api/board/${boardName}/exploration`)
      .then(res => (res.ok ? res.json() : null))
      .then(data => setExp(data))
      .catch(() => {});
  }, [boardName]);
  // Poll while the tab is open: exploration runs take minutes (cold
  // boots clone the repo) and the docs should appear without a reload.
  useEffect(() => {
    load();
    const t = setInterval(load, 10000);
    return () => clearInterval(t);
  }, [load]);

  const kickoff = (kind, extra) => {
    setBusy(kind);
    fetch(`/api/board/${boardName}/explore`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ kind, ...extra }),
    }).then(res => {
      if (!res.ok) { setBusy(''); return; }
      // Optimistic queued state until the server reports it.
      setExp(prev => ({ ...(prev || {}), pending: kind }));
      setTimeout(load, 2000);
      setTimeout(() => setBusy(''), 2000);
    }).catch(() => setBusy(''));
  };

  const sb = exp && exp.sandbox;
  const dive = () => {
    if (!topic.trim() || busy === 'topic') return;
    kickoff('topic', { topic });
    setTopic('');
  };
  return (
    <div className="work-card" style={{ padding: '14px', textAlign: 'left', fontSize: 'small' }}>
      {/* The hero: one search-page box — ask anything, verbs underneath. */}
      <div style={{ border: '1px solid var(--border-color)', borderRadius: '10px',
        background: 'var(--bg-secondary)', padding: '10px 12px' }}>
        <textarea rows={3} value={topic} onChange={e => setTopic(e.target.value)}
          placeholder="Ask anything about this repo — a question, a subsystem, or 'compare with …'"
          onKeyDown={e => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); dive(); } }}
          style={{ width: '100%', border: 'none', outline: 'none', resize: 'none',
            background: 'transparent', color: 'var(--text-primary)',
            font: 'inherit', boxSizing: 'border-box' }} />
        <div style={{ display: 'flex', alignItems: 'center', gap: '8px', marginTop: '6px', flexWrap: 'wrap' }}>
          <button className="btn btn-sm" disabled={busy === 'onboard'}
            title="Agent reads the repo and writes overview, architecture (mermaid) and code-map docs to your fork's exploration/notes branch"
            onClick={() => kickoff('onboard')}>Generate Overview</button>
          <span style={{ display: 'inline-flex', alignItems: 'center', gap: '4px' }}>
            <button className="btn btn-sm" disabled={busy === 'activity'}
              title="Agent digests the recent window: themes, churn, notable merges, and maintainer asks (help-wanted, review-starved PRs)"
              onClick={() => kickoff('activity', { since })}>What happened</button>
            <select value={since} onChange={e => setSince(e.target.value)} style={{ padding: '3px' }}>
              <option value="2 weeks">2 weeks</option>
              <option value="1 month">1 month</option>
              <option value="3 months">3 months</option>
            </select>
          </span>
          <span style={{ flex: 1 }} />
          {exp && exp.pending && (!sb || sb.taskState !== 'Running') && (
            <Chip text={`${exp.pending} requested — preparing the sandbox…`}
              color="#b08800" bg="rgba(176,136,0,0.12)" />
          )}
          {sb && (
            <a href={`#/terminal/${exp.forkOwner}/${sb.name}?chat=explore`}
              target="_blank" rel="noopener noreferrer" style={{ textDecoration: 'none' }}
              title="Interactive exploration — ask questions, the agent updates the docs; same session across days">
              <Chip text={`explore: ${sb.taskState === 'Running' ? 'running' : 'parked'}`}
                color={sb.taskState === 'Running' ? '#b08800' : '#6a737d'}
                bg={sb.taskState === 'Running' ? 'rgba(176,136,0,0.12)' : 'var(--bg-card)'} />
            </a>
          )}
          {sb && (
            <span onClick={() => onOpenSandbox && onOpenSandbox(sb.name)}
              style={{ cursor: 'pointer', display: 'inline-flex', alignItems: 'center' }}
              title={`${sb.name} — tasks & logs`}>
              <EngineIcon engine={sb.engine} />
            </span>
          )}
          <button className="btn btn-sm" disabled={!topic.trim() || busy === 'topic'}
            onClick={dive}>Explore</button>
        </div>
      </div>
      {/* Artifacts: git is the record — rendered apart from the controls. */}
      <div style={{ marginTop: '12px', paddingTop: '10px', borderTop: '1px solid var(--border-color)' }}>
        {exp && (
          <div style={{ marginBottom: '6px' }}>
            <a href={exp.branchURL} target="_blank" rel="noopener noreferrer"
              title="Your understanding docs, rendered on GitHub">notes branch ↗</a>
          </div>
        )}
        {(!exp || (exp.docs || []).length === 0) ? (
          <div style={{ color: 'var(--text-secondary)', fontStyle: 'italic' }}>
            No exploration notes yet — Generate Overview writes the first ones to your fork.
          </div>
        ) : (
          <ul style={{ margin: 0, paddingLeft: '18px' }}>
            {exp.docs.map(d => (
              <li key={d.path} style={{ padding: '2px 0' }}>
                <a href={d.htmlURL} target="_blank" rel="noopener noreferrer">{d.name}</a>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
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
        // Identity-stable: replacing boards with an equal array re-arms
        // the poll effect and double-fetches every tick.
        setBoards(prev => (JSON.stringify(prev) === JSON.stringify(data) ? prev : data));
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
    // Polling skips hidden tabs (quota) and browsers throttle background
    // timers — so returning to the tab must refresh NOW, not at the next
    // tick: the wait reads as a frozen board.
    const onReturn = () => { if (!document.hidden) { fetchWork(); fetchBoards(); } };
    document.addEventListener('visibilitychange', onReturn);
    window.addEventListener('focus', onReturn);
    return () => {
      clearInterval(interval);
      document.removeEventListener('visibilitychange', onReturn);
      window.removeEventListener('focus', onReturn);
    };
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
          {/* Compact repo links instead of the raw URL: the repo itself,
              its architecture diagram, and the code wiki. */}
          <a href={board.repoURL} target="_blank" rel="noopener noreferrer" title={board.repoURL}>GitHub</a>
          <span style={{ margin: '0 6px' }}>·</span>
          <a href={`https://gitdiagram.com/${(board.repoURL || '').replace(/^https:\/\/github\.com\//, '').replace(/\.git$/, '')}`}
            target="_blank" rel="noopener noreferrer" title="Architecture diagram (gitdiagram.com)">GitDiagram</a>
          <span style={{ margin: '0 6px' }}>·</span>
          <a href={`https://codewiki.google/github.com/${(board.repoURL || '').replace(/^https:\/\/github\.com\//, '').replace(/\.git$/, '')}`}
            target="_blank" rel="noopener noreferrer" title="Code wiki (codewiki.google)">CodeWiki</a>
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
                    <tr><td colSpan="5" style={{ padding: '24px 8px', color: 'var(--text-secondary)', fontStyle: 'italic' }}>
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
                    <tr><td colSpan="5" style={{ padding: '16px 8px', color: 'var(--status-green)' }}>
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
              <button
                className={`group-tab ${shown === 'explore' ? 'active' : ''}`}
                title="Understanding docs for this repo — onboarding, architecture, recent activity, deep dives"
                onClick={() => setActiveGroup('explore')}
              >Explore</button>
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

            {shown === 'explore' ? (
              <ExplorePanel boardName={activeBoard} onOpenSandbox={setCardSandbox} />
            ) : (
            <div className="work-card">
              <table className="work-table">
                {header}
                <tbody>
                  {loadingWork && (
                    <tr><td colSpan="5" style={{ padding: '24px 8px', color: 'var(--text-secondary)', fontStyle: 'italic' }}>
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
                      <tr><td colSpan="5" style={{ padding: '16px 8px', color: 'var(--status-green)' }}>
                        ✓ Nothing needs you right now.
                      </td></tr>
                    ) : (
                      <tr><td colSpan="5" style={{ padding: '16px 8px', color: 'var(--text-secondary)' }}>
                        Nothing in {(GROUPS.find(g => g.key === shown) || {}).label || 'this group'} — {(GROUPS.find(g => g.key === shown) || {}).hint || ''}.
                      </td></tr>
                    )
                  )}
                </tbody>
              </table>
            </div>
            )}
          </div>
        );
      })()}

      {cardSandbox && (
        <SandboxCard name={cardSandbox} namespace={namespace} onClose={() => setCardSandbox(null)} />
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
                <label style={{ display: 'flex', flexDirection: 'column', gap: '2px' }}
                  title="Finished sandboxes hold their slot until paused after this idle window — it doubles as the churn throttle on automation. Lower it for faster turnover, raise it to keep checkouts warm.">
                  Idle minutes before pause
                  <input type="number" min="1" value={spec.idleMinutes || 60} style={{ width: '80px' }}
                    onChange={e => setSpec({ ...spec, idleMinutes: parseInt(e.target.value, 10) || 60 })} />
                </label>
                <label style={{ display: 'flex', flexDirection: 'column', gap: '2px' }}
                  title="Agent engine for this board's tasks. Per-launch: flipping it affects the next task; in-flight runs finish on the engine they started with. Claude needs an anthropic-api-key secret in your namespace.">
                  Engine
                  <select value={spec.engine || 'gemini'} onChange={e => setSpec({ ...spec, engine: e.target.value })}>
                    <option value="gemini">gemini</option>
                    <option value="claude">claude</option>
                  </select>
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
