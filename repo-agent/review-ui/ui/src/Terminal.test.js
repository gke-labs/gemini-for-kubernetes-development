import { terminalPath } from './Terminal';

// An overseer's sandbox's terminal is under that overseer's admin routes;
// any other is the sandbox card's, in the member's own namespace.
test('terminalPath', () => {
  expect(terminalPath('overseer-kcc', 'fix-k8s-config-connector-1')).toBe('/api/overseers/kcc/sandbox-card/fix-k8s-config-connector-1/terminal');
  expect(terminalPath('barney-s', 'fix-repo-2')).toBe('/api/sandbox-card/fix-repo-2/terminal');
  expect(terminalPath('', 'x')).toBe('/api/sandbox-card/x/terminal');
});
