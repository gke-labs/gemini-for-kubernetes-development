import React, { useState, useEffect, useCallback, useRef } from 'react';
import ReactMarkdown from 'react-markdown';
// react-markdown is CommonMark only. Agents answer with GFM — tables
// above all — and without this a table parses as one paragraph, its
// newlines collapsing to spaces into a wall of pipes.
import remarkGfm from 'remark-gfm';

// Research: conversation-based deep research, the third mode alongside
// doc-based Explore and deploy-based Runs.
//
// The split this file lives on: creating a session goes through the
// board's mailbox (only the controller's image carries the factory CLI),
// but talking to one does not — acpd is plain HTTP on the sandbox pod and
// the API proxies it. So POST /api/board/:board/research is a *request*
// that a sandbox appear minutes later, while everything else here is
// addressed by session id alone.
//
// The transcript is the record. It lives as NDJSON on the sandbox's PVC,
// and the event stream is resumable by byte offset — so this component
// never has to hold conversation state that a reload would lose. It
// replays from 0 on open and from the last offset it saw on reconnect.

// ---------------------------------------------------------------------
// Pure helpers. Exported for the unit tests: everything below this line
// is decision-making that would otherwise only be reachable by driving a
// websocket.
// ---------------------------------------------------------------------

// repoFromURL mirrors the server's parseRepoURL, because a session view
// carries the repo name the server derived and the UI has only the
// board's repoURL to match it against. Same rule, same answer: exactly
// two path segments, .git stripped.
export function repoFromURL(repoURL) {
  const path = String(repoURL || '').replace(/^[a-zA-Z][a-zA-Z0-9+.-]*:\/\/[^/]*/, '');
  const parts = path.replace(/^\/+|\/+$/g, '').split('/');
  if (parts.length !== 2 || !parts[0] || !parts[1]) return '';
  return parts[1].replace(/\.git$/, '');
}

// chunkText pulls display text out of an ACP content payload.
//
// The shape differs by variant — a single ContentBlock for message and
// thought chunks, a list of tool-call content for tool calls — and the
// engine is allowed to add variants without repo-agent changing. So this
// walks whatever it is given and returns the text it can find rather
// than switching on a kind it may not know.
export function chunkText(content) {
  if (content === null || content === undefined) return '';
  if (typeof content === 'string') return content;
  if (Array.isArray(content)) return content.map(chunkText).join('');
  if (typeof content === 'object') {
    if (typeof content.text === 'string') return content.text;
    if (content.content !== undefined) return chunkText(content.content);
  }
  return '';
}

// An option whose kind starts with "allow" approves the tool call; the
// reject kinds deny it. Anything else is an engine-defined option we
// have no opinion about, and a neutral button is the honest rendering.
export function permissionTone(kind) {
  const k = String(kind || '');
  if (k.indexOf('allow') === 0) return 'allow';
  if (k.indexOf('reject') === 0) return 'reject';
  return 'neutral';
}

// shortSession is the id as a human refers to it. Full ids are UUIDs and
// never fit anywhere useful; the sandbox name is built from a digest
// prefix of the same id, so eight characters is already the granularity
// the rest of the system reads at.
export function shortSession(sessionID) {
  return String(sessionID || '').slice(0, 8);
}

export function ageOf(ts) {
  if (!ts) return '';
  const mins = Math.floor((Date.now() - new Date(ts).getTime()) / 60000);
  if (!isFinite(mins) || mins < 0) return '';
  if (mins < 60) return `${mins}m`;
  if (mins < 60 * 24) return `${Math.floor(mins / 60)}h`;
  return `${Math.floor(mins / (60 * 24))}d`;
}

// emptyTranscript is the fold's zero value.
//
// busy is tracked here rather than read from the session because the
// stream is the only thing that reports a turn ending. It is NOT
// authoritative during replay — see `busy` in ResearchConversation.
//
// mode carries the same caveat for the same reason: it is the last mode
// the transcript saw, which is the live one only once replay is done.
export const emptyTranscript = { items: [], plan: null, busy: false, stopReason: '', mode: '' };

// applyResearchEvent folds one transcript event into the render model.
//
// Two things make this worth isolating. Message chunks arrive one token
// at a time and have to coalesce into a single bubble, but only while
// nothing else interrupts them — a tool call between two runs of text is
// two bubbles, which is what the agent actually did. And tool calls are
// opened by one event and amended by later ones, so a tool row is
// identity-tracked by toolCallId rather than appended.
//
// Unknown kinds are rendered, not dropped: the engine forwards ACP
// session updates verbatim and is allowed to grow new ones.
export function applyResearchEvent(state, event) {
  if (!event || !event.kind) return state;
  const data = event.data || {};
  const items = state.items;
  const last = items.length ? items[items.length - 1] : null;
  const key = `e${event.seq}`;

  const push = (item) => ({ ...state, items: items.concat([{ key, ...item }]) });
  // Coalescing: extend the previous bubble when it is the same voice and
  // still the last thing said.
  const appendText = (role, text) => {
    if (!text) return state;
    if (last && last.role === role) {
      return { ...state, items: items.slice(0, -1).concat([{ ...last, text: last.text + text }]) };
    }
    return push({ role, text });
  };

  switch (event.kind) {
    case 'user_prompt':
      return { ...push({ role: 'user', text: data.text || '' }), busy: true, stopReason: '' };

    case 'user_message_chunk':
      return appendText('user', chunkText(data.content));

    case 'agent_message_chunk':
      return appendText('agent', chunkText(data.content));

    case 'agent_thought_chunk':
      return appendText('thought', chunkText(data.content));

    case 'tool_call':
      return push({
        role: 'tool',
        toolCallId: data.toolCallId || '',
        title: data.title || '',
        toolKind: data.kind || '',
        status: data.status || 'pending',
        rawInput: data.rawInput,
        content: chunkText(data.content),
      });

    case 'tool_call_update': {
      const id = data.toolCallId || '';
      let idx = -1;
      for (let i = items.length - 1; i >= 0; i--) {
        if (items[i].role === 'tool' && items[i].toolCallId === id) { idx = i; break; }
      }
      if (idx === -1) {
        // An update for a call we never saw the opening of — a stream
        // resumed mid-tool. Render it as its own row rather than drop
        // it: a tool that ran is worth showing even without its header.
        return push({
          role: 'tool',
          toolCallId: id,
          title: data.title || '',
          toolKind: data.kind || '',
          status: data.status || '',
          rawInput: data.rawInput,
          content: chunkText(data.content),
        });
      }
      const prev = items[idx];
      const next = items.slice();
      next[idx] = {
        ...prev,
        title: data.title || prev.title,
        toolKind: data.kind || prev.toolKind,
        status: data.status || prev.status,
        rawInput: data.rawInput === undefined ? prev.rawInput : data.rawInput,
        content: chunkText(data.content) || prev.content,
      };
      return { ...state, items: next };
    }

    // Two kinds, one meaning. mode_changed is acpd's own record of a
    // switch it made; current_mode_update is the engine reporting one it
    // made itself. Both say what the session is running under now, and a
    // reader wants the same thing from either — which is also what keeps
    // the header honest when the switch came from another tab.
    case 'mode_changed':
    case 'current_mode_update': {
      const next = data.currentModeId || '';
      if (!next) return state;
      return { ...push({ role: 'mode', mode: next }), mode: next };
    }

    // A plan update supersedes the last one rather than adding to the
    // transcript: it is the agent's current intent, not something it
    // said. Rendered once, next to the composer.
    case 'plan':
      return { ...state, plan: Array.isArray(data.entries) ? data.entries : null };

    case 'permission_request':
      return push({
        role: 'permission',
        requestId: data.requestId || '',
        toolCall: data.toolCall || null,
        options: Array.isArray(data.options) ? data.options : [],
        outcome: '',
      });

    case 'permission_resolved': {
      const id = data.requestId || '';
      let idx = -1;
      for (let i = items.length - 1; i >= 0; i--) {
        if (items[i].role === 'permission' && items[i].requestId === id) { idx = i; break; }
      }
      if (idx === -1) return state;
      const next = items.slice();
      next[idx] = {
        ...items[idx],
        outcome: data.outcome || 'selected',
        optionId: data.optionId || '',
        reason: data.reason || '',
      };
      return { ...state, items: next };
    }

    // end_turn is just a separator and adds no row. The other stop
    // reasons — cancelled, refusal, max_tokens — are the answer to "why
    // did it stop", so they get one.
    case 'turn_end': {
      const stopReason = data.stopReason || '';
      const ended = { ...state, busy: false, stopReason };
      if (!stopReason || stopReason === 'end_turn') return ended;
      return { ...ended, items: items.concat([{ key, role: 'stop', stopReason }]) };
    }

    // An engine that failed ends the turn without a turn_end — see the
    // prompt goroutine in factory/pkg/acpd. Clearing busy here is what
    // keeps the composer from staying dead after a crash.
    case 'error':
      return { ...push({ role: 'error', message: data.message || '', log: data.log || '' }), busy: false };

    default:
      return push({ role: 'unknown', kind: event.kind, data });
  }
}

