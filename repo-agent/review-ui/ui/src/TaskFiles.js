import React, { useCallback, useEffect, useState } from 'react';

// TaskFiles is the Files tab of a task's session page: what the task left in
// its directory (its result, step logs, transcript, and the evidence its
// recipe kept, such as validate-kind's receipts and runs), read live from
// its sandbox or, once the sandbox is reaped, from the archive
// (handlers_task_files.go). A file opens in place; its link opens it again.

// taskFilesURL is the API path of a task's file list, or of one file.
export function taskFilesURL(apiBase, sandbox, task, file) {
  const base = `${apiBase}/task-files/${encodeURIComponent(sandbox)}/${encodeURIComponent(task)}`;
  if (!file) return base;
  return `${base}/file/${file.split('/').map(encodeURIComponent).join('/')}`;
}

// groupTaskFiles splits a task's files into the top level and each
// directory one down, in the order the server sent them.
export function groupTaskFiles(files) {
  const groups = [];
  const byDir = {};
  for (const f of files || []) {
    const slash = f.name.indexOf('/');
    const dir = slash < 0 ? '' : f.name.slice(0, slash);
    if (!byDir[dir]) {
      byDir[dir] = { dir, files: [] };
      groups.push(byDir[dir]);
    }
    byDir[dir].files.push(f);
  }
  return groups;
}

// formatSize is a file size for people.
export function formatSize(bytes) {
  if (!bytes) return '';
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

// prettyContent is a file's text as shown: a JSON document indented, every
// other file as it is.
export function prettyContent(name, text) {
  if (/\.json$/.test(name)) {
    try {
      return JSON.stringify(JSON.parse(text), null, 2);
    } catch (e) {
      return text;
    }
  }
  return text;
}

// withFileParam is the page's hash with the open file (and the Files tab)
// in its query, so its link opens it again.
export function withFileParam(hash, file) {
  const [path, query = ''] = hash.split('?');
  const params = new URLSearchParams(query);
  params.set('tab', 'files');
  if (file) params.set('file', file); else params.delete('file');
  return `${path}?${params.toString()}`;
}

export default function TaskFiles({ apiBase, sandbox, task, initialFile }) {
  const [listing, setListing] = useState(null);
  const [error, setError] = useState('');
  const [open, setOpen] = useState(initialFile || '');
  const [content, setContent] = useState(null);
  const [fileError, setFileError] = useState('');

  useEffect(() => {
    let cancelled = false;
    fetch(taskFilesURL(apiBase, sandbox, task))
      .then(async res => {
        const body = await res.json().catch(() => ({}));
        if (!res.ok) throw new Error(body.error || res.statusText);
        return body;
      })
      .then(body => { if (!cancelled) setListing(body); })
      .catch(err => { if (!cancelled) setError(err.message); });
    return () => { cancelled = true; };
  }, [apiBase, sandbox, task]);

  const openFile = useCallback(name => {
    setOpen(name);
    window.history.replaceState(null, '', withFileParam(window.location.hash, name));
  }, []);

  useEffect(() => {
    if (!open) { setContent(null); return undefined; }
    let cancelled = false;
    setContent(null);
    setFileError('');
    fetch(taskFilesURL(apiBase, sandbox, task, open))
      .then(async res => {
        const text = await res.text();
        if (!res.ok) {
          let msg = res.statusText;
          try { msg = JSON.parse(text).error || msg; } catch (e) { /* not JSON */ }
          throw new Error(msg);
        }
        return text;
      })
      .then(text => { if (!cancelled) setContent(prettyContent(open, text)); })
      .catch(err => { if (!cancelled) setFileError(err.message); });
    return () => { cancelled = true; };
  }, [apiBase, sandbox, task, open]);

  if (error) return <div className="task-files" style={{ padding: 16, textAlign: 'left', color: 'var(--danger, #c62828)' }}>Could not list this task's files: {error}</div>;
  if (!listing) return <div className="task-files" style={{ padding: 16, textAlign: 'left' }}>Loading files…</div>;

  const groups = groupTaskFiles(listing.files);
  return (
    // Left-aligned: the page sits in .App, whose text-align is center (App.css).
    <div className="task-files" style={{ display: 'flex', flex: 1, minHeight: 0, textAlign: 'left' }}>
      <div style={{ width: 320, overflowY: 'auto', borderRight: '1px solid var(--border, #ddd)', padding: 8 }}>
        <div style={{ fontSize: 12, opacity: 0.75, marginBottom: 8 }} data-testid="task-files-source">
          {listing.source === 'archive' ? 'From the archive (the sandbox is gone)' : 'Live from the sandbox'}
          {' · '}{listing.files.length} files
        </div>
        {groups.map(g => (
          <div key={g.dir || '.'} style={{ marginBottom: 8 }}>
            {g.dir && <div style={{ fontWeight: 600, fontSize: 12, margin: '6px 0 2px' }}>{g.dir}/</div>}
            {g.files.map(f => (
              <button key={f.name} type="button" onClick={() => openFile(f.name)} title={f.name}
                style={{
                  display: 'flex', width: '100%', justifyContent: 'space-between', gap: 8, textAlign: 'left',
                  padding: '3px 6px', border: 0, borderRadius: 4, cursor: 'pointer', fontFamily: 'monospace', fontSize: 12,
                  background: f.name === open ? 'var(--selected, rgba(66,133,244,0.15))' : 'transparent', color: 'inherit',
                }}>
                <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                  {g.dir ? f.name.slice(g.dir.length + 1) : f.name}
                </span>
                <span style={{ opacity: 0.6, flexShrink: 0 }}>{formatSize(f.size)}</span>
              </button>
            ))}
          </div>
        ))}
      </div>
      <div style={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column' }}>
        {open ? (
          <>
            <div style={{ display: 'flex', gap: 12, alignItems: 'center', padding: '6px 12px', borderBottom: '1px solid var(--border, #ddd)', fontSize: 12 }}>
              <code style={{ flex: 1, overflow: 'hidden', textOverflow: 'ellipsis' }}>{open}</code>
              <a href={taskFilesURL(apiBase, sandbox, task, open)} target="_blank" rel="noreferrer">raw</a>
              <button type="button" onClick={() => navigator.clipboard && navigator.clipboard.writeText(window.location.href)}>copy link</button>
            </div>
            {fileError
              ? <div style={{ padding: 12, color: 'var(--danger, #c62828)' }}>Could not read {open}: {fileError}</div>
              : <pre data-testid="task-file-content" style={{ flex: 1, margin: 0, padding: 12, overflow: 'auto', fontSize: 12, whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
                  {content === null ? 'Loading…' : content}
                </pre>}
          </>
        ) : (
          <div style={{ padding: 16, opacity: 0.7 }}>Pick a file to view it.</div>
        )}
      </div>
    </div>
  );
}
