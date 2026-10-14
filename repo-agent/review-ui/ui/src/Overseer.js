import React, { useState, useEffect, useCallback, useRef } from 'react';
import './App.css';
import OverseerWork from './OverseerWork';
import BoardPicker from './BoardPicker';

export const getTimestampDetails = (ts) => {
    if (!ts) return null;
    try {
        const d = new Date(ts);
        if (isNaN(d.getTime())) return null;
        const exact = d.toLocaleString();
        const diffMs = Date.now() - d.getTime();
        let rel = '';
        if (diffMs >= 0) {
            const seconds = Math.floor(diffMs / 1000);
            if (seconds < 60) {
                rel = `${seconds}s ago`;
            } else {
                const minutes = Math.floor(seconds / 60);
                if (minutes < 60) {
                    rel = `${minutes}m ago`;
                } else {
                    const hours = Math.floor(minutes / 60);
                    if (hours < 24) {
                        rel = `${hours}h ${minutes % 60}m ago`;
                    } else {
                        const days = Math.floor(hours / 24);
                        rel = `${days}d ${hours % 24}h ago`;
                    }
                }
            }
        } else {
            rel = 'just now';
        }
        return { exact, rel };
    } catch (e) {
        return null;
    }
};

export const formatQueueTimestamp = (ts) => {
    if (!ts) return '-';
    const details = getTimestampDetails(ts);
    if (!details) return ts;
    const { exact, rel } = details;
    return (
        <div>
            <div>{exact}</div>
            {rel && <div style={{ fontSize: '0.75rem', color: 'var(--text-muted)', marginTop: '2px' }}>{`(${rel})`}</div>}
        </div>
    );
};

export const formatTriggerReason = (reason) => {
    switch (reason) {
        case 'IssueCreated': return 'Issue Created';
        case 'IssueLabeled': return 'Issue Labeled';
        case 'PRCommentsAdded': return 'PR Comments';
        case 'PRCheckFailed': return 'CI Failure';
        case 'PRMergeConflict': return 'Merge Conflict';
        case 'PRReadyForReview': return 'Ready for Review';
        case 'ChoreScheduled': return 'Scheduled Chore';
        default: return reason || '';
    }
};

