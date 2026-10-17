import React from 'react';
import { createRoot } from 'react-dom/client';
import { act as domAct } from 'react-dom/test-utils';

jest.mock('react-markdown', () => ({ children }) => children);
jest.mock('remark-gfm', () => 'gfm-plugin-stub');
jest.mock('xterm', () => ({ Terminal: class { open() {} write() {} dispose() {} onData() {} loadAddon() {} } }));
jest.mock('xterm-addon-fit', () => ({ FitAddon: class { fit() {} } }));

const SessionSlideOver = require('./SessionSlideOver').default;
const SandboxCard = require('./SandboxCard').default;
const { taskSessionHash } = require('./Research');

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
    global.fetch = jest.fn(() => Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve({}), text: () => Promise.resolve('') }));
});

afterEach(() => {
    act(() => root.unmount());
    container.remove();
    delete global.fetch;
    jest.useRealTimers();
});

const fetched = () => global.fetch.mock.calls.map(c => c[0]);

// The board's slide-over and card, pointed at an overseer's routes
// (docs/design/overseer-work-view.md): the same components, apiBase.
describe('shared with the Overseer page', () => {
    test('the slide-over asks the overseer for its session, and pops out under it', async () => {
        await act(async () => {
            root.render(<SessionSlideOver session={{ sandbox: 'fix-r-5', task: 'recipe-fix-1' }} onClose={() => {}} apiBase="/api/overseers/kcc" />);
        });
        await flush();
        expect(fetched()).toContain('/api/overseers/kcc/task-sessions/fix-r-5/recipe-fix-1');
        expect(fetched().some(u => u.startsWith('/api/task-sessions/'))).toBe(false);
        const popOut = container.querySelector('a[aria-label="Open in a new tab"]');
        expect(popOut.getAttribute('href')).toBe('#/overseers/kcc/task-session/fix-r-5/recipe-fix-1');
    });

    test('the slide-over on the board is unchanged', async () => {
        await act(async () => {
            root.render(<SessionSlideOver session={{ sandbox: 'fix-r-5', task: 'recipe-fix-1' }} onClose={() => {}} />);
        });
        await flush();
        expect(fetched()).toContain('/api/task-sessions/fix-r-5/recipe-fix-1');
    });

    test('the card asks the overseer for its sandbox; on the board, the member', async () => {
        await act(async () => {
            root.render(<SandboxCard name="fix-r-5" namespace="overseer-kcc" onClose={() => {}} apiBase="/api/overseers/kcc" />);
        });
        await flush();
        expect(fetched()).toEqual(['/api/overseers/kcc/sandbox-card/fix-r-5']);

        global.fetch.mockClear();
        await act(async () => {
            root.render(<SandboxCard name="fix-r-6" namespace="alice" onClose={() => {}} />);
        });
        await flush();
        expect(fetched()).toEqual(['/api/sandbox-card/fix-r-6']);
    });

    test('taskSessionHash', () => {
        expect(taskSessionHash({ sandbox: 's', task: 't' })).toBe('#/task-session/s/t');
        expect(taskSessionHash({ sandbox: 's', task: 't' }, '/api/overseers/kcc')).toBe('#/overseers/kcc/task-session/s/t');
    });
});

// An overseer's run's result: read-only (no actions), with when the
// overseer applied it.
test('an overseer run\'s result shows when it was applied', async () => {
    global.fetch = jest.fn((url) => {
        if (url === '/api/overseers/kcc/task-sessions/fix-r-5/recipe-care-ci-9') {
            return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({
                sessionId: 'recipe-care-ci-9', task: 'recipe-care-ci-9', sandbox: 'fix-r-5', namespace: 'overseer-kcc', live: false,
                draft: { kind: 'Change', markdown: '', spec: 'report: unit-tests failed', appliedAt: '2026-10-12T12:30:00Z', actions: [] },
            }) });
        }
        return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve({}), text: () => Promise.resolve('') });
    });
    await act(async () => {
        root.render(<SessionSlideOver session={{ sandbox: 'fix-r-5', task: 'recipe-care-ci-9' }} onClose={() => {}} apiBase="/api/overseers/kcc" />);
    });
    await flush();
    await flush();
    expect(container.textContent).toContain('Change draft');
    expect(container.textContent).toContain('applied ');
    expect(Array.from(container.querySelectorAll('button')).map(b => b.textContent)).not.toContain('Post replies');
});

// A failed apply shows beside the draft, red, with why on hover.
test('an overseer run whose apply failed says so', async () => {
    global.fetch = jest.fn((url) => {
        if (url === '/api/overseers/kcc/task-sessions/fix-r-5/recipe-care-comments-3') {
            return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({
                sessionId: 'recipe-care-comments-3', task: 'recipe-care-comments-3', sandbox: 'fix-r-5', namespace: 'overseer-kcc', live: false,
                draft: { kind: 'Change', markdown: '', spec: 'replies: []', actions: [],
                    applyFailed: { action: 'post-replies', at: '2026-10-12T12:40:00Z', error: 'replying to comment 7: neither a review comment nor a comment' } },
            }) });
        }
        return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve({}), text: () => Promise.resolve('') });
    });
    await act(async () => {
        root.render(<SessionSlideOver session={{ sandbox: 'fix-r-5', task: 'recipe-care-comments-3' }} onClose={() => {}} apiBase="/api/overseers/kcc" />);
    });
    await flush();
    await flush();
    expect(container.textContent).toContain('apply failed: replying to comment 7');
    const chip = Array.from(container.querySelectorAll('span[title]')).find(s => s.textContent.startsWith('apply failed'));
    expect(chip.title).toContain('applying post-replies failed');
    expect(chip.title).toContain('neither a review comment nor a comment');
});
