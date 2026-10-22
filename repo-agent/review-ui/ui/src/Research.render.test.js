import React from 'react';
import { createRoot } from 'react-dom/client';
import { act as domAct } from 'react-dom/test-utils';

import { ResearchConversation, markdownComponents, FORCE_STOP_AFTER_MS } from './Research';

// React.act landed in 18.3 and deprecated the react-dom copy; the
// package range still admits 18.2, so take whichever this install has.
const act = React.act || domAct;

// react-markdown is ESM and jest does not transform node_modules. The
// stub renders its children as text, which is all these assertions need.
// The stub renders its children as text and records the props it was
// given, so a test can assert the GFM plugin is still wired in.
const mockMarkdownCalls = [];
jest.mock('react-markdown', () => ({ children, remarkPlugins }) => {
    mockMarkdownCalls.push({ remarkPlugins });
    return children;
});
// remark-gfm is ESM too; the identity of the stub is all we assert on.
jest.mock('remark-gfm', () => 'gfm-plugin-stub');
// mermaid is ESM and far too big to lay out in jsdom anyway. The stub
// accepts anything that starts like a flowchart and draws it as an svg.
// Its behaviour is installed in the suite's beforeEach, not here:
// react-scripts runs jest with resetMocks, which strips a jest.fn's
// implementation before every test, and a parse() stripped to
// `undefined` rejects every diagram — so the one test expecting
// nothing drawn passes for the wrong reason and the rest fail.
const mockMermaid = { initialize: jest.fn(), parse: jest.fn(), render: jest.fn() };
jest.mock('mermaid', () => ({ __esModule: true, default: mockMermaid }));

// Rendering tests for the parts the pure reducer tests cannot reach: the
// probe-then-attach state machine, and the board filter on the session
// list. No @testing-library — react-dom is already a dependency and
// these assertions are plain DOM reads.

global.IS_REACT_ACT_ENVIRONMENT = true;

// A stand-in for the browser websocket that lets a test push frames.
class FakeSocket {
    constructor(url) {
        this.url = url;
        this.onmessage = null;
        this.onclose = null;
        this.closed = false;
        FakeSocket.instances.push(this);
    }

    close() { this.closed = true; }

    // deliver pushes one server frame, as the API's researchFrame shape.
    deliver(frame) {
        if (this.onmessage) this.onmessage({ data: JSON.stringify(frame) });
    }
}
FakeSocket.instances = [];

const reply = (status, body) => Promise.resolve({
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
});

// Typing into a controlled input: React installs its own value setter
// on the element, so assigning .value directly is invisible to it. The
// prototype's setter is what an actual keystroke goes through.
const nativeSet = (el, value) => {
    const proto = el.tagName === 'TEXTAREA' ? window.HTMLTextAreaElement
        : el.tagName === 'SELECT' ? window.HTMLSelectElement
            : window.HTMLInputElement;
    Object.getOwnPropertyDescriptor(proto.prototype, 'value').set.call(el, value);
};

// flush lets the probe's promise chain settle inside act, so the render
// that follows sees the state the fetch produced.
const flush = () => act(async () => { await Promise.resolve(); await Promise.resolve(); });

// A research conversation is a task session: taskOf is the one the
// list's row for research session id names, and sessionAt its API.
const taskOf = id => ({ sandbox: `rsch-${id}`, task: 'research-1' });
const sessionAt = id => `/api/task-sessions/rsch-${id}/research-1`;

let container;
let root;

beforeEach(() => {
    FakeSocket.instances = [];
    mockMarkdownCalls.length = 0;
    // The rich/terminal choice is sticky across sessions, so a test that
    // flips it would otherwise flip it for the tests after it.
    localStorage.clear();
    global.WebSocket = FakeSocket;
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
});

afterEach(() => {
    act(() => root.unmount());
    container.remove();
    delete global.fetch;
});

