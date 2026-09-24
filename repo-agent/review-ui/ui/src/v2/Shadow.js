import React, { useCallback, useEffect, useState } from 'react';

// Platform v2, phase 0: shadow read.
//
// This page renders itself from a spec the API serves — sections with a
// source, a view, and verbs — rather than from hand-placed components.
// Five view types are implemented here in code; everything about which
// sections exist, where their rows come from, and what verbs they offer
// is data (docs/design/platform-v2.md).
//
// Nothing here writes. Every verb renders disabled while the repo is
// v1-managed, because two platforms driving one repo is how you get
// duplicate runs.

const api = (p) => fetch(p).then((r) => (r.ok ? r.json() : Promise.reject(r.status)));

function Chip({ text, color, bg, title }) {
  return (
    <span title={title} style={{
      display: 'inline-block', padding: '1px 8px', borderRadius: '10px',
      fontSize: 'x-small', color, background: bg, whiteSpace: 'nowrap',
    }}>{text}</span>
  );
}

// A verdict is a word; the badge is how it reads at a glance. Unknown
// verdicts render as themselves rather than disappearing — a recipe we
// have never seen should still show its result.
function verdictChip(verdict) {
  if (!verdict) return <span style={{ color: 'var(--text-secondary)' }}>—</span>;
  const v = verdict.toUpperCase();
  const map = [
    ['VERIFIED', '✅', '#28a745', 'rgba(40,167,69,0.10)'],
    ['PLANNED', '📋', '#0366d6', 'rgba(3,102,214,0.08)'],
    ['TORN-DOWN', '🔻', '#6a737d', 'var(--bg-secondary)'],
    ['BLOCKED', '🔒', '#d73a49', 'rgba(215,58,73,0.12)'],
    ['FAILED', '❌', '#d73a49', 'rgba(215,58,73,0.12)'],
    ['DEPLOYED-UNVERIFIED', '⚠', '#b08800', 'rgba(176,136,0,0.12)'],
  ];
  for (const [key, icon, color, bg] of map) {
    if (v.startsWith(key)) return <Chip text={`${icon} ${key.toLowerCase()}`} color={color} bg={bg} />;
  }
  return <Chip text={v.toLowerCase()} color="#6a737d" bg="var(--bg-secondary)" />;
}

function Verbs({ verbs, recipes, readOnly, onRun, target, busy }) {
  const byName = Object.fromEntries((recipes || []).map((r) => [r.name, r]));
  return (
    <span style={{ display: 'inline-flex', gap: '6px', flexWrap: 'wrap' }}>
      {(verbs || []).map((v) => {
        const def = byName[v];
        // Availability and its reason come from the API, so a verb the
        // backend would refuse is visible and explained rather than
        // hidden — you can see the capability and what it needs.
        const unavailable = def && def.available === false;
        const reason = readOnly
          ? 'this repo is v1-managed — shadow read only'
          : unavailable ? def.reason : (def && def.summary) || v;
        return (
          <button key={v} className="btn btn-sm"
            disabled={readOnly || unavailable || busy === v}
            title={reason}
            onClick={onRun ? () => onRun(v, target) : undefined}>
            {busy === v ? `${v}…` : v}
          </button>
        );
      })}
    </span>
  );
}

// ── views ────────────────────────────────────────────────────────────
// Five of them, deliberately. A section picks one by name; adding a
// sixth is a code change, which is the boundary that keeps this from
// becoming a UI framework expressed in YAML.

function InboxView({ rows, readOnly, onRun, busy }) {
  if (!rows.length) return null;
  return (
    <div>
      {rows.map((t) => (
        <div key={t.id} style={{
          display: 'flex', alignItems: 'center', gap: '8px',
          padding: '5px 0', borderTop: '1px solid var(--border-color)',
        }}>
          <span style={{ color: 'var(--text-secondary)', minWidth: '92px' }}>{t.id}</span>
          <a href={t.url} target="_blank" rel="noopener noreferrer"
            style={{ flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis',
              whiteSpace: 'nowrap', color: 'var(--text-primary)', textDecoration: 'none' }}>
            {t.title}
          </a>
          {t.attentionReason && (
            <Chip text={t.attentionReason} color="#d73a49" bg="rgba(215,58,73,0.12)" />
          )}
          <Verbs verbs={(t.recipes || []).map((r) => r.name).slice(0, 3)}
            recipes={t.recipes} readOnly={readOnly} onRun={onRun} target={t.id}
            busy={busy && busy.target === t.id ? busy.recipe : ''} />
        </div>
      ))}
    </div>
  );
}

