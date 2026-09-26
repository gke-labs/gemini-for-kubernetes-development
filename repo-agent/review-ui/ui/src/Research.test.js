import {
    repoFromURL,
    chunkText,
    permissionTone,
    shortSession,
    ageOf,
    emptyTranscript,
    applyResearchEvent,
    buildResearchTranscript,
    pendingPermission,
} from './Research';

// react-markdown ships as ESM and jest does not transform node_modules,
// so importing it for real would fail to parse before a single
// assertion ran. Nothing here renders, so a stub is enough.
jest.mock('react-markdown', () => () => null);
jest.mock('remark-gfm', () => () => {});

// Events as acpd writes them: a seq, a kind, and a payload whose shape
// is the kind's business.
const ev = (seq, kind, data) => ({ seq, kind, data, time: '2026-09-26T10:00:00Z' });
const textChunk = (seq, kind, text) => ev(seq, kind, { sessionUpdate: kind, content: { type: 'text', text } });

describe('repoFromURL', () => {
    test('takes the repo segment of a GitHub URL', () => {
        expect(repoFromURL('https://github.com/gke-labs/repo-agent')).toBe('repo-agent');
    });

    test('strips a .git suffix, as the server does', () => {
        expect(repoFromURL('https://github.com/gke-labs/repo-agent.git')).toBe('repo-agent');
    });

    test('tolerates a trailing slash', () => {
        expect(repoFromURL('https://github.com/gke-labs/repo-agent/')).toBe('repo-agent');
    });

    test('rejects anything that is not exactly owner/repo', () => {
        // The server's parseRepoURL requires two path segments, so a URL
        // it would reject must not match a session here either.
        expect(repoFromURL('https://github.com/gke-labs')).toBe('');
        expect(repoFromURL('https://github.com/gke-labs/repo-agent/tree/main')).toBe('');
        expect(repoFromURL('')).toBe('');
        expect(repoFromURL(null)).toBe('');
        expect(repoFromURL(undefined)).toBe('');
    });
});

describe('chunkText', () => {
    test('reads a plain ContentBlock', () => {
        expect(chunkText({ type: 'text', text: 'hello' })).toBe('hello');
    });

    test('concatenates a list of content', () => {
        expect(chunkText([{ type: 'text', text: 'a' }, { type: 'text', text: 'b' }])).toBe('ab');
    });

    test('unwraps tool-call content, which nests a block under content', () => {
        expect(chunkText([{ type: 'content', content: { type: 'text', text: 'out' } }])).toBe('out');
    });

    test('passes a bare string through', () => {
        expect(chunkText('raw')).toBe('raw');
    });

    test('returns empty for absent or unrecognised payloads', () => {
        expect(chunkText(null)).toBe('');
        expect(chunkText(undefined)).toBe('');
        expect(chunkText({ type: 'image', data: 'abc' })).toBe('');
    });
});

describe('permissionTone', () => {
    test('classifies the ACP option kinds', () => {
        expect(permissionTone('allow_once')).toBe('allow');
        expect(permissionTone('allow_always')).toBe('allow');
        expect(permissionTone('reject_once')).toBe('reject');
        expect(permissionTone('reject_always')).toBe('reject');
    });

    test('an engine-defined kind gets no opinion', () => {
        expect(permissionTone('something_new')).toBe('neutral');
        expect(permissionTone('')).toBe('neutral');
        expect(permissionTone(undefined)).toBe('neutral');
    });
});

describe('shortSession', () => {
    test('is the first eight characters, the granularity the sandbox name uses', () => {
        expect(shortSession('0f8c1a2b-3d4e-5f60-7a8b-9c0d1e2f3a4b')).toBe('0f8c1a2b');
    });

    test('is safe on short or missing ids', () => {
        expect(shortSession('abc')).toBe('abc');
        expect(shortSession('')).toBe('');
        expect(shortSession(undefined)).toBe('');
    });
});