describe('ResearchConversation', () => {
    test('probes, attaches, and renders the replayed transcript', async () => {
        global.fetch = jest.fn(() => reply(200, {
            sessionId: 's1', sandbox: 'rsch-repo-abcd1234', namespace: 'ns', repo: 'repo-agent', live: false,
        }));

        await act(async () => { root.render(<ResearchConversation sessionId="s1" task={taskOf('s1')} />); });
        await flush();

        expect(global.fetch).toHaveBeenCalledWith(sessionAt('s1'));
        expect(FakeSocket.instances).toHaveLength(1);
        // Resuming from the start: a fresh view replays the whole
        // transcript off the sandbox's disk.
        expect(FakeSocket.instances[0].url).toContain('/api/task-session-events/rsch-s1/research-1?offset=0');

        const ws = FakeSocket.instances[0];
        await act(async () => {
            ws.deliver({ type: 'open', session: { id: 's1', busy: false, offset: 120 } });
            ws.deliver({ type: 'event', offset: 40, event: { seq: 1, kind: 'user_prompt', data: { text: 'where is the retry loop?' } } });
            ws.deliver({
                type: 'event', offset: 120,
                event: { seq: 2, kind: 'agent_message_chunk', data: { content: { type: 'text', text: 'In watcher.go.' } } },
            });
        });

        expect(container.textContent).toContain('where is the retry loop?');
        expect(container.textContent).toContain('In watcher.go.');
        // The replayed prompt has no turn_end after it, but the open
        // frame said the engine is idle — so the composer stays usable
        // rather than waiting forever on a turn that is not running.
        expect(container.textContent).toContain('ready');
        const send = container.querySelector('.term-send');
        expect(send.disabled).toBe(true); // nothing typed yet
        expect(container.querySelector('textarea').disabled).toBe(false);
    });

    test('agent messages are rendered with the GFM plugin', async () => {
        // Without it react-markdown is CommonMark only, and a table —
        // which is how agents answer comparison questions — degrades
        // into one paragraph of pipes.
        global.fetch = jest.fn(() => reply(200, { sessionId: 's1', repo: 'repo-agent' }));
        await act(async () => { root.render(<ResearchConversation sessionId="s1" task={taskOf('s1')} />); });
        await flush();

        const ws = FakeSocket.instances[0];
        await act(async () => {
            ws.deliver({ type: 'open', session: { busy: false, offset: 0 } });
            ws.deliver({
                type: 'event', offset: 40,
                event: {
                    seq: 1, kind: 'agent_message_chunk',
                    data: { content: { type: 'text', text: '| a | b |\n| :-- | :-- |\n| 1 | 2 |' } },
                },
            });
        });

        expect(mockMarkdownCalls.length).toBeGreaterThan(0);
        expect(mockMarkdownCalls.at(-1).remarkPlugins).toContain('gfm-plugin-stub');
    });

    // A conversation with one prompt and one table in it, which is the
    // shape the two views actually differ over.
    const withATable = async (table) => {
        global.fetch = jest.fn(() => reply(200, { sessionId: 's1', repo: 'repo-agent' }));
        await act(async () => { root.render(<ResearchConversation sessionId="s1" task={taskOf('s1')} />); });
        await flush();

        const ws = FakeSocket.instances[0];
        await act(async () => {
            ws.deliver({ type: 'open', session: { busy: false, offset: 0 } });
            ws.deliver({ type: 'event', offset: 20, event: { seq: 1, kind: 'user_prompt', data: { text: 'compare them' } } });
            ws.deliver({
                type: 'event', offset: 80,
                event: { seq: 2, kind: 'agent_message_chunk', data: { content: { type: 'text', text: table } } },
            });
            ws.deliver({ type: 'event', offset: 100, event: { seq: 3, kind: 'turn_end', data: { stopReason: 'end_turn' } } });
        });
    };

    const chooseView = async (label) => {
        const seg = [...container.querySelectorAll('button')].find(b => b.textContent === label);
        await act(async () => { seg.click(); });
    };

    test('the conversation is a terminal before anybody chooses anything', async () => {
        // There is no page-typeset view to fall back to any more, so
        // the canvas is not something you switch on — it is what a
        // conversation is. A fresh browser with nothing in localStorage
        // is the case that used to land on `rich`.
        await withATable('| a | b |\n| :-- | :-- |\n| 1 | 2 |');

        const pane = container.querySelector('.research-terminal');
        expect(pane).toBeTruthy();
        // Prompt and status line are inside it, not floating below it:
        // one window, not a terminal with a web form stapled under it.
        expect(pane.querySelector('.term-prompt textarea')).toBeTruthy();
        expect(pane.querySelector('.term-statusbar')).toBeTruthy();
        // And the default is still parsed markdown, which is what both
        // retired views did.
        expect(pane.querySelector('.term-agent').className).toContain('term-rendered');
    });

    test('thinking starts open, a tool call as its one line', async () => {
        global.fetch = jest.fn(() => reply(200, { sessionId: 's1', repo: 'repo-agent' }));
        await act(async () => { root.render(<ResearchConversation sessionId="s1" task={taskOf('s1')} />); });
        await flush();
        const ws = FakeSocket.instances[0];
        await act(async () => {
            ws.deliver({ type: 'open', session: { busy: false, offset: 0 } });
            ws.deliver({ type: 'event', offset: 20, event: { seq: 1, kind: 'agent_thought_chunk', data: { content: { type: 'text', text: 'Checking E2E tests' } } } });
            ws.deliver({ type: 'event', offset: 30, event: { seq: 2, kind: 'usage_update', data: { used: 1000 } } });
            ws.deliver({ type: 'event', offset: 40, event: { seq: 3, kind: 'tool_call', data: { toolCallId: 't1', title: './dev/tasks/run-e2e -h', kind: 'execute', status: 'completed', rawInput: { command: './dev/tasks/run-e2e -h' } } } });
        });
        const pane = container.querySelector('.research-terminal');
        expect(pane.textContent).toContain('▾ thinking');
        expect(pane.textContent).toContain('Checking E2E tests');
        expect(pane.textContent).not.toContain('usage_update');
        expect(pane.textContent).toContain('./dev/tasks/run-e2e -h [completed]');
        // The tool's input is behind its line until clicked.
        expect(pane.textContent).not.toContain('"command"');

        const thinking = [...pane.querySelectorAll('.term-dim')].find(e => e.textContent.startsWith('▾ thinking'));
        await act(async () => { thinking.click(); });
        expect(pane.textContent).toContain('▸ thinking');
        expect(pane.textContent).not.toContain('Checking E2E tests');
    });

    test('the prompt sigil does not borrow the spacing that separates turns', async () => {
        // It is the same mark as a user turn's and wants the same
        // colour, which is how it came to wear .term-user — but that
        // class also carries `margin: 12px 0 2px`, the gap between one
        // turn and the last. On the prompt that put the `❯` half a line
        // below the caret beside it, and made the row that much taller
        // than the line in it, which pushed the status line down with
        // it. jsdom has no layout, so the class list is the only place
        // this is visible; the colour lives in a .term-prompt rule now.
        await withATable('| a | b |\n| :-- | :-- |\n| 1 | 2 |');

        const sigil = container.querySelector('.term-prompt .term-sigil');
        expect(sigil.textContent).toBe('❯ ');
        expect(sigil.className).toBe('term-sigil');

        // The transcript's own turns still want that spacing — this is
        // about the prompt only.
        expect(container.querySelector('.term-user').className).toContain('term-user');
    });

    test('the raw view shows the source unparsed and remembers itself', async () => {
        const table = '| a | b |\n| :-- | :-- |\n| 1 | 2 |';
        await withATable(table);

        const rendersBefore = mockMarkdownCalls.length;
        await chooseView('raw');

        // No markdown pass at all in this view: the agent's own line
        // breaks are what make the table line up.
        expect(mockMarkdownCalls.length).toBe(rendersBefore);
        const pane = container.querySelector('.research-terminal');
        expect(pane.querySelector('.term-agent').textContent).toBe(table);
        expect(pane.querySelector('.term-user').textContent).toContain('compare them');
        expect(localStorage.getItem('repoboard.research.view')).toBe('raw');

        // And the next conversation opened comes up in it.
        await act(async () => { root.render(<ResearchConversation sessionId="s1" task={taskOf('s1')} />); });
        await flush();
        expect(container.querySelector('.term-rendered')).toBeNull();
    });

    test('the rendered view parses the markdown on the same canvas', async () => {
        // The one thing the two views differ over: the table gets real
        // borders instead of pipes that happen to line up. Everything
        // else — the face, the canvas, the sigils — is shared.
        const table = '| a | b |\n| :-- | :-- |\n| 1 | 2 |';
        await withATable(table);
        await chooseView('raw');

        const rendersBefore = mockMarkdownCalls.length;
        await chooseView('rendered');

        const pane = container.querySelector('.research-terminal');
        expect(mockMarkdownCalls.length).toBeGreaterThan(rendersBefore);
        expect(mockMarkdownCalls.at(-1).remarkPlugins).toContain('gfm-plugin-stub');
        // Parsed, so it is not a pre-wrap log line any more.
        expect(pane.querySelector('.term-agent').className).toContain('term-rendered');
        expect(localStorage.getItem('repoboard.research.view')).toBe('rendered');

        // Your own prompt is a line you typed, not a document: it keeps
        // its sigil and does not go through markdown in either view.
        expect(pane.querySelector('.term-user').className).toContain('term-line');

        // And raw is still raw — the two are not the same segment.
        await chooseView('raw');
        expect(container.querySelector('.term-rendered')).toBeNull();
        expect(container.querySelector('.term-agent').textContent).toBe(table);
    });

    test('a live turn disables the composer and offers Stop', async () => {
        global.fetch = jest.fn(() => reply(200, { sessionId: 's1', repo: 'repo-agent' }));
        await act(async () => { root.render(<ResearchConversation sessionId="s1" task={taskOf('s1')} />); });
        await flush();

        const ws = FakeSocket.instances[0];
        await act(async () => {
            ws.deliver({ type: 'open', session: { busy: false, offset: 0 } });
            ws.deliver({ type: 'event', offset: 30, event: { seq: 1, kind: 'user_prompt', data: { text: 'go' } } });
        });

        expect(container.textContent).toContain('agent working');
        // Beside Send, not in the placeholder: a placeholder vanishes
        // the moment anyone types, which is when it matters most.
        expect(container.querySelector('[aria-label="Composer state"]').textContent)
            .toContain('Stop to interrupt');
        expect([...container.querySelectorAll('button')].some(b => b.textContent === 'Stop')).toBe(true);

        await act(async () => {
            ws.deliver({ type: 'event', offset: 60, event: { seq: 2, kind: 'turn_end', data: { stopReason: 'end_turn' } } });
        });
        expect(container.textContent).toContain('ready');
    });

    // Stop only asks the engine; one that never ends its turn leaves the
    // session working for good. Once Stop has had its time, it becomes
    // Force stop, which ends the turn on acpd's side once confirmed.
    test('Stop becomes Force stop when the engine does not stop', async () => {
        global.fetch = jest.fn(() => reply(200, { sessionId: 's1', repo: 'repo-agent' }));
        await act(async () => { root.render(<ResearchConversation sessionId="s1" task={taskOf('s1')} />); });
        await flush();
        const ws = FakeSocket.instances[0];
        await act(async () => {
            ws.deliver({ type: 'open', session: { busy: false, offset: 0 } });
            ws.deliver({ type: 'event', offset: 30, event: { seq: 1, kind: 'user_prompt', data: { text: 'go' } } });
        });
        const button = name => [...container.querySelectorAll('button')].find(b => b.textContent === name);
        expect(button('Restart')).toBeUndefined();

        jest.useFakeTimers();
        try {
            global.fetch.mockClear();
            global.fetch.mockImplementation(() => reply(204, {}));
            await act(async () => { button('Stop').click(); });
            await act(async () => { await Promise.resolve(); await Promise.resolve(); });
            expect(global.fetch).toHaveBeenCalledWith(sessionAt('s1') + '/cancel', { method: 'POST' });
            expect(button('Stopping…').disabled).toBe(true);
            expect(button('Force stop')).toBeUndefined();

            await act(async () => { jest.advanceTimersByTime(FORCE_STOP_AFTER_MS); });
            expect(button('Force stop')).toBeTruthy();
            expect(container.querySelector('[aria-label="Composer state"]').textContent).toContain('Force stop ends the turn');

            const confirm = jest.spyOn(window, 'confirm').mockReturnValueOnce(false);
            global.fetch.mockClear();
            await act(async () => { button('Force stop').click(); });
            expect(global.fetch).not.toHaveBeenCalled();
            confirm.mockReturnValueOnce(true);
            await act(async () => { button('Force stop').click(); });
            expect(global.fetch).toHaveBeenCalledWith(sessionAt('s1') + '/stop', { method: 'POST' });
            confirm.mockRestore();
        } finally {
            jest.useRealTimers();
        }

        // The forced turn_end ends the turn: back to ready, and to Stop.
        await act(async () => {
            ws.deliver({ type: 'event', offset: 60, event: { seq: 2, kind: 'turn_end', data: { stopReason: 'cancelled', forced: true } } });
        });
        expect(container.textContent).toContain('ready');
        expect(button('Force stop')).toBeUndefined();
    });

    test('a pending permission is rendered with its options', async () => {
        global.fetch = jest.fn(() => reply(200, { sessionId: 's1', repo: 'repo-agent' }));
        await act(async () => { root.render(<ResearchConversation sessionId="s1" task={taskOf('s1')} />); });
        await flush();

        const ws = FakeSocket.instances[0];
        await act(async () => {
            ws.deliver({ type: 'open', session: { busy: true, offset: 0 } });
            ws.deliver({
                type: 'event', offset: 50,
                event: {
                    seq: 1, kind: 'permission_request',
                    data: {
                        requestId: 'r1',
                        toolCall: { title: 'run go test ./...', kind: 'execute' },
                        options: [
                            { optionId: 'o1', name: 'Allow once', kind: 'allow_once' },
                            { optionId: 'o2', name: 'Reject', kind: 'reject_once' },
                        ],
                    },
                },
            });
        });

        expect(container.textContent).toContain('The agent needs permission');
        expect(container.textContent).toContain('run go test ./...');
        const allow = [...container.querySelectorAll('button')].find(b => b.textContent === 'Allow once');
        expect(allow).toBeTruthy();

        global.fetch.mockClear();
        global.fetch.mockImplementation(() => reply(204, {}));
        await act(async () => { allow.click(); });
        expect(global.fetch).toHaveBeenCalledWith(`${sessionAt('s1')}/permission`, expect.objectContaining({
            method: 'POST',
            body: JSON.stringify({ requestId: 'r1', optionId: 'o1' }),
        }));
    });

    // A turn blocked on a permission is a busy turn, so the pill used to
    // read "agent working" — true, and the opposite of useful: it reads as
    // progress to the one person who could unblock it. The pill is what
    // you can see without scrolling, so it is where the ask belongs.
    test('a turn blocked on a permission says so in the status pill', async () => {
        global.fetch = jest.fn(() => reply(200, { sessionId: 's1', repo: 'repo-agent' }));
        await act(async () => { root.render(<ResearchConversation sessionId="s1" task={taskOf('s1')} />); });
        await flush();

        const pill = () => [...container.querySelectorAll('span')]
            .find(s => /agent working|needs permission|^ready$/.test(s.textContent));

        const ws = FakeSocket.instances[0];
        await act(async () => {
            ws.deliver({ type: 'open', session: { busy: false, offset: 0 } });
            ws.deliver({ type: 'event', offset: 30, event: { seq: 1, kind: 'user_prompt', data: { text: 'go' } } });
        });
        expect(pill().textContent).toBe('agent working');

        await act(async () => {
            ws.deliver({
                type: 'event', offset: 60,
                event: {
                    seq: 2, kind: 'permission_request',
                    data: {
                        requestId: 'r1',
                        toolCall: { title: 'git show d1254bd', kind: 'execute' },
                        options: [{ optionId: 'o1', name: 'Allow', kind: 'allow_once' }],
                    },
                },
            });
        });
        expect(pill().textContent).toContain('needs permission');
        // And names the call, so the pill answers "permission for what?"
        // without a scroll.
        expect(pill().getAttribute('title')).toContain('git show d1254bd');

        // Answered, it goes back to reporting the turn it is in.
        await act(async () => {
            ws.deliver({
                type: 'event', offset: 90,
                event: { seq: 3, kind: 'permission_resolved', data: { requestId: 'r1', outcome: 'selected', optionId: 'o1' } },
            });
        });
        expect(pill().textContent).toBe('agent working');
    });

    test('the header carries the title and renames it in place', async () => {
        global.fetch = jest.fn((url, opts) => {
            if (opts && opts.method === 'PATCH') return reply(200, { sessionId: 's1', title: 'retry loop' });
            return reply(200, { sessionId: 's1', repo: 'repo-agent', title: 'overview' });
        });
        await act(async () => { root.render(<ResearchConversation sessionId="s1" task={taskOf('s1')} title="overview" />); });
        await flush();
        expect(container.querySelector('strong').textContent).toBe('overview');

        await act(async () => { container.querySelector('strong').click(); });
        const field = container.querySelector('input[aria-label="Session title"]');
        expect(field.value).toBe('overview');
        await act(async () => {
            nativeSet(field, 'retry loop');
            field.dispatchEvent(new Event('input', { bubbles: true }));
            field.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
        });
        await flush();

        expect(global.fetch).toHaveBeenCalledWith('/api/research/s1', expect.objectContaining({
            method: 'PATCH',
            body: JSON.stringify({ title: 'retry loop' }),
        }));
        expect(container.querySelector('strong').textContent).toBe('retry loop');
    });

    test('an unnamed session rests in the box that asks for a name', async () => {
        // It used to fall back to the repo, so the one session that
        // needed naming was the one that looked like it had a name —
        // and the repo is a link two inches to the right anyway.
        global.fetch = jest.fn(() => reply(200, { sessionId: 's1', repo: 'repo-agent' }));
        await act(async () => { root.render(<ResearchConversation sessionId="s1" task={taskOf('s1')} />); });
        await flush();

        expect(container.querySelector('strong')).toBeNull();
        const field = container.querySelector('input[aria-label="Session title"]');
        expect(field.value).toBe('');
        expect(field.placeholder).toBe('Name this conversation…');
    });

    test('an unnamed session names itself from its first question', async () => {
        // TitleAnnotation has promised this fallback since it existed.
        // The list cannot do it — it is built from annotations and will
        // not read N transcripts to draw a sidebar — but the
        // conversation has the transcript open already, so one PATCH
        // makes the name durable for the list too.
        const patched = [];
        global.fetch = jest.fn((url, opts) => {
            if (opts && opts.method === 'PATCH') {
                patched.push(JSON.parse(opts.body));
                return reply(200, { sessionId: 's1', title: 'where does the retry loop live' });
            }
            return reply(200, { sessionId: 's1', repo: 'repo-agent' });
        });
        await act(async () => { root.render(<ResearchConversation sessionId="s1" task={taskOf('s1')} />); });
        await flush();

        const ws = FakeSocket.instances[0];
        await act(async () => {
            ws.deliver({ type: 'open', session: { busy: false, offset: 0 } });
            ws.deliver({
                type: 'event', offset: 40,
                event: {
                    seq: 1, kind: 'user_prompt',
                    data: { text: 'where does the retry loop live, and what backs it off?' },
                },
            });
            ws.deliver({ type: 'event', offset: 60, event: { seq: 2, kind: 'turn_end', data: { stopReason: 'end_turn' } } });
        });
        await flush();

        // Sent raw: Truncate on the server is what a title means, and
        // duplicating that rule here would be a second answer to it.
        expect(patched).toEqual([{ title: 'where does the retry loop live, and what backs it off?' }]);
        // And what comes back is what appears — never the paragraph.
        expect(container.querySelector('strong').textContent).toBe('where does the retry loop live');
    });

    test('a session the server has already named does not rename itself', async () => {
        // name is seeded from the row that was clicked, so the guard has
        // to be the server's title. A canned or topic session is named
        // at creation, which is what keeps the rendered brief in
        // topic.txt from ever becoming somebody's session name.
        global.fetch = jest.fn((url, opts) => {
            if (opts && opts.method === 'PATCH') throw new Error('should not rename a named session');
            return reply(200, { sessionId: 's1', repo: 'repo-agent', title: 'overview' });
        });
        await act(async () => { root.render(<ResearchConversation sessionId="s1" task={taskOf('s1')} />); });
        await flush();

        const ws = FakeSocket.instances[0];
        await act(async () => {
            ws.deliver({ type: 'open', session: { busy: false, offset: 0 } });
            ws.deliver({
                type: 'event', offset: 40,
                event: { seq: 1, kind: 'user_prompt', data: { text: 'You are researching the repository…' } },
            });
        });
        await flush();

        expect(container.querySelector('strong').textContent).toBe('overview');
    });

    test('a session the old research path made says to start a new one and opens no socket', async () => {
        global.fetch = jest.fn(() => reply(200, {}));
        await act(async () => { root.render(<ResearchConversation sessionId="s1" legacy />); });
        await flush();

        expect(container.textContent).toContain('start a new one');
        expect(global.fetch).not.toHaveBeenCalled();
        expect(FakeSocket.instances).toHaveLength(0);
    });

    test('a claim with no task yet waits for one, and opens it when the list brings it', async () => {
        global.fetch = jest.fn(() => reply(200, { repo: 'repo-agent' }));
        await act(async () => { root.render(<ResearchConversation sessionId="s1" pending />); });
        await flush();
        expect(global.fetch).not.toHaveBeenCalled();
        expect(container.textContent).toContain('Preparing the sandbox');

        await act(async () => { root.render(<ResearchConversation sessionId="s1" pending task={taskOf('s1')} />); });
        await flush();
        expect(global.fetch).toHaveBeenCalledWith(sessionAt('s1'));
        expect(FakeSocket.instances).toHaveLength(1);
    });

    test('a paused session says so and opens no socket', async () => {
        global.fetch = jest.fn(() => reply(409, { error: 'research session is paused', paused: true, sandbox: 'rsch-x' }));
        await act(async () => { root.render(<ResearchConversation sessionId="s1" task={taskOf('s1')} />); });
        await flush();

        expect(FakeSocket.instances).toHaveLength(0);
        expect(container.textContent).toContain('paused');
        expect(container.textContent).toContain('scaled to zero');
        expect(container.querySelector('textarea').disabled).toBe(true);
    });

    test('a paused session wakes its sandbox from the session view', async () => {
        global.fetch = jest.fn(url => url.includes('/lifecycle')
            ? reply(200, {})
            : reply(409, { error: 'research session is paused', paused: true, sandbox: 'rsch-s1' }));
        await act(async () => { root.render(<ResearchConversation sessionId="s1" task={taskOf('s1')} />); });
        await flush();

        // The header's call to action, not an item of the ⋯ menu.
        const button = [...container.querySelectorAll('button')].find(b => b.textContent === '▶ Wake sandbox');
        expect(button).toBeTruthy();
        expect(button.className).toContain('btn-submit');
        await act(async () => { button.click(); });
        await flush();
        await flush();

        expect(global.fetch).toHaveBeenCalledWith('/api/sandbox-card/rsch-s1/lifecycle', expect.objectContaining({
            method: 'POST', body: JSON.stringify({ action: 'wake' }),
        }));
        expect(container.textContent).toContain('waking the sandbox');
        expect(container.textContent).not.toContain('scaled to zero');
    });

    // The modes gemini advertises, as the API forwards them.
    const modes = [
        { id: 'default', name: 'Default', description: 'Prompts for approval' },
        { id: 'autoEdit', name: 'Auto Edit', description: 'Auto-approves edit tools' },
        { id: 'yolo', name: 'YOLO', description: 'Auto-approves all tools' },
    ];

    test('the header offers the engine\'s modes and switches between them', async () => {
        global.fetch = jest.fn(() => reply(200, {
            sessionId: 's1', repo: 'repo-agent', mode: 'yolo', availableModes: modes,
        }));
        await act(async () => { root.render(<ResearchConversation sessionId="s1" task={taskOf('s1')} />); });
        await flush();
        await act(async () => {
            FakeSocket.instances[0].deliver({
                type: 'open', session: { busy: false, offset: 0, mode: 'yolo', availableModes: modes },
            });
        });

        const picker = container.querySelector('select[aria-label="Approval mode"]');
        expect(picker).toBeTruthy();
        // Research starts auto-approving; the control is there to tighten
        // that, so it has to open on what the session is actually in.
        expect(picker.value).toBe('yolo');
        expect([...picker.options].map(o => o.textContent)).toEqual(['Default', 'Auto Edit', 'YOLO']);

        global.fetch.mockClear();
        global.fetch.mockImplementation(() => reply(200, {
            sessionId: 's1', mode: 'default', availableModes: modes,
        }));
        await act(async () => {
            nativeSet(picker, 'default');
            picker.dispatchEvent(new Event('change', { bubbles: true }));
        });
        await flush();

        expect(global.fetch).toHaveBeenCalledWith(`${sessionAt('s1')}/mode`, expect.objectContaining({
            method: 'POST',
            body: JSON.stringify({ mode: 'default' }),
        }));
        expect(container.querySelector('select[aria-label="Approval mode"]').value).toBe('default');
    });

    test('a mode changed elsewhere arrives on the stream and moves the control', async () => {
        global.fetch = jest.fn(() => reply(200, {
            sessionId: 's1', repo: 'repo-agent', mode: 'yolo', availableModes: modes,
        }));
        await act(async () => { root.render(<ResearchConversation sessionId="s1" task={taskOf('s1')} />); });
        await flush();

        const ws = FakeSocket.instances[0];
        await act(async () => {
            ws.deliver({ type: 'open', session: { busy: false, offset: 0, mode: 'yolo', availableModes: modes } });
            ws.deliver({
                type: 'event', offset: 40,
                event: { seq: 1, kind: 'mode_changed', data: { currentModeId: 'default' } },
            });
        });

        // Another tab, or the engine leaving a mode on its own: the reply
        // to our own POST is not the only way a mode changes.
        expect(container.querySelector('select[aria-label="Approval mode"]').value).toBe('default');
        expect(container.textContent).toContain('approval mode: default');
    });

    test('the picker is labelled by what it means, not by the mode name', async () => {
        global.fetch = jest.fn(() => reply(200, {
            sessionId: 's1', repo: 'repo-agent', mode: 'yolo', availableModes: modes,
        }));
        await act(async () => { root.render(<ResearchConversation sessionId="s1" task={taskOf('s1')} />); });
        await flush();
        await act(async () => {
            FakeSocket.instances[0].deliver({
                type: 'open',
                session: { busy: false, offset: 0, mode: 'yolo', availableModes: modes, autoApprove: true },
            });
        });

        expect(container.textContent).toContain('auto-approving');

        // Tightening it flips the label — and the label follows the
        // server's answer, not the mode's name, because the mode is
        // only half of what decides whether anything stops to ask.
        global.fetch.mockImplementation(() => reply(200, {
            sessionId: 's1', mode: 'default', availableModes: modes, autoApprove: false,
        }));
        const picker = container.querySelector('select[aria-label="Approval mode"]');
        await act(async () => {
            nativeSet(picker, 'default');
            picker.dispatchEvent(new Event('change', { bubbles: true }));
        });
        await flush();

        expect(container.textContent).not.toContain('auto-approving');
        expect(container.textContent).toContain('asks first');
    });

    test('the body says how the session runs even when nothing switched it', async () => {
        // A research session is born auto-approving and usually never
        // switches, so mode_changed never fires and the transcript used
        // to go its whole life without mentioning it.
        global.fetch = jest.fn(() => reply(200, {
            sessionId: 's1', repo: 'repo-agent', mode: 'yolo', availableModes: modes,
        }));
        await act(async () => { root.render(<ResearchConversation sessionId="s1" task={taskOf('s1')} />); });
        await flush();
        await act(async () => {
            FakeSocket.instances[0].deliver({
                type: 'open',
                session: { busy: false, offset: 0, mode: 'yolo', availableModes: modes, autoApprove: true },
            });
        });

        expect(container.textContent).toContain('This conversation runs in');
        expect(container.textContent).toContain('tool calls are approved for you');
    });

    test('a probe that could not reach acpd does not claim the session asks first', async () => {
        // A probe that reports live:false never got to the session, so
        // it says nothing about the answering. Reading that silence as
        // "no" would tell the member the calm story — this paused
        // session will ask you first — on no evidence at all.
        jest.useFakeTimers();
        try {
            global.fetch = jest.fn(() => reply(200, {
                sessionId: 's1', repo: 'repo-agent', live: true,
                mode: 'yolo', availableModes: modes, autoApprove: true,
            }));
            await act(async () => { root.render(<ResearchConversation sessionId="s1" task={taskOf('s1')} />); });
            await flush();
            await act(async () => {
                FakeSocket.instances[0].deliver({
                    type: 'open',
                    session: { busy: false, offset: 0, mode: 'yolo', availableModes: modes, autoApprove: true },
                });
            });
            expect(container.textContent).toContain('auto-approving');

            // The sandbox is scaled to zero: the socket drops and the
            // probe that follows can no longer see the session.
            global.fetch.mockImplementation(() => reply(200, {
                sessionId: 's1', repo: 'repo-agent', live: false, paused: 'scaled to zero',
            }));
            await act(async () => { FakeSocket.instances[0].onclose(); });
            await act(async () => { jest.advanceTimersByTime(6000); });
            await flush();

            expect(global.fetch).toHaveBeenCalledTimes(2);
            expect(container.textContent).toContain('auto-approving');
            expect(container.textContent).not.toContain('asks first');
        } finally {
            jest.useRealTimers();
        }
    });

    test('the once-a-session actions live behind the overflow, not in the header', async () => {
        global.fetch = jest.fn(() => reply(200, {
            sessionId: 's1', repo: 'repo-agent', sandbox: 'rsch-1', namespace: 'barney-s',
        }));
        await act(async () => { root.render(<ResearchConversation sessionId="s1" task={taskOf('s1')} />); });
        await flush();

        const labels = () => [...container.querySelectorAll('button, a')].map(e => e.textContent);
        expect(labels()).not.toContain('Delete');
        expect(labels()).not.toContain('terminal ↗');

        const more = container.querySelector('[aria-label="More actions"]');
        await act(async () => { more.dispatchEvent(new MouseEvent('click', { bubbles: true })); });

        expect(labels()).toContain('Delete');
        expect(labels()).toContain('terminal ↗');

        // And it closes again, so Delete is never one stray click away
        // from the controls used every turn.
        await act(async () => { more.dispatchEvent(new MouseEvent('click', { bubbles: true })); });
        expect(labels()).not.toContain('Delete');
    });

    test('the composer says why it will not send, and keeps saying it while you type', async () => {
        global.fetch = jest.fn(() => reply(200, { sessionId: 's1', repo: 'repo-agent' }));
        await act(async () => { root.render(<ResearchConversation sessionId="s1" task={taskOf('s1')} />); });
        await flush();

        const ws = FakeSocket.instances[0];
        await act(async () => {
            ws.deliver({ type: 'open', session: { busy: false, offset: 0 } });
            ws.deliver({
                type: 'event', offset: 50,
                event: {
                    seq: 1, kind: 'permission_request',
                    data: { requestId: 'p1', toolCall: { title: 'rm -rf /' }, options: [] },
                },
            });
        });

        const state = () => container.querySelector('[aria-label="Composer state"]').textContent;
        expect(state()).toContain('Waiting for you');

        // The composer invites a follow-up, not a fresh question: the
        // landing pane is where you start one, and a composer wearing
        // the same words reads as a second place to do that rather
        // than the place you carry on.
        expect(container.querySelector('textarea').placeholder)
            .toBe('Continue the research — ask a follow-up…');

        // The placeholder used to carry this, and a placeholder is gone
        // the moment anybody types into the box it was in.
        const box = container.querySelector('textarea');
        await act(async () => {
            nativeSet(box, 'never mind, do something else');
            box.dispatchEvent(new Event('input', { bubbles: true }));
        });
        expect(state()).toContain('Waiting for you');
    });

    test('the composer grows with what you type, and stops before it eats the transcript', async () => {
        // jsdom has no layout, so every element it renders reports a
        // scrollHeight of zero and the component would measure nothing.
        // Standing in for the browser here is what makes the wiring —
        // collapse, measure, set — observable at all; the arithmetic
        // itself is grownHeight's own test.
        let contentHeight = 0;
        const real = Object.getOwnPropertyDescriptor(window.HTMLElement.prototype, 'scrollHeight');
        Object.defineProperty(window.HTMLTextAreaElement.prototype, 'scrollHeight', {
            configurable: true,
            // Not just the content: a browser's scrollHeight is the
            // content's height *or* the height already set, whichever
            // is larger. Modelling that is the point — it is the only
            // reason the component has to collapse the box before it
            // measures, and a stub that returned the content alone
            // would let that step be deleted without a test noticing.
            // 'auto' parses to NaN, which is the collapsed case.
            get() { return Math.max(contentHeight, parseFloat(this.style.height) || 0); },
        });

        try {
            global.fetch = jest.fn(() => reply(200, { sessionId: 's1', repo: 'repo-agent' }));
            await act(async () => { root.render(<ResearchConversation sessionId="s1" task={taskOf('s1')} />); });
            await flush();
            await act(async () => {
                FakeSocket.instances[0].deliver({ type: 'open', session: { busy: false, offset: 0 } });
            });

            const box = container.querySelector('.term-prompt textarea');
            // rows, not a height, is what the box falls back to before
            // anything has been measured — and it is one line, not the
            // three the floating card used to reserve.
            expect(box.rows).toBe(1);

            const type = async (text, height) => {
                contentHeight = height;
                await act(async () => {
                    nativeSet(box, text);
                    box.dispatchEvent(new Event('input', { bubbles: true }));
                });
            };

            // jsdom reports no line-height, so the component falls back
            // to 16px a line: ten rows is a 160px ceiling.
            await type('one line', 16);
            expect(box.style.height).toBe('16px');
            expect(box.style.overflowY).toBe('hidden');

            await type('four\nlines\nof\nit', 64);
            expect(box.style.height).toBe('64px');

            // Back down again. Without the collapse-before-measure step
            // scrollHeight would still report the height already set,
            // and a box that had once been tall would stay tall.
            await type('one line', 16);
            expect(box.style.height).toBe('16px');

            // A pasted stack trace stops at the cap and scrolls, rather
            // than pushing the conversation it is a follow-up to off
            // the screen.
            await type('a hundred lines of goroutine dump', 1600);
            expect(box.style.height).toBe('160px');
            expect(box.style.overflowY).toBe('auto');
        } finally {
            delete window.HTMLTextAreaElement.prototype.scrollHeight;
            if (real) Object.defineProperty(window.HTMLElement.prototype, 'scrollHeight', real);
        }
    });

    test('a mode the session did not get is flagged beside the picker', async () => {
        const refused = 'session/set_mode "yolo": Cannot enable privileged approval modes in an untrusted folder.';
        global.fetch = jest.fn(() => reply(200, {
            sessionId: 's1', repo: 'repo-agent', mode: 'default', availableModes: modes, modeError: refused,
        }));
        await act(async () => { root.render(<ResearchConversation sessionId="s1" task={taskOf('s1')} />); });
        await flush();
        await act(async () => {
            FakeSocket.instances[0].deliver({
                type: 'open',
                session: { busy: false, offset: 0, mode: 'default', availableModes: modes, modeError: refused },
            });
        });

        // The session works, so nothing here blocks: the picker still
        // shows what it is really in, with the reason it is not what was
        // asked for hanging off it.
        expect(container.querySelector('select[aria-label="Approval mode"]').value).toBe('default');
        const warning = container.querySelector('[aria-label="Approval mode warning"]');
        expect(warning).toBeTruthy();
        expect(warning.title).toContain('untrusted folder');

        // And a switch that works settles it, without waiting for a
        // probe to come back and say so.
        global.fetch.mockImplementation(() => reply(200, {
            sessionId: 's1', mode: 'yolo', availableModes: modes,
        }));
        const picker = container.querySelector('select[aria-label="Approval mode"]');
        await act(async () => {
            nativeSet(picker, 'yolo');
            picker.dispatchEvent(new Event('change', { bubbles: true }));
        });
        await flush();

        expect(container.querySelector('[aria-label="Approval mode warning"]')).toBeNull();
    });

    test('an engine that offers no modes gets no control', async () => {
        global.fetch = jest.fn(() => reply(200, { sessionId: 's1', repo: 'repo-agent' }));
        await act(async () => { root.render(<ResearchConversation sessionId="s1" task={taskOf('s1')} />); });
        await flush();
        await act(async () => {
            FakeSocket.instances[0].deliver({ type: 'open', session: { busy: false, offset: 0 } });
        });

        expect(container.querySelector('select[aria-label="Approval mode"]')).toBeNull();
    });

    test('a 404 is "gone" for an existing session and "starting" for a just-claimed one', async () => {
        global.fetch = jest.fn(() => reply(404, { error: 'research session not found' }));

        await act(async () => { root.render(<ResearchConversation sessionId="s1" task={taskOf('s1')} />); });
        await flush();
        expect(container.textContent).toContain('no longer exists');

        // The same 404 seconds after filing a mailbox claim means the
        // controller has not built the sandbox yet, not that it is gone.
        await act(async () => { root.render(<ResearchConversation sessionId="s2" task={taskOf('s2')} pending />); });
        await flush();
        expect(container.textContent).toContain('Preparing the sandbox');
    });

    test('the repo in the header is a way to get to it', async () => {
        const status = {
            sessionId: 's1', sandbox: 'rsch-open-rl-1', namespace: 'ns',
            repo: 'open-rl', title: 'the retry loop', live: false,
        };
        global.fetch = jest.fn(() => reply(200, {
            ...status, htmlUrl: 'https://github.com/gke-labs/open-rl',
        }));

        await act(async () => { root.render(<ResearchConversation sessionId="s1" task={taskOf('s1')} />); });
        await flush();

        const link = [...container.querySelectorAll('a')].find(a => a.textContent === 'open-rl');
        expect(link).toBeTruthy();
        expect(link.getAttribute('href')).toBe('https://github.com/gke-labs/open-rl');

        // A sandbox annotated with no URL still says which repo it is;
        // it just does not pretend to be a link to nowhere.
        global.fetch = jest.fn(() => reply(200, status));
        await act(async () => { root.render(<ResearchConversation sessionId="s3" task={taskOf('s3')} />); });
        await flush();
        expect(container.textContent).toContain('open-rl');
        expect([...container.querySelectorAll('a')].map(a => a.textContent)).not.toContain('open-rl');
    });
});

