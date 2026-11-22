import React from 'react';
import { createRoot } from 'react-dom/client';
import { act as domAct } from 'react-dom/test-utils';

jest.mock('react-markdown', () => ({ children }) => children);
jest.mock('remark-gfm', () => 'gfm-plugin-stub');
jest.mock('xterm', () => ({ Terminal: class { open() {} write() {} dispose() {} onData() {} loadAddon() {} } }));
jest.mock('xterm-addon-fit', () => ({ FitAddon: class { fit() {} } }));

const { default: TaskFiles, FileView, taskFilesURL, groupTaskFiles, formatSize, prettyContent, withFileParam, viewKind, parseData } = require('./TaskFiles');
const { summary } = require('./DataTree');

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
    // A JSON file opens as a tree; Source shows it exactly as stored.
    const tree = container.querySelector('[data-testid="task-file-tree"]');
    expect(tree.textContent).toContain('name: ');
    expect(tree.textContent).toContain('"p"');
    const source = Array.from(container.querySelectorAll('button')).find(b => b.textContent === 'Source');
    await act(async () => { source.click(); });
    expect(container.querySelector('[data-testid="task-file-content"]').textContent).toBe('{"name":"p"}');
  });
});

test('viewKind and parseData', () => {
  expect(viewKind('receipts.md')).toBe('markdown');
  expect(viewKind('runs/run1__cloud__get.json')).toBe('json');
  expect(viewKind('change.yaml')).toBe('yaml');
  expect(viewKind('k8s/x.yml')).toBe('yaml');
  expect(viewKind('steps/01.log')).toBe('text');
  expect(parseData('json', '{"a":[1,2]}')).toEqual({ value: { a: [1, 2] } });
  expect(parseData('json', 'nope')).toBeNull();
  expect(parseData('yaml', 'kind: Change\nspec:\n  title: t\n')).toEqual({ value: { kind: 'Change', spec: { title: 't' } } });
  expect(parseData('yaml', 'a: 1\n---\nb: 2\n')).toEqual({ value: [{ a: 1 }, { b: 2 }] });
  expect(parseData('yaml', 'a: [1\n')).toBeNull();
});

test('summary describes a folded branch', () => {
  expect(summary([1, 2, 3])).toBe('[3 items]');
  expect(summary([1])).toBe('[1 item]');
  expect(summary({ a: 1, b: 2 })).toBe('{2 keys}');
});

describe('FileView', () => {
  let container;
  let root;
  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });
  afterEach(() => {
    act(() => root.unmount());
    container.remove();
  });
  const render = async props => { await act(async () => { root.render(<FileView {...props} />); }); };

  test('Markdown is rendered, and its source shown as it is', async () => {
    await render({ name: 'receipts.md', content: '# Receipts\n', mode: 'rendered' });
    expect(container.querySelector('[data-testid="task-file-markdown"]').textContent).toContain('# Receipts');
    await render({ name: 'receipts.md', content: '# Receipts\n', mode: 'source' });
    expect(container.querySelector('[data-testid="task-file-content"]').textContent).toBe('# Receipts\n');
  });

  test('YAML opens as a tree; a file that does not parse falls back to its text', async () => {
    await render({ name: 'change.yaml', content: 'change:\n  title: hi\n', mode: 'rendered' });
    expect(container.querySelector('[data-testid="task-file-tree"]').textContent).toContain('title: "hi"');
    await render({ name: 'bad.json', content: '{nope', mode: 'rendered' });
    expect(container.textContent).toContain('Not valid JSON');
    expect(container.querySelector('[data-testid="task-file-content"]').textContent).toBe('{nope');
    await render({ name: 'steps/01.log', content: 'log line', mode: 'rendered' });
    expect(container.querySelector('[data-testid="task-file-content"]').textContent).toBe('log line');
  });
});