describe('ageOf', () => {
    const realNow = Date.now;
    afterEach(() => { Date.now = realNow; });

    test('renders minutes, hours and days', () => {
        Date.now = jest.fn(() => new Date('2026-09-26T12:00:00Z').getTime());
        expect(ageOf('2026-09-26T11:30:00Z')).toBe('30m');
        expect(ageOf('2026-09-26T09:00:00Z')).toBe('3h');
        expect(ageOf('2026-09-24T12:00:00Z')).toBe('2d');
    });

    test('is empty for missing or unparseable timestamps', () => {
        expect(ageOf('')).toBe('');
        expect(ageOf(null)).toBe('');
        expect(ageOf('not-a-date')).toBe('');
    });
});

describe('applyResearchEvent', () => {
    test('a user prompt becomes a bubble and marks the turn in flight', () => {
        const t = applyResearchEvent(emptyTranscript, ev(1, 'user_prompt', { text: 'why is this slow?' }));
        expect(t.items).toHaveLength(1);
        expect(t.items[0].role).toBe('user');
        expect(t.items[0].text).toBe('why is this slow?');
        expect(t.busy).toBe(true);
    });

    test('agent message chunks coalesce into one bubble', () => {
        const t = buildResearchTranscript([
            textChunk(1, 'agent_message_chunk', 'The '),
            textChunk(2, 'agent_message_chunk', 'answer '),
            textChunk(3, 'agent_message_chunk', 'is 42.'),
        ]);
        expect(t.items).toHaveLength(1);
        expect(t.items[0].text).toBe('The answer is 42.');
    });

    test('a tool call between two runs of text splits them, because that is what happened', () => {
        const t = buildResearchTranscript([
            textChunk(1, 'agent_message_chunk', 'looking'),
            ev(2, 'tool_call', { toolCallId: 'c1', title: 'read main.go', kind: 'read', status: 'pending' }),
            textChunk(3, 'agent_message_chunk', 'found it'),
        ]);
        expect(t.items.map(i => i.role)).toEqual(['agent', 'tool', 'agent']);
        expect(t.items[0].text).toBe('looking');
        expect(t.items[2].text).toBe('found it');
    });

    test('thoughts coalesce separately from speech', () => {
        const t = buildResearchTranscript([
            textChunk(1, 'agent_thought_chunk', 'hmm '),
            textChunk(2, 'agent_thought_chunk', 'maybe'),
            textChunk(3, 'agent_message_chunk', 'here goes'),
        ]);
        expect(t.items.map(i => i.role)).toEqual(['thought', 'agent']);
        expect(t.items[0].text).toBe('hmm maybe');
    });

    test('an empty chunk changes nothing', () => {
        const before = buildResearchTranscript([textChunk(1, 'agent_message_chunk', 'x')]);
        const after = applyResearchEvent(before, textChunk(2, 'agent_message_chunk', ''));
        expect(after).toBe(before);
    });

    test('a tool call update amends its row instead of adding one', () => {
        const t = buildResearchTranscript([
            ev(1, 'tool_call', { toolCallId: 'c1', title: 'grep', kind: 'search', status: 'pending' }),
            ev(2, 'tool_call_update', { toolCallId: 'c1', status: 'completed', content: [{ type: 'content', content: { type: 'text', text: '3 hits' } }] }),
        ]);
        expect(t.items).toHaveLength(1);
        expect(t.items[0].status).toBe('completed');
        expect(t.items[0].title).toBe('grep');
        expect(t.items[0].content).toBe('3 hits');
    });

    test('an update for a call we never saw the start of still renders', () => {
        // A stream resumed part-way through a tool call: the opening
        // event is behind the offset we reconnected at.
        const t = buildResearchTranscript([
            ev(7, 'tool_call_update', { toolCallId: 'c9', status: 'completed', title: 'build' }),
        ]);
        expect(t.items).toHaveLength(1);
        expect(t.items[0].role).toBe('tool');
        expect(t.items[0].toolCallId).toBe('c9');
        expect(t.items[0].status).toBe('completed');
    });

    test('a plan update supersedes the previous plan and adds no transcript row', () => {
        const t = buildResearchTranscript([
            ev(1, 'plan', { entries: [{ content: 'read code', status: 'pending' }] }),
            ev(2, 'plan', { entries: [{ content: 'read code', status: 'completed' }, { content: 'write up', status: 'pending' }] }),
        ]);
        expect(t.items).toHaveLength(0);
        expect(t.plan).toHaveLength(2);
        expect(t.plan[0].status).toBe('completed');
    });

    test('a permission request is resolved in place', () => {
        const t = buildResearchTranscript([
            ev(1, 'permission_request', { requestId: 'r1', toolCall: { title: 'run tests' }, options: [{ optionId: 'o1', name: 'Allow', kind: 'allow_once' }] }),
            ev(2, 'permission_resolved', { requestId: 'r1', outcome: 'selected', optionId: 'o1' }),
        ]);
        expect(t.items).toHaveLength(1);
        expect(t.items[0].outcome).toBe('selected');
        expect(t.items[0].optionId).toBe('o1');
    });

    test('a resolution carries acpd\'s own reason, which is how a timeout reads', () => {
        const t = buildResearchTranscript([
            ev(1, 'permission_request', { requestId: 'r1', options: [] }),
            ev(2, 'permission_resolved', { requestId: 'r1', outcome: 'cancelled', reason: 'timed out' }),
        ]);
        expect(t.items[0].outcome).toBe('cancelled');
        expect(t.items[0].reason).toBe('timed out');
    });

    test('a resolution with no matching request is ignored', () => {
        const before = buildResearchTranscript([textChunk(1, 'agent_message_chunk', 'hi')]);
        const after = applyResearchEvent(before, ev(2, 'permission_resolved', { requestId: 'nope', outcome: 'selected' }));
        expect(after).toBe(before);
    });

    test('a clean turn end clears busy without adding a row', () => {
        const t = buildResearchTranscript([
            ev(1, 'user_prompt', { text: 'hi' }),
            textChunk(2, 'agent_message_chunk', 'hello'),
            ev(3, 'turn_end', { stopReason: 'end_turn' }),
        ]);
        expect(t.busy).toBe(false);
        expect(t.items.map(i => i.role)).toEqual(['user', 'agent']);
        expect(t.stopReason).toBe('end_turn');
    });

    test('any other stop reason is worth a row, because it answers "why did it stop"', () => {
        const t = buildResearchTranscript([
            ev(1, 'user_prompt', { text: 'hi' }),
            ev(2, 'turn_end', { stopReason: 'cancelled' }),
        ]);
        expect(t.busy).toBe(false);
        expect(t.items.map(i => i.role)).toEqual(['user', 'stop']);
        expect(t.items[1].stopReason).toBe('cancelled');
    });

    test('an engine error ends the turn, since acpd writes no turn_end on that path', () => {
        const t = buildResearchTranscript([
            ev(1, 'user_prompt', { text: 'hi' }),
            ev(2, 'error', { message: 'prompt failed: engine exited', log: '/tmp/engine.log' }),
        ]);
        expect(t.busy).toBe(false);
        expect(t.items[1].role).toBe('error');
        expect(t.items[1].log).toBe('/tmp/engine.log');
    });

    test('both mode kinds mean the same thing, and the last one wins', () => {
        // acpd writes mode_changed for a switch it made, because gemini
        // does not notify on its own set_mode; current_mode_update is the
        // engine reporting one it made itself. A reader wants the same
        // answer from either.
        const t = buildResearchTranscript([
            ev(1, 'mode_changed', { currentModeId: 'yolo' }),
            ev(2, 'user_prompt', { text: 'hi' }),
            ev(3, 'current_mode_update', { currentModeId: 'plan' }),
        ]);
        expect(t.mode).toBe('plan');
        expect(t.items.map(i => i.role)).toEqual(['mode', 'user', 'mode']);
        expect(t.items[0].mode).toBe('yolo');
    });

    test('a mode event with no mode in it changes nothing', () => {
        const before = buildResearchTranscript([ev(1, 'mode_changed', { currentModeId: 'yolo' })]);
        expect(applyResearchEvent(before, ev(2, 'mode_changed', {}))).toBe(before);
    });

    test('an unknown kind is rendered, not dropped', () => {
        const t = applyResearchEvent(emptyTranscript, ev(1, 'available_commands_update', { commands: ['x'] }));
        expect(t.items).toHaveLength(1);
        expect(t.items[0].role).toBe('unknown');
        expect(t.items[0].kind).toBe('available_commands_update');
    });

    test('a malformed event changes nothing', () => {
        expect(applyResearchEvent(emptyTranscript, null)).toBe(emptyTranscript);
        expect(applyResearchEvent(emptyTranscript, {})).toBe(emptyTranscript);
    });

    test('folding never mutates the state it was given', () => {
        const before = buildResearchTranscript([textChunk(1, 'agent_message_chunk', 'one')]);
        const snapshot = JSON.parse(JSON.stringify(before));
        applyResearchEvent(before, textChunk(2, 'agent_message_chunk', ' two'));
        applyResearchEvent(before, ev(3, 'tool_call', { toolCallId: 'c1' }));
        expect(before).toEqual(snapshot);
        // The shared zero value in particular must survive being folded on.
        expect(emptyTranscript.items).toHaveLength(0);
    });

    test('items carry stable keys drawn from the transcript sequence', () => {
        const t = buildResearchTranscript([
            ev(1, 'user_prompt', { text: 'a' }),
            ev(2, 'tool_call', { toolCallId: 'c1' }),
        ]);
        expect(t.items.map(i => i.key)).toEqual(['e1', 'e2']);
    });

    test('a coalesced bubble keeps the key of its first chunk', () => {
        const t = buildResearchTranscript([
            textChunk(4, 'agent_message_chunk', 'a'),
            textChunk(5, 'agent_message_chunk', 'b'),
        ]);
        expect(t.items[0].key).toBe('e4');
    });
});

