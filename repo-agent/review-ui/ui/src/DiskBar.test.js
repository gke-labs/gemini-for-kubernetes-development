import React from 'react';
import { createRoot } from 'react-dom/client';
import { act as domAct } from 'react-dom/test-utils';
import DiskBar, { diskLevel } from './DiskBar';

const act = React.act || domAct;
global.IS_REACT_ACT_ENVIRONMENT = true;

let container, root;
beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
});
afterEach(() => {
  act(() => root.unmount());
  container.remove();
});

const GiB = 1024 ** 3;

// Below 75% ok, from 75% low, from 90% full; nothing known, nothing.
test('diskLevel', () => {
  expect(diskLevel({ used: 30 * GiB, size: 60 * GiB })).toBe('ok');
  expect(diskLevel({ used: 46 * GiB, size: 60 * GiB })).toBe('low');
  expect(diskLevel({ used: 55 * GiB, size: 60 * GiB })).toBe('full');
  expect(diskLevel(null)).toBe(null);
  expect(diskLevel({ used: 1, size: 0 })).toBe(null);
});

// A live disk shows used of size, the percentage and free; a recorded one
// says when; none renders nothing.
test('DiskBar shows the disk, and when it was recorded', () => {
  act(() => root.render(<DiskBar disk={{ used: 55 * GiB, size: 60 * GiB, free: 5 * GiB, live: true }} />));
  const bar = container.querySelector('[data-testid="disk-bar"]');
  expect(bar.dataset.level).toBe('full');
  expect(bar.textContent).toContain('55.0 / 60.0 GB (92%)');
  expect(bar.textContent).toContain('5.0 GB free');
  expect(bar.textContent).not.toContain('as of');

  act(() => root.render(<DiskBar disk={{ used: 30 * GiB, size: 60 * GiB, free: 30 * GiB, at: '2026-11-06T20:00:00Z' }} />));
  expect(container.querySelector('[data-testid="disk-bar"]').textContent).toContain('as of');

  act(() => root.render(<DiskBar disk={null} />));
  expect(container.querySelector('[data-testid="disk-bar"]')).toBe(null);
});
