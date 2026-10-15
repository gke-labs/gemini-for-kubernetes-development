import React from 'react';
import { createRoot } from 'react-dom/client';
import { act as domAct } from 'react-dom/test-utils';

jest.mock('react-markdown', () => ({ children }) => children);
jest.mock('remark-gfm', () => 'gfm-plugin-stub');
jest.mock('xterm', () => ({ Terminal: class { open() {} write() {} dispose() {} onData() {} loadAddon() {} } }));
jest.mock('xterm-addon-fit', () => ({ FitAddon: class { fit() {} } }));

const OverseerWork = require('./OverseerWork').default;
const { matchesWork, rowChips, groupOf, WorkRow, cooldownTitle } = require('./OverseerWork');

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
const sectionLabels = () => Array.from(container.querySelectorAll('tr.work-section')).map(r => r.textContent.trim());
const fetched = () => global.fetch.mock.calls.map(c => c[0]);
const button = (text) => Array.from(container.querySelectorAll('button')).find(b => b.textContent.includes(text));

describe('OverseerWork', () => {
    test('sections in order, idle collapsed, an issue with the PR its fix opened', async () => {
        await render();
        expect(sectionLabels()).toEqual(['▾ Needs attention (1)', '▾ Working (1)', '▸ Idle (1)', '▾ Agents (1)']);
        // The board's row: number, age, title cell, agent cell, rail.
        const row = container.querySelector('[data-testid="row-issue-13870"]');
        expect(row.querySelectorAll('td').length).toBe(5);
        expect(row.querySelector('td.work-num').textContent).toBe('◉ #13870');
        expect(row.textContent).toContain('↳ PR #13873');
        expect(row.textContent).toContain('⦿ coder-bot');
        // One Care chip for the care session: its queued rebase, rank 2;
        // the fix's chip; the sandbox icon.
        expect(row.textContent).toContain('Care #2');
        expect(row.textContent).toContain('Fix');
        expect(row.querySelectorAll('img[alt="sandbox"]').length).toBe(1);
        expect(container.querySelector('[data-testid="row-issue-10588"]')).toBeNull();
        await act(async () => { Array.from(container.querySelectorAll('tr.work-section')).find(r => r.textContent.includes('Idle')).click(); });
        expect(container.querySelector('[data-testid="row-issue-10588"]').textContent).toContain('workflow');
    });

    test('a run opens the shared slide-over on the overseer; a classic one, the card', async () => {
        await render();
        const issue = container.querySelector('[data-testid="row-issue-13870"]');
        await act(async () => { Array.from(issue.querySelectorAll('button')).find(b => b.textContent.includes('Fix')).click(); });
        await flush();
        expect(fetched()).toContain('/api/overseers/kcc/task-sessions/fix-kcc-13870/recipe-fix-1');
        expect(container.querySelector('[role="dialog"]')).not.toBeNull();

        act(() => { window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' })); });
        global.fetch.mockClear();
        const pr = container.querySelector('[data-testid="row-pr-13900"]');
        await act(async () => { Array.from(pr.querySelectorAll('button')).find(b => b.textContent.includes('Care')).click(); });
        await flush();
        expect(fetched()).toContain('/api/overseers/kcc/sandbox-card/fix-kcc-13900');
    });

    test('a sandbox opens its card', async () => {
        await render();
        global.fetch.mockClear();
        await act(async () => { container.querySelector('[data-testid="row-issue-13870"] img[alt="sandbox"]').click(); });
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
        await act(async () => { root.render(<Overseer />); });
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

describe('rowChips', () => {
    test('one chip per session group, its newest run; a queued run of a resting group', () => {
        const chips = rowChips({
            sessions: [
                { recipe: 'care-ci', status: 'failed', sandbox: 's', task: 't2' },
                { recipe: 'care-comments', status: 'done', sandbox: 's', task: 't1' },
                { recipe: 'fix', status: 'done', sandbox: 's', task: 't0' },
            ],
            queued: [{ fileName: 'f', type: 'pr-review', recipe: 'review', state: 'incoming', rank: 1 }],
        });
        expect(chips.map(c => `${c.label}:${c.status}`)).toEqual(['Care:failed', 'Fix:done', 'Review:queued']);
        expect(chips[0].run.task).toBe('t2');
    });
    test('groupOf', () => {
        expect(groupOf('care-rebase')).toEqual({ group: 'care', label: 'Care' });
        expect(groupOf('fanout')).toEqual({ group: 'fanout', label: 'Fan out' });
        expect(groupOf('summarize')).toEqual({ group: 'summarize', label: 'Summarize' });
    });
});

describe('a cooldown', () => {
    const cooldown = { until: '2026-10-15T19:16:02Z', workflow: 'kcc-greenfield', lastStatus: 'Failed', lastCompletedAt: '2026-10-15T18:56:02Z' };

    test('is a "cooldown until" label on its row, explained on hover', async () => {
        const el = document.createElement('div');
        document.body.appendChild(el);
        const root = createRoot(el);
        await act(async () => {
            root.render(<table><tbody><WorkRow item={{ key: 'issue-13996', kind: 'issue', number: 13996, workflow: true, cooldown, sandboxes: [], sessions: [] }}
                onOpenSession={() => {}} onOpenSandbox={() => {}} /></tbody></table>);
        });
        const label = Array.from(el.querySelectorAll('span[title]')).find(s => s.textContent.startsWith('cooldown until'));
        expect(label).toBeDefined();
        expect(label.getAttribute('title')).toContain('a failed run waits 20 minutes');
        await act(async () => { root.unmount(); });
        el.remove();
    });

    test('says what it waits out', () => {
        expect(cooldownTitle(cooldown)).toMatch(/^Not queued again before .*; last run failed at .*; a failed run waits 20 minutes$/);
        expect(cooldownTitle({ ...cooldown, lastStatus: 'Completed' })).toContain("workflow kcc-greenfield's cooldown");
        expect(cooldownTitle({ until: cooldown.until })).toContain("fix's cooldown");
    });

    test('is absent without one', async () => {
        const el = document.createElement('div');
        const root = createRoot(el);
        await act(async () => {
            root.render(<table><tbody><WorkRow item={{ key: 'issue-1', kind: 'issue', number: 1, sandboxes: [], sessions: [] }}
                onOpenSession={() => {}} onOpenSandbox={() => {}} /></tbody></table>);
        });
        expect(el.textContent).not.toContain('cooldown');
        await act(async () => { root.unmount(); });
    });
});