describe('buildResearchTranscript', () => {
    test('is empty for a missing or non-array transcript', () => {
        expect(buildResearchTranscript(null).items).toHaveLength(0);
        expect(buildResearchTranscript(undefined).items).toHaveLength(0);
        expect(buildResearchTranscript({}).items).toHaveLength(0);
    });

    test('a replayed conversation whose turn never ended still reads as in flight', () => {
        // This is the acpd-restart shape: a prompt on disk with no
        // turn_end after it. The fold says busy; the component overrides
        // it with the live session until something new arrives.
        const t = buildResearchTranscript([ev(1, 'user_prompt', { text: 'hi' })]);
        expect(t.busy).toBe(true);
    });
});

describe('pendingPermission', () => {
    test('finds the unresolved request', () => {
        const t = buildResearchTranscript([
            ev(1, 'permission_request', { requestId: 'r1', options: [] }),
        ]);
        expect(pendingPermission(t).requestId).toBe('r1');
    });

    test('is null once it is answered', () => {
        const t = buildResearchTranscript([
            ev(1, 'permission_request', { requestId: 'r1', options: [] }),
            ev(2, 'permission_resolved', { requestId: 'r1', outcome: 'selected', optionId: 'o1' }),
        ]);
        expect(pendingPermission(t)).toBeNull();
    });

    test('picks the newest when an earlier one was answered', () => {
        const t = buildResearchTranscript([
            ev(1, 'permission_request', { requestId: 'r1', options: [] }),
            ev(2, 'permission_resolved', { requestId: 'r1', outcome: 'selected' }),
            ev(3, 'permission_request', { requestId: 'r2', options: [] }),
        ]);
        expect(pendingPermission(t).requestId).toBe('r2');
    });

    test('is null for an empty or absent transcript', () => {
        expect(pendingPermission(emptyTranscript)).toBeNull();
        expect(pendingPermission(null)).toBeNull();
    });
});
