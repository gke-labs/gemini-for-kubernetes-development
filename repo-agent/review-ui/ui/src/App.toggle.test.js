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
    global.admin = false;
    jest.useFakeTimers();
    window.history.replaceState(null, '', '/');
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    const json = (body) => Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) });
    global.fetch = jest.fn((url) => {
        if (url === '/api/auth/status') return json({ user: 'alice', isAdmin: global.admin });
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
        // Back on Work, the URL names its board again.
        expect(window.location.hash).toBe('#/boards/g4k');
        // Still mounted, now hidden.
        expect(container.querySelector('textarea[aria-label="Start a session"]').closest('[hidden]')).not.toBeNull();
        expect(container.querySelector('button[aria-label="Board"]').closest('[hidden]')).toBeNull();
        global.fetch.mockClear();
        await act(async () => { jest.advanceTimersByTime(30000); });
        expect(fetched()).not.toContain('/api/sessions');
        expect(fetched()).toContain('/api/board/g4k/work');
    });

    test('Overseer is a third choice, for admins only', async () => {
        await render();
        expect(toggle('Overseer')).toBeUndefined();
        act(() => root.unmount());
        root = createRoot(container);
        global.admin = true;
        await render();
        expect(Array.from(container.querySelectorAll('nav[aria-label="View"] button')).map(b => b.textContent)).toEqual(['Work', 'Sessions', 'Overseer']);
        await act(async () => { toggle('Overseer').click(); });
        await flush();
        expect(toggle('Overseer').getAttribute('aria-pressed')).toBe('true');
        expect(fetched()).toContain('/api/overseers');
        expect(container.textContent).not.toContain('Back to Dashboard');
        // Work stays mounted behind it.
        expect(container.querySelector('button[aria-label="Board"]').closest('[hidden]')).not.toBeNull();
    });

    test('the URL keeps the board and the overseer, across reloads and toggles', async () => {
        global.admin = true;
        const json = (body) => Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) });
        const overseers = [
            { metadata: { name: 'kcc' }, spec: { repoURL: 'https://github.com/G/k8s-config-connector' } },
            { metadata: { name: 'g4k-ov', annotations: { 'overseer.gemini.google.com/upgrade-mode': 'true' } }, spec: { repoURL: 'https://github.com/gke-labs/g4k' } },
        ];
        const base = global.fetch.getMockImplementation();
        global.fetch.mockImplementation((url) => {
            if (url === '/api/boards') return json([{ name: 'g4k', repoURL: 'https://github.com/gke-labs/g4k' }, { name: 'kcc', repoURL: 'https://github.com/G/k8s-config-connector' }]);
            if (url === '/api/overseers') return json(overseers);
            return base(url);
        });
        localStorage.removeItem('overseer.active');
        // A link to a board opens Work on it.
        window.history.replaceState(null, '', '/#/boards/kcc');
        await render();
        expect(container.querySelector('button[aria-label="Board"]').textContent).toContain('kcc');
        expect(window.location.hash).toBe('#/boards/kcc');

        // Overseer opens on the first, the URL naming it; picking another
        // moves the URL and is remembered.
        await act(async () => { toggle('Overseer').click(); });
        await flush();
        expect(window.location.hash).toBe('#/overseers/kcc');
        const picker = () => container.querySelector('button[aria-label="Overseer"]');
        await act(async () => { picker().click(); });
        const opts = Array.from(container.querySelectorAll('[role="option"]')).map(o => o.textContent);
        expect(opts).toEqual(['✓kccG/k8s-config-connector', 'g4k-ovgke-labs/g4kUPGRADING']);
        // Picking the one shown changes nothing: no deselect.
        await act(async () => { container.querySelectorAll('[role="option"]')[0].click(); });
        expect(picker().textContent).toContain('kcc');
        await act(async () => { picker().click(); });
        await act(async () => { container.querySelectorAll('[role="option"]')[1].click(); });
        await flush();
        expect(window.location.hash).toBe('#/overseers/g4k-ov');
        expect(localStorage.getItem('overseer.active')).toBe('g4k-ov');
        expect(container.textContent).toContain('🚧 UPGRADING');

        // Toggled away and back, the overseer page reopens on it.
        await act(async () => { toggle('Work').click(); });
        await flush();
        expect(window.location.hash).toBe('#/boards/kcc');
        await act(async () => { toggle('Overseer').click(); });
        await flush();
        expect(picker().textContent).toContain('g4k-ov');
        expect(window.location.hash).toBe('#/overseers/g4k-ov');
    });

    test('a link to an overseer opens the Overseer view on it', async () => {
        global.admin = true;
        const json = (body) => Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) });
        const base = global.fetch.getMockImplementation();
        global.fetch.mockImplementation((url) => (url === '/api/overseers'
            ? json([{ metadata: { name: 'a' }, spec: { repoURL: 'https://github.com/o/a' } }, { metadata: { name: 'b' }, spec: { repoURL: 'https://github.com/o/b' } }])
            : base(url)));
        localStorage.setItem('overseer.active', 'a');
        window.history.replaceState(null, '', '/#/overseers/b');
        await render();
        expect(toggle('Overseer').getAttribute('aria-pressed')).toBe('true');
        expect(container.querySelector('button[aria-label="Overseer"]').textContent).toContain('b');
        expect(window.location.hash).toBe('#/overseers/b');
    });

    test('the menu no longer lists Work or Sessions', async () => {
        await render();
        await act(async () => { container.querySelector('button[title="Menu"]').click(); });
        const menu = Array.from(container.querySelectorAll('div')).find(d => d.textContent.startsWith('Hi, alice') && d.textContent.includes('Dark mode'));
        const items = Array.from(menu.querySelectorAll('button')).map(b => b.textContent);
        expect(items).toContain('Usage');
        expect(items).not.toContain('Work');
        expect(items).not.toContain('Sessions');
        expect(items).not.toContain('Overseer');
    });
});
