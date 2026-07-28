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

// Age of the subject: creation to close (if closed) or to now (if open).
const ageOf = (r) => {
    if (!r.createdAt) return '-';
    const end = r.closedAt ? new Date(r.closedAt) : new Date();
    const diffMs = end - new Date(r.createdAt);
    if (isNaN(diffMs) || diffMs < 0) return '-';
    const hours = Math.floor(diffMs / 3600000);
    const days = Math.floor(hours / 24);
    return days > 0 ? `${days}d ${hours % 24}h` : `${hours}h`;
};

const StatusBadge = ({ state }) => {
    if (!state) return <span style={{ color: 'var(--text-secondary)' }}>-</span>;
    const isOpen = state === 'open';
    return (
        <span style={{
            padding: '2px 10px', borderRadius: '12px', fontSize: '12px', fontWeight: '600',
            backgroundColor: isOpen ? 'rgba(35, 134, 54, 0.15)' : 'rgba(130, 80, 223, 0.15)',
            color: isOpen ? '#2da44e' : '#8250df',
        }}>{state}</span>
    );
};

const RollupTable = ({ title, subtitle, rollups, keyLabel, renderKey, showSubject }) => {
    const [expanded, setExpanded] = useState(null);
    return (
        <div style={{ backgroundColor: 'var(--bg-card)', borderRadius: '8px', boxShadow: 'var(--shadow-card)', padding: '16px', marginBottom: '24px' }}>
            <h3 style={{ margin: '0 0 2px 4px', color: 'var(--text-primary)' }}>{title}</h3>
            {subtitle && <p style={{ margin: '0 0 12px 4px', color: 'var(--text-secondary)', fontSize: '13px' }}>{subtitle}</p>}
            {(!rollups || rollups.length === 0) ? (
                <p style={{ color: 'var(--text-secondary)', margin: '4px' }}>No usage recorded yet.</p>
            ) : (
                <table style={{ width: '100%', borderCollapse: 'collapse', textAlign: 'left' }}>
                    <thead>
                        <tr>
                            <th style={thStyle}>{keyLabel}</th>
                            {showSubject && <th style={thStyle}>Status</th>}
                            {showSubject && <th style={{ ...thStyle, textAlign: 'right' }}>Age</th>}
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
                                    <tr className="table-row-hover" style={{ cursor: 'pointer' }}
                                        onClick={() => setExpanded(isOpen ? null : r.key)}>
                                        <td style={{ ...tdStyle, fontWeight: 'bold' }}>
                                            <span style={{ marginRight: '6px' }}>{isOpen ? '▾' : '▸'}</span>
                                            {renderKey ? renderKey(r) : r.key}
                                        </td>
                                        {showSubject && <td style={tdStyle}><StatusBadge state={r.state} /></td>}
                                        {showSubject && <td style={{ ...numStyle, whiteSpace: 'nowrap' }} title={r.createdAt ? `created ${r.createdAt}${r.closedAt ? `, closed ${r.closedAt}` : ''}` : ''}>{ageOf(r)}</td>}
                                        <td style={numStyle}>{fmt(r.taskCount)}</td>
                                        <TokenCells s={s} />
                                    </tr>
                                    {isOpen && (r.records || []).map(rec => (
                                        <tr key={rec.key} style={{ backgroundColor: 'var(--bg-comment-card)' }}>
                                            <td style={{ ...tdStyle, paddingLeft: '36px', fontSize: '13px' }} colSpan={showSubject ? 3 : 1}>
                                                {rec.taskType || '-'} · {rec.sandbox || '-'}
                                                {rec.pr ? ` · PR #${rec.pr}` : ''}{rec.issue ? ` · issue #${rec.issue}` : ''}
                                                {rec.recordedAt ? ` · ${rec.recordedAt.slice(0, 10)}` : ''}
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
};

const prList = (prs) => (prs || []).map(n => `#${n}`).join(', ');

const ToolTelemetrySection = ({ toolData }) => {
    const [periodTab, setPeriodTab] = useState('day');
    if (!toolData) return null;

    const topCmds = toolData.top_slowest_commands || [];
    const periodCalls = (toolData.slowest_by_period && toolData.slowest_by_period[periodTab]) || [];

    const tabLabels = {
        day: 'Past 24 Hours',
        week: 'Past 7 Days',
        month: 'Past 30 Days'
    };

    const formatTs = (ts) => {
        if (!ts) return '-';
        try {
            const d = new Date(ts);
            return isNaN(d.getTime()) ? ts : d.toLocaleString();
        } catch (e) {
            return ts;
        }
    };

    return (
        <div style={{ backgroundColor: 'var(--bg-card)', borderRadius: '8px', boxShadow: 'var(--shadow-card)', padding: '16px', marginBottom: '24px' }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '12px' }}>
                <div>
                    <h3 style={{ margin: '0 0 2px 4px', color: 'var(--text-primary)' }}>⚡ Slowest Tool Execution Telemetry</h3>
                    <p style={{ margin: '0 0 4px 4px', color: 'var(--text-secondary)', fontSize: '13px' }}>
                        Tracking top shell command bottlenecks, average execution durations, and historical slowest commands over time.
                    </p>
                </div>
            </div>

            {/* Period Slowest Section */}
            <div style={{ marginBottom: '20px', padding: '12px', backgroundColor: 'var(--bg-secondary)', borderRadius: '6px' }}>
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '10px' }}>
                    <h4 style={{ margin: 0, fontSize: '14px', color: '#58a6ff' }}>🏆 Top 5 Slowest Executions ({tabLabels[periodTab]})</h4>
                    <div style={{ display: 'flex', gap: '8px' }}>
                        {['day', 'week', 'month'].map(p => (
                            <button
                                key={p}
                                className="btn"
                                onClick={() => setPeriodTab(p)}
                                style={{
                                    padding: '4px 10px',
                                    fontSize: '12px',
                                    backgroundColor: periodTab === p ? '#1f6feb' : 'transparent',
                                    color: periodTab === p ? '#fff' : 'var(--text-secondary)',
                                    border: '1px solid var(--border-color)'
                                }}
                            >
                                {tabLabels[p]}
                            </button>
                        ))}
                    </div>
                </div>

                {periodCalls.length === 0 ? (
                    <p style={{ color: 'var(--text-secondary)', fontSize: '13px', margin: '4px' }}>No command executions recorded in this time window.</p>
                ) : (
                    <div style={{ overflowX: 'auto' }}>
                        <table style={{ width: '100%', borderCollapse: 'collapse', textAlign: 'left', fontSize: '12px' }}>
                            <thead>
                                <tr style={{ borderBottom: '1px solid var(--border-color)', color: 'var(--text-secondary)' }}>
                                    <th style={{ padding: '6px 10px', width: '40px' }}>Rank</th>
                                    <th style={{ padding: '6px 10px' }}>Command</th>
                                    <th style={{ padding: '6px 10px', textAlign: 'right' }}>Duration</th>
                                    <th style={{ padding: '6px 10px' }}>Date / Time</th>
                                    <th style={{ padding: '6px 10px' }}>Context</th>
                                </tr>
                            </thead>
                            <tbody>
                                {periodCalls.map((c, idx) => (
                                    <tr key={idx} style={{ borderBottom: '1px solid var(--border-color)' }}>
                                        <td style={{ padding: '6px 10px', fontWeight: 'bold', color: '#8b949e' }}>#{idx + 1}</td>
                                        <td style={{ padding: '6px 10px', fontFamily: 'monospace', color: '#7ee787', wordBreak: 'break-all' }}>{c.cmd}</td>
                                        <td style={{ padding: '6px 10px', textAlign: 'right', fontWeight: 'bold', color: c.duration_sec > 60 ? '#ff7b72' : '#e6edf3' }}>
                                            {c.duration_sec}s
                                        </td>
                                        <td style={{ padding: '6px 10px', whiteSpace: 'nowrap', color: 'var(--text-secondary)' }}>{formatTs(c.timestamp)}</td>
                                        <td style={{ padding: '6px 10px', color: 'var(--text-secondary)', fontSize: '11px', whiteSpace: 'nowrap' }}>
                                            {c.sandbox || c.taskType || '-'} {c.repo ? `(${c.repo})` : ''}
                                        </td>
                                    </tr>
                                ))}
                            </tbody>
                        </table>
                    </div>
                )}
            </div>

            {/* Aggregated Commands Section */}
            <div>
                <h4 style={{ margin: '0 0 10px 4px', fontSize: '14px', color: 'var(--text-primary)' }}>📊 Top Slowest Aggregated Commands (Count & Avg Duration)</h4>
                {topCmds.length === 0 ? (
                    <p style={{ color: 'var(--text-secondary)', margin: '4px' }}>No shell commands recorded yet.</p>
                ) : (
                    <div style={{ overflowX: 'auto' }}>
                        <table style={{ width: '100%', borderCollapse: 'collapse', textAlign: 'left' }}>
                            <thead>
                                <tr>
                                    <th style={thStyle}>Command</th>
                                    <th style={{ ...thStyle, textAlign: 'right' }}>Calls</th>
                                    <th style={{ ...thStyle, textAlign: 'right' }}>Total Time</th>
                                    <th style={{ ...thStyle, textAlign: 'right' }}>Avg Time</th>
                                    <th style={{ ...thStyle, textAlign: 'right' }}>Max Time</th>
                                    <th style={{ ...thStyle, textAlign: 'right' }}>Last Executed</th>
                                </tr>
                            </thead>
                            <tbody>
                                {topCmds.map((ac, idx) => (
                                    <tr key={idx} className="table-row-hover">
                                        <td style={{ ...tdStyle, fontFamily: 'monospace', color: '#7ee787', fontSize: '12px', wordBreak: 'break-all' }}>{ac.cmd}</td>
                                        <td style={numStyle}>{fmt(ac.count)}</td>
                                        <td style={numStyle}>{ac.total_sec}s</td>
                                        <td style={{ ...numStyle, fontWeight: 'bold' }}>{ac.avg_sec}s</td>
                                        <td style={{ ...numStyle, color: ac.max_sec > 60 ? '#ff7b72' : 'inherit', fontWeight: ac.max_sec > 60 ? 'bold' : 'normal' }}>{ac.max_sec}s</td>
                                        <td style={{ ...numStyle, fontSize: '12px', color: 'var(--text-secondary)' }}>{formatTs(ac.last_executed_at)}</td>
                                    </tr>
                                ))}
                            </tbody>
                        </table>
                    </div>
                )}
            </div>
        </div>
    );
};

const TokenUsage = ({ onBack }) => {
    const [daily, setDaily] = useState([]);
    const [workflows, setWorkflows] = useState([]);
    const [issues, setIssues] = useState([]);
    const [prs, setPRs] = useState([]);
    const [toolData, setToolData] = useState(null);
    const [error, setError] = useState(null);

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
                    if (path.includes('v1/usage/tools')) {
                        setter(data);
                    } else {
                        setter(data.rollups || []);
                    }
                });

        Promise.all([
            get('v1/usage/rollups/daily', setDaily),
            get('v1/usage/rollups/workflows', setWorkflows),
            get('v1/usage/rollups/issues', setIssues),
            get('v1/usage/rollups/prs', setPRs),
            get('v1/usage/tools', setToolData),
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

    return (
        <div style={{ padding: '24px', maxWidth: '1400px', margin: '0 auto' }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '20px' }}>
                <h2 style={{ margin: 0, color: 'var(--text-primary)' }}>Gemini Token & Tool Telemetry Usage</h2>
                <button className="btn" onClick={onBack} style={{ padding: '8px 16px', fontWeight: '500' }}>Back to Dashboard</button>
            </div>

            {error && (
                <div className="warning-banner">
                    <strong>⚠️ Failed to load token usage:</strong> {error}
                </div>
            )}

            <ToolTelemetrySection toolData={toolData} />

            <RollupTable
                title="Daily Usage"
                subtitle="All usage grouped by the day it was first recorded (UTC)."
                rollups={daily}
                keyLabel="Date"
            />
            <RollupTable
                title="Workflow Issues"
                subtitle="Workflow sandboxes (wf-issue-*), including all tasks and PRs spawned by the workflow."
                rollups={workflows}
                keyLabel="Workflow"
                renderKey={r => `${r.workflowName ? `${r.workflowName} · ` : ''}${r.key}${r.prs?.length ? ` (PRs: ${prList(r.prs)})` : ''}`}
                showSubject
            />
            <RollupTable
                title="Issue / PR Sandboxes"
                subtitle="Issue sandboxes and the PR work they led to; issues owned by a workflow are counted above instead."
                rollups={issues}
                keyLabel="Issue"
                renderKey={r => `#${r.key}${r.prs?.length ? ` / PR ${prList(r.prs)}` : ''}`}
                showSubject
            />
            <RollupTable
                title="PR Sandboxes"
                subtitle="Standalone PR work (reviews, investigations, adoptions) not linked to any issue or workflow."
                rollups={prs}
                keyLabel="Pull Request"
                renderKey={r => `#${r.key}`}
                showSubject
            />
        </div>
    );
};

export default TokenUsage;