export function buildResearchTranscript(events) {
  return (Array.isArray(events) ? events : []).reduce(applyResearchEvent, emptyTranscript);
}

// pendingPermission is the request the engine is blocked on, if any.
// Only one can be outstanding — the agent's handler waits on it — so the
// newest unresolved one is the one to answer.
export function pendingPermission(transcript) {
  const items = (transcript && transcript.items) || [];
  for (let i = items.length - 1; i >= 0; i--) {
    if (items[i].role === 'permission' && !items[i].outcome) return items[i];
  }
  return null;
}

// ---------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------

function Pill({ text, color, bg, title }) {
  if (!text) return null;
  return (
    <span title={title} style={{
      color, backgroundColor: bg,
      padding: '2px 8px', borderRadius: '10px',
      fontSize: 'x-small', whiteSpace: 'nowrap',
    }}>{text}</span>
  );
}

const TOOL_STATUS_COLOR = {
  completed: 'var(--status-green)',
  failed: 'var(--text-danger)',
  in_progress: '#b08800',
  pending: 'var(--text-secondary)',
};

function ToolRow({ item }) {
  const [open, setOpen] = useState(false);
  const detail = [
    item.rawInput === undefined ? '' : JSON.stringify(item.rawInput, null, 2),
    item.content || '',
  ].filter(Boolean).join('\n\n');
  return (
    <div style={{
      border: '1px solid var(--border-color)', borderRadius: '8px',
      padding: '6px 10px', margin: '6px 0', background: 'var(--bg-secondary)',
      fontSize: 'small',
    }}>
      <div onClick={() => detail && setOpen(o => !o)}
        style={{ display: 'flex', alignItems: 'center', gap: '8px', cursor: detail ? 'pointer' : 'default' }}>
        {detail && <span style={{ fontSize: 'x-small', color: 'var(--text-secondary)' }}>{open ? '▾' : '▸'}</span>}
        <span style={{ fontFamily: 'monospace', color: 'var(--text-secondary)' }}>{item.toolKind || 'tool'}</span>
        <span style={{ flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
          {item.title || item.toolCallId}
        </span>
        <Pill text={item.status} color={TOOL_STATUS_COLOR[item.status] || 'var(--text-secondary)'} bg="transparent" />
      </div>
      {open && detail && (
        <pre style={{
          margin: '6px 0 0', maxHeight: '240px', overflow: 'auto', fontSize: 'x-small',
          background: 'var(--bg-card)', padding: '8px', borderRadius: '6px', whiteSpace: 'pre-wrap',
        }}>{detail}</pre>
      )}
    </div>
  );
}

function ThoughtRow({ item }) {
  const [open, setOpen] = useState(false);
  return (
    <div style={{ margin: '4px 0', fontSize: 'small' }}>
      <span onClick={() => setOpen(o => !o)}
        style={{ cursor: 'pointer', color: 'var(--text-muted)', fontStyle: 'italic' }}>
        {open ? '▾' : '▸'} thinking
      </span>
      {open && (
        <div style={{
          color: 'var(--text-muted)', fontStyle: 'italic', whiteSpace: 'pre-wrap',
          borderLeft: '2px solid var(--border-color)', paddingLeft: '10px', marginTop: '4px',
        }}>{item.text}</div>
      )}
    </div>
  );
}

function PermissionRow({ item, onResolve, busy }) {
  const toolCall = item.toolCall || {};
  const resolved = !!item.outcome;
  return (
    <div style={{
      border: `1px solid ${resolved ? 'var(--border-color)' : '#b08800'}`,
      borderRadius: '8px', padding: '10px', margin: '8px 0',
      background: resolved ? 'var(--bg-secondary)' : 'rgba(176,136,0,0.10)',
      fontSize: 'small',
    }}>
      <div style={{ marginBottom: resolved ? 0 : '8px' }}>
        <strong>{resolved ? 'Permission' : 'The agent needs permission'}</strong>
        {toolCall.title && <span> — {toolCall.title}</span>}
        {toolCall.kind && (
          <span style={{ fontFamily: 'monospace', color: 'var(--text-secondary)' }}> ({toolCall.kind})</span>
        )}
      </div>
      {resolved ? (
        <div style={{ color: 'var(--text-secondary)' }}>
          {item.outcome === 'cancelled' ? 'cancelled' : `allowed: ${item.optionId || '—'}`}
          {/* A reason is set only when acpd answered for us, which today
              means the ten-minute timeout ran out. Saying so is the
              difference between "you did this" and "nobody did". */}
          {item.reason && <span> — {item.reason}</span>}
        </div>
      ) : (
        <div style={{ display: 'flex', gap: '6px', flexWrap: 'wrap' }}>
          {item.options.map(opt => {
            const tone = permissionTone(opt.kind);
            return (
              <button key={opt.optionId} className="btn btn-sm" disabled={busy}
                title={opt.kind}
                onClick={() => onResolve({ requestId: item.requestId, optionId: opt.optionId })}
                style={{
                  borderColor: tone === 'allow' ? 'var(--status-green)' : tone === 'reject' ? 'var(--text-danger)' : undefined,
                  color: tone === 'allow' ? 'var(--status-green)' : tone === 'reject' ? 'var(--text-danger)' : undefined,
                }}>{opt.name || opt.optionId}</button>
            );
          })}
          <button className="btn btn-sm" disabled={busy}
            title="Answer nothing and end the tool call"
            onClick={() => onResolve({ requestId: item.requestId, cancelled: true })}>Cancel</button>
        </div>
      )}
    </div>
  );
}

function TranscriptItem({ item, onResolve, resolving }) {
  switch (item.role) {
    case 'user':
      return (
        <div style={{ display: 'flex', justifyContent: 'flex-end', margin: '10px 0' }}>
          <div style={{
            maxWidth: '78%', background: 'var(--bg-secondary)', border: '1px solid var(--border-color)',
            borderRadius: '12px', padding: '8px 12px', whiteSpace: 'pre-wrap', textAlign: 'left',
          }}>{item.text}</div>
        </div>
      );
    case 'agent':
      return (
        <div className="research-markdown md-body" style={{ margin: '10px 0', lineHeight: 1.5 }}>
          <ReactMarkdown remarkPlugins={[remarkGfm]}>{item.text}</ReactMarkdown>
        </div>
      );
    case 'thought':
      return <ThoughtRow item={item} />;
    case 'tool':
      return <ToolRow item={item} />;
    case 'permission':
      return <PermissionRow item={item} onResolve={onResolve} busy={resolving} />;
    // Marked in the body of the transcript, not only in the header,
    // because it is the answer to "why was nothing asked before that
    // command ran" — and the answer depends on when it changed.
    case 'mode':
      return (
        <div style={{ margin: '8px 0', fontSize: 'x-small', color: 'var(--text-secondary)', fontStyle: 'italic' }}>
          approval mode: {item.mode}
        </div>
      );
    case 'stop':
      return (
        <div style={{ margin: '8px 0', fontSize: 'x-small', color: 'var(--text-secondary)', fontStyle: 'italic' }}>
          turn ended: {item.stopReason}
        </div>
      );
    case 'error':
      return (
        <div style={{
          margin: '8px 0', padding: '8px 10px', borderRadius: '8px', fontSize: 'small',
          border: '1px solid var(--border-danger)', background: 'var(--bg-danger-light)', color: 'var(--text-danger)',
        }}>
          {item.message}
          {item.log && (
            <div style={{ fontSize: 'x-small', marginTop: '4px', fontFamily: 'monospace' }}>
              engine stderr in the sandbox: {item.log}
            </div>
          )}
        </div>
      );
    default:
      // A session update this build has never heard of. It reached the
      // browser on purpose; showing it raw beats pretending it did not
      // happen.
      return (
        <details style={{ margin: '6px 0', fontSize: 'x-small', color: 'var(--text-secondary)' }}>
          <summary style={{ cursor: 'pointer' }}>{item.kind}</summary>
          <pre style={{ whiteSpace: 'pre-wrap', overflow: 'auto' }}>{JSON.stringify(item.data, null, 2)}</pre>
        </details>
      );
  }
}

// TerminalItem is the same transcript rendered the way a terminal would
// render it: no markdown parsing at all, the agent's source shown as it
// was written, in a fixed-width font on a flat log rather than bubbles.
//
// It exists because the rich view is lossy in one direction. Markdown is
// a rendering *of* something, and a research answer often wants reading
// as the thing itself — a table's exact columns, a diff, a command to
// copy out unchanged. The rich view is better for prose; this one is
// better for anything you intend to use.
//
// Permission prompts keep their rich form. They are the one part of a
// transcript that is a control and not a record, and a misread
// permission is a worse outcome than a seam in the styling.
function TerminalItem({ item, onResolve, resolving }) {
  const [open, setOpen] = useState(false);

  switch (item.role) {
    case 'user':
      return (
        <div className="term-line term-user"><span className="term-sigil">❯ </span>{item.text}</div>
      );
    case 'agent':
      return <div className="term-line term-agent">{item.text}</div>;
    case 'thought':
      return (
        <div className="term-line term-dim" onClick={() => setOpen(o => !o)} style={{ cursor: 'pointer' }}>
          <span className="term-sigil">{open ? '▾ ' : '▸ '}</span>thinking
          {open && <div className="term-quote">{item.text}</div>}
        </div>
      );
    case 'tool': {
      const detail = [
        item.rawInput === undefined ? '' : JSON.stringify(item.rawInput, null, 2),
        item.content || '',
      ].filter(Boolean).join('\n\n');
      return (
        <div className="term-line">
          <span onClick={() => detail && setOpen(o => !o)} style={{ cursor: detail ? 'pointer' : 'default' }}>
            <span className={`term-sigil term-status-${item.status || 'pending'}`}>● </span>
            <span className="term-dim">{item.toolKind || 'tool'} </span>
            {item.title || item.toolCallId}
            <span className={`term-status-${item.status || 'pending'}`}> [{item.status || 'pending'}]</span>
          </span>
          {open && detail && <div className="term-quote">{detail}</div>}
        </div>
      );
    }
    case 'permission':
      return <PermissionRow item={item} onResolve={onResolve} busy={resolving} />;
    case 'mode':
      return <div className="term-line term-dim">— approval mode: {item.mode} —</div>;
    case 'stop':
      return <div className="term-line term-dim">— turn ended: {item.stopReason} —</div>;
    case 'error':
      return (
        <div className="term-line term-error">
          ! {item.message}
          {item.log && <div className="term-quote">engine stderr in the sandbox: {item.log}</div>}
        </div>
      );
    default:
      return (
        <div className="term-line term-dim" onClick={() => setOpen(o => !o)} style={{ cursor: 'pointer' }}>
          <span className="term-sigil">{open ? '▾ ' : '▸ '}</span>{item.kind}
          {open && <div className="term-quote">{JSON.stringify(item.data, null, 2)}</div>}
        </div>
      );
  }
}

function PlanPanel({ entries }) {
  const [open, setOpen] = useState(true);
  if (!entries || !entries.length) return null;
  const mark = { completed: '✓', in_progress: '◐', pending: '○' };
  return (
    <div style={{
      border: '1px solid var(--border-color)', borderRadius: '8px',
      background: 'var(--bg-secondary)', padding: '6px 10px', marginBottom: '8px', fontSize: 'small',
    }}>
      <div onClick={() => setOpen(o => !o)} style={{ cursor: 'pointer', color: 'var(--text-secondary)' }}>
        {open ? '▾' : '▸'} plan ({entries.filter(e => e.status === 'completed').length}/{entries.length})
      </div>
      {open && entries.map((e, i) => (
        <div key={i} style={{
          padding: '2px 0 2px 14px',
          color: e.status === 'completed' ? 'var(--text-muted)' : 'var(--text-primary)',
          textDecoration: e.status === 'completed' ? 'line-through' : 'none',
        }}>
          <span style={{ marginRight: '6px' }}>{mark[e.status] || '○'}</span>{e.content}
        </div>
      ))}
    </div>
  );
}

// How long to wait before re-probing a session that is not reachable
// yet. A research sandbox is an image pull, a PVC and a clone, so this
// is minutes of polling — slow enough not to hammer the API, fast enough
// that the conversation opens on its own when the pod lands.
const PROBE_INTERVAL_MS = 5000;

// Where the rich/terminal choice is remembered.
const RESEARCH_VIEW_KEY = 'repoboard.research.view';

// ResearchConversation is one conversation: the transcript, the
// composer, and the machinery that keeps a websocket attached to it.
//
// `pending` softens the 404: for a session whose claim was filed seconds
// ago, "not found" means the controller has not made the sandbox yet,
// while for any other session it means the sandbox is gone. The caller
// knows which; this component cannot.
export function ResearchConversation({ sessionId, pending, title, onBack, onDeleted, onRenamed, fill }) {
  // phase: what we are waiting on, and therefore what to render.
  //   probing  — asking whether the sandbox can be talked to
  //   starting — it exists but has no running pod yet
  //   paused   — scaled to zero; the transcript survives, the engine does not
  //   gone     — no such session
  //   live     — websocket attached
  //   failed   — the API said something we cannot recover from
  const [phase, setPhase] = useState('probing');
  const [detail, setDetail] = useState('');
  const [info, setInfo] = useState(null);
  // The session's name, and the draft while it is being edited (null
  // when it is not). Seeded from the row that was clicked so the header
  // reads right before the probe answers.
  const [name, setName] = useState(title || '');
  const [renaming, setRenaming] = useState(null);
  // Mirrored so the probe's answer can tell "nobody is editing" without
  // making itself depend on the draft.
  const renamingRef = useRef(null);
  const editName = (draftName) => { renamingRef.current = draftName; setRenaming(draftName); };
  const [transcript, setTranscript] = useState(emptyTranscript);
  const [openBusy, setOpenBusy] = useState(false);
  const [caughtUp, setCaughtUp] = useState(false);
  const [draft, setDraft] = useState('');
  const [sending, setSending] = useState(false);
  const [resolving, setResolving] = useState(false);
  const [error, setError] = useState('');
  // The approval mode the session is in, and the set the engine will
  // accept. Seeded from whichever of the probe and the open frame
  // answers first; the transcript takes over once it is caught up,
  // because a mode switched in another tab — or left by the engine on
  // its own — arrives as an event and never as a reply to us.
  const [modeState, setModeState] = useState({ current: '', available: [] });
  const [switching, setSwitching] = useState(false);
  // rich | terminal. A reading preference, not session state, so it is
  // remembered across conversations and across the pop-out window —
  // whoever wants the terminal wants it for all of them.
  const [view, setView] = useState(() => {
    try { return localStorage.getItem(RESEARCH_VIEW_KEY) === 'terminal' ? 'terminal' : 'rich'; }
    catch (e) { return 'rich'; } // private mode
  });
  const terminal = view === 'terminal';
  const chooseView = (next) => {
    setView(next);
    try { localStorage.setItem(RESEARCH_VIEW_KEY, next); } catch (e) { /* private mode */ }
  };

  // The resume cursor. A ref, not state: the socket's onmessage handler
  // closes over it, and it must never be a render behind.
  const offsetRef = useRef(0);
  // The replay/live boundary: the transcript's length when we attached.
  // Everything at or below it is history being replayed.
  const boundaryRef = useRef(0);
  // Whether this session was ever reachable. It is what lets a 404 mean
  // "deleted" for a session we have talked to, while still meaning "not
  // created yet" for one whose claim was filed a moment ago.
  const everLiveRef = useRef(false);
  const scrollRef = useRef(null);
  const stickRef = useRef(true);

  // Busy is the composer's gate, and during replay the transcript is the
  // wrong source for it. An acpd that restarted leaves a user_prompt
  // with no turn_end on disk forever, so folding history would say "a
  // turn is in flight" about an engine that no longer exists. The open
  // frame's session.busy is the live answer; the fold only takes over
  // once something new has actually happened.
  const busy = caughtUp ? transcript.busy : openBusy;

  // Probe, then attach. Split in two because the websocket cannot report
  // "not up yet" — resolveResearch answers 409 before the upgrade, which
  // the browser sees only as a socket that would not open.
  useEffect(() => {
    if (!sessionId) return undefined;
    const state = { closed: false, ws: null, timer: null };

    const later = (fn, ms) => { state.timer = setTimeout(fn, ms); };

    const attach = () => {
      if (state.closed) return;
      const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const url = `${proto}//${window.location.host}/api/research-events/${encodeURIComponent(sessionId)}?offset=${offsetRef.current}`;
      const ws = new WebSocket(url);
      state.ws = ws;
      ws.onmessage = (ev) => {
        let frame;
        try { frame = JSON.parse(ev.data); } catch (e) { return; }
        if (frame.type === 'open') {
          const session = frame.session || {};
          everLiveRef.current = true;
          // session.offset is the transcript's length at the moment acpd
          // answered — exactly where replay stops and live begins.
          boundaryRef.current = typeof session.offset === 'number' ? session.offset : offsetRef.current;
          setPhase('live');
          setError('');
          setOpenBusy(!!session.busy);
          setCaughtUp(false);
          setModeState(m => ({
            current: session.mode || m.current,
            available: session.availableModes || m.available,
          }));
          return;
        }
        if (frame.type === 'event' && frame.event) {
          // The offset AFTER the event: store it before rendering, so a
          // socket that dies during this handler still resumes past it.
          if (typeof frame.offset === 'number') {
            // Past the boundary means this is new, not replayed — which
            // is when the folded busy flag becomes the truthful one.
            if (frame.offset > boundaryRef.current) setCaughtUp(true);
            offsetRef.current = frame.offset;
          }
          setTranscript(prev => applyResearchEvent(prev, frame.event));
          return;
        }
        if (frame.type === 'closed') {
          if (frame.error) setError(frame.error);
          // Do not reconnect from here: the socket's own onclose runs
          // next and owns the retry, so doing it twice would double the
          // connection rate on a session that is failing to start.
        }
      };
      ws.onclose = () => {
        if (state.closed) return;
        state.ws = null;
        // Back to probing rather than straight to a reconnect: the usual
        // reason a stream ends is that the sandbox went away, and the
        // probe is what tells the difference between that and a blip.
        setPhase(p => (p === 'live' ? 'probing' : p));
        later(probe, PROBE_INTERVAL_MS);
      };
    };

    const probe = () => {
      if (state.closed) return;
      fetch(`/api/research/${encodeURIComponent(sessionId)}`)
        .then(res => res.json().then(body => ({ status: res.status, body })))
        .then(({ status, body }) => {
          if (state.closed) return;
          if (status === 200) {
            setInfo(body);
            setModeState(m => ({
              current: body.mode || m.current,
              available: body.availableModes || m.available,
            }));
            if (body.unreachable) {
              // The sandbox is up but acpd is not answering. Said
              // separately from "still starting" because the remedy is
              // different: this is the shape a broken image takes, and
              // waiting will not fix it.
              setPhase('unreachable');
              setDetail(body.unreachable);
              later(probe, PROBE_INTERVAL_MS);
              return;
            }
            setDetail('');
            attach();
            return;
          }
          if (status === 409) {
            setPhase(body.paused ? 'paused' : 'starting');
            setDetail(body.error || '');
            later(probe, PROBE_INTERVAL_MS);
            return;
          }
          if (status === 404) {
            if (pending && !everLiveRef.current) {
              setPhase('starting');
              setDetail('waiting for the controller to create the sandbox');
              later(probe, PROBE_INTERVAL_MS);
              return;
            }
            setPhase('gone');
            return;
          }
          setPhase('failed');
          setDetail((body && body.error) || `HTTP ${status}`);
          later(probe, PROBE_INTERVAL_MS);
        })
        .catch(err => {
          if (state.closed) return;
          setPhase('probing');
          setDetail(String(err));
          later(probe, PROBE_INTERVAL_MS);
        });
    };

    // A fresh session id is a fresh transcript: reset the cursor too, or
    // the next conversation opens part-way through.
    offsetRef.current = 0;
    boundaryRef.current = 0;
    everLiveRef.current = false;
    setTranscript(emptyTranscript);
    setCaughtUp(false);
    setModeState({ current: '', available: [] });
    setPhase('probing');
    setDetail('');
    probe();

    return () => {
      state.closed = true;
      if (state.timer) clearTimeout(state.timer);
      if (state.ws) { try { state.ws.close(); } catch (e) { /* already gone */ } }
    };
  }, [sessionId, pending]);

  // Follow the tail, but only for a reader who is already at it —
  // yanking the view down while someone is reading back is worse than
  // making them scroll.
  useEffect(() => {
    const el = scrollRef.current;
    if (el && stickRef.current) el.scrollTop = el.scrollHeight;
  }, [transcript, phase]);

  // A different session is a different name; the seed from the list is
  // the best one available until the probe answers with the stored one.
  useEffect(() => { setName(title || ''); editName(null); }, [sessionId, title]);
  useEffect(() => {
    // Never while the member is typing over it: the probe re-runs on
    // every reconnect, and it must not eat an edit in progress.
    if (info && info.title && renamingRef.current === null) setName(info.title);
  }, [info]);

  const send = () => {
    const text = draft.trim();
    if (!text || sending || busy || phase !== 'live') return;
    setSending(true);
    setError('');
    fetch(`/api/research/${encodeURIComponent(sessionId)}/prompt`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ text }),
    })
      .then(async res => {
        if (res.ok) {
          // The turn appears on the stream as a user_prompt event, which
          // is what draws the bubble and sets busy. Nothing to do here
          // but stop holding the text the member already sent.
          setDraft('');
          stickRef.current = true;
          return;
        }
        const body = await res.json().catch(() => ({}));
        setError(body.error || `send failed: HTTP ${res.status}`);
      })
      .catch(err => setError(`send failed: ${err}`))
      .finally(() => setSending(false));
  };

  const commitRename = () => {
    const next = (renaming || '').trim();
    editName(null);
    if (!next || next === name) return;
    // Shown immediately and corrected by the answer: the server
    // truncates, so what comes back is what the name actually is.
    setName(next);
    fetch(`/api/research/${encodeURIComponent(sessionId)}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ title: next }),
    })
      .then(async res => {
        const body = await res.json().catch(() => ({}));
        if (!res.ok) { setError(body.error || `rename failed: HTTP ${res.status}`); return; }
        if (body.title) setName(body.title);
        if (onRenamed) onRenamed(sessionId, body.title || next);
      })
      .catch(err => setError(`rename failed: ${err}`));
  };

  const resolve = (resolution) => {
    setResolving(true);
    fetch(`/api/research/${encodeURIComponent(sessionId)}/permission`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(resolution),
    })
      .then(async res => {
        if (res.ok) return;
        const body = await res.json().catch(() => ({}));
        setError(body.error || `permission failed: HTTP ${res.status}`);
      })
      .catch(err => setError(`permission failed: ${err}`))
      .finally(() => setResolving(false));
  };

  // Switching mid-turn is deliberate: the reason to reach for this is
  // usually a prompt that has just appeared, and making the member stop
  // the turn first would throw away the work that produced it.
  const chooseMode = (next) => {
    if (!next || switching) return;
    setSwitching(true);
    setError('');
    fetch(`/api/research/${encodeURIComponent(sessionId)}/mode`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ mode: next }),
    })
      .then(async res => {
        const body = await res.json().catch(() => ({}));
        if (!res.ok) { setError(body.error || `mode change failed: HTTP ${res.status}`); return; }
        setModeState(m => ({
          current: body.mode || next,
          available: body.availableModes || m.available,
        }));
      })
      .catch(err => setError(`mode change failed: ${err}`))
      .finally(() => setSwitching(false));
  };

  const cancel = () => {
    fetch(`/api/research/${encodeURIComponent(sessionId)}/cancel`, { method: 'POST' })
      .then(async res => {
        if (res.ok) return;
        const body = await res.json().catch(() => ({}));
        setError(body.error || `cancel failed: HTTP ${res.status}`);
      })
      .catch(err => setError(`cancel failed: ${err}`));
  };

  const destroy = () => {
    if (!window.confirm('Delete this conversation? The sandbox and its transcript go with it.')) return;
    fetch(`/api/research/${encodeURIComponent(sessionId)}`, { method: 'DELETE' })
      .then(res => {
        if (res.ok || res.status === 404) { if (onDeleted) onDeleted(sessionId); return; }
        res.json().catch(() => ({})).then(body => setError(body.error || `delete failed: HTTP ${res.status}`));
      })
      .catch(err => setError(`delete failed: ${err}`));
  };

  const waiting = pendingPermission(transcript);
  const statusPill = {
    probing: { text: 'connecting…', color: 'var(--text-secondary)', bg: 'var(--bg-secondary)' },
    starting: { text: 'sandbox starting…', color: '#b08800', bg: 'rgba(176,136,0,0.12)' },
    paused: { text: 'paused', color: 'var(--text-secondary)', bg: 'var(--bg-secondary)' },
    unreachable: { text: 'not answering', color: 'var(--text-danger)', bg: 'var(--bg-danger-light)' },
    gone: { text: 'gone', color: 'var(--text-danger)', bg: 'var(--bg-danger-light)' },
    failed: { text: 'unavailable', color: 'var(--text-danger)', bg: 'var(--bg-danger-light)' },
    live: busy
      ? { text: 'agent working', color: '#b08800', bg: 'rgba(176,136,0,0.12)' }
      : { text: 'ready', color: 'var(--status-green)', bg: 'rgba(40,167,69,0.12)' },
  }[phase];

  const repo = (info && info.repo) || '';
  const composerDisabled = phase !== 'live' || busy || sending;

  // Same split as `busy`: the fold is the truthful source only once
  // replay is behind us, and until then the session we attached to is.
  const mode = (caughtUp && transcript.mode) || modeState.current;
  // A mode the engine no longer advertises still gets an entry, so the
  // control shows what the session is in rather than an empty box —
  // the list comes from the engine and can change under a live session.
  const modeOptions = !mode || modeState.available.some(m => m.id === mode)
    ? modeState.available
    : modeState.available.concat([{ id: mode, name: mode }]);

  return (
    <div style={fill
      ? { flex: '1 1 auto', display: 'flex', flexDirection: 'column', minHeight: 0, padding: '0 14px 12px' }
      : { display: 'flex', flexDirection: 'column', minHeight: 0, maxHeight: '72vh' }}>
      <div style={{
        display: 'flex', alignItems: 'center', gap: '8px', flexWrap: 'wrap',
        padding: '8px 0', fontSize: 'small', flex: '0 0 auto',
      }}>
        {onBack && <button className="btn btn-sm" onClick={onBack}>← Sessions</button>}
        {/* The name, editable in place. A session is found again by what
            it was about, so the title is the one thing here worth the
            width — the repo and the id follow it, quietly. */}
        {renaming === null ? (
          <strong onClick={() => editName(name)} style={{ cursor: 'text' }}
            title="Click to rename this conversation">
            {name || repo || 'research'}
          </strong>
        ) : (
          <input autoFocus value={renaming} aria-label="Session title"
            onChange={e => editName(e.target.value)}
            onBlur={commitRename}
            onKeyDown={e => {
              if (e.key === 'Enter') { e.preventDefault(); commitRename(); }
              if (e.key === 'Escape') { e.preventDefault(); editName(null); }
            }}
            style={{
              font: 'inherit', fontWeight: 600, padding: '1px 6px', minWidth: '220px',
              border: '1px solid var(--border-color)', borderRadius: '6px',
              background: 'var(--bg-card)', color: 'var(--text-primary)',
            }} />
        )}
        {name && repo && <span style={{ color: 'var(--text-secondary)' }}>{repo}</span>}
        <span style={{ fontFamily: 'monospace', color: 'var(--text-secondary)' }}>{shortSession(sessionId)}</span>
        <Pill {...statusPill} title={detail} />
        <span style={{ flex: 1 }} />
        {/* How much the engine asks before it acts. A research session
            starts auto-approving — nobody is necessarily watching one,
            and a prompt nobody answers stalls the turn until acpd
            cancels it — so this is here to tighten that, not to loosen
            it. Hidden entirely for an engine that offers no modes:
            there is nothing to choose between. */}
        {modeOptions.length > 0 && (
          <label style={{
            display: 'inline-flex', alignItems: 'center', gap: '4px',
            fontSize: 'x-small', color: 'var(--text-secondary)',
          }}>
            approvals
            <select value={mode} aria-label="Approval mode"
              disabled={phase !== 'live' || switching}
              onChange={e => chooseMode(e.target.value)}
              title={(modeOptions.find(m => m.id === mode) || {}).description
                || 'How much the engine asks before it acts'}
              style={{
                font: 'inherit', padding: '1px 4px', borderRadius: '6px',
                border: '1px solid var(--border-color)',
                background: 'var(--bg-card)', color: 'var(--text-primary)',
              }}>
              {modeOptions.map(m => (
                <option key={m.id} value={m.id} title={m.description || ''}>{m.name || m.id}</option>
              ))}
            </select>
          </label>
        )}
        {/* Two segments rather than one flip button: which view you are
            in should be readable without knowing whether the label names
            the state or the action. */}
        <span style={{
          display: 'inline-flex', border: '1px solid var(--border-color)',
          borderRadius: '6px', overflow: 'hidden',
        }}>
          {[
            ['rich', 'Rendered markdown'],
            ['terminal', 'The transcript as a terminal would print it — fixed-width, unrendered source'],
          ].map(([v, hint]) => (
            <button key={v} onClick={() => chooseView(v)} title={hint}
              style={{
                border: 'none', padding: '2px 8px', fontSize: 'x-small', cursor: 'pointer',
                background: view === v ? 'var(--bg-hover)' : 'transparent',
                color: view === v ? 'var(--text-primary)' : 'var(--text-secondary)',
                fontWeight: view === v ? 600 : 400,
              }}>{v}</button>
          ))}
        </span>
        {phase === 'live' && busy && (
          <button className="btn btn-sm" onClick={cancel} title="Interrupt the turn in flight">Stop</button>
        )}
        {info && info.sandbox && info.namespace && (
          <a href={`#/terminal/${info.namespace}/${info.sandbox}`} target="_blank" rel="noopener noreferrer"
            style={{ fontSize: 'x-small' }} title={`Shell into ${info.sandbox}`}>terminal ↗</a>
        )}
        {!fill && (
          <a href={`#/research/${sessionId}`} target="_blank" rel="noopener noreferrer"
            style={{ fontSize: 'x-small' }} title="Open this conversation in its own window">pop out ↗</a>
        )}
        <button className="btn btn-delete btn-sm" onClick={destroy}
          title="Delete the sandbox — the transcript lives on its disk and goes with it">Delete</button>
      </div>

      {error && (
        <div className="warning-banner" style={{ cursor: 'pointer', flex: '0 0 auto' }}
          onClick={() => setError('')} title="Dismiss">{error}</div>
      )}

      <div ref={scrollRef}
        className={terminal ? 'research-terminal' : undefined}
        onScroll={e => {
          const el = e.currentTarget;
          stickRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
        }}
        style={{
          flex: '1 1 auto', minHeight: fill ? 0 : '240px', overflowY: 'auto', textAlign: 'left',
          border: '1px solid var(--border-color)', borderRadius: '10px',
          // The terminal canvas is its own colour, and an inline
          // background would win over the class that sets it.
          background: terminal ? undefined : 'var(--bg-card)',
          padding: '10px 14px',
        }}>
        {phase === 'gone' && (
          <p style={{ color: 'var(--text-secondary)' }}>
            This session no longer exists — the sandbox that held its transcript has been deleted.
          </p>
        )}
        {phase === 'paused' && (
          <p style={{ color: 'var(--text-secondary)' }}>
            This session is paused: the sandbox is scaled to zero. Its transcript survives on the
            sandbox's disk, but nothing is running to answer. {detail}
          </p>
        )}
        {(phase === 'starting' || phase === 'probing') && !transcript.items.length && (
          <p style={{ color: 'var(--text-secondary)', fontStyle: 'italic' }}>
            {phase === 'starting'
              ? `Preparing the sandbox — image pull, disk and clone take a few minutes.${detail ? ` (${detail})` : ''}`
              : 'Connecting…'}
          </p>
        )}
        {phase === 'unreachable' && (
          <p style={{ color: 'var(--text-danger)' }}>
            The sandbox is running but its conversation server is not answering: {detail}
          </p>
        )}
        {phase === 'failed' && (
          <p style={{ color: 'var(--text-danger)' }}>Cannot reach this session: {detail}</p>
        )}
        {phase === 'live' && !transcript.items.length && (
          info && info.openingError ? (
            <p style={{ color: 'var(--text-danger)' }}>
              The opening question was never delivered: {info.openingError}. Ask it yourself below —
              the sandbox itself is fine.
            </p>
          ) : info && info.opening ? (
            <p style={{ color: 'var(--text-secondary)', fontStyle: 'italic' }}>
              Sending the opening question…
            </p>
          ) : (
            <p style={{ color: 'var(--text-secondary)', fontStyle: 'italic' }}>
              Nothing said yet. Ask about {repo ? `${repo}` : 'the repository'} — the agent has the
              checkout in front of it.
            </p>
          )
        )}
        {transcript.items.map(item => (terminal
          ? <TerminalItem key={item.key} item={item} onResolve={resolve} resolving={resolving} />
          : <TranscriptItem key={item.key} item={item} onResolve={resolve} resolving={resolving} />
        ))}
        {busy && !waiting && (
          <div style={{ color: 'var(--text-secondary)', fontStyle: 'italic', fontSize: 'small', margin: '8px 0' }}>
            working…
          </div>
        )}
      </div>

      <div style={{ flex: '0 0 auto', marginTop: '8px' }}>
        <PlanPanel entries={transcript.plan} />
        <div style={{
          border: '1px solid var(--border-color)', borderRadius: '10px',
          background: 'var(--bg-secondary)', padding: '10px 12px',
        }}>
          <textarea rows={3} value={draft} onChange={e => setDraft(e.target.value)}
            disabled={phase !== 'live'}
            placeholder={
              phase !== 'live' ? 'Not connected yet…'
                : waiting ? 'The agent is waiting on a permission above…'
                  : busy ? 'The agent is working — Stop to interrupt…'
                    : 'Ask a question about this repository…'
            }
            onKeyDown={e => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send(); } }}
            style={{
              width: '100%', border: 'none', outline: 'none', resize: 'none',
              background: 'transparent', color: 'var(--text-primary)',
              font: 'inherit', boxSizing: 'border-box',
            }} />
          <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: '6px' }}>
            <button className="btn btn-sm" disabled={!draft.trim() || composerDisabled} onClick={send}>
              {sending ? 'Sending…' : 'Send'}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}

