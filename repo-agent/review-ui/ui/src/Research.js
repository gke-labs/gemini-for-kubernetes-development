import React, { useState, useEffect, useLayoutEffect, useCallback, useMemo, useRef } from 'react';
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
//
// auto is the other half of "will it ask me": null means the transcript
// has not said either way, which is not the same as no — see the
// mode_changed fold.
export const emptyTranscript = { items: [], plan: null, busy: false, stopReason: '', mode: '', auto: null };

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
      // autoApprove rides acpd's own entry only: current_mode_update is
      // the engine's, and the engine knows nothing about acpd answering
      // underneath it. Absent therefore means "this event says nothing
      // about that", not "no", so the last known answer stands.
      const auto = data.autoApprove === undefined ? state.auto : !!data.autoApprove;
      return { ...push({ role: 'mode', mode: next, auto }), mode: next, auto };
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
          {/* A reason is set only when acpd answered for us: the
              ten-minute timeout ran out, or the session auto-approves.
              Saying so is the difference between "you did this" and
              "nobody did". */}
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

// modeSuffix says what a mode entry means for the reader.
//
// The name of the mode does not answer the question anyone is asking of
// it. "yolo" tells you nothing about whether a prompt is still coming,
// because two layers decide that — the engine's mode and acpd answering
// underneath — and only the pair of them is an answer. null is an entry
// written before acpd recorded the second layer; saying nothing there
// beats guessing, since the guess would be the reassuring one.
export function modeSuffix(auto) {
  if (auto === null || auto === undefined) return '';
  return auto ? ' — prompts answered for you' : ' — you will be asked';
}

// MermaidBlock draws a ```mermaid fence as the diagram it describes.
// The research prompt asks the agent for Mermaid when structure is the
// answer, and a flowchart read as source is a puzzle, not a picture.
//
// mermaid is large, so it loads on the first diagram and not before —
// a conversation without one never pays for it.
//
// The agent's text streams, so the fence arrives a chunk at a time and
// is invalid for most of its life. Until it draws it stays what it
// would have been without this: its source, in a code block. A fence
// that never draws — bad syntax, or a layout mermaid gives up on —
// stays its source for good; mermaid's own error graphic is never
// shown. Re-renders wait for the chunks to pause, so a diagram is not
// redrawn once per token.
let mermaidSeq = 0;
const MERMAID_SETTLE_MS = 250;

export function MermaidBlock({ code }) {
  const [svg, setSvg] = useState({ code: '', svg: '' });
  const first = useRef(true);
  const dark = document.body.className.indexOf('dark-mode') !== -1;

  useEffect(() => {
    let alive = true;
    const draw = async () => {
      try {
        const mermaid = (await import('mermaid')).default;
        // Every draw, not once: it is only a config write, and the
        // theme is whatever the page is at the moment of drawing.
        //
        // suppressErrorRendering: a diagram that parses and then fails
        // to lay out otherwise gets mermaid's "Syntax error" bomb drawn
        // into a scratch element on document.body, left there, before
        // it throws. With it set, mermaid cleans up and just throws,
        // and the catch below leaves the source showing.
        mermaid.initialize({
          startOnLoad: false, securityLevel: 'strict', suppressErrorRendering: true,
          theme: dark ? 'dark' : 'neutral',
        });
        if (!(await mermaid.parse(code, { suppressErrors: true }))) return;
        const res = await mermaid.render(`research-mmd-${++mermaidSeq}`, code);
        if (alive) setSvg({ code, svg: res.svg });
      } catch (e) {
        // Parsed and still would not draw: the source below is the answer.
      }
    };
    // A finished message, or one reopened from history, draws at once.
    if (first.current) {
      first.current = false;
      draw();
      return () => { alive = false; };
    }
    const timer = setTimeout(draw, MERMAID_SETTLE_MS);
    return () => { alive = false; clearTimeout(timer); };
  }, [code, dark]);

  // A diagram drawn from an earlier state of the fence is stale the
  // moment the text moves on; the source is never wrong.
  if (svg.code !== code || !svg.svg) {
    return <pre><code className="language-mermaid">{code}</code></pre>;
  }
  return <div className="term-diagram" dangerouslySetInnerHTML={{ __html: svg.svg }} />;
}

// markdownComponents hands a mermaid fence to MermaidBlock and leaves
// every other block to react-markdown. It hooks <pre> rather than
// <code> so the diagram is not drawn inside a monospace code box.
export const markdownComponents = {
  pre({ node, children, ...props }) {
    const code = node && node.children && node.children[0];
    const classes = (code && code.tagName === 'code' && code.properties && code.properties.className) || [];
    if (classes.includes('language-mermaid')) {
      const text = code.children.map(c => c.value || '').join('').replace(/\n$/, '');
      return <MermaidBlock code={text} />;
    }
    return <pre {...props}>{children}</pre>;
  },
};

// TerminalItem is one line of the transcript: a flat fixed-width log
// with a sigil in front of each entry, rather than a page of bubbles.
// It is the only way the transcript is drawn — see the `view` state for
// why the page-typeset alternative is gone.
//
// Permission prompts are the exception, and keep their page styling.
// They are the one part of a transcript that is a control and not a
// record, and a misread permission is a worse outcome than a seam.
//
// `rendered` is the one thing the two surviving views differ over: the
// agent's prose goes through markdown, so a table gets real borders
// instead of pipes that happen to line up. Everything else — the
// sigils, the collapsed tool lines, the fixed-width face — is identical
// either way.
//
// Only the agent's prose is affected. Your own prompt keeps its `❯ `
// and stays verbatim — it is a line you typed, not a document — and
// tool output and expanded thinking stay verbatim too: they are program
// output, not markdown, and a JSON blob with an asterisk in it should
// not come back italic.
function TerminalItem({ item, onResolve, resolving, rendered }) {
  const [open, setOpen] = useState(false);

  switch (item.role) {
    case 'user':
      return (
        <div className="term-line term-user"><span className="term-sigil">❯ </span>{item.text}</div>
      );
    case 'agent':
      return rendered
        ? (
          <div className="term-agent term-rendered md-body">
            <ReactMarkdown remarkPlugins={[remarkGfm]} components={markdownComponents}>{item.text}</ReactMarkdown>
          </div>
        )
        : <div className="term-line term-agent">{item.text}</div>;
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
      return <div className="term-line term-dim">— approval mode: {item.mode}{modeSuffix(item.auto)} —</div>;
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

// How tall a box holding `scrollHeight` of text should be, and whether
// it has to start scrolling to stay that tall.
//
// Its own function because it is the only arithmetic in GrowingTextarea
// and the only part of it a test can see: jsdom has no layout, so every
// element it renders reports a scrollHeight of zero.
export function grownHeight(scrollHeight, lineHeight, minRows, maxRows) {
  const max = lineHeight * maxRows;
  return {
    height: Math.max(lineHeight * minRows, Math.min(scrollHeight, max)),
    scroll: scrollHeight > max,
  };
}

// GrowingTextarea is as tall as what has been typed into it, between one
// line and maxRows, and scrolls past that.
//
// A fixed `rows` is wrong in both directions at once: three empty lines
// under a one-line question, and a scrollbar the instant anybody pastes
// a stack trace. The cap is there because the composer must not be able
// to eat the transcript it is a follow-up to.
//
// Measured rather than counted. A line that wraps is two lines tall and
// counting "\n" cannot see that, which is exactly the case — a pasted
// paragraph — where getting it wrong is most obvious.
// inputRef, when given, is handed the textarea itself. For the one
// caller that has to reach it — filling the ask box with a canned prompt
// and then putting the caret at the end of it — rather than a forwarded
// ref, because this component's own ref is what measures the box.
function GrowingTextarea({ value, minRows = 1, maxRows = 10, style, inputRef, ...rest }) {
  const ref = useRef(null);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    // Collapse before measuring. scrollHeight is the content's height
    // *or* the height already set, whichever is larger, so a box that
    // has once been tall never shrinks again without this.
    el.style.height = 'auto';
    // 'normal' is not a number, and is what jsdom and an unstyled
    // element both report.
    const line = parseFloat(window.getComputedStyle(el).lineHeight) || 16;
    const { height, scroll } = grownHeight(el.scrollHeight, line, minRows, maxRows);
    el.style.height = `${height}px`;
    el.style.overflowY = scroll ? 'auto' : 'hidden';
  }, [value, minRows, maxRows]);
  return <textarea
    ref={el => {
      ref.current = el;
      if (inputRef) inputRef.current = el;
    }}
    rows={minRows} value={value}
    style={{ resize: 'none', ...style }} {...rest} />;
}

