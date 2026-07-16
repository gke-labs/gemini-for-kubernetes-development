import React, { useState, useEffect, useCallback, useRef } from 'react';
import './App.css';
import SandboxTerminal from './Terminal';



const Overseer = ({ onBack, namespace: userNamespace }) => {
    const [overseers, setOverseers] = useState([]);
    const [error, setError] = useState(null);
    const [activeOverseer, setActiveOverseer] = useState(null);
    const [sandboxes, setSandboxes] = useState([]);
    const [activeSandbox, setActiveSandbox] = useState(null);
    const [tasks, setTasks] = useState([]);
    const [searchFilter, setSearchFilter] = useState('');
    
    const [logs, setLogs] = useState('');
    const [showOverseerLogs, setShowOverseerLogs] = useState(false);
    const [showTerminal, setShowTerminal] = useState(false);
    const [showPodLogs, setShowPodLogs] = useState(false);
    const [taskLogs, setTaskLogs] = useState({});

    const logIntervalRef = useRef(null);

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
                setOverseers(data || []);
                setActiveOverseer(prev => {
                    if (data && data.length > 0 && !prev) {
                        return data[0];
                    } else if (prev) {
                        const updated = (data || []).find(o => o.metadata?.name === prev.metadata?.name);
                        return updated || prev;
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

    const fetchSandboxes = useCallback(() => {
        if (!activeOverseer) return;
        fetch(`/api/overseers/${activeOverseer.metadata.name}/sandboxes`)
            .then(res => {
                if (!res.ok) throw new Error("Failed to fetch sandboxes");
                return res.json();
            })
            .then(data => {
                setSandboxes(data || []);
                setActiveSandbox(prev => {
                    if (!prev) return null;
                    const updated = (data || []).find(s => s.metadata?.name === prev.metadata?.name);
                    return updated || prev;
                });
            })
            .catch(err => {
                console.error("Failed to fetch sandboxes:", err);
                setSandboxes([]);
            });
    }, [activeOverseer]);

    useEffect(() => {
        fetchSandboxes();
        const interval = setInterval(fetchSandboxes, 5000);
        return () => clearInterval(interval);
    }, [fetchSandboxes]);

    const fetchSandboxTasks = useCallback(() => {
        if (!activeOverseer || !activeSandbox) return;
        fetch(`/api/overseers/${activeOverseer.metadata.name}/sandboxes/${activeSandbox.metadata.name}/tasks`)
            .then(res => {
                if (!res.ok) throw new Error("Failed to fetch sandbox tasks");
                return res.json();
            })
            .then(data => {
                setTasks(data || []);
            })
            .catch(err => {
                console.error("Failed to fetch sandbox tasks:", err);
                setTasks([]);
            });
    }, [activeOverseer, activeSandbox]);

    useEffect(() => {
        if (activeSandbox) {
            fetchSandboxTasks();
            const interval = setInterval(fetchSandboxTasks, 5000);
            return () => clearInterval(interval);
        } else {
            setTasks([]);
        }
    }, [activeSandbox, fetchSandboxTasks]);

    const fetchLogs = useCallback(() => {
        if (!activeOverseer) return;
        let url = '';
        if (showOverseerLogs) {
            url = `/api/overseers/${activeOverseer.metadata.name}/logs`;
        } else if (activeSandbox && showPodLogs) {
            url = `/api/overseers/${activeOverseer.metadata.name}/sandboxes/${activeSandbox.metadata.name}/logs`;
        } else {
            return;
        }

        fetch(url)
            .then(res => res.text())
            .then(data => setLogs(data))
            .catch(err => console.error("Failed to fetch logs:", err));
    }, [activeOverseer, activeSandbox, showOverseerLogs, showPodLogs]);

    useEffect(() => {
        if (logIntervalRef.current) clearInterval(logIntervalRef.current);
        
        if (showOverseerLogs || (activeSandbox && showPodLogs)) {
            fetchLogs();
            logIntervalRef.current = setInterval(fetchLogs, 5000);
        } else {
            setLogs('');
        }

        return () => {
            if (logIntervalRef.current) clearInterval(logIntervalRef.current);
        };
    }, [showOverseerLogs, showPodLogs, activeSandbox, fetchLogs]);

    const handleOverseerClick = (ov) => {
        if (activeOverseer?.metadata.name === ov.metadata.name) {
            setActiveOverseer(null);
            setActiveSandbox(null);
            setShowOverseerLogs(false);
            setShowTerminal(false);
            setShowPodLogs(false);
        } else {
            setActiveOverseer(ov);
            setActiveSandbox(null);
            setShowOverseerLogs(true);
            setShowTerminal(false);
            setShowPodLogs(false);
        }
    };

    const handleSandboxClick = (sb) => {
        const isController = sb.metadata?.name === `overseer-${activeOverseer?.metadata.name}`;
        setActiveSandbox(sb);
        setShowOverseerLogs(false);
        setShowTerminal(false);
        setShowPodLogs(isController);
    };

    const handleOverseerDaemonClick = () => {
        if (!activeOverseer) return;
        const ovName = `overseer-${activeOverseer.metadata.name}`;
        const ovSandbox = sandboxes.find(sb => sb.metadata?.name === ovName) || {
            metadata: {
                name: ovName,
                creationTimestamp: activeOverseer.metadata?.creationTimestamp || new Date().toISOString(),
                labels: {
                    'sandbox.gemini.google.com/type': 'Controller / Daemon',
                    'sandbox-type': 'Controller / Daemon'
                }
            },
            spec: { replicas: 1 }
        };
        handleSandboxClick(ovSandbox);
    };

    const handleDeleteSandbox = (sbName) => {
        if (!activeOverseer) return;
        if (!window.confirm(`Are you sure you want to delete/evict sandbox '${sbName}'?`)) return;

        fetch(`/api/overseers/${activeOverseer.metadata.name}/sandboxes/${sbName}`, { method: 'DELETE' })
            .then(res => {
                if (res.ok) {
                    if (activeSandbox?.metadata?.name === sbName) {
                        setActiveSandbox(null);
                    }
                    fetchSandboxes();
                } else {
                    alert("Failed to delete sandbox");
                }
            })
            .catch(err => console.error("Failed to delete sandbox:", err));
    };

    const toggleTaskLogs = (taskName) => {
        if (!activeOverseer || !activeSandbox) return;
        const current = taskLogs[taskName];
        if (current && current.show) {
            setTaskLogs(prev => ({ ...prev, [taskName]: { ...prev[taskName], show: false } }));
            return;
        }

        setTaskLogs(prev => ({ ...prev, [taskName]: { loading: true, show: true, content: 'Loading task execution logs...' } }));

        fetch(`/api/overseers/${activeOverseer.metadata.name}/sandboxes/${activeSandbox.metadata.name}/tasks/${encodeURIComponent(taskName)}/logs`)
            .then(res => res.text())
            .then(data => {
                setTaskLogs(prev => ({ ...prev, [taskName]: { loading: false, show: true, content: data || 'No output log found.' } }));
            })
            .catch(err => {
                setTaskLogs(prev => ({ ...prev, [taskName]: { loading: false, show: true, content: `Error loading log: ${err.message}` } }));
            });
    };

    const getSandboxTypeIcon = (sb) => {
        const t = sb.metadata?.labels?.['sandbox.gemini.google.com/type'] || sb.metadata?.labels?.['sandbox-type'] || 'dev';
        if (t === 'fix') return '🐞';
        if (t === 'review') return '🔄';
        if (t === 'adopt') return '🚀';
        if (t === 'agent') return '⚡';
        if (t.includes('pr')) return '🔨';
        return '📦';
    };

    const getSandboxBadgeLabel = (sb) => {
        const t = sb.metadata?.labels?.['sandbox.gemini.google.com/type'] || sb.metadata?.labels?.['sandbox-type'] || '';
        const name = sb.metadata?.name || '';
        if (t === 'pr' || name.startsWith('factory-pr-') || name.startsWith('pr-')) return 'PR';
        if (t === 'fix' || name.startsWith('factory-fix-') || name.startsWith('fix-')) return 'Fix (Issue->PR)';
        if (t === 'review') return 'Review';
        if (t === 'adopt') return 'Adopt';
        return t || 'Factory';
    };

    const getStatusBadgeColor = (status) => {
        if (!status) return 'var(--status-grey)';
        const s = status.toLowerCase();
        if (s === 'completed' || s === 'ready' || s === 'success' || s === '0') return 'var(--status-green)';
        if (s === 'running' || s === 'provisioning') return 'var(--status-yellow)';
        if (s === 'failed' || s === 'crashed' || s === 'error') return 'var(--text-danger)';
        return 'var(--status-grey)';
    };

    const getSandboxPodInfo = (sb) => {
        if (!sb) return { status: 'Unknown', label: 'Unknown', badgeLabel: 'Unknown', color: 'var(--text-secondary)', bgColor: 'var(--bg-secondary)', isSuspended: false, isEvicted: false, isFailed: false, evictionCount: 0 };
        const replicas = sb.spec?.replicas;
        const conditions = sb.status?.conditions || [];
        const evictionCountStr = sb.metadata?.annotations?.['sandbox.gemini.google.com/eviction-count'] || '0';
        const evictionCount = parseInt(evictionCountStr, 10) || 0;
        const evictionSuffix = evictionCount > 0 ? ` (Evictions: ${evictionCount})` : '';

        let isSuspended = (replicas === 0 || replicas === '0');
        if (!isSuspended) {
            for (const c of conditions) {
                if (c.message && c.message.toLowerCase().includes('replicas is 0')) {
                    isSuspended = true;
                    break;
                }
            }
        }
        if (isSuspended) {
            return { status: 'scaled down', label: `Scaled Down (0)${evictionSuffix}`, badgeLabel: 'Scaled Down', color: '#856404', bgColor: '#fff3cd', isSuspended: true, isEvicted: false, isFailed: false, evictionCount };
        }

        let isEvicted = false;
        let isFailed = false;
        let failReason = 'Failed';
        for (const c of conditions) {
            const msg = (c.message || '').toLowerCase();
            const reason = (c.reason || '').toLowerCase();
            if (msg.includes('evicted') || reason === 'evicted') {
                isEvicted = true;
                failReason = 'Evicted';
                break;
            }
            if (msg.includes('phase: failed') || reason === 'podfailed') {
                isFailed = true;
            }
        }
        if (isEvicted || isFailed) {
            return {
                status: isEvicted ? 'evicted' : 'failed',
                label: `${isEvicted ? 'Evicted (1)' : 'Failed (1)'}${evictionSuffix}`,
                badgeLabel: failReason,
                color: '#d93025',
                bgColor: '#fce8e6',
                isSuspended: false,
                isEvicted: true,
                isFailed: true,
                evictionCount
            };
        }

        return { status: 'running', label: `Running (1)${evictionSuffix}`, badgeLabel: 'Active', color: 'var(--status-green)', bgColor: 'var(--bg-secondary)', isSuspended: false, isEvicted: false, isFailed: false, evictionCount };
    };

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

    const filterSandbox = (sb) => {
        if (!searchFilter || !searchFilter.trim()) return true;
        const q = searchFilter.trim().toLowerCase();
        const name = sb.metadata?.name || '';
        const typeBadge = getSandboxBadgeLabel(sb);
        const rawType = sb.metadata?.labels?.['sandbox.gemini.google.com/type'] || sb.metadata?.labels?.['sandbox-type'] || '';
        const podInfo = getSandboxPodInfo(sb);
        const status = podInfo.status;
        const issue = sb.metadata?.labels?.['factory.gemini.google.com/issue'] || sb.metadata?.labels?.issue || '';
        const pr = sb.metadata?.labels?.['factory.gemini.google.com/pr'] || sb.metadata?.annotations?.pr || '';
        const user = sb.metadata?.labels?.['factory.gemini.google.com/user'] || '';
        const desc = sb.metadata?.annotations?.['sandbox.gemini.google.com/description'] || '';
        const tType = sb.metadata?.annotations?.['sandbox.gemini.google.com/last-task-type'] || '';
        const tState = sb.metadata?.annotations?.['sandbox.gemini.google.com/last-task-state'] || '';
        const url = sb.metadata?.annotations?.['sandbox.gemini.google.com/html-url'] || sb.metadata?.annotations?.htmlURL || '';
        return name.toLowerCase().includes(q) ||
               typeBadge.toLowerCase().includes(q) ||
               rawType.toLowerCase().includes(q) ||
               status.toLowerCase().includes(q) ||
               String(issue).toLowerCase().includes(q) ||
               String(pr).toLowerCase().includes(q) ||
               user.toLowerCase().includes(q) ||
               desc.toLowerCase().includes(q) ||
               tType.toLowerCase().includes(q) ||
               tState.toLowerCase().includes(q) ||
               url.toLowerCase().includes(q);
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

                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '25px', borderBottom: '1px solid var(--border-color)', paddingBottom: '15px', flexWrap: 'wrap', gap: '15px' }}>
                    <div>
                        <div style={{ display: 'flex', alignItems: 'center', gap: '12px' }}>
                            <span style={{ fontSize: '1.4rem', fontWeight: 'bold', color: 'var(--text-primary)' }}>
                                📂 Overseer Repo:
                            </span>
                            {overseers.length > 1 ? (
                                <select 
                                    value={activeOverseer?.metadata.name || ''} 
                                    onChange={(e) => {
                                        const selected = overseers.find(o => o.metadata.name === e.target.value);
                                        if (selected) handleOverseerClick(selected);
                                    }}
                                    style={{
                                        padding: '6px 14px',
                                        borderRadius: '6px',
                                        border: '1px solid var(--border-color)',
                                        backgroundColor: 'var(--bg-card)',
                                        color: 'var(--text-primary)',
                                        fontSize: '1.1rem',
                                        fontWeight: 'bold'
                                    }}
                                >
                                    {overseers.map(o => (
                                        <option key={o.metadata.name} value={o.metadata.name}>
                                            {o.metadata.name}{isOverseerInUpgradeMode(o) ? ' (UPGRADING)' : ''}
                                        </option>
                                    ))}
                                </select>
                            ) : (
                                <span style={{ fontSize: '1.4rem', fontWeight: 'bold', color: 'var(--text-active)' }}>
                                    {activeOverseer?.metadata.name || 'Loading...'}
                                </span>
                            )}
                            {isOverseerInUpgradeMode(activeOverseer) && (
                                <span style={{
                                    padding: '4px 12px',
                                    borderRadius: '16px',
                                    fontSize: '0.78rem',
                                    fontWeight: 'bold',
                                    backgroundColor: '#fff3cd',
                                    color: '#856404',
                                    border: '1px solid #ffeeba',
                                    display: 'inline-flex',
                                    alignItems: 'center',
                                    gap: '6px'
                                }} title="Overseer is in upgrade mode and not accepting new tasks">
                                    🚧 UPGRADING (DO NOT PROCESS)
                                </span>
                            )}
                        </div>
                        {activeOverseer && (
                            <p style={{ margin: '6px 0 0 0', fontSize: '0.85rem', color: 'var(--text-secondary)' }}>
                                Repo: <a href={activeOverseer.spec?.repoURL} target="_blank" rel="noopener noreferrer" style={{ color: 'var(--text-link)', fontWeight: '500' }}>{activeOverseer.spec?.repoURL}</a>
                                {activeOverseer.spec?.pollInterval && ` • Poll: ${activeOverseer.spec?.pollInterval}`}
                                {activeOverseer.spec?.robotAccount && ` • Bot: ${activeOverseer.spec?.robotAccount}`}
                            </p>
                        )}
                    </div>
                    <button className="btn" onClick={onBack} style={{ padding: '8px 16px', fontWeight: '500' }}>Back to Dashboard</button>
                </div>

                {showOverseerLogs ? (
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
                ) : activeSandbox ? (
                    <div>
                        <div style={{ marginBottom: '15px' }}>
                            <button 
                                className="btn btn-sm" 
                                style={{ backgroundColor: 'var(--bg-card)', border: '1px solid var(--border-color)', color: 'var(--text-primary)', fontWeight: '600', display: 'flex', alignItems: 'center', gap: '6px' }}
                                onClick={() => setActiveSandbox(null)}
                            >
                                ⬅ Back to Sandboxes Table
                            </button>
                        </div>

                        {/* Sandbox Header Box */}
                        <div style={{ backgroundColor: 'var(--bg-card)', padding: '20px', borderRadius: '8px', border: '1px solid var(--border-color)', marginBottom: '25px', boxShadow: 'var(--shadow-card)' }}>
                            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', flexWrap: 'wrap', gap: '15px' }}>
                                <div>
                                    <div style={{ display: 'flex', alignItems: 'center', gap: '10px', marginBottom: '8px' }}>
                                        <span style={{ fontSize: '1.2rem', fontWeight: 'bold', color: 'var(--text-primary)' }}>{activeSandbox.metadata.name}</span>
                                        <span style={{ 
                                            padding: '3px 10px', 
                                            borderRadius: '12px', 
                                            fontSize: '0.75rem', 
                                            fontWeight: 'bold', 
                                            backgroundColor: 'var(--bg-secondary)', 
                                            color: 'var(--text-primary)',
                                            textTransform: 'uppercase'
                                        }}>
                                            {getSandboxBadgeLabel(activeSandbox)}
                                        </span>
                                        {(() => {
                                            const podInfo = getSandboxPodInfo(activeSandbox);
                                            const badges = [];
                                            if (podInfo.evictionCount > 0) {
                                                badges.push(
                                                    <span key="eviction-badge" style={{ 
                                                        padding: '3px 10px', 
                                                        borderRadius: '12px', 
                                                        fontSize: '0.75rem', 
                                                        fontWeight: 'bold', 
                                                        backgroundColor: '#fff3cd', 
                                                        color: '#856404', 
                                                        border: '1px solid #ffeeba'
                                                    }} title="Total number of times this sandbox's pod has been evicted and automatically cleaned up/recreated.">
                                                        🔄 Evictions: {podInfo.evictionCount}
                                                    </span>
                                                );
                                            }
                                            if (podInfo.isSuspended) {
                                                badges.push(
                                                    <span key="status-badge" style={{ 
                                                        padding: '3px 10px', 
                                                        borderRadius: '12px', 
                                                        fontSize: '0.75rem', 
                                                        fontWeight: 'bold', 
                                                        backgroundColor: podInfo.bgColor, 
                                                        color: podInfo.color, 
                                                        border: '1px solid #ffeeba'
                                                    }}>
                                                        ⏸️ Scaled Down (Replicas: 0)
                                                    </span>
                                                );
                                            } else if (podInfo.isEvicted || podInfo.isFailed) {
                                                badges.push(
                                                    <span key="status-badge" style={{ 
                                                        padding: '3px 10px', 
                                                        borderRadius: '12px', 
                                                        fontSize: '0.75rem', 
                                                        fontWeight: 'bold', 
                                                        backgroundColor: podInfo.bgColor, 
                                                        color: podInfo.color, 
                                                        border: '1px solid #f8d7da'
                                                    }}>
                                                        ⚠️ {podInfo.label}
                                                    </span>
                                                );
                                            }
                                            return badges;
                                        })()}
                                    </div>

                                    <div style={{ fontSize: '0.85rem', color: 'var(--text-secondary)', display: 'flex', flexWrap: 'wrap', gap: '15px' }}>
                                        <span><strong>Created:</strong> {new Date(activeSandbox.metadata.creationTimestamp).toLocaleString()}</span>
                                        {activeSandbox.metadata?.labels?.['factory.gemini.google.com/user'] && (
                                            <span><strong>User:</strong> {activeSandbox.metadata.labels['factory.gemini.google.com/user']}</span>
                                        )}
                                        {activeSandbox.metadata?.annotations?.pr && (
                                            <span><strong>PR:</strong> #{activeSandbox.metadata.annotations.pr}</span>
                                        )}
                                        {activeSandbox.metadata?.annotations?.htmlURL && (
                                            <span>
                                                <a href={activeSandbox.metadata.annotations.htmlURL} target="_blank" rel="noopener noreferrer" style={{ color: 'var(--text-link)', fontWeight: '600' }}>
                                                    View PR/Issue on GitHub ↗
                                                </a>
                                            </span>
                                        )}
                                    </div>
                                </div>

                                <div style={{ display: 'flex', gap: '10px', alignItems: 'center' }}>
                                    <button 
                                        className="btn" 
                                        style={{ 
                                            backgroundColor: showTerminal ? 'var(--bg-active)' : 'var(--bg-secondary)', 
                                            color: showTerminal ? 'white' : 'var(--text-primary)',
                                            border: '1px solid var(--border-color)',
                                            fontWeight: '600'
                                        }}
                                        onClick={() => { setShowTerminal(!showTerminal); setShowPodLogs(false); }}
                                    >
                                        &gt;_ Live Terminal
                                    </button>

                                    <button 
                                        className="btn" 
                                        style={{ 
                                            backgroundColor: showPodLogs ? 'var(--bg-active)' : 'var(--bg-secondary)', 
                                            color: showPodLogs ? 'white' : 'var(--text-primary)',
                                            border: '1px solid var(--border-color)',
                                            fontWeight: '600'
                                        }}
                                        onClick={() => { setShowPodLogs(!showPodLogs); setShowTerminal(false); }}
                                    >
                                        📑 Pod Logs
                                    </button>

                                    {activeSandbox.metadata.name !== `overseer-${activeOverseer?.metadata.name}` && (
                                        <button 
                                            className="btn" 
                                            style={{ backgroundColor: 'var(--bg-danger-light)', color: 'var(--text-danger)', border: '1px solid var(--border-danger)', fontWeight: '600' }}
                                            onClick={() => handleDeleteSandbox(activeSandbox.metadata.name)}
                                        >
                                            🗑️ Evict / Delete
                                        </button>
                                    )}
                                </div>
                            </div>
                        </div>

                        {/* Interactive SSH Terminal View */}
                        {showTerminal && (
                            <div className="terminal-container" style={{ marginBottom: '25px', borderRadius: '8px', overflow: 'hidden', border: '1px solid var(--border-color)', boxShadow: 'var(--shadow-card)' }}>
                                <div style={{ backgroundColor: '#21262d', color: '#c9d1d9', padding: '10px 15px', fontSize: '0.85rem', fontWeight: 'bold', borderBottom: '1px solid #30363d' }}>
                                    Interactive SSH Terminal (`/workspaces`)
                                </div>
                                <SandboxTerminal 
                                    namespace={`overseer-${activeOverseer?.metadata?.name}`} 
                                    sandboxName={activeSandbox.metadata.name} 
                                />
                            </div>
                        )}

                        {/* Pod Logs View */}
                        {showPodLogs && (
                            <div className="logs-container" style={{ textAlign: 'left', marginBottom: '25px' }}>
                                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '10px' }}>
                                    <h4 style={{ margin: 0 }}>Full Sandbox Pod Logs</h4>
                                    <span style={{ fontSize: '0.8rem', color: 'var(--text-muted)' }}>Live refreshing...</span>
                                </div>
                                <div className="logs-display" style={{ backgroundColor: '#1e1e1e', color: '#d4d4d4', padding: '15px', borderRadius: '6px', height: '450px', overflowY: 'auto', fontFamily: 'monospace', fontSize: '13px' }}>
                                    <pre style={{ margin: 0, whiteSpace: 'pre-wrap' }}>
                                        {logs || 'Loading pod logs...'}
                                    </pre>
                                </div>
                            </div>
                        )}

                        {/* Tasks Section */}
                        <div style={{ backgroundColor: 'var(--bg-card)', padding: '20px', borderRadius: '8px', border: '1px solid var(--border-color)', boxShadow: 'var(--shadow-card)' }}>
                            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '15px' }}>
                                <h3 style={{ margin: 0, fontSize: '1.2rem', color: 'var(--text-primary)' }}>
                                    Executed Tasks (`/workspaces/tasks/*`)
                                </h3>
                                <span style={{ fontSize: '0.8rem', color: 'var(--text-muted)' }}>
                                    {tasks.length} task(s) detected inside sandbox
                                </span>
                            </div>

                            {tasks.length === 0 ? (
                                <div style={{ padding: '30px', textAlign: 'center', color: 'var(--text-muted)', backgroundColor: 'var(--bg-review-section)', borderRadius: '6px', border: '1px dashed var(--border-color)' }}>
                                    <p style={{ margin: 0, fontSize: '0.95rem' }}>No tasks found in <code>/workspaces/tasks/</code> for this sandbox yet.</p>
                                </div>
                            ) : (
                                <div style={{ display: 'flex', flexDirection: 'column', gap: '12px' }}>
                                    {tasks.map((task) => {
                                        const taskName = task.metadata?.name || 'unknown-task';
                                        const taskType = task.spec?.taskType || task.spec?.type || taskName;
                                        const state = task.status?.state || task.status?.taskState || 'Pending';
                                        const exitCode = task.status?.exitCode;
                                        const badgeColor = getStatusBadgeColor(state);
                                        const logInfo = taskLogs[taskName];

                                        return (
                                            <div key={taskName} style={{ border: '1px solid var(--border-color)', borderRadius: '6px', backgroundColor: 'var(--bg-review-section)', overflow: 'hidden' }}>
                                                <div style={{ padding: '14px 18px', display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: '10px' }}>
                                                    <div>
                                                        <span style={{ fontWeight: 'bold', fontSize: '1rem', color: 'var(--text-primary)' }}>{taskType}</span>
                                                        <span style={{ fontSize: '0.8rem', color: 'var(--text-muted)', marginLeft: '12px', fontFamily: 'monospace' }}>
                                                            {taskName}
                                                        </span>
                                                    </div>

                                                    <div style={{ display: 'flex', alignItems: 'center', gap: '12px' }}>
                                                        {exitCode !== undefined && exitCode !== null && (
                                                            <span style={{ fontSize: '0.8rem', color: 'var(--text-secondary)', fontFamily: 'monospace' }}>
                                                                Exit Code: <strong>{exitCode}</strong>
                                                            </span>
                                                        )}

                                                        <span style={{ 
                                                            backgroundColor: badgeColor, 
                                                            color: state.toLowerCase() === 'running' || state.toLowerCase() === 'provisioning' ? 'black' : 'white', 
                                                            padding: '4px 10px', 
                                                            borderRadius: '12px', 
                                                            fontSize: '0.75rem', 
                                                            fontWeight: 'bold',
                                                            textTransform: 'uppercase'
                                                        }}>
                                                            {state}
                                                        </span>

                                                        <button 
                                                            className="btn btn-sm" 
                                                            style={{ 
                                                                backgroundColor: logInfo?.show ? 'var(--bg-active)' : 'var(--bg-card)', 
                                                                color: logInfo?.show ? 'white' : 'var(--text-primary)',
                                                                border: '1px solid var(--border-color)',
                                                                fontSize: '0.8rem',
                                                                padding: '5px 12px'
                                                            }}
                                                            onClick={() => toggleTaskLogs(taskName)}
                                                        >
                                                            {logInfo?.show ? 'Hide Logs' : 'View Execution Log'}
                                                        </button>
                                                    </div>
                                                </div>

                                                {logInfo?.show && (
                                                    <div style={{ borderTop: '1px solid var(--border-color)', padding: '15px', backgroundColor: '#161b22', color: '#c9d1d9', textAlign: 'left' }}>
                                                        <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: '8px', fontSize: '0.8rem', color: '#8b949e', textAlign: 'left' }}>
                                                            <span>Output (`/workspaces/tasks/{taskName}/execution.log`)</span>
                                                        </div>
                                                        <pre style={{ margin: 0, whiteSpace: 'pre-wrap', fontFamily: 'monospace', fontSize: '12px', maxHeight: '400px', overflowY: 'auto', lineHeight: '1.5', textAlign: 'left' }}>
                                                            {logInfo.loading ? 'Loading log...' : logInfo.content}
                                                        </pre>
                                                    </div>
                                                )}
                                            </div>
                                        );
                                    })}
                                </div>
                            )}
                        </div>
                    </div>
                ) : activeOverseer ? (() => {
                    const overseerName = `overseer-${activeOverseer.metadata.name}`;
                    const filtered = sandboxes.filter(filterSandbox);
                    const runningSandboxes = filtered.filter(sb => {
                        const info = getSandboxPodInfo(sb);
                        return !info.isSuspended && !info.isEvicted && !info.isFailed;
                    });
                    const evictedSandboxes = filtered.filter(sb => {
                        const info = getSandboxPodInfo(sb);
                        return !info.isSuspended && (info.isEvicted || info.isFailed);
                    });
                    const suspendedSandboxes = filtered.filter(sb => {
                        const info = getSandboxPodInfo(sb);
                        return info.isSuspended;
                    });

                    const renderTableRow = (sb) => {
                        const podInfo = getSandboxPodInfo(sb);
                        const isSuspended = podInfo.isSuspended;
                        const isEvicted = podInfo.isEvicted || podInfo.isFailed;
                        const name = sb.metadata?.name || '';
                        const typeBadge = getSandboxBadgeLabel(sb);
                        const icon = getSandboxTypeIcon(sb);
                        const assignedBot = sb.metadata?.labels?.['factory.gemini.google.com/user'] || '-';
                        
                        let lastTaskStr = '-';
                        if (sb.metadata?.annotations) {
                            const tType = sb.metadata.annotations['sandbox.gemini.google.com/last-task-type'];
                            const tState = sb.metadata.annotations['sandbox.gemini.google.com/last-task-state'];
                            if (tType && tState) lastTaskStr = `${tType} (${tState})`;
                            else if (tType) lastTaskStr = tType;
                        }

                        let htmlURL = '-';
                        if (sb.metadata?.annotations) {
                            const u = sb.metadata.annotations['sandbox.gemini.google.com/html-url'] || sb.metadata.annotations.htmlURL;
                            if (u) {
                                htmlURL = (
                                    <a href={u} target="_blank" rel="noopener noreferrer" onClick={(e) => e.stopPropagation()} style={{ color: 'var(--text-link)', fontWeight: '600', textDecoration: 'underline' }}>
                                        View ↗
                                    </a>
                                );
                            }
                        }

                        const diffMs = Date.now() - new Date(sb.metadata.creationTimestamp).getTime();
                        const hours = Math.floor(diffMs / 3600000);
                        const days = Math.floor(hours / 24);
                        const ageStr = days > 0 ? `${days}d ${hours % 24}h` : `${hours}h`;

                        return (
                            <tr 
                                key={name}
                                onClick={() => handleSandboxClick(sb)}
                                style={{ 
                                    cursor: 'pointer', 
                                    borderBottom: '1px solid var(--border-color)',
                                    backgroundColor: isEvicted ? 'rgba(217, 48, 37, 0.04)' : (isSuspended ? 'rgba(255, 243, 205, 0.04)' : 'transparent'),
                                    opacity: isSuspended ? 0.75 : 1
                                }}
                                className="table-row-hover"
                            >
                                <td style={{ padding: '12px 16px', fontWeight: 'bold', color: 'var(--text-primary)', display: 'flex', alignItems: 'center', gap: '8px' }}>
                                    <span>{isEvicted ? '⚠️' : (isSuspended ? '⏸️' : icon)}</span>
                                    <span style={{ textDecoration: isSuspended ? 'line-through' : 'none' }}>{name}</span>
                                </td>
                                <td style={{ padding: '12px 16px' }}>
                                    <span style={{ fontSize: '0.75rem', padding: '3px 8px', borderRadius: '10px', backgroundColor: podInfo.bgColor, color: podInfo.color, fontWeight: (isSuspended || isEvicted) ? 'bold' : 'normal', whiteSpace: 'nowrap' }}>
                                        {isSuspended ? `Scaled Down • ${typeBadge}` : (isEvicted ? `${podInfo.badgeLabel} • ${typeBadge}` : typeBadge)}
                                    </span>
                                </td>
                                <td style={{ padding: '12px 16px', fontSize: '0.85rem' }}>
                                    <span style={{ color: podInfo.color, fontWeight: '600' }}>
                                        {podInfo.label}
                                    </span>
                                </td>
                                <td style={{ padding: '12px 16px', fontSize: '0.85rem', fontFamily: 'monospace', color: 'var(--text-secondary)' }}>
                                    {assignedBot}
                                </td>
                                <td style={{ padding: '12px 16px', fontSize: '0.85rem', color: 'var(--text-secondary)' }}>
                                    {lastTaskStr}
                                </td>
                                <td style={{ padding: '12px 16px', fontSize: '0.85rem' }}>
                                    {htmlURL}
                                </td>
                                <td style={{ padding: '12px 16px', fontSize: '0.85rem', color: 'var(--text-muted)' }}>
                                    {ageStr}
                                </td>
                            </tr>
                        );
                    };

                    return (
                        <div style={{ backgroundColor: 'var(--bg-card)', borderRadius: '8px', border: '1px solid var(--border-color)', boxShadow: 'var(--shadow-card)', overflow: 'hidden' }}>
                            <div style={{ padding: '20px', borderBottom: '1px solid var(--border-color)', display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: '15px' }}>
                                <div>
                                    <h3 style={{ margin: 0, fontSize: '1.3rem', color: 'var(--text-primary)', display: 'flex', alignItems: 'center', gap: '10px' }}>
                                        <span>📂 Sandboxes Table: <strong>{activeOverseer.metadata.name}</strong></span>
                                    </h3>
                                    <p style={{ margin: '5px 0 0 0', fontSize: '0.85rem', color: 'var(--text-muted)' }}>
                                        Click any row to drill down into the sandbox detail view, live terminal, and task logs.
                                    </p>
                                </div>
                                <div style={{ display: 'flex', gap: '10px', alignItems: 'center' }}>
                                    <input 
                                        type="text"
                                        placeholder="🔍 Filter Name, Type, Status, Bot, Task..."
                                        title="Searches across all columns: Name, Type, Status, Assigned Bot, Last Task, and PR/Issue URL/Number"
                                        value={searchFilter}
                                        onChange={(e) => setSearchFilter(e.target.value)}
                                        style={{
                                            padding: '8px 12px',
                                            borderRadius: '6px',
                                            border: '1px solid var(--border-color)',
                                            backgroundColor: 'var(--bg-secondary)',
                                            color: 'var(--text-primary)',
                                            fontSize: '0.85rem',
                                            width: '310px'
                                        }}
                                    />
                                    <button 
                                        className="btn btn-sm"
                                        style={{ backgroundColor: 'var(--bg-active)', color: 'white', fontWeight: '600' }}
                                        onClick={handleOverseerDaemonClick}
                                    >
                                        🤖 Overseer Daemon Log
                                    </button>
                                </div>
                            </div>

                            <div style={{ overflowX: 'auto' }}>
                                <table style={{ width: '100%', borderCollapse: 'collapse', textAlign: 'left' }}>
                                    <thead>
                                        <tr style={{ backgroundColor: 'var(--bg-sidebar)', borderBottom: '2px solid var(--border-color)', color: 'var(--text-muted)', fontSize: '0.75rem', textTransform: 'uppercase' }}>
                                            <th style={{ padding: '12px 16px' }}>NAME</th>
                                            <th style={{ padding: '12px 16px' }}>TYPE</th>
                                            <th style={{ padding: '12px 16px' }}>STATUS</th>
                                            <th style={{ padding: '12px 16px' }}>ASSIGNED BOT</th>
                                            <th style={{ padding: '12px 16px' }}>LAST TASK</th>
                                            <th style={{ padding: '12px 16px' }}>PR/ISSUE URL</th>
                                            <th style={{ padding: '12px 16px' }}>AGE</th>
                                        </tr>
                                    </thead>
                                    <tbody>
                                        {/* Special Overseer Daemon Row right on top */}
                                        <tr 
                                            onClick={handleOverseerDaemonClick}
                                            style={{ cursor: 'pointer', borderBottom: '2px solid var(--border-color)', backgroundColor: 'rgba(56, 139, 253, 0.08)' }}
                                            className="table-row-hover"
                                        >
                                            <td style={{ padding: '12px 16px', fontWeight: 'bold', color: 'var(--text-active)', display: 'flex', alignItems: 'center', gap: '8px' }}>
                                                <span>🤖</span>
                                                <span>overseer-{activeOverseer.metadata.name} (Controller)</span>
                                            </td>
                                            <td style={{ padding: '12px 16px' }}>
                                                <span style={{ fontSize: '0.75rem', padding: '3px 8px', borderRadius: '10px', backgroundColor: 'var(--bg-active)', color: 'white', fontWeight: 'bold' }}>
                                                    Controller / Daemon
                                                </span>
                                            </td>
                                            <td style={{ padding: '12px 16px', fontSize: '0.85rem' }}>
                                                <span style={{ color: 'var(--status-green)', fontWeight: '600' }}>Running</span>
                                            </td>
                                            <td style={{ padding: '12px 16px', fontSize: '0.85rem', fontFamily: 'monospace', color: 'var(--text-secondary)' }}>
                                                overseer-controller
                                            </td>
                                            <td style={{ padding: '12px 16px', fontSize: '0.85rem', color: 'var(--text-secondary)' }}>
                                                Daemon Loop
                                            </td>
                                            <td style={{ padding: '12px 16px', fontSize: '0.85rem' }}>
                                                -
                                            </td>
                                            <td style={{ padding: '12px 16px', fontSize: '0.85rem', color: 'var(--text-muted)' }}>
                                                Active
                                            </td>
                                        </tr>

                                        {runningSandboxes.map(sb => renderTableRow(sb))}

                                        {evictedSandboxes.length > 0 && (
                                            <>
                                                <tr style={{ backgroundColor: 'var(--bg-sidebar)', borderTop: '2px solid var(--border-color)' }}>
                                                    <td colSpan={7} style={{ padding: '10px 16px', fontSize: '0.75rem', fontWeight: 'bold', color: '#d93025', textTransform: 'uppercase' }}>
                                                        ⚠️ Evicted / Failed Sandboxes ({evictedSandboxes.length})
                                                    </td>
                                                </tr>
                                                {evictedSandboxes.map(sb => renderTableRow(sb))}
                                            </>
                                        )}

                                        {suspendedSandboxes.length > 0 && (
                                            <>
                                                <tr style={{ backgroundColor: 'var(--bg-sidebar)', borderTop: '2px solid var(--border-color)' }}>
                                                    <td colSpan={7} style={{ padding: '10px 16px', fontSize: '0.75rem', fontWeight: 'bold', color: '#856404', textTransform: 'uppercase' }}>
                                                        ⏸️ Scaled Down Sandboxes ({suspendedSandboxes.length})
                                                    </td>
                                                </tr>
                                                {suspendedSandboxes.map(sb => renderTableRow(sb))}
                                            </>
                                        )}

                                        {runningSandboxes.length === 0 && evictedSandboxes.length === 0 && suspendedSandboxes.length === 0 && (
                                            <tr>
                                                <td colSpan={7} style={{ padding: '40px', textAlign: 'center', color: 'var(--text-muted)', fontStyle: 'italic' }}>
                                                    No sandboxes match your current filter or repo.
                                                </td>
                                            </tr>
                                        )}
                                    </tbody>
                                </table>
                            </div>
                        </div>
                    );
                })() : (
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
