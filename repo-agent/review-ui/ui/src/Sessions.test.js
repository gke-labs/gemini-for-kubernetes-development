import React from 'react';
import { createRoot } from 'react-dom/client';
import { act as domAct } from 'react-dom/test-utils';

jest.mock('react-markdown', () => ({ children }) => children);
jest.mock('remark-gfm', () => 'gfm-plugin-stub');
jest.mock('xterm', () => ({ Terminal: class { open() {} write() {} dispose() {} onData() {} loadAddon() {} } }));
jest.mock('xterm-addon-fit', () => ({ FitAddon: class { fit() {} } }));

const Sessions = require('./Sessions').default;
const { orderedRows, visibleRows, rowDoing, rowKey, sessionsRoute } = require('./Sessions');

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
