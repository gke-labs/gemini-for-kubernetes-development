import React from 'react';
import { createRoot } from 'react-dom/client';
import { act as domAct } from 'react-dom/test-utils';

jest.mock('react-markdown', () => ({ children }) => children);
jest.mock('remark-gfm', () => 'gfm-plugin-stub');
jest.mock('xterm', () => ({ Terminal: class { open() {} write() {} dispose() {} onData() {} loadAddon() {} } }));
jest.mock('xterm-addon-fit', () => ({ FitAddon: class { fit() {} } }));

const OverseerWork = require('./OverseerWork').default;
const { matchesWork } = require('./OverseerWork');

const act = React.act || domAct;
global.IS_REACT_ACT_ENVIRONMENT = true;
const flush = () => act(async () => { await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); });

const KCC = 'https://github.com/GoogleCloudPlatform/k8s-config-connector';

// FEED is a /work answer shaped like staging's.
const FEED = {
    queueError: '',
    items: [
        {
            key: 'issue-13870', kind: 'issue', number: 13870, pr: 13873, htmlUrl: `${KCC}/issues/13870`, prUrl: `${KCC}/pull/13873`,
            section: 'attention', reason: 'care-ci failed', updatedAt: '2026-10-12T12:00:00Z',
            sandboxes: [{ name: 'fix-kcc-13870', pod: 'paused', user: 'coder-bot', replicas: 0 }],
            sessions: [
                { recipe: 'care-ci', sandbox: 'fix-kcc-13870', task: 'recipe-care-ci-2', status: 'failed', run: 'r1' },
                { recipe: 'fix', sandbox: 'fix-kcc-13870', task: 'recipe-fix-1', status: 'done', run: 'r2' },
            ],
            queued: [{ fileName: 'task-pr-13873-iterate.yaml', type: 'pr-iterate', recipe: 'care-rebase', state: 'incoming', rank: 2 }],
        },
        {
            key: 'pr-13900', kind: 'pr', number: 13900, htmlUrl: `${KCC}/pull/13900`, section: 'working',
            sandboxes: [{ name: 'fix-kcc-13900', pod: 'running' }],
            sessions: [{ recipe: 'care-comments', sandbox: 'fix-kcc-13900', status: 'running' }],
        },
        {
            key: 'issue-10588', kind: 'issue', number: 10588, workflow: true, section: 'idle',
            sandboxes: [{ name: 'wf-issue-10588', pod: 'paused' }], sessions: [{ recipe: 'workflow', sandbox: 'wf-issue-10588', status: 'done' }],
        },
        {
            key: 'sandbox-agent-kcc-release', kind: 'agent', section: 'agents',
            sandboxes: [{ name: 'agent-kcc-release', pod: 'paused' }], sessions: [{ recipe: 'agent', sandbox: 'agent-kcc-release', status: 'done' }],
        },
    ],
};

let container;
let root;

