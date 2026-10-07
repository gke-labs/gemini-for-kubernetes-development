import React, { useState, useEffect, useCallback, useRef } from 'react';
import { ResearchPanel, AllResearchPanel, ResearchConversation } from './Research';
import antigravityIcon from './antigravity-icon.svg';
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
const ENGINE_ICON = { antigravity: antigravityIcon, claude: claudeIcon, gemini: geminiIcon };
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
// A row follows its runs by one set of rules, the same for every recipe
// (the API's row rules): each recipe's newest run is a chip in the Agent
// column, ‹label›: running / ready / done / failed (or starting / queued
// while a click waits for its run), opening the run's session; the rail
// offers a recipe not run yet by its label, one done or failed as
// ‹label› again, and a ready one as its draft's button — clicking opens
// the panel, and the verdict verbs (publish/approve/reject/…) live there,
// under the content they judge.
const RUN_STYLE = {
  running: { color: '#b08800', bg: 'rgba(176,136,0,0.12)' },
  starting: { color: '#b08800', bg: 'rgba(176,136,0,0.12)' },
  queued: { color: '#6a737d', bg: 'rgba(106,115,125,0.12)' },
  ready: { color: '#b08800', bg: 'rgba(176,136,0,0.16)' },
  // A pending review on GitHub with no run left: finalize or discard it there.
  pending: { color: '#b08800', bg: 'rgba(176,136,0,0.16)' },
  done: { color: '#6a737d', bg: 'rgba(106,115,125,0.12)' },
  failed: { color: 'var(--danger, #d33)', bg: 'rgba(221,51,51,0.12)' },
};
// READY_STYLE is a draft's button: amber = your verdict is the bottleneck;
// purple = your saved review awaits finalize.
const READY_STYLE = { color: '#b08800', bg: 'rgba(176,136,0,0.16)' };

// latestRuns is the newest run of each recipe on the row (its sessions
// are newest first).
function latestRuns(item) {
  const seen = new Set();
  return (item.sessions || []).filter(s => !seen.has(s.recipe) && seen.add(s.recipe));
}

// bareReviewRequest is a row that needs the member only because their
// review was requested: nothing is prepared for them yet, so it waits
// behind finished agent work in Up Next.
function bareReviewRequest(item) {
  return item.type !== 'issue' && !!item.reviewRequested && !item.reviewPending && !item.error &&
    !latestRuns(item).some(r => r.status === 'ready' || r.status === 'failed');
}

// The cross-board inbox: a synthetic board whose only view is Up Next —
// "what do I owe right now" is a question about you, not a repo.
const ALL_BOARDS = '__all__';

// How long a window nobody is using keeps polling before it gives up.
// document.hidden does not cover the case that actually spends the GitHub
// budget: a board left visible on a second monitor, unfocused and unread,
// refetching every repo you watch all day. Five minutes is long enough to
// read a row and come back to it without the board ever going quiet.
const IDLE_AFTER = 5 * 60 * 1000;
// How often the pull request rows re-read the board's runs. Runs change
// on the scale of a plan, which is minutes.
const RUN_STATE_EVERY = 2 * 60 * 1000;

const UP_NEXT = 'up-next';
const GROUPS = [
  { key: UP_NEXT, label: 'Up Next', hint: 'Everything that needs you, across all groups' },
  { key: 'issues', label: 'Issues', hint: 'All open issues — yours, unclaimed, and filed by you; actions follow each row' },
  { key: 'prs', label: 'PRs', hint: 'Open pull requests — review others\' and refine your own; actions follow each row' },
];

function groupOf(item) {
  return item.group || (item.type === 'issue' ? 'issues' : 'prs');
}

// Per-row accent (CSS vars so dark mode derives automatically): your own
// PRs keep their colour inside the one PRs group.
function accentOf(item) {
  if (groupOf(item) === 'issues') return 'var(--group-fix)';
  return item.mine ? 'var(--group-mine-pr)' : 'var(--group-review)';
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

// A pull request's run is named for the runbook and the pull request,
// so picking the same runbook again finds the same run. The -in-pod
// suffix stays last: it is what lets a run go without a GCP project.
const MAX_TARGET_RUN_NAME = 40;
const prRunName = (runbook, number) => (runbook.endsWith('-in-pod')
  ? `${runbook.slice(0, -'-in-pod'.length)}-pr${number}-in-pod`
  : `${runbook}-pr${number}`);

// PRDeploy: Deploy ▾ on a pull request row. The first pick plans a new
// run from the runbook, pinned to the pull request; picking it again
// re-plans that run, which moves the pin to the pull request's head.
// It stops at the plan — the Runs tab is where a plan is read and
// deployed.
function PRDeploy({ boardName, number, runbooks, runs, onStarted }) {
  const [busy, setBusy] = useState('');
  const [error, setError] = useState('');
  const start = (runbook) => {
    const name = prRunName(runbook, number);
    const exists = (runs || []).some(r => r.name === name);
    setBusy(name);
    setError('');
    fetch(`/api/board/${boardName}/runbook`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ mode: 'plan', name, intent: '', target: number, ...(exists ? {} : { runbook }) }),
    }).then(res => {
      if (res.ok) {
        if (onStarted) onStarted();
      } else {
        res.text().then(t => setError(`plan ${name} failed: ${t}`));
      }
    }).catch(err => setError(`plan ${name} failed: ${err}`))
      .finally(() => setBusy(''));
  };
  return (
    <div style={{ fontSize: 'small', padding: '10px', borderRadius: '6px', backgroundColor: 'var(--bg-secondary)', textAlign: 'left' }}>
      <div style={{ display: 'flex', gap: '6px', alignItems: 'center', flexWrap: 'wrap' }}>
        <span style={{ color: 'var(--text-secondary)' }}>Plan a run of PR #{number} from:</span>
        {runbooks.map(rb => {
          const name = prRunName(rb, number);
          const exists = (runs || []).some(r => r.name === name);
          const tooLong = name.length > MAX_TARGET_RUN_NAME;
          return (
            <button key={rb} className="btn btn-sm" disabled={!!busy || tooLong} onClick={() => start(rb)}
              title={tooLong ? `${name} is longer than ${MAX_TARGET_RUN_NAME} characters`
                : exists ? `Re-plan ${name} at the pull request's current head`
                  : `Plan a new run, ${name}, from ${rb}, pinned to this pull request`}>
              {busy === name ? `${rb}…` : rb}
            </button>
          );
        })}
      </div>
      {error && (
        <div style={{
          marginTop: '6px', padding: '6px 10px', borderRadius: '6px',
          backgroundColor: 'color-mix(in srgb, var(--danger, #d33) 10%, transparent)', whiteSpace: 'pre-wrap',
        }}>{error}</div>
      )}
    </div>
  );
}

// prRunChip: a run pinned to this pull request, by name and the first
// word of its newest verdict.
function prRunChip(run) {
  const word = (((run.latestReceipt || {}).verdict || '').trim().split(/\s+/)[0] || '').toLowerCase();
  const text = word ? `${run.name} · ${word}` : run.name;
  const link = (run.runbook && run.runbook.htmlURL) || run.htmlURL;
  const chip = <Chip text={text} color="var(--text-secondary)" bg="var(--bg-secondary)"
    title={`Run pinned to this pull request${run.targetSHA ? ` at ${run.targetSHA.slice(0, 7)}` : ''}`} />;
  return link ? (
    <a key={run.name} href={link} target="_blank" rel="noopener noreferrer" style={{ textDecoration: 'none', marginLeft: '6px' }}>{chip}</a>
  ) : <span key={run.name} style={{ marginLeft: '6px' }}>{chip}</span>;
}

