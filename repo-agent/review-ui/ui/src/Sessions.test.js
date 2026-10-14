import React from 'react';
import { createRoot } from 'react-dom/client';
import { act as domAct } from 'react-dom/test-utils';

jest.mock('react-markdown', () => ({ children }) => children);
jest.mock('remark-gfm', () => 'gfm-plugin-stub');
jest.mock('xterm', () => ({ Terminal: class { open() {} write() {} dispose() {} onData() {} loadAddon() {} } }));
jest.mock('xterm-addon-fit', () => ({ FitAddon: class { fit() {} } }));

const Sessions = require('./Sessions').default;
const { apiBaseOf, parseItemRef, refBoard, itemSuggestions, pickSuggestion, orderedRows, visibleRows, rowDoing, rowKey, sessionsRoute } = require('./Sessions');

const act = React.act || domAct;
global.IS_REACT_ACT_ENVIRONMENT = true;
const flush = () => act(async () => { await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); });

const now = new Date().toISOString();
const old = '2020-01-01T00:00:00Z';

// FEED is a /api/sessions answer: a PR's sandbox with two sessions, a
// research conversation asking for a permission, an idle triage, a
// requested research session and a long-finished fix.
const FEED = {
    sessions: [
        {
            sandbox: 'fix-g4k-1774', task: 'recipe-care-rebase-3', repo: 'gke-labs/g4k', item: 'pr', number: 1774,
            htmlURL: 'https://github.com/gke-labs/g4k/pull/1774', label: 'Rebase', recipe: 'care-rebase', session: 'care',
            engine: 'gemini', pod: 'running', live: true, busy: true, state: 'working', updatedAt: now,
            runs: [{ recipe: 'care-rebase', label: 'Rebase', status: 'running', run: 'r3' }, { recipe: 'care-ci', label: 'Fix CI', status: 'done', run: 'r2' }],
        },
        {
            sandbox: 'fix-g4k-1774', task: 'recipe-fix-1', repo: 'gke-labs/g4k', item: 'pr', number: 1774,
            label: 'Fix', recipe: 'fix', pod: 'running', state: 'done', updatedAt: now, runs: [{ recipe: 'fix', status: 'done', run: 'r1' }],
        },
        {
            sandbox: 'rsch-kcc-3f2a', task: 'recipe-research-abc', research: 'abc', repo: 'G/kcc', title: 'reconciler deletes',
            recipe: 'research', label: 'Research', pod: 'running', live: true, busy: true, waiting: true, state: 'needs-you', updatedAt: now,
            runs: [{ recipe: 'research', status: 'done', run: 'r' }],
        },
        {
            sandbox: 'fix-g4k-1681', task: 'recipe-triage-1', repo: 'gke-labs/g4k', item: 'issue', number: 1681,
            label: 'Triage', recipe: 'triage', pod: 'running', live: true, state: 'idle', updatedAt: now, runs: [{ recipe: 'triage', status: 'done' }],
        },
        { sandbox: 'rsch-kcc-new', research: 'new1', repo: 'G/kcc', title: 'what changed', recipe: 'research', requested: true, pod: 'starting', state: 'working', updatedAt: now },
        { sandbox: 'fix-kcc-1', task: 'recipe-fix-9', repo: 'G/kcc', item: 'issue', number: 1, label: 'Fix', recipe: 'fix', pod: 'paused', state: 'paused', updatedAt: old, runs: [{ status: 'done' }] },
    ],
};

let container;
let root;

beforeEach(() => {
    jest.useFakeTimers();
    window.history.replaceState(null, '', '/');
    localStorage.clear();
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    global.fetch = jest.fn((url) => {
        if (url === '/api/sessions') return Promise.resolve({ ok: true, json: () => Promise.resolve(FEED) });
        if (url === '/api/boards') return Promise.resolve({ ok: true, json: () => Promise.resolve([{ name: 'g4k', repoURL: 'https://github.com/gke-labs/g4k' }]) });
        if (url === '/api/board/g4k/research/prompts') return Promise.resolve({ ok: true, json: () => Promise.resolve({ prompts: { onboard: 'Overview of g4k', activity: 'Last month' } }) });
        return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve({}), text: () => Promise.resolve('') });
    });
});