// overseerRoute reads #/overseers/<name>: the overseer the page is on.
// (#/overseers/<name>/task-session/… is a popped-out session, App's.)
export function overseerRoute(hash = window.location.hash) {
    const m = hash.match(/^#\/overseers\/([A-Za-z0-9._-]+)$/);
    return m ? { name: m[1] } : null;
}

// OVERSEER_KEY remembers the overseer last shown: the page unmounts when
// another view is toggled to, and comes back to it.
const OVERSEER_KEY = 'overseer.active';

const repoPath = (url) => (url || '').replace(/\.git$/, '').replace(/^https?:\/\/github\.com\//, '');

const Overseer = ({ namespace: userNamespace }) => {
    const [overseers, setOverseers] = useState([]);
    const [error, setError] = useState(null);
    const [activeOverseer, setActiveOverseer] = useState(null);
    // The overseer to open on: the URL's, else the one last shown.
    const wantedRef = useRef((() => {
        const route = overseerRoute();
        if (route) return route.name;
        try { return localStorage.getItem(OVERSEER_KEY) || ''; } catch (e) { return ''; }
    })());

    const [logs, setLogs] = useState('');
    const [showOverseerLogs, setShowOverseerLogs] = useState(false);

    const [showTaskQueue, setShowTaskQueue] = useState(false);
    const [queueData, setQueueData] = useState(null);
    const [queueFilter, setQueueFilter] = useState('');

    const [showStatus, setShowStatus] = useState(false);
    const [statusData, setStatusData] = useState(null);
    const [statusLoading, setStatusLoading] = useState(false);

    const logIntervalRef = useRef(null);
    const activeOverseerName = activeOverseer?.metadata?.name;
    const statusDataRef = useRef(statusData);
    const prevOverseerNameRef = useRef(activeOverseerName);

    useEffect(() => {
        statusDataRef.current = statusData;
    }, [statusData]);

    const fetchQueue = useCallback(() => {
        if (!activeOverseerName) return;
        fetch(`/api/overseers/${activeOverseerName}/queue`)
            .then(res => {
                if (!res.ok) throw new Error("Failed to fetch task queue");
                return res.json();
            })
            .then(data => {
                if (data && data.isSyncing) {
                    setQueueData(prev => prev ? { ...prev, isSyncing: true } : data);
                } else {
                    setQueueData(prev => {
                        if (prev && JSON.stringify(prev) === JSON.stringify(data)) return prev;
                        return data;
                    });
                }
            })
            .catch(err => {
                console.error("Failed to fetch task queue:", err);
                setQueueData(prev => prev ? { ...prev, isSyncing: true } : null);
            });
    }, [activeOverseerName]);

    useEffect(() => {
        fetchQueue();
        const interval = setInterval(fetchQueue, 5000);
        return () => clearInterval(interval);
    }, [fetchQueue]);

    const fetchStatus = useCallback((isManual = false) => {
        if (!activeOverseerName) return;
        if (isManual || !statusDataRef.current) {
            setStatusLoading(true);
        }
        fetch(`/api/overseers/${activeOverseerName}/status`)
            .then(res => {
                if (!res.ok) throw new Error("Failed to fetch status");
                return res.json();
            })
            .then(data => {
                setStatusData(prev => {
                    if (prev && JSON.stringify(prev) === JSON.stringify(data)) return prev;
                    return data;
                });
                setStatusLoading(false);
            })
            .catch(err => {
                console.error("Failed to fetch status:", err);
                setStatusLoading(false);
            });
    }, [activeOverseerName]);

    useEffect(() => {
        if (showStatus) {
            fetchStatus();
            const interval = setInterval(() => fetchStatus(false), 15000);
            return () => clearInterval(interval);
        }
    }, [showStatus, fetchStatus]);

    useEffect(() => {
        if (activeOverseerName && prevOverseerNameRef.current && prevOverseerNameRef.current !== activeOverseerName) {
            setStatusData(null);
            setQueueData(null);
        }
        prevOverseerNameRef.current = activeOverseerName;
    }, [activeOverseerName]);

    const handleMakeCritical = (fileName, currentPriority) => {
        if (!activeOverseerName) return;
        const newPriority = currentPriority === 'critical' ? 'medium' : 'critical';
        fetch(`/api/overseers/${activeOverseerName}/queue/${encodeURIComponent(fileName)}/priority`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ priority: newPriority })
        })
        .then(res => {
            if (!res.ok) throw new Error("Failed to update priority");
            return res.json();
        })
        .then(() => fetchQueue())
        .catch(err => alert("Failed to update task priority: " + err.message));
    };

    const fetchOverseers = useCallback(() => {
        fetch('/api/overseers')
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
                setOverseers(prev => {
                    if (prev && JSON.stringify(prev) === JSON.stringify(data)) return prev;
                    return data || [];
                });
                setActiveOverseer(prev => {
                    if (data && data.length > 0 && !prev) {
                        return data.find(o => o.metadata?.name === wantedRef.current) || data[0];
                    } else if (prev) {
                        const updated = (data || []).find(o => o.metadata?.name === prev.metadata?.name);
                        if (!updated) return prev;
                        if (JSON.stringify(prev) === JSON.stringify(updated)) return prev;
                        return updated;
                    }
                    return prev;
                });
            })
            .catch(err => {
                console.error("Failed to fetch overseers:", err);
                setError(err.message);
                setOverseers([]);
            });
    }, []);

    useEffect(() => {
        fetchOverseers();
        const interval = setInterval(fetchOverseers, 15000);
        return () => clearInterval(interval);
    }, [fetchOverseers]);

    const fetchLogs = useCallback(() => {
        if (!activeOverseerName) return;
        if (!showOverseerLogs) return;
        fetch(`/api/overseers/${activeOverseerName}/logs`)
            .then(res => res.text())
            .then(data => setLogs(data))
            .catch(err => console.error("Failed to fetch logs:", err));
    }, [activeOverseerName, showOverseerLogs]);

    useEffect(() => {
        if (logIntervalRef.current) clearInterval(logIntervalRef.current);
        
        if (showOverseerLogs) {
            fetchLogs();
            logIntervalRef.current = setInterval(fetchLogs, 5000);
        } else {
            setLogs('');
        }

        return () => {
            if (logIntervalRef.current) clearInterval(logIntervalRef.current);
        };
    }, [showOverseerLogs, fetchLogs]);

    const handleOverseerClick = (ov) => {
        setActiveOverseer(ov);
        setShowOverseerLogs(false);
        setShowStatus(false);
        setShowTaskQueue(false);
    };

    // The URL and the browser say which overseer is shown: a reload, a
    // link, or a toggle back from another view opens on it.
    useEffect(() => {
        if (!activeOverseerName) return;
        window.history.replaceState(null, '', `#/overseers/${activeOverseerName}`);
        try { localStorage.setItem(OVERSEER_KEY, activeOverseerName); } catch (e) { /* private mode */ }
    }, [activeOverseerName]);

    const isOverseerInUpgradeMode = (overseer) => {
        if (!overseer) return false;
        const annotations = overseer.metadata?.annotations || {};
        return annotations['overseer.gemini.google.com/upgrade-mode'] === 'true' ||
               annotations['overseer.gemini.google.com/upgrade-mode'] === true ||
               overseer.upgradeMode === true;
    };

    const getOverseerUpgradeTimestamp = (overseer) => {
        if (!overseer) return null;
        const annotations = overseer.metadata?.annotations || {};
        return annotations['overseer.gemini.google.com/upgrade-timestamp'] || overseer.upgradeTimestamp || null;
    };

    return (
        <div className="dev-layout" style={{ height: 'calc(100vh - 80px)' }}>
            {/* Main Panel - Full Width */}
            <div className="dev-main" style={{ width: '100%', flex: 1, padding: '25px', overflowY: 'auto', backgroundColor: 'var(--bg-color)' }}>
                {error && (
                    <div className="warning-banner" style={{ backgroundColor: '#fdecea', color: '#721c24', borderColor: '#f5c6cb', marginBottom: '20px', padding: '12px', borderRadius: '4px', border: '1px solid #f5c6cb' }}>
                        <strong>Error fetching overseers:</strong> {error}
                    </div>
                )}

                {isOverseerInUpgradeMode(activeOverseer) && (
                    <div style={{
                        background: 'linear-gradient(135deg, #fff3cd 0%, #ffecb5 100%)',
                        color: '#664d03',
                        border: '1px solid #ffecb5',
                        borderLeft: '6px solid #ffc107',
                        padding: '16px 20px',
                        borderRadius: '8px',
                        marginBottom: '22px',
                        display: 'flex',
                        alignItems: 'center',
                        justifyContent: 'space-between',
                        boxShadow: '0 4px 12px rgba(255, 193, 7, 0.18)',
                        flexWrap: 'wrap',
                        gap: '15px'
                    }}>
                        <div style={{ display: 'flex', alignItems: 'center', gap: '15px' }}>
                            <span style={{ fontSize: '1.8rem' }}>🚧</span>
                            <div>
                                <div style={{ fontWeight: '700', fontSize: '1.1rem', marginBottom: '4px', display: 'flex', alignItems: 'center', gap: '10px' }}>
                                    <span>Overseer Upgrade Mode Active</span>
                                    <span style={{
                                        backgroundColor: '#ffc107',
                                        color: '#212529',
                                        padding: '2px 10px',
                                        borderRadius: '12px',
                                        fontSize: '0.72rem',
                                        fontWeight: '800',
                                        textTransform: 'uppercase',
                                        letterSpacing: '0.5px'
                                    }}>
                                        DO NOT PROCESS
                                    </span>
                                </div>
                                <div style={{ fontSize: '0.94rem', lineHeight: '1.4' }}>
                                    <strong>{activeOverseer?.metadata?.name}</strong> is currently in upgrade mode and <strong>will not accept new tasks</strong>. Existing tasks will finish draining before the Overseer restarts.
                                    {getOverseerUpgradeTimestamp(activeOverseer) && (
                                        <span style={{ marginLeft: '8px', opacity: 0.85, fontWeight: '500' }}>
                                            • Started at {new Date(getOverseerUpgradeTimestamp(activeOverseer)).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })}
                                        </span>
                                    )}
                                </div>
                            </div>
                        </div>
                    </div>
                )}

                {/* One line, as Work's: the overseer picker, its repository's
                    links, its bot and poll, and whether it is upgrading. */}
                <div style={{ display: 'flex', alignItems: 'center', gap: '10px', flexWrap: 'wrap', marginBottom: '16px', fontSize: 'small', color: 'var(--text-secondary)' }}>
                    {overseers.length > 0 ? (
                        <BoardPicker label="Overseer" showRepo active={activeOverseerName}
                            boards={overseers.map(o => ({ name: o.metadata.name, repoURL: o.spec?.repoURL, tag: isOverseerInUpgradeMode(o) ? 'UPGRADING' : '' }))}
                            onPick={(name) => { const ov = overseers.find(o => o.metadata.name === name); if (ov) handleOverseerClick(ov); }} />
                    ) : (
                        <span>{error ? 'No overseers.' : 'Loading…'}</span>
                    )}
                    {activeOverseer && activeOverseer.spec?.repoURL && (
                        <span>
                            <a href={activeOverseer.spec.repoURL} target="_blank" rel="noopener noreferrer" title={activeOverseer.spec.repoURL}>GitHub</a>
                            <span style={{ margin: '0 6px' }}>·</span>
                            <a href={`https://gitdiagram.com/${repoPath(activeOverseer.spec.repoURL)}`} target="_blank" rel="noopener noreferrer" title="Architecture diagram (gitdiagram.com)">GitDiagram</a>
                            <span style={{ margin: '0 6px' }}>·</span>
                            <a href={`https://codewiki.google/github.com/${repoPath(activeOverseer.spec.repoURL)}`} target="_blank" rel="noopener noreferrer" title="Code wiki (codewiki.google)">CodeWiki</a>
                        </span>
                    )}
                    {activeOverseer?.spec?.robotAccount && <span title="The bot account it works as">🤖 {activeOverseer.spec.robotAccount}</span>}
                    {activeOverseer?.spec?.pollInterval && <span title="How often it polls the repository">⏱ {activeOverseer.spec.pollInterval}</span>}
                    {isOverseerInUpgradeMode(activeOverseer) && (
                        <span style={{ padding: '2px 10px', borderRadius: '12px', fontSize: 'x-small', fontWeight: 'bold', backgroundColor: '#fff3cd', color: '#856404', border: '1px solid #ffeeba' }}
                            title="Overseer is in upgrade mode and not accepting new tasks">🚧 UPGRADING</span>
                    )}
                </div>

                {/* Sub-Tab Navigation Bar */}
                <div style={{ display: 'flex', gap: '10px', marginBottom: '20px', borderBottom: '1px solid var(--border-color)', paddingBottom: '12px' }}>
                    <button 
                        className={`btn ${!showTaskQueue && !showOverseerLogs && !showStatus ? 'btn-primary' : 'btn-secondary'}`}
                        onClick={() => { setShowTaskQueue(false); setShowOverseerLogs(false); setShowStatus(false); }}
                        style={{ fontWeight: '600', padding: '8px 16px', display: 'inline-flex', alignItems: 'center', gap: '6px' }}
                    >
                        🧭 Work
                    </button>
                    <button 
                        className={`btn ${showTaskQueue ? 'btn-primary' : 'btn-secondary'}`}
                        onClick={() => { setShowTaskQueue(true); setShowOverseerLogs(false); setShowStatus(false); }}
                        style={{ fontWeight: '600', padding: '8px 16px', display: 'inline-flex', alignItems: 'center', gap: '6px' }}
                    >
                        📥 Queue ({queueData?.summary?.totalPending || 0})
                        {queueData?.summary?.byPriority?.critical > 0 && (
                            <span style={{ backgroundColor: '#d93025', color: '#fff', padding: '1px 7px', borderRadius: '10px', fontSize: '0.75rem', fontWeight: 'bold', marginLeft: '4px' }}>
                                {queueData.summary.byPriority.critical} Critical
                            </span>
                        )}
                    </button>
                    <button 
                        className={`btn ${showOverseerLogs ? 'btn-primary' : 'btn-secondary'}`}
                        onClick={() => { setShowOverseerLogs(true); setShowTaskQueue(false); setShowStatus(false); }}
                        style={{ fontWeight: '600', padding: '8px 16px', display: 'inline-flex', alignItems: 'center', gap: '6px' }}
                    >
                        📜 Logs
                    </button>
                    <button 
                        className={`btn ${showStatus ? 'btn-primary' : 'btn-secondary'}`}
                        onClick={() => { setShowStatus(true); setShowTaskQueue(false); setShowOverseerLogs(false); }}
                        style={{ fontWeight: '600', padding: '8px 16px', display: 'inline-flex', alignItems: 'center', gap: '6px' }}
                    >
                        🔑 Keys
                    </button>
                </div>

                {showStatus ? (
                    <div style={{ textAlign: 'left', backgroundColor: 'var(--bg-card)', padding: '24px', borderRadius: '8px', border: '1px solid var(--border-color)', boxShadow: 'var(--shadow-card)' }}>
                        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '20px' }}>
                            <h3 style={{ margin: 0, color: 'var(--text-primary)' }}>📊 Factory / API Token Status</h3>
                            <button 
                                className="btn btn-sm btn-secondary" 
                                onClick={() => fetchStatus(true)}
                                disabled={statusLoading}
                                style={{ display: 'flex', alignItems: 'center', gap: '6px' }}
                            >
                                {statusLoading ? '⏳ Refreshing...' : '🔄 Refresh Status'}
                            </button>
                        </div>

                        {!statusData && statusLoading ? (
                            <div style={{ padding: '20px', textAlign: 'center', color: 'var(--text-secondary)' }}>
                                🔄 Loading status data, please wait...
                            </div>
                        ) : !statusData ? (
                            <div style={{ padding: '20px', textAlign: 'center', color: 'var(--text-muted)' }}>
                                No status data available. Click Refresh Status to load.
                            </div>
                        ) : (
                            <div>
                                {statusData?.isSyncing && (
                                    <div style={{ backgroundColor: 'rgba(240, 173, 78, 0.1)', border: '1px solid #f0ad4e', padding: '12px 16px', borderRadius: '6px', color: '#f0ad4e', fontWeight: 'bold', display: 'flex', alignItems: 'center', gap: '8px', marginBottom: '20px' }}>
                                        ⚠️ Overseer daemon is currently initializing / in cycle sync phase. Displaying cached view...
                                    </div>
                                )}

                                {/* Status summary cards */}
                                <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))', gap: '15px', marginBottom: '25px' }}>
                                    <div style={{ backgroundColor: 'rgba(92, 184, 92, 0.1)', border: '1px solid #5cb85c', padding: '16px', borderRadius: '8px' }}>
                                        <div style={{ fontSize: '0.85rem', color: '#5cb85c', fontWeight: 'bold', marginBottom: '4px' }}>🟢 Active Keys</div>
                                        <div style={{ fontSize: '1.8rem', fontWeight: 'bold', color: '#5cb85c' }}>{statusData?.active || 0}</div>
                                    </div>
                                    <div style={{ backgroundColor: 'rgba(240, 173, 78, 0.1)', border: '1px solid #f0ad4e', padding: '16px', borderRadius: '8px' }}>
                                        <div style={{ fontSize: '0.85rem', color: '#f0ad4e', fontWeight: 'bold', marginBottom: '4px' }}>🟡 Quota Exceeded Keys</div>
                                        <div style={{ fontSize: '1.8rem', fontWeight: 'bold', color: '#f0ad4e' }}>{statusData?.quotaExceeded || 0}</div>
                                    </div>
                                    <div style={{ backgroundColor: 'rgba(217, 83, 79, 0.1)', border: '1px solid #d9534f', padding: '16px', borderRadius: '8px' }}>
                                        <div style={{ fontSize: '0.85rem', color: '#d9534f', fontWeight: 'bold', marginBottom: '4px' }}>🔴 Suspended Keys</div>
                                        <div style={{ fontSize: '1.8rem', fontWeight: 'bold', color: '#d9534f' }}>{statusData?.suspended || 0}</div>
                                    </div>
                                    <div style={{ backgroundColor: 'var(--bg-body)', border: '1px solid var(--border-color)', padding: '16px', borderRadius: '8px' }}>
                                        <div style={{ fontSize: '0.85rem', color: 'var(--text-secondary)', marginBottom: '4px' }}>🔑 Total Tracked Keys</div>
                                        <div style={{ fontSize: '1.8rem', fontWeight: 'bold', color: 'var(--text-primary)' }}>{statusData?.total || 0}</div>
                                    </div>
                                </div>

                                {/* Lists of keys */}
                                <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(300px, 1fr))', gap: '20px' }}>
                                    {/* Active keys column */}
                                    <div style={{ border: '1px solid var(--border-color)', borderRadius: '6px', padding: '15px', backgroundColor: 'var(--bg-body)' }}>
                                        <h4 style={{ marginTop: 0, marginBottom: '12px', color: '#5cb85c', borderBottom: '1px solid var(--border-color)', paddingBottom: '8px' }}>🟢 Active Keys</h4>
                                        {statusData?.activeList && statusData.activeList.length > 0 ? (
                                            <ul style={{ listStyleType: 'none', paddingLeft: 0, margin: 0 }}>
                                                {statusData.activeList.map((key, idx) => (
                                                    <li key={idx} style={{ padding: '8px 12px', marginBottom: '6px', backgroundColor: 'var(--bg-card)', border: '1px solid var(--border-color)', borderRadius: '4px', fontFamily: 'monospace', fontSize: '0.9rem' }}>
                                                        {key}
                                                    </li>
                                                ))}
                                            </ul>
                                        ) : (
                                            <div style={{ color: 'var(--text-muted)', fontSize: '0.9rem' }}>No active keys currently found.</div>
                                        )}
                                    </div>

                                    {/* Quota Exceeded keys column */}
                                    <div style={{ border: '1px solid var(--border-color)', borderRadius: '6px', padding: '15px', backgroundColor: 'var(--bg-body)' }}>
                                        <h4 style={{ marginTop: 0, marginBottom: '12px', color: '#f0ad4e', borderBottom: '1px solid var(--border-color)', paddingBottom: '8px' }}>🟡 Quota Exceeded Keys</h4>
                                        {statusData?.quotaExceededList && statusData.quotaExceededList.length > 0 ? (
                                            <ul style={{ listStyleType: 'none', paddingLeft: 0, margin: 0 }}>
                                                {statusData.quotaExceededList.map((key, idx) => (
                                                    <li key={idx} style={{ padding: '8px 12px', marginBottom: '6px', backgroundColor: 'var(--bg-card)', border: '1px solid var(--border-color)', borderRadius: '4px', fontFamily: 'monospace', fontSize: '0.9rem' }}>
                                                        {key}
                                                    </li>
                                                ))}
                                            </ul>
                                        ) : (
                                            <div style={{ color: 'var(--text-muted)', fontSize: '0.9rem' }}>No quota-exceeded keys currently found.</div>
                                        )}
                                    </div>

                                    {/* Suspended keys column */}
                                    <div style={{ border: '1px solid var(--border-color)', borderRadius: '6px', padding: '15px', backgroundColor: 'var(--bg-body)' }}>
                                        <h4 style={{ marginTop: 0, marginBottom: '12px', color: '#d9534f', borderBottom: '1px solid var(--border-color)', paddingBottom: '8px' }}>🔴 Suspended Keys</h4>
                                        {statusData?.suspendedList && statusData.suspendedList.length > 0 ? (
                                            <ul style={{ listStyleType: 'none', paddingLeft: 0, margin: 0 }}>
                                                {statusData.suspendedList.map((key, idx) => (
                                                    <li key={idx} style={{ padding: '8px 12px', marginBottom: '6px', backgroundColor: 'var(--bg-card)', border: '1px solid var(--border-color)', borderRadius: '4px', fontFamily: 'monospace', fontSize: '0.9rem', overflowX: 'auto' }}>
                                                        {key}
                                                    </li>
                                                ))}
                                            </ul>
                                        ) : (
                                            <div style={{ color: 'var(--text-muted)', fontSize: '0.9rem' }}>No suspended keys currently found.</div>
                                        )}
                                    </div>
                                </div>
                            </div>
                        )}
                    </div>
                ) : showTaskQueue ? (
                    <div>
                        {queueData?.isSyncing && (
                            <div style={{
                                backgroundColor: '#fff3cd',
                                color: '#856404',
                                border: '1px solid #ffeeba',
                                borderLeft: '6px solid #ffc107',
                                padding: '12px 18px',
                                borderRadius: '8px',
                                marginBottom: '20px',
                                fontSize: '0.9rem',
                                display: 'flex',
                                alignItems: 'center',
                                gap: '12px',
                                boxShadow: '0 2px 8px rgba(0,0,0,0.05)'
                            }}>
                                <span style={{ fontSize: '1.4rem' }}>🔄</span>
                                <div>
                                    <strong style={{ fontWeight: '700' }}>Overseer Cycle Sync Active:</strong> The Overseer daemon is currently running LLM scan / pushing state to GitHub. Displaying cached task queue view while HTTP service synchronizes...
                                </div>
                            </div>
                        )}

                        {/* Summary Ribbon */}
                        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))', gap: '15px', marginBottom: '22px' }}>
                            <div style={{ backgroundColor: 'var(--bg-card)', padding: '16px', borderRadius: '8px', border: '1px solid var(--border-color)', boxShadow: 'var(--shadow-card)' }}>
                                <div style={{ fontSize: '0.85rem', color: 'var(--text-secondary)', marginBottom: '4px' }}>📥 Total Pending Tasks</div>
                                <div style={{ fontSize: '1.8rem', fontWeight: 'bold', color: 'var(--text-primary)' }}>{queueData?.summary?.totalPending || 0}</div>
                            </div>
                            <div style={{ backgroundColor: 'var(--bg-card)', padding: '16px', borderRadius: '8px', border: '1px solid var(--border-color)', boxShadow: 'var(--shadow-card)' }}>
                                <div style={{ fontSize: '0.85rem', color: 'var(--text-secondary)', marginBottom: '4px' }}>⚙️ In-Flight / Processing</div>
                                <div style={{ fontSize: '1.8rem', fontWeight: 'bold', color: '#0275d8' }}>{queueData?.summary?.totalProcessing || 0}</div>
                            </div>
                            <div style={{ backgroundColor: 'var(--bg-card)', padding: '16px', borderRadius: '8px', border: '1px solid var(--border-color)', boxShadow: 'var(--shadow-card)' }}>
                                <div style={{ fontSize: '0.85rem', color: 'var(--text-secondary)', marginBottom: '4px' }}>⚡ Critical Priority Tasks</div>
                                <div style={{ fontSize: '1.8rem', fontWeight: 'bold', color: queueData?.summary?.byPriority?.critical > 0 ? '#d93025' : 'var(--text-primary)' }}>
                                    {queueData?.summary?.byPriority?.critical || 0}
                                </div>
                            </div>
                            <div style={{ backgroundColor: 'var(--bg-card)', padding: '16px', borderRadius: '8px', border: '1px solid var(--border-color)', boxShadow: 'var(--shadow-card)' }}>
                                <div style={{ fontSize: '0.85rem', color: 'var(--text-secondary)', marginBottom: '4px' }}>✅ Recently Completed</div>
                                <div style={{ fontSize: '1.8rem', fontWeight: 'bold', color: '#5cb85c' }}>{queueData?.summary?.totalCompleted || 0}</div>
                            </div>
                        </div>

                        {/* Search and Controls */}
                        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '15px', flexWrap: 'wrap', gap: '15px' }}>
                            <input 
                                type="text"
                                placeholder="🔍 Search tasks by issue/PR #, type, assignee, or priority..."
                                value={queueFilter}
                                onChange={(e) => setQueueFilter(e.target.value)}
                                style={{
                                    padding: '8px 14px',
                                    borderRadius: '6px',
                                    border: '1px solid var(--border-color)',
                                    backgroundColor: 'var(--bg-card)',
                                    color: 'var(--text-primary)',
                                    width: '380px',
                                    fontSize: '0.9rem'
                                }}
                            />
                            <button 
                                className="btn btn-sm btn-secondary" 
                                onClick={fetchQueue}
                                style={{ display: 'flex', alignItems: 'center', gap: '6px' }}
                            >
                                🔄 Refresh Queue
                            </button>
                        </div>

                        {/* Queue Table */}
                        <div className="table-responsive" style={{ backgroundColor: 'var(--bg-card)', borderRadius: '8px', border: '1px solid var(--border-color)', overflow: 'hidden' }}>
                            <table className="table" style={{ margin: 0, width: '100%', borderCollapse: 'collapse' }}>
                                <thead>
                                    <tr style={{ backgroundColor: 'var(--bg-secondary)', borderBottom: '1px solid var(--border-color)', fontSize: '0.85rem' }}>
                                        <th style={{ padding: '12px 16px', textAlign: 'left', width: '75px' }}>Rank</th>
                                        <th style={{ padding: '12px 16px', textAlign: 'left' }}>Type</th>
                                        <th style={{ padding: '12px 16px', textAlign: 'left' }}>Target Issue / PR</th>
                                        <th style={{ padding: '12px 16px', textAlign: 'left' }}>Priority</th>
                                        <th style={{ padding: '12px 16px', textAlign: 'left' }}>Assignee</th>
                                        <th style={{ padding: '12px 16px', textAlign: 'left' }}>Trigger Event</th>
                                        <th style={{ padding: '12px 16px', textAlign: 'left' }}>Enqueued At</th>
                                        <th style={{ padding: '12px 16px', textAlign: 'right' }}>Actions</th>
                                    </tr>
                                </thead>
                                <tbody>
                                    {/* Processing tasks */}
                                    {queueData?.processing?.map(t => (
                                        <tr key={`processing-${t.fileName}`} style={{ backgroundColor: 'rgba(2, 117, 216, 0.08)', borderBottom: '1px solid var(--border-color)' }}>
                                            <td style={{ padding: '12px 16px', fontWeight: 'bold' }}>
                                                <span style={{ backgroundColor: '#0275d8', color: '#fff', padding: '2px 8px', borderRadius: '10px', fontSize: '0.75rem' }}>RUNNING</span>
                                                {t.startedAt && (
                                                    <div style={{ fontSize: '0.72rem', color: 'var(--text-muted)', marginTop: '3px', fontWeight: 'normal' }} title={`Started: ${t.startedAt}`}>
                                                        {formatQueueTimestamp(t.startedAt)}
                                                    </div>
                                                )}
                                            </td>
                                            <td style={{ padding: '12px 16px' }}>
                                                <span style={{ backgroundColor: 'var(--bg-secondary)', padding: '3px 8px', borderRadius: '4px', fontSize: '0.8rem', fontWeight: '600' }}>{t.type}</span>
                                            </td>
                                            <td style={{ padding: '12px 16px' }}>
                                                {t.url ? (
                                                    <a href={t.url} target="_blank" rel="noopener noreferrer" style={{ color: 'var(--text-link)', fontWeight: '600' }}>
                                                        #{t.number > 0 ? t.number : t.fileName}
                                                    </a>
                                                ) : t.fileName}
                                            </td>
                                            <td style={{ padding: '12px 16px' }}>
                                                {t.priority === 'critical' ? (
                                                    <span style={{ backgroundColor: '#d93025', color: '#fff', padding: '2px 8px', borderRadius: '10px', fontSize: '0.75rem', fontWeight: 'bold' }}>⚡ CRITICAL</span>
                                                ) : (
                                                    <span style={{ backgroundColor: '#0275d8', color: '#fff', padding: '2px 8px', borderRadius: '10px', fontSize: '0.75rem', fontWeight: 'bold' }}>{(t.priority || 'medium').toUpperCase()}</span>
                                                )}
                                            </td>
                                            <td style={{ padding: '12px 16px', fontSize: '0.85rem' }}>{t.assignee || '-'}</td>
                                            <td style={{ padding: '12px 16px', fontSize: '0.85rem', color: 'var(--text-secondary)' }} title={t.triggerNotes || t.triggerReason || t.triggerEventTime || t.createdAt || ''}>
                                                {t.triggerEventTime ? (
                                                    <div>
                                                        {formatQueueTimestamp(t.triggerEventTime)}
                                                        {t.triggerReason && (
                                                            <div style={{ fontSize: '0.75rem', marginTop: '2px', maxWidth: '220px', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }} title={t.triggerNotes || t.triggerReason}>
                                                                <span style={{ backgroundColor: 'var(--bg-secondary)', color: 'var(--text-secondary)', padding: '1px 6px', borderRadius: '3px', fontWeight: '600', fontSize: '0.73rem' }}>
                                                                    {formatTriggerReason(t.triggerReason)}
                                                                </span>
                                                            </div>
                                                        )}
                                                    </div>
                                                ) : (
                                                    <div>{t.createdAt ? t.createdAt.split('T')[0] : '-'}</div>
                                                )}
                                            </td>
                                            <td style={{ padding: '12px 16px', fontSize: '0.85rem', color: 'var(--text-secondary)' }} title={t.enqueuedAt || ''}>
                                                {formatQueueTimestamp(t.enqueuedAt)}
                                                {(() => {
                                                    if (t.triggerEventTime && t.enqueuedAt) {
                                                        const ev = new Date(t.triggerEventTime).getTime();
                                                        const enq = new Date(t.enqueuedAt).getTime();
                                                        if (!isNaN(ev) && !isNaN(enq) && enq >= ev) {
                                                            const diffSec = Math.floor((enq - ev) / 1000);
                                                            let lagStr = '';
                                                            if (diffSec < 60) lagStr = `${diffSec}s lag`;
                                                            else if (diffSec < 3600) lagStr = `${Math.floor(diffSec / 60)}m ${diffSec % 60}s lag`;
                                                            else lagStr = `${Math.floor(diffSec / 3600)}h ${Math.floor((diffSec % 3600) / 60)}m lag`;
                                                            return (
                                                                <div style={{ fontSize: '0.72rem', color: '#f57c00', fontWeight: '500', marginTop: '2px' }} title={`Response lag from original event to task enqueued: ${lagStr}\nNotes: ${t.triggerNotes || t.triggerReason || ''}`}>
                                                                    ⏱️ {lagStr}
                                                                </div>
                                                            );
                                                        }
                                                    }
                                                    return null;
                                                })()}
                                            </td>
                                            <td style={{ padding: '12px 16px', textAlign: 'right' }}>
                                                {t.startedAt ? (
                                                    (() => {
                                                        const startMs = new Date(t.startedAt).getTime();
                                                        if (!isNaN(startMs)) {
                                                            const elapsedSec = Math.max(0, Math.floor((Date.now() - startMs) / 1000));
                                                            let durStr = '';
                                                            if (elapsedSec < 60) durStr = `${elapsedSec}s`;
                                                            else if (elapsedSec < 3600) durStr = `${Math.floor(elapsedSec / 60)}m ${elapsedSec % 60}s`;
                                                            else durStr = `${Math.floor(elapsedSec / 3600)}h ${Math.floor((elapsedSec % 3600) / 60)}m`;
                                                            return (
                                                                <span style={{ backgroundColor: 'rgba(2, 117, 216, 0.12)', color: '#0275d8', padding: '2px 8px', borderRadius: '4px', fontWeight: '600', fontSize: '0.75rem' }} title={`Task started at ${t.startedAt} (running for ${durStr})`}>
                                                                    ⏳ {durStr}
                                                                </span>
                                                            );
                                                        }
                                                        return '-';
                                                    })()
                                                ) : '-'}
                                            </td>
                                        </tr>
                                    ))}

                                    {/* Filtered Pending Tasks */}
                                    {(() => {
                                        const filtered = (queueData?.incoming || []).filter(t => {
                                            if (!queueFilter) return true;
                                            const q = queueFilter.toLowerCase();
                                            return (t.fileName || '').toLowerCase().includes(q) ||
                                                   (t.type || '').toLowerCase().includes(q) ||
                                                   (t.priority || '').toLowerCase().includes(q) ||
                                                   (t.assignee || '').toLowerCase().includes(q) ||
                                                   (t.enqueuedAt || '').toLowerCase().includes(q) ||
                                                   (t.triggerReason || '').toLowerCase().includes(q) ||
                                                   (t.triggerNotes || '').toLowerCase().includes(q) ||
                                                   (t.triggerEventTime || '').toLowerCase().includes(q) ||
                                                   (t.startedAt || '').toLowerCase().includes(q) ||
                                                   (t.completedAt || '').toLowerCase().includes(q) ||
                                                   String(t.number).includes(q) ||
                                                   (t.url || '').toLowerCase().includes(q);
                                        });

                                        if (filtered.length === 0) {
                                            return (
                                                <tr>
                                                    <td colSpan="8" style={{ padding: '30px', textAlign: 'center', color: 'var(--text-secondary)' }}>
                                                        {queueData ? 'No pending tasks match the current filter.' : 'Loading task queue...'}
                                                    </td>
                                                </tr>
                                            );
                                        }

                                        return filtered.map(t => (
                                            <tr key={t.fileName} style={{ borderBottom: '1px solid var(--border-color)' }}>
                                                <td style={{ padding: '12px 16px', fontWeight: 'bold', color: t.priority === 'critical' ? '#d93025' : 'var(--text-primary)' }}>
                                                    #{t.rank}
                                                </td>
                                                <td style={{ padding: '12px 16px' }}>
                                                    <span style={{ backgroundColor: 'var(--bg-secondary)', padding: '3px 8px', borderRadius: '4px', fontSize: '0.8rem', fontWeight: '600' }}>{t.type}</span>
                                                </td>
                                                <td style={{ padding: '12px 16px' }}>
                                                    {t.url ? (
                                                        <a href={t.url} target="_blank" rel="noopener noreferrer" style={{ color: 'var(--text-link)', fontWeight: '600' }}>
                                                            #{t.number > 0 ? t.number : t.fileName}
                                                        </a>
                                                    ) : t.fileName}
                                                </td>
                                                <td style={{ padding: '12px 16px' }}>
                                                    {t.priority === 'critical' ? (
                                                        <span style={{ backgroundColor: '#d93025', color: '#fff', padding: '2px 8px', borderRadius: '10px', fontSize: '0.75rem', fontWeight: 'bold' }}>⚡ CRITICAL</span>
                                                    ) : t.priority === 'high' || t.priority === 'urgent' ? (
                                                        <span style={{ backgroundColor: '#f57c00', color: '#fff', padding: '2px 8px', borderRadius: '10px', fontSize: '0.75rem', fontWeight: 'bold' }}>🟠 {(t.priority).toUpperCase()}</span>
                                                    ) : (
                                                        <span style={{ backgroundColor: '#0275d8', color: '#fff', padding: '2px 8px', borderRadius: '10px', fontSize: '0.75rem', fontWeight: 'bold' }}>{(t.priority || 'medium').toUpperCase()}</span>
                                                    )}
                                                </td>
                                                <td style={{ padding: '12px 16px', fontSize: '0.85rem' }}>{t.assignee || '-'}</td>
                                                <td style={{ padding: '12px 16px', fontSize: '0.85rem', color: 'var(--text-secondary)' }} title={t.triggerNotes || t.triggerReason || t.triggerEventTime || t.createdAt || ''}>
                                                    {t.triggerEventTime ? (
                                                        <div>
                                                            {formatQueueTimestamp(t.triggerEventTime)}
                                                            {t.triggerReason && (
                                                                <div style={{ fontSize: '0.75rem', marginTop: '2px', maxWidth: '220px', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }} title={t.triggerNotes || t.triggerReason}>
                                                                    <span style={{ backgroundColor: 'var(--bg-secondary)', color: 'var(--text-secondary)', padding: '1px 6px', borderRadius: '3px', fontWeight: '600', fontSize: '0.73rem' }}>
                                                                        {formatTriggerReason(t.triggerReason)}
                                                                    </span>
                                                                </div>
                                                            )}
                                                        </div>
                                                    ) : (
                                                        <div>{t.createdAt ? t.createdAt.split('T')[0] : '-'}</div>
                                                    )}
                                                </td>
                                                <td style={{ padding: '12px 16px', fontSize: '0.85rem', color: 'var(--text-secondary)' }} title={t.enqueuedAt || ''}>
                                                    {formatQueueTimestamp(t.enqueuedAt)}
                                                    {(() => {
                                                        if (t.triggerEventTime && t.enqueuedAt) {
                                                            const ev = new Date(t.triggerEventTime).getTime();
                                                            const enq = new Date(t.enqueuedAt).getTime();
                                                            if (!isNaN(ev) && !isNaN(enq) && enq >= ev) {
                                                                const diffSec = Math.floor((enq - ev) / 1000);
                                                                let lagStr = '';
                                                                if (diffSec < 60) lagStr = `${diffSec}s lag`;
                                                                else if (diffSec < 3600) lagStr = `${Math.floor(diffSec / 60)}m ${diffSec % 60}s lag`;
                                                                else lagStr = `${Math.floor(diffSec / 3600)}h ${Math.floor((diffSec % 3600) / 60)}m lag`;
                                                                return (
                                                                    <div style={{ fontSize: '0.72rem', color: '#f57c00', fontWeight: '500', marginTop: '2px' }} title={`Response lag from original event to task enqueued: ${lagStr}\nNotes: ${t.triggerNotes || t.triggerReason || ''}`}>
                                                                        ⏱️ {lagStr}
                                                                    </div>
                                                                );
                                                            }
                                                        }
                                                        return null;
                                                    })()}
                                                </td>
                                                <td style={{ padding: '12px 16px', textAlign: 'right' }}>
                                                    <button 
                                                        className={`btn btn-sm ${t.priority === 'critical' ? 'btn-secondary' : 'btn-danger'}`}
                                                        style={{ padding: '3px 10px', fontSize: '0.78rem', fontWeight: '600' }}
                                                        onClick={() => handleMakeCritical(t.fileName, t.priority)}
                                                    >
                                                        {t.priority === 'critical' ? 'Demote to Medium' : '⚡ Make Critical'}
                                                    </button>
                                                </td>
                                            </tr>
                                        ));
                                    })()}
                                </tbody>
                            </table>
                        </div>
                    </div>
                ) : showOverseerLogs ? (
                    <div className="logs-container" style={{ textAlign: 'left' }}>
                        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '12px' }}>
                            <div style={{ display: 'flex', alignItems: 'center', gap: '15px' }}>
                                <button 
                                    className="btn btn-sm" 
                                    style={{ backgroundColor: 'var(--bg-card)', border: '1px solid var(--border-color)', color: 'var(--text-primary)', fontWeight: '600', display: 'flex', alignItems: 'center', gap: '6px' }}
                                    onClick={() => setShowOverseerLogs(false)}
                                >
                                    ⬅ Back to Sandboxes Table
                                </button>
                                <h4 style={{ margin: 0 }}>Overseer Watch Daemon Logs</h4>
                            </div>
                            <span style={{ fontSize: '0.8rem', color: 'var(--text-muted)' }}>Live refreshing every 5s...</span>
                        </div>
                        <div className="logs-display" style={{ backgroundColor: '#1e1e1e', color: '#d4d4d4', padding: '18px', borderRadius: '6px', height: '620px', overflowY: 'auto', fontFamily: '"Consolas", "Monaco", "Courier New", monospace', fontSize: '13px', lineHeight: '1.5', boxShadow: 'inset 0 0 10px rgba(0,0,0,0.5)' }}>
                            <pre style={{ margin: 0, whiteSpace: 'pre-wrap' }}>
                                {logs || 'Loading daemon logs...'}
                            </pre>
                        </div>
                    </div>
                ) : activeOverseer ? (
                    <OverseerWork overseer={activeOverseer.metadata.name} />
                ) : (
                    <div style={{ textAlign: 'center', padding: '60px 20px', color: 'var(--text-muted)', backgroundColor: 'var(--bg-card)', borderRadius: '8px', border: '1px dashed var(--border-color)' }}>
                        <h3 style={{ margin: '0 0 10px 0', fontSize: '1.3rem', color: 'var(--text-secondary)' }}>Welcome to the Overseer Factory Control Center</h3>
                        <p style={{ margin: 0, fontSize: '0.95rem', maxWidth: '600px', display: 'inline-block', lineHeight: '1.6' }}>
                            Select an <strong>Overseer Repo</strong> from the sidebar on the left to inspect its active <strong>Factory Sandboxes</strong> (`pr-iterate-*`, `fix-*`, `adopt-*`, `agent-*`) and peek directly inside `/workspaces/tasks/*` for live task execution status and log streaming.
                        </p>
                    </div>
                )}
            </div>
        </div>
    );
};

export default Overseer;
