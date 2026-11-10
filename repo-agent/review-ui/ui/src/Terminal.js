import React, { useEffect, useRef, useState } from 'react';
import { Terminal } from 'xterm';
import { FitAddon } from 'xterm-addon-fit';
import 'xterm/css/xterm.css';

// A sandbox's terminal. On a daemon that serves terminal sessions
// (factory's termd) the program runs on a PTY the daemon owns: this
// component counts the output bytes it has written and, reconnecting
// after a drop, asks for the stream from that offset - nothing lost,
// nothing repeated (a client too far behind gets a snapshot of the
// screen). On an older sandbox the server attaches tmux over exec, and
// the stream is plain output.
// chat: a factory task type ("plan", …) — instead of a shell, the
// engine's CLI continuing its conversation in the sandbox.
// terminalPath is the socket a sandbox's terminal is on: an overseer's
// sandbox (namespace overseer-<name>) under that overseer's admin routes,
// any other the sandbox card's, in the member's own namespace.
export function terminalPath(namespace, sandboxName) {
    const sb = encodeURIComponent(sandboxName);
    if (namespace && namespace.startsWith('overseer-')) {
        return `/api/overseers/${encodeURIComponent(namespace.slice('overseer-'.length))}/sandbox-card/${sb}/terminal`;
    }
    return `/api/sandbox-card/${sb}/terminal`;
}

// applyFrame is what one websocket message does to the terminal, and to
// stream - {offset}, the byte the next output starts at, null until a
// daemon has said (an older sandbox never does). Text frames from a
// daemon are JSON control: attached {offset, reset}, snapshot {data},
// exit {code}; binary frames are output. Anything else is output too.
// A state frame says what the program is doing (termd's activity: working,
// needs-you, idle, done, error) and is returned as state.
// It returns {reset, write, exited, state}: clear the terminal first, what
// to write, the program's exit code when it ended, and its state.
export function applyFrame(stream, data) {
    if (typeof data !== 'string') {
        const bytes = new Uint8Array(data);
        if (stream.offset !== null) stream.offset += bytes.byteLength;
        return { reset: false, write: bytes, exited: null };
    }
    let frame = null;
    try { frame = JSON.parse(data); } catch (e) { /* plain text output */ }
    if (!frame || typeof frame.type !== 'string') {
        return { reset: false, write: data, exited: null };
    }
    switch (frame.type) {
        case 'attached':
            stream.offset = frame.offset || 0;
            return { reset: !!frame.reset, write: null, exited: null };
        case 'snapshot':
            return { reset: false, write: frame.data || '', exited: null };
        case 'state':
            return { reset: false, write: null, exited: null, state: { state: frame.state || 'idle', message: frame.message || '', title: frame.title || '' } };
        case 'exit':
            // The next connection starts a new program, from its first byte.
            stream.offset = null;
            return { reset: false, write: `\r\n[exited with code ${frame.code || 0}]\r\n`, exited: frame.code || 0 };
        default:
            return { reset: false, write: null, exited: null };
    }
}

// TERMINAL_PROGRAMS are the engine CLIs the launcher opens in a sandbox,
// each continuing its latest conversation in the checkout.
export const TERMINAL_PROGRAMS = [
    { program: 'gemini', label: 'gemini' },
    { program: 'claude', label: 'claude' },
    { program: 'agy', label: 'agy (antigravity)' },
];

// terminalHash is the full-window terminal page: a shell, a task's chat,
// or a launcher program's CLI.
export function terminalHash(namespace, sandboxName, { chat = '', program = '' } = {}) {
    const q = chat ? `?chat=${encodeURIComponent(chat)}` : program ? `?program=${encodeURIComponent(program)}` : '';
    return `#/terminal/${namespace}/${sandboxName}${q}`;
}

