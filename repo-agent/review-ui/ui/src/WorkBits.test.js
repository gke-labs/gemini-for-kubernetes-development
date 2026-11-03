import { recipeSource, taskLabel, taskLabelTitle } from './WorkBits';

// A run's hover says where its recipe came from: the built-in, or the
// repository's file at the commit it was read at.
test('recipeSource', () => {
  expect(recipeSource({ source: 'builtin' })).toBe('recipe: built-in');
  expect(recipeSource({ source: '.agents/recipes/fix.yaml@0123456789abcdef' })).toBe('recipe: .agents/recipes/fix.yaml @ 0123456');
  expect(recipeSource({ source: '.agents/recipes/fix.yaml' })).toBe('recipe: .agents/recipes/fix.yaml');
  expect(recipeSource({})).toBe('');
  expect(recipeSource(undefined)).toBe('');
});

// A queued task is named by what it does: its rule, else its recipe, else
// (an older watch's task) its type.
test('taskLabel', () => {
  expect(taskLabel({ type: 'pr-rule', rule: 'ci', recipe: 'care-ci' })).toBe('Fix CI');
  expect(taskLabel({ type: 'pr-rule', rule: 'comments', recipe: 'care-comments' })).toBe('Address comments');
  expect(taskLabel({ type: 'pr-rule', rule: 'conflict', recipe: 'care-rebase' })).toBe('Resolve conflicts');
  expect(taskLabel({ type: 'issue-rule', rule: 'fix', recipe: 'fix' })).toBe('Fix issue');
  expect(taskLabel({ type: 'issue-rule', rule: 'triage-2', recipe: 'triage' })).toBe('Triage');
  expect(taskLabel({ type: 'issue-rule', rule: 'my-custom-rule' })).toBe('My custom rule');
  expect(taskLabel({ type: 'pr-rule', rule: 'constructor' })).toBe('Constructor');
  expect(taskLabel({ type: 'agent-chore' })).toBe('Chore');
  expect(taskLabel({ type: 'pr-rule' })).toBe('pr-rule');
  expect(taskLabel(undefined)).toBe('');
  expect(taskLabelTitle({ type: 'pr-rule', rule: 'ci', recipe: 'care-ci' })).toBe('type: pr-rule\nrule: ci\nrecipe: care-ci');
});
