import React, { useState, useEffect, useCallback, useRef } from 'react';
import { Terminal as XTerm } from 'xterm';
import { FitAddon } from 'xterm-addon-fit';
import 'xterm/css/xterm.css';
import Chip from './Chip';

// apiBase is where the card's routes are: /api, the member's (the
// board's), by default; /api/overseers/<name> for an overseer's
// sandboxes (docs/design/overseer-work-view.md).
// SandboxCard: the half-screen overlay behind every sandbox chip — task
// history (newest first) with expandable log tails and Follow, plus
// wake/pause. Terminal and richer lifecycle land here later.
export default function SandboxCard({ name, namespace, onClose, apiBase = '/api' }) {
  const [card, setCard] = useState(null);
  const [err, setErr] = useState('');
  const [openTask, setOpenTask] = useState('');
  const [logText, setLogText] = useState('');
  const [follow, setFollow] = useState(false);
  const logRef = useRef(null);
  const [showTerminal, setShowTerminal] = useState(false);
  const [expanded, setExpanded] = useState(false);
  const [termStatus, setTermStatus] = useState('');
  const termHostRef = useRef(null);
  const termRef = useRef(null); // { term, fit, ws, closed }

  // The terminal: xterm ↔ ws ↔ pods/exec attaching `tmux new -As board`.
  // The session lives in tmux, so a dropped socket loses nothing — we
  // just reconnect into the same session (with backoff) until the pane
  // is closed.
  useEffect(() => {
    if (!showTerminal || !termHostRef.current) return undefined;
    const state = { closed: false, retry: 0, ws: null };
    termRef.current = state;
    const term = new XTerm({ fontSize: 12, cursorBlink: true, theme: { background: '#0d1117' } });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(termHostRef.current);
    fit.fit();
    state.term = term;

    // Refit when the card itself resizes (expand toggle, drag) — width
    // changes must reach tmux, not just window resizes.
    const hostObserver = new ResizeObserver(() => { try { fit.fit(); sendResize(); } catch (e) { /* detached */ } });
    hostObserver.observe(termHostRef.current);

    const sendResize = () => {
      if (state.ws && state.ws.readyState === 1) {
        state.ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }));
      }
    };
    const onWindowResize = () => { try { fit.fit(); sendResize(); } catch (e) { /* detached */ } };
    window.addEventListener('resize', onWindowResize);

    const connect = () => {
      if (state.closed) return;
      setTermStatus(state.retry ? `reconnecting (try ${state.retry})…` : 'connecting…');
      const proto = window.location.protocol === 'https:' ? 'wss' : 'ws';
      const ws = new WebSocket(`${proto}://${window.location.host}${apiBase}/sandbox-card/${name}/terminal`);
      ws.binaryType = 'arraybuffer';
      state.ws = ws;
      ws.onopen = () => { state.retry = 0; setTermStatus('connected'); fit.fit(); sendResize(); };
      ws.onmessage = (ev) => {
        term.write(typeof ev.data === 'string' ? ev.data : new Uint8Array(ev.data));
      };
      ws.onclose = () => {
        if (state.closed) return;
        state.retry += 1;
        const delay = Math.min(1000 * Math.pow(1.6, state.retry), 10000);
        setTermStatus(`disconnected — reconnecting in ${Math.round(delay / 1000)}s (session survives in tmux)`);
        setTimeout(connect, delay);
      };
    };
    term.onData((data) => {
      if (state.ws && state.ws.readyState === 1) state.ws.send(JSON.stringify({ type: 'input', data }));
    });
    connect();

    return () => {
      state.closed = true;
      hostObserver.disconnect();
      window.removeEventListener('resize', onWindowResize);
      if (state.ws) try { state.ws.close(); } catch (e) { /* gone */ }
      term.dispose();
      termRef.current = null;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [showTerminal, name, apiBase]);

  const load = useCallback(() => {
    fetch(`${apiBase}/sandbox-card/${name}`)
      .then(res => (res.ok ? res.json() : Promise.reject(res.statusText)))
      .then(setCard)
      .catch(e => setErr(`load failed: ${e}`));
  }, [name, apiBase]);
  useEffect(() => { setCard(null); setOpenTask(''); setLogText(''); setFollow(false); load(); }, [load]);

  const loadLog = useCallback((task) => {
    fetch(`${apiBase}/sandbox-card/${name}/log?task=${encodeURIComponent(task)}`)
      .then(res => res.text())
      .then(text => {
        setLogText(text);
        if (logRef.current) logRef.current.scrollTop = logRef.current.scrollHeight;
      })
      .catch(e => setLogText(`log fetch failed: ${e}`));
  }, [name, apiBase]);

  useEffect(() => {
    if (!follow || !openTask) return undefined;
    const t = setInterval(() => loadLog(openTask), 3000);
    return () => clearInterval(t);
  }, [follow, openTask, loadLog]);

  useEffect(() => {
    const onKey = (e) => { if (e.key === 'Escape') onClose(); };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  const lifecycle = (action) => {
    fetch(`${apiBase}/sandbox-card/${name}/lifecycle`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ action }),
    }).then(res => {
      if (!res.ok) { res.text().then(t => setErr(`${action} failed: ${t}`)); return; }
      if (action === 'delete') { onClose(); return; } // the card's subject is gone
      setTimeout(load, 1500);
    });
  };

  const statusColor = { running: '#b08800', succeeded: '#22863a', failed: '#d73a49', aborted: '#6a737d' };
  return (
    <>
      <div onClick={onClose} style={{ position: 'fixed', inset: 0, backgroundColor: 'rgba(0,0,0,0.35)', zIndex: 900 }} />
      <div style={{
        position: 'fixed', top: 0, right: 0, height: '100%', width: expanded ? '80%' : 'min(640px, 55%)',
        backgroundColor: 'var(--bg-card)', borderLeft: '1px solid var(--border-color)',
        boxShadow: '-6px 0 24px rgba(0,0,0,0.25)', zIndex: 901, overflowY: 'auto',
        padding: '14px 16px', textAlign: 'left',
      }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
          <strong style={{ fontSize: 'small', overflow: 'hidden', textOverflow: 'ellipsis' }}>{name}</strong>
          {card && (
            <Chip
              text={card.paused ? 'paused' : card.starting ? 'starting' : (card.taskState || 'active').toLowerCase()}
              color={card.paused ? '#6a737d' : '#22863a'}
              bg={card.paused ? 'rgba(106,115,125,0.12)' : 'rgba(34,134,58,0.12)'}
            />
          )}
          <span style={{ marginLeft: 'auto', display: 'flex', gap: '6px' }}>
            {card && card.paused && (
              <button className="btn btn-sm" onClick={() => lifecycle('wake')} title="Scale the sandbox back up to inspect tasks and logs">Wake</button>
            )}
            {card && !card.paused && !card.starting && (
              <button className="btn btn-sm" onClick={() => setShowTerminal(v => { const next = !v; if (next) setExpanded(true); return next; })}
                title="Shell into the sandbox — the session lives in tmux and survives disconnects">
                {showTerminal ? 'Hide terminal' : 'Terminal'}
              </button>
            )}
            {card && !card.paused && !card.starting && (
              <button className="btn btn-sm" onClick={() => lifecycle('pause')} title="Scale the sandbox to zero (state stays on its disk)">Pause</button>
            )}
            {/* Restart is the other recovery path, short of Delete: a
                session stuck "working" (its engine never ended the turn)
                or a pod that was evicted. Starting covers the evicted
                pod, which never comes back by itself. */}
            {card && !card.paused && (
              <button className="btn btn-sm"
                title="Restart the sandbox's pod on the same disk: ends a stuck agent turn or brings back an evicted pod. Anything running in it stops; the checkout and conversations stay."
                onClick={() => {
                  if (!window.confirm(`Restart sandbox ${name}?\n\nIts pod starts again on the same disk: anything running in it stops (a stuck agent turn, builds, tests). The checkout, drafts and conversations stay.`)) return;
                  lifecycle('restart');
                }}>Restart</button>
            )}
            {/* Delete is the wedge-recovery path, so it must stay
                available in exactly the states a wedge presents as:
                paused, starting-forever, or running. */}
            {card && (
              <button className="btn btn-sm"
                style={{ marginLeft: '8px', color: 'var(--danger, #d33)', borderColor: 'var(--danger, #d33)' }}
                title="Recover from a wedged sandbox by deleting it — destroys the checkout, plan drafts and approvals, review breadcrumbs, and chat sessions. The next agent action recreates it fresh."
                onClick={() => {
                  if (!window.confirm(`Delete sandbox ${name}?\n\nThis destroys its checkout, plan drafts/approvals, review breadcrumbs, and chat sessions. The next agent action recreates it fresh.`)) return;
                  lifecycle('delete');
                }}>Delete</button>
            )}
            <button className="btn btn-sm" onClick={() => setExpanded(v => !v)} title={expanded ? 'Shrink the card' : 'Expand the card to 80%'}>{expanded ? '⇥' : '⛶'}</button>
            <button className="btn btn-sm" onClick={load} title="Refresh">↻</button>
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: '4px' }}>
              <button className="btn btn-sm" onClick={onClose} title="Close (Esc)">✕</button>
              <kbd style={{ fontSize: 'x-small', color: 'var(--text-secondary)', border: '1px solid var(--border-color)', borderRadius: '3px', padding: '0 4px' }}>esc</kbd>
            </span>
          </span>
        </div>
        {err && <div style={{ color: 'var(--danger, #d33)', fontSize: 'small', marginTop: '8px' }}>{err}</div>}
        {!card && !err && <div style={{ color: 'var(--text-secondary)', fontStyle: 'italic', marginTop: '12px' }}>Loading…</div>}
        {card && card.paused && (
          <div style={{ color: 'var(--text-secondary)', fontSize: 'small', marginTop: '12px' }}>
            Sandbox is paused — its task history lives on the workspace disk. Wake it to browse tasks and logs.
          </div>
        )}
        {card && card.starting && (
          <div style={{ color: 'var(--text-secondary)', fontSize: 'small', marginTop: '12px' }}>
            Pod is starting — tasks will appear once it is ready.
          </div>
        )}
        {showTerminal && (
          <div style={{ marginTop: '12px' }}>
            <div style={{ display: 'flex', alignItems: 'center', marginBottom: '4px' }}>
              <span style={{ fontSize: 'small', fontWeight: 700, letterSpacing: '0.04em', color: 'var(--text-secondary)' }}>TERMINAL</span>
              <span style={{ marginLeft: '8px', fontSize: 'x-small', color: 'var(--text-secondary)' }}>{termStatus}</span>
              <a href={`#/terminal/${namespace}/${name}`} target="_blank" rel="noopener noreferrer"
                className="btn btn-sm" style={{ marginLeft: 'auto', textDecoration: 'none' }}
                title="Pop out into its own window — same tmux session, real window management">↗ pop out</a>
            </div>
            <div ref={termHostRef} style={{ height: 'calc(100vh - 300px)', minHeight: '260px', backgroundColor: '#0d1117', borderRadius: '6px', padding: '4px' }} />
          </div>
        )}
        {card && !card.paused && !card.starting && (
          <div style={{ marginTop: '12px' }}>
            <div style={{ fontSize: 'small', fontWeight: 700, letterSpacing: '0.04em', color: 'var(--text-secondary)', marginBottom: '6px' }}>
              TASKS ({card.tasks.length})
            </div>
            {!card.tasks.length && <div style={{ color: 'var(--text-secondary)', fontSize: 'small' }}>No tasks have run in this sandbox yet.</div>}
            {card.tasks.map(task => (
              <div key={task.name} style={{ borderTop: '1px solid var(--border-color)', padding: '6px 0' }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: '8px', cursor: 'pointer' }}
                  onClick={() => {
                    const next = openTask === task.name ? '' : task.name;
                    setOpenTask(next); setLogText(''); setFollow(false);
                    if (next) loadLog(next);
                  }}>
                  <span style={{ fontSize: 'small' }}>{openTask === task.name ? '▾' : '▸'} <strong>{task.type}</strong></span>
                  <span style={{ fontSize: 'x-small', color: 'var(--text-secondary)' }}>{task.startedAt}</span>
                  <Chip text={task.status + (task.exitCode && task.exitCode !== '0' ? ` (${task.exitCode})` : '')}
                    color={statusColor[task.status] || 'var(--text-secondary)'}
                    bg={`color-mix(in srgb, ${statusColor[task.status] || 'var(--text-secondary)'} 12%, transparent)`} />
                  <span style={{ marginLeft: 'auto', fontSize: 'x-small', color: 'var(--text-secondary)' }}>{Math.round(task.logBytes / 1024)}k log</span>
                </div>
                {openTask === task.name && (
                  <div style={{ marginTop: '6px' }}>
                    <div style={{ display: 'flex', gap: '8px', alignItems: 'center', marginBottom: '4px' }}>
                      <label style={{ fontSize: 'x-small', cursor: 'pointer' }}>
                        <input type="checkbox" checked={follow} onChange={e => setFollow(e.target.checked)} /> Follow
                      </label>
                      <button className="btn btn-sm" onClick={() => loadLog(task.name)}>↻</button>
                    </div>
                    <pre ref={logRef} style={{
                      whiteSpace: 'pre-wrap', fontSize: 'x-small', margin: 0, padding: '8px',
                      backgroundColor: 'var(--bg-secondary)', borderRadius: '6px',
                      maxHeight: '45vh', overflowY: 'auto', textAlign: 'left',
                    }}>{logText || '…'}</pre>
                  </div>
                )}
              </div>
            ))}
          </div>
        )}
      </div>
    </>
  );
}

