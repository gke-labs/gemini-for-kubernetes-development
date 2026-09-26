import React from 'react';
import { createRoot } from 'react-dom/client';
import { act as domAct } from 'react-dom/test-utils';

import { ResearchPanel, ResearchConversation } from './Research';

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
    const proto = el.tagName === 'TEXTAREA' ? window.HTMLTextAreaElement : window.HTMLInputElement;
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
        const send = [...container.querySelectorAll('button')].find(b => b.textContent === 'Send');
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

    test('the terminal view shows the source unparsed and remembers itself', async () => {
        const table = '| a | b |\n| :-- | :-- |\n| 1 | 2 |';
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
        expect(container.querySelector('.research-terminal')).toBeNull();

        const rendersBefore = mockMarkdownCalls.length;
        const flip = [...container.querySelectorAll('button')].find(b => b.textContent === 'terminal');
        await act(async () => { flip.click(); });

        // No markdown pass at all in this view: the agent's own line
        // breaks are what make the table line up.
        expect(mockMarkdownCalls.length).toBe(rendersBefore);
        const pane = container.querySelector('.research-terminal');
        expect(pane).toBeTruthy();
        expect(pane.querySelector('.term-agent').textContent).toBe(table);
        expect(pane.querySelector('.term-user').textContent).toContain('compare them');
        expect(localStorage.getItem('repoboard.research.view')).toBe('terminal');

        // And the next conversation opened comes up in it.
        await act(async () => { root.render(<ResearchConversation sessionId="s1" />); });
        await flush();
        expect(container.querySelector('.research-terminal')).toBeTruthy();
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
        expect(container.querySelector('textarea').placeholder).toContain('Stop to interrupt');
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
});

describe('ResearchPanel', () => {
    const sessions = {
        sessions: [
            { sessionId: 'aaaaaaaa-1111', sandbox: 'rsch-repo-agent-1', repo: 'repo-agent', createdAt: '2026-09-26T10:00:00Z', paused: false },
            { sessionId: 'bbbbbbbb-2222', sandbox: 'rsch-kubectl-2', repo: 'kubectl', createdAt: '2026-09-26T09:00:00Z', paused: true },
        ],
    };

    test('lists this board\'s sessions and hides other repos behind a disclosure', async () => {
        global.fetch = jest.fn(() => reply(200, sessions));

        await act(async () => {
            root.render(<ResearchPanel boardName="repo-agent" repoURL="https://github.com/gke-labs/repo-agent" />);
        });
        await flush();

        expect(global.fetch).toHaveBeenCalledWith('/api/research');
        expect(container.textContent).toContain('aaaaaaaa');
        expect(container.textContent).toContain('rsch-repo-agent-1');
        // The other repo's session is accounted for but not in the table.
        expect(container.textContent).not.toContain('rsch-kubectl-2');
        expect(container.textContent).toContain('1 conversation for other repositories');
    });

    // claimFetch answers the list and the claim; everything else 404s,
    // which is what a session whose sandbox does not exist yet looks
    // like to the conversation's probe.
    const claimFetch = (claimed) => jest.fn((url, opts) => {
        if (url === '/api/research') return reply(200, { sessions: [] });
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

    test('an empty conversation claims nothing in particular and opens it', async () => {
        const claimed = [];
        global.fetch = claimFetch(claimed);

        await renderPanel();
        expect(container.textContent).toContain('No research conversations for repo-agent yet');
        await clickButton('Empty conversation');

        expect(claimed).toEqual([{}]);
        // Straight into the conversation, which reports the wait rather
        // than a 404 — the sandbox is minutes away.
        await flush();
        expect(container.textContent).toContain('Preparing the sandbox');
    });

    test('Generate Overview claims the canned read and shows the row at once', async () => {
        const claimed = [];
        global.fetch = claimFetch(claimed);

        await renderPanel();
        await clickButton('Generate Overview');

        expect(claimed).toEqual([{ kind: 'onboard' }]);
        // Stays on the list: a second click is a second sandbox and a
        // second engine, so the request has to be visible immediately —
        // before the server's own list has caught up with the claim.
        expect(container.textContent).toContain('overview');
        expect(container.textContent).toContain('requested');
        expect(container.textContent).not.toContain('Preparing the sandbox');
    });

    test('what happened asks for the window that was picked', async () => {
        const claimed = [];
        global.fetch = claimFetch(claimed);

        await renderPanel();
        await clickButton('What happened ▾');
        const window = [...container.querySelectorAll('div')].find(d => d.textContent === 'last 1 month');
        await act(async () => { window.click(); });
        await flush();

        expect(claimed).toEqual([{ kind: 'activity', since: '1 month' }]);
    });

    test('a typed question carries its optional name and opens the conversation', async () => {
        const claimed = [];
        global.fetch = claimFetch(claimed);

        await renderPanel();
        const box = container.querySelector('textarea');
        const name = container.querySelector('input[aria-label="Session name"]');
        await act(async () => {
            nativeSet(box, 'where does the retry loop live?');
            box.dispatchEvent(new Event('input', { bubbles: true }));
            nativeSet(name, 'retries');
            name.dispatchEvent(new Event('input', { bubbles: true }));
        });
        await clickButton('Research');

        expect(claimed).toEqual([{ kind: 'topic', topic: 'where does the retry loop live?', title: 'retries' }]);
        await flush();
        expect(container.textContent).toContain('Preparing the sandbox');
    });

    test('a requested session is listed by name, with no sandbox yet', async () => {
        global.fetch = jest.fn(() => reply(200, {
            sessions: [{
                sessionId: 'cccccccc-3333', repo: 'repo-agent', requested: true,
                title: 'what happened · 2 weeks', createdAt: '2026-09-26T10:00:00Z',
            }],
        }));

        await renderPanel();
        expect(container.textContent).toContain('what happened · 2 weeks');
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
        global.fetch = jest.fn(() => reply(200, { ...sessions, forkOwner: 'barney-s' }));

        await renderPanel();
        const link = [...container.querySelectorAll('a')]
            .find(a => a.textContent.includes('exploration/notes'));
        expect(link).toBeTruthy();
        expect(link.getAttribute('href'))
            .toBe('https://github.com/barney-s/repo-agent/tree/exploration/notes');
    });

    test('with no fork owner there is no footnote to click', async () => {
        global.fetch = jest.fn(() => reply(200, sessions));

        await renderPanel();
        expect(container.textContent).not.toContain('exploration/notes');
    });
});
