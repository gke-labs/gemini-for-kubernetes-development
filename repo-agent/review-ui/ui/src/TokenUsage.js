import React, { useState, useEffect, useCallback } from 'react';
import './App.css';

const fmt = (n) => (n || 0).toLocaleString();

// Sum per-model stats into one row: { requests, errors, input, output, cached, thoughts, total }
const sumStats = (stats) => {
    const acc = { requests: 0, errors: 0, input: 0, output: 0, cached: 0, thoughts: 0, total: 0 };
    Object.values(stats?.models || {}).forEach(mu => {
        acc.requests += mu.api?.totalRequests || 0;
        acc.errors += mu.api?.totalErrors || 0;
        acc.input += mu.tokens?.input || 0;
        acc.output += mu.tokens?.output || 0;
        acc.cached += mu.tokens?.cached || 0;
        acc.thoughts += mu.tokens?.thoughts || 0;
        acc.total += mu.tokens?.total || 0;
    });
    return acc;
};

const thStyle = { padding: '10px 16px', borderBottom: '2px solid var(--border-color)', color: 'var(--text-secondary)', fontSize: '13px', textTransform: 'uppercase' };
const tdStyle = { padding: '10px 16px', borderBottom: '1px solid var(--border-color)', color: 'var(--text-primary)' };
const numStyle = { ...tdStyle, textAlign: 'right', fontVariantNumeric: 'tabular-nums' };

const TokenCells = ({ s }) => (
    <>
        <td style={numStyle}>{fmt(s.requests)}</td>
        <td style={numStyle}>{fmt(s.errors)}</td>
        <td style={numStyle}>{fmt(s.input)}</td>
        <td style={numStyle}>{fmt(s.output)}</td>
        <td style={numStyle}>{fmt(s.cached)}</td>
        <td style={numStyle}>{fmt(s.thoughts)}</td>
        <td style={{ ...numStyle, fontWeight: 'bold' }}>{fmt(s.total)}</td>
    </>
);

const TokenHeaderCells = () => (
    <>
        <th style={{ ...thStyle, textAlign: 'right' }}>Requests</th>
        <th style={{ ...thStyle, textAlign: 'right' }}>Errors</th>
        <th style={{ ...thStyle, textAlign: 'right' }}>Input</th>
        <th style={{ ...thStyle, textAlign: 'right' }}>Output</th>
        <th style={{ ...thStyle, textAlign: 'right' }}>Cached</th>
        <th style={{ ...thStyle, textAlign: 'right' }}>Thoughts</th>
        <th style={{ ...thStyle, textAlign: 'right' }}>Total</th>
    </>
);

const RollupTable = ({ title, rollups, keyLabel, renderKey, expanded, onToggle, records }) => (
    <div style={{ backgroundColor: 'var(--bg-card)', borderRadius: '8px', boxShadow: 'var(--shadow-card)', padding: '16px', marginBottom: '24px' }}>
        <h3 style={{ margin: '0 0 12px 4px', color: 'var(--text-primary)' }}>{title}</h3>
        {(!rollups || rollups.length === 0) ? (
            <p style={{ color: 'var(--text-secondary)', margin: '4px' }}>No usage recorded yet.</p>
        ) : (
            <table style={{ width: '100%', borderCollapse: 'collapse', textAlign: 'left' }}>
                <thead>
                    <tr>
                        <th style={thStyle}>{keyLabel}</th>
                        <th style={{ ...thStyle, textAlign: 'right' }}>Tasks</th>
                        <TokenHeaderCells />
                    </tr>
                </thead>
                <tbody>
                    {rollups.map(r => {
                        const s = sumStats(r.stats);
                        const isOpen = expanded === r.key;
                        return (
                            <React.Fragment key={r.key}>
                                <tr className="table-row-hover" style={{ cursor: onToggle ? 'pointer' : 'default' }}
                                    onClick={onToggle ? () => onToggle(isOpen ? null : r.key) : undefined}>
                                    <td style={{ ...tdStyle, fontWeight: 'bold' }}>
                                        {onToggle && <span style={{ marginRight: '6px' }}>{isOpen ? '▾' : '▸'}</span>}
                                        {renderKey ? renderKey(r) : r.key}
                                    </td>
                                    <td style={numStyle}>{fmt(r.taskCount)}</td>
                                    <TokenCells s={s} />
                                </tr>
                                {isOpen && (records || r.records || []).map(rec => (
                                    <tr key={rec.key} style={{ backgroundColor: 'var(--bg-comment-card)' }}>
                                        <td style={{ ...tdStyle, paddingLeft: '36px', fontSize: '13px' }}>
                                            {rec.taskType || '-'} · {rec.sandbox || '-'}
                                            {rec.pr ? ` · PR #${rec.pr}` : ''}{rec.issue ? ` · issue #${rec.issue}` : ''}
                                        </td>
                                        <td style={numStyle}>-</td>
                                        <TokenCells s={sumStats(rec.stats)} />
                                    </tr>
                                ))}
                            </React.Fragment>
                        );
                    })}
                </tbody>
            </table>
        )}
    </div>
);