afterEach(() => {
    act(() => root.unmount());
    container.remove();
    delete global.fetch;
    jest.useRealTimers();
});

const render = async () => {
    await act(async () => { root.render(<Sessions namespace="alice" />); });
    await flush();
};
const fetched = () => global.fetch.mock.calls.map(c => c[0]);
const button = (text) => Array.from(container.querySelectorAll('button')).find(b => b.textContent.includes(text));

describe('Sessions', () => {
    test('opens split on + New: the list on the left, the start box focused on the right', async () => {
        await render();
        const box = container.querySelector('textarea[aria-label="Start a session"]');
        expect(box).not.toBeNull();
        expect(document.activeElement).toBe(box);
        expect(container.querySelector('[role="separator"]')).not.toBeNull();
        // + New is the list's first row, under the search, and is the one open.
        const newRow = button('New session');
        expect(newRow.getAttribute('aria-current')).toBe('true');
        expect(container.querySelector('input[aria-label="Search sessions"]').compareDocumentPosition(newRow) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
        const text = container.textContent;
        // No section headers: red, then blue, then green, then grey, the
        // newest first in each; the 2020 fix is not listed.
        expect(text).not.toContain('Needs you (');
        const rows = Array.from(container.querySelectorAll('button[title]')).filter(b => b.title.includes(' · '));
        expect(rows.map(b => b.title.split(' · ')[0])).toEqual(['reconciler deletes', 'Rebase', 'what changed', 'Triage', 'Fix']);
        const dot = (b) => b.querySelector('span').style.color;
        expect(rows.map(dot)).toEqual(['rgb(215, 58, 73)', 'rgb(9, 105, 218)', 'rgb(9, 105, 218)', 'rgb(34, 134, 58)', 'rgb(139, 148, 158)']);
        expect(container.querySelector('[aria-label="Group by"]')).toBeNull();
        expect(container.querySelector('[aria-label="State filter"]')).toBeNull();
        expect(container.querySelector('[aria-label="Repository filter"]')).toBeNull();
        expect(container.querySelector('[aria-label="Legend"]').textContent).toBe('● needs you● working● idle● paused · done1 need you');
        // One line a row: state, engine, recipe, item, repo, age; what the
        // agent is doing and the sandbox are its hover.
        const rebase = Array.from(container.querySelectorAll('button')).find(b => b.textContent.startsWith('●Rebase'));
        expect(rebase.textContent).toBe('●Rebase#1774g4k0m');
        // One size for all of it, no link: the item's link is in the strip.
        expect(rebase.style.fontSize).toBe('x-small');
        expect(rebase.querySelector('a')).toBeNull();
        expect(rebase.title).toBe('Rebase · working · fix-g4k-1774');
        const asking = Array.from(container.querySelectorAll('button')).find(b => b.textContent.includes('reconciler deletes'));
        expect(asking.title).toBe('reconciler deletes · ⚠ asking permission · rsch-kcc-3f2a');
        expect(text).not.toContain('fix-g4k-1774');
        expect(text).toContain('1 need you');
    });

    test('a row opens the session on the right, with its runs and the sandbox\'s other session', async () => {
        await render();
        await act(async () => { button('Rebase').click(); });
        await flush();
        expect(fetched()).toContain('/api/task-sessions/fix-g4k-1774/recipe-care-rebase-3');
        expect(window.location.hash).toBe('#/sessions/fix-g4k-1774/recipe-care-rebase-3');
        expect(container.querySelector('[role="separator"]')).not.toBeNull();
        expect(container.textContent).toContain('runs: Fix CI');
        expect(container.textContent).toContain('also here: Fix');
        expect(container.querySelector('textarea[aria-label="Start a session"]')).toBeNull();
        // Escape goes back to + New.
        act(() => { window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' })); });
        expect(container.querySelector('textarea[aria-label="Start a session"]')).not.toBeNull();
        expect(window.location.hash).toBe('#/sessions');
    });

    test('↑ from the first row is + New', async () => {
        await render();
        act(() => { document.body.focus(); window.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown' })); });
        expect(window.location.hash).toBe('#/sessions/research/abc');
        act(() => { window.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowUp' })); });
        expect(window.location.hash).toBe('#/sessions');
    });

    test('the list collapses to a strip of dots, + on top', async () => {
        await render();
        await act(async () => { button('Rebase').click(); });
        await act(async () => { container.querySelector('[aria-label="Collapse the list"]').click(); });
        expect(container.querySelector('[aria-label="Show the list"]')).not.toBeNull();
        expect(container.textContent).not.toContain('Triage');
        await act(async () => { container.querySelector('[aria-label="New session"]').click(); });
        expect(container.querySelector('textarea[aria-label="Start a session"]')).not.toBeNull();
    });

    test('starting a session opens it pending', async () => {
        global.fetch.mockImplementationOnce(() => Promise.resolve({ ok: true, json: () => Promise.resolve(FEED) }));
        await render();
        const box = container.querySelector('textarea[aria-label="Start a session"]');
        await act(async () => { button('overview').click(); });
        expect(box.value).toBe('Overview of g4k');
        global.fetch.mockImplementation((url, opts) => {
            if (url === '/api/board/g4k/research' && opts && opts.method === 'POST') {
                return Promise.resolve({ ok: true, json: () => Promise.resolve({ sessionId: 'fresh', title: 'Overview' }) });
            }
            return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve({}), text: () => Promise.resolve('') });
        });
        await act(async () => { button('Start').click(); });
        await flush();
        // A board with no engine set runs its sessions on gemini.
        expect(JSON.parse(global.fetch.mock.calls.find(c => c[0] === '/api/board/g4k/research')[1].body)).toEqual({ kind: 'topic', topic: 'Overview of g4k', engine: 'gemini' });
        expect(window.location.hash).toBe('#/sessions/research/fresh');
        expect(container.querySelector('[role="separator"]')).not.toBeNull();
    });
});

describe('the engine chip', () => {
    const boardsWith = (engine) => [{ name: 'g4k', repoURL: 'https://github.com/gke-labs/g4k', engine }];
    const startWith = async (engine, pick) => {
        global.fetch = jest.fn((url, opts) => {
            if (url === '/api/sessions') return Promise.resolve({ ok: true, json: () => Promise.resolve({ sessions: [] }) });
            if (url === '/api/boards') return Promise.resolve({ ok: true, json: () => Promise.resolve(boardsWith(engine)) });
            if (url === '/api/board/g4k/research' && opts && opts.method === 'POST') {
                return Promise.resolve({ ok: true, json: () => Promise.resolve({ sessionId: 'x', title: 'q' }) });
            }
            return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve({}), text: () => Promise.resolve('') });
        });
        await act(async () => { root.render(<Sessions namespace="alice" />); });
        await flush();
        const chip = container.querySelector('select[aria-label="Engine"]');
        const setValue = (el, v) => {
            const set = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(el), 'value').set;
            set.call(el, v);
            el.dispatchEvent(new Event(el.tagName === 'SELECT' ? 'change' : 'input', { bubbles: true }));
        };
        const shown = chip.value;
        if (pick) await act(async () => { setValue(chip, pick); });
        await act(async () => { setValue(container.querySelector('textarea[aria-label="Start a session"]'), 'q'); });
        await act(async () => { button('Start').click(); });
        await flush();
        const call = global.fetch.mock.calls.find(c => c[0] === '/api/board/g4k/research');
        return { shown, options: Array.from(chip.options).map(o => o.value), sent: JSON.parse(call[1].body).engine };
    };

    test('defaults to the board\'s engine and offers only what sessions run on', async () => {
        expect(await startWith('antigravity')).toEqual({ shown: 'antigravity', options: ['gemini', 'antigravity'], sent: 'antigravity' });
    });
    test('a claude board\'s sessions default to gemini', async () => {
        expect((await startWith('claude')).sent).toBe('gemini');
    });
    test('the pick is what is sent', async () => {
        expect((await startWith('gemini', 'antigravity')).sent).toBe('antigravity');
    });
});

describe('the item launcher', () => {
    const boards = [
        { name: 'g4k', repoURL: 'https://github.com/gke-labs/g4k' },
        { name: 'kcc', repoURL: 'https://github.com/GoogleCloudPlatform/k8s-config-connector' },
    ];
    const KCC_FEED = [
        {
            type: 'pr', number: 1488, title: 'fix drift', author: 'carol', recipes: [
                { name: 'review', label: 'Review' },
                { name: 'care-ci', label: 'Fix CI', session: 'care' },
                { name: 'care-iterate', label: 'Iterate', session: 'care', inputs: ['instruction'] },
            ],
        },
        {
            type: 'issue', number: 7, title: 'plan me', recipes: [{ name: 'plan', label: 'Plan' }, { name: 'fix', label: 'Fix' }],
            sessions: [{ recipe: 'plan', label: 'Plan', status: 'ready', sandbox: 'fix-kcc-7', task: 'recipe-plan-1' }],
        },
    ];
    let posted;
    const serve = () => {
        posted = [];
        global.fetch = jest.fn((url, opts) => {
            if (url === '/api/sessions') return Promise.resolve({ ok: true, json: () => Promise.resolve({ sessions: [] }) });
            if (url === '/api/boards') return Promise.resolve({ ok: true, json: () => Promise.resolve(boards) });
            if (url === '/api/board/kcc/work') return Promise.resolve({ ok: true, json: () => Promise.resolve(KCC_FEED) });
            if (opts && opts.method === 'POST') { posted.push({ url, body: opts.body }); return Promise.resolve({ ok: true, json: () => Promise.resolve({ sessionId: 's', title: 't' }) }); }
            return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve({}), text: () => Promise.resolve('') });
        });
    };
    const type = async (text) => {
        const box = container.querySelector('textarea[aria-label="Start a session"]');
        await act(async () => {
            const set = Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype, 'value').set;
            set.call(box, text);
            box.dispatchEvent(new Event('input', { bubbles: true }));
        });
        await act(async () => { jest.advanceTimersByTime(300); });
        await flush();
        return box;
    };
    const options = () => Array.from(container.querySelectorAll('[aria-label="Item launcher"] [role="option"]'));
    const press = async (box, key) => { await act(async () => { box.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true })); }); await flush(); };

    test('kcc#1488 offers the PR\'s recipes, the typed word highlighted; Enter starts it on the row', async () => {
        serve();
        await act(async () => { root.render(<Sessions namespace="alice" />); });
        await flush();
        const box = await type('review kcc#1488');
        expect(container.querySelector('[aria-label="Item launcher"]').textContent).toContain('PR #1488 · fix drift · carol');
        // The repository chip follows the reference.
        expect(container.querySelector('select[aria-label="Repository"]').value).toBe('kcc');
        // Iterate needs typed input: it stays on Work.
        expect(options().map(o => o.textContent.replace('⏎', ''))).toEqual(['▸Review', 'Fix CI', 'Ask “review kcc#1488” as a question']);
        await press(box, 'Enter');
        expect(posted).toEqual([{ url: '/api/board/kcc/prs/1488/recipes/review', body: undefined }]);
        expect(container.textContent).toContain('Review is starting on #1488');
    });

    test('a group with a draft offers its session, and ↓ moves to the next', async () => {
        serve();
        await act(async () => { root.render(<Sessions namespace="alice" />); });
        await flush();
        const box = await type('kcc#7');
        expect(options().map(o => o.textContent.replace('⏎', '').replace('▸', ''))).toEqual(['Open Plan session ↗', 'Fix', 'Ask “kcc#7” as a question']);
        await press(box, 'ArrowDown');
        expect(options().map(o => o.getAttribute('aria-selected'))).toEqual(['false', 'true', 'false']);
        await press(box, 'ArrowUp');
        await press(box, 'Enter');
        expect(window.location.hash).toBe('#/sessions/fix-kcc-7/recipe-plan-1');
    });

    test('an item not on the board says so; Enter still asks it as a question', async () => {
        serve();
        await act(async () => { root.render(<Sessions namespace="alice" />); });
        await flush();
        const box = await type('what is kcc#99 about');
        expect(container.textContent).toContain("#99 is not on kcc's board");
        await press(box, 'Enter');
        expect(posted.map(p => p.url)).toEqual(['/api/board/kcc/research']);
        expect(JSON.parse(posted[0].body).topic).toBe('what is kcc#99 about');
    });

    test('no reference, no launcher', async () => {
        serve();
        await act(async () => { root.render(<Sessions namespace="alice" />); });
        await flush();
        await type('how does the reconciler delete?');
        expect(container.querySelector('[aria-label="Item launcher"]')).toBeNull();
        expect(global.fetch).not.toHaveBeenCalledWith('/api/board/kcc/work');
    });
});

