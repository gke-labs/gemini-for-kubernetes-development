import React from 'react';
import { createRoot } from 'react-dom/client';
import { act as domAct } from 'react-dom/test-utils';

const { default: TaskFiles, taskFilesURL, groupTaskFiles, formatSize, prettyContent, withFileParam } = require('./TaskFiles');

const act = React.act || domAct;
global.IS_REACT_ACT_ENVIRONMENT = true;
const flush = () => act(async () => { await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); });

test('taskFilesURL escapes each part of the path', () => {
  expect(taskFilesURL('/api/overseers/kcc', 'fix-r-5', 'recipe-fix-1')).toBe('/api/overseers/kcc/task-files/fix-r-5/recipe-fix-1');
  expect(taskFilesURL('/api/overseers/kcc', 'fix-r-5', 'recipe-fix-1', 'runs/run1__REPORT.md')).toBe('/api/overseers/kcc/task-files/fix-r-5/recipe-fix-1/file/runs/run1__REPORT.md');
});

test('groupTaskFiles keeps the top level first, then each directory', () => {
  const groups = groupTaskFiles([{ name: 'receipts.md' }, { name: 'runs/a.json' }, { name: 'runs/b.json' }, { name: 'steps/01.log' }]);
  expect(groups.map(g => g.dir)).toEqual(['', 'runs', 'steps']);
  expect(groups[1].files.map(f => f.name)).toEqual(['runs/a.json', 'runs/b.json']);
});

test('formatSize, prettyContent and withFileParam', () => {
  expect(formatSize(0)).toBe('');
  expect(formatSize(512)).toBe('512 B');
  expect(formatSize(2048)).toBe('2.0 KB');
  expect(prettyContent('a.json', '{"a":1}')).toBe('{\n  "a": 1\n}');
  expect(prettyContent('a.json', 'not json')).toBe('not json');
  expect(prettyContent('a.log', '{"a":1}')).toBe('{"a":1}');
  expect(withFileParam('#/overseers/kcc/task-session/s/t', 'runs/x.json')).toBe('#/overseers/kcc/task-session/s/t?tab=files&file=runs%2Fx.json');
  expect(withFileParam('#/overseers/kcc/task-session/s/t?tab=files&file=a', '')).toBe('#/overseers/kcc/task-session/s/t?tab=files');
});

describe('TaskFiles', () => {
  let container;
  let root;
  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    global.fetch = jest.fn(url => {
      if (url.endsWith('/task-files/fix-r-5/recipe-v-1')) {
        return Promise.resolve({ ok: true, json: () => Promise.resolve({ sandbox: 'fix-r-5', task: 'recipe-v-1', source: 'archive', files: [{ name: 'receipts.md', size: 20 }, { name: 'runs/run1__cloud__get.json', size: 30 }] }) });
      }
      if (url.endsWith('/file/runs/run1__cloud__get.json')) {
        return Promise.resolve({ ok: true, text: () => Promise.resolve('{"name":"p"}') });
      }
      return Promise.resolve({ ok: false, statusText: 'Not Found', text: () => Promise.resolve('{"error":"no such file"}'), json: () => Promise.resolve({ error: 'no such file' }) });
    });
  });
  afterEach(() => {
    act(() => root.unmount());
    container.remove();
  });

  test('lists the files, says where they came from, and opens the one in the link', async () => {
    await act(async () => {
      root.render(<TaskFiles apiBase="/api/overseers/kcc" sandbox="fix-r-5" task="recipe-v-1" initialFile="runs/run1__cloud__get.json" />);
    });
    await flush();
    expect(container.querySelector('[data-testid="task-files-source"]').textContent).toContain('From the archive');
    expect(container.textContent).toContain('receipts.md');
    expect(container.textContent).toContain('runs/');
    await flush();
    expect(container.querySelector('[data-testid="task-file-content"]').textContent).toBe('{\n  "name": "p"\n}');
  });
});