// A draft's write is the controller's (factory apply), taken in the
// draft's session.
// POSTING_POLL_EVERY is the feed's cadence while a write stands.
const POSTING_POLL_EVERY = 3000;

// taskSessionHref is a task's agent session (plan, triage) in its own
// tab: watched while the task runs, continued after.
function taskSessionHref(session) {
  return `#/task-session/${session.sandbox}/${session.task}`;
}

// sessionClick opens a session link in the board's slide-over, unless the
// click asks for a tab of its own (a modifier, a middle click) or there is
// no slide-over to open it in.
function sessionClick(onOpenSession, session) {
  return (e) => {
    if (!onOpenSession || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    e.preventDefault();
    onOpenSession(session);
  };
}

// SessionSlideOver: a task's session beside the board, 80% of its width,
// the same view as its own tab (which its ↗ pops out to). Escape or the
// backdrop closes it; the session goes on without it.
function SessionSlideOver({ session, onClose }) {
  useEffect(() => {
    const onKey = (e) => { if (e.key === 'Escape' && !e.defaultPrevented) onClose(); };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);
  const m = session.task.match(/^recipe-([a-z0-9]+)-/);
  const title = `${m ? m[1] : session.task} · ${session.sandbox}`;
  return (
    <>
      <div onClick={onClose} style={{ position: 'fixed', inset: 0, backgroundColor: 'rgba(0,0,0,0.35)', zIndex: 900 }} />
      <div role="dialog" aria-label={title} style={{
        position: 'fixed', top: 0, right: 0, height: '100%', width: '80%',
        backgroundColor: 'var(--bg-card)', borderLeft: '1px solid var(--border-color)',
        boxShadow: '-6px 0 24px rgba(0,0,0,0.25)', zIndex: 901,
        display: 'flex', flexDirection: 'column', textAlign: 'left',
      }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: '8px', padding: '8px 12px', borderBottom: '1px solid var(--border-color)' }}>
          <strong style={{ fontSize: 'small', overflow: 'hidden', textOverflow: 'ellipsis' }}>{title}</strong>
          <span style={{ marginLeft: 'auto', display: 'flex', gap: '6px' }}>
            <a className="btn btn-sm" href={taskSessionHref(session)} target="_blank" rel="noopener noreferrer"
              onClick={onClose} title="Open this session in its own tab">Pop out ↗</a>
            <button className="btn btn-sm" onClick={onClose} aria-label="Close" title="Close (Esc)">✕</button>
          </span>
        </div>
        <div style={{ flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column' }}>
          <ResearchConversation key={`${session.sandbox}/${session.task}`} task={{ sandbox: session.sandbox, task: session.task }}
            title={title} fill />
        </div>
      </div>
    </>
  );
}

// anyPosting is whether a row has a write or a revise standing, filed on
// one of its runs.
function anyPosting(items) {
  return (items || []).some(item => item.posting);
}

function WorkRow({ item, boardName, onAction, onRefresh, namespace, groupTag, onGroupTagClick, onOpenSandbox, onOpenSession, runState, onRunStarted }) {
  // The row's runs: each recipe's newest, and what it is now — a click
  // not started yet is starting (or queued), until its run is.
  const runs = latestRuns(item);
  const runOf = name => runs.find(r => r.recipe === name);
  const statusOf = name => ((item.launching || {})[name]) || (runOf(name) || {}).status || '';
  // A failed run's session continues where it stopped.
  const failedRun = runs.find(r => statusOf(r.recipe) === 'failed');

  const [showError, setShowError] = useState(false);
  const [showDeploy, setShowDeploy] = useState(false);

  const group = groupOf(item);
  const isPR = item.type !== 'issue';

  // The rail (the last column): the recipes to launch and the row's own
  // GitHub moves; a run's history is its chip's session.
  const actions = [];
  for (const rec of item.recipes || []) {
    const status = statusOf(rec.name);
    // Running, a click starting, or a draft to judge: the chip is the
    // row's move, not another run.
    if (['running', 'starting', 'queued', 'ready'].includes(status)) continue;
    if (rec.name === 'review' && item.reviewPending) continue;
    const again = status === 'done' || status === 'failed';
    // A requested review is the same verb wearing the urgency: someone
    // is waiting on you, so the button tints red instead of adding a chip.
    const requested = rec.name === 'review' && item.reviewRequested && !again;
    actions.push({
      label: again ? `${rec.label} again` : rec.label,
      path: `${isPR ? 'prs' : 'issues'}/${item.number}/recipes/${rec.name}`,
      inputs: rec.inputs,
      tint: requested ? ATTENTION_STYLE['needs-you'] : undefined,
      title: requested ? `Your review was requested — run ${rec.label} as you` : `Run ${rec.label} as you`,
    });
  }
  if (isPR && item.mine && item.draftPR) {
    // Promoting your own draft PR is an author right, not a repo write.
    actions.push({ label: 'Promote PR', path: `prs/${item.number}/promote`, title: 'Mark the draft PR ready for review' });
  }
  // Runbooks deploy pull requests too. runState is the board's Runs
  // state, present only on a single board's view.
  const runbooks = (runState && runState.repoRunbooks) || [];
  const prRuns = isPR ? ((runState && runState.instances) || []).filter(r => r.target === item.number) : [];
  if (isPR && runbooks.length) {
    actions.push({ label: showDeploy ? 'Deploy ▴' : 'Deploy ▾', onClick: () => setShowDeploy(v => !v), title: 'Plan a run of this pull request from one of the repository\'s runbooks' });
  }

  // The Agent column: each recipe's newest run, or the click waiting for
  // it. A failed run with an error to show opens it first (the "why");
  // the session is one more click from there.
  // A pending review of the member's on GitHub is the review's draft, run
  // or not: it awaits their verdict.
  const chips = runs.map(run => ({ recipe: run.recipe, label: run.label || run.recipe, run,
    status: run.recipe === 'review' && item.reviewPending ? 'ready' : statusOf(run.recipe) }));
  for (const [name, state] of Object.entries(item.launching || {})) {
    if (runOf(name)) continue;
    const rec = (item.recipes || []).find(r => r.name === name);
    chips.push({ recipe: name, label: rec ? rec.label : name, status: state });
  }
  // What GitHub still records of a review or a fix whose run is gone
  // (its sandbox deleted) is the recipe's chip too, linking GitHub.
  if (!runOf('review') && !(item.launching || {}).review) {
    if (item.reviewPending) {
      chips.push({ recipe: 'review', label: 'Review', status: 'pending', href: `${item.htmlURL}/files`,
        title: 'Your pending review is saved on GitHub, visible only to you — finalize it there' });
    } else if (item.reviewed) {
      chips.push({ recipe: 'review', label: 'Review', status: 'done', href: item.htmlURL, title: 'Your review is submitted — open the PR' });
    }
  }
  if (item.type === 'issue' && item.prURL && !runOf('fix') && !(item.launching || {}).fix) {
    chips.push({ recipe: 'fix', label: 'Fix', status: 'done', href: item.prURL, title: 'Fix shipped — open the PR' });
  }
  const failedChip = chips.some(c => c.status === 'failed');

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
        {item.author && item.type !== 'issue' && !item.mine && (
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
        {prRuns.map(prRunChip)}
        {/* factory pr watch follows a fix's PR up on its own (fix-ci,
            address-comments, as care); the chip turns it on or off. */}
        {isPR && item.myPR && runOf('fix') && item.sandbox && item.sandbox.autoIterate && (
          <span
            onClick={() => {
              fetch(`/api/board/${boardName}/prs/${item.number}/auto-iterate`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ mode: item.sandbox.autoIterate === 'on' ? 'off' : 'on' }),
              }).then(res => { if (res.ok && onRefresh) onRefresh(); }).catch(() => {});
            }}
            style={{ cursor: 'pointer', marginLeft: '6px' }}
            title={`Auto follow-up is ${item.sandbox.autoIterate}${item.sandbox.autoIterateOverridden ? ' (set for this PR)' : ' (board default)'} — click to turn ${item.sandbox.autoIterate === 'on' ? 'off' : 'on'} for this PR`}>
            <Chip text={item.sandbox.autoIterate === 'on' ? 'auto ⏻' : 'auto ⏸'}
              color={item.sandbox.autoIterate === 'on' ? '#22863a' : '#6a737d'}
              bg={item.sandbox.autoIterate === 'on' ? 'rgba(34,134,58,0.14)' : 'rgba(106,115,125,0.12)'} />
          </span>
        )}
      </td>
      {/* GitHub facts on the left …, repo-agent state on the right:
          Agent (machine facts, incl. run outcomes), then the one-action
          rail — launch verbs and the row's GitHub moves. */}
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
        {chips.map(c => {
          const style = RUN_STYLE[c.status] || RUN_STYLE.running;
          const text = `${c.label}: ${c.status}`;
          if (c.status === 'failed' && item.error) {
            return (
              <span key={c.recipe} onClick={() => setShowError(v => !v)} style={{ cursor: 'pointer', marginLeft: '4px' }}
                title="Show why the run stopped">
                <Chip text={`${text} !`} color={style.color} bg={style.bg} />
              </span>
            );
          }
          if (c.href) {
            return (
              <a key={c.recipe} href={c.href} target="_blank" rel="noopener noreferrer" style={{ textDecoration: 'none', marginLeft: '4px' }} title={c.title}>
                <Chip text={`${text} ↗`} color={style.color} bg={style.bg} />
              </a>
            );
          }
          // A ready run's chip is the row's move: it looks pressable, and
          // opens the session its draft is read and applied in.
          if (c.status === 'ready' && c.run) {
            return (
              <a key={c.recipe} className="btn btn-sm" href={taskSessionHref(c.run)} target="_blank" rel="noopener noreferrer"
                onClick={sessionClick(onOpenSession, c.run)}
                title={`${c.label}'s draft awaits your verdict — open its session to read and apply it`}
                style={{ marginLeft: '4px', textDecoration: 'none', color: READY_STYLE.color, backgroundColor: READY_STYLE.bg, borderColor: READY_STYLE.color, fontWeight: 600 }}>
                {`${text} ↗`}
              </a>
            );
          }
          // A running task's session can be watched, not driven; an
          // ended one continued.
          return c.run ? (
            <a key={c.recipe} href={taskSessionHref(c.run)} target="_blank" rel="noopener noreferrer"
              onClick={sessionClick(onOpenSession, c.run)}
              style={{ textDecoration: 'none', marginLeft: '4px' }}
              title={c.status === 'running' ? 'Watch the agent\'s conversation as it runs' : 'Continue the conversation'}>
              <Chip text={`${text} ↗`} color={style.color} bg={style.bg} />
            </a>
          ) : (
            <span key={c.recipe} style={{ marginLeft: '4px' }}
              title={c.status === 'queued' ? 'Waiting for a free slot on the board' : 'Launching — the run has not started yet'}>
              <Chip text={text} color={style.color} bg={style.bg} />
            </span>
          );
        })}
        {item.error && !failedChip && (
          <span onClick={() => setShowError(v => !v)} style={{ cursor: 'pointer', marginLeft: '4px' }} title="Show why the run stopped">
            <Chip text="failed !" color={RUN_STYLE.failed.color} bg={RUN_STYLE.failed.bg} />
          </span>
        )}
      </td>
      <td style={{ padding: '6px 8px', textAlign: 'right', whiteSpace: 'nowrap' }}>
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
              // A recipe's required inputs with no default are asked here.
              const inputs = {};
              for (const name of a.inputs || []) {
                const v = window.prompt(`${a.label}: ${name}`);
                if (!v || !v.trim()) return;
                inputs[name] = v.trim();
              }
              onAction(a.path, a.label, (a.inputs || []).length ? { inputs } : {});
            }}
          >{a.label}</button>
        ))}
      </td>
    </tr>
    {showDeploy && isPR && runbooks.length > 0 && (
      <tr>
        <td colSpan="5" style={{ padding: '0 8px 10px 8px' }}>
          <PRDeploy boardName={boardName} number={item.number} runbooks={runbooks}
            runs={runState.instances} onStarted={() => { setShowDeploy(false); if (onRunStarted) onRunStarted(); }} />
        </td>
      </tr>
    )}
    {showError && item.error && (
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
            {failedRun && (
              <a className="btn btn-sm" href={taskSessionHref(failedRun)} onClick={sessionClick(onOpenSession, failedRun)}
                target="_blank" rel="noopener noreferrer" style={{ textDecoration: 'none', marginLeft: '4px' }}
                title="Continue the run's conversation where it left off"
              >Continue session ↗</a>
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

// The receipt's first line, compressed to a badge — shared by the
// per-board Runs tab and the All-boards runs view.
function verdictBadge(receipt) {
  if (!receipt || !receipt.verdict) return <span style={{ color: 'var(--text-secondary)' }}>—</span>;
  const v = receipt.verdict.toUpperCase();
  const date = (receipt.name.match(/(\d{8})/) || [])[1];
  const when = date ? ` · ${date.slice(4, 6)}-${date.slice(6, 8)}` : '';
  if (v.startsWith('PLANNED')) return <Chip text={`📋 planned${when} — review, then Deploy`} color="#0366d6" bg="rgba(3,102,214,0.08)" />;
  if (v.startsWith('VERIFIED')) return <Chip text={`✅ verified${when}`} color="#28a745" bg="rgba(40,167,69,0.10)" />;
  if (v.startsWith('TORN-DOWN')) return <Chip text={`🔻 torn down${when}`} color="#6a737d" bg="var(--bg-secondary)" />;
  if (v.startsWith('BLOCKED')) return <Chip text={`🔒 blocked${when} — see receipt: Needs from owner`} color="#d73a49" bg="rgba(215,58,73,0.12)" />;
  if (v.startsWith('FAILED') || v.startsWith('PARTIAL')) return <Chip text={`❌ ${v.split(' ')[0].toLowerCase()}${when}`} color="#d73a49" bg="rgba(215,58,73,0.12)" />;
  return <Chip text={`${v.split(' ')[0].toLowerCase()}${when}`} color="#b08800" bg="rgba(176,136,0,0.12)" />;
}

// localOnlyChip marks a run whose records live only in its sandbox:
// the repository could not be forked, so nothing was pushed, and
// deleting the sandbox deletes the run — teardown script and all.
function localOnlyChip(run) {
  if (!run || !run.localOnly) return null;
  return (
    <span style={{ marginLeft: '6px' }}
      title={`Local-only: the repository can't be forked, so this run's records are kept in sandbox ${run.sandbox} and never pushed. Deleting that sandbox deletes them, teardown script included.`}>
      <Chip text="local-only" color="#b08800" bg="rgba(176,136,0,0.12)" />
    </span>
  );
}

// elapsedSince renders a compact age ("1h43m") for running tasks.
function elapsedSince(iso) {
  if (!iso) return '';
  const mins = Math.max(0, Math.floor((Date.now() - new Date(iso).getTime()) / 60000));
  return mins < 60 ? `${mins}m` : `${Math.floor(mins / 60)}h${String(mins % 60).padStart(2, '0')}m`;
}

// What the live task is doing, named by the standing claim's mode —
// "running" alone leaves you guessing whether a teardown took.
const RUNBOOK_VERB = { plan: 'planning', deploy: 'deploying', run: 'deploying', teardown: 'tearing down' };

// A click that failed before anything ran is still a click: the server
// keeps it for a week precisely so the tab can say so. Everything else
// about a pending row means "in flight".
const clickFailed = (pend) => !!pend && pend.phase === 'Failed';

// pendingFor: the one standing click a run's row speaks for.
//
// There can be more than one — a deploy that failed is kept for a week
// while a fresh plan on the same run is in flight — and a live click
// always outranks a dead one. Taking whichever came first in the list
// would leave last week's failure on the row and, worse, leave the
// buttons enabled underneath a run that is already working.
const pendingFor = (pending, name) => {
  const mine = (pending || []).filter(p => (p.instance || p.scenario) === name);
  return mine.find(p => !clickFailed(p)) || mine[0];
};

// pendingChipFor: what the standing click says while there is no
// sandbox task to read — queued, or dead with the reason attached.
function pendingChipFor(pend, provisionalText) {
  if (clickFailed(pend)) {
    return <Chip text={`${pend.mode} failed`} title={pend.message || pend.reason}
      color="#d73a49" bg="rgba(215,58,73,0.12)" />;
  }
  return <Chip text={provisionalText || `${pend.mode} queued…`} color="#b08800" bg="rgba(176,136,0,0.12)" />;
}

// runningChipFor: the truth about a Running sandbox — alive (with verb
// and elapsed), finished-but-unstamped, or interrupted (pid gone, no
// exit code).
function runningChipFor(sb, pend) {
  if (sb.taskAlive === false) {
    if (sb.taskExit !== '' && sb.taskExit !== undefined) {
      return <Chip text="finishing…" color="#b08800" bg="rgba(176,136,0,0.12)" />;
    }
    return <Chip text="💥 interrupted — Re-deploy to retry" color="#d73a49" bg="rgba(215,58,73,0.12)" />;
  }
  const verb = (pend && RUNBOOK_VERB[pend.mode]) || 'running';
  const age = elapsedSince(sb.taskStartedAt);
  const tearing = pend && pend.mode === 'teardown';
  return <Chip text={age ? `${verb} · ${age}` : verb}
    color={tearing ? '#6a737d' : '#b08800'}
    bg={tearing ? 'var(--bg-secondary)' : 'rgba(176,136,0,0.12)'} />;
}

// AllRunsPanel: the fleet dashboard — every deployment across every
// board in one table. Read + act (each row posts to its own board);
// creation stays on the per-board Runs tab. Polls at 30s: the runbook
// GET fans out to GitHub, and deployments change on minute scales.
function AllRunsPanel({ boards, onOpenSandbox, onGoBoard }) {
  const [data, setData] = useState({});
  const [busy, setBusy] = useState('');

  const names = boards.map(b => b.name);
  const key = names.join(',');
  const load = useCallback(() => {
    Promise.all(names.map(n =>
      fetch(`/api/board/${n}/runbook`).then(r => (r.ok ? r.json() : null)).then(d => [n, d]).catch(() => [n, null])
    )).then(entries => setData(Object.fromEntries(entries.filter(e => e[1]))));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);
  useEffect(() => {
    load();
    const t = setInterval(load, 30000);
    return () => clearInterval(t);
  }, [load]);

  const kickoff = (board, mode, name) => {
    setBusy(`${board}:${name}`);
    fetch(`/api/board/${board}/runbook`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ mode, name, intent: '' }),
    }).then(() => { setTimeout(load, 2000); setTimeout(() => setBusy(''), 2000); }).catch(() => setBusy(''));
  };

  const rows = [];
  for (const [board, d] of Object.entries(data)) {
    const instances = d.instances || [];
    const pending = d.pending || [];
    const sandboxes = d.sandboxes || [];
    const merged = [...instances];
    for (const p of pending) {
      if (p.mode === 'teardown') continue;
      const name = p.instance || p.scenario;
      if (!merged.some(i => i.name === name)) merged.push({ name, provisional: true });
    }
    for (const inst of merged) {
      const sb = sandboxes.find(s => (s.instance || s.scenario) === inst.name);
      const pend = pendingFor(pending, inst.name);
      rows.push({ board, inst, sb, pend });
    }
  }
  const rank = r => {
    const v = ((r.inst.latestReceipt || {}).verdict || '').toUpperCase();
    if (r.sb && r.sb.taskState === 'Running') return 0;
    if (r.pend) return 0;
    if (v.startsWith('PLANNED')) return 1;
    if (v.startsWith('BLOCKED') || v.startsWith('FAILED')) return 2;
    if (v.startsWith('VERIFIED')) return 3;
    return 4;
  };
  rows.sort((a, b) => rank(a) - rank(b) || a.board.localeCompare(b.board) || a.inst.name.localeCompare(b.inst.name));
  const cell = { padding: '5px 8px', verticalAlign: 'middle' };

  return (
    <div className="work-card" style={{ padding: '14px', textAlign: 'left', fontSize: 'small' }}>
      {rows.length === 0 ? (
        <div style={{ color: 'var(--text-secondary)', fontStyle: 'italic' }}>
          No deployments anywhere — plan one from a board's Runs tab.
        </div>
      ) : (
        <table style={{ width: '100%', borderCollapse: 'collapse' }}>
          <thead>
            <tr style={{ textAlign: 'left', color: 'var(--text-secondary)', fontSize: 'x-small' }}>
              <th style={cell}>board</th>
              <th style={cell}>deployment</th>
              <th style={cell}>agent</th>
              <th style={cell}>procedure</th>
              <th style={cell}>last run</th>
              <th style={{ ...cell, textAlign: 'right' }}></th>
            </tr>
          </thead>
          <tbody>
            {rows.map(({ board, inst, sb, pend }) => {
              // The annotation can be stale (a dead watcher never stamped
              // the final state); the probe is the truth when it speaks.
              const running = sb && sb.taskState === 'Running' && sb.taskAlive !== false;
              // A dead click must not hold the buttons down: clicking
              // again is the only retry there is.
              const inFlight = !!pend && !clickFailed(pend);
              const receipt = inst.latestReceipt;
              const v = ((receipt || {}).verdict || '').toUpperCase();
              return (
                <tr key={`${board}/${inst.name}`} style={{ borderTop: '1px solid var(--border-color)' }}>
                  <td style={cell}>
                    <a href="#board" onClick={e => { e.preventDefault(); onGoBoard(board); }}
                      title="Open this board's Runs tab">{board}</a>
                  </td>
                  <td style={cell}>
                    {inst.provisional ? <span style={{ fontWeight: 500 }}>⛭ {inst.name}</span> : (
                      <>
                        <a href={inst.htmlURL} target="_blank" rel="noopener noreferrer"
                          style={{ fontWeight: 500, textDecoration: 'none', color: 'var(--text-primary)' }}>⛭ {inst.name} ↗</a>
                        {localOnlyChip(inst)}
                      </>
                    )}
                  </td>
                  <td style={cell}>
                    {sb ? (
                      <span onClick={() => onOpenSandbox && onOpenSandbox(sb.name, board)} style={{ cursor: 'pointer', display: 'inline-flex', alignItems: 'center' }}
                        title={`${sb.name} — tasks & logs`}>
                        {ENGINE_ICON[sb.engine] ? <EngineIcon engine={sb.engine} /> : <span style={{ marginRight: '6px' }}>⚙</span>}
                        {running && runningChipFor(sb, pend)}
                        {pend && !running && pendingChipFor(pend)}
                      </span>
                    ) : (pend ? pendingChipFor(pend, inst.provisional ? 'preparing…' : '') : <span style={{ color: 'var(--text-secondary)' }}>—</span>)}
                  </td>
                  <td style={cell}>
                    {inst.runbook
                      ? <a href={inst.runbook.htmlURL} target="_blank" rel="noopener noreferrer"
                          title="This run's procedure">runbook.md ↗</a>
                      : <span style={{ color: 'var(--text-secondary)' }}>—</span>}
                  </td>
                  <td style={cell}>
                    {verdictBadge(receipt)}{' '}
                    {receipt && <a href={receipt.htmlURL} target="_blank" rel="noopener noreferrer">receipt ↗</a>}
                  </td>
                  <td style={{ ...cell, textAlign: 'right', whiteSpace: 'nowrap' }}>
                    {v.startsWith('PLANNED') ? (
                      <button className="btn btn-sm" disabled={running || inFlight || busy === `${board}:${inst.name}`}
                        title="Execute the reviewed plan"
                        onClick={() => kickoff(board, 'deploy', inst.name)}>▶ Deploy</button>
                    ) : (
                      <button className="btn btn-sm" disabled={running || inFlight || busy === `${board}:${inst.name}`}
                        title="Plan this run again"
                        onClick={() => kickoff(board, 'plan', inst.name)}>▶ Re-plan</button>
                    )}
                    {inst.deployed === true && (
                      <button className="btn btn-sm" disabled={running || inFlight || busy === `${board}:${inst.name}`} style={{ marginLeft: '6px' }}
                        title="Remove what this run created"
                        onClick={() => kickoff(board, 'teardown', inst.name)}>Tear down</button>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
    </div>
  );
}

// TryPanel is the Runs tab: every run for this repo, and the composer
// that starts a new one.
//
// A run owns its own runbook.md — the procedure, authored at plan time
// and corrected by each deploy. Starting one means naming it and either
// saying what it should do, or picking a runbook to start from: one of
// the repository's (.agents/runbooks/) or another of your runs. The
// runbook is copied into the new run and planned for it, so nothing is
// shared afterwards; what you type then is only what to change.
function TryPanel({ boardName, onOpenSandbox }) {
  const [state, setState] = useState(null);
  const [name, setName] = useState('');
  const [intent, setIntent] = useState('');
  const [runbook, setRunbook] = useState('');
  const [refining, setRefining] = useState('');
  const [refineText, setRefineText] = useState('');
  const [busy, setBusy] = useState('');
  const [error, setError] = useState('');

  const load = useCallback(() => {
    fetch(`/api/board/${boardName}/runbook`)
      .then(res => (res.ok ? res.json() : null))
      .then(data => setState(data))
      .catch(() => {});
  }, [boardName]);
  useEffect(() => {
    load();
    const t = setInterval(load, 10000);
    return () => clearInterval(t);
  }, [load]);

  const sandboxes = (state && state.sandboxes) || [];
  const pending = (state && state.pending) || [];
  const runs = (state && state.instances) || [];
  const repoRunbooks = (state && state.repoRunbooks) || [];
  const findSb = (n) => sandboxes.find(s => (s.instance || s.scenario) === n);
  const findPending = (n) => pendingFor(pending, n);
  const slug = (s) => String(s || '').toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^-+|-+$/g, '');

  const kickoff = (mode, runName, text, from) => {
    setBusy(`${mode}:${runName}`);
    fetch(`/api/board/${boardName}/runbook`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ mode, name: runName, intent: (text || '').trim(), ...(from ? { runbook: from } : {}) }),
    }).then(res => {
      if (res.ok) {
        setState(prev => prev ? { ...prev, pending: [...(prev.pending || []), { mode, instance: runName }] } : prev);
      } else {
        // A refused start used to vanish: the row appeared, then went.
        res.text().then(t => setError(`${mode} ${runName} failed: ${t}`));
      }
      setTimeout(load, 2000);
      setTimeout(() => setBusy(''), 2000);
    }).catch(() => setBusy(''));
  };

  const startNew = () => {
    const n = slug(name);
    if (!n) return;
    kickoff('plan', n, intent, runbook);
    setName('');
    setIntent('');
    setRunbook('');
  };

  const replan = (runName) => {
    kickoff('plan', runName, refineText);
    setRefining('');
    setRefineText('');
  };

  // Deleting the receipts is the one thing here that cannot be undone
  // — and it used to report nothing at all, so a removal that failed
  // looked exactly like one that worked on a row that never went away.
  const removeRun = (runName) => {
    if (!window.confirm(`Remove "${runName}"? Its runbook and receipts are deleted from the branch. Cloud resources are not touched.`)) return;
    setError('');
    fetch(`/api/board/${boardName}/runbook/instance/${runName}`, { method: 'DELETE' })
      .then(res => {
        if (res.ok) { setTimeout(load, 1500); }
        else { res.text().then(t => setError(`Remove ${runName} failed: ${t}`)); }
      })
      .catch(err => setError(`Remove ${runName} failed: ${err}`));
  };

  const composerName = slug(name);
  const taken = composerName && runs.some(r => r.name === composerName);
  // The value is the runbook's name; the repository's and your runs'
  // are one namespace to factory, which looks in the repository first.
  const fromItself = runbook && runbook === composerName;
  const runRunbooks = runs.map(r => r.name).filter(n => !repoRunbooks.includes(n));
  const cell = { padding: '5px 8px', verticalAlign: 'middle' };

  // A run just clicked has no directory on the branch yet — the first
  // push lands minutes later, after a cold boot and clone. Show it
  // immediately so the click is not swallowed.
  const rows = [...runs];
  for (const p of pending) {
    if (p.mode === 'teardown') continue;
    const n = p.instance || p.scenario;
    if (n && !rows.some(r => r.name === n)) rows.push({ name: n, provisional: true });
  }

  return (
    <div className="work-card" style={{ padding: '14px', textAlign: 'left', fontSize: 'small' }}>
      {state && state.gcpProject === '' && (
        <div style={{ border: '1px solid #b08800', borderRadius: '10px', padding: '8px 12px',
          marginBottom: '10px', color: '#b08800', background: 'rgba(176,136,0,0.08)' }}>
          ⚠ No GCP project configured — a plan will stop at the procedure and tell you what it
          needs. Set one in <a href="#/settings" style={{ color: 'inherit' }}>Settings</a>.
        </div>
      )}

      {error && (
        <div style={{ border: '1px solid #d73a49', borderRadius: '10px', padding: '8px 12px',
          marginBottom: '10px', color: '#d73a49', background: 'rgba(215,58,73,0.08)' }}>
          {error}
          <button className="btn btn-sm" style={{ marginLeft: '8px' }} onClick={() => setError('')}>dismiss</button>
        </div>
      )}

      <div style={{ border: '1px solid var(--border-color)', borderRadius: '10px',
        background: 'var(--bg-secondary)', padding: '10px 12px', marginBottom: '10px' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: '8px', flexWrap: 'wrap' }}>
          <span style={{ color: 'var(--text-secondary)' }}>new run:</span>
          <span style={{ display: 'inline-flex', alignItems: 'center', flex: '0 1 320px',
            border: '1px solid var(--border-color)', borderRadius: '4px', padding: '0 0 0 8px' }}
            title="Cloud resources this run creates are named <prefix>-<name>[-suffix] — the prefix is added for you, so don't repeat the repo in the name">
            <span style={{ color: 'var(--text-secondary)', whiteSpace: 'nowrap' }}>{(state && state.repoShort) || ''}-</span>
            <input type="text" value={name} onChange={e => setName(e.target.value)}
              placeholder="name, e.g. deploy-gke-k8s1"
              style={{ flex: 1, padding: '3px 8px 3px 2px', border: 'none', outline: 'none',
                background: 'transparent', color: 'var(--text-primary)', font: 'inherit' }} />
          </span>
          {taken && <Chip text="name already used" color="#d73a49" bg="rgba(215,58,73,0.12)" />}
          <span style={{ color: 'var(--text-secondary)' }}>from:</span>
          <select value={runbook} onChange={e => setRunbook(e.target.value)}
            aria-label="runbook to start from"
            title="Start from an existing runbook: it is copied into the new run and planned for it — its prefix, project and region become this run's"
            style={{ padding: '3px 6px', border: '1px solid var(--border-color)', borderRadius: '4px',
              background: 'var(--bg-primary)', color: 'var(--text-primary)', font: 'inherit' }}>
            <option value="">nothing — describe it</option>
            {repoRunbooks.length > 0 && (
              <optgroup label="repository (.agents/runbooks)">
                {repoRunbooks.map(n => <option key={`repo:${n}`} value={n}>{n}</option>)}
              </optgroup>
            )}
            {runRunbooks.length > 0 && (
              <optgroup label="your runs">
                {runRunbooks.map(n => <option key={`run:${n}`} value={n}>{n}</option>)}
              </optgroup>
            )}
          </select>
          {fromItself && <Chip text="a run cannot start from itself" color="#d73a49" bg="rgba(215,58,73,0.12)" />}
        </div>
        <div style={{ display: 'flex', alignItems: 'flex-end', gap: '8px', marginTop: '6px' }}>
          <textarea rows={2} value={intent} onChange={e => setIntent(e.target.value)}
            placeholder={runbook
              ? `anything to change from ${runbook}? (optional) e.g. 5 nodes, us-east1`
              : 'what should this run do? e.g. deploy this repo on GKE with 3 ubuntu nodes and the streams flag on'}
            style={{ flex: 1, border: 'none', outline: 'none', resize: 'vertical',
              background: 'transparent', color: 'var(--text-primary)', font: 'inherit', boxSizing: 'border-box' }} />
          <button className="btn btn-sm" disabled={!composerName || taken || fromItself || !!busy}
            title={runbook
              ? `Copies ${runbook} into this run and plans it here, pushed for review. Nothing executes until you approve.`
              : 'Writes the procedure and its scripts, pushed for review. Nothing executes until you approve.'}
            onClick={startNew}>▶ Plan</button>
        </div>
      </div>

      {rows.length === 0 ? (
        <div style={{ color: 'var(--text-secondary)', fontStyle: 'italic' }}>
          No runs yet — name one above and say what it should do.
        </div>
      ) : (
        <div style={{ border: '1px solid var(--border-color)', borderRadius: '10px', padding: '6px 8px' }}>
          <table style={{ width: '100%', borderCollapse: 'collapse' }}>
            <tbody>
              {rows.map(run => {
                const sb = findSb(run.name);
                const pend = findPending(run.name);
                // The annotation can be stale (a dead watcher never
                // stamped the final state); the probe is the truth
                // when it speaks.
                const running = sb && sb.taskState === 'Running' && sb.taskAlive !== false;
                const receipt = run.latestReceipt;
                const verdict = ((receipt && receipt.verdict) || '').toUpperCase();
                const planned = verdict.startsWith('PLANNED');
                // Whether infrastructure exists is the API's call now,
                // from the newest receipt that settles it — a re-plan
                // of a live run must not hide its teardown.
                const deployed = run.deployed === true;
                const idle = !running && (!pend || clickFailed(pend));
                return (
                  <React.Fragment key={run.name}>
                    <tr style={{ borderTop: '1px solid var(--border-color)' }}>
                      <td style={cell}>
                        {run.provisional ? (
                          <span style={{ fontWeight: 500 }}
                            title="Provisioning — the run's directory appears on the branch after the plan pushes (a first run boots and clones, a few minutes)">⛭ {run.name}</span>
                        ) : (
                          <>
                            <a href={run.htmlURL} target="_blank" rel="noopener noreferrer"
                              style={{ fontWeight: 500, textDecoration: 'none', color: 'var(--text-primary)' }}
                              title={run.localOnly ? "This run's procedure, read from its sandbox" : "This run's files on GitHub"}>⛭ {run.name} ↗</a>
                            {localOnlyChip(run)}
                          </>
                        )}
                      </td>
                      <td style={cell}>
                        {sb ? (
                          <span onClick={() => onOpenSandbox && onOpenSandbox(sb.name)} style={{ cursor: 'pointer', display: 'inline-flex', alignItems: 'center' }}
                            title={`${sb.name} — tasks & logs`}>
                            {ENGINE_ICON[sb.engine] ? <EngineIcon engine={sb.engine} /> : <span style={{ marginRight: '6px' }}>⚙</span>}
                            {running && runningChipFor(sb, pend)}
                            {pend && !running && pendingChipFor(pend)}
                          </span>
                        ) : (pend ? pendingChipFor(pend, run.provisional ? 'preparing the sandbox…' : '') : <span style={{ color: 'var(--text-secondary)' }}>—</span>)}
                      </td>
                      <td style={cell}>{verdictBadge(receipt)}</td>
                      <td style={cell}>
                        {run.runbook && (
                          <a href={run.runbook.htmlURL} target="_blank" rel="noopener noreferrer"
                            title="The procedure — read this before approving. Deploys correct it in place.">runbook.md ↗</a>
                        )}
                        {receipt && (
                          <a href={receipt.htmlURL} target="_blank" rel="noopener noreferrer" style={{ marginLeft: '8px' }}
                            title="Latest receipt — verdict, evidence, what is left running">receipt ↗</a>
                        )}
                      </td>
                      <td style={{ ...cell, textAlign: 'right', whiteSpace: 'nowrap' }}>
                        {planned && (
                          <button className="btn btn-sm" disabled={!idle} style={{ marginRight: '6px' }}
                            title="Change the plan before it runs — re-plans in place, keeping this run's history"
                            onClick={() => { setRefining(refining === run.name ? '' : run.name); setRefineText(''); }}>Refine</button>
                        )}
                        {planned ? (
                          <button className="btn btn-sm" disabled={!idle}
                            title="Execute the reviewed plan — runs the pushed deploy.sh, then brings runbook.md in line with what actually worked"
                            onClick={() => kickoff('deploy', run.name, '')}>▶ Deploy</button>
                        ) : (
                          <button className="btn btn-sm" disabled={!idle}
                            title="Plan this run again — after a teardown, an edit, code drift, or a failure"
                            onClick={() => { setRefining(refining === run.name ? '' : run.name); setRefineText(''); }}>▶ Re-plan</button>
                        )}
                        {deployed && (
                          <button className="btn btn-sm" disabled={!idle} style={{ marginLeft: '6px' }}
                            title="Runs this run's teardown script, verifies the resources are gone, writes a teardown receipt"
                            onClick={() => kickoff('teardown', run.name, '')}>Tear down</button>
                        )}
                        {!deployed && !run.provisional && !run.localOnly && idle && (
                          <button className="btn btn-sm" style={{ marginLeft: '6px' }}
                            title="Remove this run's records from the branch (receipts included) — never touches cloud resources"
                            onClick={() => removeRun(run.name)}>✕ Remove</button>
                        )}
                      </td>
                    </tr>
                    {refining === run.name && (
                      <tr>
                        <td colSpan={5} style={{ padding: '0 8px 8px' }}>
                          <div style={{ display: 'flex', alignItems: 'flex-end', gap: '8px' }}>
                            <textarea rows={2} value={refineText} onChange={e => setRefineText(e.target.value)}
                              placeholder="what to change — e.g. 5 nodes, state bucket in us-east1. The rest of the plan stands."
                              style={{ flex: 1, border: '1px solid var(--border-color)', borderRadius: '4px',
                                padding: '4px 8px', resize: 'vertical', background: 'var(--bg-primary, transparent)',
                                color: 'var(--text-primary)', font: 'inherit', boxSizing: 'border-box' }} />
                            <button className="btn btn-sm" disabled={!refineText.trim() || !idle}
                              onClick={() => replan(run.name)}>Re-plan</button>
                            <button className="btn btn-sm" onClick={() => { setRefining(''); setRefineText(''); }}>Cancel</button>
                          </div>
                        </td>
                      </tr>
                    )}
                  </React.Fragment>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
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
  // Attention, not focus. The poll gives up on an unattended window and
  // every way back in — focus, a key, the pointer — restarts it with an
  // immediate fetch, so the board you return to is never the one you left.
  const lastActiveAt = useRef(Date.now());
  const idle = useRef(false);
  const [activeGroup, setActiveGroup] = useState(''); // '' = auto-pick
  const [loading, setLoading] = useState(false);
  const [addURL, setAddURL] = useState('');
  const [repoSuggestions, setRepoSuggestions] = useState([]);
  const [error, setError] = useState('');
  // View is fluid per-user UI state (localStorage, per board) — it never
  // touches the board spec, so flipping it can never change what runs.
  const defaultView = { issues: 'all', prs: 'all', labels: '' };
  const [view, setView] = useState(defaultView);
  const [cardSandbox, setCardSandbox] = useState(null);
  // The task session open beside the board, { sandbox, task }.
  const [openSession, setOpenSession] = useState(null);
  const closeSession = useCallback(() => setOpenSession(null), []);
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

  // The board's Runs state, for Deploy ▾ and the run chips on pull
  // request rows. Reading it costs a GitHub call per run, so it is one
  // board's, never ALL's, and refreshed on a slow clock or after a
  // deploy rather than with every tick of the feed.
  const [runState, setRunState] = useState(null);
  const fetchRunState = useCallback(() => {
    const board = activeBoard;
    if (!board || board === ALL_BOARDS) return;
    fetch(`/api/board/${board}/runbook`)
      .then(res => (res.ok ? res.json() : null))
      .then(data => {
        if (board !== activeBoardRef.current) return;
        setRunState(data && !Array.isArray(data) ? data : null);
      })
      .catch(() => {});
  }, [activeBoard]);
  useEffect(() => {
    setRunState(null);
    fetchRunState();
    const t = setInterval(() => { if (!document.hidden && !idle.current) fetchRunState(); }, RUN_STATE_EVERY);
    return () => clearInterval(t);
  }, [fetchRunState]);
  // A plan just filed shows as a run only once factory has written it.
  const onRunStarted = () => { setTimeout(fetchRunState, 5000); };

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
    // One tick of the aggregate view is one request per board — every
    // repo you watch, refetched, for a list nobody reads at
    // twenty-second resolution. The board you are actually looking at
    // keeps the fast cadence; ALL settles for a minute, which is what
    // the server holds a feed fresh for anyway.
    const pollEvery = activeBoard === ALL_BOARDS ? 60000 : 20000;
    const poll = () => { if (!document.hidden) { fetchWork(); fetchBoards(); } };
    const interval = setInterval(() => {
      if (document.hidden) return;
      // A focused window is being read; keep its cadence. An unfocused one
      // may still be sitting in plain sight on another screen, which the
      // hidden check never catches — give it five quiet minutes and then
      // stop. Nobody is reading it, and it is spending a budget shared
      // with everything else that talks to GitHub.
      if (!document.hasFocus() && Date.now() - lastActiveAt.current > IDLE_AFTER) {
        idle.current = true;
        return;
      }
      poll();
    }, pollEvery);
    // Polling skips hidden tabs (quota) and browsers throttle background
    // timers — so returning to the tab must refresh NOW, not at the next
    // tick: the wait reads as a frozen board.
    const onReturn = () => { lastActiveAt.current = Date.now(); idle.current = false; poll(); };
    // Coming back without switching windows: the pointer or a key on a
    // board that had gone quiet. Same promise — refresh on the way in.
    const onActivity = () => {
      lastActiveAt.current = Date.now();
      if (!idle.current) return;
      idle.current = false;
      poll();
    };
    const activity = ['pointerdown', 'pointermove', 'keydown', 'wheel', 'touchstart'];
    document.addEventListener('visibilitychange', onReturn);
    window.addEventListener('focus', onReturn);
    activity.forEach(e => document.addEventListener(e, onActivity, { passive: true }));
    return () => {
      clearInterval(interval);
      document.removeEventListener('visibilitychange', onReturn);
      window.removeEventListener('focus', onReturn);
      activity.forEach(e => document.removeEventListener(e, onActivity));
    };
  }, [fetchWork, fetchBoards, activeBoard]);

  // A write the controller is doing (a row reading "posting") is done in
  // seconds; at the twenty-second cadence the button would keep saying
  // posting long after it was. The server keeps such a feed fresh for
  // seconds too, so polling faster while one stands shows it done.
  const posting = anyPosting(work);
  useEffect(() => {
    if (!posting) return undefined;
    const t = setInterval(() => { if (!document.hidden) fetchWork(); }, POSTING_POLL_EVERY);
    return () => clearInterval(t);
  }, [posting, fetchWork]);

  const handleAction = (path, label, boardName, body) => {
    fetch(`/api/board/${boardName || activeBoard}/${path}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body || {}),
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
        const upNextAll = work
          .filter(i => i.attention === 'needs-you')
          .sort((x, y) => (bareReviewRequest(x) - bareReviewRequest(y)) || (x.updatedAt < y.updatedAt ? -1 : 1));
        const allTab = ['all-research', 'all-runs'].includes(activeGroup) ? activeGroup : 'up-next';
        return (
          <div>
            <nav className="group-tabs" style={{ display: 'flex', gap: '6px', marginBottom: '8px' }}>
              <button className={`group-tab ${allTab === 'up-next' ? 'active' : ''}`}
                onClick={() => setActiveGroup('')}
                style={allTab === 'up-next' ? { color: '#d73a49' } : {}}
              >Up Next</button>
              {/* Research and Runs in the same order as a board's own
                  tabs, so the two rows of tabs read the same way. */}
              <button className={`group-tab ${allTab === 'all-research' ? 'active' : ''}`}
                title="Every research conversation, whichever board it was started from — including ones whose board is gone"
                onClick={() => setActiveGroup('all-research')}
              >Research</button>
              <button className={`group-tab ${allTab === 'all-runs' ? 'active' : ''}`}
                title="Every deployment across every board — the fleet dashboard"
                onClick={() => setActiveGroup('all-runs')}
              >Runs</button>
              <span style={{ marginLeft: 'auto', fontSize: 'x-small', color: 'var(--text-secondary)', alignSelf: 'center' }}>
                across {boards.length} boards
              </span>
            </nav>
            {allTab === 'all-research' ? (
              <AllResearchPanel />
            ) : allTab === 'all-runs' ? (
              <AllRunsPanel boards={boards}
                onOpenSandbox={(name) => setCardSandbox(name)}
                onGoBoard={(b) => { setActiveBoard(b); setWork([]); setActiveGroup('try'); }} />
            ) : (
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
                      onAction={(p, l, b) => handleAction(p, l, item.board, b)} onRefresh={fetchWork}
                      namespace={namespace} groupTag={item.board}
                      onGroupTagClick={() => { setActiveBoard(item.board); setWork([]); setActiveGroup(''); }}
                      onOpenSandbox={setCardSandbox} onOpenSession={setOpenSession} />
                  ))}
                  {!loadingWork && !upNextAll.length && (
                    <tr><td colSpan="5" style={{ padding: '16px 8px', color: 'var(--status-green)' }}>
                      ✓ Nothing needs you anywhere.
                    </td></tr>
                  )}
                </tbody>
              </table>
            </div>
            )}
          </div>
        );
      })() : (() => {
        // The feed is the full universe; the view narrows it here, client
        // side. In-flight items (sandbox, agent motion) always surface —
        // tightening a filter must never hide running work.
        const inFlight = i => !!i.sandbox || i.attention === 'working' || Object.keys(i.launching || {}).length > 0;
        const labelFilters = (view.labels || '').split(',').map(v => v.trim().toLowerCase()).filter(Boolean);
        const visible = work.filter(item => {
          if (inFlight(item)) return true;
          if (labelFilters.length && !(item.labels || []).some(l => labelFilters.includes(l.toLowerCase()))) return false;
          const g = groupOf(item);
          if (g === 'issues' && view.issues === 'mine') {
            // Assignee list is viewer-first, so a simple prefix test works.
            if (!(item.assignee || '').startsWith(namespace) && item.author !== namespace) return false;
          }
          if (g === 'prs') {
            if (view.prs === 'mine' && !item.mine) return false;
            if (view.prs === 'requested' && !item.reviewRequested) return false;
            if (view.prs === 'drafts' && !item.draftPR) return false;
          }
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
        const groupLabel = item => (groupOf(item) === 'issues' ? 'ISSUE' : item.mine ? 'MY PR' : 'PR');
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
                className={`group-tab ${shown === 'research' ? 'active' : ''}`}
                title="Understand this repo — an overview read, a digest of recent activity, or any question you ask; each one a conversation with an agent that has the repo checked out"
                onClick={() => setActiveGroup('research')}
              >Research</button>
              <button
                className={`group-tab ${shown === 'try' ? 'active' : ''}`}
                title="Plan, deploy, verify and tear down — each run carries its own procedure"
                onClick={() => setActiveGroup('try')}
              >Runs</button>
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
                {shown === 'prs' && (
                  <span title="View scope — display only, never changes what runs">
                    {[['all', 'All'], ['mine', 'Mine'], ['requested', 'Review requested'], ['drafts', 'Drafts']].map(([v, label]) => (
                      <button key={v} className="btn btn-sm"
                        style={{ marginLeft: '2px', opacity: view.prs === v ? 1 : 0.5 }}
                        onClick={() => updateView({ prs: v })}
                      >{label}</button>
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

            {shown === 'research' ? (
              <ResearchPanel boardName={activeBoard} repoURL={board && board.repoURL} />
            ) : shown === 'try' ? (
              <TryPanel boardName={activeBoard} onOpenSandbox={setCardSandbox} />
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
                      onOpenSandbox={setCardSandbox} onOpenSession={setOpenSession}
                      onAction={(p, l, b) => handleAction(p, l, undefined, b)} onRefresh={fetchWork} namespace={namespace}
                      groupTag={shown === UP_NEXT ? groupLabel(item) : undefined}
                      runState={runState} onRunStarted={onRunStarted} />
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

      {openSession && (
        <SessionSlideOver session={openSession} onClose={closeSession} />
      )}

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
                  title="Agent engine for this board's tasks. Per-launch: flipping it affects the next task; in-flight runs finish on the engine they started with. Claude needs an anthropic-api-key secret in your namespace; antigravity (the agy CLI) uses your Gemini API key.">
                  Engine
                  <select value={spec.engine || 'gemini'} onChange={e => setSpec({ ...spec, engine: e.target.value })}>
                    <option value="gemini">gemini</option>
                    <option value="claude">claude</option>
                    <option value="antigravity">antigravity</option>
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
              <label style={{ cursor: 'pointer', fontSize: 'small' }} title="Let the agent say it is an agent: the generated-by line in PR descriptions, and the footer on its comments and CI reports. Off, it posts none of them.">
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

export { TryPanel, WorkRow, PRDeploy, SessionSlideOver, prRunName, anyPosting };
export default Work;