describe('overseer sessions', () => {
    const OV = {
        sessions: [
            { sandbox: 'fix-repo-9', task: 'recipe-fix-1', repo: 'gke-labs/g4k', item: 'issue', number: 9, label: 'Fix', recipe: 'fix', pod: 'running', state: 'done', updatedAt: now, runs: [{ status: 'done' }] },
            // kcc's, in a sandbox named like the member's.
            { overseer: 'kcc', sandbox: 'fix-repo-9', task: 'recipe-fix-1', repo: 'G/kcc', item: 'pr', number: 12, label: 'Fix CI', recipe: 'care-ci', pod: 'running', busy: true, state: 'working', updatedAt: now, runs: [{ status: 'running' }] },
        ],
    };
    const serve = () => {
        global.fetch = jest.fn((url) => {
            if (url === '/api/sessions') return Promise.resolve({ ok: true, json: () => Promise.resolve(OV) });
            if (url === '/api/sessions?overseers=false') return Promise.resolve({ ok: true, json: () => Promise.resolve({ sessions: OV.sessions.slice(0, 1) }) });
            if (url === '/api/boards') return Promise.resolve({ ok: true, json: () => Promise.resolve([]) });
            return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve({}), text: () => Promise.resolve('') });
        });
    };
    const rowButtons = () => Array.from(container.querySelectorAll('button[title]')).filter(b => b.title.includes(' · '));

    test('listed with a chip, by default; opening one uses the overseer\'s routes', async () => {
        serve();
        await act(async () => { root.render(<Sessions namespace="alice" />); });
        await flush();
        expect(rowButtons().length).toBe(2);
        const ov = rowButtons().find(b => b.textContent.includes('🤖 kcc'));
        expect(ov.title).toBe("Fix CI · working · kcc's fix-repo-9");
        await act(async () => { ov.click(); });
        await flush();
        expect(global.fetch.mock.calls.map(c => c[0])).toContain('/api/overseers/kcc/task-sessions/fix-repo-9/recipe-fix-1');
        expect(window.location.hash).toBe('#/sessions/overseers/kcc/fix-repo-9/recipe-fix-1');
        // The member's sandbox of the same name is not "also here".
        expect(container.textContent).not.toContain('also here');
    });

    test('the 🤖 Overseers button hides them, reads without them, and is remembered', async () => {
        serve();
        await act(async () => { root.render(<Sessions namespace="alice" />); });
        await flush();
        const filter = button('🤖 Overseers');
        expect(filter.getAttribute('aria-pressed')).toBe('true');
        await act(async () => { filter.click(); });
        await flush();
        expect(button('🤖 Overseers').getAttribute('aria-pressed')).toBe('false');
        expect(rowButtons().some(b => b.textContent.includes('🤖 kcc'))).toBe(false);
        expect(rowButtons().length).toBe(1);
        expect(global.fetch.mock.calls.map(c => c[0])).toContain('/api/sessions?overseers=false');
        expect(localStorage.getItem('sessions.overseers')).toBe('false');
    });

    test('keys, routes and API bases name the overseer', () => {
        const row = { overseer: 'kcc', sandbox: 'fix-repo-9', task: 't' };
        expect(rowKey(row)).toBe('o:kcc/fix-repo-9/t');
        expect(rowKey({ sandbox: 'fix-repo-9', task: 't' })).toBe('fix-repo-9/t');
        expect(sessionsRoute('#/sessions/overseers/kcc/fix-repo-9/t')).toEqual({ key: 'o:kcc/fix-repo-9/t' });
        expect(apiBaseOf(row)).toBe('/api/overseers/kcc');
        expect(apiBaseOf({ sandbox: 's' })).toBe('/api');
    });
});