// terminalRoute reads the full-window terminal page's hash (terminalHash):
// #/terminal/<ns>/<name>, with
// `?chat=<taskType>` in the hash resumes that task's agent conversation
// instead of opening a shell (e.g. continue a plan session);
// `?program=gemini|claude|agy` opens that engine's CLI (the launcher).
export function terminalRoute(hash = window.location.hash) {
  const m = hash.match(/^#\/terminal\/([A-Za-z0-9._-]+)\/([A-Za-z0-9._-]+)(?:\?(chat|program)=([a-z]+))?$/);
  if (!m) return null;
  return { namespace: m[1], name: m[2], chat: m[3] === 'chat' ? m[4] : '', program: m[3] === 'program' ? m[4] : '' };
}

// terminalURL is the socket to connect to: the chat or program, and where
// to resume.
export function terminalURL(namespace, sandboxName, chat, offset, program) {
    const params = new URLSearchParams();
    if (chat) params.set('chat', chat);
    if (program) params.set('program', program);
    if (offset !== null && offset !== undefined) params.set('offset', String(offset));
    const q = params.toString();
    return `${terminalPath(namespace, sandboxName)}${q ? `?${q}` : ''}`;
}

// ACTIVITY is a state frame's dot and word, in the Sessions page's colours.
const ACTIVITY = {
    'needs-you': { color: '#d73a49', word: 'needs you' },
    error: { color: '#d73a49', word: 'error' },
    working: { color: '#0969da', word: 'working' },
    idle: { color: '#22863a', word: 'idle' },
    done: { color: '#8b949e', word: 'done' },
};

// ActivityMark shows what the program is doing; its message and title are
// the program's own text, shown as text.
export function ActivityMark({ activity }) {
    const a = ACTIVITY[activity.state] || ACTIVITY.idle;
    const detail = [activity.message, activity.title].filter(Boolean).join(' · ');
    return (
        <span title={detail || a.word} style={{ display: 'inline-flex', alignItems: 'center', gap: '4px', color: a.color, fontWeight: 600 }}>
            <span aria-hidden="true">●</span>{a.word}{activity.message ? <span style={{ color: '#8b949e', fontWeight: 400 }}>: {activity.message}</span> : null}
        </span>
    );
}

const SandboxTerminal = ({ namespace, sandboxName, fill, chat, program }) => {
    const terminalRef = useRef(null);
    const [status, setStatus] = useState('');
    // What the program is doing, from the daemon's state frames.
    const [activity, setActivity] = useState(null);

    useEffect(() => {
        if (!namespace || !sandboxName) return undefined;

        const state = { closed: false, retry: 0, ws: null };
        // Where the daemon's stream is up to; survives reconnects.
        const stream = { offset: null };
        const term = new Terminal({
            cursorBlink: true,
            theme: { background: '#1e1e1e' },
        });
        const fitAddon = new FitAddon();
        term.loadAddon(fitAddon);
        term.open(terminalRef.current);
        fitAddon.fit();

        const sendResize = () => {
            if (state.ws && state.ws.readyState === WebSocket.OPEN) {
                state.ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }));
            }
        };

        const connect = () => {
            if (state.closed) return;
            setStatus(state.retry ? `reconnecting (try ${state.retry})…` : 'connecting…');
            const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
            const socket = new WebSocket(`${protocol}//${window.location.host}${terminalURL(namespace, sandboxName, chat, stream.offset, program)}`);
            socket.binaryType = 'arraybuffer';
            state.ws = socket;

            socket.onopen = () => {
                state.retry = 0;
                setStatus(chat
                    ? `connected — continuing the ${chat} conversation (survives disconnects)`
                    : program
                        ? `connected — ${program} (survives disconnects)`
                        : 'connected — the session survives disconnects');
                fitAddon.fit();
                sendResize();
            };
            socket.onmessage = (event) => {
                const { reset, write, state } = applyFrame(stream, event.data);
                if (state) setActivity(state);
                if (reset) term.reset();
                if (write !== null && write !== '') term.write(write);
            };
            socket.onclose = () => {
                if (state.closed) return;
                state.retry += 1;
                const delay = Math.min(1000 * Math.pow(1.6, state.retry), 10000);
                setStatus(`disconnected — reconnecting in ${Math.round(delay / 1000)}s (the session survives)`);
                setTimeout(connect, delay);
            };
        };

        term.onData((data) => {
            if (state.ws && state.ws.readyState === WebSocket.OPEN) {
                state.ws.send(JSON.stringify({ type: 'input', data }));
            }
        });
        connect();

        const handleResize = () => { fitAddon.fit(); sendResize(); };
        window.addEventListener('resize', handleResize);
        const resizeObserver = new ResizeObserver(handleResize);
        if (terminalRef.current) {
            resizeObserver.observe(terminalRef.current);
        }
        setTimeout(() => fitAddon.fit(), 100);

        return () => {
            state.closed = true;
            if (state.ws) try { state.ws.close(); } catch (e) { /* gone */ }
            term.dispose();
            window.removeEventListener('resize', handleResize);
            resizeObserver.disconnect();
        };
    }, [namespace, sandboxName, chat, program]);

    return (
        <div style={fill
            ? { width: '100%', height: '100%', display: 'flex', flexDirection: 'column', minHeight: 0 }
            : { width: '100%' }}>
            <div style={{ fontSize: 'x-small', color: '#8b949e', textAlign: 'left', padding: '2px 4px', flex: '0 0 auto', display: 'flex', gap: '8px' }}>
                {activity && <ActivityMark activity={activity} />}
                <span>{status}</span>
            </div>
            <div
                ref={terminalRef}
                style={{
                    textAlign: 'left',
                    width: '100%',
                    ...(fill
                        ? { flex: '1 1 auto', minHeight: 0 }
                        : { height: '600px', resize: 'vertical' }),
                    backgroundColor: '#1e1e1e',
                    padding: '10px',
                    overflow: 'hidden',
                }}
            />
        </div>
    );
};

export default SandboxTerminal;
