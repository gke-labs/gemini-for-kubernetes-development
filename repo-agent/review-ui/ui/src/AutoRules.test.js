import React from 'react';
import { createRoot } from 'react-dom/client';
import { act as domAct } from 'react-dom/test-utils';
import AutoRules, { conditionOf, itemTypes } from './AutoRules';

const act = React.act || domAct;
global.IS_REACT_ACT_ENVIRONMENT = true;

const RECIPES = [
  { name: 'triage', label: 'Triage', on: ['issue'] },
  { name: 'review', label: 'Review', on: ['pr'], onFinish: 'post-review' },
  { name: 'summarize', label: 'Summarize', on: ['issue', 'pr'] },
  { name: 'care', label: 'Care', on: ['my-pr'], onFinish: 'post-replies' },
];

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

const render = async (props) => {
  await act(async () => { root.render(<AutoRules recipes={RECIPES} {...props} />); });
};
const select = (row, label) => row.querySelector(`[aria-label="${label}"]`);
const change = async (el, value) => {
  await act(async () => {
    const setter = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(el), 'value').set;
    setter.call(el, value);
    el.dispatchEvent(new Event(el.tagName === 'SELECT' ? 'change' : 'input', { bubbles: true }));
  });
};

test('a rule shows one condition, or custom for several', () => {
  expect(conditionOf({})).toBe('all');
  expect(conditionOf({ unclaimed: true })).toBe('unclaimed');
  expect(conditionOf({ labels: ['docs'] })).toBe('labels');
  expect(conditionOf({ unclaimed: true, labels: ['docs'] })).toBe('custom');
});

test('a my-pr recipe cannot be a rule: its PRs are the member’s, a rule’s anyone’s', () => {
  expect(itemTypes(RECIPES[2])).toEqual(['issue', 'pr']);
  expect(itemTypes(RECIPES[3])).toEqual([]);
  expect(itemTypes({ name: 'x' })).toEqual(['issue', 'pr']);
});

test('rules are added, edited and removed, offering what each recipe runs on', async () => {
  const onChange = jest.fn();
  await render({ rules: [], onChange });
  expect(container.textContent).toContain('No rules');
  await act(async () => { Array.from(container.querySelectorAll('button')).find(b => b.textContent === '+ Add rule').click(); });
  expect(onChange).toHaveBeenLastCalledWith([{ recipe: 'triage', on: 'issue', when: {} }]);

  const rules = [{ recipe: 'review', on: 'pr', when: { reviewRequestedFromOwner: true } }];
  await render({ rules, onChange });
  const row = container.querySelector('[data-testid="auto-rule"]');
  expect(Array.from(select(row, 'Recipe').options).map(o => o.value)).toEqual(['triage', 'review', 'summarize']);
  expect(Array.from(select(row, 'Item type').options).map(o => o.value)).toEqual(['pr']);
  expect(select(row, 'Condition').value).toBe('requested');
  expect(row.textContent).toContain('Review applies post-review when it finishes');

  await change(select(row, 'Recipe'), 'summarize');
  expect(onChange).toHaveBeenLastCalledWith([{ recipe: 'summarize', on: 'pr', when: { reviewRequestedFromOwner: true } }]);
  await change(select(row, 'Condition'), 'labels');
  expect(onChange).toHaveBeenLastCalledWith([{ recipe: 'review', on: 'pr', when: { labels: [] } }]);
  await change(select(row, 'Per day'), '3');
  expect(onChange).toHaveBeenLastCalledWith([{ ...rules[0], maxPerDay: 3 }]);
  await act(async () => { select(row, 'Remove rule').click(); });
  expect(onChange).toHaveBeenLastCalledWith([]);
});

test('an issue rule offers no review requests, and the controller’s skips are shown', async () => {
  await render({ rules: [{ recipe: 'summarize', on: 'issue', when: {} }], errors: ['nope on issue: no recipe "nope"'], onChange: () => {} });
  const row = container.querySelector('[data-testid="auto-rule"]');
  expect(Array.from(select(row, 'Condition').options).map(o => o.value)).not.toContain('requested');
  expect(container.textContent).toContain('Skipped: nope on issue: no recipe "nope"');
});
