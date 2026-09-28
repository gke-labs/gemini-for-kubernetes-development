import React from 'react';
import { createRoot } from 'react-dom/client';
import { act as domAct } from 'react-dom/test-utils';

// Work pulls in Research, which pulls in react-markdown and remark-gfm.
// Both are ESM and jest does not transform node_modules; nothing here
// renders markdown, so a stub is enough to get the module loaded.
jest.mock('react-markdown', () => ({ children }) => children);
jest.mock('remark-gfm', () => 'gfm-plugin-stub');
// xterm measures a canvas the moment it loads, which jsdom has not
// implemented; nothing here opens a terminal.
jest.mock('xterm', () => ({ Terminal: class { open() {} write() {} dispose() {} onData() {} loadAddon() {} } }));
jest.mock('xterm-addon-fit', () => ({ FitAddon: class { fit() {} } }));

const { TryPanel } = require('./Work');
const Work = require('./Work').default;

const act = React.act || domAct;

global.IS_REACT_ACT_ENVIRONMENT = true;

const flush = () => act(async () => { await Promise.resolve(); await Promise.resolve(); });

let container;
let root;

beforeEach(() => {
    jest.useFakeTimers();
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
});

afterEach(() => {
    act(() => root.unmount());
    container.remove();
    delete global.fetch;
    jest.useRealTimers();
});

// runbookReply serves the panel's poll, and whatever the test wants for
// the DELETE.
const runbookReply = (removeStatus, removeBody) => (url, opts) => {
    if (opts && opts.method === 'DELETE') {
        return Promise.resolve({
            ok: removeStatus >= 200 && removeStatus < 300,
            status: removeStatus,
            text: () => Promise.resolve(removeBody),
        });
    }
    return Promise.resolve({
        ok: true,
        status: 200,
        json: () => Promise.resolve({
            repoShort: 'orl', gcpProject: 'p', sandboxes: [], pending: [],
            instances: [{ name: 'instance1', deployed: false }],
        }),
    });
};

const findButton = (text) =>
    Array.from(container.querySelectorAll('button')).find(b => b.textContent.includes(text));

// clickReply serves the panel's poll with one standing click and no
// runs at all: the state right after a click, before anything exists.
const clickReply = (pending) => () => Promise.resolve({
    ok: true,
    status: 200,
    json: () => Promise.resolve({
        repoShort: 'orl', gcpProject: 'p', sandboxes: [], instances: [], pending,
    }),
});

describe('TryPanel standing clicks', () => {
    test('a click that failed says so, and lets you click again', async () => {
        global.fetch = jest.fn(clickReply([{
            mode: 'plan', scenario: 'gcevm', instance: 'gcevm',
            phase: 'Failed', reason: 'LaunchInterrupted',
            message: 'the controller restarted mid-launch; check the run before clicking again',
        }]));

        await act(async () => { root.render(<TryPanel boardName="myboard" />); });
        await flush();

        // The whole point of keeping a failed click for a week: the tab
        // used to look exactly as empty as it did before the click.
        expect(container.textContent).toContain('plan failed');
        const replan = findButton('Re-plan');
        expect(replan).toBeTruthy();
        expect(replan.disabled).toBe(false);
    });

    test('a click still in flight holds the buttons down', async () => {
        global.fetch = jest.fn(clickReply([{
            mode: 'plan', scenario: 'gcevm', instance: 'gcevm', phase: 'Running',
        }]));

        await act(async () => { root.render(<TryPanel boardName="myboard" />); });
        await flush();

        // Provisional (no run on the branch yet), so the row says what
        // it is waiting for rather than "queued" — either way it is the
        // same click, and it holds the buttons.
        expect(container.textContent).toContain('preparing the sandbox');
        expect(findButton('Re-plan').disabled).toBe(true);
    });

    // A run can have two standing clicks at once: a deploy that failed
    // is kept for a week, and the re-plan you started this morning is
    // live. The row speaks for one of them, and it has to be the live
    // one — otherwise the row shows last week's failure and, worse,
    // leaves the buttons enabled underneath work already in flight.
    test('a live click outranks a week-old failure on the same run', async () => {
        global.fetch = jest.fn(clickReply([
            {
                mode: 'deploy', scenario: 'gcevm', instance: 'gcevm',
                phase: 'Failed', reason: 'LaunchInterrupted', message: 'the controller restarted mid-launch',
            },
            { mode: 'plan', scenario: 'gcevm', instance: 'gcevm', phase: 'Running' },
        ]));

        await act(async () => { root.render(<TryPanel boardName="myboard" />); });
        await flush();

        expect(container.textContent).not.toContain('deploy failed');
        expect(findButton('Re-plan').disabled).toBe(true);
    });
});

