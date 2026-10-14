import React, { useEffect } from 'react';
import { ResearchConversation } from './Research';

// apiBase is where the session's routes are: /api, the member's (the
// board's), by default; /api/overseers/<name> for an overseer's
// sandboxes (docs/design/overseer-work-view.md), whose session answers
// with no recipes and a draft without actions, so the same component is
// read-only there beyond the conversation.
// SessionSlideOver: a task's session beside the board, 80% of its width,
// the same view as its own tab (which its ↗ pops out to). Escape or the
// backdrop closes it; the session goes on without it.
export default function SessionSlideOver({ session, onClose, apiBase }) {
  useEffect(() => {
    const onKey = (e) => { if (e.key === 'Escape' && !e.defaultPrevented) onClose(); };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);
  const m = session.task.match(/^recipe-([a-z0-9]+)-/);
  const title = `${m ? m[1] : session.task} · ${session.sandbox}`;
  return (
    <>
      <div onClick={onClose} style={{ position: 'fixed', inset: 0, backgroundColor: 'rgba(0,0,0,0.35)', zIndex: 900 }} />
      <div role="dialog" aria-label={title} style={{
        position: 'fixed', top: 0, right: 0, height: '100%', width: '80%',
        backgroundColor: 'var(--bg-card)', borderLeft: '1px solid var(--border-color)',
        boxShadow: '-6px 0 24px rgba(0,0,0,0.25)', zIndex: 901,
        display: 'flex', flexDirection: 'column', textAlign: 'left',
      }}>
        {/* One header: the conversation's, with ↗ (pop out) and ✕. */}
        <div style={{ flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column', padding: '0 12px 12px' }}>
          <ResearchConversation key={`${session.sandbox}/${session.task}`} task={{ sandbox: session.sandbox, task: session.task }}
            title={title} fill onDismiss={onClose} apiBase={apiBase} />
        </div>
      </div>
    </>
  );
}

