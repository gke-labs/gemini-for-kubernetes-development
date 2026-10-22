import React, { useState, useEffect, useCallback, useRef } from 'react';
import { DRAFT_VERBS } from './Research';
import agentSandboxIcon from './agent-sandbox-icon.svg';
import SandboxSize from './SandboxSize';
import Chip from './Chip';
import { ENGINE_ICON, EngineIcon, SessionMark, RUN_STYLE, READY_STYLE, ageOf, recipeSource } from './WorkBits';
import SessionSlideOver from './SessionSlideOver';
import SandboxCard from './SandboxCard';
import BoardPicker from './BoardPicker';
import AutoRules from './AutoRules';

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
// A row follows its runs by one set of rules, the same for every recipe
// (the API's row rules): each recipe's newest run is a chip in the Agent
// column, ‹label›: running / ready / done / failed (or starting / queued
// while a click waits for its run), opening the run's session; the rail
// offers a recipe not run yet, or one done or failed, by its label (the
// chip beside it says it ran), and a ready one as its draft's button — clicking opens
// the panel, and the verdict verbs (publish/approve/reject/…) live there,
// under the content they judge.
// actionLabel is how a draft's action reads: its own label, else its
// verb's button's.
function actionLabel(a) {
  return a.label || (DRAFT_VERBS[a.verb] || {}).label || a.verb;
}

// runState is where a run stands, in words, the same for every recipe:
// what it waits on, or what became of its draft.
function runState(c, error) {
  const run = c.run;
  switch (c.status) {
    case 'running':
      return 'agent working';
    case 'failed':
      return error ? error.trim().split('\n')[0] : 'open its session to see why';
    case 'ready': {
      // A review's chip stays ready while its pending review is on GitHub.
      if (run.status !== 'ready') return 'your pending review is on GitHub — submit or discard it there';
      const moves = (run.actions || []).map(actionLabel);
      return `draft needs your approval${moves.length ? `: ${moves.join(' · ')}` : ''}`;
    }
    case 'done': {
      if (!run.output) return 'nothing to apply';
      const applied = Object.entries(run.applied || {}).filter(([verb]) => verb !== 'edit')
        .map(([verb, at]) => {
          const label = verb === 'reject' ? 'discarded'
            : actionLabel((run.actions || []).find(a => a.verb === verb) || { verb });
          const ago = ageOf(at);
          return ago ? `${label} ${ago} ago` : label;
        });
      return applied.length ? applied.join(' · ') : 'draft applied';
    }
    default:
      return '';
  }
}

// chipTitle is a run chip's hover: where the run stands, and a glance at
// its draft. A chip with no run yet says what it is waiting for (hint).
function chipTitle(c, hint, error) {
  const state = c.run ? runState(c, error) : hint;
  const lines = [`${c.label}: ${c.status}${state ? ` — ${state}` : ''}`];
  if (recipeSource(c.run)) lines.push(recipeSource(c.run));
  if (c.run && c.run.preview && c.status !== 'running') lines.push(c.run.preview);
  return lines.join('\n');
}

// runGroup is the group a run or a recipe is in: its session tag, else
// itself. A group's recipes run into one conversation, one at a time.
function runGroup(s) {
  return s.session || s.recipe || s.name;
}

// latestRuns is the newest run of each group on the row (its sessions
// are newest first).
function latestRuns(item) {
  const seen = new Set();
  return (item.sessions || []).filter(s => !seen.has(runGroup(s)) && seen.add(runGroup(s)));
}

