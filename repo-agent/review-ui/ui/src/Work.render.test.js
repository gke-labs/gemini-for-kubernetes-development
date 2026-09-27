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
