import React, { useEffect, useRef, useState } from 'react';
import { Terminal } from 'xterm';
import { FitAddon } from 'xterm-addon-fit';
import 'xterm/css/xterm.css';

// The Overseer tab's terminal, on the tmux-backed transport the sandbox
// card uses: the session lives in tmux inside the pod, so a dropped
// socket loses nothing — this component reconnects with backoff into the
// same session instead of dying with a "connection closed" epitaph.
const SandboxTerminal = ({ namespace, sandboxName, fill }) => {
    const terminalRef = useRef(null);
    const [status, setStatus] = useState('');

    useEffect(() => {
        if (!namespace || !sandboxName) return undefined;

        const state = { closed: false, retry: 0, ws: null };
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
            const socket = new WebSocket(`${protocol}//${window.location.host}/api/terminal/${namespace}/${sandboxName}`);
            socket.binaryType = 'arraybuffer';
            state.ws = socket;

            socket.onopen = () => {
                state.retry = 0;
                setStatus('connected — session lives in tmux and survives disconnects');
                fitAddon.fit();
                sendResize();
            };
            socket.onmessage = (event) => {
                term.write(typeof event.data === 'string' ? event.data : new Uint8Array(event.data));
            };
            socket.onclose = () => {
                if (state.closed) return;
                state.retry += 1;
                const delay = Math.min(1000 * Math.pow(1.6, state.retry), 10000);
                setStatus(`disconnected — reconnecting in ${Math.round(delay / 1000)}s (session survives in tmux)`);
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
    }, [namespace, sandboxName]);

    return (
        <div style={{ width: '100%' }}>
            <div style={{ fontSize: 'x-small', color: '#8b949e', textAlign: 'left', padding: '2px 4px' }}>{status}</div>
            <div
                ref={terminalRef}
                style={{
                    textAlign: 'left',
                    width: '100%',
                    height: fill ? 'calc(100% - 20px)' : '600px',
                    backgroundColor: '#1e1e1e',
                    padding: '10px',
                    resize: 'vertical',
                    overflow: 'hidden',
                }}
            />
        </div>
    );
};

export default SandboxTerminal;
