import React from 'react';
import { createRoot } from 'react-dom/client';
import { act as domAct } from 'react-dom/test-utils';

import { ResearchPanel, ResearchConversation, AllResearchPanel, markdownComponents } from './Research';

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

        await act(async () => { root.render(<ResearchConversation sessionId="s1" />); });
        await flush();

        expect(global.fetch).toHaveBeenCalledWith('/api/research/s1');
        expect(FakeSocket.instances).toHaveLength(1);
        // Resuming from the start: a fresh view replays the whole
        // transcript off the sandbox's disk.
        expect(FakeSocket.instances[0].url).toContain('/api/research-events/s1?offset=0');

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
        await act(async () => { root.render(<ResearchConversation sessionId="s1" />); });
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
        await act(async () => { root.render(<ResearchConversation sessionId="s1" />); });
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
        await act(async () => { root.render(<ResearchConversation sessionId="s1" />); });
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
        await act(async () => { root.render(<ResearchConversation sessionId="s1" />); });
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

    test('a pending permission is rendered with its options', async () => {
        global.fetch = jest.fn(() => reply(200, { sessionId: 's1', repo: 'repo-agent' }));
        await act(async () => { root.render(<ResearchConversation sessionId="s1" />); });
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
        expect(global.fetch).toHaveBeenCalledWith('/api/research/s1/permission', expect.objectContaining({
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
        await act(async () => { root.render(<ResearchConversation sessionId="s1" />); });
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
        await act(async () => { root.render(<ResearchConversation sessionId="s1" title="overview" />); });
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
        await act(async () => { root.render(<ResearchConversation sessionId="s1" />); });
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
        await act(async () => { root.render(<ResearchConversation sessionId="s1" />); });
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
        await act(async () => { root.render(<ResearchConversation sessionId="s1" />); });
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

    test('an opening turn still owed is said rather than left blank', async () => {
        global.fetch = jest.fn(() => reply(200, { sessionId: 's1', repo: 'repo-agent', title: 'overview', opening: true }));
        await act(async () => { root.render(<ResearchConversation sessionId="s1" />); });
        await flush();
        await act(async () => { FakeSocket.instances[0].deliver({ type: 'open', session: { busy: false, offset: 0 } }); });

        expect(container.textContent).toContain('Sending the opening question');
        expect(container.textContent).not.toContain('Nothing said yet');
    });

    test('a paused session says so and opens no socket', async () => {
        global.fetch = jest.fn(() => reply(409, { error: 'research session is paused', paused: true, sandbox: 'rsch-x' }));
        await act(async () => { root.render(<ResearchConversation sessionId="s1" />); });
        await flush();

        expect(FakeSocket.instances).toHaveLength(0);
        expect(container.textContent).toContain('paused');
        expect(container.textContent).toContain('scaled to zero');
        expect(container.querySelector('textarea').disabled).toBe(true);
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
        await act(async () => { root.render(<ResearchConversation sessionId="s1" />); });
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

        expect(global.fetch).toHaveBeenCalledWith('/api/research/s1/mode', expect.objectContaining({
            method: 'POST',
            body: JSON.stringify({ mode: 'default' }),
        }));
        expect(container.querySelector('select[aria-label="Approval mode"]').value).toBe('default');
    });

    test('a mode changed elsewhere arrives on the stream and moves the control', async () => {
        global.fetch = jest.fn(() => reply(200, {
            sessionId: 's1', repo: 'repo-agent', mode: 'yolo', availableModes: modes,
        }));
        await act(async () => { root.render(<ResearchConversation sessionId="s1" />); });
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
        await act(async () => { root.render(<ResearchConversation sessionId="s1" />); });
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
        await act(async () => { root.render(<ResearchConversation sessionId="s1" />); });
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
            await act(async () => { root.render(<ResearchConversation sessionId="s1" />); });
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
        await act(async () => { root.render(<ResearchConversation sessionId="s1" />); });
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
        await act(async () => { root.render(<ResearchConversation sessionId="s1" />); });
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
            await act(async () => { root.render(<ResearchConversation sessionId="s1" />); });
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
        await act(async () => { root.render(<ResearchConversation sessionId="s1" />); });
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
        await act(async () => { root.render(<ResearchConversation sessionId="s1" />); });
        await flush();
        await act(async () => {
            FakeSocket.instances[0].deliver({ type: 'open', session: { busy: false, offset: 0 } });
        });

        expect(container.querySelector('select[aria-label="Approval mode"]')).toBeNull();
    });

    test('a 404 is "gone" for an existing session and "starting" for a just-claimed one', async () => {
        global.fetch = jest.fn(() => reply(404, { error: 'research session not found' }));

        await act(async () => { root.render(<ResearchConversation sessionId="s1" />); });
        await flush();
        expect(container.textContent).toContain('no longer exists');

        // The same 404 seconds after filing a mailbox claim means the
        // controller has not built the sandbox yet, not that it is gone.
        await act(async () => { root.render(<ResearchConversation sessionId="s2" pending />); });
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

        await act(async () => { root.render(<ResearchConversation sessionId="s1" />); });
        await flush();

        const link = [...container.querySelectorAll('a')].find(a => a.textContent === 'open-rl');
        expect(link).toBeTruthy();
        expect(link.getAttribute('href')).toBe('https://github.com/gke-labs/open-rl');

        // A sandbox annotated with no URL still says which repo it is;
        // it just does not pretend to be a link to nowhere.
        global.fetch = jest.fn(() => reply(200, status));
        await act(async () => { root.render(<ResearchConversation sessionId="s3" />); });
        await flush();
        expect(container.textContent).toContain('open-rl');
        expect([...container.querySelectorAll('a')].map(a => a.textContent)).not.toContain('open-rl');
    });

    // Saving notes: the conversation's one way of outliving the disk it
    // is written on. One click, no answers — what to write is the
    // conversation and where it goes is the session, and the server
    // knows both. The click asks the agent to write the note; the push
    // happens minutes later, in the controller, which is why everything
    // this pane shows about it comes back off the probe.

    // liveSession gets a conversation to the point where the save
    // button is live, and collects what the button posts.
    const liveSession = async ({ status = {}, posts = [] } = {}) => {
        global.fetch = jest.fn((url, opts) => {
            if (url === '/api/research/s1/capture') {
                posts.push({ body: opts && opts.body, method: opts && opts.method });
                return reply(202, { sessionId: 's1', note: 'notes.md', path: 'x', offset: 10 });
            }
            return reply(200, { sessionId: 's1', repo: 'repo-agent', sandbox: 'rsch-1', namespace: 'ns', ...status });
        });
        await act(async () => { root.render(<ResearchConversation sessionId="s1" />); });
        await flush();
        await act(async () => {
            FakeSocket.instances[0].deliver({ type: 'open', session: { busy: !!status.busyTurn, offset: 0 } });
        });
        return posts;
    };

    const saveButton = () => container.querySelector('[aria-label="Save notes"]');

    test('saving takes one click and sends nothing to fill in', async () => {
        const posts = await liveSession();

        await act(async () => { saveButton().dispatchEvent(new MouseEvent('click', { bubbles: true })); });
        await flush();

        // No body at all. Every answer the old form asked for — which
        // part, which file — is derivable at the other end, and a
        // second answer here is a second way for two captures to
        // disagree about the file they are both writing.
        expect(posts).toEqual([{ method: 'POST', body: undefined }]);
        // And nothing opened: what happens next takes minutes and none
        // of it happens here.
        expect(container.querySelector('textarea[aria-label="What to capture"]')).toBeNull();
    });

    test('the save control is on the bar, not behind the ⋯ menu', async () => {
        // Saving is something you do *while* reading, at the moment the
        // answer lands. The menu is for what you do to a session once,
        // and it is closed while you read.
        await liveSession();
        expect(saveButton()).toBeTruthy();

        const more = container.querySelector('[aria-label="More actions"]');
        await act(async () => { more.dispatchEvent(new MouseEvent('click', { bubbles: true })); });
        const items = [...container.querySelectorAll('[role="menuitem"]')].map(b => b.textContent);
        expect(items.join(' ')).not.toContain('Save notes');
    });

    test('a capture will not go while the agent is working, and says so', async () => {
        // A capture is a prompt, and acpd refuses a second one mid-turn.
        await liveSession({ status: { busyTurn: true } });

        expect(saveButton().disabled).toBe(true);
        expect(saveButton().title).toContain('The agent is working');
    });

    test('an owed save is read off the sandbox, not remembered here', async () => {
        // Which is what makes it survive a reload, show up in the second
        // tab, and outlive the API replica that took the click.
        global.fetch = jest.fn(() => reply(200, {
            sessionId: 's1', repo: 'repo-agent', capturing: 'notes.md',
        }));
        await act(async () => { root.render(<ResearchConversation sessionId="s1" />); });
        await flush();

        expect(container.textContent).toContain('Writing this conversation up');
        expect(container.textContent).toContain('pushed to your fork when this turn finishes');
    });

    test('a push that failed says so until another one is asked for', async () => {
        // Not dismissible: it is written on the sandbox, and a banner
        // the member could wave away would leave the session looking
        // like it had saved.
        global.fetch = jest.fn(() => reply(200, {
            sessionId: 's1', repo: 'repo-agent', captureError: 'pushing the notes: no notes were written',
        }));
        await act(async () => { root.render(<ResearchConversation sessionId="s1" />); });
        await flush();

        const banner = container.querySelector('.warning-banner');
        expect(banner.textContent).toContain('no notes were written');
        expect(banner.textContent).toContain('Ask for it again to retry');
    });

});

describe('ResearchPanel', () => {
    const sessions = {
        sessions: [
            { sessionId: 'aaaaaaaa-1111', title: 'the retry loop', sandbox: 'rsch-repo-agent-1', repo: 'repo-agent', createdAt: '2026-09-26T10:00:00Z', paused: false },
            { sessionId: 'bbbbbbbb-2222', title: 'rollout flags', sandbox: 'rsch-kubectl-2', repo: 'kubectl', createdAt: '2026-09-26T09:00:00Z', paused: true },
        ],
    };

    test('lists this board\'s sessions and nobody else\'s', async () => {
        global.fetch = jest.fn(() => reply(200, sessions));

        await act(async () => {
            root.render(<ResearchPanel boardName="repo-agent" repoURL="https://github.com/gke-labs/repo-agent" />);
        });
        await flush();

        expect(global.fetch).toHaveBeenCalledWith('/api/research');

        // The title first and with the width, then the caption: repo,
        // the short id, how old it is, what it is doing. One row across
        // the tab, which is what the 260px rail could not hold.
        const row = [...container.querySelectorAll('button')]
            .find(b => b.textContent.startsWith('the retry loop'));
        expect(row).toBeTruthy();
        expect(row.textContent).toContain('repo-agent');
        expect(row.textContent).toContain('aaaaaaaa');
        // A list of rows, not a table. No columns to line up and no
        // header to head them: on every row but the title, the cell is
        // either the same word all the way down or a caption.
        expect(container.querySelector('table')).toBeNull();
        // Age is relative to now, so it is checked for being there
        // rather than for what it says.
        expect(row.textContent).toContain('up');
        expect(row.querySelector('[title="2026-09-26T10:00:00Z"]')).toBeTruthy();

        // The sandbox is still the hover, being the one id you only
        // want when you are going to go and look at the pod.
        expect(row.textContent).not.toContain('rsch-repo-agent-1');
        expect(row.title).toContain('session aaaaaaaa-1111');
        expect(row.title).toContain('sandbox rsch-repo-agent-1');

        // A session for another repository is not this board's business
        // — not as a row, and not as a count in a disclosure either.
        expect(container.textContent).not.toContain('rollout flags');
        expect(container.textContent).not.toContain('kubectl');
        expect(container.textContent).not.toContain('other repositories');
    });

    // selectRow clicks a row by the title it starts with.
    const selectRow = async (title) => {
        const row = [...container.querySelectorAll('button')]
            .find(b => b.textContent.startsWith(title));
        expect(row).toBeTruthy();
        await act(async () => { row.dispatchEvent(new MouseEvent('click', { bubbles: true })); });
        await flush();
        return row;
    };

    // rowFor is the row whose first line is this title.
    const rowFor = (title) => {
        const row = [...container.querySelectorAll('button')]
            .find(b => b.textContent.startsWith(title));
        expect(row).toBeTruthy();
        return row;
    };

    const liveList = async (rows) => {
        global.fetch = jest.fn(() => reply(200, {
            sessions: rows.map((r, i) => ({
                sessionId: `live-${i}`, repo: 'repo-agent',
                createdAt: '2026-09-26T10:00:00Z', ...r,
            })),
        }));
        await act(async () => {
            root.render(<ResearchPanel boardName="repo-agent" repoURL="https://github.com/gke-labs/repo-agent" />);
        });
        await flush();
    };

    // The list exists to tell you which conversation to open. "up" answers
    // a question about the pod, which is not one anybody asked.
    test('a row says what its agent is doing, not that its pod is running', async () => {
        await liveList([
            { title: 'thinking one', live: true, busy: true },
            { title: 'blocked one', live: true, busy: true, waiting: true },
            { title: 'quiet one', live: true },
            { title: 'cold one' },
            { title: 'silent one', unreachable: 'dial tcp 10.0.0.1:49984: connection refused' },
        ]);

        expect(rowFor('thinking one').textContent).toContain('working…');
        expect(rowFor('quiet one').textContent).toContain('idle');
        // No engine in the pod yet: nothing is wrong, and the conversation
        // starts when you open it.
        expect(rowFor('cold one').textContent).toContain('up');

        // The one that has stopped to ask something is the one worth
        // finding, and it is busy too — so busy must not be what it says.
        const blocked = rowFor('blocked one');
        expect(blocked.textContent).toContain('needs you');
        expect(blocked.textContent).not.toContain('working');

        // A pod we could not ask is not a quiet one. The reason is a
        // hover away rather than on the row, which is one line.
        const silent = rowFor('silent one');
        expect(silent.textContent).toContain('no answer');
        expect(silent.textContent).not.toContain('idle');
        expect(silent.textContent).not.toContain('connection refused');
        expect(silent.querySelector('[title*="connection refused"]')).toBeTruthy();
    });

    // A canned first turn that stopped to ask something is still an
    // unanswered question with your name on it. "opening…" reads as
    // "wait", which is how a session sits there until the permission
    // timeout takes its turn away.
    test('a first turn that stopped to ask something says so, not that it is opening', async () => {
        await liveList([{ title: 'the overview', opening: true, live: true, busy: true, waiting: true }]);

        const row = rowFor('the overview');
        expect(row.textContent).toContain('needs you');
        expect(row.textContent).not.toContain('opening');
    });

    // Live state is the last thing the row is allowed to say. A sandbox
    // with no pod cannot be busy, and a stale flag on a paused row would
    // send someone to a conversation with no engine in it.
    test('a paused session is paused, whatever else the row is carrying', async () => {
        await liveList([{ title: 'the old one', paused: true, busy: true, waiting: true }]);

        const row = rowFor('the old one');
        expect(row.textContent).toContain('paused');
        expect(row.textContent).not.toContain('needs you');
    });

    test('the row is the selection, and the list it came from stays put', async () => {
        global.fetch = jest.fn(() => reply(404, { error: 'not found' }));
        global.fetch.mockImplementationOnce(() => reply(200, sessions));

        await act(async () => {
            root.render(<ResearchPanel boardName="repo-agent" repoURL="https://github.com/gke-labs/repo-agent" />);
        });
        await flush();

        expect([...container.querySelectorAll('button')].map(b => b.textContent)).not.toContain('Open');

        const row = await selectRow('the retry loop');

        // The conversation fills the right-hand pane; the row that
        // opened it is still on the left, and now says it is the one
        // being shown.
        expect(container.textContent).toContain('no longer exists');
        expect(row.isConnected).toBe(true);
        expect(row.getAttribute('aria-current')).toBe('true');
    });

    test('the ask box is the top of the list, and nothing has to be clicked to get back to it', async () => {
        // `+ New conversation` was a rail row standing in for a box that
        // was one pane away. The box is at the top of the list now and
        // never leaves it, so there is nothing to go back to.
        global.fetch = jest.fn(() => reply(404, { error: 'not found' }));
        global.fetch.mockImplementationOnce(() => reply(200, sessions));

        await act(async () => {
            root.render(<ResearchPanel boardName="repo-agent" repoURL="https://github.com/gke-labs/repo-agent" />);
        });
        await flush();

        expect([...container.querySelectorAll('button')].map(b => b.textContent))
            .not.toContain('+ New conversation');
        const askBox = container.querySelector('textarea[placeholder^="Ask anything about repo-agent"]');
        expect(askBox).toBeTruthy();

        // Opening a conversation covers the list rather than replacing
        // it: the box is still mounted underneath, draft and all.
        await selectRow('the retry loop');
        expect(askBox.isConnected).toBe(true);

        // And Escape uncovers it.
        await act(async () => {
            window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
        });
        await flush();
        expect(container.querySelector('textarea[placeholder^="Ask anything about repo-agent"]')).toBeTruthy();
        expect(container.querySelector('[aria-label="Close this conversation"]')).toBeNull();
    });

    test('a half-typed question does not follow you into the next conversation', async () => {
        // The conversation resets what it knows is per-session, but the
        // composer is not on that list, and a draft carried across and
        // sent into the wrong session is the failure that matters.
        const seen = [];
        // Both on this board: the list only holds this repository's.
        const two = {
            sessions: [
                sessions.sessions[0],
                {
                    sessionId: 'cccccccc-3333', title: 'the lease', sandbox: 'rsch-repo-agent-3',
                    repo: 'repo-agent', createdAt: '2026-09-26T08:00:00Z',
                },
            ],
        };
        global.fetch = jest.fn((url) => {
            if (url === '/api/research') return reply(200, two);
            const m = /^\/api\/research\/([^/?]+)$/.exec(url);
            if (m) {
                seen.push(m[1]);
                return reply(200, { sessionId: m[1], sandbox: 'rsch-x', repo: 'repo-agent', live: true });
            }
            return reply(404, { error: 'not found' });
        });

        await act(async () => {
            root.render(<ResearchPanel boardName="repo-agent" repoURL="https://github.com/gke-labs/repo-agent" />);
        });
        await flush();

        const goLive = async (id) => {
            const ws = FakeSocket.instances[FakeSocket.instances.length - 1];
            await act(async () => { ws.deliver({ type: 'open', session: { id, busy: false, offset: 0 } }); });
            await flush();
        };

        await selectRow('the retry loop');
        expect(seen).toContain('aaaaaaaa-1111');
        await goLive('aaaaaaaa-1111');

        // By its own label: the ask box is mounted too, under the
        // conversation, and is the first textarea in the document.
        const composerBox = () => container.querySelector('textarea[aria-label="Continue the research"]');
        const composer = composerBox();
        await act(async () => {
            nativeSet(composer, 'half a question about retries');
            composer.dispatchEvent(new Event('input', { bubbles: true }));
        });
        expect(composerBox().value).toBe('half a question about retries');

        // Escape back to the list and into the other one, which is the
        // switch the rail used to do in one click.
        await act(async () => {
            window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
        });
        await flush();
        await selectRow('the lease');
        expect(seen).toContain('cccccccc-3333');
        await goLive('cccccccc-3333');

        expect(composerBox().value).toBe('');
    });

    test('clicking a row opens the conversation full screen, and says how to leave', async () => {
        // Full screen is not somewhere the conversation is expanded to
        // any more — it is how a conversation opens. The row is the
        // collapsed state, so there is nothing to expand from.
        global.fetch = jest.fn((url) => {
            if (url === '/api/research') return reply(200, sessions);
            if (/^\/api\/research\/[^/?]+$/.test(url)) {
                return reply(200, { sessionId: 'aaaaaaaa-1111', sandbox: 'rsch-x', repo: 'repo-agent', live: true });
            }
            return reply(404, { error: 'not found' });
        });
        await act(async () => {
            root.render(<ResearchPanel boardName="repo-agent" repoURL="https://github.com/gke-labs/repo-agent" />);
        });
        await flush();

        // Nothing offers to expand, because nothing is small.
        expect(container.querySelector('[aria-label="Full screen"]')).toBeNull();

        await selectRow('the retry loop');
        await act(async () => {
            const ws = FakeSocket.instances[FakeSocket.instances.length - 1];
            ws.deliver({ type: 'open', session: { id: 'aaaaaaaa-1111', busy: false, offset: 0 } });
        });
        await flush();

        const tab = container.querySelector('[aria-label="Open in a new tab"]');
        expect(tab.getAttribute('href')).toBe('#/research/aaaaaaaa-1111');
        expect(tab.getAttribute('target')).toBe('_blank');

        expect(container.textContent).toContain('the retry loop');
        expect(document.body.style.overflow).toBe('hidden');

        // Both hints: the button names the key, and the key hints on the
        // status line say it in the voice they say ⏎ and ⇧⏎ in. A pane
        // that covers the window should not hide its way out in a title
        // attribute nobody hovers to read.
        const close = container.querySelector('[aria-label="Close this conversation"]');
        expect(close.textContent).toContain('esc');
        expect(container.querySelector('.term-statusbar').textContent).toContain('esc close');

        // Escape goes back to the list, and gives the page its
        // scrollbar with it.
        await act(async () => {
            window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
        });
        await flush();
        expect(container.querySelector('[aria-label="Close this conversation"]')).toBeNull();
        expect(document.body.style.overflow).not.toBe('hidden');
        expect(rowFor('the retry loop')).toBeTruthy();
    });

    test('the button does what the key does', async () => {
        global.fetch = jest.fn(() => reply(200, sessions));
        await act(async () => {
            root.render(<ResearchPanel boardName="repo-agent" repoURL="https://github.com/gke-labs/repo-agent" />);
        });
        await flush();
        await selectRow('the retry loop');

        const close = container.querySelector('[aria-label="Close this conversation"]');
        await act(async () => { close.dispatchEvent(new MouseEvent('click', { bubbles: true })); });
        await flush();

        expect(container.querySelector('[aria-label="Close this conversation"]')).toBeNull();
        expect(document.body.style.overflow).not.toBe('hidden');
    });

    test('Escape that something else already answered does not also close the conversation', async () => {
        // The rename box cancels an edit with Escape and calls
        // preventDefault. Typing a name and thinking better of it should
        // not also throw away the screen you were reading it on.
        global.fetch = jest.fn(() => reply(200, sessions));
        await act(async () => {
            root.render(<ResearchPanel boardName="repo-agent" repoURL="https://github.com/gke-labs/repo-agent" />);
        });
        await flush();
        await selectRow('the retry loop');

        const title = [...container.querySelectorAll('strong')].find(s => s.textContent === 'the retry loop');
        await act(async () => { title.dispatchEvent(new MouseEvent('click', { bubbles: true })); });
        const box = container.querySelector('[aria-label="Session title"]');
        await act(async () => {
            box.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
        });

        // The edit is off, and the conversation is still open.
        expect(container.querySelector('[aria-label="Close this conversation"]')).toBeTruthy();
    });

    test('the popped-out window does not offer to pop itself out again', async () => {
        global.fetch = jest.fn(() => reply(200, { sessionId: 's1', repo: 'repo-agent' }));
        await act(async () => { root.render(<ResearchConversation sessionId="s1" standalone />); });
        await flush();

        expect(container.querySelector('[aria-label="Open in a new tab"]')).toBeNull();
        expect(container.querySelector('[aria-label="Full screen"]')).toBeNull();
    });

    test('double-clicking a row renames it, in the box the header already had', async () => {
        // One rename and one PATCH: the row sends you to the header's
        // box rather than growing an editor of its own. What the gesture
        // buys is discoverability — bold text gives no sign it is
        // clickable, and a row does.
        global.fetch = jest.fn(() => reply(200, sessions));
        await act(async () => {
            root.render(<ResearchPanel boardName="repo-agent" repoURL="https://github.com/gke-labs/repo-agent" />);
        });
        await flush();

        const row = [...container.querySelectorAll('button')]
            .find(b => b.textContent.startsWith('the retry loop'));
        await act(async () => { row.dispatchEvent(new MouseEvent('dblclick', { bubbles: true })); });
        await flush();

        const field = container.querySelector('input[aria-label="Session title"]');
        expect(field).toBeTruthy();
        expect(field.value).toBe('the retry loop');
        // autoFocus is a mount-time prop and would not have covered the
        // unnamed case, where the box is already on screen.
        expect(document.activeElement).toBe(field);
        expect(row.title).toContain('double-click to rename');
    });

    test('a board with only other repositories\' sessions is an empty list', async () => {
        // Not "no sessions found" hedged with a disclosure holding four
        // of somebody else's: an empty list is the truthful answer to
        // "what has been asked about this repository", and the invitation
        // to ask something is what should be filling the pane.
        global.fetch = jest.fn(() => reply(200, sessions));

        await act(async () => {
            root.render(<ResearchPanel boardName="open-rl" repoURL="https://github.com/gke-labs/open-rl" />);
        });
        await flush();

        expect(container.textContent).toContain('No conversations for open-rl yet');
        expect(container.textContent).not.toContain('the retry loop');
        expect(container.textContent).not.toContain('rollout flags');
    });

    // claimFetch answers the list and the claim; everything else 404s,
    // which is what a session whose sandbox does not exist yet looks
    // like to the conversation's probe.
    // The canned openings, as the server renders them for the board's
    // repository. Short stand-ins for the real templates — what matters
    // to the pane is that the text arrives and that its first line is
    // what the session will be called.
    const cannedPrompts = {
        onboard: 'Overview of the repo\n\nrepo-agent is checked out in front of you.\n\nCode map: directory by directory.',
        activity: 'Changes in the last month\n\nWhat happened in repo-agent over that window?',
    };

    const claimFetch = (claimed) => jest.fn((url, opts) => {
        if (url === '/api/research') return reply(200, { sessions: [] });
        if (url === '/api/board/repo-agent/research/prompts') return reply(200, { prompts: cannedPrompts });
        if (url === '/api/board/repo-agent/research') {
            claimed.push(JSON.parse((opts && opts.body) || '{}'));
            return reply(202, { sessionId: 'new-session', title: 'overview' });
        }
        return reply(404, { error: 'research session not found' });
    });

    const clickButton = async (label) => {
        const button = [...container.querySelectorAll('button')].find(b => b.textContent === label);
        expect(button).toBeTruthy();
        await act(async () => { button.click(); });
        await flush();
    };

    const renderPanel = async () => {
        await act(async () => {
            root.render(<ResearchPanel boardName="repo-agent" repoURL="https://github.com/gke-labs/repo-agent" />);
        });
        await flush();
    };

    test('an empty list says so, and offers no way to start an empty session', async () => {
        // Every session now starts with something said to the agent.
        // The ask box was already the empty conversation with a first
        // message in it, and a second control for the same gesture,
        // three inches under a box that was already empty, read as the
        // same button twice.
        global.fetch = claimFetch([]);

        await renderPanel();
        expect(container.textContent).toContain('No conversations for repo-agent yet');
        expect(container.textContent).not.toContain('Empty conversation');
    });

    test('overview fills the box with the prompt instead of starting a session', async () => {
        // The canned read stopped being a button that spends minutes of
        // sandbox on a prompt nobody was shown. It puts that prompt in
        // the ask box, where it can be read, narrowed, or sent as it
        // stands — and until it is sent, nothing has been claimed.
        const claimed = [];
        global.fetch = claimFetch(claimed);

        await renderPanel();
        await clickButton('overview');

        const box = container.querySelector('textarea');
        expect(box.value).toContain('Overview of the repo');
        expect(box.value).toContain('Code map');
        expect(claimed).toEqual([]);
    });

    test('the filled prompt is sent as an ordinary question', async () => {
        // No kind on the wire: what runs is what is in the box, edits
        // and all, and the session is named from its first line — which
        // is why the prompt opens with one.
        const claimed = [];
        global.fetch = claimFetch(claimed);

        await renderPanel();
        await clickButton('overview');
        await clickButton('Research');

        expect(claimed).toHaveLength(1);
        expect(claimed[0].kind).toBe('topic');
        expect(claimed[0].topic).toContain('Overview of the repo');
        expect(claimed[0].title).toBeUndefined();
    });

    test('the box says Enter sends it, and how to get a newline instead', async () => {
        // Enter here spends a sandbox and minutes of wall clock. The
        // conversation's own composer has said `⏎ send · ⇧⏎ newline` on
        // its status line since it became a terminal; the box that
        // starts the conversation said nothing at all, so the way you
        // found out was by losing a half-written question to it.
        global.fetch = claimFetch([]);

        await renderPanel();
        expect(container.textContent).toContain('⏎ research');
        expect(container.textContent).toContain('⇧⏎ newline');
    });

    test('Enter asks, and Shift+Enter does not', async () => {
        const claimed = [];
        global.fetch = claimFetch(claimed);

        await renderPanel();
        const box = container.querySelector('textarea[aria-label="Research question"]');
        await act(async () => {
            nativeSet(box, 'where does the retry loop live?');
            box.dispatchEvent(new Event('input', { bubbles: true }));
        });

        await act(async () => {
            box.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', shiftKey: true, bubbles: true, cancelable: true }));
        });
        await flush();
        expect(claimed).toEqual([]);

        await act(async () => {
            box.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }));
        });
        await flush();
        expect(claimed).toHaveLength(1);
        expect(claimed[0].topic).toContain('retry loop');
    });

    test('overview waits for the prompt it would fill the box with', async () => {
        // Nothing to put in the box until the prompts land, and a
        // control that silently does nothing is worse than one that
        // says it is not ready yet.
        global.fetch = jest.fn((url) => {
            if (url === '/api/research') return reply(200, { sessions: [] });
            return reply(500, { error: 'no prompts today' });
        });

        await renderPanel();
        const overview = [...container.querySelectorAll('button')].find(b => b.textContent === 'overview');
        expect(overview.disabled).toBe(true);
    });

    test('recent changes fills the box, window and all', async () => {
        // The window used to be a dropdown of three fixed spans. It is
        // the first line of the prompt now, in the box, where it can be
        // changed to a week — or to something no dropdown could have
        // offered, like "since the 1.4 release".
        const claimed = [];
        global.fetch = claimFetch(claimed);

        await renderPanel();
        await clickButton('recent changes');

        const box = container.querySelector('textarea');
        expect(box.value).toContain('Changes in the last month');
        expect(claimed).toEqual([]);
    });

    test('a window edited in the box is the window that runs', async () => {
        // The whole argument for filling the box rather than running the
        // prompt behind a button: what is sent is what is on screen,
        // including the edit — and the session is named after it,
        // because the window lives in the line the name comes from.
        const claimed = [];
        global.fetch = claimFetch(claimed);

        await renderPanel();
        await clickButton('recent changes');
        const box = container.querySelector('textarea');
        await act(async () => {
            nativeSet(box, box.value.replace('last month', 'last week'));
            box.dispatchEvent(new Event('input', { bubbles: true }));
        });
        await clickButton('Research');

        expect(claimed).toHaveLength(1);
        expect(claimed[0].topic).toContain('Changes in the last week');
        expect(claimed[0].topic).not.toContain('last month');
    });

    test('a typed question is the whole of it, and opens the conversation', async () => {
        // No name goes with it. The box that asked for one was optional
        // and was left empty every time, which is the right answer —
        // the server names the session after the question — so what it
        // actually was is a second control between a typed question and
        // sending it.
        const claimed = [];
        global.fetch = claimFetch(claimed);

        await renderPanel();
        expect(container.querySelector('input[aria-label="Session name"]')).toBeNull();
        const box = container.querySelector('textarea');
        await act(async () => {
            nativeSet(box, 'where does the retry loop live?');
            box.dispatchEvent(new Event('input', { bubbles: true }));
        });
        await clickButton('Research');

        expect(claimed).toEqual([{ kind: 'topic', topic: 'where does the retry loop live?' }]);
        await flush();
        expect(container.textContent).toContain('Preparing the sandbox');
    });

    test('a requested session is listed by name, with no sandbox yet', async () => {
        global.fetch = jest.fn(() => reply(200, {
            sessions: [{
                sessionId: 'cccccccc-3333', repo: 'repo-agent', requested: true,
                title: 'Changes in the last month', createdAt: '2026-09-26T10:00:00Z',
            }],
        }));

        await renderPanel();
        expect(container.textContent).toContain('Changes in the last month');
        expect(container.textContent).toContain('requested');
    });

    test('an opening that never landed says so on the row', async () => {
        global.fetch = jest.fn(() => reply(200, {
            sessions: [{
                sessionId: 'dddddddd-4444', sandbox: 'rsch-repo-agent-4', repo: 'repo-agent',
                title: 'overview', openingError: 'no running pod yet', createdAt: '2026-09-26T10:00:00Z',
            }],
        }));

        await renderPanel();
        expect(container.textContent).toContain('opening failed');
    });

    test('a sandbox whose pod is not up yet is starting, not up', async () => {
        global.fetch = jest.fn(() => reply(200, {
            sessions: [{
                sessionId: 'eeeeeeee-5555', sandbox: 'rsch-repo-agent-5', repo: 'repo-agent',
                title: 'overview', starting: true, opening: true, createdAt: '2026-09-26T10:00:00Z',
            }],
        }));

        await renderPanel();
        // Starting wins over opening: the prompt cannot land on a pod
        // that does not exist, and the wait is the thing being reported.
        expect(container.textContent).toContain('starting…');
        expect(container.textContent).not.toContain('opening…');
    });

    test('the notes branch is a footnote link to the member\'s fork', async () => {
        global.fetch = jest.fn(() => reply(200, {
            ...sessions, forkOwner: 'barney-s', notesBranch: 'research/notes',
        }));

        await renderPanel();
        const link = [...container.querySelectorAll('a')]
            .find(a => a.textContent.includes('research/notes'));
        expect(link).toBeTruthy();
        expect(link.getAttribute('href'))
            .toBe('https://github.com/barney-s/repo-agent/tree/research/notes');
    });

    // The branch is whatever the server says it is. Writing the name
    // into the page instead would put a second copy of it here, free to
    // drift from the Go constant the write path pushes to — and a
    // footnote pointing at a branch nothing writes is a 404 that looks
    // like the notes were lost.
    test('the link follows the branch the server named', async () => {
        global.fetch = jest.fn(() => reply(200, {
            ...sessions, forkOwner: 'barney-s', notesBranch: 'somewhere/else',
        }));

        await renderPanel();
        const link = [...container.querySelectorAll('a')]
            .find(a => a.textContent.includes('somewhere/else'));
        expect(link).toBeTruthy();
        expect(link.getAttribute('href'))
            .toBe('https://github.com/barney-s/repo-agent/tree/somewhere/else');
    });

    test('with no fork owner there is no footnote to click', async () => {
        global.fetch = jest.fn(() => reply(200, sessions));

        await renderPanel();
        expect(container.textContent).not.toContain('research/notes');
    });
});