describe('launcher helpers', () => {
    test('parseItemRef', () => {
        expect(parseItemRef('see https://github.com/o/r/pull/12 pls')).toMatchObject({ owner: 'o', repo: 'r', number: 12 });
        expect(parseItemRef('review gke-labs/g4k#5')).toMatchObject({ owner: 'gke-labs', repo: 'g4k', number: 5 });
        expect(parseItemRef('review kcc#13873')).toMatchObject({ alias: 'kcc', number: 13873 });
        expect(parseItemRef('fix #42 now')).toEqual({ number: 42, text: '#42' });
        expect(parseItemRef('no numbers here, C# is a language')).toBeNull();
    });
    test('refBoard', () => {
        const boards = [{ name: 'g4k', repoURL: 'https://github.com/gke-labs/gemini-for-kubernetes-development' }, { name: 'kcc', repoURL: 'https://github.com/G/k8s-config-connector.git' }];
        expect(refBoard({ alias: 'KCC', number: 1 }, boards, null).name).toBe('kcc');
        expect(refBoard({ alias: 'k8s-config-connector', number: 1 }, boards, null).name).toBe('kcc');
        expect(refBoard({ owner: 'gke-labs', repo: 'gemini-for-kubernetes-development', number: 1 }, boards, null).name).toBe('g4k');
        expect(refBoard({ number: 1 }, boards, boards[1]).name).toBe('kcc');
        expect(refBoard({ alias: 'nope', number: 1 }, boards, boards[0])).toBeNull();
    });
    test('itemSuggestions follows the row rules', () => {
        const mine = { type: 'pr', mine: true, recipes: [{ name: 'review', label: 'Review' }, { name: 'care', label: 'Care', session: 'care' }] };
        expect(itemSuggestions(mine).map(s => s.label)).toEqual(['Care']);
        expect(itemSuggestions(mine, { selfReview: true }).map(s => s.label)).toEqual(['Review', 'Care']);
        const running = { type: 'issue', recipes: [{ name: 'fix', label: 'Fix' }], launching: { fix: 'queued' } };
        expect(itemSuggestions(running)).toEqual([{ kind: 'busy', label: 'Fix queued…', group: 'fix' }]);
        const done = { type: 'issue', recipes: [{ name: 'triage', label: 'Triage' }], sessions: [{ recipe: 'triage', status: 'done', sandbox: 's', task: 't' }] };
        expect(itemSuggestions(done).map(s => s.label)).toEqual(['Triage again']);
        const pending = { type: 'pr', reviewPending: true, recipes: [{ name: 'review', label: 'Review' }] };
        expect(itemSuggestions(pending)).toEqual([]);
    });
    test('pickSuggestion', () => {
        const sg = [{ kind: 'busy', label: 'Fix queued…' }, { kind: 'launch', label: 'Plan', recipe: 'plan' }, { kind: 'launch', label: 'Triage', recipe: 'triage' }];
        expect(pickSuggestion(sg, 'triage #3')).toBe(2);
        expect(pickSuggestion(sg, '#3')).toBe(1);
    });
});

