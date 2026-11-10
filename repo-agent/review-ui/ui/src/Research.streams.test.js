import { stepText, lastSeq, buildResearchTranscript } from './Research';

jest.mock('react-markdown', () => () => null);
jest.mock('remark-gfm', () => () => {});

describe('stepText', () => {
  it('names a numbered step and its kind', () => {
    expect(stepText('10:ask (ask)')).toBe('step 10 · ask');
    expect(stepText('16:push (uses)')).toBe('step 16 · push');
  });
  it('names a step by its id', () => {
    expect(stepText('unchanged (run)')).toBe('step unchanged · run');
  });
  it('is empty before the first step, and plain for anything else', () => {
    expect(stepText('')).toBe('');
    expect(stepText(undefined)).toBe('');
    expect(stepText('odd')).toBe('step odd');
  });
});

describe('lastSeq', () => {
  it('is the newest seq, -1 for none', () => {
    expect(lastSeq([])).toBe(-1);
    expect(lastSeq([{ seq: 1 }, { seq: 3 }, { seq: 2 }, {}])).toBe(3);
  });
});

describe('a side session transcript', () => {
  it('builds from its stream events like the main one', () => {
    const t = buildResearchTranscript([
      { seq: 1, kind: 'user_prompt', data: { text: 'Review it' } },
      { seq: 2, kind: 'agent_message_chunk', data: { content: { type: 'text', text: 'Looking' } } },
    ]);
    expect(t.items.length).toBeGreaterThan(0);
  });
});