describe('AllResearchPanel', () => {
    const renderAll = async (body) => {
        global.fetch = jest.fn(() => reply(200, body));
        await act(async () => { root.render(<AllResearchPanel />); });
        await flush();
    };

    const rowFor = (title) => [...container.querySelectorAll('button')]
        .find(b => b.textContent.startsWith(title));

    // The whole point of the tab: a board's own list is about that
    // repository, so a conversation you remember having is only
    // findable if you also remember which repo it was about.
    test('lists every conversation, whichever board it came from', async () => {
        await renderAll({
            sessions: [
                { sessionId: 'aaaaaaaa-1111', title: 'the retry loop', repo: 'repo-agent', createdAt: '2026-09-26T10:00:00Z' },
                { sessionId: 'bbbbbbbb-2222', title: 'rollout flags', repo: 'kubectl', createdAt: '2026-09-26T09:00:00Z' },
                // No board for this one any more. It was reachable only
                // by its own link before this tab existed.
                { sessionId: 'cccccccc-3333', title: 'orphaned read', repo: 'deleted-board', createdAt: '2026-09-25T09:00:00Z' },
            ],
        });

        expect(global.fetch).toHaveBeenCalledWith('/api/research');
        for (const [title, repo] of [
            ['the retry loop', 'repo-agent'],
            ['rollout flags', 'kubectl'],
            ['orphaned read', 'deleted-board'],
        ]) {
            const row = rowFor(title);
            expect(row).toBeTruthy();
            // The repo is the caption that tells two rows apart here —
            // on a board's tab it is the same word all the way down.
            expect(row.textContent).toContain(repo);
        }
    });

    // Starting one means choosing a repository, and a repository is a
    // board. This tab is for finding a conversation, not opening a
    // second front door to making one.
    test('there is no ask box on it', async () => {
        await renderAll({ sessions: [] });

        expect(container.querySelector('textarea')).toBeNull();
        expect(container.textContent).toContain('No conversations anywhere yet');
        expect(container.textContent).toContain("board's Research tab");
    });

    // A conversation stopped on a permission request is a question with
    // your name on it, and the turn is cancelled if nobody answers. It
    // outranks recency; nothing else does.
    test('a conversation that needs you comes first', async () => {
        await renderAll({
            sessions: [
                { sessionId: 'a', title: 'newest', repo: 'repo-agent', createdAt: '2026-09-26T10:00:00Z', live: true },
                { sessionId: 'b', title: 'older', repo: 'kubectl', createdAt: '2026-09-26T09:00:00Z', live: true },
                { sessionId: 'c', title: 'blocked one', repo: 'kubectl', createdAt: '2026-09-25T09:00:00Z', live: true, busy: true, waiting: true },
            ],
        });

        // The rows are the only buttons on the tab: no ask box, no
        // filters, nothing but conversations.
        const rows = [...container.querySelectorAll('button')];
        expect(rows).toHaveLength(3);
        expect(rows[0].textContent.startsWith('blocked one')).toBe(true);
        expect(rows[0].textContent).toContain('needs you');
        // Server order — newest first — for everything else.
        expect(rows[1].textContent.startsWith('newest')).toBe(true);
        expect(rows[2].textContent.startsWith('older')).toBe(true);
    });

    // Opening one is the reason to have found it, and it opens the same
    // way it does from a board: over the whole window.
    test('a row opens the conversation', async () => {
        global.fetch = jest.fn((url) => {
            if (url === '/api/research') {
                return reply(200, {
                    sessions: [{ sessionId: 'aaaaaaaa-1111', title: 'the retry loop', repo: 'repo-agent', createdAt: '2026-09-26T10:00:00Z' }],
                });
            }
            // The conversation's own probe, for the session just clicked.
            return reply(200, {
                sessionId: 'aaaaaaaa-1111', sandbox: 'rsch-repo-abcd1234',
                namespace: 'ns', repo: 'repo-agent', live: false,
            });
        });
        await act(async () => { root.render(<AllResearchPanel />); });
        await flush();

        const row = rowFor('the retry loop');
        await act(async () => { row.dispatchEvent(new MouseEvent('click', { bubbles: true })); });
        await flush();

        expect(global.fetch).toHaveBeenCalledWith('/api/research/aaaaaaaa-1111');
        expect(row.getAttribute('aria-current')).toBe('true');
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
            root.render(<ResearchConversation sessionId={task.task} task={task} title="plan · fix-granule-42" />);
        });
        await flush();
    };

    test('is reached at the task session routes, with no rename, notes or delete', async () => {
        await render({ sessionId: task.task, sandbox: task.sandbox, namespace: 'ns', repo: 'granule' });
        expect(global.fetch).toHaveBeenCalledWith('/api/task-sessions/fix-granule-42/recipe-plan-20261004-120000-ab12');
        expect(FakeSocket.instances[0].url)
            .toContain('/api/task-session-events/fix-granule-42/recipe-plan-20261004-120000-ab12?offset=0');
        expect(container.textContent).toContain('plan · fix-granule-42');
        expect(container.querySelector('input[aria-label="Session title"]')).toBeNull();
        expect(container.querySelector('[aria-label="Save notes"]')).toBeNull();
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

    test('the draft\'s revises are buttons that file the revise on its board row, and follow it', async () => {
        const offered = { verb: 'revise', revise: 'plan', label: 'Update plan', enabled: true };
        await render({ sessionId: task.task, repo: 'granule', board: 'granule', number: 42, revises: [offered] });
        await act(async () => {
            FakeSocket.instances[0].deliver({ type: 'open', session: { busy: false, task: task.task, offset: 0 } });
        });
        const button = () => [...container.querySelectorAll('button')].find(b => b.textContent.startsWith('Update plan'));
        expect(button().disabled).toBe(false);

        let status = { ...offered, enabled: false, reason: 'revising' };
        global.fetch = jest.fn((url, opts) => (opts && opts.method === 'POST'
            ? reply(202, { request: 'r' })
            : reply(200, { sessionId: task.task, board: 'granule', number: 42, revises: [status] })));
        jest.useFakeTimers();
        try {
            await act(async () => { button().click(); });
            await act(async () => { await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); });
            expect(global.fetch).toHaveBeenCalledWith('/api/board/granule/issues/42/actions/revise', expect.objectContaining({
                method: 'POST',
                body: JSON.stringify({ kind: 'Plan', revise: 'plan' }),
            }));
            expect(button().textContent).toBe('Update plan…');
            expect(button().disabled).toBe(true);

            status = offered;
            await act(async () => { jest.advanceTimersByTime(3000); });
            await act(async () => { await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); });
        } finally {
            jest.useRealTimers();
        }
        expect(button().disabled).toBe(false);
        expect(container.textContent).toContain('now the draft on the board');
    });

    test('a revise that failed says why', async () => {
        await render({
            sessionId: task.task, repo: 'granule', board: 'granule', number: 42,
            revises: [{ verb: 'revise', revise: 'plan', label: 'Update plan', enabled: true, error: 'the session is busy' }],
        });
        expect(container.textContent).toContain('Update plan failed: the session is busy');
    });

    test('a revise waits for the turn in flight, and for a running task', async () => {
        await render({
            sessionId: task.task, repo: 'granule', board: 'granule', number: 42,
            revises: [{ verb: 'revise', revise: 'plan', label: 'Update plan' }],
        });
        await act(async () => {
            FakeSocket.instances[0].deliver({ type: 'open', session: { busy: true, held: true, task: task.task, offset: 0 } });
        });
        const button = [...container.querySelectorAll('button')].find(b => b.textContent === 'Update plan');
        expect(button.disabled).toBe(true);
    });

    test('a session that wrote no draft offers no revise', async () => {
        await render({ sessionId: task.task, repo: 'granule' });
        await act(async () => {
            FakeSocket.instances[0].deliver({ type: 'open', session: { busy: false, task: task.task, offset: 0 } });
        });
        expect([...container.querySelectorAll('button')].some(b => b.textContent === 'Update plan')).toBe(false);
    });

    test('an older image falls back to the terminal', async () => {
        await render({ error: 'no sessions', legacy: true, sandbox: task.sandbox, namespace: 'ns' }, 409);
        expect(FakeSocket.instances).toHaveLength(0);
        const link = [...container.querySelectorAll('a')].find(a => a.textContent.includes('terminal'));
        expect(link.getAttribute('href')).toBe('#/terminal/ns/fix-granule-42?chat=plan');
    });
});