// ResearchPanel is the board's Research tab: the ways to start a
// conversation about this repository, the conversations already
// started, and the conversation itself once one is open.
//
// The three starters differ only in who writes the first message. Two
// are canned — the overview read, the recent-activity digest — and one
// is whatever the member types. All of them produce the same thing: an
// ordinary session you can ask a follow-up in, which is why the old
// Explore tab collapsed into this one.
//
// The list comes from /api/research, which is namespace-wide and knows
// nothing about boards — a session carries only the repo it was started
// on. So the board's repoURL is reduced to a repo name the same way the
// server does it, and matched. Sessions for any other repo are still
// shown, behind a disclosure: they are the member's, they cost a disk,
// and hiding them because their board was deleted would make them
// unreachable.
export function ResearchPanel({ boardName, repoURL }) {
  const [sessions, setSessions] = useState(null);
  const [open, setOpen] = useState(null); // { sessionId, pending, title }
  const [busy, setBusy] = useState(''); // which starter is in flight
  const [topic, setTopic] = useState('');
  const [title, setTitle] = useState('');
  const [sinceOpen, setSinceOpen] = useState(false);
  const [error, setError] = useState('');
  const [showOthers, setShowOthers] = useState(false);
  const [forkOwner, setForkOwner] = useState('');

  const load = useCallback(() => {
    fetch('/api/research')
      .then(res => (res.ok ? res.json() : Promise.reject(res.statusText)))
      .then(data => {
        setSessions(Array.isArray(data.sessions) ? data.sessions : []);
        setForkOwner(data.forkOwner || '');
      })
      .catch(err => { setSessions([]); setError(`Could not list research sessions: ${err}`); });
  }, []);

  // Poll while the tab is open. A newly claimed session takes minutes to
  // become a sandbox; until it does it is a standing claim, which the
  // list reports as a requested row.
  useEffect(() => {
    load();
    const t = setInterval(() => { if (!document.hidden) load(); }, 10000);
    return () => clearInterval(t);
  }, [load]);

  const repo = repoFromURL(repoURL);

  // start files a claim. `enter` opens the conversation straight away:
  // right for a question you just typed and want the answer to, wrong
  // for a canned read you fire and come back to — that one leaves you on
  // the list, where its row appears immediately so nobody clicks twice
  // and pays for a second sandbox.
  const start = (kickoff, enter) => {
    const what = kickoff.kind || 'new';
    setBusy(what);
    setError('');
    fetch(`/api/board/${boardName}/research`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(kickoff),
    })
      .then(async res => {
        const body = await res.json().catch(() => ({}));
        if (!res.ok) { setError(body.error || `could not start a session: HTTP ${res.status}`); return; }
        if (enter) {
          // `pending` is what stops the 404 in the meantime reading as
          // "this session does not exist".
          setOpen({ sessionId: body.sessionId, pending: true, title: body.title || '' });
        } else {
          setSessions(prev => [{
            sessionId: body.sessionId,
            title: body.title || '',
            repo,
            requested: true,
            createdAt: new Date().toISOString(),
          }, ...(prev || [])]);
        }
        setTimeout(load, 3000);
      })
      .catch(err => setError(`could not start a session: ${err}`))
      .finally(() => setBusy(''));
  };

  const ask = () => {
    if (!topic.trim() || busy) return;
    start({ kind: 'topic', topic: topic.trim(), title: title.trim() }, true);
    setTopic('');
    setTitle('');
  };

  if (open) {
    return (
      <div className="work-card" style={{ padding: '0 14px 14px', textAlign: 'left', fontSize: 'small' }}>
        <ResearchConversation
          sessionId={open.sessionId}
          pending={open.pending}
          title={open.title}
          onBack={() => { setOpen(null); load(); }}
          onDeleted={() => { setOpen(null); load(); }}
          onRenamed={load}
        />
      </div>
    );
  }

  const all = sessions || [];
  const mine = repo ? all.filter(s => s.repo === repo) : all;
  const others = repo ? all.filter(s => s.repo !== repo) : [];

  const stateOf = (s) => {
    if (s.requested) {
      return <Pill text="requested" color="#b08800" bg="rgba(176,136,0,0.12)"
        title="Asked for — the controller has not built the sandbox yet. A few minutes." />;
    }
    if (s.paused) {
      return <Pill text="paused" color="var(--text-secondary)" bg="var(--bg-secondary)"
        title="Scaled to zero — the transcript survives, the engine does not" />;
    }
    if (s.openingError) {
      return <Pill text="opening failed" color="var(--text-danger)" bg="var(--bg-danger-light)"
        title={s.openingError} />;
    }
    // Before opening…: the sandbox object exists for minutes before its
    // pod does, and "up" on a session nothing can reach yet is a lie
    // that costs a click.
    if (s.starting) {
      return <Pill text="starting…" color="#b08800" bg="rgba(176,136,0,0.12)"
        title="The sandbox exists; its pod is still coming up — image, disk, clone" />;
    }
    if (s.opening) {
      return <Pill text="opening…" color="#b08800" bg="rgba(176,136,0,0.12)"
        title="The first question has not reached the agent yet" />;
    }
    return <Pill text="up" color="var(--status-green)" bg="rgba(40,167,69,0.12)" />;
  };

  const row = (s) => (
    <tr key={s.sessionId} style={{ borderBottom: '1px solid var(--border-color)' }}>
      <td style={{ padding: '6px 8px' }}>
        {/* The title carries the row: it is what someone scanning for
            "the one about the retry loop" is actually reading. */}
        <div>{s.title || <span style={{ color: 'var(--text-secondary)', fontStyle: 'italic' }}>untitled</span>}</div>
        <div style={{ fontFamily: 'monospace', fontSize: 'x-small', color: 'var(--text-secondary)' }}>
          {shortSession(s.sessionId)}
        </div>
      </td>
      <td style={{ padding: '6px 8px' }}>
        {s.htmlUrl
          ? <a href={s.htmlUrl} target="_blank" rel="noopener noreferrer">{s.repo}</a>
          : s.repo}
      </td>
      <td style={{ padding: '6px 8px', color: 'var(--text-secondary)' }} title={s.createdAt}>{ageOf(s.createdAt)}</td>
      <td style={{ padding: '6px 8px' }}>{stateOf(s)}</td>
      <td style={{ padding: '6px 8px', fontFamily: 'monospace', fontSize: 'x-small', color: 'var(--text-secondary)' }}>
        {s.sandbox}
      </td>
      <td style={{ padding: '6px 8px', textAlign: 'right' }}>
        <button className="btn btn-sm"
          onClick={() => setOpen({ sessionId: s.sessionId, pending: !!s.requested, title: s.title || '' })}>
          Open
        </button>
      </td>
    </tr>
  );

  return (
    <div className="work-card" style={{ padding: '14px', textAlign: 'left', fontSize: 'small' }}>
      {/* The canned reads, then the ask box: the same shape the Explore
          tab had, now producing sessions rather than a separate thing. */}
      <div style={{ display: 'flex', alignItems: 'center', gap: '8px', flexWrap: 'wrap', marginBottom: '10px' }}>
        <button className="btn" disabled={!!busy}
          title="Starts a conversation that reads the repo end to end: what it is, how it is put together, where the code lives"
          onClick={() => start({ kind: 'onboard' }, false)}>
          {busy === 'onboard' ? 'Requesting…' : 'Generate Overview'}
        </button>
        <span style={{ position: 'relative' }}>
          <button className="btn" disabled={!!busy}
            title="Starts a conversation that digests a recent window: themes, churn, notable merges, and maintainer asks"
            onClick={() => setSinceOpen(o => !o)}>What happened ▾</button>
          {sinceOpen && (
            <div style={{
              position: 'absolute', top: '100%', left: 0, marginTop: '4px', zIndex: 20,
              background: 'var(--bg-card)', border: '1px solid var(--border-color)',
              borderRadius: '6px', boxShadow: '0 4px 12px rgba(0,0,0,0.15)', minWidth: '140px',
            }}>
              {['2 weeks', '1 month', '3 months'].map(win => (
                <div key={win}
                  onClick={() => { setSinceOpen(false); start({ kind: 'activity', since: win }, false); }}
                  style={{ padding: '6px 12px', cursor: 'pointer', whiteSpace: 'nowrap' }}
                  onMouseEnter={e => { e.currentTarget.style.background = 'var(--bg-hover)'; }}
                  onMouseLeave={e => { e.currentTarget.style.background = 'transparent'; }}>
                  last {win}
                </div>
              ))}
            </div>
          )}
        </span>
        <span style={{ flex: 1 }} />
        <button className="btn btn-sm" disabled={!!busy}
          title="Start an empty conversation and type the first message yourself"
          onClick={() => start({}, true)}>
          {busy === 'new' ? 'Requesting…' : 'Empty conversation'}
        </button>
      </div>

      <div style={{
        border: '1px solid var(--border-color)', borderRadius: '10px',
        background: 'var(--bg-secondary)', padding: '10px 12px',
      }}>
        <textarea rows={3} value={topic} onChange={e => setTopic(e.target.value)}
          placeholder="Ask anything about this repo — a question, a subsystem, or 'compare with …'"
          onKeyDown={e => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); ask(); } }}
          style={{
            width: '100%', border: 'none', outline: 'none', resize: 'none',
            background: 'transparent', color: 'var(--text-primary)',
            font: 'inherit', boxSizing: 'border-box',
          }} />
        <div style={{ display: 'flex', alignItems: 'center', gap: '8px', marginTop: '6px' }}>
          {/* Optional: the question makes a perfectly good name, and
              nobody should have to invent one to ask something. */}
          <input value={title} onChange={e => setTitle(e.target.value)}
            placeholder="name (optional)" aria-label="Session name"
            onKeyDown={e => { if (e.key === 'Enter') { e.preventDefault(); ask(); } }}
            style={{
              flex: '0 1 220px', font: 'inherit', padding: '2px 8px',
              border: '1px solid var(--border-color)', borderRadius: '6px',
              background: 'var(--bg-card)', color: 'var(--text-primary)',
            }} />
          <span style={{ flex: 1 }} />
          <button className="btn btn-sm" disabled={!topic.trim() || !!busy} onClick={ask}>
            {busy === 'topic' ? 'Requesting…' : 'Research'}
          </button>
        </div>
      </div>

      <div style={{ color: 'var(--text-secondary)', margin: '8px 0 10px' }}>
        Each one is a sandbox with this repo checked out. It takes a few minutes to appear.
      </div>

      {error && (
        <div className="warning-banner" style={{ cursor: 'pointer' }} onClick={() => setError('')} title="Dismiss">
          {error}
        </div>
      )}

      {sessions === null ? (
        <div style={{ color: 'var(--text-secondary)', fontStyle: 'italic' }}>loading…</div>
      ) : !mine.length ? (
        <div style={{ color: 'var(--text-secondary)', fontStyle: 'italic' }}>
          No research conversations for {repo || 'this board'} yet.
        </div>
      ) : (
        <table style={{ width: '100%', borderCollapse: 'collapse' }}>
          <thead>
            <tr style={{ textAlign: 'left', borderBottom: '2px solid var(--border-color)', color: 'var(--text-secondary)' }}>
              <th style={{ padding: '6px 8px' }}>Conversation</th>
              <th style={{ padding: '6px 8px' }}>Repo</th>
              <th style={{ padding: '6px 8px' }}>Age</th>
              <th style={{ padding: '6px 8px' }}>State</th>
              <th style={{ padding: '6px 8px' }}>Sandbox</th>
              <th style={{ padding: '6px 8px' }}></th>
            </tr>
          </thead>
          <tbody>{mine.map(row)}</tbody>
        </table>
      )}

      {others.length > 0 && (
        <div style={{ marginTop: '12px', paddingTop: '8px', borderTop: '1px solid var(--border-color)' }}>
          <div onClick={() => setShowOthers(o => !o)}
            style={{ cursor: 'pointer', color: 'var(--text-secondary)' }}
            title="Sessions you own for other repositories — listed here so one whose board is gone is still reachable">
            {showOthers ? '▾' : '▸'} {others.length} conversation{others.length === 1 ? '' : 's'} for other repositories
          </div>
          {showOthers && (
            <table style={{ width: '100%', borderCollapse: 'collapse', marginTop: '6px' }}>
              <tbody>{others.map(row)}</tbody>
            </table>
          )}
        </div>
      )}

      {/* The notes branch on the member's fork. Research does not write
          there yet — runs still do, and the old explore notes are still
          on it — so this is a plain link out rather than anything the
          page reads back. */}
      {forkOwner && repo && (
        <div style={{ marginTop: '14px', paddingTop: '8px', borderTop: '1px solid var(--border-color)', color: 'var(--text-secondary)', fontSize: 'x-small' }}>
          Earlier notes and run artifacts live on{' '}
          <a href={`https://github.com/${forkOwner}/${repo}/tree/exploration/notes`}
            target="_blank" rel="noopener noreferrer">
            {forkOwner}/{repo} @ exploration/notes ↗
          </a>
        </div>
      )}
    </div>
  );
}

export default ResearchPanel;