function PlanPanel({ entries }) {
  const [open, setOpen] = useState(true);
  if (!entries || !entries.length) return null;
  const mark = { completed: '✓', in_progress: '◐', pending: '○' };
  return (
    // Terminal colours, because the plan only ever sits on the terminal
    // canvas now — the page's greys read as a hole against it.
    <div style={{
      borderTop: '1px solid var(--term-rule)',
      padding: '6px 14px', color: 'var(--term-fg)',
    }}>
      <div onClick={() => setOpen(o => !o)} style={{ cursor: 'pointer', color: 'var(--term-dim)' }}>
        <span className="term-sigil">{open ? '▾ ' : '▸ '}</span>
        plan ({entries.filter(e => e.status === 'completed').length}/{entries.length})
      </div>
      {open && entries.map((e, i) => (
        <div key={i} style={{
          padding: '1px 0 1px 14px',
          color: e.status === 'completed' ? 'var(--term-dim)' : 'var(--term-fg)',
          textDecoration: e.status === 'completed' ? 'line-through' : 'none',
        }}>
          <span className="term-sigil" style={{ marginRight: '6px' }}>{mark[e.status] || '○'}</span>{e.content}
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

// Where the reading choice is remembered.
const RESEARCH_VIEW_KEY = 'repoboard.research.view';

// normaliseView reads a stored choice, including the two this tab used
// to write before the conversation became a terminal outright.
//
// Every migration preserves the one thing the member actually chose —
// whether the agent's prose is parsed — and drops the part that is no
// longer theirs to pick. `rich` and `mono` were both parsed markdown
// differing only in the canvas, and there is one canvas now, so both
// land on `rendered`. `terminal` and `raw` were both unparsed source.
//
// Nobody is moved to a view they have never seen: the two survivors are
// the two halves of a choice that always existed.
export function normaliseView(stored) {
  switch (stored) {
    case 'raw':
    case 'terminal':
      return 'raw';
    default:
      return 'rendered';
  }
}

// taskSessionHash is where a task's session opens in its own tab.
export function taskSessionHash(task) {
  return `#/task-session/${task.sandbox}/${task.task}`;
}

// taskSessionFallback is the terminal for a task whose sandbox keeps no
// session: a plan's resumes in the agent's own CLI, anything else is a
// shell.
function taskSessionFallback(namespace, task) {
  const chat = task.task.startsWith('recipe-plan-') ? '?chat=plan' : '';
  return `#/terminal/${namespace}/${task.sandbox}${chat}`;
}

// ResearchConversation is one conversation: the transcript, the
// composer, and the machinery that keeps a websocket attached to it.
//
// `pending` softens the 404: for a session whose claim was filed seconds
// ago, "not found" means the controller has not made the sandbox yet,
// while for any other session it means the sandbox is gone. The caller
// knows which; this component cannot.
// `fill` stretches the conversation to its container instead of
// capping it at 72vh; `standalone` says this *is* the popped-out
// window, which is the one place with nothing to offer more room —
// it already has the whole window, so neither full screen nor a
// second tab of itself means anything there.
// They were one prop, which stopped being true the moment the panel
// opened the conversation in a slide-over: that wants the full height
// and the pop-out both. The panel passes neither any more — it opens
// full screen, which is its own height — so `fill` is the standalone
// route's, where the conversation is the page.
// `onClose` says there is a list behind this conversation and full
// screen is how it was opened, not somewhere it was expanded to. It
// therefore mounts full screen, and the two ways out of full screen —
// Escape and the header's button — go back to that list instead of
// shrinking into a pane the panel no longer has.
//
// `task` is the session: `{ sandbox, task }`, whichever recipe started
// it — a research conversation, a plan, a triage. Everything this shows
// besides the conversation comes from the session's GET: the recipe's
// revises as buttons, its draft as one panel with that draft's actions.
// Nothing here knows which recipe it is.
// `sessionId` is the research conversation the session is, when it is
// one — the list knows it before the session does — and is what rename
// and delete go to; a task's session is named by the task and is not
// this view's to delete. While the task runs it is the task's, so the
// composer stays shut and the member watches.
// A research row with no task yet (`pending`, or `legacy`, made by the
// old research path) has no session to open, and says so instead.
// REVISE_POLL_EVERY is how often a running revise or a standing write is
// looked at again.
const REVISE_POLL_EVERY = 3000;

// DRAFT_VERBS is how a verb a draft offers looks when the output gives
// it no label, and what to confirm before taking it. Whether the member
// may take it is the API's word, in its enabled and reason.
export const DRAFT_VERBS = {
  edit: { label: 'Edit', title: 'Edit the draft before it goes anywhere' },
  comment: {
    label: 'Post', title: 'Posts the draft as a comment on the issue under your identity',
    confirm: 'Post this draft on the issue as you?',
  },
  run: {
    label: 'Approve & Fix', title: 'Approves the plan and launches the fix — the plan ships in the PR description',
    confirm: 'Approve this plan and launch the fix as you?',
  },
  'push-notes': { label: 'Save to research/notes', title: 'Push this draft to research/notes in your fork' },
  'open-pr': {
    label: 'Open draft PR', title: 'Opens a draft PR with this change under your identity',
    confirm: 'Open a draft PR with this change as you?',
  },
  'post-replies': {
    label: 'Post replies', title: 'Posts the replies to the PR comments under your identity',
    confirm: 'Post these replies on the PR as you?',
  },
  'post-review': {
    label: 'Post review', title: 'Posts this review as a pending review on the PR, for you to submit',
  },
  reject: {
    label: 'Discard', title: 'Drop the draft',
    confirm: 'Discard this draft?',
  },
};

export function ResearchConversation({
  sessionId, pending, legacy, title, onDeleted, onRenamed, onClose, fill, standalone, renameAt, task, onDismiss,
}) {
  const api = task
    ? `/api/task-sessions/${encodeURIComponent(task.sandbox)}/${encodeURIComponent(task.task)}`
    : '';
  const eventsPath = task
    ? `/api/task-session-events/${encodeURIComponent(task.sandbox)}/${encodeURIComponent(task.task)}`
    : '';
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
  // The research conversation this session is, if it is one: from the
  // row that was clicked, else from the session's own answer (a session
  // opened by its link).
  const researchId = sessionId || (info && info.research) || '';
  // The session's name, and the draft while it is being edited (null
  // when it is not). Seeded from the row that was clicked so the header
  // reads right before the probe answers.
  const [name, setName] = useState(title || '');
  const [renaming, setRenaming] = useState(null);
  // Mirrored so the probe's answer can tell "nobody is editing" without
  // making itself depend on the draft.
  const renamingRef = useRef(null);
  const editName = (draftName) => { renamingRef.current = draftName; setRenaming(draftName); };
  // The title box, and whether the next render owes it the caret.
  // autoFocus is not enough: it is a mount-time thing, and an unnamed
  // session already rests in the box, so the render that starts an edit
  // there mounts nothing. One mechanism for both, rather than a prop
  // that works for half the cases.
  const titleBoxRef = useRef(null);
  const focusTitleRef = useRef(false);
  const beginRename = (seed) => { focusTitleRef.current = true; editName(seed); };
  const [transcript, setTranscript] = useState(emptyTranscript);
  const [openBusy, setOpenBusy] = useState(false);
  const [caughtUp, setCaughtUp] = useState(false);
  const [draft, setDraft] = useState('');
  const [sending, setSending] = useState(false);
  const [resolving, setResolving] = useState(false);
  const [error, setError] = useState('');
  const [waking, setWaking] = useState(false);
  // The approval mode the session is in, and the set the engine will
  // accept. Seeded from whichever of the probe and the open frame
  // answers first; the transcript takes over once it is caught up,
  // because a mode switched in another tab — or left by the engine on
  // its own — arrives as an event and never as a reply to us.
  // problem is acpd's account of why current is not the mode the
  // session was created with — the session runs anyway, it just asks
  // before it acts, and this is the only place that says why.
  // auto is whether acpd answers the prompts itself. Carried alongside
  // current rather than derived from it: the rule that ties the two is
  // the server's, and a UI that re-derived it would go on claiming the
  // session asks first the moment the server's rule changed.
  const [modeState, setModeState] = useState({ current: '', available: [], problem: '', auto: false });
  // A task session's two facts, from the session we attached to: held,
  // the task is still running and only it may drive the session; loaded,
  // the agent remembers what the task said (false: it starts fresh, and
  // the transcript above is only for the member to read).
  const [taskState, setTaskState] = useState({ held: false, loaded: true });
  const [switching, setSwitching] = useState(false);
  // The overflow menu behind ⋯. Open state and nothing else: what is in
  // it are the three things you reach for once a session and never
  // while reading one, and they were costing header width all the time.
  const [menuOpen, setMenuOpen] = useState(false);
  // Only so the composer can lift when it has the caret. :focus-within
  // would do it in a stylesheet, but every style in this file is
  // inline and one rule in App.css for one box is worse than a bool.
  const [composerFocused, setComposerFocused] = useState(false);
  // Whether the conversation has taken over the window. Not remembered:
  // full screen is something you do to read one long answer, not a way
  // you like the page to be, and a board that came back full screen
  // after a refresh would be a board you had lost the rest of.
  //
  // Opened from a list, it starts here: the row is the collapsed state,
  // so there is no smaller version of this to expand from.
  const [expanded, setExpanded] = useState(!!onClose);
  // rendered | raw: whether the agent's prose is parsed as markdown.
  // A reading preference, not session state, so it is remembered across
  // conversations and across the pop-out window.
  //
  // There used to be a third, `rich`, which was the same parsed
  // markdown set in the page's own type on a card. It is gone because
  // the conversation is a terminal now and `rich` was the only part of
  // it that was not — keeping it would have meant two entirely
  // different shells for one pane, which is more UI and not less.
  //
  // What is left is the question that was always the real one: the
  // bytes the agent actually sent, or a table with real borders. The
  // first is what you want when you are going to copy it out; the
  // second when you are going to read it.
  const [view, setView] = useState(() => {
    try { return normaliseView(localStorage.getItem(RESEARCH_VIEW_KEY)); }
    catch (e) { return 'rendered'; } // private mode
  });
  const chooseView = (next) => {
    setView(next);
    try { localStorage.setItem(RESEARCH_VIEW_KEY, next); } catch (e) { /* private mode */ }
  };

  // Escape leaves full screen, and while we are in it the page behind
  // does not scroll — an overlay you can scroll the board underneath is
  // an overlay that reads as a bug.
  //
  // defaultPrevented is the whole contract with everything else on the
  // page that answers Escape: the rename box cancels an edit with it and
  // calls preventDefault, so typing a name and hitting Escape puts the
  // name back without also throwing away the screen you were reading it
  // on. Only the outermost unhandled Escape gets here.
  // Where Escape and the header's button both land. Opened from a list
  // there is nothing to shrink to — the row is the small version — so
  // leaving full screen is closing the conversation.
  const leaveFullScreen = useCallback(() => {
    if (onClose) onClose(); else setExpanded(false);
  }, [onClose]);

  useEffect(() => {
    if (!expanded) return undefined;
    const onKey = (e) => {
      if (e.key !== 'Escape' || e.defaultPrevented) return;
      leaveFullScreen();
    };
    window.addEventListener('keydown', onKey);
    const wasOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    return () => {
      window.removeEventListener('keydown', onKey);
      document.body.style.overflow = wasOverflow;
    };
  }, [expanded, leaveFullScreen]);

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
  // "not up yet" — the server answers 409 before the upgrade, which the
  // browser sees only as a socket that would not open.
  useEffect(() => {
    if (!api) {
      // No session to talk to: a claim the controller has not made a
      // sandbox for yet (the list's next poll brings the task), or an
      // old research sandbox nothing can open.
      setPhase(legacy ? 'legacy' : 'starting');
      setDetail(legacy
        ? 'Made by the old research path, which is gone: delete it and start a new one.'
        : 'waiting for the controller to create the sandbox');
      return undefined;
    }
    const state = { closed: false, ws: null, timer: null };

    const later = (fn, ms) => { state.timer = setTimeout(fn, ms); };

    const attach = () => {
      if (state.closed) return;
      const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const url = `${proto}//${window.location.host}${eventsPath}?offset=${offsetRef.current}`;
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
          setTaskState({ held: !!session.held, loaded: !session.task || !!session.loaded });
          setModeState(m => ({
            current: session.mode || m.current,
            available: session.availableModes || m.available,
            // Not defended with `|| m.problem`, unlike the two above: a
            // session that is in the mode it was asked for says nothing
            // here, and that silence is the good news.
            problem: session.modeError || '',
            // Nor here, and for the same reason: the field is omitted
            // when false, so absent is the answer and not a gap.
            auto: !!session.autoApprove,
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
      fetch(api)
        .then(res => res.json().then(body => ({ status: res.status, body })))
        .then(({ status, body }) => {
          if (state.closed) return;
          if (status === 200) {
            setInfo(body);
            setModeState(m => ({
              current: body.mode || m.current,
              available: body.availableModes || m.available,
              problem: body.modeError || '',
              // Only a live probe has been to the session. A probe that
              // could not reach acpd reports nothing about it, and
              // reading that silence as "asks first" would tell the
              // member the calmer of the two stories on no evidence.
              auto: body.live ? !!body.autoApprove : m.auto,
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
          if (status === 409 && body.legacy) {
            // An image whose daemon keeps no sessions for its tasks. Not
            // worth retrying: an image does not change under a pod.
            setInfo(body);
            setPhase('legacy');
            setDetail(body.error || '');
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
    setModeState({ current: '', available: [], problem: '', auto: false });
    setTaskState({ held: false, loaded: true });
    setPhase('probing');
    setDetail('');
    probe();

    return () => {
      state.closed = true;
      if (state.timer) clearTimeout(state.timer);
      if (state.ws) { try { state.ws.close(); } catch (e) { /* already gone */ } }
    };
  }, [pending, legacy, api, eventsPath]);

  // Follow the tail, but only for a reader who is already at it —
  // yanking the view down while someone is reading back is worse than
  // making them scroll.
  useEffect(() => {
    const el = scrollRef.current;
    if (el && stickRef.current) el.scrollTop = el.scrollHeight;
  }, [transcript, phase]);

  // A different session is a different name; the seed from the list is
  // the best one available until the probe answers with the stored one.
  const taskName = task ? task.task : '';
  useEffect(() => { setName(title || ''); editName(null); }, [sessionId, taskName, title]);
  useEffect(() => {
    // Never while the member is typing over it: the probe re-runs on
    // every reconnect, and it must not eat an edit in progress.
    if (info && info.title && renamingRef.current === null) setName(info.title);
  }, [info]);

  // The list asking for a rename. It is a counter and not a boolean
  // because double-clicking the row you are already on has to work too,
  // and that does not remount anything — the only thing that changes is
  // that you asked again.
  //
  // Declared after the seed effect above so that on the mount which
  // brings a new session in, the seed's editName(null) runs first and
  // this has the last word.
  useEffect(() => {
    if (renameAt) beginRename(name);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [renameAt]);

  // Hands the caret to the title box on the render that owes it one,
  // and selects what is there so typing replaces the old name rather
  // than appending to it — a rename is usually a different name.
  useEffect(() => {
    if (!focusTitleRef.current) return;
    // Held, not consumed, until the box is actually there. Starting an
    // edit on a named session sets the flag and the state in the same
    // commit, and the effects of that commit run before the render that
    // swaps the bold text for the input — so the first time through
    // there is nothing to focus yet.
    const el = titleBoxRef.current;
    if (!el) return;
    focusTitleRef.current = false;
    el.focus();
    el.select();
  });

  const send = () => {
    const text = draft.trim();
    if (!text || sending || busy || phase !== 'live') return;
    setSending(true);
    setError('');
    fetch(`${api}/prompt`, {
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

  // applyRename is the PATCH, shared by the member typing a name and by
  // a session naming itself.
  //
  // `optimistic` is whether to show the text before the server answers.
  // A typed name is already a title, so showing it immediately is right.
  // An auto-name is a whole first question, and the point of sending it
  // raw is that the server's Truncate is what a title means — flashing
  // the untruncated paragraph first would be showing a name that was
  // never going to be the name.
  const applyRename = (raw, optimistic) => {
    const next = (raw || '').trim();
    if (!next || next === name) return;
    if (optimistic) setName(next);
    fetch(`/api/research/${encodeURIComponent(researchId)}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ title: next }),
    })
      .then(async res => {
        const body = await res.json().catch(() => ({}));
        if (!res.ok) { setError(body.error || `rename failed: HTTP ${res.status}`); return; }
        if (body.title) setName(body.title);
        if (onRenamed) onRenamed(researchId, body.title || next);
      })
      .catch(err => setError(`rename failed: ${err}`));
  };

  const commitRename = () => {
    const typed = renaming;
    editName(null);
    applyRename(typed, true);
  };

  // An unnamed session names itself from its first question.
  //
  // TitleAnnotation has documented this since it existed — "absent means
  // the list should fall back to the first line of the transcript" — and
  // nothing ever delivered it, because the list is built from sandbox
  // annotations and making it read N transcripts to draw a sidebar is
  // not a trade worth making. This side already has the transcript open.
  // One PATCH the first time an unnamed session is read, and the name is
  // durable: the list sees it, the next tab sees it, and it survives
  // everything except deleting the sandbox it is written on.
  //
  // Guarded on info.title, which is what the server has, and not on
  // `name`, which is seeded from the row that was clicked — a session
  // the list already has a title for must never be renamed by whatever
  // it happens to open with. And skipped outright while the member is
  // typing a name, because they are answering the same question better.
  //
  // Only sessions with no kickoff reach this. A canned or topic session
  // is named at creation by Kickoff.ResolvedTitle, so info.title is set
  // long before the transcript is, which is what keeps the rendered
  // brief in topic.txt from ever becoming somebody's session name.
  const namedRef = useRef(false);
  useEffect(() => {
    // A task's session is named by the task.
    if (namedRef.current || !caughtUp || !researchId) return;
    if (!info || info.title || renamingRef.current !== null) return;
    const first = transcript.items.find(i => i.role === 'user' && (i.text || '').trim());
    if (!first) return;
    namedRef.current = true;
    applyRename(first.text, false);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [info, caughtUp, transcript]);

  const resolve = (resolution) => {
    setResolving(true);
    fetch(`${api}/permission`, {
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
    fetch(`${api}/mode`, {
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
          // A switch that worked settles the complaint the create left
          // behind: the member has just been told what the session is
          // in, by putting it there themselves.
          problem: '',
          // Read back rather than assumed. The whole point of the
          // switch is that acpd stops answering underneath a tightened
          // mode; taking the server's word for it is what makes the
          // control's new label true instead of hopeful.
          auto: !!body.autoApprove,
        }));
      })
      .catch(err => setError(`mode change failed: ${err}`))
      .finally(() => setSwitching(false));
  };

  const cancel = () => {
    fetch(`${api}/cancel`, { method: 'POST' })
      .then(async res => {
        if (res.ok) return;
        const body = await res.json().catch(() => ({}));
        setError(body.error || `cancel failed: HTTP ${res.status}`);
      })
      .catch(err => setError(`cancel failed: ${err}`));
  };

  // The recipe's revises (Update plan, Save notes): each runs a step of
  // the recipe into this session, so it shows here as the next turn and
  // what it writes becomes the draft. The draft is the session's task
  // output with the actions it offers (post it, save it, edit, discard).
  // Both come from the session's GET, which says what the newest click
  // of each says — revising, posting, or why the last one failed — and
  // is read again while one stands. The server files each click where
  // the draft lives; this only names the session.
  const [revising, setRevising] = useState(''); // the revise POST in flight
  const [revised, setRevised] = useState('');
  const [drafting, setDrafting] = useState(''); // the draft action POST in flight
  const [draftEdit, setDraftEdit] = useState(null); // the edit box, when open
  // The draft folds to its one line unless asked for: open, it took half
  // the pane from the conversation, and stayed that way after the save.
  const [draftOpen, setDraftOpen] = useState(false);
  const revises = useMemo(() => (info && info.revises) || [], [info]);
  const sessionDraft = (info && info.draft) || null;
  const standing = revises.some(r => r.reason === 'revising')
    || !!(sessionDraft && sessionDraft.actions.some(a => a.reason === 'posting'));
  const refreshInfo = useCallback(() => {
    if (!api) return;
    fetch(api)
      .then(res => (res.ok ? res.json() : null))
      .then(body => { if (body) setInfo(body); })
      .catch(() => {});
  }, [api]);
  useEffect(() => {
    if (!standing) return undefined;
    const timer = setInterval(refreshInfo, REVISE_POLL_EVERY);
    return () => clearInterval(timer);
  }, [standing, refreshInfo]);
  // Done: it ran, and did not fail.
  const wasRunning = useRef('');
  useEffect(() => {
    const running = revises.find(r => r.reason === 'revising');
    if (running) {
      wasRunning.current = running.revise;
      return;
    }
    if (!wasRunning.current) return;
    const done = revises.find(r => r.revise === wasRunning.current);
    wasRunning.current = '';
    if (done && !done.error) setRevised(done.label || done.revise);
  }, [revises]);

  const revise = (r) => {
    // A revise that asks for inputs (Iterate's instruction) gets them
    // asked for, one at a time; cancelling any files nothing.
    const inputs = {};
    for (const name of r.inputs || []) {
      const value = window.prompt(`${r.label || r.revise}: ${name}`);
      if (!value || !value.trim()) return;
      inputs[name] = value.trim();
    }
    setRevising(r.revise);
    setRevised('');
    setError('');
    fetch(`${api}/revise`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(r.inputs && r.inputs.length ? { revise: r.revise, inputs } : { revise: r.revise }),
    })
      .then(async res => {
        if (res.ok) {
          // The revise is a turn like any other, so it scrolls in below
          // — and the member should be looking at it.
          stickRef.current = true;
          refreshInfo();
          return;
        }
        const body = await res.json().catch(() => ({}));
        setError(body.error || `${r.label || 'revise'} failed: HTTP ${res.status}`);
      })
      .catch(err => setError(`${r.label || 'revise'} failed: ${err}`))
      .finally(() => setRevising(''));
  };

  const takeDraftAction = (a, text) => {
    const label = a.label || (DRAFT_VERBS[a.verb] || {}).label || a.verb;
    setDrafting(a.verb + (a.run || ''));
    setError('');
    fetch(`${api}/draft/${encodeURIComponent(a.verb)}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ run: a.run || '', text: text || '' }),
    })
      .then(async res => {
        if (res.ok) {
          if (a.verb === 'edit') setDraftEdit(null);
          refreshInfo();
          return;
        }
        const body = await res.json().catch(() => ({}));
        setError(body.error || `${label} failed: HTTP ${res.status}`);
      })
      .catch(err => setError(`${label} failed: ${err}`))
      .finally(() => setDrafting(''));
  };

  const destroy = () => {
    if (!window.confirm('Delete this conversation? The sandbox and its transcript go with it.')) return;
    fetch(`/api/research/${encodeURIComponent(researchId)}`, { method: 'DELETE' })
      .then(res => {
        if (res.ok || res.status === 404) { if (onDeleted) onDeleted(researchId); return; }
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
    legacy: { text: 'no session', color: 'var(--text-secondary)', bg: 'var(--bg-secondary)' },
    // A blocked turn is busy, so "agent working" is true and useless: it
    // reads as progress when in fact nothing will happen until someone
    // scrolls down and clicks. The pill is the one part of this panel
    // visible from across a room, so it is where the ask belongs.
    live: waiting
      ? { text: '⚠ needs permission', color: 'var(--status-red, #c62828)', bg: 'rgba(198,40,40,0.10)' }
      : busy
        ? { text: 'agent working', color: '#b08800', bg: 'rgba(176,136,0,0.12)' }
        : { text: 'ready', color: 'var(--status-green)', bg: 'rgba(40,167,69,0.12)' },
  }[phase];

  const repo = (info && info.repo) || '';
  // Waking is the sandbox card's wake: scale the sandbox back up. The
  // probe, still polling the paused session, attaches once acpd answers.
  const wake = () => {
    setWaking(true);
    setError('');
    fetch(`/api/sandbox-card/${encodeURIComponent(task.sandbox)}/lifecycle`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ action: 'wake' }),
    })
      .then(res => {
        if (!res.ok) return res.text().then(t => setError(`wake failed: ${t || `HTTP ${res.status}`}`));
        setPhase('starting');
        setDetail('waking the sandbox');
      })
      .catch(err => setError(`wake failed: ${err}`))
      .finally(() => setWaking(false));
  };

  const composerDisabled = phase !== 'live' || busy || sending || taskState.held;

  // Why the composer will not send, as a line beside the Send button.
  // It used to be the textarea's placeholder, which is the one place it
  // could not stay: a placeholder is gone the instant anybody types,
  // and "the agent is waiting on a permission above" starts mattering
  // precisely when someone is typing into a box that will not send.
  // null is the ordinary case, where the placeholder's invitation is
  // the whole story and a second line would only be noise.
  const composerState = phase !== 'live' ? { text: 'Not connected', urgent: false }
    : taskState.held ? { text: 'The task is still running — watch it here, and continue once it ends', urgent: false }
    : waiting ? { text: 'Waiting for you to answer the permission above', urgent: true }
      : busy ? { text: 'The agent is working — Stop to interrupt', urgent: false }
        : null;

  // Same split as `busy`: the fold is the truthful source only once
  // replay is behind us, and until then the session we attached to is.
  const mode = (caughtUp && transcript.mode) || modeState.current;
  // A mode the engine no longer advertises still gets an entry, so the
  // control shows what the session is in rather than an empty box —
  // the list comes from the engine and can change under a live session.
  const modeOptions = !mode || modeState.available.some(m => m.id === mode)
    ? modeState.available
    : modeState.available.concat([{ id: mode, name: mode }]);
  // Same precedence as mode, for the same reason — a switch made in
  // another tab reaches this one as an entry and never as a reply to
  // us — with the extra step that null means the transcript did not
  // say, which falls back rather than counting as no.
  const autoApproving = caughtUp && transcript.auto !== null && transcript.auto !== undefined
    ? transcript.auto
    : modeState.auto;

  return (
    // Full screen is the same conversation in a fixed pane over the
    // page, not a second copy of it somewhere else: the socket, the
    // transcript and the draft are all still this component's, so
    // expanding mid-answer does not drop the answer.
    <div style={expanded
      ? {
        position: 'fixed', inset: 0, zIndex: 50,
        display: 'flex', flexDirection: 'column', minHeight: 0,
        background: 'var(--bg-color)', padding: '0 24px 16px',
      }
      : fill
        ? { flex: '1 1 auto', display: 'flex', flexDirection: 'column', minHeight: 0, padding: '0 14px 12px' }
        : { display: 'flex', flexDirection: 'column', minHeight: 0, maxHeight: '72vh' }}>
      {/* One frame around the whole conversation, so it reads as a
          terminal window and not as three cards that happen to be
          stacked. Inside it: a title bar the page styles like any other
          chrome, and under that a canvas that is nothing but text.
          Terminal.app is the shape being copied — the title bar is the
          window's, the black is the program's. */}
      <div style={{
        flex: '1 1 auto', display: 'flex', flexDirection: 'column', minHeight: 0,
        marginTop: '8px',
        border: '1px solid var(--border-color)', borderRadius: '10px', overflow: 'hidden',
      }}>
        <div style={{
          display: 'flex', alignItems: 'center', gap: '8px', flexWrap: 'wrap',
          padding: '6px 10px', fontSize: 'small', flex: '0 0 auto',
          background: 'var(--bg-card)', borderBottom: '1px solid var(--border-color)',
        }}>
          {/* The name, editable in place. A session is found again by what
              it was about, so the title is the one thing here worth the
              width — the repo and the id follow it, quietly.

              An unnamed session rests *in* the box rather than beside it.
              It used to fall back to the repo, which read as a name, so
              the one session that needs naming was the one that looked
              like it already had one — and the way to fix that was to
              click a word that gave no sign it was clickable. The repo is
              a link two inches to the right; saying it twice bought
              nothing and cost the empty box that asks for a name. */}
          {!researchId || (renaming === null && name) ? (
            <strong onClick={researchId ? () => beginRename(name) : undefined} style={{ cursor: researchId ? 'text' : 'default' }}
              title={researchId ? 'Click to rename this conversation' : task && task.task}>
              {name}
            </strong>
          ) : (
            <input ref={titleBoxRef} aria-label="Session title"
              value={renaming === null ? '' : renaming}
              placeholder="Name this conversation…"
              // Clicking into the resting box is the edit starting. Until
              // it does, renamingRef is null and the probe is free to put
              // a title it finds straight into the header — which would
              // swap the box out from under a caret already in it.
              onFocus={() => { if (renaming === null) editName(''); }}
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
          {/* The repo, as a way to get to it. It has been a piece of grey
              text here since the header existed, which is the one place
              the name is not also a link — the list links it, and a
              conversation opened from a link never saw the list. */}
          {/* repo is read off info, so info is here whenever repo is. */}
          {name && repo && (info.htmlUrl
            ? <a href={info.htmlUrl} target="_blank" rel="noopener noreferrer"
              title={`Open ${repo} on GitHub`}>{repo}</a>
            : <span style={{ color: 'var(--text-secondary)' }}>{repo}</span>)}
          <span style={{ fontFamily: 'monospace', color: 'var(--text-secondary)' }}>{shortSession(researchId || taskName)}</span>
          <Pill {...statusPill} title={phase === 'live' && waiting
            ? `Waiting for you: ${(waiting.toolCall && waiting.toolCall.title) || 'a tool call'}`
            : detail} />
          {/* A paused session's one way forward, so it is the header's
              call to action rather than a link in the text below. */}
          {phase === 'paused' && task && (
            <button className="btn btn-sm btn-submit" onClick={wake} disabled={waking}
              title={`Scale ${task.sandbox} back up and reconnect to this session`}>
              {waking ? 'Waking…' : '▶ Wake sandbox'}
            </button>
          )}
          <span style={{ flex: 1 }} />
          {/* How much the engine asks before it acts. A research session
              starts auto-approving — nobody is necessarily watching one,
              and a prompt nobody answers stalls the turn until acpd
              cancels it — so this is here to tighten that, not to loosen
              it. Hidden entirely for an engine that offers no modes:
              there is nothing to choose between.

              Labelled by its consequence rather than by the mode's name.
              "approvals: yolo" names a setting; what the member wants to
              know at a glance is whether anything is going to stop and
              ask them, and the mode alone does not say — acpd answering
              underneath is the other half. The amber is the point: the
              state worth noticing is the one where nobody is asked. */}
          {modeOptions.length > 0 && (
            <label title={(modeOptions.find(m => m.id === mode) || {}).description
              || 'How much the engine asks before it acts'}
              style={{
                display: 'inline-flex', alignItems: 'center', gap: '4px', fontSize: 'x-small',
                color: autoApproving ? 'var(--status-amber, #b08800)' : 'var(--text-secondary)',
              }}>
              {autoApproving ? '⚡ auto-approving' : 'asks first'}
              <select value={mode} aria-label="Approval mode"
                disabled={phase !== 'live' || switching}
                onChange={e => chooseMode(e.target.value)}
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
          {/* The session asked for a mode and did not get it. It is in the
              transcript too, where it belongs chronologically, but that
              scrolls away and this does not — and the question it answers
              ("why is it asking me again?") is asked long after the first
              screen. Outside the label rather than in it: clicking a
              warning should not open the picker it is warning about. */}
          {modeState.problem && (
            <span role="status" aria-label="Approval mode warning" title={modeState.problem}
              style={{ fontSize: 'x-small', color: 'var(--status-red, #c62828)', cursor: 'help' }}>
              ⚠ not applied
            </span>
          )}
          {phase === 'live' && busy && !taskState.held && (
            <button className="btn btn-sm" onClick={cancel} title="Interrupt the turn in flight">Stop</button>
          )}
          {/* The recipe's revises: run a step of it again from this
              conversation. Not while a turn is in flight or the session is
              a task's — factory would refuse it — and not twice at once. */}
          {revises.map(r => (
            <button key={r.revise} className="btn btn-sm"
              disabled={!r.enabled || busy || taskState.held || !!revising}
              onClick={() => revise(r)}
              title={r.reason === 'revising' ? `${r.label || r.revise}: running in this conversation`
                : !r.enabled ? `Not now: ${r.reason}`
                  : taskState.held ? 'The session is a task\'s until it ends'
                    : busy ? 'The agent is working — once the turn ends'
                      : r.verb === 'recipe' ? `Runs ${r.label || r.revise} in this conversation, on the PR as it is now, as the row's button does`
                        : 'Runs in this conversation; what it writes becomes the draft below, and nothing is posted'}>
              {revising === r.revise || r.reason === 'revising' ? `${r.label || r.revise}…` : (r.label || r.revise)}
            </button>
          ))}
          {/* Two ways to get more room, and they are different enough to
              both be here rather than one behind the other. Full screen
              keeps the conversation you are in — same socket, same draft,
              Escape puts it back — and is what you want for the answer in
              front of you. The tab is a second place to leave it, which is
              what you want when you are going to keep the board.

              Out of the ⋯ menu, where pop out used to live: these are read
              *while* reading, and a menu is for things you do to a session
              once. An icon each, because they are a pair. */}
          {!standalone && task && (
            <>
              {/* In a slide-over the slide-over is the room: no full screen,
                  and ✕ (below) closes it. */}
              {!onDismiss && (<>
              {/* Opened from a list, this is the way back to it, and it
                  says which key does the same thing. The glyph alone
                  named the gesture and not the shortcut, which left
                  Escape documented in a `title` nobody hovers to read —
                  on a pane that covers the whole window, the way out
                  should be legible without being hunted for. */}
              <button className="btn btn-sm"
                onClick={() => { if (onClose) leaveFullScreen(); else setExpanded(e => !e); }}
                aria-pressed={onClose ? undefined : expanded}
                aria-label={onClose ? 'Close this conversation'
                  : expanded ? 'Exit full screen' : 'Full screen'}
                title={onClose ? 'Back to the conversations (Esc)'
                  : expanded ? 'Exit full screen (Esc)' : 'Fill the window with this conversation'}>
                {onClose ? 'esc ⤢' : expanded ? '⤢' : '⛶'}
              </button>
              </>)}
              <a className="btn btn-sm" href={taskSessionHash(task)}
                target="_blank" rel="noopener noreferrer" aria-label="Open in a new tab"
                onClick={onDismiss}
                title="Open this conversation in its own tab">↗</a>
            </>
          )}
          {/* Everything you do to a session rather than in it, folded
              behind one button. These are once-a-session actions and one
              of them is destructive; they were sitting permanently beside
              the controls used every turn, which both crowded those and
              put Delete a stray click from Stop. */}
          {/* Only when there is something in it. */}
          {((info && info.sandbox && info.namespace) || researchId) && (
          <span style={{ position: 'relative', display: 'inline-flex' }}>
            <button className="btn btn-sm" aria-label="More actions" aria-expanded={menuOpen}
              onClick={() => setMenuOpen(o => !o)} title="More actions">⋯</button>
            {menuOpen && (
              <>
                {/* Catches the click that should dismiss the menu. A
                    backdrop rather than a blur handler, so that the click
                    which closes the menu does not also press whatever it
                    landed on. */}
                <div onClick={() => setMenuOpen(false)}
                  style={{ position: 'fixed', inset: 0, zIndex: 10 }} />
                <div role="menu" style={{
                  position: 'absolute', top: '100%', right: 0, zIndex: 11, marginTop: '4px',
                  display: 'flex', flexDirection: 'column', alignItems: 'stretch', gap: '2px',
                  padding: '6px', minWidth: '160px', textAlign: 'left',
                  border: '1px solid var(--border-color)', borderRadius: '8px',
                  background: 'var(--bg-card)', boxShadow: '0 4px 14px rgba(0,0,0,0.18)',
                }}>
                  {info && info.sandbox && info.namespace && (
                    <a role="menuitem" href={`#/terminal/${info.namespace}/${info.sandbox}`}
                      target="_blank" rel="noopener noreferrer" onClick={() => setMenuOpen(false)}
                      style={{ fontSize: 'x-small', padding: '4px 6px' }}
                      title={`Shell into ${info.sandbox}`}>terminal ↗</a>
                  )}
                  {/* A task's sandbox is the issue's, and not this
                      conversation's to delete. */}
                  {researchId && (
                    <button role="menuitem" className="btn btn-delete btn-sm"
                      onClick={() => { setMenuOpen(false); destroy(); }}
                      title="Delete the sandbox — the transcript lives on its disk and goes with it">
                      Delete
                    </button>
                  )}
                </div>
              </>
            )}
          </span>
          )}
          {onDismiss && (
            <button className="btn btn-sm" onClick={onDismiss} aria-label="Close" title="Close (Esc)">✕</button>
          )}
        </div>

        {error && (
          <div className="warning-banner" style={{ cursor: 'pointer', flex: '0 0 auto' }}
            onClick={() => setError('')} title="Dismiss">{error}</div>
        )}
        {revises.filter(r => r.error).map(r => (
          <div key={`revise-err-${r.revise}`} className="warning-banner" style={{ flex: '0 0 auto' }}>
            {r.label || r.revise} failed: {r.error}
          </div>
        ))}
        {revised && (
          <div role="status" style={{ cursor: 'pointer', flex: '0 0 auto', fontSize: 'small', padding: '4px 10px' }}
            onClick={() => setRevised('')} title="Dismiss">
            {revised}: done — what it wrote is the draft.
          </div>
        )}
        {revises.filter(r => r.reason === 'revising').map(r => (
          <div key={`revising-${r.revise}`} style={{
            flex: '0 0 auto', padding: '4px 10px', textAlign: 'left', fontSize: 'x-small',
            color: 'var(--text-secondary)', borderBottom: '1px solid var(--border-color)',
          }}>
            {r.label || r.revise}: running in this conversation — the draft appears here when the turn finishes.
          </div>
        ))}
        {sessionDraft && sessionDraft.actions.filter(a => a.error).map(a => (
          <div key={`draft-err-${a.verb}${a.run || ''}`} className="warning-banner" style={{ flex: '0 0 auto' }}>
            {a.label || (DRAFT_VERBS[a.verb] || {}).label || a.verb} failed: {a.error}
          </div>
        ))}
        {/* The draft: the session's task output, whatever its kind, with
            the actions it offers — its markdown, or its spec as YAML. One line until Show (or Edit) opens
            it, and then a bounded box, so it never pushes the
            conversation away. */}
        {sessionDraft && (
          <div style={{
            flex: '0 0 auto', maxHeight: draftOpen || draftEdit !== null ? '40%' : undefined,
            overflow: 'auto', padding: '6px 10px', textAlign: 'left',
            borderBottom: '1px solid var(--border-color)', background: 'var(--bg-secondary)',
          }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: '6px', flexWrap: 'wrap', fontSize: 'x-small' }}>
              <strong>{sessionDraft.kind} draft</strong>
              {sessionDraft.note && <code>{sessionDraft.note}.md</code>}
              <span style={{ color: 'var(--text-secondary)' }}>
                {sessionDraft.savedAt ? `saved ${new Date(sessionDraft.savedAt).toLocaleString()}`
                  : sessionDraft.draftedAt ? `drafted ${new Date(sessionDraft.draftedAt).toLocaleString()}` : ''}
              </span>
              <span style={{ flex: 1 }} />
              {draftEdit === null ? (
                <>
                  <button className="btn btn-sm" aria-expanded={draftOpen}
                    onClick={() => setDraftOpen(o => !o)}>{draftOpen ? 'Hide' : 'Show'}</button>
                  {sessionDraft.actions.map(a => {
                    const v = DRAFT_VERBS[a.verb] || {};
                    const label = a.label || v.label || a.verb;
                    const key = a.verb + (a.run || '');
                    return (
                      <button key={key} className="btn btn-sm" disabled={!a.enabled || !!drafting}
                        title={a.enabled ? (v.title || label) : `Not now: ${a.reason}`}
                        onClick={() => {
                          if (a.verb === 'edit') { setDraftEdit(sessionDraft.markdown || sessionDraft.spec || ''); return; }
                          if (v.confirm && !window.confirm(v.confirm)) return;
                          takeDraftAction(a);
                        }}>
                        {drafting === key || a.reason === 'posting' ? `${label}…` : label}
                      </button>
                    );
                  })}
                </>
              ) : (
                <>
                  <button className="btn btn-sm" disabled={!!drafting || !draftEdit.trim()}
                    onClick={() => takeDraftAction(sessionDraft.actions.find(a => a.verb === 'edit'), draftEdit)}>
                    {drafting === 'edit' ? 'Storing…' : 'Store edit'}
                  </button>
                  <button className="btn btn-sm" disabled={!!drafting} onClick={() => setDraftEdit(null)}>Cancel</button>
                </>
              )}
            </div>
            {draftEdit === null ? (draftOpen && (
              <div style={{ fontSize: 'small' }}>
                {sessionDraft.markdown
                  ? <ReactMarkdown remarkPlugins={[remarkGfm]} components={markdownComponents}>{sessionDraft.markdown}</ReactMarkdown>
                  : <pre style={{ whiteSpace: 'pre-wrap', margin: '6px 0' }}>{sessionDraft.spec}</pre>}
              </div>
            )) : (
              <textarea value={draftEdit} onChange={e => setDraftEdit(e.target.value)} aria-label="Edit the draft"
                style={{ width: '100%', minHeight: '160px', marginTop: '6px', fontFamily: 'monospace', fontSize: 'small' }} />
            )}
          </div>
        )}

        {/* The canvas: log, prompt and status line, all one colour and one
            face. The class carries the palette, so nothing in here may set
            a background inline — an inline one would win over it. */}
        <div className="research-terminal" style={{
          flex: '1 1 auto', display: 'flex', flexDirection: 'column', minHeight: 0,
        }}>
          <div ref={scrollRef}
            onScroll={e => {
              const el = e.currentTarget;
              stickRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
            }}
            style={{
              flex: '1 1 auto', minHeight: fill || expanded ? 0 : '240px', overflowY: 'auto', textAlign: 'left',
              padding: '10px 14px',
            }}>
            {/* How this conversation runs, said once at the top of what it
                said. The transcript only records a mode when one changes,
                so a session that started auto-approving and never switched
                — which is every research session — went its whole life
                without the transcript mentioning it anywhere. Someone
                reading back through a command they did not approve should
                find the answer in the thing they are reading, not have to
                infer it from a control in the header. */}
            {mode && (
              <div style={{
                margin: '0 0 10px', paddingBottom: '8px', fontSize: 'x-small',
                borderBottom: '1px solid var(--term-rule)',
                color: autoApproving ? 'var(--term-yellow)' : 'var(--term-dim)',
              }}>
                This conversation runs in <strong>{(modeOptions.find(m => m.id === mode) || {}).name || mode}</strong>
                {autoApproving
                  ? ' — tool calls are approved for you, including ones the engine would otherwise stop and ask about.'
                  : ' — you are asked before tool calls that need approval.'}
                {' '}Change that with the approvals control above.
              </div>
            )}
            {phase === 'gone' && (
              <p style={{ color: 'var(--term-dim)' }}>
                This session no longer exists — the sandbox that held its transcript has been deleted.
              </p>
            )}
            {phase === 'paused' && (
              <p style={{ color: 'var(--term-dim)' }}>
                This session is paused: the sandbox is scaled to zero. Its transcript survives on the
                sandbox's disk, but nothing is running to answer.
                {task && ' Wake the sandbox to continue the conversation.'}
              </p>
            )}
            {(phase === 'starting' || phase === 'probing') && !transcript.items.length && (
              <p style={{ color: 'var(--term-dim)', fontStyle: 'italic' }}>
                {phase === 'starting'
                  ? `Preparing the sandbox — image pull, disk and clone take a few minutes.${detail ? ` (${detail})` : ''}`
                  : 'Connecting…'}
              </p>
            )}
            {phase === 'unreachable' && (
              <p style={{ color: 'var(--term-red)' }}>
                The sandbox is running but its conversation server is not answering: {detail}
              </p>
            )}
            {phase === 'failed' && (
              <p style={{ color: 'var(--term-red)' }}>Cannot reach this session: {detail}</p>
            )}
            {phase === 'legacy' && !task && (
              <p style={{ color: 'var(--term-dim)' }}>{detail}</p>
            )}
            {phase === 'legacy' && task && (
              <p style={{ color: 'var(--term-dim)' }}>
                This sandbox runs an image that keeps no agent session for its tasks.{' '}
                {info && info.namespace && task && (
                  <a href={taskSessionFallback(info.namespace, task)} target="_blank" rel="noopener noreferrer">
                    Continue in the terminal ↗
                  </a>
                )}
              </p>
            )}
            {phase === 'live' && task && !taskState.loaded && (
              <p style={{ color: 'var(--term-yellow)', fontSize: 'x-small' }}>
                The agent could not pick this conversation back up, so it starts fresh: what is above is
                for you to read, and it does not remember it.
              </p>
            )}
            {phase === 'live' && !transcript.items.length && (
              <p style={{ color: 'var(--term-dim)', fontStyle: 'italic' }}>
                Nothing said yet. Ask about {repo ? `${repo}` : 'the repository'} — the agent has the
                checkout in front of it.
              </p>
            )}
            {transcript.items.map(item => (
              <TerminalItem key={item.key} item={item} rendered={view === 'rendered'}
                onResolve={resolve} resolving={resolving} />
            ))}
            {busy && !waiting && (
              <div className="term-line term-dim" style={{ margin: '8px 0' }}>working…</div>
            )}
          </div>

          <PlanPanel entries={transcript.plan} />

          {/* The prompt, as a terminal draws one: the same `❯ ` the
              transcript puts in front of every question you have already
              asked, and the same face, on the same canvas.

              This used to be a rounded card floating clear of the
              transcript on a drop shadow, because flush and unmarked it
              read as the last thing in the scroll rather than the one
              thing you are meant to type into. The sigil is what does that
              job now, and does it better: it is the mark a prompt has, and
              it says the same thing before and after you press Enter.

              The rule above it is the one liberty taken with the idiom. A
              real terminal's prompt is the last line of the scrollback and
              needs no separating from it, but this log scrolls under a
              prompt that does not, and unmarked that reads as a bug. */}
          <div className="term-prompt" style={{
            flex: '0 0 auto', padding: '6px 14px 2px',
            borderTop: `1px solid ${composerFocused ? 'var(--term-accent)' : 'var(--term-rule)'}`,
          }}>
            <span className="term-sigil" aria-hidden="true">❯ </span>
            {/* Not "ask a question about this repository" — that is what
                the landing pane says, and repeating it here made the
                composer read like a second place to start rather than
                the place you carry on. The answer above is the point of
                a research conversation; the follow-up is what it is
                for. */}
            <GrowingTextarea value={draft} onChange={e => setDraft(e.target.value)}
              disabled={phase !== 'live'}
              aria-label="Continue the research"
              placeholder="Continue the research — ask a follow-up…"
              onFocus={() => setComposerFocused(true)}
              onBlur={() => setComposerFocused(false)}
              onKeyDown={e => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send(); } }} />
          </div>

          {/* The status line, which is what a terminal puts under its
              prompt: what the session is doing, how to send, and how you
              are reading it. Dim, because none of it is the conversation —
              it is the line you glance at, not the one you read. */}
          {/* Tight under the prompt rather than floating between it and
              the frame: a status line belongs to the line above it. */}
          <div className="term-statusbar" style={{ flex: '0 0 auto', padding: '0 14px 6px' }}>
            {/* With nothing to report, the key hints have the space. They
                are a separate element from the status below rather than a
                fallback inside it, because that one is a live region: a
                screen reader would read these out afresh every time the
                turn ended, which is the moment it should be saying that
                the turn ended. */}
            {/* `esc close` joins them rather than being a badge of its
                own: it is a key hint, the pane already has a voice for
                those, and a second voice for one more would read as a
                second kind of thing. The header carries the way out as
                well — this group is conditional, and the moment the
                composer has something to say is not the moment to stop
                saying how to leave. */}
            {!composerState && (
              <span className="term-dim">
                ⏎ send · ⇧⏎ newline{onClose ? ' · esc close' : ''}
              </span>
            )}
            {/* Why it will not send, beside the button rather than in the
                placeholder: a placeholder is gone the instant anybody
                types, and "the agent is waiting on a permission above"
                starts mattering precisely then. Mounted even when empty,
                so the region exists before it has anything to announce. */}
            <span role="status" aria-label="Composer state"
              className={composerState && composerState.urgent ? 'term-urgent' : 'term-dim'}>
              {composerState ? composerState.text : ''}
            </span>
            <span style={{ flex: 1 }} />
            <span className="term-views">
              {[
                ['rendered', 'The agent\'s markdown parsed — tables get real borders'],
                ['raw', 'The bytes the agent sent, unparsed, as a terminal would print them'],
              ].map(([v, hint]) => (
                <button key={v} onClick={() => chooseView(v)} title={hint}
                  aria-pressed={view === v}>{v}</button>
              ))}
            </span>
            <button className="term-send" disabled={!draft.trim() || composerDisabled} onClick={send}>
              {sending ? 'sending…' : 'send'}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}

// useResearchSessions is the member's conversations, polled while a
// list of them is on screen.
//
// /api/research is namespace-wide and knows nothing about boards — a
// session carries only the repo it was started on — so every list in
// this file is this one list, narrowed or not. Polled because a newly
// claimed session takes minutes to become a sandbox; until it does it
// is a standing claim, which the list reports as a requested row.
function useResearchSessions() {
  const [sessions, setSessions] = useState(null);
  const [forkOwner, setForkOwner] = useState('');
  // The branch notes are written to, as the server names it. Held in
  // state rather than written here so there is one copy of the string,
  // on the Go side the write path also reads; the initial value is only
  // what to show for the frame before the first response lands.
  const [notesBranch, setNotesBranch] = useState('research/notes');
  const [listError, setListError] = useState('');

  const load = useCallback(() => {
    fetch('/api/research')
      .then(res => (res.ok ? res.json() : Promise.reject(res.statusText)))
      .then(data => {
        setSessions(Array.isArray(data.sessions) ? data.sessions : []);
        setForkOwner(data.forkOwner || '');
        if (data.notesBranch) setNotesBranch(data.notesBranch);
      })
      .catch(err => { setSessions([]); setListError(`Could not list research sessions: ${err}`); });
  }, []);

  useEffect(() => {
    load();
    const t = setInterval(() => { if (!document.hidden) load(); }, 10000);
    return () => clearInterval(t);
  }, [load]);

  return { sessions, forkOwner, notesBranch, listError, setListError, load };
}

// sessionState is the pill at the end of a row: what this conversation
// is doing, in the words of someone deciding whether to open it.
function sessionState(s) {
  if (s.requested) {
    return <Pill text="requested" color="#b08800" bg="rgba(176,136,0,0.12)"
      title="Asked for — the controller has not built the sandbox yet. A few minutes." />;
  }
  if (s.paused) {
    return <Pill text="paused" color="var(--text-secondary)" bg="var(--bg-secondary)"
      title="Scaled to zero — the transcript survives, the engine does not" />;
  }
  // Made by the old research path: nothing can open it any more.
  if (s.legacy) {
    return <Pill text="old — delete" color="var(--text-secondary)" bg="var(--bg-secondary)"
      title="Made by the old research path and can no longer be opened: delete it and start a new one" />;
  }
  // Before working…: the sandbox object exists for minutes before its
  // pod does, and "up" on a session nothing can reach yet is a lie
  // that costs a click.
  if (s.starting) {
    return <Pill text="starting…" color="#b08800" bg="rgba(176,136,0,0.12)"
      title="The sandbox exists; its pod is still coming up — image, disk, clone" />;
  }
  // Above `working…` deliberately: a turn that stopped to ask something
  // is an unanswered question with your name on it, and "working…" reads
  // as "wait" — which is how a session sits there until the permission
  // timeout takes the turn away.
  if (s.waiting) {
    return <Pill text="needs you" color="var(--text-danger)" bg="var(--bg-danger-light)"
      title="Stopped on a permission request. Open it and answer, or the turn is cancelled after ten minutes." />;
  }
  if (s.busy) {
    return <Pill text="working…" color="var(--link-color, #0969da)" bg="rgba(9,105,218,0.12)"
      title="A turn is in flight — the agent is thinking, and nothing is being asked of you" />;
  }
  // Not a failure: the sandbox is fine and the conversation is intact,
  // we just could not ask its engine anything this time round. Said out
  // loud rather than shown as idle, because a wedged pod reported as
  // quiet is the one lie this whole change exists to stop telling.
  if (s.unreachable) {
    return <Pill text="no answer" color="var(--text-secondary)" bg="var(--bg-secondary)"
      title={`The sandbox is up but its conversation server did not answer:\n${s.unreachable}`} />;
  }
  if (s.live) {
    return <Pill text="idle" color="var(--text-secondary)" bg="var(--bg-secondary)"
      title="The agent is up with nothing in flight — waiting on your next question" />;
  }
  // No engine in the pod: the resting state of a session nobody has
  // opened, and of one whose daemon restarted. Green because nothing is
  // wrong — the conversation starts when you open it.
  return <Pill text="up" color="var(--status-green)" bg="rgba(40,167,69,0.12)"
    title="The sandbox is ready. Nothing is running in it until you open the conversation." />;
}

// SessionRow is one conversation, in either list: the title with the
// room to be read, then the repo, the id, its age and what it is
// doing. One line, because everything after the title is the answer to
// "is this one worth opening" and that is small print by definition.
//
// It was a 260px rail beside the conversation, and the rail cost the
// conversation 272px of width for the whole session while truncating
// every title it held. Full width buys back both: the titles fit, and
// what opens fills the window.
//
// The whole row is the control, not just the title: a click that
// lands two pixels off the text should not do nothing.
//
// No repo column, strictly speaking — the repo is on the row, but as
// a word beside the id rather than a column. On a board's tab it is
// the same word all the way down; on the all-boards tab it is the one
// thing that differs, and it reads the same either way.
function SessionRow({ session: s, selected, onOpen, onRename }) {
  return (
    <button type="button"
      aria-current={selected ? 'true' : undefined}
      onClick={() => onOpen(s)}
      // Double-click renames. The box it opens is the one in the
      // header, not a second editor down here: there is one rename
      // and one PATCH, and the list is a list of rows rather than a
      // place things get edited. What the gesture buys is that the
      // row you want to rename is the row you are looking at — the
      // header's title has been clickable all along and nothing about
      // a piece of bold text says so.
      onDoubleClick={() => onRename(s)}
      title={[
        `session ${s.sessionId}`,
        'click to open · double-click to rename',
        s.sandbox ? `sandbox ${s.sandbox}` : '',
      ].filter(Boolean).join('\n')}
      style={{
        display: 'flex', alignItems: 'center', gap: '10px',
        width: '100%', textAlign: 'left', font: 'inherit',
        padding: '9px 12px', marginBottom: '6px', cursor: 'pointer', borderRadius: '8px',
        border: `1px solid ${selected ? 'var(--link-color, #0969da)' : 'var(--border-color)'}`,
        background: 'var(--bg-card)',
        color: 'var(--text-primary)',
      }}>
      {/* The title has the width now, and the width is the whole
          point of the change: it gets as much of the row as the small
          print leaves, and only then does it ellipsise. */}
      <span style={{ flex: '1 1 auto', minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
        {s.title || <span style={{ color: 'var(--text-secondary)', fontStyle: 'italic' }}>untitled</span>}
      </span>
      <span style={{ color: 'var(--text-secondary)', fontSize: 'x-small' }}>{s.repo}</span>
      <span style={{ fontFamily: 'monospace', color: 'var(--text-secondary)', fontSize: 'x-small' }}>
        {shortSession(s.sessionId)}
      </span>
      <span style={{ color: 'var(--text-secondary)', fontSize: 'x-small' }} title={s.createdAt}>
        {ageOf(s.createdAt)}
      </span>
      {sessionState(s)}
    </button>
  );
}

// openedBy turns a clicked row into what the conversation pane needs.
// `pending` is what stops the 404 in the meantime reading as "this
// session does not exist".
const openedBy = (s) => ({ sessionId: s.sessionId, pending: !!s.requested, title: s.title || '' });

// renamedBy is the same, plus the ask to open the header's title box.
// The counter is so that asking twice in the same millisecond — or
// twice for the session already open — is still two asks.
const renamedBy = (open, s) => ({ ...openedBy(s), renameAt: ((open && open.renameAt) || 0) + 1 });

// openRow is the open conversation's row as the list has it now, not as
// it was clicked: a claim gains its sandbox and task minutes after it is
// opened, and that is when the conversation has a session to talk to.
function openRow(open, sessions) {
  const row = (sessions || []).find(s => s.sessionId === open.sessionId) || {};
  return {
    task: row.task && row.sandbox ? { sandbox: row.sandbox, task: row.task } : null,
    legacy: !!row.legacy,
  };
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
// server does it, and matched, and nothing else is listed.
//
// The rail used to end in a disclosure holding every session for every
// other repository, on the argument that one whose board was deleted
// would otherwise be unreachable. That is a rare session paid for by
// every common one: a member with five boards open saw four rails each
// carrying the other four boards' conversations, and the count in the
// summary line — "4 conversations for other repositories" — was
// four-fifths of their work described as somewhere else's. A board's
// research tab is about that repository. An orphaned session is still
// reachable by its own link, #/task-session/<sandbox>/<task>, and still
// listed by the API, which is the thing that would have to change to
// make one truly lost.
export function ResearchPanel({ boardName, repoURL }) {
  const { sessions, forkOwner, notesBranch, listError, setListError, load } = useResearchSessions();
  const [open, setOpen] = useState(null); // { sessionId, pending, title }
  const [busy, setBusy] = useState(false); // a claim is in flight
  const [topic, setTopic] = useState('');
  const [error, setError] = useState('');
  // The canned openings as text, keyed by kind, fetched once. They are
  // what the box gets filled with — see the landing pane — so they are
  // loaded before the click rather than on it: a control that puts text
  // in front of you should do it at once, not after a round trip that
  // can fail while you watch.
  const [prompts, setPrompts] = useState({});
  const askBoxRef = useRef(null);

  // The canned prompts, rendered for this board's repository. Failing is
  // quiet: it costs the two fill controls, and the box they would have
  // filled still works.
  useEffect(() => {
    let live = true;
    fetch(`/api/board/${boardName}/research/prompts`)
      .then(res => (res.ok ? res.json() : Promise.reject(res.statusText)))
      .then(data => { if (live) setPrompts(data.prompts || {}); })
      .catch(() => {});
    return () => { live = false; };
  }, [boardName]);

  const repo = repoFromURL(repoURL);

  // fill puts a canned read in the box instead of running it. The prompt
  // stops being something that happens behind a button and becomes the
  // first thing you can edit: narrow it to one subsystem, strike the
  // Mermaid diagram, add "and compare it with the one in envd". What
  // then gets sent is an ordinary question, named after its own first
  // line — which is why those prompts open with a title.
  const fill = (kind) => {
    const text = prompts[kind];
    if (!text) return;
    setTopic(text);
    // The caret goes to the end of what just appeared, so the box is
    // ready to be typed into rather than merely full.
    const box = askBoxRef.current;
    if (box) {
      box.focus();
      window.requestAnimationFrame(() => {
        box.selectionStart = box.selectionEnd = box.value.length;
        box.scrollTop = box.scrollHeight;
      });
    }
  };

  // ask files a claim for what is in the box, and opens the conversation
  // on the way: it is a question you want the answer to, and the pending
  // view is what fills the pane until the sandbox exists.
  //
  // One shape of request, since the canned reads became text in this
  // same box. There used to be a second — fire a kind and stay on the
  // list, with an optimistic row inserted so nobody clicked twice and
  // paid for a second sandbox — and no kind is sent from here any more.
  //
  // No title travels with it either: the server derives one from the
  // first line, and the conversation's header renames it in place.
  const ask = () => {
    if (!topic.trim() || busy) return;
    setBusy(true);
    setError('');
    fetch(`/api/board/${boardName}/research`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ kind: 'topic', topic: topic.trim() }),
    })
      .then(async res => {
        const body = await res.json().catch(() => ({}));
        if (!res.ok) { setError(body.error || `could not start a session: HTTP ${res.status}`); return; }
        setTopic('');
        // `pending` is what stops the 404 in the meantime reading as
        // "this session does not exist".
        setOpen({ sessionId: body.sessionId, pending: true, title: body.title || '' });
        setTimeout(load, 3000);
      })
      .catch(err => setError(`could not start a session: ${err}`))
      .finally(() => setBusy(false));
  };

  const all = sessions || [];
  const mine = repo ? all.filter(s => s.repo === repo) : all;

  // The rows, shared with the all-boards tab: one conversation each,
  // the title first and the small print after it.
  const sessionRow = (s) => (
    <SessionRow key={s.sessionId} session={s}
      selected={!!open && open.sessionId === s.sessionId}
      onOpen={x => setOpen(openedBy(x))}
      onRename={x => setOpen(o => renamedBy(o, x))} />
  );

  const askBox = (
    <div style={{ marginBottom: '14px' }}>
      {/* No heading over the box. It said "Ask anything about
          substrate" directly above a box whose placeholder said "Ask
          anything about this repo" — the same sentence twice, one of
          them in a voice nothing else on the pane uses. The
          placeholder carries it now, repo name and all. */}
      <div style={{
        border: '1px solid var(--border-color)', borderRadius: '10px',
        background: 'var(--bg-secondary)', padding: '10px 12px',
      }}>
        {/* Starts at two lines rather than one. A single line at the
            top of a list reads as a filter over it, which is not what
            is being asked for. */}
        <GrowingTextarea value={topic} onChange={e => setTopic(e.target.value)} minRows={2}
          inputRef={askBoxRef}
          aria-label="Research question"
          placeholder={`Ask anything about ${repo || 'this repository'} — a question, a subsystem, or 'compare with …'`}
          onKeyDown={e => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); ask(); } }}
          style={{
            width: '100%', border: 'none', outline: 'none', resize: 'none',
            background: 'transparent', color: 'var(--text-primary)',
            font: 'inherit', boxSizing: 'border-box',
          }} />
        {/* No name box. It was optional and everybody left it empty,
            which is the right answer: the server names the session
            after what was asked, and the header renames it in place
            the moment that turns out to be wrong. What it cost was a
            second box between a typed question and sending it, on the
            one screen whose whole job is to get out of the way. */}
        {/* The canned reads, in the box rather than beside it: they
            fill this box with a prompt instead of starting a session
            behind one. Small, and left of the button that sends,
            because that is the order it happens in — and small
            because they are ways to start typing, not second ways to
            ask.
            Neither carries an option any more. The overview never had
            one, and the activity window was a dropdown of three fixed
            spans in front of the one people wanted; it is a word in
            the first line of the prompt now, and the prompt is in
            front of you. Change "last month" to "last week", or to
            "since the 1.4 release", which the dropdown could not
            offer at all. */}
        <div style={{ display: 'flex', alignItems: 'center', gap: '10px', marginTop: '6px' }}>
          {[
            ['onboard', 'overview', 'Puts the overview prompt in the box — read it, edit it, send it'],
            ['activity', 'recent changes', 'Puts the recent-changes prompt in the box. The window is the first line: edit it'],
          ].map(([kind, label, hint]) => (
            <button key={kind} type="button" disabled={!prompts[kind] || !!busy}
              title={prompts[kind] ? hint : 'Loading the canned prompts…'}
              onClick={() => fill(kind)}
              style={{
                border: 'none', background: 'none', padding: 0, font: 'inherit',
                fontSize: 'x-small', color: 'var(--link-color, #0969da)',
                cursor: prompts[kind] ? 'pointer' : 'default',
                opacity: prompts[kind] ? 1 : 0.5,
              }}>
              {label}
            </button>
          ))}
          <span style={{ flex: 1 }} />
          {/* Enter in this box spends a sandbox, which is a surprising
              thing for a two-line textarea to do unannounced — the
              conversation's own composer has said `⏎ send · ⇧⏎ newline`
              on its status line all along, and this is the box where
              getting it wrong costs minutes. Same words, same order,
              next to the button it is an alternative to. */}
          <span style={{ color: 'var(--text-secondary)', fontSize: 'x-small' }}>
            ⏎ research · ⇧⏎ newline
          </span>
          <button className="btn btn-sm" disabled={!topic.trim() || !!busy} onClick={ask}>
            {busy ? 'Requesting…' : 'Research'}
          </button>
        </div>
      </div>

      <div style={{ color: 'var(--text-secondary)', marginTop: '6px', fontSize: 'x-small' }}>
        Session creation could take a few minutes.
      </div>
    </div>
  );

  return (
    <div className="work-card" style={{ padding: '14px', textAlign: 'left', fontSize: 'small' }}>
      {(error || listError) && (
        <div className="warning-banner" style={{ cursor: 'pointer', marginBottom: '10px' }}
          onClick={() => { setError(''); setListError(''); }} title="Dismiss">
          {error || listError}
        </div>
      )}

      {/* The box, then the conversations, all the way across. It was a
          260px rail beside a pane, which was itself an answer to the
          conversation replacing the whole tab — the rail kept the list
          from going away while you read. A conversation opens over the
          window now, so the list does not have to hold a place beside
          it, and stops paying 272px for the privilege.

          No columns, on purpose. A table of three columns in which one
          is the same word on every row is a table pretending to be
          sortable; what is actually scanned is the title, and the rest
          is caption. */}
      {askBox}

      <div>
        {sessions === null ? (
          <div style={{ color: 'var(--text-secondary)', fontStyle: 'italic', padding: '6px 2px' }}>
            loading…
          </div>
        ) : !mine.length ? (
          <div style={{ color: 'var(--text-secondary)', fontStyle: 'italic', padding: '6px 2px' }}>
            No conversations for {repo || 'this board'} yet.
          </div>
        ) : mine.map(s => sessionRow(s))}
      </div>

      {/* The notes branch on the member's fork. Research does not
          write there yet — runs still do, and the old explore notes
          are still on it — so this is a plain link out rather than
          anything the page reads back. */}
      {forkOwner && repo && (
        <div style={{
          marginTop: '12px', paddingTop: '8px', borderTop: '1px solid var(--border-color)',
          color: 'var(--text-secondary)', fontSize: 'x-small',
        }}>
          Earlier notes and run artifacts live on{' '}
          <a href={`https://github.com/${forkOwner}/${repo}/tree/${notesBranch}`}
            target="_blank" rel="noopener noreferrer">
            {forkOwner}/{repo} @ {notesBranch} ↗
          </a>
        </div>
      )}

      {/* Opened, a conversation is the window: a fixed pane over the
          board, Escape back to the row it came from. The list stays
          mounted underneath — switching is Escape and a click, one
          more than the rail cost, which is what buys every conversation
          the full width to be read in.

          Keyed on the session so switching resets what is per-session
          (transcript, cursor, mode) and does not carry a half-typed
          question into someone else's conversation.

          onClose replaced onBack, which had been dead since the rail:
          a `← Sessions` button for a list that was never left. */}
      {open && (
        <ResearchConversation
          key={open.sessionId}
          sessionId={open.sessionId}
          {...openRow(open, sessions)}
          pending={open.pending}
          title={open.title}
          renameAt={open.renameAt}
          onClose={() => setOpen(null)}
          onDeleted={() => { setOpen(null); load(); }}
          onRenamed={load}
        />
      )}
    </div>
  );
}

// AllResearchPanel is the Research tab under All: every conversation
// the member has, whichever board it was started from.
//
// A board's own tab is deliberately about that repository — it shows
// nothing else, because a member with five boards open does not want
// four rails each describing four-fifths of their work as somewhere
// else's. That leaves the question this tab answers: where is the
// conversation I had on Tuesday? It was reachable only by its own link,
// or by remembering which repo it was about and going there.
//
// No ask box. Starting a conversation means choosing the repository it
// is checked out from — that is a board, and the board's tab is where
// the canned openings and the composer already live. This one is for
// finding a conversation, and finding one means opening it, which it
// does over the whole window like everywhere else.
//
// It lists sessions for repositories with no board too. The API is
// namespace-wide and the session carries its repo, so a conversation
// whose board was deleted turns up here rather than being lost — which
// is the case the per-board tab gave up on by design.
export function AllResearchPanel() {
  const { sessions, listError, setListError, load } = useResearchSessions();
  const [open, setOpen] = useState(null); // { sessionId, pending, title }

  // Server order is newest first, which is the right default for a list
  // you are scanning for something you remember. The one thing that
  // beats recency is a conversation stopped on a permission request:
  // it is a question with the member's name on it, and the turn is
  // cancelled if it goes unanswered for ten minutes. Everything else
  // keeps the order it arrived in.
  const all = [...(sessions || [])].sort((a, b) => (b.waiting ? 1 : 0) - (a.waiting ? 1 : 0));

  return (
    <div className="work-card" style={{ padding: '14px', textAlign: 'left', fontSize: 'small' }}>
      {listError && (
        <div className="warning-banner" style={{ cursor: 'pointer', marginBottom: '10px' }}
          onClick={() => setListError('')} title="Dismiss">
          {listError}
        </div>
      )}

      {sessions === null ? (
        <div style={{ color: 'var(--text-secondary)', fontStyle: 'italic', padding: '6px 2px' }}>
          loading…
        </div>
      ) : !all.length ? (
        <div style={{ color: 'var(--text-secondary)', fontStyle: 'italic', padding: '6px 2px' }}>
          No conversations anywhere yet — ask something on a board's Research tab.
        </div>
      ) : all.map(s => (
        <SessionRow key={s.sessionId} session={s}
          selected={!!open && open.sessionId === s.sessionId}
          onOpen={x => setOpen(openedBy(x))}
          onRename={x => setOpen(o => renamedBy(o, x))} />
      ))}

      {open && (
        <ResearchConversation
          key={open.sessionId}
          sessionId={open.sessionId}
          {...openRow(open, sessions)}
          pending={open.pending}
          title={open.title}
          renameAt={open.renameAt}
          onClose={() => setOpen(null)}
          onDeleted={() => { setOpen(null); load(); }}
          onRenamed={load}
        />
      )}
    </div>
  );
}

export default ResearchPanel;