describe('TryPanel Remove', () => {
    test('says so when the removal fails instead of pretending it worked', async () => {
        global.fetch = jest.fn(runbookReply(404, '{"error":"instance records not found"}'));
        window.confirm = jest.fn(() => true);

        await act(async () => { root.render(<TryPanel boardName="myboard" />); });
        await flush();

        const remove = findButton('Remove');
        expect(remove).toBeTruthy();
        await act(async () => { remove.click(); });
        await flush();

        expect(window.confirm).toHaveBeenCalled();
        // The failure reaches the user. It used to be dropped on the
        // floor: the row stayed, and nothing said why.
        expect(container.textContent).toContain('Remove instance1 failed');
        expect(container.textContent).toContain('instance records not found');
    });

    test('asks before deleting, and does nothing if the answer is no', async () => {
        global.fetch = jest.fn(runbookReply(200, '{}'));
        window.confirm = jest.fn(() => false);

        await act(async () => { root.render(<TryPanel boardName="myboard" />); });
        await flush();

        await act(async () => { findButton('Remove').click(); });
        await flush();

        const deletes = global.fetch.mock.calls.filter(([, opts]) => opts && opts.method === 'DELETE');
        expect(deletes).toHaveLength(0);
    });

    test('a removal that works leaves no error behind', async () => {
        global.fetch = jest.fn(runbookReply(200, '{"status":"removed"}'));
        window.confirm = jest.fn(() => true);

        await act(async () => { root.render(<TryPanel boardName="myboard" />); });
        await flush();

        await act(async () => { findButton('Remove').click(); });
        await flush();

        const deletes = global.fetch.mock.calls.filter(([, opts]) => opts && opts.method === 'DELETE');
        expect(deletes).toHaveLength(1);
        expect(deletes[0][0]).toBe('/api/board/myboard/runbook/instance/instance1');
        expect(container.textContent).not.toContain('failed');
    });
});

// The All tab is the cross-board view: Up Next, Research, Runs. Research
// is there because a conversation lives on a board and is remembered
// without one — "the one about the retry loop", not "the one on
// repo-agent".
describe('Work, all boards', () => {
    const boards = [
        { name: 'repo-agent', repoURL: 'https://github.com/gke-labs/repo-agent', needsHuman: 0, active: 0 },
        { name: 'kubectl', repoURL: 'https://github.com/kubernetes/kubectl', needsHuman: 0, active: 0 },
    ];

    const json = (body) => Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) });

    const serve = () => jest.fn((url) => {
        if (url === '/api/boards') return json(boards);
        if (url === '/api/research') {
            return json({
                sessions: [
                    { sessionId: 'aaaaaaaa-1111', title: 'the retry loop', repo: 'repo-agent', createdAt: '2026-09-26T10:00:00Z' },
                    { sessionId: 'bbbbbbbb-2222', title: 'rollout flags', repo: 'kubectl', createdAt: '2026-09-26T09:00:00Z' },
                ],
            });
        }
        return json([]);
    });

    test('Research collects the conversations from every board', async () => {
        global.fetch = serve();

        await act(async () => { root.render(<Work namespace="alice" />); });
        // Two rounds: the boards land, and the fan-out over their feeds
        // settles on the round after.
        await flush();
        await flush();

        // Lands on Up Next, as it always has.
        expect(container.textContent).toContain('Nothing needs you anywhere');

        const tab = findButton('Research');
        expect(tab).toBeTruthy();
        await act(async () => { tab.click(); });
        await flush();
        await flush();

        // Both boards' conversations, in one list, with no board filter
        // between them.
        expect(container.textContent).toContain('the retry loop');
        expect(container.textContent).toContain('rollout flags');
        // And it is the cross-board list that was asked for, not a
        // board's own tab reached sideways.
        expect(global.fetch).toHaveBeenCalledWith('/api/research');
    });
});

// The poll costs a GitHub call per board per tick, out of a budget shared
// with the controller, the factory and the member's own git. A hidden tab
// already stops — but hidden is not the case that spends it. A board left
// in plain sight on a second monitor, unfocused and unread, is.
describe('Work, an unattended window', () => {
    const boards = [
        { name: 'repo-agent', repoURL: 'https://github.com/gke-labs/repo-agent', needsHuman: 0, active: 0 },
    ];
    const json = (body) => Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) });
    const serve = () => jest.fn((url) => (url === '/api/boards' ? json(boards) : json([])));
    const feedCalls = () => global.fetch.mock.calls.filter(([url]) => String(url).endsWith('/work')).length;
    const settle = async () => { await flush(); await flush(); };
    const wait = async (ms) => { await act(async () => { jest.advanceTimersByTime(ms); }); await settle(); };

    test('stops polling after five quiet minutes, and the pointer brings it back', async () => {
        global.fetch = serve();
        const focus = jest.spyOn(document, 'hasFocus').mockReturnValue(false);

        await act(async () => { root.render(<Work namespace="alice" />); });
        await settle();

        // Just arrived: unfocused is not yet unattended.
        global.fetch.mockClear();
        await wait(3 * 60 * 1000);
        expect(feedCalls()).toBeGreaterThan(0);

        // Nobody has touched it since. It should go quiet and stay quiet.
        await wait(5 * 60 * 1000);
        global.fetch.mockClear();
        await wait(10 * 60 * 1000);
        expect(feedCalls()).toBe(0);

        // The pointer is someone coming back: refresh on the way in, not
        // at the next tick — the wait is what reads as a frozen board.
        await act(async () => { document.dispatchEvent(new Event('pointermove')); });
        await settle();
        expect(feedCalls()).toBeGreaterThan(0);

        focus.mockRestore();
    });
});