beforeEach(() => {
    jest.useFakeTimers();
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    global.fetch = jest.fn((url) => {
        if (url === '/api/overseers/kcc/work') {
            return Promise.resolve({ ok: true, json: () => Promise.resolve(FEED) });
        }
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
    await act(async () => { root.render(<OverseerWork overseer="kcc" />); });
    await flush();
};
const sectionLabels = () => Array.from(container.querySelectorAll('section > button')).map(b => b.textContent.trim());
const fetched = () => global.fetch.mock.calls.map(c => c[0]);
const button = (text) => Array.from(container.querySelectorAll('button')).find(b => b.textContent.includes(text));

describe('OverseerWork', () => {
    test('sections in order, idle collapsed, an issue with the PR its fix opened', async () => {
        await render();
        expect(sectionLabels()).toEqual(['▾ NEEDS ATTENTION (1)', '▾ WORKING (1)', '▸ IDLE (1)', '▾ AGENTS (1)']);
        const row = container.querySelector('[data-testid="row-issue-13870"]');
        expect(row.textContent).toContain('#13870');
        expect(row.textContent).toContain('PR #13873');
        expect(row.textContent).toContain('care-ci failed');
        expect(row.textContent).toContain('care-rebase ⧗ #2');
        expect(container.querySelector('[data-testid="row-issue-10588"]')).toBeNull();
        await act(async () => { button('IDLE').click(); });
        expect(container.querySelector('[data-testid="row-issue-10588"]').textContent).toContain('workflow');
    });

    test('a run opens the shared slide-over on the overseer; a classic one, the card', async () => {
        await render();
        await act(async () => { button('care-ci ✗').click(); });
        await flush();
        expect(fetched()).toContain('/api/overseers/kcc/task-sessions/fix-kcc-13870/recipe-care-ci-2');
        expect(container.querySelector('[role="dialog"]')).not.toBeNull();

        act(() => { window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' })); });
        global.fetch.mockClear();
        await act(async () => { button('care-comments ●').click(); });
        await flush();
        expect(fetched()).toContain('/api/overseers/kcc/sandbox-card/fix-kcc-13900');
    });

    test('a sandbox opens its card', async () => {
        await render();
        global.fetch.mockClear();
        await act(async () => { button('fix-kcc-13870').click(); });
        await flush();
        expect(fetched()).toContain('/api/overseers/kcc/sandbox-card/fix-kcc-13870');
    });

    test('the queue unreachable is said', async () => {
        FEED.queueError = 'the watch\'s queue is unreachable';
        await render();
        expect(container.textContent).toContain('Queue not shown: the watch\'s queue is unreachable');
        FEED.queueError = '';
    });
});

describe('matchesWork', () => {
    const [issue, pr, wf, agent] = FEED.items;
    test('type', () => {
        expect(matchesWork(issue, { type: 'pr' })).toBe(true); // its fix opened a PR
        expect(matchesWork(pr, { type: 'issue' })).toBe(false);
        expect(matchesWork(agent, { type: 'issue' })).toBe(false);
    });
    test('state', () => {
        expect(matchesWork(pr, { state: 'running' })).toBe(true);
        expect(matchesWork(issue, { state: 'running' })).toBe(false);
        expect(matchesWork(wf, { state: 'paused' })).toBe(true);
        expect(matchesWork(issue, { state: 'failed' })).toBe(true);
        expect(matchesWork(pr, { state: 'failed' })).toBe(false);
    });
    test('search', () => {
        expect(matchesWork(issue, { q: '13873' })).toBe(true);
        expect(matchesWork(issue, { q: 'care-rebase' })).toBe(true);
        expect(matchesWork(wf, { q: 'wf-issue' })).toBe(true);
        expect(matchesWork(agent, { q: 'nope' })).toBe(false);
    });
});

describe('the Overseer page', () => {
    test('opens on Work; Queue, Logs and Keys behind the header', async () => {
        const Overseer = require('./Overseer').default;
        global.fetch = jest.fn((url) => {
            if (url === '/api/overseers') return Promise.resolve({ ok: true, json: () => Promise.resolve([{ metadata: { name: 'kcc' }, spec: { repoURL: KCC } }]) });
            if (url === '/api/overseers/kcc/work') return Promise.resolve({ ok: true, json: () => Promise.resolve(FEED) });
            return Promise.resolve({ ok: true, json: () => Promise.resolve({}), text: () => Promise.resolve('') });
        });
        await act(async () => { root.render(<Overseer onBack={() => {}} />); });
        await flush();
        await flush();
        const tabs = Array.from(container.querySelectorAll('button')).map(b => b.textContent.trim());
        expect(tabs.some(t => t.startsWith('🧭 Work'))).toBe(true);
        expect(tabs.some(t => t.startsWith('📥 Queue'))).toBe(true);
        expect(tabs).toContain('📜 Logs');
        expect(tabs).toContain('🔑 Keys');
        expect(container.querySelector('[data-testid="row-issue-13870"]')).not.toBeNull();
        expect(container.textContent).not.toContain('Back to Sandboxes Table');
    });
});
