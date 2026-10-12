import React from 'react';
import { createRoot } from 'react-dom/client';
import { act as domAct } from 'react-dom/test-utils';

jest.mock('react-markdown', () => ({ children }) => children);
jest.mock('remark-gfm', () => 'gfm-plugin-stub');
jest.mock('xterm', () => ({ Terminal: class { open() {} write() {} dispose() {} onData() {} loadAddon() {} } }));
jest.mock('xterm-addon-fit', () => ({ FitAddon: class { fit() {} } }));

const Sessions = require('./Sessions').default;
const { sectionsOf, groupsOf, visibleRows, rowDoing, rowKey, sessionsRoute } = require('./Sessions');

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
    test('the box and the list across the page, sections by state, Recent collapsed', async () => {
        await render();
        expect(container.querySelector('textarea[aria-label="Start a session"]')).not.toBeNull();
        const text = container.textContent;
        expect(text).toContain('Needs you (1)');
        expect(text).toContain('Working (2)');
        expect(text).toContain('Idle · live (1)');
        expect(text).toContain('reconciler deletes');
        expect(text).toContain('⚠ asking permission');
        expect(text).toContain('starting sandbox…');
        // The Fix done today is under Recent, collapsed; the 2020 one is gone.
        expect(text).toContain('(1)▸ show');
        expect(container.querySelector('[role="separator"]')).toBeNull();
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
        // Escape closes it and the list is the page again.
        act(() => { window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' })); });
        expect(container.querySelector('[role="separator"]')).toBeNull();
        expect(window.location.hash).toBe('#/sessions');
    });

    test('the list collapses to a strip of marks', async () => {
        await render();
        await act(async () => { button('Rebase').click(); });
        await act(async () => { container.querySelector('[aria-label="Collapse the list"]').click(); });
        expect(container.querySelector('[aria-label="Show the list"]')).not.toBeNull();
        expect(container.textContent).not.toContain('Needs you (1)');
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
        expect(JSON.parse(global.fetch.mock.calls.find(c => c[0] === '/api/board/g4k/research')[1].body)).toEqual({ kind: 'topic', topic: 'Overview of g4k' });
        expect(window.location.hash).toBe('#/sessions/research/fresh');
        expect(container.querySelector('[role="separator"]')).not.toBeNull();
    });
});

describe('helpers', () => {
    test('sections and groups', () => {
        const sections = sectionsOf(FEED.sessions);
        expect(sections.map(s => `${s.id}:${s.rows.length}`)).toEqual(['needs-you:1', 'working:2', 'idle:1', 'recent:1']);
        const bySandbox = groupsOf(FEED.sessions, 'sandbox');
        expect(bySandbox[0].id).toBe('fix-g4k-1774');
        expect(bySandbox[0].rows.map(r => r.task)).toEqual(['recipe-care-rebase-3', 'recipe-fix-1']);
        expect(groupsOf(FEED.sessions, 'repo').map(g => g.id)).toEqual(['gke-labs/g4k', 'G/kcc']);
    });
    test('filters', () => {
        expect(visibleRows(FEED.sessions, { state: 'needs-you' }).length).toBe(1);
        expect(visibleRows(FEED.sessions, { state: 'live' }).length).toBe(4);
        expect(visibleRows(FEED.sessions, { query: '#1681' })[0].task).toBe('recipe-triage-1');
        expect(visibleRows(FEED.sessions, { repo: 'G/kcc' }).length).toBe(3);
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