describe('ResearchConversation notes on a recipe conversation', () => {
    const draft = { markdown: '# Findings\n\nthe backoff is linear', note: 'backoff', draftedAt: '2026-10-05T10:00:00Z' };
    const open = async (notes, calls = []) => {
        global.fetch = jest.fn((url, opts) => {
            if (opts && opts.method) {
                calls.push({ url, method: opts.method, body: opts.body });
                return reply(url.endsWith('/capture') || url.endsWith('/notes/save') ? 202 : 204, {});
            }
            return reply(200, { sessionId: 's1', repo: 'granule', sandbox: 'rsch-1', namespace: 'ns', task: 'research-1', notes });
        });
        await act(async () => { root.render(<ResearchConversation sessionId="s1" />); });
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

    test('💾 writes a draft rather than pushing', async () => {
        const calls = await open({});
        const save = container.querySelector('[aria-label="Save notes"]');
        expect(save).toBeTruthy();
        expect(save.title).toContain('draft');
        await click(save);
        expect(calls).toEqual([{ url: '/api/research/s1/capture', method: 'POST', body: undefined }]);
    });

    test('the draft is shown, saved, edited and discarded', async () => {
        const calls = await open(draft);
        expect(container.textContent).toContain('Notes draft');
        expect(container.textContent).toContain('backoff.md');
        // Folded to its line until asked for.
        expect(container.textContent).not.toContain('the backoff is linear');
        await click(button('Show'));
        expect(container.textContent).toContain('the backoff is linear');

        await click(button('Save to research/notes'));
        await click(button('Edit'));
        const box = container.querySelector('textarea[aria-label="Edit the notes"]');
        await act(async () => {
            nativeSet(box, '# Edited');
            box.dispatchEvent(new Event('input', { bubbles: true }));
        });
        await click(button('Store edit'));
        await click(button('Discard'));

        expect(calls).toEqual([
            { url: '/api/research/s1/notes/save', method: 'POST', body: undefined },
            { url: '/api/research/s1/notes', method: 'PUT', body: JSON.stringify({ markdown: '# Edited' }) },
            { url: '/api/research/s1/notes', method: 'DELETE', body: undefined },
        ]);
    });

    test('what failed is said, and 💾 waits while it writes', async () => {
        await open({ ...draft, writing: true, saveError: 'push refused' });
        expect(container.textContent).toContain('push refused');
        expect(container.textContent).toContain('Writing this conversation up');
        expect(container.querySelector('[aria-label="Save notes"]').disabled).toBe(true);
    });
});