describe('mermaid fences', () => {
    // What react-markdown hands a `pre` for a fenced block: a hast node
    // with the code element inside it, source text and all.
    const fence = (lang, text) => ({
        type: 'element', tagName: 'pre', properties: {},
        children: [{
            type: 'element', tagName: 'code',
            properties: { className: lang ? [`language-${lang}`] : [] },
            children: [{ type: 'text', value: text + '\n' }],
        }],
    });
    const Pre = markdownComponents.pre;
    // The draw is a chain of awaits — the lazy import, parse, render —
    // so a fixed count of microtask flushes is a guess. A few real
    // macrotask turns are not.
    const settle = async () => {
        for (let i = 0; i < 3; i++) {
            await act(async () => { await new Promise(r => setTimeout(r, 0)); });
        }
    };

    beforeEach(() => {
        mockMermaid.parse.mockImplementation(async (code) => /^flowchart /.test(code));
        mockMermaid.render.mockImplementation(async () => ({ svg: '<svg></svg>' }));
    });

    test('a mermaid fence is drawn as a diagram', async () => {
        const src = 'flowchart TB\n  A --> B';
        await act(async () => { root.render(<Pre node={fence('mermaid', src)}><code>{src}</code></Pre>); });
        await settle();

        const svg = container.querySelector('.term-diagram svg');
        expect(svg).toBeTruthy();
        // The trailing newline the fence carries is not part of the diagram.
        expect(mockMermaid.render).toHaveBeenCalledWith(expect.any(String), src);
        expect(container.querySelector('pre')).toBeNull();
    });

    test('a fence that does not parse yet stays its source', async () => {
        // Which is every mermaid fence for most of the time it streams.
        const src = 'flow';
        await act(async () => { root.render(<Pre node={fence('mermaid', src)}><code>{src}</code></Pre>); });
        await settle();

        expect(mockMermaid.parse).toHaveBeenCalled();
        expect(mockMermaid.render).not.toHaveBeenCalled();
        expect(container.querySelector('.term-diagram')).toBeNull();
        expect(container.querySelector('pre code.language-mermaid').textContent).toBe(src);
    });

    test('a fence that parses but will not draw falls back to its source', async () => {
        // Layout can fail after parsing succeeds. Mermaid's answer to
        // that is an error graphic; ours is the text the agent wrote.
        mockMermaid.render.mockImplementationOnce(async () => { throw new Error('layout failed'); });
        const src = 'flowchart TB\n  A --> B';
        await act(async () => { root.render(<Pre node={fence('mermaid', src)}><code /></Pre>); });
        await settle();

        expect(mockMermaid.render).toHaveBeenCalled();
        expect(container.querySelector('.term-diagram')).toBeNull();
        expect(container.querySelector('pre code.language-mermaid').textContent).toBe(src);
        // And mermaid was told not to draw its own error graphic, which
        // it would otherwise leave behind on document.body.
        expect(mockMermaid.initialize).toHaveBeenCalledWith(expect.objectContaining({ suppressErrorRendering: true }));
    });

    test('a diagram of an older state of the fence is not shown', async () => {
        const drawn = 'flowchart TB\n  A --> B';
        await act(async () => { root.render(<Pre node={fence('mermaid', drawn)}><code /></Pre>); });
        await settle();
        expect(container.querySelector('.term-diagram')).toBeTruthy();

        // More text arrives; until it settles and redraws, the page
        // shows the new source rather than the old picture.
        const grown = drawn + '\n  B --> C';
        await act(async () => { root.render(<Pre node={fence('mermaid', grown)}><code /></Pre>); });
        expect(container.querySelector('.term-diagram')).toBeNull();
        expect(container.querySelector('pre code').textContent).toBe(grown);

        // Real time, not fake timers: the redraw waits out the stream.
        await act(async () => { await new Promise(r => setTimeout(r, 300)); });
        await settle();
        expect(mockMermaid.render).toHaveBeenLastCalledWith(expect.any(String), grown);
        expect(container.querySelector('.term-diagram')).toBeTruthy();
    });

    test('every other fence is left to react-markdown', async () => {
        await act(async () => { root.render(<Pre node={fence('go', 'x := 1')}><code>x := 1</code></Pre>); });
        await settle();

        expect(container.querySelector('pre code').textContent).toBe('x := 1');
        expect(mockMermaid.parse).not.toHaveBeenCalled();
    });
});

