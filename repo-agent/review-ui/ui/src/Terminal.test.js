import { terminalPath, applyFrame, terminalURL, terminalHash, terminalRoute } from './Terminal';

// An overseer's sandbox's terminal is under that overseer's admin routes;
// any other is the sandbox card's, in the member's own namespace.
test('terminalPath', () => {
  expect(terminalPath('overseer-kcc', 'fix-k8s-config-connector-1')).toBe('/api/overseers/kcc/sandbox-card/fix-k8s-config-connector-1/terminal');
  expect(terminalPath('barney-s', 'fix-repo-2')).toBe('/api/sandbox-card/fix-repo-2/terminal');
  expect(terminalPath('', 'x')).toBe('/api/sandbox-card/x/terminal');
});

// A daemon's stream: attached sets the offset (and may reset), output
// advances it, a snapshot is written as is, exit forgets the offset so
// the next connection starts the new program from its first byte.
test('applyFrame on a daemon stream', () => {
  const stream = { offset: null };
  expect(applyFrame(stream, JSON.stringify({ type: 'attached', offset: 10, reset: true }))).toEqual({ reset: true, write: null, exited: null });
  expect(stream.offset).toBe(10);
  const out = applyFrame(stream, new Uint8Array([104, 105, 33]).buffer);
  expect(out.reset).toBe(false);
  expect(Array.from(out.write)).toEqual([104, 105, 33]);
  expect(stream.offset).toBe(13);
  expect(applyFrame(stream, JSON.stringify({ type: 'snapshot', data: 'SCREEN' })).write).toBe('SCREEN');
  expect(stream.offset).toBe(13);
  const exit = applyFrame(stream, JSON.stringify({ type: 'exit', code: 3 }));
  expect(exit.exited).toBe(3);
  expect(exit.write).toContain('exited with code 3');
  expect(stream.offset).toBe(null);
});

// An older sandbox (tmux over exec) never sends attached: output is
// written, and no offset is kept to resume from.
test('applyFrame on a tmux stream', () => {
  const stream = { offset: null };
  applyFrame(stream, new Uint8Array([97]).buffer);
  expect(stream.offset).toBe(null);
  expect(applyFrame(stream, 'plain text')).toEqual({ reset: false, write: 'plain text', exited: null });
});

test('terminalURL', () => {
  expect(terminalURL('barney-s', 'sb', '', null)).toBe('/api/sandbox-card/sb/terminal');
  expect(terminalURL('barney-s', 'sb', 'plan', null)).toBe('/api/sandbox-card/sb/terminal?chat=plan');
  expect(terminalURL('overseer-kcc', 'sb', '', 0)).toBe('/api/overseers/kcc/sandbox-card/sb/terminal?offset=0');
  expect(terminalURL('barney-s', 'sb', 'fix', 512)).toBe('/api/sandbox-card/sb/terminal?chat=fix&offset=512');
});

// The launcher's links, and the page that reads them back.
test('terminalHash and terminalRoute', () => {
  expect(terminalHash('ns', 'sb')).toBe('#/terminal/ns/sb');
  expect(terminalHash('ns', 'sb', { program: 'claude' })).toBe('#/terminal/ns/sb?program=claude');
  expect(terminalHash('ns', 'sb', { chat: 'plan' })).toBe('#/terminal/ns/sb?chat=plan');
  expect(terminalRoute('#/terminal/ns/sb?program=agy')).toEqual({ namespace: 'ns', name: 'sb', chat: '', program: 'agy' });
  expect(terminalRoute('#/terminal/ns/sb?chat=plan')).toEqual({ namespace: 'ns', name: 'sb', chat: 'plan', program: '' });
  expect(terminalRoute('#/terminal/ns/sb')).toEqual({ namespace: 'ns', name: 'sb', chat: '', program: '' });
  expect(terminalURL('ns', 'sb', '', 40, 'gemini')).toBe('/api/sandbox-card/sb/terminal?program=gemini&offset=40');
});

// A state frame is what the program is doing, for the status line.
test('applyFrame state', () => {
  const stream = { offset: 5 };
  const out = applyFrame(stream, JSON.stringify({ type: 'state', state: 'needs-you', message: 'Allow edit?', title: 'claude' }));
  expect(out.state).toEqual({ state: 'needs-you', message: 'Allow edit?', title: 'claude' });
  expect(out.write).toBe(null);
  expect(stream.offset).toBe(5);
});
