import React, { useEffect, useRef } from 'react';
import { Terminal } from 'xterm';
import { FitAddon } from 'xterm-addon-fit';
import 'xterm/css/xterm.css';

const SandboxTerminal = ({ namespace, sandboxName }) => {
    const terminalRef = useRef(null);

    useEffect(() => {
        if (!namespace || !sandboxName) return;

        const term = new Terminal({
            cursorBlink: true,
            theme: {
                background: '#1e1e1e',
            }
        });
        const fitAddon = new FitAddon();
        term.loadAddon(fitAddon);

        term.open(terminalRef.current);
        fitAddon.fit();

        const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
        // The path must match the backend route
        const socket = new WebSocket(`${protocol}//${window.location.host}/api/terminal/${namespace}/${sandboxName}`);
        socket.binaryType = 'arraybuffer';

        socket.onopen = () => {
            term.write('\r\n*** Connected to sandbox terminal ***\r\n');
            // Trigger a resize to ensure the terminal size is correct on the backend if we implement that
            fitAddon.fit();
            if (socket.readyState === WebSocket.OPEN) {
                socket.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }));
            }
        };

        socket.onmessage = (event) => {
            if (typeof event.data === 'string') {
                term.write(event.data);
            } else {
                term.write(new Uint8Array(event.data));
            }
        };

        socket.onclose = (event) => {
            term.write(`\r\n*** Connection closed (Code: ${event.code}) ***\r\n`);
        };

        socket.onerror = (error) => {
             console.error('WebSocket error:', error);
             term.write('\r\n*** WebSocket error ***\r\n');
        }

        term.onData(data => {
            if (socket.readyState === WebSocket.OPEN) {
                socket.send(JSON.stringify({ type: 'input', data }));
            }
        });

        const handleResize = () => {
            fitAddon.fit();
            if (socket.readyState === WebSocket.OPEN) {
                socket.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }));
            }
        };

        window.addEventListener('resize', handleResize);
        
        // Use ResizeObserver to detect container resize (e.g. via CSS resize property)
        const resizeObserver = new ResizeObserver(() => {
            handleResize();
        });
        if (terminalRef.current) {
            resizeObserver.observe(terminalRef.current);
        }

        // Wait a bit for layout to settle then fit
        setTimeout(() => fitAddon.fit(), 100);

        return () => {
            socket.close();
            term.dispose();
            window.removeEventListener('resize', handleResize);
            resizeObserver.disconnect();
        };
    }, [namespace, sandboxName]);

    return (
        <div 
            ref={terminalRef} 
            style={{ 
                textAlign: 'left',
                width: '100%', 
                height: '600px', 
                backgroundColor: '#1e1e1e', 
                padding: '10px',
                resize: 'vertical',
                overflow: 'hidden'
            }} 
        />
    );
};

export default SandboxTerminal;