function TableView({ rows, columns, readOnly, onRun, busy }) {
  if (!rows.length) return <div style={{ color: 'var(--text-secondary)', fontStyle: 'italic' }}>none</div>;
  const cell = { padding: '5px 8px', verticalAlign: 'middle' };
  return (
    <table style={{ width: '100%', borderCollapse: 'collapse' }}>
      <tbody>
        {rows.map((t) => (
          <tr key={t.id} style={{ borderTop: '1px solid var(--border-color)' }}>
            <td style={cell}>
              <a href={t.url} target="_blank" rel="noopener noreferrer"
                style={{ fontWeight: 500, color: 'var(--text-primary)', textDecoration: 'none' }}>
                ⛭ {t.title} ↗
              </a>
            </td>
            <td style={cell}>{verdictChip(t.latestRun && t.latestRun.verdict)}</td>
            <td style={cell}>
              {t.latestRun && t.latestRun.running && (
                <Chip text="running" color="#b08800" bg="rgba(176,136,0,0.12)" />
              )}
            </td>
            <td style={{ ...cell, textAlign: 'right' }}>
              <Verbs verbs={(t.recipes || []).map((r) => r.name)} recipes={t.recipes}
                readOnly={readOnly} onRun={onRun} target={t.id}
                busy={busy && busy.target === t.id ? busy.recipe : ''} />
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function DocListView({ rows }) {
  return (
    <div style={{ display: 'flex', flexWrap: 'wrap', gap: '4px 14px' }}>
      {rows.map((f) => (
        <a key={f.path} href={f.htmlURL} target="_blank" rel="noopener noreferrer">{f.name}</a>
      ))}
      {!rows.length && <span style={{ color: 'var(--text-secondary)', fontStyle: 'italic' }}>nothing yet</span>}
    </div>
  );
}

function ChipsView({ rows }) {
  return (
    <div style={{ display: 'flex', flexWrap: 'wrap', gap: '6px' }}>
      {rows.map((f) => (
        <a key={f.path} href={f.htmlURL} target="_blank" rel="noopener noreferrer"
          style={{ border: '1px solid var(--border-color)', borderRadius: '12px',
            padding: '2px 10px', textDecoration: 'none' }}>
          {f.name.replace(/\.md$/, '')}
        </a>
      ))}
      {!rows.length && <span style={{ color: 'var(--text-secondary)', fontStyle: 'italic' }}>none yet</span>}
    </div>
  );
}

function LogView({ rows }) {
  if (!rows.length) return <div style={{ color: 'var(--text-secondary)', fontStyle: 'italic' }}>no runs recorded</div>;
  return (
    <div>
      {rows.map((r, i) => (
        <div key={i} style={{ display: 'flex', gap: '8px', alignItems: 'center', padding: '3px 0' }}>
          {verdictChip(r.verdict)}
          <span style={{ color: 'var(--text-secondary)' }}>{r.recipe}</span>
          <span>{r.target}</span>
          <span style={{ color: 'var(--text-secondary)' }}>{(r.at || '').replace('T', ' ').replace(':00Z', '')}</span>
          <span style={{ flex: 1 }} />
          {r.url && <a href={r.url} target="_blank" rel="noopener noreferrer">receipt ↗</a>}
        </div>
      ))}
    </div>
  );
}

const VIEWS = { inbox: InboxView, table: TableView, doclist: DocListView, chips: ChipsView, log: LogView };

// ── section ──────────────────────────────────────────────────────────

function Section({ repo, spec, recipes, readOnly, onRan }) {
  const [rows, setRows] = useState([]);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(null);

  // Starting work is one POST: a Run is created, and the reconciler
  // decides everything else. The UI does not know what a sandbox is.
  const run = (recipe, target) => {
    setBusy({ recipe, target: target || 'repo' });
    fetch(`/api/v2/repos/${repo}/runs`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ recipe, target: target || 'repo' }),
    })
      .then(async (r) => {
        if (!r.ok) {
          const body = await r.json().catch(() => ({}));
          throw new Error(body.error || `HTTP ${r.status}`);
        }
      })
      .catch((e) => setError(String(e.message || e)))
      .finally(() => {
        setTimeout(() => setBusy(null), 1500);
        if (onRan) onRan();
      });
  };

  const load = useCallback(() => {
    // Sections mount before the board list resolves; fetching with an
    // empty repo asks for /repos//targets and earns a 403 that would
    // otherwise stick, because a later success never cleared it.
    if (!repo) return;
    setError('');
    const src = spec.source || {};
    let url = null;
    if (src.targets) {
      const q = new URLSearchParams(src.targets).toString();
      url = `/api/v2/repos/${repo}/targets?${q}`;
    } else if (src.artifacts) {
      url = `/api/v2/repos/${repo}/artifacts?glob=${encodeURIComponent(src.artifacts)}`;
    } else if (src.runs) {
      url = `/api/v2/repos/${repo}/runs`;
    }
    if (!url) { setError('unknown source'); return; }
    api(url)
      .then((data) => {
        let out = Array.isArray(data) ? data : [];
        // Exclusions are part of the source, not the view: a doc list
        // shows understanding, not the recipes and receipts that other
        // sections own.
        if (src.exclude) {
          out = out.filter((f) => !src.exclude.some((p) => (f.name || '').startsWith(p)));
        }
        setRows(out);
      })
      .catch((e) => setError(`could not load (${e})`));
  }, [repo, spec]);

  useEffect(() => { load(); }, [load]);

  if (!repo) return null;

  // An unknown view is skipped, never fatal — layout data must never be
  // load-bearing (the same rule runbook parsing follows).
  const View = VIEWS[spec.view];
  if (!View) return null;

  return (
    <div style={{ marginBottom: '16px' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '10px', marginBottom: '4px' }}>
        <span style={{ fontWeight: 700, letterSpacing: '0.04em', fontSize: 'small' }}>
          {(spec.title || spec.id).toUpperCase()}
        </span>
        <Verbs verbs={spec.verbs} recipes={recipes} readOnly={readOnly}
          onRun={run} target="repo" busy={busy && busy.target === 'repo' ? busy.recipe : ''} />
        <span style={{ flex: 1 }} />
        <span style={{ fontSize: 'x-small', color: 'var(--text-secondary)' }}>{spec.view}</span>
      </div>
      {error ? (
        <div style={{ color: 'var(--danger, #d33)', fontStyle: 'italic' }}>{error}</div>
      ) : rows.length === 0 && spec.empty ? (
        <div style={{ color: 'var(--status-green, #28a745)' }}>{spec.empty}</div>
      ) : (
        <View rows={rows} columns={spec.columns} readOnly={readOnly} onRun={run}
          busy={busy} />
      )}
    </div>
  );
}

// ── page ─────────────────────────────────────────────────────────────

export default function Shadow({ onBack }) {
  const [repos, setRepos] = useState([]);
  const [repo, setRepo] = useState('');
  const [info, setInfo] = useState(null);
  const [page, setPage] = useState(null);
  const [recipes, setRecipes] = useState([]);
  // Starting a run should show up without a manual reload; remounting
  // sections is the bluntest correct way to refetch every source.
  const [tick, setTick] = useState(0);

  useEffect(() => {
    api('/api/boards').then((bs) => {
      const names = (bs || []).map((b) => b.name);
      setRepos(names);
      setRepo((prev) => prev || names[0] || '');
    }).catch(() => {});
    api('/api/v2/pages/repo').then(setPage).catch(() => {});
    api('/api/v2/recipes').then(setRecipes).catch(() => {});
  }, []);

  useEffect(() => {
    if (!repo) return;
    api(`/api/v2/repos/${repo}`).then(setInfo).catch(() => setInfo(null));
  }, [repo]);

  const readOnly = !info || info.readOnly;

  return (
    <div style={{ padding: '14px', textAlign: 'left', fontSize: 'small' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '10px', marginBottom: '10px' }}>
        <strong style={{ fontSize: 'medium' }}>v2 preview</strong>
        <select value={repo} onChange={(e) => setRepo(e.target.value)} style={{ padding: '3px' }}>
          {repos.map((r) => <option key={r} value={r}>{r}</option>)}
        </select>
        {info && <span style={{ color: 'var(--text-secondary)' }}>{info.repo} · {info.engine}</span>}
        <span style={{ flex: 1 }} />
        {onBack && <button className="btn btn-sm" onClick={onBack}>back to v1</button>}
      </div>

      {readOnly ? (
        <div style={{ border: '1px solid #b08800', borderRadius: '10px', padding: '8px 12px',
          marginBottom: '12px', color: '#b08800', background: 'rgba(176,136,0,0.08)' }}>
          Shadow read — this repo is <strong>{(info && info.platform) || 'v1'}</strong>-managed.
          Every verb is disabled here; v1 remains the only writer, because two platforms
          driving one repo is how duplicate runs happen.
        </div>
      ) : (
        <div style={{ border: '1px solid #28a745', borderRadius: '10px', padding: '8px 12px',
          marginBottom: '12px', color: '#28a745', background: 'rgba(40,167,69,0.08)' }}>
          v2-managed — recipes run here, and v1 leaves this repo alone. Every verb creates a
          Run; watch it in Activity.
        </div>
      )}

      {!page ? (
        <div style={{ color: 'var(--text-secondary)', fontStyle: 'italic' }}>loading page spec…</div>
      ) : (
        (page.sections || []).map((spec) => (
          <Section key={`${spec.id}:${tick}`} repo={repo} spec={spec} recipes={recipes}
            readOnly={readOnly} onRan={() => setTimeout(() => setTick((n) => n + 1), 1500)} />
        ))
      )}

      <div style={{ marginTop: '18px', paddingTop: '8px', borderTop: '1px dashed var(--border-color)',
        color: 'var(--text-secondary)', fontSize: 'x-small' }}>
        This page is rendered from <code>/api/v2/pages/repo</code> — sections, sources and verbs are
        data; the five view types are code. Compare it against v1 on the same repo: the rows should
        agree, because targets of kind issue and pr come from the same builder v1 renders.
      </div>
    </div>
  );
}
