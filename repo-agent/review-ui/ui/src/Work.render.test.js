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

const { TryPanel, WorkRow, SessionSlideOver, prRunName, anyPosting } = require('./Work');
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

describe('TryPanel local-only runs', () => {
    const localReply = () => Promise.resolve({
        ok: true,
        status: 200,
        json: () => Promise.resolve({
            repoShort: 'granule', gcpProject: 'p', sandboxes: [], pending: [],
            instances: [{
                name: 'gke-std', deployed: false, localOnly: true, sandbox: 'runbook-granule-gke-std',
                htmlURL: '/api/board/myboard/runbook/instance/gke-std/file/runbook.md',
                runbook: { name: 'runbook.md', htmlURL: '/api/board/myboard/runbook/instance/gke-std/file/runbook.md' },
            }],
        }),
    });

    test('are marked, link to the sandbox copy, and offer no Remove', async () => {
        global.fetch = jest.fn(localReply);

        await act(async () => { root.render(<TryPanel boardName="myboard" />); });
        await flush();

        expect(container.textContent).toContain('local-only');
        const links = Array.from(container.querySelectorAll('a')).map(a => a.getAttribute('href'));
        expect(links).toContain('/api/board/myboard/runbook/instance/gke-std/file/runbook.md');
        expect(links.some(h => h && h.includes('github.com'))).toBe(false);
        // Its records are the sandbox's: removing them is deleting the
        // sandbox, which takes the teardown script with it.
        expect(findButton('Remove')).toBeFalsy();
    });
});

// React listens for the native setter, not a plain assignment.
const setValue = (el, value) => {
    const proto = el.tagName === 'SELECT' ? window.HTMLSelectElement.prototype
        : el.tagName === 'TEXTAREA' ? window.HTMLTextAreaElement.prototype
            : window.HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(proto, 'value').set.call(el, value);
    el.dispatchEvent(new Event(el.tagName === 'SELECT' ? 'change' : 'input', { bubbles: true }));
};

// pickerReply serves the poll with a repository runbook and one run —
// both things a new run can start from — and accepts the plan.
const pickerReply = (url, opts) => {
    if (opts && opts.method === 'POST') {
        return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ status: 'requested' }) });
    }
    return Promise.resolve({
        ok: true,
        status: 200,
        json: () => Promise.resolve({
            repoShort: 'orl', gcpProject: 'p', sandboxes: [], pending: [],
            instances: [{ name: 'gke1', deployed: false }],
            repoRunbooks: ['gke'],
        }),
    });
};

