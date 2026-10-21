import React from 'react';
import { createRoot } from 'react-dom/client';
import { act as domAct } from 'react-dom/test-utils';
import SandboxSize from './SandboxSize';

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

// Each size shows the board's value, or factory's default as its
// placeholder; editing one changes only that field.
test('sandbox size shows the board and edits one field', () => {
  const changes = [];
  const spec = { engine: 'gemini', ephemeralStorage: '10Gi' };
  act(() => root.render(<SandboxSize spec={spec} onChange={s => changes.push(s)} />));
  const inputs = [...container.querySelectorAll('input')];
  expect(inputs.map(i => i.placeholder)).toEqual(['10Gi', 'cluster default', '6Gi', '500m', '4', '2Gi', '6Gi']);
  expect(inputs[2].value).toBe('10Gi');
  expect(inputs[4].value).toBe('');

  const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
  act(() => {
    setter.call(inputs[4], '8');
    inputs[4].dispatchEvent(new Event('input', { bubbles: true }));
  });
  expect(changes).toEqual([{ engine: 'gemini', ephemeralStorage: '10Gi', cpuLimit: '8' }]);
});