describe('ResearchConversation on a task session', () => {
    const task = { sandbox: 'fix-granule-42', task: 'recipe-plan-20261004-120000-ab12' };
    const render = async (body, status = 200) => {
        global.fetch = jest.fn(() => reply(status, body));
        await act(async () => {
            root.render(<ResearchConversation task={task} title="plan · fix-granule-42" />);
        });
        await flush();
    };

    test('is reached at the task session routes, with no rename or delete', async () => {
        await render({ sessionId: task.task, sandbox: task.sandbox, namespace: 'ns', repo: 'granule' });
        expect(global.fetch).toHaveBeenCalledWith('/api/task-sessions/fix-granule-42/recipe-plan-20261004-120000-ab12');
        expect(FakeSocket.instances[0].url)
            .toContain('/api/task-session-events/fix-granule-42/recipe-plan-20261004-120000-ab12?offset=0');
        expect(container.textContent).toContain('plan · fix-granule-42');
        expect(container.querySelector('input[aria-label="Session title"]')).toBeNull();
        await act(async () => { container.querySelector('[aria-label="More actions"]').click(); });
        expect(container.textContent).not.toContain('Delete');
    });

    test('a running task is watched, not driven', async () => {
        await render({ sessionId: task.task, repo: 'granule' });
        await act(async () => {
            FakeSocket.instances[0].deliver({ type: 'open', session: { busy: true, held: true, task: task.task, offset: 0 } });
        });
        const box = container.querySelector('textarea');
        await act(async () => {
            nativeSet(box, 'why this approach?');
            box.dispatchEvent(new Event('input', { bubbles: true }));
        });
        expect(container.querySelector('.term-send').disabled).toBe(true);
        expect(container.textContent).toContain('The task is still running');
        expect(container.textContent).not.toContain('Stop');
    });

    test('says when the agent starts fresh', async () => {
        await render({ sessionId: task.task, repo: 'granule' });
        await act(async () => {
            FakeSocket.instances[0].deliver({ type: 'open', session: { busy: false, task: task.task, loaded: false, offset: 0 } });
        });
        expect(container.textContent).toContain('starts fresh');
        expect(container.textContent).not.toContain('The task is still running');
    });

    test('a session\'s group runs its recipes here, and its leader in a new conversation', async () => {
        const care = { verb: 'recipe', recipe: 'care', label: 'Care', enabled: true };
        const ci = { verb: 'recipe', recipe: 'care-ci', label: 'Fix CI', enabled: true };
        await render({ sessionId: task.task, repo: 'granule', recipes: [care, ci] });
        await act(async () => {
            FakeSocket.instances[0].deliver({ type: 'open', session: { busy: false, task: task.task, offset: 0 } });
        });
        global.fetch = jest.fn((url, opts) => (opts && opts.method === 'POST'
            ? reply(202, { request: 'r' })
            : reply(200, { sessionId: task.task, recipes: [care, ci] })));
        await act(async () => { [...container.querySelectorAll('button')].find(b => b.textContent === 'Fix CI').click(); });
        expect(global.fetch).toHaveBeenCalledWith(
            '/api/task-sessions/fix-granule-42/recipe-plan-20261004-120000-ab12/recipe',
            expect.objectContaining({ method: 'POST', body: JSON.stringify({ recipe: 'care-ci' }) }));
        await flush();

        window.confirm = jest.fn(() => true);
        await act(async () => { container.querySelector('[aria-label="More actions"]').click(); });
        await act(async () => { [...container.querySelectorAll('[role=menuitem]')].find(b => b.textContent === 'New conversation').click(); });
        expect(global.fetch).toHaveBeenCalledWith(
            '/api/task-sessions/fix-granule-42/recipe-plan-20261004-120000-ab12/recipe',
            expect.objectContaining({ method: 'POST', body: JSON.stringify({ recipe: 'care', newSession: true }) }));
    });

    test('a recipe of the session is followed while it runs, and says when it is done', async () => {
        const offered = { verb: 'recipe', recipe: 'care-ci', label: 'Fix CI', enabled: true };
        await render({ sessionId: task.task, repo: 'granule', recipes: [offered] });
        await act(async () => {
            FakeSocket.instances[0].deliver({ type: 'open', session: { busy: false, task: task.task, offset: 0 } });
        });
        const button = () => [...container.querySelectorAll('button')].find(b => b.textContent.startsWith('Fix CI'));
        expect(button().disabled).toBe(false);

        let status = { ...offered, enabled: false, reason: 'running' };
        global.fetch = jest.fn((url, opts) => (opts && opts.method === 'POST'
            ? reply(202, { request: 'r' })
            : reply(200, { sessionId: task.task, recipes: [status] })));
        jest.useFakeTimers();
        try {
            await act(async () => { button().click(); });
            await act(async () => { await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); });
            expect(button().textContent).toBe('Fix CI…');
            expect(button().disabled).toBe(true);

            status = offered;
            await act(async () => { jest.advanceTimersByTime(3000); });
            await act(async () => { await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); });
        } finally {
            jest.useRealTimers();
        }
        expect(button().disabled).toBe(false);
        expect(container.textContent).toContain('Fix CI: done');
    });

    test('a recipe that failed says why', async () => {
        await render({
            sessionId: task.task, repo: 'granule',
            recipes: [{ verb: 'recipe', recipe: 'care-ci', label: 'Fix CI', enabled: true, error: 'the session is busy' }],
        });
        expect(container.textContent).toContain('Fix CI failed: the session is busy');
    });

    test('a recipe waits for the turn in flight, and for a running task', async () => {
        await render({
            sessionId: task.task, repo: 'granule',
            recipes: [{ verb: 'recipe', recipe: 'care-ci', label: 'Fix CI', enabled: true }],
        });
        await act(async () => {
            FakeSocket.instances[0].deliver({ type: 'open', session: { busy: true, held: true, task: task.task, offset: 0 } });
        });
        const button = [...container.querySelectorAll('button')].find(b => b.textContent === 'Fix CI');
        expect(button.disabled).toBe(true);
    });

    test('a session in no group has no recipe buttons', async () => {
        await render({ sessionId: task.task, repo: 'granule' });
        await act(async () => {
            FakeSocket.instances[0].deliver({ type: 'open', session: { busy: false, task: task.task, offset: 0 } });
        });
        expect([...container.querySelectorAll('button')].some(b => b.textContent === 'Fix CI' || b.textContent === 'Update plan')).toBe(false);
    });

    test('a turn that ends reads the session again, for the draft the agent wrote', async () => {
        await render({ sessionId: task.task, repo: 'granule' });
        const ws = FakeSocket.instances[0];
        await act(async () => {
            ws.deliver({ type: 'open', session: { busy: false, task: task.task, offset: 0 } });
        });
        global.fetch = jest.fn(() => reply(200, {
            sessionId: task.task, repo: 'granule',
            draft: { kind: 'Plan', markdown: '## Summary\nRewritten.', actions: [] },
        }));
        await act(async () => {
            ws.deliver({ type: 'event', offset: 20, event: { seq: 1, kind: 'user_prompt', data: { text: 'save the plan' } } });
        });
        await act(async () => {
            ws.deliver({ type: 'event', offset: 40, event: { seq: 2, kind: 'turn_end', data: { stopReason: 'end_turn' } } });
        });
        await flush();
        expect(container.textContent).toContain('Plan draft');
    });

    test('an older image falls back to the terminal', async () => {
        await render({ error: 'no sessions', legacy: true, sandbox: task.sandbox, namespace: 'ns' }, 409);
        expect(FakeSocket.instances).toHaveLength(0);
        const link = [...container.querySelectorAll('a')].find(a => a.textContent.includes('terminal'));
        expect(link.getAttribute('href')).toBe('#/terminal/ns/fix-granule-42?chat=plan');
    });
});