const TokenUsage = ({ onBack }) => {
    const [workflows, setWorkflows] = useState([]);
    const [issues, setIssues] = useState([]);
    const [prs, setPRs] = useState([]);
    const [error, setError] = useState(null);
    const [expandedWorkflow, setExpandedWorkflow] = useState(null);
    const [workflowRecords, setWorkflowRecords] = useState([]);

    const fetchRollups = useCallback(() => {
        const get = (path, setter) =>
            fetch(`/api/usage/${path}`)
                .then(async res => {
                    if (!res.ok) {
                        let errMsg = `HTTP error ${res.status}`;
                        try {
                            const errData = await res.json();
                            if (errData && errData.error) errMsg = errData.error;
                        } catch (e) {}
                        throw new Error(errMsg);
                    }
                    return res.json();
                })
                .then(data => {
                    setError(null);
                    setter(data.rollups || []);
                });

        Promise.all([
            get('v1/usage/rollups/workflows', setWorkflows),
            get('v1/usage/rollups/issues', setIssues),
            get('v1/usage/rollups/prs', setPRs),
        ]).catch(err => {
            console.error('Failed to fetch token usage:', err);
            setError(err.message);
        });
    }, []);

    useEffect(() => {
        fetchRollups();
        const interval = setInterval(fetchRollups, 30000);
        return () => clearInterval(interval);
    }, [fetchRollups]);

    useEffect(() => {
        if (!expandedWorkflow) {
            setWorkflowRecords([]);
            return;
        }
        fetch(`/api/usage/v1/usage/rollups/workflows/${encodeURIComponent(expandedWorkflow)}`)
            .then(res => {
                if (!res.ok) throw new Error(`HTTP error ${res.status}`);
                return res.json();
            })
            .then(data => setWorkflowRecords(data.records || []))
            .catch(err => {
                console.error('Failed to fetch workflow detail:', err);
                setWorkflowRecords([]);
            });
    }, [expandedWorkflow]);

    return (
        <div style={{ padding: '24px', maxWidth: '1400px', margin: '0 auto' }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '20px' }}>
                <h2 style={{ margin: 0, color: 'var(--text-primary)' }}>Gemini Token Usage</h2>
                <button className="btn" onClick={onBack} style={{ padding: '8px 16px', fontWeight: '500' }}>Back to Dashboard</button>
            </div>

            {error && (
                <div className="warning-banner">
                    <strong>⚠️ Failed to load token usage:</strong> {error}
                </div>
            )}

            <RollupTable
                title="Per Workflow"
                rollups={workflows}
                keyLabel="Workflow"
                renderKey={r => `${r.workflowName ? `${r.workflowName} · ` : ''}${r.key}${r.prs?.length ? ` (PRs: ${r.prs.map(n => `#${n}`).join(', ')})` : ''}`}
                expanded={expandedWorkflow}
                onToggle={setExpandedWorkflow}
                records={workflowRecords}
            />
            <RollupTable title="Per Issue" rollups={issues} keyLabel="Issue" renderKey={r => `#${r.key}`} />
            <RollupTable title="Per Pull Request" rollups={prs} keyLabel="Pull Request" renderKey={r => `#${r.key}`} />
        </div>
    );
};

export default TokenUsage;
