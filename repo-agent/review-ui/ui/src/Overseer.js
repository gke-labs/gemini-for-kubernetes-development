import React, { useState, useEffect, useCallback, useRef } from 'react';
import './App.css';
import SandboxTerminal from './Terminal';

export const formatQueueTimestamp = (ts) => {
    if (!ts) return '-';
    try {
        const d = new Date(ts);
        if (isNaN(d.getTime())) return ts;
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
        return (
            <div>
                <div>{exact}</div>
                {rel && <div style={{ fontSize: '0.75rem', color: 'var(--text-muted)', marginTop: '2px' }}>{`(${rel})`}</div>}
            </div>
        );
    } catch (e) {
        return ts;
    }
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

    const [showTaskQueue, setShowTaskQueue] = useState(false);
    const [queueData, setQueueData] = useState(null);
    const [queueFilter, setQueueFilter] = useState('');

    const [showStatus, setShowStatus] = useState(false);
    const [statusData, setStatusData] = useState(null);
    const [statusLoading, setStatusLoading] = useState(false);

    const logIntervalRef = useRef(null);

    const fetchQueue = useCallback(() => {
        if (!activeOverseer) return;
        fetch(`/api/overseers/${activeOverseer.metadata.name}/queue`)
            .then(res => {
                if (!res.ok) throw new Error("Failed to fetch task queue");
                return res.json();
            })
            .then(data => {
                if (data && data.isSyncing) {
                    setQueueData(prev => prev ? { ...prev, isSyncing: true } : data);
                } else {
                    setQueueData(data);
                }
            })
            .catch(err => {
                console.error("Failed to fetch task queue:", err);
                setQueueData(prev => prev ? { ...prev, isSyncing: true } : null);
            });
    }, [activeOverseer]);

    useEffect(() => {
        fetchQueue();
        const interval = setInterval(fetchQueue, 5000);
        return () => clearInterval(interval);
    }, [fetchQueue]);

    const fetchStatus = useCallback(() => {
        if (!activeOverseer) return;
        setStatusLoading(true);
        fetch(`/api/overseers/${activeOverseer.metadata.name}/status`)
            .then(res => {
                if (!res.ok) throw new Error("Failed to fetch status");
                return res.json();
            })
            .then(data => {
                setStatusData(data);
                setStatusLoading(false);
            })
            .catch(err => {
                console.error("Failed to fetch status:", err);
                setStatusLoading(false);
            });
    }, [activeOverseer]);

    useEffect(() => {
        if (showStatus) {
            fetchStatus();
            const interval = setInterval(fetchStatus, 5000);
            return () => clearInterval(interval);
        }
    }, [showStatus, fetchStatus]);

    useEffect(() => {
        setStatusData(null);
        setQueueData(null);
    }, [activeOverseer]);

    const handleMakeCritical = (fileName, currentPriority) => {
        if (!activeOverseer) return;
        const newPriority = currentPriority === 'critical' ? 'medium' : 'critical';
        fetch(`/api/overseers/${activeOverseer.metadata.name}/queue/${encodeURIComponent(fileName)}/priority`, {
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
        setShowStatus(false);
        setShowTaskQueue(false);
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

    const handleUnpauseSandbox = (sbName) => {
        if (!activeOverseer) return;
        fetch(`/api/overseers/${activeOverseer.metadata.name}/sandboxes/${sbName}/unpause`, { method: 'POST' })
            .then(res => {
                if (res.ok) {
                    fetchSandboxes();
                } else {
                    alert("Failed to unpause sandbox");
                }
            })
            .catch(err => console.error("Failed to unpause sandbox:", err));
    };

    const handlePauseSandbox = (sbName) => {
        if (!activeOverseer) return;
        fetch(`/api/overseers/${activeOverseer.metadata.name}/sandboxes/${sbName}/pause`, { method: 'POST' })
            .then(res => {
                if (res.ok) {
                    fetchSandboxes();
                } else {
                    alert("Failed to pause sandbox");
                }
            })
            .catch(err => console.error("Failed to pause sandbox:", err));
    };

    const toggleTaskLogs = (taskName) => {
        if (!activeOverseer || !activeSandbox) return;
        const current = taskLogs[taskName];
        if (current && current.show) {
            setTaskLogs(prev => ({ ...prev, [taskName]: { ...prev[taskName], show: false } }));
            return;
        }

        setTaskLogs(prev => ({ ...prev, [taskName]: { loading: true, show: true, content: 'Loading task execution logs...', telemetry: null } }));

        fetch(`/api/overseers/${activeOverseer.metadata.name}/sandboxes/${activeSandbox.metadata.name}/tasks/${encodeURIComponent(taskName)}/logs`)
            .then(res => res.text())
            .then(data => {
                setTaskLogs(prev => ({ ...prev, [taskName]: { ...prev[taskName], loading: false, show: true, content: data || 'No output log found.' } }));
            })
            .catch(err => {
                setTaskLogs(prev => ({ ...prev, [taskName]: { ...prev[taskName], loading: false, show: true, content: `Error loading log: ${err.message}` } }));
            });

        fetch(`/api/overseers/${activeOverseer.metadata.name}/sandboxes/${activeSandbox.metadata.name}/tasks/${encodeURIComponent(taskName)}/telemetry`)
            .then(res => res.json())
            .then(telemetry => {
                if (telemetry && telemetry.total_tool_calls > 0) {
                    setTaskLogs(prev => ({ ...prev, [taskName]: { ...prev[taskName], telemetry } }));
                }
            })
            .catch(() => {});
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

                {/* Sub-Tab Navigation Bar */}
                <div style={{ display: 'flex', gap: '10px', marginBottom: '20px', borderBottom: '1px solid var(--border-color)', paddingBottom: '12px' }}>
                    <button 
                        className={`btn ${!showTaskQueue && !showOverseerLogs && !showStatus && !activeSandbox ? 'btn-primary' : 'btn-secondary'}`}
                        onClick={() => { setShowTaskQueue(false); setShowOverseerLogs(false); setShowStatus(false); setActiveSandbox(null); }}
                        style={{ fontWeight: '600', padding: '8px 16px', display: 'inline-flex', alignItems: 'center', gap: '6px' }}
                    >
                        📦 Active Sandboxes ({sandboxes.length})
                    </button>
                    <button 
                        className={`btn ${showTaskQueue ? 'btn-primary' : 'btn-secondary'}`}
                        onClick={() => { setShowTaskQueue(true); setShowOverseerLogs(false); setShowStatus(false); setActiveSandbox(null); }}
                        style={{ fontWeight: '600', padding: '8px 16px', display: 'inline-flex', alignItems: 'center', gap: '6px' }}
                    >
                        📥 Incoming Task Queue ({queueData?.summary?.totalPending || 0})
                        {queueData?.summary?.byPriority?.critical > 0 && (
                            <span style={{ backgroundColor: '#d93025', color: '#fff', padding: '1px 7px', borderRadius: '10px', fontSize: '0.75rem', fontWeight: 'bold', marginLeft: '4px' }}>
                                {queueData.summary.byPriority.critical} Critical
                            </span>
                        )}
                    </button>
                    <button 
                        className={`btn ${showOverseerLogs ? 'btn-primary' : 'btn-secondary'}`}
                        onClick={() => { setShowOverseerLogs(true); setShowTaskQueue(false); setShowStatus(false); setActiveSandbox(null); }}
                        style={{ fontWeight: '600', padding: '8px 16px', display: 'inline-flex', alignItems: 'center', gap: '6px' }}
                    >
                        📜 Daemon Logs
                    </button>
                    <button 
                        className={`btn ${showStatus ? 'btn-primary' : 'btn-secondary'}`}
                        onClick={() => { setShowStatus(true); setShowTaskQueue(false); setShowOverseerLogs(false); setActiveSandbox(null); }}
                        style={{ fontWeight: '600', padding: '8px 16px', display: 'inline-flex', alignItems: 'center', gap: '6px' }}
                    >
                        📊 Status
                    </button>
                </div>

                {showStatus ? (
                    <div style={{ textAlign: 'left', backgroundColor: 'var(--bg-card)', padding: '24px', borderRadius: '8px', border: '1px solid var(--border-color)', boxShadow: 'var(--shadow-card)' }}>
                        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '20px' }}>
                            <h3 style={{ margin: 0, color: 'var(--text-primary)' }}>📊 Factory / API Token Status</h3>
                            <button 
                                className="btn btn-sm btn-secondary" 
                                onClick={fetchStatus}
                                style={{ display: 'flex', alignItems: 'center', gap: '6px' }}
                            >
                                🔄 Refresh Status
                            </button>
                        </div>

                        {statusLoading && !statusData ? (
                            <div style={{ padding: '20px', textAlign: 'center', color: 'var(--text-secondary)' }}>
                                🔄 Loading status data, please wait...
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

                                <div style={{ display: 'flex', gap: '10px', alignItems: 'center', flexWrap: 'wrap' }}>
                                    {(() => {
                                        const podInfo = getSandboxPodInfo(activeSandbox);
                                        const isOverseerDaemon = activeSandbox.metadata.name === `overseer-${activeOverseer?.metadata.name}`;
                                        if (isOverseerDaemon) return null;
                                        if (podInfo.isSuspended) {
                                            return (
                                                <button 
                                                    className="btn" 
                                                    style={{ backgroundColor: '#d4edda', color: '#155724', border: '1px solid #c3e6cb', fontWeight: '600' }}
                                                    onClick={() => handleUnpauseSandbox(activeSandbox.metadata.name)}
                                                    title="Unpause sandbox and keep it running for at least the idle timeout duration"
                                                >
                                                    ▶️ Unpause Sandbox
                                                </button>
                                            );
                                        } else if (!podInfo.isEvicted && !podInfo.isFailed) {
                                            return (
                                                <button 
                                                    className="btn" 
                                                    style={{ backgroundColor: '#fff3cd', color: '#856404', border: '1px solid #ffeeba', fontWeight: '600' }}
                                                    onClick={() => handlePauseSandbox(activeSandbox.metadata.name)}
                                                    title="Pause sandbox by scaling replicas down to 0"
                                                >
                                                    ⏸️ Pause Sandbox
                                                </button>
                                            );
                                        }
                                        return null;
                                    })()}
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
                                                        {logInfo.telemetry && logInfo.telemetry.total_tool_calls > 0 && (
                                                            <div style={{ marginBottom: '15px', padding: '12px', backgroundColor: '#0d1117', border: '1px solid #30363d', borderRadius: '6px' }}>
                                                                <div style={{ fontWeight: 'bold', color: '#58a6ff', marginBottom: '8px', fontSize: '13px', display: 'flex', alignItems: 'center', gap: '6px' }}>
                                                                    <span>⚡ Tool Execution Telemetry</span>
                                                                    <span style={{ fontWeight: 'normal', color: '#8b949e', fontSize: '12px' }}>({logInfo.telemetry.total_tool_calls} calls, {logInfo.telemetry.total_tool_duration_sec}s total)</span>
                                                                </div>
                                                                <div style={{ overflowX: 'auto' }}>
                                                                    <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: '12px', textAlign: 'left' }}>
                                                                        <thead>
                                                                            <tr style={{ borderBottom: '1px solid #21262d', color: '#8b949e' }}>
                                                                                <th style={{ padding: '6px 8px' }}>Tool</th>
                                                                                <th style={{ padding: '6px 8px' }}>Calls</th>
                                                                                <th style={{ padding: '6px 8px' }}>Total (s)</th>
                                                                                <th style={{ padding: '6px 8px' }}>Max (s)</th>
                                                                                <th style={{ padding: '6px 8px' }}>Slowest Command / Arg</th>
                                                                            </tr>
                                                                        </thead>
                                                                        <tbody>
                                                                            {Object.entries(logInfo.telemetry.tools || {}).map(([tname, tstat]) => (
                                                                                <tr key={tname} style={{ borderBottom: '1px solid #21262d' }}>
                                                                                    <td style={{ padding: '6px 8px', fontFamily: 'monospace', color: '#7ee787' }}>{tname}</td>
                                                                                    <td style={{ padding: '6px 8px' }}>{tstat.count}</td>
                                                                                    <td style={{ padding: '6px 8px' }}>{tstat.total_sec}</td>
                                                                                    <td style={{ padding: '6px 8px', color: tstat.max_sec > 60 ? '#ff7b72' : 'inherit', fontWeight: tstat.max_sec > 60 ? 'bold' : 'normal' }}>{tstat.max_sec}</td>
                                                                                    <td style={{ padding: '6px 8px', fontFamily: 'monospace', fontSize: '11px', color: '#8b949e', maxWidth: '300px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={tstat.slowest_cmd}>{tstat.slowest_cmd || '-'}</td>
                                                                                </tr>
                                                                            ))}
                                                                        </tbody>
                                                                    </table>
                                                                </div>
                                                                {logInfo.telemetry.shell_calls && logInfo.telemetry.shell_calls.length > 0 && (
                                                                    <div style={{ marginTop: '12px', paddingTop: '10px', borderTop: '1px solid #21262d' }}>
                                                                        <div style={{ fontWeight: 'bold', color: '#d2a8ff', marginBottom: '8px', fontSize: '12px', display: 'flex', alignItems: 'center', gap: '6px' }}>
                                                                            <span>🐚 Top {Math.min(10, logInfo.telemetry.shell_calls.length)} Slowest Shell Commands</span>
                                                                        </div>
                                                                        <div style={{ overflowX: 'auto' }}>
                                                                            <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: '11px', textAlign: 'left' }}>
                                                                                <thead>
                                                                                    <tr style={{ borderBottom: '1px solid #21262d', color: '#8b949e' }}>
                                                                                        <th style={{ padding: '4px 6px', width: '25px' }}>#</th>
                                                                                        <th style={{ padding: '4px 6px', width: '70px' }}>Duration</th>
                                                                                        <th style={{ padding: '4px 6px' }}>Command</th>
                                                                                    </tr>
                                                                                </thead>
                                                                                <tbody>
                                                                                    {logInfo.telemetry.shell_calls.slice(0, 10).map((call, idx) => (
                                                                                        <tr key={idx} style={{ borderBottom: '1px solid #161b22' }}>
                                                                                            <td style={{ padding: '4px 6px', color: '#8b949e' }}>{idx + 1}</td>
                                                                                            <td style={{ padding: '4px 6px', color: call.duration_sec > 60 ? '#ff7b72' : call.duration_sec > 15 ? '#ffa657' : '#7ee787', fontWeight: 'bold' }}>{call.duration_sec}s</td>
                                                                                            <td style={{ padding: '4px 6px', fontFamily: 'monospace', color: '#c9d1d9', wordBreak: 'break-all' }}>{call.cmd}</td>
                                                                                        </tr>
                                                                                    ))}
                                                                                </tbody>
                                                                            </table>
                                                                        </div>
                                                                    </div>
                                                                )}
                                                            </div>
                                                        )}
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