describe('ResearchConversation notes and draft', () => {
    // One view for every recipe: a research conversation's notes draft
    // comes from the same field a plan's draft does.
    const notesDraft = {
        kind: 'Notes', markdown: '# Findings\n\nthe backoff is linear', note: 'backoff', draftedAt: '2026-10-05T10:00:00Z',
        actions: [
            { verb: 'push-notes', label: 'Save to research/notes', enabled: true },
            { verb: 'edit', enabled: true },
            { verb: 'reject', enabled: true },
        ],
    };
    const at = sessionAt('s1');
    const open = async (status, calls = []) => {
        global.fetch = jest.fn((url, opts) => {
            if (opts && opts.method) {
                calls.push({ url, method: opts.method, body: opts.body });
                return reply(202, {});
            }
            return reply(200, { research: 's1', repo: 'granule', sandbox: 'rsch-s1', namespace: 'ns', ...status });
        });
        await act(async () => { root.render(<ResearchConversation sessionId="s1" task={taskOf('s1')} />); });
        await flush();
        await act(async () => {
            FakeSocket.instances[0].deliver({ type: 'open', session: { busy: false, task: 'research-1', offset: 0 } });
        });
        return calls;
    };
    const button = (text) => [...container.querySelectorAll('button')].find(b => b.textContent === text);
    const click = async (el) => {
        await act(async () => { el.dispatchEvent(new MouseEvent('click', { bubbles: true })); });
        await flush();
    };

    test('Save notes asks the agent for them, as a turn, before any draft exists', async () => {
        const calls = await open({});
        expect(container.textContent).not.toContain('draft');
        await click(button('Save notes'));
        expect(calls).toEqual([{ url: `${at}/prompt`, method: 'POST', body: JSON.stringify({ text: 'Save notes.' }) }]);
    });

    test('a session that is no research conversation has no Save notes', async () => {
        const plan = { sandbox: 'fix-granule-42', task: 'recipe-plan-1' };
        global.fetch = jest.fn(() => reply(200, { sessionId: plan.task, repo: 'granule' }));
        await act(async () => { root.render(<ResearchConversation task={plan} />); });
        await flush();
        await act(async () => {
            FakeSocket.instances[0].deliver({ type: 'open', session: { busy: false, task: plan.task, offset: 0 } });
        });
        expect(button('Save notes')).toBeUndefined();
    });

    test('the draft is shown, and its actions are its own buttons', async () => {
        window.confirm = jest.fn(() => true);
        const calls = await open({ draft: notesDraft });
        expect(container.textContent).toContain('Notes draft');
        expect(container.textContent).toContain('backoff.md');
        // Folded to its line until asked for.
        expect(container.textContent).not.toContain('the backoff is linear');
        await click(button('Show'));
        expect(container.textContent).toContain('the backoff is linear');

        await click(button('Save to research/notes'));
        await click(button('Edit'));
        const box = container.querySelector('textarea[aria-label="Edit the draft"]');
        expect(box.value).toBe(notesDraft.markdown);
        await act(async () => {
            nativeSet(box, '# Edited');
            box.dispatchEvent(new Event('input', { bubbles: true }));
        });
        await click(button('Store edit'));
        await click(button('Discard'));

        expect(calls).toEqual([
            { url: `${at}/draft/push-notes`, method: 'POST', body: JSON.stringify({ run: '', text: '' }) },
            { url: `${at}/draft/edit`, method: 'POST', body: JSON.stringify({ run: '', text: '# Edited' }) },
            { url: `${at}/draft/reject`, method: 'POST', body: JSON.stringify({ run: '', text: '' }) },
        ]);
    });

    test('a plan session gets the same panel, with the plan\'s actions', async () => {
        await open({
            draft: { kind: 'Plan', markdown: '## Summary', actions: [{ verb: 'comment', label: 'Post plan', enabled: true }] },
        });
        expect(container.textContent).toContain('Plan draft');
        expect(button('Update plan')).toBeUndefined();
        expect(button('Post plan')).toBeTruthy();
    });

    test('a review session links its pending review on GitHub', async () => {
        await open({ pendingReview: 'https://github.com/o/r/pull/42/files' });
        const link = [...container.querySelectorAll('a')].find(a => a.textContent.includes('finalize or discard it there'));
        expect(link.getAttribute('href')).toBe('https://github.com/o/r/pull/42/files');
        expect(container.textContent).toContain('Your review is pending on GitHub');
    });

    test('no pending review, no link', async () => {
        await open({});
        expect(container.textContent).not.toContain('pending on GitHub');
    });

    test('a kind without markdown shows, and edits, its spec as YAML', async () => {
        const spec = 'title: Fix the backoff\nbranch: issue-908';
        const calls = await open({
            draft: {
                kind: 'Change', spec,
                actions: [{ verb: 'edit', enabled: true }, { verb: 'open-pr', enabled: false, reason: 'applied' }],
            },
        });
        expect(container.textContent).toContain('Change draft');
        await click(button('Show'));
        expect(container.querySelector('pre').textContent).toBe(spec);
        expect(button('Open draft PR').disabled).toBe(true);
        await click(button('Edit'));
        expect(container.querySelector('textarea[aria-label="Edit the draft"]').value).toBe(spec);
        await click(button('Store edit'));
        expect(calls).toEqual([
            { url: `${at}/draft/edit`, method: 'POST', body: JSON.stringify({ run: '', text: spec }) },
        ]);
    });

    test('what is running and what failed is said, and what waits says why', async () => {
        await open({
            recipes: [{ verb: 'recipe', recipe: 'care-ci', label: 'Fix CI', enabled: false, reason: 'running' }],
            draft: {
                ...notesDraft,
                actions: [
                    { verb: 'push-notes', label: 'Save to research/notes', enabled: false, reason: 'the run is running', error: 'push refused' },
                    { verb: 'reject', enabled: true },
                ],
            },
        });
        expect(container.textContent).toContain('Save to research/notes failed: push refused');
        expect(container.textContent).toContain('Fix CI: running in this conversation');
        expect(button('Fix CI…').disabled).toBe(true);
        const push = button('Save to research/notes');
        expect(push.disabled).toBe(true);
        expect(push.title).toBe('Not now: the run is running');
    });
});