describe('helpers', () => {
    test('order', () => {
        const ordered = orderedRows([...FEED.sessions]);
        expect(ordered.map(r => r.state)).toEqual(['needs-you', 'working', 'working', 'idle', 'done']);
        expect(ordered[1].task).toBe('recipe-care-rebase-3');
    });
    test('filters', () => {
        expect(visibleRows(FEED.sessions, { query: '#1681' })[0].task).toBe('recipe-triage-1');
        expect(visibleRows(FEED.sessions, { query: 'kcc' }).length).toBe(3);
    });
    test('what a row is doing', () => {
        expect(rowDoing({ runs: [{ status: 'ready' }] })).toBe('📝 draft ready');
        expect(rowDoing({ retrying: { attempt: 1 }, busy: true })).toBe('⟳ retrying');
        expect(rowDoing({ runs: [{ status: 'running' }], pod: 'starting' })).toBe('starting…');
    });
    test('keys and routes', () => {
        expect(rowKey({ research: 'x', sandbox: 's', task: 't' })).toBe('r:x');
        expect(rowKey({ sandbox: 's', task: 't' })).toBe('s/t');
        expect(sessionsRoute('#/sessions')).toEqual({ key: '' });
        expect(sessionsRoute('#/sessions/research/abc')).toEqual({ key: 'r:abc' });
        expect(sessionsRoute('#/sessions/fix-a-1/recipe-fix-1')).toEqual({ key: 'fix-a-1/recipe-fix-1' });
        expect(sessionsRoute('#/work')).toBeNull();
    });
});