describe('TryPanel runbook picker', () => {
    test('offers the repository\'s runbooks and your runs, and plans from the one picked', async () => {
        global.fetch = jest.fn(pickerReply);

        await act(async () => { root.render(<TryPanel boardName="myboard" />); });
        await flush();

        const select = container.querySelector('select[aria-label="runbook to start from"]');
        expect(select).toBeTruthy();
        const groups = Array.from(select.querySelectorAll('optgroup')).map(g =>
            [g.label, Array.from(g.querySelectorAll('option')).map(o => o.value)]);
        expect(groups).toEqual([
            ['repository (.agents/runbooks)', ['gke']],
            ['your runs', ['gke1']],
        ]);

        await act(async () => { setValue(container.querySelector('input[type="text"]'), 'pr-42'); });
        await act(async () => { setValue(select, 'gke1'); });
        // With a runbook, what you type is only what to change.
        expect(container.querySelector('textarea').placeholder).toContain('anything to change from gke1');

        await act(async () => { findButton('▶ Plan').click(); });
        await flush();

        const posts = global.fetch.mock.calls.filter(([, opts]) => opts && opts.method === 'POST');
        expect(posts).toHaveLength(1);
        expect(JSON.parse(posts[0][1].body)).toEqual({ mode: 'plan', name: 'pr-42', intent: '', runbook: 'gke1' });
    });

    test('a run cannot be started from itself', async () => {
        global.fetch = jest.fn(pickerReply);

        await act(async () => { root.render(<TryPanel boardName="myboard" />); });
        await flush();

        await act(async () => { setValue(container.querySelector('input[type="text"]'), 'gke'); });
        await act(async () => { setValue(container.querySelector('select[aria-label="runbook to start from"]'), 'gke'); });

        expect(container.textContent).toContain('a run cannot start from itself');
        expect(findButton('▶ Plan').disabled).toBe(true);
    });

    test('with nothing picked, a plan carries no runbook', async () => {
        global.fetch = jest.fn(pickerReply);

        await act(async () => { root.render(<TryPanel boardName="myboard" />); });
        await flush();

        await act(async () => { setValue(container.querySelector('input[type="text"]'), 'fresh'); });
        await act(async () => { findButton('▶ Plan').click(); });
        await flush();

        const posts = global.fetch.mock.calls.filter(([, opts]) => opts && opts.method === 'POST');
        expect(JSON.parse(posts[0][1].body)).toEqual({ mode: 'plan', name: 'fresh', intent: '' });
    });

    test('a refused start says why instead of vanishing', async () => {
        global.fetch = jest.fn((url, opts) => (opts && opts.method === 'POST')
            ? Promise.resolve({ ok: false, status: 400, text: () => Promise.resolve('a runbook can only be planned from') })
            : pickerReply(url, opts));

        await act(async () => { root.render(<TryPanel boardName="myboard" />); });
        await flush();

        await act(async () => { setValue(container.querySelector('input[type="text"]'), 'pr-42'); });
        await act(async () => { setValue(container.querySelector('select[aria-label="runbook to start from"]'), 'gke'); });
        await act(async () => { findButton('▶ Plan').click(); });
        await flush();
        await flush();

        expect(container.textContent).toContain('plan pr-42 failed: a runbook can only be planned from');
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

// Deploy ▾ on a pull request row: the repository's runbooks, each a
// plan of a run named for the runbook and the pull request, pinned to
// it. The runs pinned to a pull request show on its row.
describe('WorkRow Deploy', () => {
    const pr = {
        type: 'pull', number: 42, group: 'prs', title: 'retry the fetch',
        htmlURL: 'https://github.com/o/r/pull/42', updatedAt: '2026-09-28T10:00:00Z',
    };
    // A factory, not one shared mock: CRA resets every mock's
    // implementation before each test.
    const accepted = () => jest.fn(() => Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ status: 'requested' }) }));
    const renderRow = async (runState, item = pr) => {
        await act(async () => {
            root.render(<table><tbody>
                <WorkRow item={item} boardName="myboard" namespace="alice" onAction={() => {}} runState={runState} />
            </tbody></table>);
        });
    };
    const posted = () => global.fetch.mock.calls
        .filter(([, opts]) => opts && opts.method === 'POST')
        .map(([url, opts]) => [url, JSON.parse(opts.body)]);

    test('the -in-pod suffix stays last', () => {
        expect(prRunName('gke', 42)).toBe('gke-pr42');
        expect(prRunName('gke-in-pod', 42)).toBe('gke-pr42-in-pod');
    });

    test('with no runbooks there is nothing to deploy with', async () => {
        await renderRow({ repoRunbooks: [], instances: [] });
        expect(findButton('Deploy')).toBeUndefined();
    });

    test('an issue is not deployed', async () => {
        await renderRow({ repoRunbooks: ['gke'], instances: [] }, { ...pr, type: 'issue', group: 'issues' });
        expect(findButton('Deploy')).toBeUndefined();
    });

    test('the first pick plans a new run from the runbook, pinned to the pull request', async () => {
        global.fetch = accepted();
        await renderRow({ repoRunbooks: ['gke'], instances: [] });

        await act(async () => { findButton('Deploy ▾').click(); });
        await act(async () => { findButton('gke').click(); });
        await flush();

        expect(posted()).toEqual([['/api/board/myboard/runbook',
            { mode: 'plan', name: 'gke-pr42', intent: '', target: 42, runbook: 'gke' }]]);
    });

    test('picking it again re-plans that run, which moves the pin', async () => {
        global.fetch = accepted();
        await renderRow({ repoRunbooks: ['gke'], instances: [{ name: 'gke-pr42', target: 42 }] });

        await act(async () => { findButton('Deploy ▾').click(); });
        await act(async () => { findButton('gke').click(); });
        await flush();

        expect(posted()).toEqual([['/api/board/myboard/runbook',
            { mode: 'plan', name: 'gke-pr42', intent: '', target: 42 }]]);
    });

    test('a name too long for the sandbox cannot be picked', async () => {
        const long = 'a'.repeat(38);
        await renderRow({ repoRunbooks: [long], instances: [] });
        await act(async () => { findButton('Deploy ▾').click(); });
        expect(findButton(long).disabled).toBe(true);
    });

    test('a refused plan says why', async () => {
        global.fetch = jest.fn(() => Promise.resolve({ ok: false, status: 400, text: () => Promise.resolve('no GCP project configured') }));
        await renderRow({ repoRunbooks: ['gke'], instances: [] });

        await act(async () => { findButton('Deploy ▾').click(); });
        await act(async () => { findButton('gke').click(); });
        await flush();
        await flush();

        expect(container.textContent).toContain('plan gke-pr42 failed: no GCP project configured');
    });

    test('the runs pinned to this pull request show on its row, and no others', async () => {
        await renderRow({
            repoRunbooks: [],
            instances: [
                { name: 'gke-pr42', target: 42, htmlURL: 'https://github.com/alice/r/tree/research/runs/docs-exploration/agent-runs/gke-pr42', latestReceipt: { verdict: 'PLANNED (3 steps)' } },
                { name: 'gke-pr7', target: 7 },
                { name: 'gke' },
            ],
        });
        expect(container.textContent).toContain('gke-pr42 · planned');
        expect(container.textContent).not.toContain('gke-pr7');
        const link = Array.from(container.querySelectorAll('a')).find(a => a.textContent.includes('gke-pr42'));
        expect(link.getAttribute('href')).toContain('agent-runs/gke-pr42');
    });
});

