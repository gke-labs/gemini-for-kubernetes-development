import React from 'react';
import { createRoot } from 'react-dom/client';
import { act as domAct } from 'react-dom/test-utils';

jest.mock('react-markdown', () => ({ children }) => children);
jest.mock('remark-gfm', () => 'gfm-plugin-stub');
jest.mock('xterm', () => ({ Terminal: class { open() {} write() {} dispose() {} onData() {} loadAddon() {} } }));
jest.mock('xterm-addon-fit', () => ({ FitAddon: class { fit() {} } }));

const App = require('./App').default;

const act = React.act || domAct;
global.IS_REACT_ACT_ENVIRONMENT = true;
const flush = () => act(async () => { await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); });

let container;
let root;

beforeEach(() => {
    jest.useFakeTimers();
    window.history.replaceState(null, '', '/');
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    const json = (body) => Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) });
    global.fetch = jest.fn((url) => {
        if (url === '/api/auth/status') return json({ user: 'alice', isAdmin: false });
        if (url === '/api/auth/providers') return json({ github: true });
        if (url === '/api/settings') return json({ gemini_api_key_set: true });
        if (url === '/api/boards') return json([{ name: 'g4k', repoURL: 'https://github.com/gke-labs/g4k' }]);
        if (url === '/api/sessions') return json({ sessions: [] });
        return json([]);
    });
});

afterEach(() => {
    act(() => root.unmount());
    container.remove();
    delete global.fetch;
    jest.useRealTimers();
});

const render = async () => {
    await act(async () => { root.render(<App />); });
    await flush();
    await flush();
};
const toggle = (label) => Array.from(container.querySelectorAll('nav[aria-label="View"] button')).find(b => b.textContent === label);
const fetched = () => global.fetch.mock.calls.map(c => c[0]);

describe('the Work | Sessions toggle', () => {
    test('opens on Work; Sessions is not mounted until it is shown', async () => {
        await render();
        expect(toggle('Work').getAttribute('aria-pressed')).toBe('true');
        expect(toggle('Sessions').getAttribute('aria-pressed')).toBe('false');
        expect(fetched()).not.toContain('/api/sessions');
        expect(container.querySelector('textarea[aria-label="Start a session"]')).toBeNull();
    });

    test('switching keeps both mounted, shows one, and the hidden one polls nothing', async () => {
        await render();
        await act(async () => { toggle('Sessions').click(); });
        await flush();
        expect(window.location.hash).toBe('#/sessions');
        expect(fetched()).toContain('/api/sessions');
        // Work is still mounted, hidden.
        expect(container.querySelector('button[aria-label="Board"]').closest('[hidden]')).not.toBeNull();

        await act(async () => { toggle('Work').click(); });
        await flush();
        expect(window.location.hash).toBe('');
        // Still mounted, now hidden.
        expect(container.querySelector('textarea[aria-label="Start a session"]').closest('[hidden]')).not.toBeNull();
        expect(container.querySelector('button[aria-label="Board"]').closest('[hidden]')).toBeNull();
        global.fetch.mockClear();
        await act(async () => { jest.advanceTimersByTime(30000); });
        expect(fetched()).not.toContain('/api/sessions');
        expect(fetched()).toContain('/api/board/g4k/work');
    });

    test('the menu no longer lists Work or Sessions', async () => {
        await render();
        await act(async () => { container.querySelector('button[title="Menu"]').click(); });
        const menu = Array.from(container.querySelectorAll('div')).find(d => d.textContent.startsWith('Hi, alice') && d.textContent.includes('Dark mode'));
        const items = Array.from(menu.querySelectorAll('button')).map(b => b.textContent);
        expect(items).toContain('Usage');
        expect(items).not.toContain('Work');
        expect(items).not.toContain('Sessions');
    });
});
