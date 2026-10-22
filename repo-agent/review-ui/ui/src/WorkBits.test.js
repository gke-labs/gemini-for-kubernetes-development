import { recipeSource } from './WorkBits';

// A run's hover says where its recipe came from: the built-in, or the
// repository's file at the commit it was read at.
test('recipeSource', () => {
  expect(recipeSource({ source: 'builtin' })).toBe('recipe: built-in');
  expect(recipeSource({ source: '.agents/recipes/fix.yaml@0123456789abcdef' })).toBe('recipe: .agents/recipes/fix.yaml @ 0123456');
  expect(recipeSource({ source: '.agents/recipes/fix.yaml' })).toBe('recipe: .agents/recipes/fix.yaml');
  expect(recipeSource({})).toBe('');
  expect(recipeSource(undefined)).toBe('');
});