// A row says a write or a revise filed on its runs stands, and the board
// polls fast while one does.
describe('anyPosting', () => {
    test('is whether a row has a write or a revise standing', () => {
        expect(anyPosting([])).toBe(false);
        expect(anyPosting([{ number: 1 }])).toBe(false);
        expect(anyPosting([{ number: 1 }, { number: 2, posting: true }])).toBe(true);
    });
});

describe('WorkRow review', () => {
    const review = {
        type: 'pull', number: 42, reviewPending: true, group: 'prs', title: 'retry the fetch',
        htmlURL: 'https://github.com/o/r/pull/42', updatedAt: '2026-10-05T10:00:00Z',
    };
    const renderRow = async (item) => {
        await act(async () => {
            root.render(<table><tbody>
                <WorkRow item={item} boardName="myboard" namespace="alice" onAction={() => {}}
                    runState={{ repoRunbooks: [], instances: [] }} />
            </tbody></table>);
        });
    };
    const link = (text) => Array.from(container.querySelectorAll('a')).find(a => a.textContent.includes(text));
    // A chip shows its recipe; its status is the colour and its hover's first line.
    const chip = (status) => Array.from(container.querySelectorAll('a')).find(a => (a.getAttribute('title') || '').split(' — ')[0] === status);

    const reviewRun = (status) => ({ recipe: 'review', label: 'Review', sandbox: 'review-r-42', task: 'recipe-review-1', status });

    test('a pending review whose run is gone is the review chip, linking GitHub to finalize or discard it', async () => {
        await renderRow(review);
        expect(chip('Review: pending').getAttribute('href')).toBe('https://github.com/o/r/pull/42/files');
        expect(findButton('Review ready')).toBeUndefined();
        expect(link('Finalize')).toBeUndefined();
        expect(findButton('✕')).toBeUndefined();
    });

    test('a pending review with its run recorded is the run\'s ready chip, to its session', async () => {
        await renderRow({ ...review, sessions: [reviewRun('done')] });
        expect(chip('Review: ready').getAttribute('href')).toBe('#/task-session/review-r-42/recipe-review-1');
        expect(chip('Review: pending')).toBeUndefined();
    });

    test('a submitted review whose run is gone is a done chip linking the PR', async () => {
        await renderRow({ ...review, reviewPending: false, reviewed: true, recipes: [{ name: 'review', label: 'Review' }] });
        expect(chip('Review: done').getAttribute('href')).toBe('https://github.com/o/r/pull/42');
        expect(link('Review ✓')).toBeUndefined();
        expect(findButton('Review')).toBeDefined();
    });

    test('a review at work can be watched', async () => {
        await renderRow({ ...review, reviewPending: false, recipes: [{ name: 'review', label: 'Review' }],
            sessions: [reviewRun('running')] });
        expect(chip('Review: running').getAttribute('href')).toBe('#/task-session/review-r-42/recipe-review-1');
        expect(findButton('Review')).toBeUndefined();
    });
});

