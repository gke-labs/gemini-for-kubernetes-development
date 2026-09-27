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

        // The placeholder used to carry this, and a placeholder is gone
        // the moment anybody types into the box it was in.
        const box = container.querySelector('textarea');
        await act(async () => {
            nativeSet(box, 'never mind, do something else');
            box.dispatchEvent(new Event('input', { bubbles: true }));
        });
        expect(state()).toContain('Waiting for you');
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
});

describe('ResearchPanel', () => {
    const sessions = {
        sessions: [
            { sessionId: 'aaaaaaaa-1111', title: 'the retry loop', sandbox: 'rsch-repo-agent-1', repo: 'repo-agent', createdAt: '2026-09-26T10:00:00Z', paused: false },
            { sessionId: 'bbbbbbbb-2222', title: 'rollout flags', sandbox: 'rsch-kubectl-2', repo: 'kubectl', createdAt: '2026-09-26T09:00:00Z', paused: true },
        ],
    };

    test('lists this board\'s sessions and hides other repos behind a disclosure', async () => {
        global.fetch = jest.fn(() => reply(200, sessions));

        await act(async () => {
            root.render(<ResearchPanel boardName="repo-agent" repoURL="https://github.com/gke-labs/repo-agent" />);
        });
        await flush();

        expect(global.fetch).toHaveBeenCalledWith('/api/research');

        // Name, then age and sandbox state in small print. On the
        // board's own rail the repo is the board you are standing on,
        // and both ids are two spellings of "which pod" — none of the
        // three is what anyone is scanning the list for.
        const row = [...container.querySelectorAll('button')]
            .find(b => b.textContent.startsWith('the retry loop'));
        expect(row).toBeTruthy();
        expect(container.textContent).not.toContain('aaaaaaaa');
        expect(container.textContent).not.toContain('rsch-repo-agent-1');
        // The rail is a list, not a table: no header row survives the
        // width, and there is nothing left to head.
        expect(container.querySelector('table')).toBeNull();
        // Age is relative to now, so it is checked for being there
        // rather than for what it says.
        expect(row.textContent).toContain('up');
        expect(row.querySelector('[title="2026-09-26T10:00:00Z"]')).toBeTruthy();

        // Both ids are still one hover away, for the times you are
        // going to go and look at the pod.
        expect(row.title).toContain('session aaaaaaaa-1111');
        expect(row.title).toContain('sandbox rsch-repo-agent-1');

        // The other repo's session is accounted for but not in the table.
        expect(container.textContent).not.toContain('rollout flags');
        expect(container.textContent).toContain('1 conversation for other repositories');
    });

    // selectRow clicks a rail row by the title on its first line.
    const selectRow = async (title) => {
        const row = [...container.querySelectorAll('button')]
            .find(b => b.textContent.startsWith(title));
        expect(row).toBeTruthy();
        await act(async () => { row.dispatchEvent(new MouseEvent('click', { bubbles: true })); });
        await flush();
        return row;
    };

    test('the row is the selection, and the rail it came from stays put', async () => {
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

    test('the tab lands on the ask box, and + New conversation goes back to it', async () => {
        // Nothing is covering anything, so there is no ✕ and no
        // Escape: the way back is the row at the top of the rail.
        global.fetch = jest.fn(() => reply(404, { error: 'not found' }));
        global.fetch.mockImplementationOnce(() => reply(200, sessions));

        await act(async () => {
            root.render(<ResearchPanel boardName="repo-agent" repoURL="https://github.com/gke-labs/repo-agent" />);
        });
        await flush();

        const newRow = [...container.querySelectorAll('button')]
            .find(b => b.textContent === '+ New conversation');
        expect(newRow.getAttribute('aria-current')).toBe('true');
        expect(container.querySelector('textarea')).toBeTruthy();

        await selectRow('the retry loop');
        expect(container.querySelector('textarea[placeholder^="Ask anything about this repo"]')).toBeNull();
        expect(newRow.getAttribute('aria-current')).toBeNull();

        await act(async () => { newRow.dispatchEvent(new MouseEvent('click', { bubbles: true })); });
        await flush();
        expect(container.querySelector('textarea[placeholder^="Ask anything about this repo"]')).toBeTruthy();
        expect(newRow.getAttribute('aria-current')).toBe('true');
    });

    test('a half-typed question does not follow you into the next conversation', async () => {
        // Switching straight from one conversation to another is new
        // with the rail. The conversation resets what it knows is
        // per-session, but the composer is not on that list, and a
        // draft carried across and sent into the wrong session is the
        // failure that matters.
        const seen = [];
        global.fetch = jest.fn((url) => {
            if (url === '/api/research') return reply(200, sessions);
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

        const composer = container.querySelector('textarea');
        await act(async () => {
            nativeSet(composer, 'half a question about retries');
            composer.dispatchEvent(new Event('input', { bubbles: true }));
        });
        expect(container.querySelector('textarea').value).toBe('half a question about retries');

        // The other repo's session, reached through the disclosure.
        const disclosure = container.querySelector('[title^="Sessions you own for other repositories"]');
        await act(async () => { disclosure.dispatchEvent(new MouseEvent('click', { bubbles: true })); });
        await selectRow('rollout flags');
        expect(seen).toContain('bbbbbbbb-2222');
        await goLive('bbbbbbbb-2222');

        expect(container.querySelector('textarea').value).toBe('');
    });

    test('a conversation in the detail pane still offers to pop out', async () => {
        // fill and standalone used to be one prop. The detail pane
        // wants the full height and the pop-out both; only the
        // popped-out window itself should be without it.
        global.fetch = jest.fn(() => reply(200, sessions));
        await act(async () => {
            root.render(<ResearchPanel boardName="repo-agent" repoURL="https://github.com/gke-labs/repo-agent" />);
        });
        await flush();

        await selectRow('the retry loop');

        const more = container.querySelector('[aria-label="More actions"]');
        await act(async () => { more.dispatchEvent(new MouseEvent('click', { bubbles: true })); });
        expect([...container.querySelectorAll('a')].map(a => a.textContent)).toContain('pop out ↗');
    });

    test('the other-repositories list keeps the repo, which is why it is separate', async () => {
        global.fetch = jest.fn(() => reply(200, sessions));

        await act(async () => {
            root.render(<ResearchPanel boardName="repo-agent" repoURL="https://github.com/gke-labs/repo-agent" />);
        });
        await flush();

        const disclosure = container.querySelector('[title^="Sessions you own for other repositories"]');
        await act(async () => { disclosure.dispatchEvent(new MouseEvent('click', { bubbles: true })); });

        expect(container.textContent).toContain('rollout flags');
        expect(container.textContent).toContain('kubectl');
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
        expect(container.textContent).toContain('No conversations for repo-agent yet');
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