// boardRoute reads #/boards/<name>: the board Work is on.
export function boardRoute(hash = window.location.hash) {
  const m = hash.match(/^#\/boards\/([A-Za-z0-9._-]+)$/);
  return m ? { name: m[1] } : null;
}

// fetchWorkFeed reads a board's feed, failing with the API's reason (a
// rejected GitHub token says to update it in Settings) rather than a bare
// status.
export function fetchWorkFeed(board) {
  return fetch(`/api/board/${board}/work`).then(res => {
    if (res.ok) return res.json();
    return res.json().catch(() => ({})).then(body => {
      let msg = body.error || res.statusText || `HTTP ${res.status}`;
      if (body.details && res.status !== 424) msg += `: ${String(body.details).slice(0, 300)}`;
      throw new Error(msg);
    });
  });
}

// WorkErrorRow is why a feed could not be read, where its rows would be.
function WorkErrorRow({ error }) {
  return (
    <tr><td colSpan="5" className="warning-banner" style={{ padding: '12px 8px', whiteSpace: 'pre-line' }}>
      {error}
    </td></tr>
  );
}

// How long a window nobody is using keeps polling before it gives up.
// document.hidden does not cover the case that actually spends the GitHub
// budget: a board left visible on a second monitor, unfocused and unread,
// refetching every repo you watch all day. Five minutes is long enough to
// read a row and come back to it without the board ever going quiet.
const IDLE_AFTER = 5 * 60 * 1000;
// How often the pull request rows re-read the board's runs. Runs change
// on the scale of a plan, which is minutes.
const RUN_STATE_EVERY = 2 * 60 * 1000;

// usePolling calls fn now and every `every` ms while active, as the feed
// polls: not while the tab is hidden, nor once an unfocused window has
// sat untouched for IDLE_AFTER; and at once on coming back to it.
export function usePolling(fn, every, active) {
  useEffect(() => {
    if (!active) return undefined;
    let lastActiveAt = Date.now();
    let idle = false;
    fn();
    const t = setInterval(() => {
      if (document.hidden) return;
      if (!document.hasFocus() && Date.now() - lastActiveAt > IDLE_AFTER) {
        idle = true;
        return;
      }
      fn();
    }, every);
    const onReturn = () => {
      lastActiveAt = Date.now();
      idle = false;
      if (!document.hidden) fn();
    };
    const onActivity = () => {
      lastActiveAt = Date.now();
      if (!idle) return;
      idle = false;
      fn();
    };
    const activity = ['pointerdown', 'keydown'];
    document.addEventListener('visibilitychange', onReturn);
    window.addEventListener('focus', onReturn);
    activity.forEach(e => document.addEventListener(e, onActivity, { passive: true }));
    return () => {
      clearInterval(t);
      document.removeEventListener('visibilitychange', onReturn);
      window.removeEventListener('focus', onReturn);
      activity.forEach(e => document.removeEventListener(e, onActivity));
    };
  }, [fn, every, active]);
}

// The board's one list of issues and pull requests: what needs you or is
// running first, then what finished lately, the rest after, narrowed by
// the filters above it.
const WORK = 'work';
const WORK_HINT = 'Open issues and pull requests — what needs you or is running first, then what finished in the last 30 days, then the rest; actions follow each row';

// isActive is whether a row belongs above Everything else: it needs
// you, an agent is working on it (Active), or its work finished lately
// (Done: a run done, a review submitted, a fix's PR open).
function isActive(item) {
  return item.attention === 'needs-you' || item.attention === 'working' || item.attention === 'done' ||
    Object.keys(item.launching || {}).length > 0;
}

// matchesSearch is whether a row has q in its title, number (#12 or 12),
// author or labels.
export function matchesSearch(item, q) {
  q = (q || '').trim().toLowerCase();
  if (!q) return true;
  if (/^#?\d+$/.test(q)) return String(item.number).startsWith(q.replace('#', ''));
  return (item.title || '').toLowerCase().includes(q) ||
    (item.author || '').toLowerCase().includes(q) ||
    (item.labels || []).some(l => l.toLowerCase().includes(q));
}

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
// POSTING_POLL_EVERY is the feed's cadence while a write stands: the
// server rebuilds such a feed from Kubernetes alone, over its last GitHub
// snapshot, so polling every five seconds costs no GraphQL.
export const POSTING_POLL_EVERY = 5000;

// taskSessionHref is a task's agent session (plan, triage) in its own
// tab: watched while the task runs, continued after.
// autoTitle is the auto chip's hover: what it does, since when, and why
// its last watch failed.
function autoTitle(auto) {
  if (!auto.on) {
    return 'Auto is off — click to watch this PR: care runs on new review comments and failed checks';
  }
  const lines = ['Auto is on: care runs on new review comments and failed checks — click to turn off'];
  if (auto.since) lines.push(`since: ${new Date(auto.since).toLocaleString()}`);
  if (auto.message) lines.push(`last watch failed: ${auto.message}`);
  return lines.join('\n');
}

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

// anyPosting is whether a row has a write standing, filed on
// one of its runs.
function anyPosting(items) {
  return (items || []).some(item => item.posting);
}

// SELF_REVIEW_KEY keeps whether the viewer is offered Review on their own
// PRs, everywhere: GitHub takes no approval from a PR's author, so a
// self-review is submitted as a comment. Off by default.
const SELF_REVIEW_KEY = 'repoboard.selfReview';

// withoutSelfReview is item without the Review button on a PR of the
// viewer's, unless they turned self-review on.
function withoutSelfReview(item, selfReview) {
  if (selfReview || !item.mine || item.type === 'issue' || !(item.recipes || []).some(r => r.name === 'review')) return item;
  return { ...item, recipes: item.recipes.filter(r => r.name !== 'review') };
}

function WorkRow({ item, boardName, onAction, onRefresh, namespace, groupTag, onGroupTagClick, onOpenSandbox, onOpenSession, runState, onRunStarted }) {
  // The row's runs: each group's newest, and what it is now — a click on
  // any of its recipes not started yet is starting (or queued), until
  // its run is.
  const runs = latestRuns(item);
  const runOf = g => runs.find(r => runGroup(r) === g);
  const recipeOf = name => (item.recipes || []).find(r => r.name === name);
  const groupOfRecipe = name => runGroup(recipeOf(name) || { name });
  const launchingOf = g => Object.entries(item.launching || {}).find(([name]) => groupOfRecipe(name) === g);
  const statusOf = g => (launchingOf(g) || [])[1] || (runOf(g) || {}).status || '';
  // A failed run's session continues where it stopped.
  const failedRun = runs.find(r => statusOf(runGroup(r)) === 'failed');

  const [showError, setShowError] = useState(false);
  const [showDeploy, setShowDeploy] = useState(false);
  const [menu, setMenu] = useState('');

  const group = groupOf(item);
  const isPR = item.type !== 'issue';

  // The rail (the last column): the recipes to launch and the row's own
  // GitHub moves; a run's history is its chip's session.
  // One button per group: the recipe named like it, the rest its ▾ menu.
  const actions = [];
  const groups = [];
  for (const rec of item.recipes || []) {
    const g = runGroup(rec);
    const known = groups.find(x => x.group === g);
    if (known) known.recipes.push(rec);
    else groups.push({ group: g, recipes: [rec] });
  }
  const recipeAction = (rec, again) => ({
    label: rec.label,
    path: `${isPR ? 'prs' : 'issues'}/${item.number}/recipes/${rec.name}`,
    inputs: rec.inputs,
    title: `Run ${rec.label}${again ? ' again' : ''} as you`,
  });
  // A recipe that takes instructions runs with them from the menu: a
  // review with instructions is Review again, saying what to change.
  const withInstructions = rec => ({
    ...recipeAction(rec, false),
    label: `${rec.label} with instructions…`,
    instructions: rec.instructions,
    title: `Run ${rec.label} as you, with instructions you give`,
  });
  for (const { group: g, recipes } of groups) {
    const status = statusOf(g);
    // Running, a click starting, or a draft to judge: the chip is the
    // row's move, not another run.
    if (['running', 'starting', 'queued', 'ready'].includes(status)) continue;
    const rec = recipes.find(r => r.name === g) || recipes[0];
    if (rec.name === 'review' && item.reviewPending) continue;
    const again = status === 'done' || status === 'failed';
    // A requested review is the same verb wearing the urgency: someone
    // is waiting on you, so the button tints red instead of adding a chip.
    const requested = rec.name === 'review' && item.reviewRequested && !again;
    const menuItems = [
      ...recipes.filter(r => r.instructions).map(withInstructions),
      ...recipes.filter(r => r !== rec).map(r => recipeAction(r, false)),
    ];
    actions.push({
      ...recipeAction(rec, again),
      tint: requested ? ATTENTION_STYLE['needs-you'] : undefined,
      title: requested ? `Your review was requested — run ${rec.label} as you` : recipeAction(rec, again).title,
      menu: menuItems.length ? { group: g, items: menuItems } : undefined,
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
  // A group's chip is labelled by what it runs: its click starting, else
  // its newest run.
  const labelOf = name => (recipeOf(name) || {}).label || name;
  const chips = runs.map(run => {
    const g = runGroup(run);
    const launching = launchingOf(g);
    const pendingReview = run.recipe === 'review' && item.reviewPending;
    // A review's draft lives on GitHub: pending, to finalize on the PR's
    // files; submitted, on the PR.
    const github = !launching && run.recipe === 'review' && (item.reviewPending
      ? { href: `${item.htmlURL}/files`, title: 'Your pending review on GitHub, visible only to you — finalize or discard it there' }
      : item.reviewed ? { href: item.htmlURL, title: 'Your review is submitted — open the PR' } : undefined);
    return { recipe: g, label: launching ? labelOf(launching[0]) : (run.label || run.recipe), run,
      status: pendingReview ? 'ready' : statusOf(g), github };
  });
  for (const [name, state] of Object.entries(item.launching || {})) {
    const g = groupOfRecipe(name);
    if (chips.some(c => c.recipe === g)) continue;
    chips.push({ recipe: g, label: labelOf(name), status: state });
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

  // A recipe's required inputs with no default are asked here.
  // and a run with instructions asks for them.
  const runAction = a => {
    const inputs = {};
    for (const name of [...(a.inputs || []), ...(a.instructions ? [a.instructions] : [])]) {
      const v = window.prompt(`${a.label}: ${name}`);
      if (!v || !v.trim()) return;
      inputs[name] = v.trim();
    }
    onAction(a.path, a.label, Object.keys(inputs).length ? { inputs } : {});
  };

  return (
    <React.Fragment>
    <tr>
      <td className="work-num" style={{ padding: '6px 4px 6px 8px', width: '1%' }} title={item.type === 'issue' ? 'Issue' : 'Pull request'}>
        {groupTag && (
          <span onClick={onGroupTagClick} style={onGroupTagClick ? { cursor: 'pointer' } : undefined}
            title={onGroupTagClick ? `Open the ${groupTag} board` : undefined}>
            <Chip text={groupTag} color={accentOf(item)} bg={tintOf(item)} title={onGroupTagClick ? undefined : (group === 'issues' ? 'An issue' : item.mine ? 'Your pull request' : 'A pull request')} />
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
        {/* Auto: factory pr watch follows a PR of yours up on its
            own (care on new comments and failed checks); the chip turns
            it on or off. */}
        {isPR && item.myPR && item.auto && (
          <span
            onClick={() => {
              fetch(`/api/board/${boardName}/prs/${item.number}/auto`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ on: !item.auto.on }),
              }).then(res => { if (res.ok && onRefresh) onRefresh(); }).catch(() => {});
            }}
            style={{ cursor: 'pointer', marginLeft: '6px' }}
            title={autoTitle(item.auto)}>
            <Chip text={item.auto.on ? (item.auto.message ? 'auto ⏻ !' : 'auto ⏻') : 'auto ⏸'}
              color={item.auto.on ? '#22863a' : '#6a737d'}
              bg={item.auto.on ? 'rgba(34,134,58,0.14)' : 'rgba(106,115,125,0.12)'} />
          </span>
        )}
      </td>
      {/* GitHub facts on the left …, repo-agent state on the right:
          Agent (machine facts, incl. run outcomes), then the one-action
          rail — launch verbs and the row's GitHub moves. */}
      <td style={{ padding: '6px 8px' }}>
        {/* The icon is the sandbox's presence on the row (agent-sandbox's
            logo; the engine is on each run's chip): click for the
            card (tasks, logs, lifecycle). Resting lifecycle (paused /
            active) is deliberately NOT a board-level chip — pause/wake
            is automatic, and every flow that needs a manual wake goes
            through the card anyway. The state lives in the tooltip. */}
        {item.sandbox && (
          <span onClick={() => onOpenSandbox && onOpenSandbox(item.sandbox.name)} style={{ cursor: 'pointer' }}>
            <img src={agentSandboxIcon} alt="sandbox"
              title={`${item.sandbox.name} (${item.sandbox.engine}${item.sandbox.replicas === '0' ? ', paused' : ''}) — tasks & logs`}
              style={{ height: '20px', verticalAlign: 'middle', marginRight: '6px' }} />
          </span>
        )}
        {chips.map(c => {
          const style = RUN_STYLE[c.status] || RUN_STYLE.running;
          const text = c.label;
          if (c.status === 'failed' && item.error) {
            return (
              <span key={c.recipe} onClick={() => setShowError(v => !v)} style={{ cursor: 'pointer', marginLeft: '4px' }}
                title={chipTitle(c, '', item.error)}>
                <Chip text={`${text} !`} color={style.color} bg={style.bg} />
              </span>
            );
          }
          if (c.href) {
            return (
              <a key={c.recipe} href={c.href} target="_blank" rel="noopener noreferrer" style={{ textDecoration: 'none', marginLeft: '4px' }} title={chipTitle(c, c.title)}>
                <Chip text={`${text} ↗`} color={style.color} bg={style.bg} />
              </a>
            );
          }
          // A review's ↗, beside its chip: where the review is on GitHub.
          // The chip and its ↗ wrap together, never apart.
          const github = c.github && (
            <a href={c.github.href} target="_blank" rel="noopener noreferrer" style={{ textDecoration: 'none', marginLeft: '2px' }}
              title={c.github.title}>↗</a>
          );
          // A ready run's chip is the row's move: it looks pressable, and
          // opens the session its draft is read and applied in.
          if (c.status === 'ready' && c.run) {
            return (
              <span key={c.recipe} style={{ whiteSpace: 'nowrap' }}>
                <a className="btn btn-sm" href={taskSessionHref(c.run)} target="_blank" rel="noopener noreferrer"
                  onClick={sessionClick(onOpenSession, c.run)}
                  title={chipTitle(c)}
                  style={{ marginLeft: '4px', textDecoration: 'none', color: READY_STYLE.color, backgroundColor: READY_STYLE.bg, borderColor: READY_STYLE.color, fontWeight: 600 }}>
                  {text}<SessionMark engine={c.run.engine} />
                </a>
                {github}
              </span>
            );
          }
          // A running task's session can be watched, not driven; an
          // ended one continued.
          return c.run ? (
            <span key={c.recipe} style={{ whiteSpace: 'nowrap' }}>
              <a href={taskSessionHref(c.run)} target="_blank" rel="noopener noreferrer"
                onClick={sessionClick(onOpenSession, c.run)}
                style={{ textDecoration: 'none', marginLeft: '4px' }}
                title={chipTitle(c)}>
                <Chip text={text} color={style.color} bg={style.bg}><SessionMark engine={c.run.engine} /></Chip>
              </a>
              {github}
            </span>
          ) : (
            <span key={c.recipe} style={{ marginLeft: '4px' }}
              title={chipTitle(c, c.status === 'queued' ? 'waiting for a free slot on the board' : 'launching — the run has not started yet')}>
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
        {actions.map(a => a.menu ? (
          <span key={a.label} style={{ position: 'relative', display: 'inline-block', marginLeft: '4px' }}>
            <button className="btn btn-sm" title={a.title} style={{ borderTopRightRadius: 0, borderBottomRightRadius: 0 }}
              onClick={() => runAction(a)}>{a.label}</button>
            <button className="btn btn-sm" aria-label={`More ${a.label} recipes`} title={`${a.label} with instructions, or the other recipes of its session`}
              style={{ borderTopLeftRadius: 0, borderBottomLeftRadius: 0, borderLeft: 'none', padding: '0 6px' }}
              onClick={() => setMenu(m => m === a.menu.group ? '' : a.menu.group)}>▾</button>
            {menu === a.menu.group && (
              <React.Fragment>
                <div onClick={() => setMenu('')} style={{ position: 'fixed', inset: 0, zIndex: 9 }} />
                <div role="menu" style={{
                  position: 'absolute', top: '100%', right: 0, zIndex: 10, marginTop: '4px',
                  display: 'flex', flexDirection: 'column', alignItems: 'stretch', gap: '2px',
                  padding: '6px', minWidth: '140px', textAlign: 'left',
                  border: '1px solid var(--border-color)', borderRadius: '8px',
                  background: 'var(--bg-card)', boxShadow: '0 4px 14px rgba(0,0,0,0.18)',
                }}>
                  {a.menu.items.map(m => (
                    <button key={m.label} role="menuitem" className="btn btn-sm" title={m.title}
                      onClick={() => { setMenu(''); runAction(m); }}>
                      {m.label}{(m.inputs || []).length && !m.instructions ? '…' : ''}
                    </button>
                  ))}
                </div>
              </React.Fragment>
            )}
          </span>
        ) : a.onClick ? (
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
            onClick={() => runAction(a)}
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
// deleting the sandbox deletes the run — teardown script and all. The
// warning is the chip's text, not only its tooltip
// (factory/design/run-branches.md).
function localOnlyChip(run) {
  if (!run || !run.localOnly) return null;
  return (
    <span style={{ marginLeft: '6px' }}
      title={`Local-only: the repository can't be forked, so this run's records are kept in sandbox ${run.sandbox} and never pushed. Deleting that sandbox deletes them, teardown script included.`}>
      <Chip text="⚠ local-only: stored only in this run's sandbox" color="#b08800" bg="rgba(176,136,0,0.12)" />
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

// TryPanel is the Runs tab: every run for this repo, and the composer
// that starts a new one.
//
// A run owns its own runbook.md — the procedure, authored at plan time
// and corrected by each deploy. Starting one means naming it and either
// saying what it should do, or picking a runbook to start from: one of
// the repository's (.agents/runbooks/) or another of your runs. The
// runbook is copied into the new run and planned for it, so nothing is
// shared afterwards; what you type then is only what to change.
function TryPanel({ boardName, onOpenSandbox, active = true }) {
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
  // Reading it costs a GitHub call per run: polled like the Runs state.
  usePolling(load, RUN_STATE_EVERY, active);

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

// active is whether Work is the view shown: hidden behind Sessions it
// stays mounted, keeping its board, filters and scroll, but polls nothing,
// and refreshes the moment it is shown again.
function Work({ onBack, namespace, active = true }) {
  const activeRef = useRef(active);
  activeRef.current = active;
  const [boards, setBoards] = useState([]);
  const [activeBoard, setActiveBoard] = useState('');
  const [work, setWork] = useState([]);
  const [loadingWork, setLoadingWork] = useState(false);
  // Why the feed could not be read: shown in its place, never an empty
  // list (that reads as "no work") or a spinner that never ends.
  const [workError, setWorkError] = useState('');
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
  const defaultView = { type: 'both', scope: 'all', labels: '' };
  const [view, setView] = useState(defaultView);
  const [selfReview, setSelfReview] = useState(() => {
    try { return localStorage.getItem(SELF_REVIEW_KEY) === 'true'; } catch (e) { return false; }
  });
  const toggleSelfReview = () => {
    setSelfReview(!selfReview);
    try { localStorage.setItem(SELF_REVIEW_KEY, String(!selfReview)); } catch (e) { /* private mode */ }
  };
  // The search narrows the list while it is typed in; it is not kept.
  const [search, setSearch] = useState('');
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
        // Work opens on the board the URL names, else the first.
        const wanted = (boardRoute() || {}).name;
        setActiveBoard(prev => prev || (data.some(b => b.name === wanted) ? wanted : (data[0] ? data[0].name : '')));
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
    fetchWorkFeed(board)
      .then(rows => {
        if (board === activeBoardRef.current) setWorkError('');
        done(rows);
      })
      .catch(err => {
        console.error('Failed to fetch work feed', err);
        if (board !== activeBoardRef.current) return;
        setWorkError(err.message);
        setLoadingWork(false);
      })
      .finally(() => { if (board === activeBoardRef.current) setSyncing(false); });
  }, [activeBoard]);

  useEffect(() => { fetchBoards(); }, [fetchBoards]);

  // The board's Runs state, for Deploy ▾ and the run chips on pull
  // request rows. Reading it costs a GitHub call per run, so it is
  // refreshed on a slow clock or after a deploy rather than with every
  // tick of the feed.
  const [runState, setRunState] = useState(null);
  const fetchRunState = useCallback(() => {
    const board = activeBoard;
    if (!board) return;
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
    const t = setInterval(() => { if (!document.hidden && !idle.current && activeRef.current) fetchRunState(); }, RUN_STATE_EVERY);
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
    setWorkError('');
    setLoadingWork(true);
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
    const pollEvery = 20000;
    const poll = () => { if (!document.hidden && activeRef.current) { fetchWork(); fetchBoards(); } };
    const interval = setInterval(() => {
      if (document.hidden || !activeRef.current) return;
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

  // The URL says which board is shown, while Work is the view: a reload
  // or a link opens on it, and toggling back from another view puts it
  // back.
  useEffect(() => {
    if (active && activeBoard) window.history.replaceState(null, '', `#/boards/${activeBoard}`);
  }, [active, activeBoard]);

  // Shown again after being hidden behind Sessions: refresh now rather
  // than at the next tick, as returning to the tab does.
  const wasActive = useRef(active);
  useEffect(() => {
    if (active && !wasActive.current) { fetchWork(); fetchBoards(); fetchRunState(); }
    wasActive.current = active;
  }, [active, fetchWork, fetchBoards, fetchRunState]);

  // A write the controller is doing (a row reading "posting") is done in
  // seconds; at the twenty-second cadence the button would keep saying
  // posting long after it was. The server keeps such a feed fresh for five
  // seconds too, so polling faster while one stands shows it done soon.
  const posting = anyPosting(work);
  useEffect(() => {
    if (!posting) return undefined;
    const t = setInterval(() => { if (!document.hidden && activeRef.current) fetchWork(); }, POSTING_POLL_EVERY);
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
      {/* One line: the board picker, the board's own links and controls
          beside it, and adding a board apart on the right. No tab per
          board: the picker's badges say which board needs you. */}
      <div style={{ display: 'flex', alignItems: 'center', gap: '8px', flexWrap: 'wrap', marginBottom: '10px', fontSize: 'small' }}>
        {onBack && <button className="btn" onClick={onBack}>← Back</button>}
        {boards.length > 0 && (
          <BoardPicker boards={boards} active={activeBoard}
            onPick={(name) => { setActiveBoard(name); setWork([]); setActiveGroup(''); }} />
        )}
        {board && (
          <div style={{ display: 'flex', alignItems: 'center', color: 'var(--text-secondary)' }}>
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
            <button
              className="btn btn-sm"
              onClick={openSpec}
              title="Board settings (trigger label, intake, limits, policy)"
              style={{ marginLeft: '8px' }}
            >⚙</button>
            <button
              className="btn btn-delete btn-sm"
              onClick={handleDeleteBoard}
              title="Remove this board (running sandboxes are not touched)"
              style={{ marginLeft: '6px' }}
            >Delete</button>
          </div>
        )}
        <span style={{ flex: 1 }} />
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

      {!boards.length ? (
        <p>No boards yet. Paste a repository URL above to create one.</p>
      ) : (() => {
        // The feed is the full universe; the view narrows it here, client
        // side. In-flight items (sandbox, agent motion) always surface —
        // tightening the scope or labels must never hide running work.
        const inFlight = i => !!i.sandbox || i.attention === 'working' || Object.keys(i.launching || {}).length > 0;
        const labelFilters = (view.labels || '').split(',').map(v => v.trim().toLowerCase()).filter(Boolean);
        const inScope = item => {
          const pr = groupOf(item) === 'prs';
          switch (view.scope) {
            case 'mine':
              // Assignee list is viewer-first, so a simple prefix test works.
              return pr ? !!item.mine : ((item.assignee || '').startsWith(namespace) || item.author === namespace);
            case 'requested': return pr && !!item.reviewRequested;
            case 'drafts': return pr && !!item.draftPR;
            default: return true;
          }
        };
        // The type and the search are what was asked for; scope and labels
        // never hide running work.
        const visible = work.filter(item => {
          if (view.type === 'issues' && groupOf(item) !== 'issues') return false;
          if (view.type === 'prs' && groupOf(item) !== 'prs') return false;
          if (!matchesSearch(item, search)) return false;
          if (inFlight(item)) return true;
          if (labelFilters.length && !(item.labels || []).some(l => labelFilters.includes(l.toLowerCase()))) return false;
          return inScope(item);
        });
        // The feed comes needs-you first, then working, then done: each
        // section keeps its order.
        const active = visible.filter(isActive);
        const busy = active.filter(i => i.attention !== 'done');
        const done = active.filter(i => i.attention === 'done');
        const rest = visible.filter(i => !isActive(i));
        // A failed refresh keeps the rows already shown; a feed never read
        // shows only why.
        const listing = !loadingWork && !(workError && !work.length);
        const needs = visible.filter(i => i.attention === 'needs-you').length;
        // Always land on Work: consistent muscle memory, and its empty
        // first section ("nothing needs you") is the good news, not a dead end.
        const shown = activeGroup === 'try' ? 'try' : WORK;
        const groupLabel = item => (groupOf(item) === 'issues' ? 'ISSUE' : item.mine ? 'MY PR' : 'PR');
        const section = (label, n) => (
          <tr className="work-section">
            <td colSpan="5" style={{ padding: '6px 8px', fontSize: 'x-small', fontWeight: 700, letterSpacing: '0.04em', color: 'var(--text-primary)', backgroundColor: 'var(--bg-secondary)', textTransform: 'uppercase' }}>
              {label} ({n})
            </td>
          </tr>
        );
        const row = item => (
          <WorkRow key={`${item.type}-${item.number}`} item={withoutSelfReview(item, selfReview)} boardName={activeBoard}
            onOpenSandbox={setCardSandbox} onOpenSession={setOpenSession}
            onAction={(p, l, b) => handleAction(p, l, undefined, b)} onRefresh={fetchWork} namespace={namespace}
            groupTag={view.type === 'both' ? groupLabel(item) : undefined}
            runState={runState} onRunStarted={onRunStarted} />
        );
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
              <button
                className={`group-tab g-work ${shown === WORK ? 'active' : ''}`}
                title={WORK_HINT}
                onClick={() => setActiveGroup(WORK)}
              >
                Work
                {visible.length > 0 && (
                  <span style={{
                    marginLeft: '6px', backgroundColor: 'var(--bg-secondary)',
                    borderRadius: '9px', padding: '0 7px', fontSize: 'x-small',
                    color: 'var(--text-secondary)',
                  }}>{visible.length}</span>
                )}
                {needs > 0 && (
                  <span style={{
                    marginLeft: '4px', backgroundColor: '#d73a49', color: 'white',
                    borderRadius: '9px', padding: '0 6px', fontSize: 'x-small',
                  }}>{needs}</span>
                )}
              </button>
              <button
                className={`group-tab ${shown === 'try' ? 'active' : ''}`}
                title="Plan, deploy, verify and tear down — each run carries its own procedure"
                onClick={() => setActiveGroup('try')}
              >Runs</button>
              <span style={{ marginLeft: 'auto', display: 'flex', gap: '6px', alignItems: 'center', fontSize: 'small' }}>
                {shown === WORK && (
                  <span title="View — display only, never changes what runs">
                    {[['both', 'Both'], ['issues', 'Issues'], ['prs', 'PRs']].map(([v, label]) => (
                      <button key={v} className="btn btn-sm"
                        style={{ marginLeft: '2px', opacity: view.type === v ? 1 : 0.5 }}
                        onClick={() => updateView({ type: v })}
                      >{label}</button>
                    ))}
                    <span style={{ margin: '0 4px', color: 'var(--border-color)' }}>|</span>
                    {[['all', 'All'], ['mine', 'Mine'], ['requested', 'Review requested'], ['drafts', 'Drafts']]
                      .filter(([v]) => view.type !== 'issues' || v === 'all' || v === 'mine')
                      .map(([v, label]) => (
                        <button key={v} className="btn btn-sm"
                          style={{ marginLeft: '2px', opacity: view.scope === v ? 1 : 0.5 }}
                          onClick={() => updateView({ scope: v })}
                        >{label}</button>
                      ))}
                    {view.type !== 'issues' && (
                      <label style={{ marginLeft: '6px', cursor: 'pointer' }}
                        title="Offer Review on your own PRs too. GitHub takes no approval or change request from a PR's author: submit the drafted review as a comment.">
                        <input type="checkbox" checked={selfReview} onChange={toggleSelfReview} style={{ marginRight: '4px', verticalAlign: 'middle' }} />
                        Review my PRs
                      </label>
                    )}
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
                {shown === WORK && (
                  <input
                    type="search"
                    value={search}
                    placeholder="search…"
                    title="Title, #number, author or label"
                    onChange={e => setSearch(e.target.value)}
                    style={{ width: '140px', padding: '3px 6px', borderRadius: '4px', border: '1px solid var(--border-color)', fontSize: 'small' }}
                  />
                )}
              </span>
            </nav>

            {shown === 'try' ? (
              <TryPanel boardName={activeBoard} onOpenSandbox={setCardSandbox} active={active} />
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
                  {!loadingWork && workError && <WorkErrorRow error={workError} />}
                  {listing && section('Active', busy.length)}
                  {listing && busy.map(row)}
                  {listing && !busy.length && (
                    <tr><td colSpan="5" style={{ padding: '8px 8px 12px', color: 'var(--status-green)' }}>
                      ✓ Nothing needs you right now.
                    </td></tr>
                  )}
                  {listing && done.length > 0 && section('Done', done.length)}
                  {listing && done.map(row)}
                  {listing && rest.length > 0 && section('Everything else', rest.length)}
                  {listing && rest.map(row)}
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
                Automation <span style={{ fontWeight: 400, color: 'var(--text-secondary)' }}>(what runs without a click, as you: one run per item a rule matches, recency-bounded)</span>
              </div>
              <AutoRules rules={spec.autoRules || []} recipes={spec.recipes || []} errors={spec.autoRuleErrors || []}
                onChange={autoRules => setSpec({ ...spec, autoRules })} />
              <div style={{ display: 'flex', gap: '12px', fontSize: 'small', flexWrap: 'wrap' }}>
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
              <SandboxSize spec={spec} onChange={setSpec} />
              <label style={{ cursor: 'pointer', fontSize: 'small' }} title="Turn auto on for each PR a fix opens: factory pr watch runs care on its new review comments and failed checks. Any PR of yours turns on from its auto chip.">
                <input type="checkbox" checked={!!spec.autoIterate} onChange={e => setSpec({ ...spec, autoIterate: e.target.checked })} style={{ marginRight: '6px' }} />
                Auto on PRs a fix opens
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

export { TryPanel, WorkRow, PRDeploy, SessionSlideOver, prRunName, anyPosting, withoutSelfReview };
export default Work;