// A PR of yours is looked after by care, a recipe like any other: no
// drawer of the fix's revises (those are care's and the sessions' now).
describe('WorkRow your PR', () => {
    const pr = {
        type: 'pull', number: 9, group: 'prs', mine: true, myPR: true, title: 'the fix',
        recipes: [{ name: 'care', label: 'Care' }, { name: 'summarize', label: 'Summarize' }],
        htmlURL: 'https://github.com/o/r/pull/9', updatedAt: '2026-10-06T10:00:00Z',
    };
    const fixRun = status => ({ recipe: 'fix', label: 'Fix', sandbox: 'fix-r-7', task: 'recipe-fix-1', status });
    const renderRow = async (item, onAction = () => {}) => {
        await act(async () => {
            root.render(<table><tbody>
                <WorkRow item={item} boardName="myboard" namespace="alice" onAction={onAction}
                    runState={{ repoRunbooks: [], instances: [] }} />
            </tbody></table>);
        });
    };
    const link = (text) => Array.from(container.querySelectorAll('a')).find(a => a.textContent.includes(text));
    // A chip shows its recipe; its status is the colour and its hover's first line.
    const chip = (status) => Array.from(container.querySelectorAll('a')).find(a => (a.getAttribute('title') || '').split(' — ')[0] === status);

    test('Care launches as a recipe, and there is no Agent drawer', async () => {
        const onAction = jest.fn();
        await renderRow({ ...pr, sessions: [fixRun('done')] }, onAction);
        expect(findButton('Agent ▾')).toBeUndefined();
        await act(async () => { findButton('Care').click(); });
        expect(onAction).toHaveBeenCalledWith('prs/9/recipes/care', 'Care', {});
    });

    test('a PR that is not mine is reviewed, not promoted', async () => {
        await renderRow({ ...pr, mine: false, myPR: false, draftPR: true, author: 'carol', recipes: [{ name: 'review', label: 'Review' }] });
        expect(findButton('Review')).toBeDefined();
        expect(findButton('Promote PR')).toBeUndefined();
        expect(container.textContent).toContain('carol');
    });

    test('a fix at work can be watched', async () => {
        await renderRow({ ...pr, sessions: [fixRun('running')] });
        expect(chip('Fix: running').getAttribute('href')).toBe('#/task-session/fix-r-7/recipe-fix-1');
    });

    test('auto follow-up shows on a PR the board fixed, and toggles', async () => {
        global.fetch = jest.fn(() => Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) }));
        const sandbox = { name: 'fix-r-7', engine: 'gemini', autoIterate: 'on' };
        await renderRow({ ...pr, sandbox });
        expect(container.textContent).not.toContain('auto ⏻');
        await renderRow({ ...pr, sandbox, sessions: [fixRun('done')] });
        const chip = Array.from(container.querySelectorAll('span')).find(s => (s.title || '').startsWith('Auto follow-up'));
        expect(chip).toBeDefined();
        await act(async () => { chip.click(); });
        expect(global.fetch).toHaveBeenCalledWith('/api/board/myboard/prs/9/auto-iterate', expect.objectContaining({ method: 'POST', body: JSON.stringify({ mode: 'off' }) }));
    });
});

// A row follows its runs by one set of rules, the same for every recipe:
// a recipe not run yet is its label, one done or failed is ‹label› again,
// a running one is a chip to its session, and a ready one is its draft.
describe('WorkRow run rules', () => {
    const issue = {
        type: 'issue', number: 5, group: 'issues', title: 'a bug',
        htmlURL: 'https://github.com/o/r/issues/5', updatedAt: '2026-10-06T10:00:00Z',
        recipes: [
            { name: 'triage', label: 'Triage' }, { name: 'plan', label: 'Plan' }, { name: 'fix', label: 'Fix' },
            { name: 'summarize', label: 'Summarize', inputs: ['length'] },
        ],
    };
    const run = (recipe, label, status) => ({ recipe, label, sandbox: 'fix-r-5', task: `recipe-${recipe}-1`, status });
    const renderRow = async (item, onAction = () => {}) => {
        await act(async () => {
            root.render(<table><tbody>
                <WorkRow item={item} boardName="myboard" namespace="alice" onAction={onAction}
                    runState={{ repoRunbooks: [], instances: [] }} />
            </tbody></table>);
        });
    };
    const link = (text) => Array.from(container.querySelectorAll('a')).find(a => a.textContent.includes(text));
    // A chip shows its recipe; its status is the colour and its hover's first line.
    const chip = (status) => Array.from(container.querySelectorAll('a')).find(a => (a.getAttribute('title') || '').split(' — ')[0] === status);

    test('every recipe the row offers is a button, in the catalog order, launched by its recipe', async () => {
        const onAction = jest.fn();
        await renderRow(issue, onAction);
        const labels = Array.from(container.querySelectorAll('button')).map(b => b.textContent);
        expect(labels).toEqual(['Triage', 'Plan', 'Fix', 'Summarize']);
        await act(async () => { findButton('Plan').click(); });
        expect(onAction).toHaveBeenCalledWith('issues/5/recipes/plan', 'Plan', {});
    });

    test('a required input is asked for, and passed', async () => {
        const onAction = jest.fn();
        window.prompt = jest.fn(() => ' short ');
        await renderRow(issue, onAction);
        await act(async () => { findButton('Summarize').click(); });
        expect(onAction).toHaveBeenCalledWith('issues/5/recipes/summarize', 'Summarize', { inputs: { length: 'short' } });
    });

    test('a cancelled input launches nothing', async () => {
        const onAction = jest.fn();
        window.prompt = jest.fn(() => null);
        await renderRow(issue, onAction);
        await act(async () => { findButton('Summarize').click(); });
        expect(onAction).not.toHaveBeenCalled();
    });

    test('a running recipe is a chip to its session, not a button', async () => {
        await renderRow({ ...issue, sessions: [run('plan', 'Plan', 'running')] });
        expect(findButton('Plan')).toBeUndefined();
        expect(chip('Plan: running').getAttribute('href')).toBe('#/task-session/fix-r-5/recipe-plan-1');
    });

    test('a session link opens beside the board; a modified click keeps its tab', async () => {
        const onOpenSession = jest.fn();
        const item = { ...issue, sessions: [run('summarize', 'Summarize', 'ready')] };
        await act(async () => {
            root.render(<table><tbody>
                <WorkRow item={item} boardName="myboard" namespace="alice" onAction={() => {}} onOpenSession={onOpenSession}
                    runState={{ repoRunbooks: [], instances: [] }} />
            </tbody></table>);
        });
        const click = (a, init) => {
            const e = new MouseEvent('click', { bubbles: true, cancelable: true, button: 0, ...init });
            act(() => { a.dispatchEvent(e); });
            return e;
        };
        const ready = chip('Summarize: ready');
        expect(click(ready).defaultPrevented).toBe(true);
        expect(onOpenSession).toHaveBeenCalledWith(item.sessions[0]);
        expect(click(ready, { metaKey: true }).defaultPrevented).toBe(false);
        expect(onOpenSession).toHaveBeenCalledTimes(1);
        expect(ready.getAttribute('href')).toBe('#/task-session/fix-r-5/recipe-summarize-1');
    });

    test('a click waiting for its run says so', async () => {
        await renderRow({ ...issue, launching: { fix: 'queued' } });
        expect(findButton('Fix')).toBeUndefined();
        expect(container.querySelector('span[title^="Fix: queued — "]')).toBeTruthy();
    });

    test('a done or failed recipe is offered again', async () => {
        await renderRow({ ...issue, sessions: [run('fix', 'Fix', 'failed'), run('summarize', 'Summarize', 'done')] });
        expect(findButton('Fix again')).toBeDefined();
        expect(findButton('Summarize again')).toBeDefined();
        expect(container.textContent).not.toContain('Fix: failed');
        expect(chip('Fix: failed')).toBeDefined();
    });

    test('a failed run with an error opens it', async () => {
        await renderRow({ ...issue, error: 'the agent gave up', sessions: [run('fix', 'Fix', 'failed')] });
        expect(container.textContent).not.toContain('the agent gave up');
        await act(async () => { container.querySelector('span[title^="Fix: failed — show why the run stopped"]').click(); });
        expect(container.textContent).toContain('the agent gave up');
    });

    test('a ready draft, of any recipe, is one chip to its session', async () => {
        await renderRow({ ...issue, sessions: [run('triage', 'Triage', 'ready'), run('plan', 'Plan', 'ready'), run('summarize', 'Summarize', 'ready')] });
        for (const label of ['Triage', 'Plan', 'Summarize']) {
            expect(findButton(label)).toBeUndefined();
            const links = Array.from(container.querySelectorAll('a')).filter(a => a.textContent.includes(label));
            expect(links.map(a => a.textContent)).toEqual([`${label} ↗`]);
        }
        expect(chip('Plan: ready').getAttribute('href')).toBe('#/task-session/fix-r-5/recipe-plan-1');
        expect(container.querySelectorAll('tr').length).toBe(1);
    });

    test('a chip is its recipe in its status colour; the status and the run are its hover', async () => {
        await renderRow({ ...issue, sessions: [
            { ...run('plan', 'Plan', 'done'), startedAt: '2026-10-06T10:00:00Z', endedAt: '2026-10-06T10:05:00Z', applied: { comment: '2026-10-06T11:00:00Z' } },
        ] });
        const plan = chip('Plan: done');
        expect(plan.textContent).toBe('Plan ↗');
        const title = plan.getAttribute('title').split('\n');
        expect(title[0]).toBe('Plan: done — continue the conversation');
        expect(title).toContain('applied: comment');
        expect(title).toContain('sandbox: fix-r-5');
        expect(title.some(l => l.startsWith('started '))).toBe(true);
        expect(title.some(l => l.startsWith('ended '))).toBe(true);
        expect(plan.querySelector('span').style.color).toBe('rgb(34, 134, 58)');
    });

    test('an issue is not promoted, its PR is', async () => {
        await renderRow({ ...issue, draftPR: true, mine: true });
        expect(findButton('Promote PR')).toBeUndefined();
        await renderRow({ type: 'pull', number: 6, group: 'prs', mine: true, draftPR: true, title: 'fix',
            htmlURL: 'https://github.com/o/r/pull/6', updatedAt: '2026-10-06T10:00:00Z' });
        expect(findButton('Promote PR')).toBeDefined();
    });

    test('an issue with an open PR offers nothing more, and links the PR', async () => {
        await renderRow({ ...issue, recipes: undefined, prURL: 'https://github.com/o/r/pull/6' });
        expect(container.querySelectorAll('button').length).toBe(0);
        expect(chip('Fix: done').getAttribute('href')).toBe('https://github.com/o/r/pull/6');
        expect(link('Fix ✓')).toBeUndefined();
    });

    test('an issue whose fix run is recorded shows the run, not the PR fact', async () => {
        await renderRow({ ...issue, recipes: undefined, prURL: 'https://github.com/o/r/pull/6', sessions: [run('fix', 'Fix', 'done')] });
        const fixes = Array.from(container.querySelectorAll('a')).filter(a => (a.getAttribute('title') || '').startsWith('Fix:'));
        expect(fixes.map(a => a.getAttribute('href'))).toEqual(['#/task-session/fix-r-5/recipe-fix-1']);
    });
});

describe('SessionSlideOver', () => {
    test('shows the session with a pop out, and Escape closes it', async () => {
        global.fetch = jest.fn(() => Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve({}), text: () => Promise.resolve('') }));
        const onClose = jest.fn();
        await act(async () => {
            root.render(<SessionSlideOver session={{ sandbox: 'fix-r-5', task: 'recipe-summarize-1' }} onClose={onClose} />);
        });
        await flush();
        const dialog = container.querySelector('[role="dialog"]');
        expect(dialog.getAttribute('aria-label')).toBe('summarize · fix-r-5');
        expect(dialog.style.width).toBe('80%');
        const popOut = Array.from(container.querySelectorAll('a')).find(a => a.textContent.includes('Pop out'));
        expect(popOut.getAttribute('href')).toBe('#/task-session/fix-r-5/recipe-summarize-1');
        expect(popOut.getAttribute('target')).toBe('_blank');
        act(() => { window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' })); });
        expect(onClose).toHaveBeenCalled();
    });
});
